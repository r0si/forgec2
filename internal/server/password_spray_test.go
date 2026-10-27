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

func sprayCtx(t *testing.T, s *Server, agentID string, form url.Values) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+"/password-spray", strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.Params = gin.Params{{Key: "id", Value: agentID}}
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}

func seedSprayAgent(t *testing.T, s *Server) string {
	t.Helper()
	id := "33333333-3333-3333-3333-333333333333"
	agent := db.Implant{ID: id, TenantID: 1, Hostname: "spray-host", Status: "online", LastSeen: time.Now()}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

// TestHandlePasswordSprayRejectsSeparatorInFields pins the guard against a task
// that silently means something else: the implant splits the command on '|', so a
// password containing one shifts every later field and the spray would run
// against the WRONG domain — locking out accounts in it.
func TestHandlePasswordSprayRejectsSeparatorInFields(t *testing.T) {
	s := mustTenantServer(t)
	id := seedSprayAgent(t, s)

	cases := []struct {
		name string
		form url.Values
	}{
		{"password", url.Values{"password": {"pa|ss"}, "domain": {"corp.local"}, "usernames": {"alice"}}},
		{"domain", url.Values{"password": {"pw"}, "domain": {"corp|local"}, "usernames": {"alice"}}},
		{"dc", url.Values{"password": {"pw"}, "domain": {"corp.local"}, "dc": {"10.0.0.1|x"}, "usernames": {"alice"}}},
		{"delay_ms", url.Values{"password": {"pw"}, "domain": {"corp.local"}, "delay_ms": {"500|1"}, "usernames": {"alice"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := sprayCtx(t, s, id, tc.form)
			s.handlePasswordSpray(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "'|'") {
				t.Fatalf("the refusal does not explain the separator: %s", w.Body.String())
			}
			var count int64
			s.db.Model(&db.Task{}).Where("agent_id = ? AND type = ?", id, "password_spray").Count(&count)
			if count != 0 {
				t.Fatal("a task was created despite the refusal")
			}
		})
	}
}

// TestHandlePasswordSprayBuildsTheTask covers the happy path: the command is
// assembled in the documented order, the username list travels as task data, and
// the required fields are enforced.
func TestHandlePasswordSprayBuildsTheTask(t *testing.T) {
	s := mustTenantServer(t)
	// password_spray is a sensitive task type: its Command is encrypted at rest,
	// so the vault must be initialised or the insert fails closed.
	forgecrypto.InitLootEncryption(testStorageKeyHex)
	id := seedSprayAgent(t, s)

	c, w := sprayCtx(t, s, id, url.Values{
		"password":  {"Spring2026!"},
		"domain":    {"corp.local"},
		"dc":        {"10.1.2.3"},
		"delay_ms":  {"750"},
		"usernames": {"alice\nbob\n\n  carol  \n"},
	})
	s.handlePasswordSpray(c)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	var task db.Task
	if err := s.db.Where("agent_id = ? AND type = ?", id, "password_spray").First(&task).Error; err != nil {
		t.Fatalf("task not created: %v", err)
	}
	if task.Command != "Spring2026!|corp.local|10.1.2.3|750" {
		t.Fatalf("command = %q", task.Command)
	}
	if !strings.Contains(task.Data, "alice") || !strings.Contains(task.Data, "carol") {
		t.Fatalf("usernames missing from task data: %q", task.Data)
	}

	// Required fields.
	for _, form := range []url.Values{
		{"domain": {"corp.local"}, "usernames": {"alice"}}, // no password
		{"password": {"pw"}, "usernames": {"alice"}},       // no domain
		{"password": {"pw"}, "domain": {"corp.local"}},     // no usernames
	} {
		c, w := sprayCtx(t, s, id, form)
		s.handlePasswordSpray(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("missing field accepted (%d): %v", w.Code, form)
		}
	}
}
