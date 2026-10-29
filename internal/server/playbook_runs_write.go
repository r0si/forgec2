package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/playbook"
	"github.com/gin-gonic/gin"
)

// The write side of playbook runs.
//
// What this endpoint does NOT do is dispatch anything: a transition records that a step started, succeeded or failed, and the reason.
// That is a checklist an operator keeps in step with reality, and it is deliberately the safe half - a playbook that can dispatch a
// task from a JSON body is a different security decision, and it belongs behind the same approval chain the AI copilot uses rather
// than in a first slice.
//
// Every transition is validated by internal/playbook, never here: a second implementation of "may this step start" is exactly the
// kind of duplicate that drifts. This file loads, calls the machine, and stores the result.

// playbookTransitionRequest is one state change.
type playbookTransitionRequest struct {
	Step   string `json:"step"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// handleCreatePlaybookRun accepts a definition and stores a normalised run.
func (s *Server) handleCreatePlaybookRun(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var body struct {
		Name  string          `json:"name"`
		Steps []playbook.Step `json:"steps"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, `invalid body: expected {"name":"...","steps":[{"id":"...","needs":["..."]}]}`)
		return
	}
	run := playbook.Run{Name: strings.TrimSpace(body.Name), Steps: body.Steps}
	// Normalise is where a cycle, a duplicate id or a dangling dependency is refused: a playbook that can never finish must not be
	// storable, because every later report would inherit the impossibility.
	if err := run.Normalise(); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "playbook"))
		return
	}

	record, err := s.savePlaybookRun(c, run, currentUsername(c))
	if err != nil {
		handleQueryError(c, err, "Failed to save the playbook run")
		return
	}
	run.ID = strconv.FormatUint(uint64(record.ID), 10)
	if _, err := s.savePlaybookRun(c, run, currentUsername(c)); err != nil {
		handleQueryError(c, err, "Failed to save the playbook run")
		return
	}

	s.LogAuditRecord(c, "playbook_run_create", "playbook", strconv.FormatUint(uint64(record.ID), 10),
		fmt.Sprintf("created playbook %q with %d step(s)", run.Name, len(run.Steps)), true, nil)

	settled, total := run.Progress()
	respond(c, gin.H{
		"id": record.ID, "name": run.Name, "state": run.State(),
		"ready": run.Ready(), "settled": settled, "total": total,
	})
}

// handleTransitionPlaybookRun applies one transition and stores the run.
func (s *Server) handleTransitionPlaybookRun(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 32)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid run id")
		return
	}
	var body playbookTransitionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, `invalid body: expected {"step":"...","action":"start|succeed|fail|skip|approve"}`)
		return
	}
	step := strings.TrimSpace(body.Step)
	if step == "" {
		respondError(c, http.StatusBadRequest, "step is required")
		return
	}

	record, run, err := s.loadPlaybookRun(c, uint(id))
	if err != nil {
		respondError(c, http.StatusNotFound, "no such playbook run")
		return
	}

	// The machine decides; this switch only names the methods it already has.
	var transitionErr error
	switch strings.ToLower(strings.TrimSpace(body.Action)) {
	case "start":
		transitionErr = run.Start(step)
	case "succeed":
		transitionErr = run.Succeed(step)
	case "fail":
		transitionErr = run.Fail(step, body.Reason)
	case "skip":
		transitionErr = run.Skip(step, body.Reason)
	case "approve":
		transitionErr = run.Approve(step)
	default:
		// A refusal, not a default: silently guessing an action would apply a transition nobody asked for.
		respondError(c, http.StatusBadRequest, `invalid action (expected start, succeed, fail, skip or approve)`)
		return
	}
	if transitionErr != nil {
		// 409: the request is well formed and the state refuses it. A 400 would blame the caller's syntax for a state problem, and a
		// 200 would tell them the transition happened.
		respondError(c, http.StatusConflict, sanitizeError(transitionErr, "playbook"))
		return
	}

	run.ID = strconv.FormatUint(uint64(record.ID), 10)
	if _, err := s.savePlaybookRun(c, run, currentUsername(c)); err != nil {
		handleQueryError(c, err, "Failed to save the playbook run")
		return
	}

	s.LogAuditRecord(c, "playbook_run_transition", "playbook", strconv.FormatUint(uint64(record.ID), 10),
		fmt.Sprintf("step %q: %s%s", step, body.Action, auditReasonSuffix(body.Reason)), true, nil)

	settled, total := run.Progress()
	respond(c, gin.H{
		"id": record.ID, "name": run.Name, "state": run.State(),
		"steps": run.Steps, "ready": run.Ready(), "settled": settled, "total": total,
		"note": "recorded only: this endpoint does not dispatch tasks - the playbook tracks what an operator did",
	})
}

// auditReasonSuffix keeps the audit detail readable when a reason exists.
func auditReasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return " (" + reason + ")"
}

// currentUsername is the operator name for created_by, tolerant of the context shapes used across handlers.
func currentUsername(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if name := strings.TrimSpace(c.GetString("user")); name != "" {
		return name
	}
	return ""
}
