package server

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestEngagementBundle covers the handover download: one zip with the summary, the tasks of the engagement's hosts,
// the audit rows of its window, the agents and the plugin set.
//
// The failure it replaces is a manual one - four downloads and a spreadsheet, with the audit part the step that gets
// skipped, which is exactly the part an after-action reviewer needs.
func TestEngagementBundle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	engagement := db.Engagement{
		TenantID: 1, Name: "DMZ exercise", StartsAt: now.Add(-3 * time.Hour), EndsAt: now.Add(-time.Hour),
	}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now, EngagementID: &engagement.ID,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	// One task inside the window and one outside it: only the inside one belongs in the handover.
	if err := s.db.Create(&db.Task{
		AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed",
		Result: "inside-window", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	// A failed task inside the window, so failures.json has something to group.
	if err := s.db.Create(&db.Task{
		AgentID: "a-1", TenantID: 1, Type: "ps", Command: "ps", Status: "failed",
		Error: "context deadline exceeded", CreatedAt: now.Add(-110 * time.Minute), UpdatedAt: now.Add(-110 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed failed task: %v", err)
	}
	// A credential with a real secret in it: the bundle must describe it without carrying the secret. The vault
	// refuses a plaintext store, so the fixture initialises loot encryption the same way production does.
	crypto.InitLootEncryption("0000000000000000000000000000000000000000000000000000000000000000")
	if err := s.db.Create(&db.CredentialEntry{
		TenantID: 1, AgentID: "a-1", Domain: "CORP", Username: "svc-backup", Password: "Sup3rSecret!",
		Hash: "8846f7eaee8fb117ad06bdd830b7586c", Type: "cleartext", Source: "lsass",
		CreatedAt: now.Add(-100 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	if err := s.db.Create(&db.Task{
		AgentID: "a-1", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed",
		Result: "before-window", CreatedAt: now.Add(-10 * time.Hour), UpdatedAt: now.Add(-10 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed old task: %v", err)
	}
	// One audit row inside the window, one outside.
	if err := s.db.Create(&db.AuditLog{
		Action: "task_create", User: "op", TenantID: 1, Success: true,
		CreatedAt: now.Add(-90 * time.Minute), PrevHash: "p", EntryHash: "e",
	}).Error; err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	if err := s.db.Create(&db.AuditLog{
		Action: "login", User: "op", TenantID: 1, Success: true,
		CreatedAt: now.Add(-10 * time.Hour), PrevHash: "p", EntryHash: "e",
	}).Error; err != nil {
		t.Fatalf("seed old audit: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/1/bundle", nil)
	c.Params = gin.Params{{Key: "id", Value: itoaUint(engagement.ID)}}
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleEngagementBundle(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/zip") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasSuffix(cd, `.zip"`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	// The transport-level headers let a receiver check the archive and learn whether it was signed WITHOUT unzipping.
	archiveSum := sha256.Sum256(w.Body.Bytes())
	if got := w.Header().Get("X-Bundle-SHA256"); got != hex.EncodeToString(archiveSum[:]) {
		t.Fatalf("X-Bundle-SHA256 = %q, want the digest of the body", got)
	}
	if got := w.Header().Get("X-Bundle-Members"); got != "10" {
		t.Fatalf("X-Bundle-Members = %q, want 10", got)
	}
	if got := w.Header().Get("X-Bundle-Signed"); got != "false" {
		t.Fatalf("X-Bundle-Signed = %q, want false without a configured key", got)
	}
	if got := w.Header().Get("X-Bundle-Public-Key"); got != "" {
		t.Fatalf("an unsigned bundle advertised a key: %q", got)
	}

	// The archive must parse, and every promised member must be present.
	reader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("the body is not a zip: %v", err)
	}
	members := map[string][]byte{}
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		content, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		members[file.Name] = content
	}
	for _, name := range []string{"summary.md", "timeline.md", "tasks.csv", "audit.ndjson", "agents.json", "credentials.json", "iocs.json", "failures.json", "plugins.json", "MANIFEST.json"} {
		if _, ok := members[name]; !ok {
			t.Fatalf("the bundle is missing %s (has %v)", name, keysOf(members))
		}
	}

	// summary.md is the same document the summary endpoint renders.
	if !strings.Contains(string(members["summary.md"]), "# Engagement: DMZ exercise") {
		t.Fatalf("summary.md = %q", string(members["summary.md"]))
	}

	// timeline.md is the same two records as a chronological narrative, in one document.
	timeline := string(members["timeline.md"])
	if !strings.Contains(timeline, "# Timeline: DMZ exercise") {
		t.Fatalf("timeline.md = %q", timeline)
	}
	// The task line shows the command and outcome, not the result text: the timeline is a narrative, and the
	// result belongs in tasks.csv where it is not flattened into one cell.
	for _, want := range []string{"## What went wrong", "| Time (UTC) | Kind | Detail |", "whoami", "task_create"} {
		if !strings.Contains(timeline, want) {
			t.Fatalf("timeline.md is missing %q:\n%s", want, timeline)
		}
	}
	// The out-of-window rows must not appear in the narrative either.
	if strings.Contains(timeline, "before-window") || strings.Contains(timeline, "| login |") {
		t.Fatalf("timeline.md contains out-of-window records:\n%s", timeline)
	}
	// Chronological: the audit row is at -90m and the task at -2h, so the task line comes first.
	taskIndex := strings.Index(timeline, "whoami")
	auditIndex := strings.Index(timeline, "task_create")
	if taskIndex < 0 || auditIndex < 0 || taskIndex > auditIndex {
		t.Fatalf("timeline is not ordered by time (task at %d, audit at %d):\n%s", taskIndex, auditIndex, timeline)
	}

	// tasks.csv holds the in-window task only.
	records, err := csv.NewReader(bytes.NewReader(members["tasks.csv"])).ReadAll()
	if err != nil {
		t.Fatalf("tasks.csv: %v", err)
	}
	// Header plus the two in-window tasks: the completed one and the failed one seeded for failures.json.
	if len(records) != 3 {
		t.Fatalf("tasks.csv rows = %d, want header + 2 (the in-window tasks)", len(records))
	}
	joinedRows := ""
	for _, row := range records[1:] {
		joinedRows += strings.Join(row, ",") + "\n"
	}
	if !strings.Contains(joinedRows, "inside-window") {
		t.Fatalf("tasks.csv rows = %v, want the completed in-window task", records[1:])
	}

	// audit.ndjson holds the in-window row with both hashes, so it can be verified offline.
	auditLines := strings.Split(strings.TrimSpace(string(members["audit.ndjson"])), "\n")
	if len(auditLines) != 1 {
		t.Fatalf("audit.ndjson lines = %d, want 1", len(auditLines))
	}
	var auditRow auditChainExportRow
	if err := json.Unmarshal([]byte(auditLines[0]), &auditRow); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if auditRow.Action != "task_create" || auditRow.EntryHash == "" || auditRow.PrevHash == "" {
		t.Fatalf("audit row = %+v, want the in-window action with both hashes", auditRow)
	}

	// plugins.json is the same document the index endpoint returns.
	var plugins []pluginIndexEntry
	if err := json.Unmarshal(members["plugins.json"], &plugins); err != nil {
		t.Fatalf("plugins.json: %v", err)
	}

	// credentials.json lists what was found WITHOUT the secret material, and says where the secrets live.
	credentialsBody := string(members["credentials.json"])
	if !strings.Contains(credentialsBody, "svc-backup") || !strings.Contains(credentialsBody, "metadata only") {
		t.Fatalf("credentials.json = %q", credentialsBody)
	}
	for _, secret := range []string{"Sup3rSecret!", "8846f7eaee8fb117ad06bdd830b7586c"} {
		if strings.Contains(credentialsBody, secret) {
			t.Fatalf("the bundle carries secret material (%q): %s", secret, credentialsBody)
		}
	}

	// failures.json groups the window's failures by shape, with the reason codes and the hosts affected - the same
	// normalisation the live signature view uses, so the reviewer can compare the package with what the console said.
	var failures struct {
		FailedTasks int `json:"failed_tasks"`
		Shapes      []struct {
			Signature string   `json:"signature"`
			Count     int      `json:"count"`
			Agents    int      `json:"agents_affected"`
			Reasons   []string `json:"reasons"`
		} `json:"shapes"`
	}
	if err := json.Unmarshal(members["failures.json"], &failures); err != nil {
		t.Fatalf("failures.json: %v (%s)", err, string(members["failures.json"]))
	}
	if failures.FailedTasks == 0 || len(failures.Shapes) == 0 {
		t.Fatalf("failures.json is empty: %s", string(members["failures.json"]))
	}
	if !containsFold(failures.Shapes[0].Signature, "deadline") {
		t.Fatalf("the shape does not describe the failure: %+v", failures.Shapes[0])
	}
	if failures.Shapes[0].Agents != 1 || len(failures.Shapes[0].Reasons) == 0 {
		t.Fatalf("the shape carries no host count or reason: %+v", failures.Shapes[0])
	}

	// iocs.json parses and says which bound it used, because its bound is looser than the other members.
	var iocs struct {
		Count      int    `json:"count"`
		WindowDays int    `json:"window_days"`
		Note       string `json:"note"`
	}
	if err := json.Unmarshal(members["iocs.json"], &iocs); err != nil {
		t.Fatalf("iocs.json: %v (%s)", err, string(members["iocs.json"]))
	}
	if iocs.WindowDays < 1 {
		t.Fatalf("iocs window_days = %d", iocs.WindowDays)
	}
	if !strings.Contains(iocs.Note, "day count") {
		t.Fatalf("iocs.json does not state its bound: %q", iocs.Note)
	}

	// MANIFEST.json describes exactly the members that are present - both directions, so a member cannot be added
	// without appearing in the manifest, nor listed without existing.
	var manifest struct {
		Engagement struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"engagement"`
		GeneratedAt   string `json:"generated_at"`
		ServerVersion string `json:"server_version"`
		Members       []struct {
			Name        string `json:"name"`
			Purpose     string `json:"purpose"`
			Source      string `json:"source"`
			Sensitivity string `json:"sensitivity"`
			SHA256      string `json:"sha256"`
			Bytes       int    `json:"bytes"`
		} `json:"members"`
	}
	if err := json.Unmarshal(members["MANIFEST.json"], &manifest); err != nil {
		t.Fatalf("MANIFEST.json: %v (%s)", err, string(members["MANIFEST.json"]))
	}
	if manifest.ServerVersion == "" {
		t.Fatal("MANIFEST.json does not say which release produced it")
	}
	if manifest.Engagement.ID != engagement.ID || manifest.Engagement.Name != "DMZ exercise" || manifest.GeneratedAt == "" {
		t.Fatalf("manifest header = %+v", manifest.Engagement)
	}
	listed := map[string]bool{}
	for _, entry := range manifest.Members {
		listed[entry.Name] = true
		if entry.Purpose == "" || entry.Source == "" || entry.Sensitivity == "" {
			t.Fatalf("manifest entry %q is incomplete: %+v", entry.Name, entry)
		}
		if entry.Name == "credentials.json" && !strings.Contains(entry.Sensitivity, "metadata only") {
			t.Fatalf("credentials.json is not described as metadata only: %+v", entry)
		}
		if entry.Name == "audit.ndjson" && !strings.Contains(entry.Sensitivity, "verifiable") {
			t.Fatalf("audit.ndjson is not described as verifiable: %+v", entry)
		}
	}
	// Every member except the manifest carries the digest of the bytes that were written, and that digest is the
	// digest of what the receiver actually unzips - that is the whole point of publishing it.
	for _, entry := range manifest.Members {
		content, present := members[entry.Name]
		if !present {
			continue // the reverse check below reports it
		}
		if entry.Name == "MANIFEST.json" {
			if entry.SHA256 != "" {
				t.Fatalf("MANIFEST.json claims a digest of itself: %q", entry.SHA256)
			}
			continue
		}
		if entry.SHA256 == "" || entry.Bytes == 0 {
			t.Fatalf("%s has no digest in the manifest: %+v", entry.Name, entry)
		}
		if entry.Bytes != len(content) {
			t.Fatalf("%s byte count = %d, the archive holds %d", entry.Name, entry.Bytes, len(content))
		}
		sum := sha256.Sum256(content)
		if got := hex.EncodeToString(sum[:]); got != entry.SHA256 {
			t.Fatalf("%s digest = %q, manifest says %q", entry.Name, got, entry.SHA256)
		}
	}
	for name := range members {
		if !listed[name] {
			t.Fatalf("%s is in the archive but not in MANIFEST.json (%v)", name, listed)
		}
	}
	for name := range listed {
		if _, ok := members[name]; !ok {
			t.Fatalf("MANIFEST.json lists %s, which is not in the archive", name)
		}
	}

	// The handover is audited.
	var audited int64
	if err := s.db.Model(&db.AuditLog{}).Where("action = ?", "engagement_bundle").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 1 {
		t.Fatalf("engagement_bundle rows = %d, want 1", audited)
	}

	// Another tenant cannot bundle this engagement.
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/1/bundle", nil)
	c2.Params = gin.Params{{Key: "id", Value: itoaUint(engagement.ID)}}
	c2.Set("user", "op")
	c2.Set("user_role", db.RoleAdmin)
	c2.Set("user_id", uint(1))
	c2.Set(middleware.TenantIDContextKey, uint(2))
	s.handleEngagementBundle(c2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant bundle = %d, want 404", w2.Code)
	}
}

// keysOf lists map keys for a failure message.
func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}

// TestEngagementBundleVerify covers the receiving side: an uploaded bundle is checked against its own manifest.
//
// The property worth pinning is that it detects damage while being honest about what it cannot detect: the digests are
// unkeyed and the manifest travels inside the archive, so a wholesale rebuild verifies.
func TestEngagementBundleVerify(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	engagement := db.Engagement{TenantID: 1, Name: "DMZ exercise", StartsAt: now.Add(-2 * time.Hour), EndsAt: now.Add(-time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}
	if err := s.db.Create(&db.Implant{
		ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online", LastSeen: now, EngagementID: &engagement.ID,
	}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// Produce a real bundle through the handler, so the fixture is never a hand-built zip.
	buildRecorder := httptest.NewRecorder()
	buildCtx, _ := gin.CreateTestContext(buildRecorder)
	buildCtx.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/1/bundle", nil)
	buildCtx.Params = gin.Params{{Key: "id", Value: itoaUint(engagement.ID)}}
	buildCtx.Set("user", "op")
	buildCtx.Set("user_role", db.RoleAdmin)
	buildCtx.Set("user_id", uint(1))
	buildCtx.Set(middleware.TenantIDContextKey, uint(1))
	s.handleEngagementBundle(buildCtx)
	if buildRecorder.Code != http.StatusOK {
		t.Fatalf("building the bundle failed: %d (%s)", buildRecorder.Code, buildRecorder.Body.String())
	}
	archiveBytes := buildRecorder.Body.Bytes()

	verify := func(body []byte) (int, map[string]interface{}) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/engagements/1/bundle/verify", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/zip")
		c.Params = gin.Params{{Key: "id", Value: itoaUint(engagement.ID)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleVerifyEngagementBundle(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}

	// 1. The bundle verifies against its own manifest.
	code, data := verify(archiveBytes)
	if code != http.StatusOK {
		t.Fatalf("verify status = %d", code)
	}
	if data["ok"] != true {
		t.Fatalf("a freshly built bundle did not verify: %v", data)
	}
	checked, _ := data["checked"].(float64)
	verified, _ := data["verified"].(float64)
	if checked == 0 || verified != checked-1 { // every member but MANIFEST.json
		t.Fatalf("checked = %v, verified = %v", data["checked"], data["verified"])
	}
	// It says what it cannot prove.
	if note, _ := data["note"].(string); !strings.Contains(note, "unkeyed") {
		t.Fatalf("the response does not state the limitation: %q", note)
	}

	// 2. A single flipped byte inside a member is detected.
	corrupted := corruptZipMember(t, archiveBytes, "tasks.csv")
	code, data = verify(corrupted)
	if code != http.StatusOK {
		t.Fatalf("verify status = %d", code)
	}
	if data["ok"] != false {
		t.Fatalf("a corrupted bundle verified: %v", data)
	}
	problems, _ := data["mismatched"].([]interface{})
	if len(problems) == 0 {
		t.Fatalf("no problem was reported for the corrupted member: %v", data)
	}

	// 3. A zip without a manifest cannot be verified against anything.
	code, _ = verify(makeZip(t, map[string]string{"notes.txt": "hello"}))
	if code != http.StatusBadRequest {
		t.Fatalf("a zip without MANIFEST.json = %d, want 400", code)
	}

	// 4. A body that is not a zip is refused rather than parsed.
	if code, _ := verify([]byte("not a zip at all")); code != http.StatusBadRequest {
		t.Fatalf("a non-zip body = %d, want 400", code)
	}
}

// corruptZipMember rewrites one member with different bytes, leaving the rest of the archive intact.
func corruptZipMember(t *testing.T, raw []byte, victim string) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, file := range reader.File {
		content := []byte("replaced")
		if file.Name != victim {
			handle, err := file.Open()
			if err != nil {
				t.Fatalf("open %s: %v", file.Name, err)
			}
			content, err = io.ReadAll(handle)
			_ = handle.Close()
			if err != nil {
				t.Fatalf("read %s: %v", file.Name, err)
			}
		}
		out, err := writer.Create(file.Name)
		if err != nil {
			t.Fatalf("create %s: %v", file.Name, err)
		}
		if _, err := out.Write(content); err != nil {
			t.Fatalf("write %s: %v", file.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return buf.Bytes()
}

// makeZip builds a small archive for the negative cases.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range files {
		out, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := out.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// TestEngagementBundleSelection covers `?include=`: a customer-facing handover must not carry the credential inventory
// merely because the operator can see it.
func TestEngagementBundleSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	now := time.Now().Truncate(time.Second)
	engagement := db.Engagement{TenantID: 1, Name: "DMZ exercise", StartsAt: now.Add(-2 * time.Hour), EndsAt: now.Add(-time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}

	build := func(query string) (int, map[string][]byte) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/1/bundle"+query, nil)
		c.Params = gin.Params{{Key: "id", Value: itoaUint(engagement.ID)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleEngagementBundle(c)
		if w.Code != http.StatusOK {
			return w.Code, nil
		}
		reader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if err != nil {
			t.Fatalf("not a zip: %v", err)
		}
		members := map[string][]byte{}
		for _, file := range reader.File {
			handle, err := file.Open()
			if err != nil {
				t.Fatalf("open %s: %v", file.Name, err)
			}
			content, err := io.ReadAll(handle)
			_ = handle.Close()
			if err != nil {
				t.Fatalf("read %s: %v", file.Name, err)
			}
			members[file.Name] = content
		}
		return w.Code, members
	}

	// 1. A selection writes exactly those members plus the manifest.
	code, members := build("?include=summary.md,audit.ndjson")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(members) != 3 {
		t.Fatalf("members = %v, want summary.md, audit.ndjson and MANIFEST.json", keysOf(members))
	}
	for _, name := range []string{"summary.md", "audit.ndjson", "MANIFEST.json"} {
		if _, ok := members[name]; !ok {
			t.Fatalf("%s is missing: %v", name, keysOf(members))
		}
	}
	if _, ok := members["credentials.json"]; ok {
		t.Fatal("the credential inventory was included in a selection that did not ask for it")
	}

	// 2. The manifest describes the selection, not the full set - that is what makes the archive verifiable.
	var manifest struct {
		Members []struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		} `json:"members"`
	}
	if err := json.Unmarshal(members["MANIFEST.json"], &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(manifest.Members) != 3 {
		t.Fatalf("manifest lists %d member(s), want 3: %+v", len(manifest.Members), manifest.Members)
	}
	for _, entry := range manifest.Members {
		if _, ok := members[entry.Name]; !ok {
			t.Fatalf("the manifest lists %s, which is not in the archive", entry.Name)
		}
		if entry.Name != "MANIFEST.json" && entry.SHA256 == "" {
			t.Fatalf("%s has no digest", entry.Name)
		}
	}

	// 3. An unknown member is refused and the message lists the valid ones.
	code, _ = build("?include=summary.md,secrets.txt")
	if code != http.StatusBadRequest {
		t.Fatalf("unknown member = %d, want 400", code)
	}

	// 4. Naming only the manifest still produces a verifiable package (the manifest always travels).
	code, members = build("?include=manifest.json")
	if code != http.StatusOK || len(members) != 1 {
		t.Fatalf("manifest-only bundle = %d %v", code, keysOf(members))
	}
}

// TestEngagementBundleSignature covers signing and the distinction the verify response makes between "the manifest is
// internally consistent" and "signed by the key I expected".
//
// A public key carried inside the archive proves nothing about origin: swap the manifest and the key together and
// everything verifies. So `trusted` requires the caller to state the key they already trust, and the test pins both
// halves - valid-but-untrusted, and trusted when the expected key is supplied.
func TestEngagementBundleSignature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	seed := "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	t.Setenv(bundleSigningKeyEnv, seed)
	signingKey := ed25519.NewKeyFromSeed(mustHexSeed(t, seed))
	expectedKey := hex.EncodeToString(signingKey.Public().(ed25519.PublicKey))

	now := time.Now().Truncate(time.Second)
	engagement := db.Engagement{TenantID: 1, Name: "DMZ exercise", StartsAt: now.Add(-2 * time.Hour), EndsAt: now.Add(-time.Hour)}
	if err := s.db.Create(&engagement).Error; err != nil {
		t.Fatalf("seed engagement: %v", err)
	}

	var buildHeaders http.Header
	buildBundle := func() []byte {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/engagements/1/bundle?include=summary.md", nil)
		c.Params = gin.Params{{Key: "id", Value: itoaUint(engagement.ID)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleEngagementBundle(c)
		if w.Code != http.StatusOK {
			t.Fatalf("bundle status = %d (%s)", w.Code, w.Body.String())
		}
		buildHeaders = w.Header()
		return w.Body.Bytes()
	}
	verify := func(body []byte, query string) map[string]interface{} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/engagements/1/bundle/verify"+query, bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/zip")
		c.Params = gin.Params{{Key: "id", Value: itoaUint(engagement.ID)}}
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uint(1))
		c.Set(middleware.TenantIDContextKey, uint(1))
		s.handleVerifyEngagementBundle(c)
		var resp struct {
			Data map[string]interface{} `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.Data
	}

	bundle := buildBundle()

	// The response announces the signature, so a script does not have to unzip to learn about it.
	if got := buildHeaders.Get("X-Bundle-Signed"); got != "true" {
		t.Fatalf("a signed bundle did not say so in a header: %q", got)
	}
	if got := buildHeaders.Get("X-Bundle-Signature-Algorithm"); got != "ed25519" {
		t.Fatalf("X-Bundle-Signature-Algorithm = %q", got)
	}
	if got := buildHeaders.Get("X-Bundle-Public-Key"); got != expectedKey {
		t.Fatalf("X-Bundle-Public-Key = %q, want %q", got, expectedKey)
	}

	// 1. Signed, internally consistent, but NOT trusted without an expected key.
	data := verify(bundle, "")
	signature, _ := data["signature"].(map[string]interface{})
	if signature["present"] != true || signature["valid"] != true {
		t.Fatalf("signature = %v, want present and valid", signature)
	}
	if signature["trusted"] != false {
		t.Fatalf("a self-carried key was reported as trusted: %v", signature)
	}
	if data["ok"] != true {
		t.Fatalf("a correctly signed bundle did not verify: %v", data)
	}

	// 2. Trusted once the caller names the key they already hold.
	data = verify(bundle, "?public_key="+expectedKey)
	signature, _ = data["signature"].(map[string]interface{})
	if signature["trusted"] != true {
		t.Fatalf("the expected key did not make the bundle trusted: %v", signature)
	}

	// 3. A wrong expected key is refused as untrusted, with the reason stated.
	data = verify(bundle, "?public_key="+strings.Repeat("00", 32))
	signature, _ = data["signature"].(map[string]interface{})
	if signature["trusted"] != false || !strings.Contains(fmt.Sprint(signature["problem"]), "different key") {
		t.Fatalf("a wrong expected key = %v", signature)
	}

	// 4. Editing a digest in the manifest after signing breaks the signature even though nothing else changed.
	edited := editManifestDigest(t, bundle)
	data = verify(edited, "")
	signature, _ = data["signature"].(map[string]interface{})
	if signature["valid"] != false {
		t.Fatalf("an edited manifest still verified: %v", signature)
	}

	// 5. Without a configured key the bundle is unsigned and says so, rather than pretending.
	t.Setenv(bundleSigningKeyEnv, "")
	unsigned := buildBundle()
	data = verify(unsigned, "")
	signature, _ = data["signature"].(map[string]interface{})
	if signature["present"] != false {
		t.Fatalf("an unsigned bundle claims a signature: %v", signature)
	}
	if problem := fmt.Sprint(signature["problem"]); !strings.Contains(problem, "unsigned") {
		t.Fatalf("the unsigned case is not explained: %q", problem)
	}
}

// mustHexSeed decodes a hex Ed25519 seed.
func mustHexSeed(t *testing.T, seed string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(seed)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return raw
}

// editManifestDigest rewrites the first member digest inside MANIFEST.json without re-signing.
func editManifestDigest(t *testing.T, raw []byte) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		content, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		if file.Name == "MANIFEST.json" {
			var parsed map[string]interface{}
			if err := json.Unmarshal(content, &parsed); err != nil {
				t.Fatalf("manifest: %v", err)
			}
			members, _ := parsed["members"].([]interface{})
			for _, entry := range members {
				member, _ := entry.(map[string]interface{})
				if member["sha256"] != nil && member["sha256"] != "" {
					member["sha256"] = strings.Repeat("ab", 32)
					break
				}
			}
			content, err = json.MarshalIndent(parsed, "", "  ")
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}
		}
		out, err := writer.Create(file.Name)
		if err != nil {
			t.Fatalf("create %s: %v", file.Name, err)
		}
		if _, err := out.Write(content); err != nil {
			t.Fatalf("write %s: %v", file.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}
