package server

import (
	"github.com/gin-gonic/gin"
)

// The event types a rule may listen to, published so a console does not hardcode them.
//
// The list is a literal slice rather than a reflection over constants on purpose: Go cannot enumerate its own const block, and a
// hand-written list that a test checks against both the registry and docs/AUTOMATION_RULES.md is honest about being a list - it just
// cannot drift silently.

// automationEventCatalogue is the published set, in the order a menu should show them.
//
// Fields is what makes this usable rather than merely correct: a console can offer "write a condition on hint_code" without a reader
// looking it up, and a field the event no longer carries is a condition that can never match. The documentation carries the same list in
// prose and a test requires the two to agree.
var automationEventCatalogue = []struct {
	Event   EventType
	Answers string
	// Fields are the payload keys a condition may reference, in addition to agent.hostname, which every event resolves.
	Fields []string
}{
	{EventImplantCheckin, "a beacon checked in", nil},
	{EventImplantDisconnect, "a beacon went offline", nil},
	{EventTaskComplete, "a task finished successfully", nil},
	{EventTaskFail, "a task finished badly", nil},
	{EventCredentialFound, "a credential was harvested", nil},
	{EventAlertTriggered, "an alert rule fired", nil},
	{EventBuildFailed, "a payload build failed",
		[]string{"build_id", "platform", "format", "filename", "error", "hint_code"}},
	{EventBuildSucceeded, "a payload build finished",
		[]string{"build_id", "platform", "format", "filename"}},
}

// handleAutomationEvents lists the event types a rule may be written against.
func (s *Server) handleAutomationEvents(c *gin.Context) {
	out := make([]gin.H, 0, len(automationEventCatalogue))
	for _, entry := range automationEventCatalogue {
		fields := entry.Fields
		if fields == nil {
			// Not empty: an event with no payload fields still resolves agent.hostname, and a consumer that receives "" cannot tell that
			// from "unknown".
			fields = []string{}
		}
		out = append(out, gin.H{
			"event_type": string(entry.Event),
			"answers":    entry.Answers,
			"fields":     fields,
		})
	}
	respond(c, gin.H{
		"events": out,
		"count":  len(out),
		"note": "payload fields a condition may use are documented per event in docs/AUTOMATION_RULES.md; " +
			"condition operators are contains, equals and regex, and anything else is refused at save time",
	})
}

// automationEventTypes returns the catalogue as plain strings, for tests and for other handlers.
func automationEventTypes() []string {
	out := make([]string, 0, len(automationEventCatalogue))
	for _, entry := range automationEventCatalogue {
		out = append(out, string(entry.Event))
	}
	return out
}
