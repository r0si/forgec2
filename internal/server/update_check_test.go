package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// updateTestServers tracks every server built by newUpdateTestServer so
// setServerVersion can drain them before restoring the package-level
// ServerVersion. handleHotUpdate finishes the update on a wg-tracked
// goroutine that reads ServerVersion, so without this drain the version
// restore raced a live worker — a genuine data race the detector flags.
var updateTestServers []*Server

func newUpdateTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.DefaultConfig()
	cfg.Server.UpdateCheckRepo = "forgec2-test/nonexistent-update-repo"
	s := &Server{db: testutil.SetupTestDB(t), cfg: cfg, ctx: context.Background()}
	updateTestServers = append(updateTestServers, s)
	return s
}

// stubReleaseAPI points the release-assets URL at a local server for the rest
// of the test. Without it the accept path reaches api.github.com for real:
// three attempts of DNS plus TLS against a repo that does not exist, which
// made this test take ~17s and fail whenever the network was unavailable.
// The stub answers 404, which is the branch performHotUpdate already handles.
func stubReleaseAPI(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	previous := releaseAssetsURLFmt
	releaseAssetsURLFmt = srv.URL + "/repos/%s/releases/tags/%s"
	t.Cleanup(func() {
		// Drain before restoring: the worker reads this var, so the write must
		// not land while one is still in flight. Doing both in one cleanup
		// makes that invariant independent of cleanup ordering.
		drainUpdateWorkers(t)
		releaseAssetsURLFmt = previous
		srv.Close()
	})
}

// drainUpdateWorkers waits for background workers spawned by the update
// handlers, bounded so a stalled fetch cannot wedge the suite. A timeout here
// is itself a failure: it means a worker outlived the test that started it.
//
// The hot-update goroutine lives in updateWg (it must not be part of the
// server's own waitgroup: it ends by shutting the server down), so both are
// drained here.
func drainUpdateWorkers(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, s := range updateTestServers {
			s.wg.Wait()
			s.updateWg.Wait()
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Error("background update worker outlived the test")
	}
}

// updateWait fails the test if ch does not produce a value in time.
func updateWait(t *testing.T, ch <-chan []byte, what string) []byte {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

// ── Version comparison ──────────────────────────────────────────────────────

// TestCompareVersions pins the ordering that decides whether a hot update is
// offered. A wrong sign here means either a downgrade (replacing the server
// with an older, vulnerable build) or an update that is never offered. An
// unreadable version on either side must report "not newer" in both
// directions — that is the fail-closed rule.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want int
	}{
		{"identical", "1.0.0", "1.0.0", 0},
		{"v prefix stripped", "v1.2.3", "1.2.3", 0},
		{"missing trailing segment equals zero", "1.0", "1.0.0", 0},
		{"extra trailing segment equals zero", "1.0.0.0", "1.0.0", 0},
		{"patch higher", "1.0.1", "1.0.0", 1},
		{"patch lower", "1.0.0", "1.0.1", -1},
		{"minor higher", "1.10.0", "1.9.0", 1},
		{"numeric not lexicographic", "2.10.0", "2.9.9", 1},
		{"major higher", "10.0.0", "9.99.99", 1},
		{"zero equals empty", "", "0.0.0", 0},
		{"empty equals empty", "", "", 0},
		{"non-numeric on either side is unordered", "1.2.x", "1.2.1", 0},
		{"non-numeric on the other side is unordered too", "1.2.1", "1.2.x", 0},
		{"two non-numeric segments tie", "1.2.x", "1.2.y", 0},
		{"prerelease is not an upgrade over its release", "2.7.0-rc1", "2.7.0", 0},
		{"leading space is not an upgrade", " 1.0.0", "1.0.0", 0},
		{"negative segment is unordered", "1.-1.0", "1.0.0", 0},
		{"dev build is unordered against a release", "dev", "1.0.0", 0},
		{"release is unordered against a dev build", "1.0.0", "dev", 0},
		{"tag style vs dev is unordered", "v99.0.0", "dev", 0},
		{"overflowing segment is unordered", "1.0.99999999999999999999", "1.0.0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareVersions(tc.a, tc.b); got != tc.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
			// The relation must be antisymmetric.
			if got := compareVersions(tc.b, tc.a); got != -tc.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.b, tc.a, got, -tc.want)
			}
		})
	}
}

