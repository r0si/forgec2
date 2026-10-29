package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestPreviewDistinguishesUnfirableFromUnmatched covers the difference between two answers that used to read the same.
//
// A preview that says "hint_code equal "x" did not match "" sends the operator to look at the EVENT. When the condition itself is invalid, the
// event is irrelevant - the rule cannot fire whatever arrives - and the preview must say so, or it is a preview of the wrong thing.
func TestPreviewDistinguishesUnfirableFromUnmatched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	// Two rules on the same event: one whose condition is merely unsatisfied, one whose operator does not exist.
	seed := func(id string, conditions []RuleCondition) {
		encoded, _ := json.Marshal(conditions)
		actions, _ := json.Marshal([]RuleAction{{Type: ActionNotify, Params: json.RawMessage(`{"message":"x"}`)}})
		if err := s.db.Create(&db.AutomationRule{
			ID: id, Name: id, Enabled: true, EventType: string(EventBuildFailed),
			Conditions: string(encoded), Actions: string(actions), TenantID: 1,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("merely-unmatched", []RuleCondition{{Field: "hint_code", Operator: "equals", Value: "disk_full"}})
	seed("cannot-fire", []RuleCondition{{Field: "hint_code", Operator: "equal", Value: "toolchain_missing"}})
	s.invalidateAutomationCache()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := `{"event_type":"build.failed","data":{"hint_code":"compile_error"}}`
	c.Request = httptest.NewRequest(http.MethodPost, "/api/automation/preview", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handlePreviewAutomationBatch(c)
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Rules []struct {
				RuleID      string `json:"rule_id"`
				WouldRun    bool   `json:"would_run"`
				FirstReason string `json:"first_reason"`
			} `json:"rules"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(resp.Data.Rules) < 2 {
		t.Fatalf("expected both rules in the preview: %+v", resp.Data.Rules)
	}

	reasonsFor := map[string]string{}
	for _, rule := range resp.Data.Rules {
		reasonsFor[rule.RuleID] = rule.FirstReason
	}

	// The valid-but-unsatisfied rule explains the EVENT: which value arrived and what the rule wanted.
	unmatched := reasonsFor["merely-unmatched"]
	if !strings.Contains(unmatched, "did not match") || !strings.Contains(unmatched, "compile_error") {
		t.Fatalf("the unsatisfied rule does not explain the event: %q", unmatched)
	}
	if strings.Contains(unmatched, "can never match") {
		t.Fatalf("an ordinary mismatch was reported as unfirable: %q", unmatched)
	}

	// The invalid rule explains the RULE, and says the event is beside the point.
	unfirable := reasonsFor["cannot-fire"]
	if !strings.Contains(unfirable, "can never match") || !strings.Contains(unfirable, "unknown operator") {
		t.Fatalf("an unfirable rule was reported as an ordinary mismatch: %q", unfirable)
	}
}

// TestPreviewAcceptsBothPayloadSpellings pins the compatibility that made the preview disagree with production.
//
// A live build.failed event carries its payload as a bare key, so a rule written as `hint_code` fires in production. The preview flattened
// the body's data to `data.hint_code` only, so the SAME rule previewed as "would not fire" - a preview that teaches the operator to rewrite
// a rule that was already correct. Both spellings must resolve, and they must resolve to the same verdict.
func TestPreviewAcceptsBothPayloadSpellings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	seed := func(id, field string) {
		conditions, _ := json.Marshal([]RuleCondition{{Field: field, Operator: "equals", Value: "compile_error"}})
		actions, _ := json.Marshal([]RuleAction{{Type: ActionNotify, Params: json.RawMessage(`{"message":"x"}`)}})
		if err := s.db.Create(&db.AutomationRule{
			ID: id, Name: id, Enabled: true, EventType: string(EventBuildFailed),
			Conditions: string(conditions), Actions: string(actions), TenantID: 1,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("bare-key", "hint_code")
	seed("dotted-key", "data.hint_code")
	s.invalidateAutomationCache()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := `{"event_type":"build.failed","data":{"hint_code":"compile_error"}}`
	c.Request = httptest.NewRequest(http.MethodPost, "/api/automation/preview", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handlePreviewAutomationBatch(c)
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Rules []struct {
				RuleID   string `json:"rule_id"`
				WouldRun bool   `json:"would_run"`
			} `json:"rules"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	verdicts := map[string]bool{}
	for _, rule := range resp.Data.Rules {
		verdicts[rule.RuleID] = rule.WouldRun
	}
	if !verdicts["bare-key"] {
		t.Fatalf("the bare-key rule previewed as not firing, which contradicts the live path: %+v", verdicts)
	}
	if !verdicts["dotted-key"] {
		t.Fatalf("the dotted-key rule stopped working: %+v", verdicts)
	}
}
