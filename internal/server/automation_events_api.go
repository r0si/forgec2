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
var automationEventCatalogue = []struct {
	Event   EventType
	Answers string
}{
	{EventImplantCheckin, "a beacon checked in"},
	{EventImplantDisconnect, "a beacon went offline"},
	{EventTaskComplete, "a task finished successfully"},
	{EventTaskFail, "a task finished badly"},
	{EventCredentialFound, "a credential was harvested"},
	{EventAlertTriggered, "an alert rule fired"},
	{EventBuildFailed, "a payload build failed"},
	{EventBuildSucceeded, "a payload build finished"},
}

// handleAutomationEvents lists the event types a rule may be written against.
func (s *Server) handleAutomationEvents(c *gin.Context) {
	out := make([]gin.H, 0, len(automationEventCatalogue))
	for _, entry := range automationEventCatalogue {
		out = append(out, gin.H{
			"event_type": string(entry.Event),
			"answers":    entry.Answers,
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