// setServerVersion pins the build version for a test and restores it, since the
// comparison guards read a package-level var. The drain and the restore are one
// cleanup so the drain always runs first, whatever order the other test
// cleanups were registered in.
func setServerVersion(t *testing.T, v string) {
	t.Helper()
	previous := ServerVersion
	ServerVersion = v
	t.Cleanup(func() {
		drainUpdateWorkers(t)
		ServerVersion = previous
	})
}

// TestHotUpdateNeverDowngradesAnUnreadableBuild proves the guard that matters
// most: when the running build carries no comparable version (a dev/nightly
// build, or one built without the version stamp), no remote release may be
// installed over it, because nothing can prove the remote build is newer.
func TestHotUpdateNeverDowngradesAnUnreadableBuild(t *testing.T) {
	s := newUpdateTestServer(t)
	setServerVersion(t, "dev")

	s.updateState.mu.Lock()
	s.updateState.Available = true
	s.updateState.LatestVersion = "v0.0.1" // far older than any real build
	s.updateState.mu.Unlock()

	c, w := updateCtx()
	s.handleHotUpdate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want the unreadable-build refusal", w.Code, w.Body.String())
	}
}

// ── Shell quoting (command-injection surface) ───────────────────────────────

// TestQuoteCmd pins the Windows cmd quoting rule: an embedded quote is doubled
// so it cannot terminate the argument early.
func TestQuoteCmd(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Program Files\forgec2\server.exe`, `"C:\Program Files\forgec2\server.exe"`},
		{`plain.exe`, `"plain.exe"`},
		{`a"b`, `"a""b"`},
		{`a"&calc&"b`, `"a""&calc&""b"`},
		{``, `""`},
		{`back\slash`, `"back\slash"`},
		{`%PATH%`, `"%PATH%"`},
	}
	for _, tc := range cases {
		if got := quoteCmd(tc.in); got != tc.want {
			t.Fatalf("quoteCmd(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestQuoteSh pins the POSIX quoting rule: single quotes make everything
// literal, and an embedded single quote is closed, escaped and reopened.
func TestQuoteSh(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/usr/local/bin/server", `'/usr/local/bin/server'`},
		{`it's`, `'it'\''s'`},
		{`$(id)`, `'$(id)'`},
		{"`id`", "'`id`'"},
		{`; rm -rf /`, `'; rm -rf /'`},
		{``, `''`},
		{`a b`, `'a b'`},
	}
	for _, tc := range cases {
		if got := quoteSh(tc.in); got != tc.want {
			t.Fatalf("quoteSh(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestQuoteShSurvivesRealShell proves the escaping is not merely string-equal
// but actually inert when handed to /bin/sh: every metacharacter payload comes
// back verbatim instead of executing.
func TestQuoteShSurvivesRealShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available on windows")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh on PATH: %v", err)
	}
	payloads := []string{
		"$(echo pwned)",
		"`echo pwned`",
		`'; echo pwned; '`,
		`a b`,
		"it's",
		`"; rm -rf /tmp/x "`,
		"new\nline",
	}
	for _, p := range payloads {
		t.Run(p, func(t *testing.T) {
			out, err := exec.Command(sh, "-c", "printf %s "+quoteSh(p)).CombinedOutput()
			if err != nil {
				t.Fatalf("sh -c failed: %v (%s)", err, out)
			}
			if string(out) != p {
				t.Fatalf("shell mangled the argument: got %q, want %q", out, p)
			}
		})
	}
}

// TestQuoteCmdCannotBeBrokenOutOf proves the Windows quoting rule structurally:
// every quote inside the quoted argument is doubled, so cmd can never see an
// unescaped quote that would close the argument and reopen the command line.
func TestQuoteCmdCannotBeBrokenOutOf(t *testing.T) {
	payloads := []string{
		`C:\Program Files\forgec2\server.exe`,
		`a"b`,
		`a"&calc&"b`,
		`"& net user hacker pass /add &"`,
		`" & del C:\ /q & rem "`,
		`x"`,
		`"x`,
		`""`,
	}
	for _, p := range payloads {
		t.Run(p, func(t *testing.T) {
			q := quoteCmd(p)
			if len(q) < 2 || q[0] != '"' || q[len(q)-1] != '"' {
				t.Fatalf("quoteCmd(%q) = %q, want a fully quoted argument", p, q)
			}
			inner := q[1 : len(q)-1]
			for i := 0; i < len(inner); i++ {
				if inner[i] != '"' {
					continue
				}
				if i+1 < len(inner) && inner[i+1] == '"' {
					i++
					continue
				}
				t.Fatalf("unescaped quote at offset %d in %q (from %q)", i, q, p)
			}
		})
	}
}

// ── Progress accounting ─────────────────────────────────────────────────────

// TestProgressWriterAccumulates proves the download progress is cumulative and
// that a nil callback is safe (responses with an unknown length still stream).
func TestProgressWriterAccumulates(t *testing.T) {
	var seen [][2]int64
	w := &progressWriter{total: 300, onProgress: func(downloaded, total int64) {
		seen = append(seen, [2]int64{downloaded, total})
	}}
	if n, err := w.Write([]byte("aaaaa")); n != 5 || err != nil {
		t.Fatalf("Write = %d, %v; want 5, nil", n, err)
	}
	if _, err := w.Write([]byte("bbbb")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if w.downloaded != 9 {
		t.Fatalf("downloaded = %d, want 9", w.downloaded)
	}
	if len(seen) != 2 || seen[1][0] != 9 || seen[1][1] != 300 {
		t.Fatalf("callbacks = %v, want a cumulative 9/300", seen)
	}

	// Nil callback must not panic.
	bare := &progressWriter{total: -1}
	if n, err := bare.Write([]byte("x")); n != 1 || err != nil {
		t.Fatalf("nil-callback Write = %d, %v", n, err)
	}
}

// ── URL construction and notification ───────────────────────────────────────

// TestReleaseURLUsesConfiguredRepo proves the download link the operator is
// shown is built from the configured repository, not from request input.
func TestReleaseURLUsesConfiguredRepo(t *testing.T) {
	s := newUpdateTestServer(t)
	s.cfg.Server.UpdateCheckRepo = "acme/forgec2"
	if got := s.releaseURL("v2.7.0"); got != "https://github.com/acme/forgec2/releases/tag/v2.7.0" {
		t.Fatalf("releaseURL = %q", got)
	}
}

// TestBroadcastUpdateNotificationCarriesDownloadLink proves the WS alert names
// the release page, so the operator never has to guess where to pull from.
func TestBroadcastUpdateNotificationCarriesDownloadLink(t *testing.T) {
	s := newUpdateTestServer(t)
	s.cfg.Server.UpdateCheckRepo = "acme/forgec2"

	conn := &wsClientConn{ch: make(chan []byte, 8)}
	s.wsClients = map[*websocket.Conn]*wsClientConn{nil: conn}

	s.broadcastUpdateNotification("v3.0.0")

	raw := updateWait(t, conn.ch, "the update notification")
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("notification is not JSON: %v (%s)", err, raw)
	}
	if payload["type"] != "update_available" {
		t.Fatalf("type = %v, want update_available", payload["type"])
	}
	if payload["latest"] != "v3.0.0" {
		t.Fatalf("latest = %v", payload["latest"])
	}
	if payload["download_url"] != "https://github.com/acme/forgec2/releases/tag/v3.0.0" {
		t.Fatalf("download_url = %v", payload["download_url"])
	}
	if payload["current"] != ServerVersion {
		t.Fatalf("current = %v, want %s", payload["current"], ServerVersion)
	}
}

// ── Checksum / signature retrieval ──────────────────────────────────────────

// TestFetchChecksumFileCappedAndDerived proves the checksum download is capped
// (a hostile release cannot stream unbounded data into memory) and that the
// asset name is derived from the binary path, not from the URL.
func TestFetchChecksumFileCappedAndDerived(t *testing.T) {
	body := strings.Repeat("a", 8192)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	data, name, err := fetchChecksumFile(srv.URL, filepath.Join("some", "dir", "server.exe"))
	if err != nil {
		t.Fatalf("fetchChecksumFile: %v", err)
	}
	if len(data) != 4096 {
		t.Fatalf("read %d bytes, want the 4096-byte cap", len(data))
	}
	if name != "server.exe" {
		t.Fatalf("asset name = %q, want the binary's basename", name)
	}
}

// TestFetchChecksumFileFailsOnUnreachableHost proves a network error is
// surfaced instead of an empty checksum that would skip verification.
func TestFetchChecksumFileFailsOnUnreachableHost(t *testing.T) {
	if _, _, err := fetchChecksumFile("http://127.0.0.1:1/release.sha256", "server.exe"); err == nil {
		t.Fatal("unreachable checksum host reported success")
	}
}

// TestFetchReleaseSignatureFailsLoud proves a missing signature asset is an
// error, not an empty string that would silently skip verification.
func TestFetchReleaseSignatureFailsLoud(t *testing.T) {
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release.sha256.sig" {
			t.Errorf("signature fetched from %q, want the .sig sibling", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer missing.Close()

	if _, err := fetchReleaseSignature(missing.URL + "/release.sha256"); err == nil {
		t.Fatal("missing signature reported success")
	}

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("ab", 2048))) // 4 KiB, over the 1 KiB cap
	}))
	defer ok.Close()
	sig, err := fetchReleaseSignature(ok.URL + "/release.sha256")
	if err != nil {
		t.Fatalf("fetchReleaseSignature: %v", err)
	}
	if len(sig) != 1024 {
		t.Fatalf("read %d signature bytes, want the 1024-byte cap", len(sig))
	}
}

