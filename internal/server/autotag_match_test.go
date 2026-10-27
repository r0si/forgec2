package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestAutoTagMatchOperators pins the predicate evaluator used by the auto-tag
// sweeper: every operator, case-insensitivity, the AND semantics across
// conditions, and the fail-closed cases (empty, malformed, unknown field, an
// invalid regex must never match everything).
func TestAutoTagMatchOperators(t *testing.T) {
	agent := db.Implant{
		ID: "at-1", Hostname: "WIN-DC01", OS: "Windows", Arch: "amd64",
		IP: "10.0.0.5", Username: "Administrator", PublicIP: "203.0.113.9",
		ProcessName: "lsass.exe", Domain: "CORP", LastSeen: time.Now(),
	}

	cases := []struct {
		name string
		cond string
		want bool
	}{
		{"contains, case-insensitive", `[{"field":"hostname","op":"contains","value":"dc"}]`, true},
		{"equals, case-insensitive", `[{"field":"os","op":"equals","value":"windows"}]`, true},
		{"not_equals", `[{"field":"os","op":"not_equals","value":"linux"}]`, true},
		{"starts_with", `[{"field":"hostname","op":"starts_with","value":"win-"}]`, true},
		{"regex", `[{"field":"hostname","op":"regex","value":"^win-dc\\d+$"}]`, true},
		{"public_ip alias", `[{"field":"public_ip","op":"equals","value":"203.0.113.9"}]`, true},
		{"external_ip alias", `[{"field":"external_ip","op":"equals","value":"203.0.113.9"}]`, true},
		{"process_name field", `[{"field":"process_name","op":"contains","value":"lsass"}]`, true},
		{"arch field", `[{"field":"arch","op":"equals","value":"amd64"}]`, true},
		{"domain field", `[{"field":"domain","op":"equals","value":"corp"}]`, true},
		{"username field", `[{"field":"username","op":"equals","value":"administrator"}]`, true},
		{"ip field", `[{"field":"ip","op":"starts_with","value":"10.0."}]`, true},

		// Two conditions are ANDed.
		{"both match", `[{"field":"os","op":"equals","value":"windows"},{"field":"arch","op":"equals","value":"amd64"}]`, true},
		{"one fails", `[{"field":"os","op":"equals","value":"windows"},{"field":"arch","op":"equals","value":"arm64"}]`, false},

		// Fail-closed shapes.
		{"empty", ``, false},
		{"whitespace", `   `, false},
		{"not json", `{`, false},
		{"empty array", `[]`, false},
		{"unknown field", `[{"field":"no_such_field","op":"equals","value":""}]`, false},
		{"unknown operator", `[{"field":"os","op":"matches","value":"windows"}]`, false},
		{"invalid regex", `[{"field":"hostname","op":"regex","value":"["}]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoTagMatch(tc.cond, agent); got != tc.want {
				t.Fatalf("autoTagMatch(%s) = %v, want %v", tc.cond, got, tc.want)
			}
		})
	}
}

// TestAutoTagApplyTagsMatchingAgents drives the sweeper end to end: only the
// matching agents get the tag, and a second run does not duplicate the
// assignment.
func TestAutoTagApplyTagsMatchingAgents(t *testing.T) {
	s := mustTenantServer(t)
	now := time.Now()
	agents := []db.Implant{
		{ID: "match-1", TenantID: 1, Hostname: "DC01", OS: "windows", Status: "online", LastSeen: now},
		{ID: "match-2", TenantID: 1, Hostname: "DC02", OS: "windows", Status: "online", LastSeen: now},
		{ID: "other", TenantID: 1, Hostname: "web01", OS: "linux", Status: "online", LastSeen: now},
	}
	for i := range agents {
		if err := s.db.Create(&agents[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	tag := db.AgentTag{ID: "tag-dc", Name: "domain-controller", Color: "amber"}
	if err := s.db.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	rule := db.AutoTagRule{
		ID: "rule-dc", Name: "dc hosts", Enabled: true, TagID: tag.ID, Priority: 1,
		Condition: `[{"field":"hostname","op":"starts_with","value":"dc"}]`,
	}
	if err := s.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	// GORM's default:true rewrites a zero-value bool, so ensure it is enabled.
	if err := s.db.Model(&db.AutoTagRule{}).Where("id = ?", rule.ID).Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}

	applied := autoTagApplyForTest(t, s)
	if applied != 2 {
		t.Fatalf("first sweep applied %d assignments, want 2", applied)
	}
	var assignments []db.AgentTagAssignment
	if err := s.db.Where("agent_tag_id = ?", tag.ID).Find(&assignments).Error; err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 2 {
		t.Fatalf("assignments = %d, want 2", len(assignments))
	}
	for _, a := range assignments {
		if a.ImplantID != "match-1" && a.ImplantID != "match-2" {
			t.Fatalf("tagged a non-matching agent: %s", a.ImplantID)
		}
	}

	// A second sweep is idempotent.
	if again := autoTagApplyForTest(t, s); again != 0 {
		t.Fatalf("second sweep applied %d assignments, want 0", again)
	}
	var after int64
	s.db.Model(&db.AgentTagAssignment{}).Where("agent_tag_id = ?", tag.ID).Count(&after)
	if after != 2 {
		t.Fatalf("assignments after re-run = %d", after)
	}

	// Disabling the rule stops new assignments.
	if err := s.db.Model(&db.AutoTagRule{}).Where("id = ?", rule.ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	newAgent := db.Implant{ID: "match-3", TenantID: 1, Hostname: "DC03", OS: "windows", Status: "online", LastSeen: now}
	if err := s.db.Create(&newAgent).Error; err != nil {
		t.Fatal(err)
	}
	if applied := autoTagApplyForTest(t, s); applied != 0 {
		t.Fatalf("a disabled rule applied %d assignments", applied)
	}
}

// autoTagApplyForTest runs the sweeper through its handler and returns the
// applied count.
func autoTagApplyForTest(t *testing.T, s *Server) int {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/autotag/apply", nil)
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	s.handleAutoTagApply(c)
	if w.Code != http.StatusOK {
		t.Fatalf("apply = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Applied int `json:"applied"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	return out.Applied
}
