package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/playbook"
	"github.com/gin-gonic/gin"
)

// Playbook runs: storage and the read-only view.
//
// The transitions live in internal/playbook and are exhaustive there; this file only persists what that package produced and renders
// it. Two rules keep the two apart: the stored JSON is always the serialised playbook.Run (so a resumed run is the value the tests
// cover), and the State column is written from playbook.Run.State() rather than computed a second time here.

// playbookListLimit bounds the list: a run list is a menu, not an archive.
const playbookListLimit = 100

// savePlaybookRun persists one run, creating or updating by id.
func (s *Server) savePlaybookRun(c *gin.Context, run playbook.Run, createdBy string) (*db.PlaybookRun, error) {
	encoded, err := json.Marshal(run)
	if err != nil {
		return nil, fmt.Errorf("marshal playbook run: %w", err)
	}
	tenantID := s.currentTenantID(c)

	var record db.PlaybookRun
	found := false
	if run.ID != "" {
		if id, convErr := strconv.ParseUint(run.ID, 10, 32); convErr == nil {
			if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.PlaybookRun{}), c).
				Where("id = ?", id).First(&record).Error; err == nil {
				found = true
			}
		}
	}
	if !found {
		record = db.PlaybookRun{TenantID: tenantID, CreatedBy: createdBy}
	}
	record.Name = run.Name
	record.State = run.State()
	record.StepsJSON = string(encoded)
	if err := s.db.WithContext(s.ctx).Save(&record).Error; err != nil {
		return nil, fmt.Errorf("save playbook run: %w", err)
	}
	return &record, nil
}

// loadPlaybookRun reads one run and decodes its steps.
func (s *Server) loadPlaybookRun(c *gin.Context, id uint) (*db.PlaybookRun, playbook.Run, error) {
	var record db.PlaybookRun
	if err := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.PlaybookRun{}), c).
		Where("id = ?", id).First(&record).Error; err != nil {
		return nil, playbook.Run{}, err
	}
	var run playbook.Run
	if err := json.Unmarshal([]byte(record.StepsJSON), &run); err != nil {
		return nil, playbook.Run{}, fmt.Errorf("decode playbook run: %w", err)
	}
	return &record, run, nil
}

// handleListPlaybookRuns renders the run list.
func (s *Server) handleListPlaybookRuns(c *gin.Context) {
	// attentionOnly narrows the list to runs waiting for a human; it filters AFTER decoding the steps, because "blocked" is a property
	// of a step, not of the row.
	attentionOnly := strings.TrimSpace(c.Query("attention")) == "1" || strings.EqualFold(strings.TrimSpace(c.Query("attention")), "true")
	query := s.tenantScope(s.db.WithContext(s.ctx).Model(&db.PlaybookRun{}), c)
	if state := strings.TrimSpace(c.Query("state")); state != "" {
		query = query.Where("state = ?", state)
	}
	var records []db.PlaybookRun
	if err := query.Order("updated_at desc").Limit(playbookListLimit).Find(&records).Error; err != nil {
		handleQueryError(c, err, "Failed to list playbook runs")
		return
	}

	type runSummary struct {
		ID        uint   `json:"id"`
		Name      string `json:"name"`
		State     string `json:"state"`
		CreatedBy string `json:"created_by,omitempty"`
		UpdatedAt string `json:"updated_at"`
		// Settled/Total come from the stored steps, so the list can show "2/4" without a reader opening the run.
		Settled int `json:"settled"`
		Total   int `json:"total"`
		// BlockedSteps and NextStep are the "what needs me now" columns. A list of runs that only says "blocked" makes the reader open
		// each one to find out whether the blocker is a person or a dependency - and those need different people.
		BlockedSteps []string `json:"blocked_steps,omitempty"`
		NextStep     string   `json:"next_step,omitempty"`
	}
	out := make([]runSummary, 0, len(records))
	for _, record := range records {
		var run playbook.Run
		settled, total := 0, 0
		if err := json.Unmarshal([]byte(record.StepsJSON), &run); err == nil {
			settled, total = run.Progress()
		}
		summary := runSummary{
			ID: record.ID, Name: record.Name, State: record.State, CreatedBy: record.CreatedBy,
			UpdatedAt: record.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"), Settled: settled, Total: total,
		}
		for _, step := range run.Steps {
			if step.Status == playbook.StatusBlocked {
				summary.BlockedSteps = append(summary.BlockedSteps, step.ID)
			}
		}
		if ready := run.Ready(); len(ready) > 0 {
			summary.NextStep = ready[0].ID
		}
		// ?attention=1 keeps only the runs a PERSON has to act on. The same information is in blocked_steps, but a caller that has to
		// filter it itself will usually not - and "which runs need me" is the question a queue of approvals is answered by.
		if attentionOnly && len(summary.BlockedSteps) == 0 {
			continue
		}
		out = append(out, summary)
	}
	respond(c, gin.H{"runs": out, "count": len(out)})
}

// handleGetPlaybookRun renders one run, including what may run next.
//
// The ready list is computed here rather than stored: it is a function of the current state, and a stored copy would be a second
// answer that drifts the first time a transition forgets to update it.
func (s *Server) handleGetPlaybookRun(c *gin.Context) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 32)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid run id")
		return
	}
	record, run, err := s.loadPlaybookRun(c, uint(id))
	if err != nil {
		respondError(c, http.StatusNotFound, "no such playbook run")
		return
	}
	settled, total := run.Progress()
	respond(c, gin.H{
		"id":      record.ID,
		"name":    record.Name,
		"state":   run.State(),
		"steps":   run.Steps,
		"ready":   run.Ready(),
		"settled": settled,
		"total":   total,
		"note": "ready lists the steps that may start now; an approval step is absent until it is approved, " +
			"because waiting for a person is a state, not progress",
	})
}