// ── Binary download ─────────────────────────────────────────────────────────

// TestDownloadUpdateFileWritesAndReportsProgress proves a successful download
// lands on disk byte-for-byte and drives the progress machine to completion.
func TestDownloadUpdateFileWritesAndReportsProgress(t *testing.T) {
	payload := bytes.Repeat([]byte("MZ-binary"), 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "ForgeC2/") {
			t.Errorf("User-Agent = %q, want a ForgeC2 agent", got)
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	s := newUpdateTestServer(t)
	dest := filepath.Join(t.TempDir(), "server.exe")
	if err := s.downloadUpdateFile(srv.URL, dest, "v9.9.9"); err != nil {
		t.Fatalf("downloadUpdateFile: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read downloaded binary: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(payload))
	}
	s.updateState.mu.RLock()
	stage, progress, version := s.updateState.Stage, s.updateState.Progress, s.updateState.TargetVersion
	s.updateState.mu.RUnlock()
	if stage != updateStageDownloading || progress != 100 || version != "v9.9.9" {
		t.Fatalf("progress state: stage=%q progress=%d version=%q", stage, progress, version)
	}
}

// TestDownloadUpdateFileRefusesNonOK proves a 404 release asset leaves no file
// behind: the hot-update path must abort instead of restarting on a stub.
func TestDownloadUpdateFileRefusesNonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s := newUpdateTestServer(t)
	dest := filepath.Join(t.TempDir(), "server.exe")
	err := s.downloadUpdateFile(srv.URL, dest, "v9.9.9")
	if err == nil {
		t.Fatal("404 download reported success")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error %q does not name the status", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("a stub file was left behind after a failed download")
	}
}

// TestDownloadUpdateFileRejectsBadURL proves an unusable URL fails before any
// file is created.
func TestDownloadUpdateFileRejectsBadURL(t *testing.T) {
	s := newUpdateTestServer(t)
	dest := filepath.Join(t.TempDir(), "server.exe")
	if err := s.downloadUpdateFile("://not a url", dest, "v9.9.9"); err == nil {
		t.Fatal("malformed URL reported success")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("a file was created for a malformed URL")
	}
}

// ── Handler surface ─────────────────────────────────────────────────────────

func updateCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/update-check", nil)
	c.Set("user", "update-op")
	c.Set("user_role", "admin")
	return c, w
}

