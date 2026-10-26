package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

// TestRequireOperatorRoleMatrix pins the operator gate: the built-in operator
// roles pass, the read-only/legacy ones do not, an unknown role does NOT
// inherit operator rights (the old blocklist let any string through), and a
// custom role that exists in custom_roles does.
func TestRequireOperatorRoleMatrix(t *testing.T) {
	s := mustTenantServer(t)
	seedMissingAPIAdmin(t, s)
	if err := s.db.Create(&db.CustomRole{Name: "analyst", Description: "triage"}).Error; err != nil {
		t.Fatalf("seed custom role: %v", err)
	}

	cases := []struct {
		role string
		want bool
	}{
		{db.RoleAdmin, true},
		{db.RoleUser, true},
		{"operator", true}, // legacy alias MigrateOldRoles rewrites to user
		{"analyst", true},  // custom role that exists
		{"viewer", false},  // read-only legacy role
		{"guest", false},   // legacy alias for a read-only role
		{"", false},
		{"not-a-role", false},
		{"Admin", false}, // role matching is exact
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			c, w := missingAPICtx(http.MethodPost, "/api/v1/tasks", `{}`, nil)
			c.Set("user_role", tc.role)
			got := s.requireOperator(c)
			if got != tc.want {
				t.Fatalf("requireOperator(%q) = %v, want %v", tc.role, got, tc.want)
			}
			if !tc.want {
				if w.Code != http.StatusForbidden {
					t.Fatalf("refusal status = %d, want 403", w.Code)
				}
				if !strings.Contains(w.Body.String(), "operator role required") {
					t.Fatalf("refusal body = %s", w.Body.String())
				}
			}
		})
	}
}

// TestRequireOperatorFailsClosedWithoutDB is the defensive case: a Server with
// no database cannot verify a custom role and must deny rather than allow.
func TestRequireOperatorFailsClosedWithoutDB(t *testing.T) {
	s := &Server{}
	if s.roleGrantsOperator("analyst") {
		t.Fatal("unknown role allowed without a database to verify it")
	}
	// Built-ins do not need the database.
	if !s.roleGrantsOperator(db.RoleAdmin) {
		t.Fatal("admin refused without a database")
	}
}
