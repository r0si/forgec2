package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func agentCtx(method, target, body, contentType string, uid uint, role string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType != "" {
		c.Request.Header.Set("Content-Type", contentType)
	}
	c.Set("user", "admin1")
	c.Set("user_role", role)
	c.Set("user_id", uid)
	c.Params = params
	return c, w
}

func seedAgentRow(t *testing.T, s *Server, id string, tenant uint, notes string) db.Implant {
	t.Helper()
	a := db.Implant{ID: id, TenantID: tenant, Hostname: id + "-host", Status: "online", Notes: notes, LastSeen: time.Now()}
	if err := s.db.Create(&a).Error; err != nil {
		t.Fatalf("seed agent %s: %v", id, err)
	}
	return a
}

// TestUpdateAgentNoteTenantGate pins the IDOR fix: notes and tags are written
// through a tenant-scoped query, so another tenant's agent answers 404 and is
// left untouched.
func TestUpdateAgentNoteTenantGate(t *testing.T) {
	s := mustTenantServer(t)
	seedAIUser(t, s, "admin1", 1)
	seedAgentRow(t, s, "own-agent", 1, "mine")
	seedAgentRow(t, s, "foreign-agent", 2, "theirs")

	// Cross-tenant write: 404, nothing changed.
	c, w := agentCtx(http.MethodPost, "/api/agents/foreign-agent/note", `{"notes":"pwned","tags":"pwned"}`,
		"application/json", 1, db.RoleAdmin, gin.Params{{Key: "id", Value: "foreign-agent"}})
	s.handleUpdateNote(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign note = %d %s", w.Code, w.Body.String())
	}
	var foreign db.Implant
	if err := s.db.Where("id = ?", "foreign-agent").First(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	if foreign.Notes != "theirs" || foreign.Tags != "" {
		t.Fatalf("foreign agent was modified: notes=%q tags=%q", foreign.Notes, foreign.Tags)
	}

	// Own agent, JSON body.
	c, w = agentCtx(http.MethodPost, "/api/agents/own-agent/note", `{"notes":"updated","tags":"red"}`,
		"application/json", 1, db.RoleAdmin, gin.Params{{Key: "id", Value: "own-agent"}})
	s.handleUpdateNote(c)
	if w.Code != http.StatusOK {
		t.Fatalf("own note = %d %s", w.Code, w.Body.String())
	}
	var own db.Implant
	if err := s.db.Where("id = ?", "own-agent").First(&own).Error; err != nil {
		t.Fatal(err)
	}
	if own.Notes != "updated" || own.Tags != "red" {
		t.Fatalf("own agent not updated: notes=%q tags=%q", own.Notes, own.Tags)
	}

	// Form body path.
	form := url.Values{"notes": {"form-note"}}
	c, w = agentCtx(http.MethodPost, "/api/agents/own-agent/note", form.Encode(),
		"application/x-www-form-urlencoded", 1, db.RoleAdmin, gin.Params{{Key: "id", Value: "own-agent"}})
	s.handleUpdateNote(c)
	if w.Code != http.StatusOK {
		t.Fatalf("form note = %d %s", w.Code, w.Body.String())
	}
	if err := s.db.Where("id = ?", "own-agent").First(&own).Error; err != nil {
		t.Fatal(err)
	}
	if own.Notes != "form-note" {
		t.Fatalf("form note not applied: %q", own.Notes)
	}

	// Oversized notes are rejected before any write.
	long := strings.Repeat("x", MaxNotesLength+1)
	c, w = agentCtx(http.MethodPost, "/api/agents/own-agent/note", `{"notes":"`+long+`"}`,
		"application/json", 1, db.RoleAdmin, gin.Params{{Key: "id", Value: "own-agent"}})
	s.handleUpdateNote(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("long note = %d", w.Code)
	}

	// No fields at all is a no-op success.
	c, w = agentCtx(http.MethodPost, "/api/agents/own-agent/note", `{}`,
		"application/json", 1, db.RoleAdmin, gin.Params{{Key: "id", Value: "own-agent"}})
	s.handleUpdateNote(c)
	if w.Code != http.StatusOK {
		t.Fatalf("empty note = %d %s", w.Code, w.Body.String())
	}
}

// TestDeleteAgentTenantGateAndCascade covers the ownership gate and that the
// dependent rows go with the agent.
func TestDeleteAgentTenantGateAndCascade(t *testing.T) {
	s := mustTenantServer(t)
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	seedAIUser(t, s, "admin1", 1)
	seedAgentRow(t, s, "doomed", 1, "")
	seedAgentRow(t, s, "survivor", 2, "")

	task := db.Task{AgentID: "doomed", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed"}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	cred := db.CredentialEntry{AgentID: "doomed", TenantID: 1, Username: "u", Password: "p", Type: "cleartext"}
	if err := s.db.Create(&cred).Error; err != nil {
		t.Fatal(err)
	}

	// Cross-tenant delete: 404, both rows survive.
	c, w := agentCtx(http.MethodDelete, "/api/agents/survivor", "", "", 1, db.RoleAdmin,
		gin.Params{{Key: "id", Value: "survivor"}})
	s.handleDeleteAgent(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign delete = %d %s", w.Code, w.Body.String())
	}
	var agents int64
	s.db.Model(&db.Implant{}).Where("id = ?", "survivor").Count(&agents)
	if agents != 1 {
		t.Fatal("foreign agent was deleted")
	}

	// Unknown agent: 404.
	c, w = agentCtx(http.MethodDelete, "/api/agents/nope", "", "", 1, db.RoleAdmin,
		gin.Params{{Key: "id", Value: "nope"}})
	s.handleDeleteAgent(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown delete = %d", w.Code)
	}

	// Own agent: deleted together with its tasks and credentials.
	c, w = agentCtx(http.MethodDelete, "/api/agents/doomed", "", "", 1, db.RoleAdmin,
		gin.Params{{Key: "id", Value: "doomed"}})
	s.handleDeleteAgent(c)
	if w.Code != http.StatusOK {
		t.Fatalf("own delete = %d %s", w.Code, w.Body.String())
	}
	s.db.Model(&db.Implant{}).Where("id = ?", "doomed").Count(&agents)
	if agents != 0 {
		t.Fatal("agent row survived delete")
	}
	var tasks, creds int64
	s.db.Model(&db.Task{}).Where("agent_id = ?", "doomed").Count(&tasks)
	s.db.Model(&db.CredentialEntry{}).Where("agent_id = ?", "doomed").Count(&creds)
	if tasks != 0 || creds != 0 {
		t.Fatalf("dependents survived: tasks=%d credentials=%d", tasks, creds)
	}
}

// TestAgentSortOrderAndHostBuckets covers the list ordering helpers.
func TestAgentSortOrderAndHostBuckets(t *testing.T) {
	// Unknown keys fall back to last_seen desc; a non-"desc" direction is asc.
	for _, in := range []string{"", "nonsense"} {
		if got := agentSortOrder(in, "desc"); got != "last_seen desc" {
			t.Fatalf("agentSortOrder(%q) = %q, want the default", in, got)
		}
	}
	if got := agentSortOrder("hostname", ""); !strings.HasSuffix(got, " asc") {
		t.Fatalf("agentSortOrder(hostname, empty) = %q, want asc", got)
	}
	if got := agentSortOrder("hostname", "desc"); !strings.HasSuffix(got, " desc") {
		t.Fatalf("agentSortOrder(hostname, desc) = %q", got)
	}
	// An unknown direction is treated as asc rather than injected verbatim.
	if got := agentSortOrder("hostname", "desc; DROP TABLE implants"); !strings.HasSuffix(got, " asc") {
		t.Fatalf("agentSortOrder with a hostile direction = %q", got)
	}

	now := time.Now()
	buckets := []*implantHostBucket{
		{key: "b", sortStr: "b", sortAt: now.Add(-2 * time.Hour)},
		{key: "a", sortStr: "a", sortAt: now},
		{key: "c", sortStr: "c", sortAt: now.Add(-1 * time.Hour)},
	}
	sortHostBuckets(buckets, "hostname", "asc")
	if buckets[0].key != "a" || buckets[2].key != "c" {
		t.Fatalf("hostname asc = %+v", buckets)
	}
	// Descending by last_seen puts the freshest bucket first.
	sortHostBuckets(buckets, "last_seen", "desc")
	if buckets[0].key != "a" {
		t.Fatalf("last_seen desc = %+v", buckets)
	}
}