// TestHandleUpdateCheckShapes pins the status payload for both the idle and the
// "an update exists" states, including the operator-facing download link and a
// surfaced check error (a failed check must never look like a clean state).
func TestHandleUpdateCheckShapes(t *testing.T) {
	s := newUpdateTestServer(t)
	s.cfg.Server.UpdateCheckRepo = "acme/forgec2"

	c, w := updateCtx()
	s.handleUpdateCheck(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var idle map[string]interface{}
	unmarshalData(t, w.Body.Bytes(), &idle)
	if idle["update_available"] != false {
		t.Fatalf("update_available = %v, want false", idle["update_available"])
	}
	if _, present := idle["latest_version"]; present {
		t.Fatal("idle state leaked a latest_version")
	}
	if _, present := idle["check_error"]; present {
		t.Fatal("idle state leaked a check_error")
	}

	s.updateState.mu.Lock()
	s.updateState.Available = true
	s.updateState.LatestVersion = "v3.0.0"
	s.updateState.Error = "rate limited"
	s.updateState.mu.Unlock()

	c2, w2 := updateCtx()
	s.handleUpdateCheck(c2)
	var avail map[string]interface{}
	unmarshalData(t, w2.Body.Bytes(), &avail)
	if avail["update_available"] != true || avail["latest_version"] != "v3.0.0" {
		t.Fatalf("available state = %v", avail)
	}
	if avail["download_url"] != "https://github.com/acme/forgec2/releases/tag/v3.0.0" {
		t.Fatalf("download_url = %v", avail["download_url"])
	}
	if avail["check_error"] != "rate limited" {
		t.Fatalf("check_error = %v, want the recorded failure", avail["check_error"])
	}
	if avail["current_version"] != ServerVersion {
		t.Fatalf("current_version = %v", avail["current_version"])
	}
}

// TestHandleCheckVersionReportsUpToDate proves the dashboard's "is_latest"
// verdict follows compareVersions, so an older published release can never be
// presented as something to install.
func TestHandleCheckVersionReportsUpToDate(t *testing.T) {
	s := newUpdateTestServer(t)
	setServerVersion(t, "2.6.2")
	s.cfg.Server.UpdateCheckRepo = "acme/forgec2"

	s.updateState.mu.Lock()
	s.updateState.Available = true
	s.updateState.LatestVersion = "v2.6.1"
	s.updateState.CheckOK = true
	s.updateState.mu.Unlock()

	c, w := updateCtx()
	s.handleCheckVersion(c)
	var body map[string]interface{}
	unmarshalData(t, w.Body.Bytes(), &body)
	if body["is_latest"] != true {
		t.Fatalf("is_latest = %v for an older latest_version", body["is_latest"])
	}
	if body["current_version"] != "2.6.2" {
		t.Fatalf("current_version = %v", body["current_version"])
	}
	if body["check_ok"] != true {
		t.Fatalf("check_ok = %v, want true after a successful check", body["check_ok"])
	}

	// A newer release must flip the verdict.
	s.updateState.mu.Lock()
	s.updateState.LatestVersion = "v2.7.0"
	s.updateState.mu.Unlock()
	c2, w2 := updateCtx()
	s.handleCheckVersion(c2)
	var newer map[string]interface{}
	unmarshalData(t, w2.Body.Bytes(), &newer)
	if newer["is_latest"] != false {
		t.Fatalf("is_latest = %v for a newer latest_version", newer["is_latest"])
	}
	if newer["download_url"] != "https://github.com/acme/forgec2/releases/tag/v2.7.0" {
		t.Fatalf("download_url = %v", newer["download_url"])
	}
}

// TestHandleCheckVersionNeverClaimsUpToDateAfterAFailedCheck pins the rule
// that made every update invisible: a failed check (wrong repository, rate
// limit, no network) used to report "you are up to date", because the verdict
// was derived from the availability flag alone.
func TestHandleCheckVersionNeverClaimsUpToDateAfterAFailedCheck(t *testing.T) {
	s := newUpdateTestServer(t)
	setServerVersion(t, "2.6.2")

	s.updateState.mu.Lock()
	s.updateState.LatestVersion = "v2.9.0"
	s.updateState.Available = false
	s.updateState.CheckOK = false
	s.updateState.Error = `github api: repository "forgec2/forgec2" does not exist`
	s.updateState.mu.Unlock()

	c, w := updateCtx()
	s.handleCheckVersion(c)
	var body map[string]interface{}
	unmarshalData(t, w.Body.Bytes(), &body)
	if body["is_latest"] != false {
		t.Fatalf("is_latest = %v after a failed check, want false", body["is_latest"])
	}
	if body["check_ok"] != false {
		t.Fatalf("check_ok = %v after a failed check, want false", body["check_ok"])
	}
	if body["check_state"] != "error" {
		t.Fatalf("check_state = %v, want error", body["check_state"])
	}
	if body["check_error"] == nil {
		t.Fatal("the failed check did not surface its error")
	}
}

// TestHandleHotUpdateRefusesASecondConcurrentUpdate proves the hot-update
// guard: a second request while one is running is refused instead of
// starting a second download and a second restarter against the same binary.
func TestHandleHotUpdateRefusesASecondConcurrentUpdate(t *testing.T) {
	s := newUpdateTestServer(t)
	setServerVersion(t, "2.6.2")
	stubReleaseAPI(t)

	s.updateState.mu.Lock()
	s.updateState.Available = true
	s.updateState.LatestVersion = "v99.0.0"
	s.updateState.InFlight = true
	s.updateState.mu.Unlock()

	c, w := updateCtx()
	s.handleHotUpdate(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s, want 409", w.Code, w.Body.String())
	}
}

// TestHandleHotUpdateRefusesDowngradeAndNoUpdate proves the two gates in front
// of a self-replacing restart: nothing to do, and never an older build. Neither
// refusal may leave an audit trail implying an update ran.
func TestHandleHotUpdateRefusesDowngradeAndNoUpdate(t *testing.T) {
	s := newUpdateTestServer(t)
	setServerVersion(t, "2.6.2")
	if err := s.db.Create(&db.User{Username: "update-op", TenantID: 1, Role: "admin", IsActive: true}).Error; err != nil {
		t.Fatalf("seed caller: %v", err)
	}

	// No update discovered yet.
	c, w := updateCtx()
	s.handleHotUpdate(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "No updates available") {
		t.Fatalf("no update: status=%d body=%s", w.Code, w.Body.String())
	}

	// An "available" version that is not newer must not be installed.
	s.updateState.mu.Lock()
	s.updateState.Available = true
	s.updateState.LatestVersion = "v2.6.1"
	s.updateState.mu.Unlock()
	c2, w2 := updateCtx()
	s.handleHotUpdate(c2)
	if w2.Code != http.StatusBadRequest || !strings.Contains(w2.Body.String(), "Already up to date") {
		t.Fatalf("downgrade: status=%d body=%s", w2.Code, w2.Body.String())
	}

	// The running version itself is refused too.
	s.updateState.mu.Lock()
	s.updateState.LatestVersion = ServerVersion
	s.updateState.mu.Unlock()
	c3, w3 := updateCtx()
	s.handleHotUpdate(c3)
	if w3.Code != http.StatusBadRequest || !strings.Contains(w3.Body.String(), "Already up to date") {
		t.Fatalf("same version: status=%d body=%s", w3.Code, w3.Body.String())
	}

	var audits int64
	if err := s.db.Model(&db.AuditLog{}).Count(&audits).Error; err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if audits != 0 {
		t.Fatalf("%d audit entries written for refused hot updates", audits)
	}
}

// TestHandleHotUpdateAcceptsNewerVersionAndAudits proves the accepted path:
// 202, the target version recorded for the progress poller, and an audit entry
// stamped with the requesting operator.
func TestHandleHotUpdateAcceptsNewerVersionAndAudits(t *testing.T) {
	s := newUpdateTestServer(t)
	setServerVersion(t, "2.6.2")
	stubReleaseAPI(t)
	if err := s.db.Create(&db.User{Username: "update-op", TenantID: 1, Role: "admin", IsActive: true}).Error; err != nil {
		t.Fatalf("seed caller: %v", err)
	}
	s.updateState.mu.Lock()
	s.updateState.Available = true
	s.updateState.LatestVersion = "v99.0.0"
	s.updateState.mu.Unlock()

	c, w := updateCtx()
	s.handleHotUpdate(c)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s, want 202", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	unmarshalData(t, w.Body.Bytes(), &body)
	if body["latest_version"] != "v99.0.0" || body["progress_url"] != "/api/update-progress" {
		t.Fatalf("accepted response = %v (body=%s)", body, w.Body.String())
	}
	s.updateState.mu.RLock()
	target := s.updateState.TargetVersion
	s.updateState.mu.RUnlock()
	if target != "v99.0.0" {
		t.Fatalf("TargetVersion = %q, want the accepted version", target)
	}

	var entry db.AuditLog
	if err := s.db.Where("action = ?", "hot_update").First(&entry).Error; err != nil {
		t.Fatalf("no hot_update audit entry: %v", err)
	}
	if entry.User != "update-op" {
		t.Fatalf("audit user = %q, want the requesting operator", entry.User)
	}
	if !strings.Contains(entry.Details, "v99.0.0") {
		t.Fatalf("audit details = %q, want the target version", entry.Details)
	}
	if !entry.Success {
		t.Fatal("audit entry not marked successful")
	}
}

// ── Version check semantics ──────────────────────────────────────────────────

// stubReleaseAssets points the release-assets endpoint at a local server that
// serves the given asset list.
func stubReleaseAssets(t *testing.T, assets []GitHubAsset) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body, err := json.Marshal(GitHubReleaseFull{
			TagName: "v9.9.9",
			Assets:  assets,
		})
		if err != nil {
			t.Errorf("encode release: %v", err)
			return
		}
		_, _ = w.Write(body)
	}))
	previous := releaseAssetsURLFmt
	releaseAssetsURLFmt = srv.URL + "/repos/%s/releases/tags/%s"
	t.Cleanup(func() {
		drainUpdateWorkers(t)
		releaseAssetsURLFmt = previous
		srv.Close()
	})
}

