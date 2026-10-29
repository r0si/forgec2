package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestAutomationEventCatalogueMatchesEverything ties three surfaces to one list.
//
// The catalogue exists so a console can build its event dropdown without hardcoding strings; docs/AUTOMATION_RULES.md tells an operator
// what the fields mean. Two consumers, one registry - so the test checks the catalogue against the registry constants AND against the
// documentation, and the endpoint against the catalogue. Without that, one of the three drifts and the console offers an event no rule
// can receive, or the page documents one that no longer fires.
func TestAutomationEventCatalogueMatchesEverything(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/automation/events", nil)
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	c.Set(middleware.TenantIDContextKey, uint(1))
	s.handleAutomationEvents(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Count  int `json:"count"`
			Events []struct {
				EventType string `json:"event_type"`
				Answers   string `json:"answers"`
			} `json:"events"`
			Note string `json:"note"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}

	// 1. The endpoint publishes exactly the catalogue, with no empty rows.
	if resp.Data.Count != len(automationEventCatalogue) || len(resp.Data.Events) != len(automationEventCatalogue) {
		t.Fatalf("count = %d / %d events, want %d", resp.Data.Count, len(resp.Data.Events), len(automationEventCatalogue))
	}
	served := make([]string, 0, len(resp.Data.Events))
	for _, event := range resp.Data.Events {
		if event.EventType == "" || event.Answers == "" {
			t.Fatalf("an event row is incomplete: %+v", event)
		}
		served = append(served, event.EventType)
	}
	sort.Strings(served)

	// 2. Every registry constant that a rule may listen to is present, and nothing invented is.
	registry := []string{
		string(EventImplantCheckin), string(EventImplantDisconnect), string(EventTaskComplete),
		string(EventTaskFail), string(EventCredentialFound), string(EventAlertTriggered),
		string(EventBuildFailed), string(EventBuildSucceeded),
	}
	sort.Strings(registry)
	if strings.Join(served, ",") != strings.Join(registry, ",") {
		t.Fatalf("catalogue = %v, registry = %v", served, registry)
	}

	// 3. And the documentation lists every one of them, so the three surfaces agree.
	raw, err := os.ReadFile("../../docs/AUTOMATION_RULES.md")
	if err != nil {
		t.Fatalf("the automation page is missing: %v", err)
	}
	for _, eventType := range served {
		if !strings.Contains(string(raw), "`"+eventType+"`") {
			t.Fatalf("docs/AUTOMATION_RULES.md does not list %q", eventType)
		}
	}

	// 4. The note points at the two things a rule author needs next, rather than repeating them badly.
	if !strings.Contains(resp.Data.Note, "AUTOMATION_RULES.md") || !strings.Contains(resp.Data.Note, "contains, equals and regex") {
		t.Fatalf("the note is not actionable: %q", resp.Data.Note)
	}
}
