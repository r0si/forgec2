package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestHandleRerunTaskRequiresOperator pins the rerun gate: it creates a task,
// so a missing role, a viewer and an unknown role must all be refused (the old
// check only rejected the literal "viewer").
func TestHandleRerunTaskRequiresOperator(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	seedTenantAgent(t, s, "rerun-agent", 1)
	original := db.Task{AgentID: "rerun-agent", TenantID: 1, Type: "shell", Command: "whoami", Status: "completed"}
	if err := s.db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}

	call := func(role string, setRole bool) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/agents/rerun-agent/task/1/rerun", nil)
		c.Params = gin.Params{
			{Key: "id", Value: "rerun-agent"},
			{Key: "taskId", Value: jsonNumber(original.ID)},
		}
		c.Set("user", "admin")
		c.Set("user_id", uint(1))
		c.Set("tenant_id", uint(1))
		if setRole {
			c.Set("user_role", role)
		}
		s.handleRerunTask(c)
		return w.Code
	}

	if code := call("", false); code != http.StatusForbidden {
		t.Fatalf("rerun without a role = %d, want 403", code)
	}
	if code := call("viewer", true); code != http.StatusForbidden {
		t.Fatalf("rerun as viewer = %d, want 403", code)
	}
	if code := call("not-a-role", true); code != http.StatusForbidden {
		t.Fatalf("rerun as an unknown role = %d, want 403", code)
	}
	if code := call(db.RoleUser, true); code == http.StatusForbidden {
		t.Fatal("rerun as user was refused")
	}
}

// TestUserCanViewScreenGate covers the socket-side screen fan-out gate.
func TestUserCanViewScreenGate(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)

	for _, u := range []db.User{
		{Username: "watch-viewer", Role: "viewer", TenantID: 1},
		{Username: "watch-blank", Role: "", TenantID: 1},
		{Username: "watch-unknown", Role: "not-a-role", TenantID: 1},
	} {
		if err := s.db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Create(&db.CustomRole{Name: "screens", Permissions: `["agents.write"]`}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&db.User{Username: "watch-custom", Role: "screens", TenantID: 1}).Error; err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		user string
		want bool
	}{
		{"admin", true},
		{"watch-viewer", false},
		{"watch-blank", false},
		{"watch-unknown", false},
		{"watch-custom", true},
		{"nobody-at-all", false},
	}
	for _, tc := range cases {
		if got := s.userCanViewScreen(tc.user); got != tc.want {
			t.Fatalf("userCanViewScreen(%q) = %v, want %v", tc.user, got, tc.want)
		}
	}
}

// TestRoleGrantsOperatorCustomRole pins that an existing custom role passes the
// gate; its write permission is enforced separately by the permission checks.
func TestRoleGrantsOperatorCustomRole(t *testing.T) {
	s := mustTenantServer(t)
	if err := s.db.Create(&db.CustomRole{Name: "read-only-custom"}).Error; err != nil {
		t.Fatal(err)
	}
	if !s.roleGrantsOperator("read-only-custom") {
		t.Fatal("existing custom role refused by the operator gate")
	}
}