// stubGitHubAPI points the version-check and repository endpoints at a local
// server. releaseStatus is the status the releases endpoint answers with,
// repoStatus the one the repository probe answers with.
func stubGitHubAPI(t *testing.T, releaseStatus int, repoStatus int, releaseBody string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/releases/") {
			w.WriteHeader(releaseStatus)
			if releaseBody != "" {
				_, _ = w.Write([]byte(releaseBody))
			}
			return
		}
		w.WriteHeader(repoStatus)
		if repoStatus == http.StatusOK {
			_, _ = w.Write([]byte(`{"full_name":"acme/forgec2"}`))
		}
	}))
	previousCheck, previousRepo := updateCheckURLFmt, releaseRepoURLFmt
	updateCheckURLFmt = srv.URL + "/repos/%s/releases/latest"
	releaseRepoURLFmt = srv.URL + "/repos/%s"
	t.Cleanup(func() {
		updateCheckURLFmt, releaseRepoURLFmt = previousCheck, previousRepo
		srv.Close()
	})
}

// TestFetchLatestVersionReportsAMissingRepository proves a mistyped
// repository is reported as an error. It used to answer 404, which was read
// as "no new release": the server claimed to be up to date and every update
// stayed invisible.
func TestFetchLatestVersionReportsAMissingRepository(t *testing.T) {
	stubGitHubAPI(t, http.StatusNotFound, http.StatusNotFound, "")

	_, err := fetchLatestVersion("acme/nope")
	if err == nil {
		t.Fatal("a missing repository reported success")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %q, want it to name the missing repository", err)
	}
}

