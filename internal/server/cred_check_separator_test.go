package server

import (
	"fmt"
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

// TestCredentialCommandHasSeparator is the guard both cred_check paths use: the
// implant splits the command on '|', so any field containing one shifts the rest
// and the check runs against the wrong account (while the lockout fuse still
// accounts for the domain the SERVER parsed).
func TestCredentialCommandHasSeparator(t *testing.T) {
	if credentialCommandHasSeparator("alice", "corp.local", "Spring2026!", "") {
		t.Fatal("clean fields were reported as containing the separator")
	}
	for _, fields := range [][]string{
		{"ali|ce", "corp.local", "pw", ""},
		{"alice", "corp|local", "pw", ""},
		{"alice", "corp.local", "p|w", ""},
		{"alice", "corp.local", "pw", "10.0.0.1|"},
	} {
		if !credentialCommandHasSeparator(fields...) {
			t.Fatalf("separator not detected in %q", fields)
		}
	}
	if credentialCommandHasSeparator() {
		t.Fatal("no fields reported a separator")
	}
}

func credCheckCtx(t *testing.T, agentID string, form url.Values) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+"/cred-check", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = gin.Params{{Key: "id", Value: agentID}}
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}

// TestHandleCredCheckRejectsSeparator pins the manual cred_check refusal, and
// that nothing is queued when it refuses.
func TestHandleCredCheckRejectsSeparator(t *testing.T) {
	s := mustTenantServer(t)
	id := seedSprayAgent(t, s)

	c, w := credCheckCtx(t, id, url.Values{
		"user":     {"alice"},
		"domain":   {"corp.local"},
		"password": {"pa|ss"},
	})
	s.handleCredCheck(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "'|'") {
		t.Fatalf("the refusal does not name the separator: %s", w.Body.String())
	}
	var count int64
	s.db.Model(&db.Task{}).Where("agent_id = ? AND type = ?", id, "cred_check").Count(&count)
	if count != 0 {
		t.Fatal("a task was created despite the refusal")
	}
}

// TestHandleBatchVerifySkipsSeparatorEntries pins the reuse path: a stored
// password containing '|' is skipped with a visible status instead of queueing a
// cred_check that would mean something else.
func TestHandleBatchVerifySkipsSeparatorEntries(t *testing.T) {
	s := mustTenantServer(t)
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	id := seedSprayAgent(t, s)

	bad := db.CredentialEntry{AgentID: id, Username: "alice", Domain: "corp.local", Password: "pa|ss", TenantID: 1}
	if err := s.db.Create(&bad).Error; err != nil {
		t.Fatal(err)
	}
	good := db.CredentialEntry{AgentID: id, Username: "bob", Domain: "corp.local", Password: "Spring2026!", TenantID: 1}
	if err := s.db.Create(&good).Error; err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"ids":[%d,%d]}`, bad.ID, good.ID)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/credentials/verify", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleBatchVerifyCredentials(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var out struct {
		Queued   int `json:"queued"`
		Outcomes []struct {
			ID     uint   `json:"id"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"results"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)

	byID := map[uint]string{}
	for _, o := range out.Outcomes {
		byID[o.ID] = o.Status
	}
	if got := byID[bad.ID]; got != "skipped_separator" {
		t.Fatalf("entry with a separator got status %q, want skipped_separator", got)
	}
	if got := byID[good.ID]; got != "queued" {
		t.Fatalf("clean entry got status %q, want queued", got)
	}
	// Only the clean entry produced a task.
	var count int64
	s.db.Model(&db.Task{}).Where("agent_id = ? AND type = ?", id, "cred_check").Count(&count)
	if count != 1 {
		t.Fatalf("cred_check tasks = %d, want 1", count)
	}
	_ = time.Now()
}