// TestFetchLatestVersionAcceptsARepositoryWithoutReleases proves an existing
// repository without any release is not an error.
func TestFetchLatestVersionAcceptsARepositoryWithoutReleases(t *testing.T) {
	stubGitHubAPI(t, http.StatusNotFound, http.StatusOK, "")

	version, err := fetchLatestVersion("acme/forgec2")
	if err != nil {
		t.Fatalf("fetchLatestVersion: %v", err)
	}
	if version != "" {
		t.Fatalf("version = %q, want an empty version", version)
	}
}

// TestFetchLatestVersionReturnsThePublishedTag proves the happy path.
func TestFetchLatestVersionReturnsThePublishedTag(t *testing.T) {
	stubGitHubAPI(t, http.StatusOK, http.StatusOK, `{"tag_name":"v2.7.1"}`)

	version, err := fetchLatestVersion("acme/forgec2")
	if err != nil {
		t.Fatalf("fetchLatestVersion: %v", err)
	}
	if version != "v2.7.1" {
		t.Fatalf("version = %q", version)
	}
}

// TestPerformHotUpdateRefusesAReleaseWithoutThisPlatform proves the hot
// update refuses a release that carries no binary for the running platform
// and leaves the installation untouched.
func TestPerformHotUpdateRefusesAReleaseWithoutThisPlatform(t *testing.T) {
	stubReleaseAssets(t, []GitHubAsset{
		{Name: "forgec2-server-plan9-amd64"},
		{Name: "forgec2-server-plan9-amd64.sha256"},
	})

	s := newUpdateTestServer(t)
	err := s.performHotUpdate("v9.9.9")
	if err == nil {
		t.Fatal("a release without this platform was installed")
	}
	if !strings.Contains(err.Error(), "no ") && !strings.Contains(err.Error(), "server binary") {
		t.Fatalf("error = %q, want it to explain the missing binary", err)
	}
	s.updateState.mu.RLock()
	stage := s.updateState.Stage
	s.updateState.mu.RUnlock()
	if stage != updateStageFailed {
		t.Fatalf("stage = %q, want failed", stage)
	}
}

// TestPerformHotUpdateRefusesAReleaseWithoutAChecksum proves the checksum
// must belong to the selected binary: a release without one is refused
// instead of installing an unverified binary.
func TestPerformHotUpdateRefusesAReleaseWithoutAChecksum(t *testing.T) {
	stubReleaseAssets(t, []GitHubAsset{
		{Name: canonicalBinaryName(runtime.GOOS, runtime.GOARCH)},
	})

	s := newUpdateTestServer(t)
	err := s.performHotUpdate("v9.9.9")
	if err == nil {
		t.Fatal("a release without a checksum was installed")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error = %q, want it to name the missing checksum", err)
	}
}

// ── Async checker lifecycle ─────────────────────────────────────────────────

// TestInitUpdateCheckerStopsWithServerContext proves the background checker is
// bound to the server context: shutting the server down releases its waitgroup
// instead of leaking a goroutine that outlives the process.
func TestInitUpdateCheckerStopsWithServerContext(t *testing.T) {
	s := newUpdateTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.initUpdateChecker()

	cancel()

	released := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(released)
	}()
	select {
	case <-released:
	case <-time.After(10 * time.Second):
		t.Fatal("update checker goroutine outlived server cancellation")
	}
}
