// Package playbook is the state machine behind structured playbooks: a list of steps, their dependencies, and the rules that stop
// a run from claiming progress it did not make.
//
// Why a state machine and not a script: a playbook is resumed across shifts, approvals and failures, and the interesting states are
// the ones a linear script cannot express - "this step is waiting for a human", "this branch was skipped because its precondition
// failed", "this step ran twice". The rules below exist because each of those, done wrong, produces a report that reads like success.
//
// The package is pure: no database, no clock, no network. Storage and execution belong to the layers above, and keeping this one
// pure is what makes the transitions testable exhaustively instead of by inspection.
package playbook

import (
	"fmt"
	"sort"
	"strings"
)

// Status is where one step stands.
type Status string

const (
	// StatusPending has not started and may become runnable once its dependencies succeed.
	StatusPending Status = "pending"
	// StatusBlocked is waiting for a human. It keeps its place in the run: a blocker is not a failure.
	StatusBlocked Status = "blocked"
	// StatusRunning has been dispatched.
	StatusRunning Status = "running"
	// StatusSucceeded finished and satisfied its dependents.
	StatusSucceeded Status = "succeeded"
	// StatusFailed finished badly. Dependents are skipped, never silently treated as satisfied.
	StatusFailed Status = "failed"
	// StatusSkipped was not run because a dependency failed or was itself skipped.
	StatusSkipped Status = "skipped"
)

// Step is one unit of work.
type Step struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	// Needs names the steps that must SUCCEED before this one may start. Empty means runnable from the start.
	Needs []string `json:"needs,omitempty"`
	// Approval marks a step that changes something a human must authorise. It starts Blocked rather than Pending, so "waiting for
	// a person" is a state a run can report, not an absence of progress.
	Approval bool   `json:"approval,omitempty"`
	Status   Status `json:"status"`
	// Attempts counts dispatches, so a step that has run twice says so.
	Attempts int `json:"attempts,omitempty"`
	// LastError carries the reason a step failed, verbatim enough to act on.
	LastError string `json:"last_error,omitempty"`
	// SkippedBecause names the dependency that stopped this step, so a skipped branch is explainable.
	SkippedBecause string `json:"skipped_because,omitempty"`
}

// Run is one playbook execution.
type Run struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Steps []Step `json:"steps"`
}

// TransitionError explains why a transition was refused.
//
// Typed errors rather than silent no-ops: a caller that asks to start a step whose dependency failed has a bug, and a state machine
// that quietly ignores it turns that bug into a playbook that looks finished.
type TransitionError struct {
	StepID string
	Reason string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("step %q: %s", e.StepID, e.Reason)
}

// Normalise validates the definition and fills in the initial status of every step.
//
// It refuses cycles, unknown dependencies and duplicate ids - all of which produce a run that can never finish, or one where a step
// silently never runs because nobody noticed it was unreachable.
func (r *Run) Normalise() error {
	if len(r.Steps) == 0 {
		return fmt.Errorf("a playbook needs at least one step")
	}
	byID := make(map[string]int, len(r.Steps))
	for i := range r.Steps {
		step := &r.Steps[i]
		if strings.TrimSpace(step.ID) == "" {
			return fmt.Errorf("every step needs an id")
		}
		if _, exists := byID[step.ID]; exists {
			return fmt.Errorf("duplicate step id %q", step.ID)
		}
		byID[step.ID] = i
	}
	for i := range r.Steps {
		step := &r.Steps[i]
		// Initial status: an approval step starts blocked, everything else pending. An already-terminal status is preserved, so a
		// resumed run keeps what it knew.
		if step.Status == "" {
			if step.Approval {
				step.Status = StatusBlocked
			} else {
				step.Status = StatusPending
			}
		}
		for _, need := range step.Needs {
			if _, exists := byID[need]; !exists {
				return fmt.Errorf("step %q needs unknown step %q", step.ID, need)
			}
			if need == step.ID {
				return &TransitionError{StepID: step.ID, Reason: "a step cannot depend on itself"}
			}
		}
	}
	if cycle := r.findCycle(); cycle != "" {
		return fmt.Errorf("dependency cycle through %s", cycle)
	}
	return nil
}

// findCycle returns the id of a step on a cycle, or "" when the graph is acyclic.
func (r *Run) findCycle() string {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(r.Steps))
	byID := make(map[string]*Step, len(r.Steps))
	for i := range r.Steps {
		byID[r.Steps[i].ID] = &r.Steps[i]
	}
	var visit func(id string) string
	visit = func(id string) string {
		switch state[id] {
		case visiting:
			return id
		case done:
			return ""
		}
		state[id] = visiting
		step := byID[id]
		if step != nil {
			for _, need := range step.Needs {
				if cycle := visit(need); cycle != "" {
					return cycle
				}
			}
		}
		state[id] = done
		return ""
	}
	// Sorted so a cycle is reported at the same step on every run.
	ids := make([]string, 0, len(r.Steps))
	for i := range r.Steps {
		ids = append(ids, r.Steps[i].ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if cycle := visit(id); cycle != "" {
			return cycle
		}
	}
	return ""
}

// step returns the named step.
func (r *Run) step(id string) (*Step, error) {
	for i := range r.Steps {
		if r.Steps[i].ID == id {
			return &r.Steps[i], nil
		}
	}
	return nil, &TransitionError{StepID: id, Reason: "no such step"}
}

// dependenciesSettled reports whether every dependency succeeded, and the id of the first one that did not.
func (r *Run) dependenciesSettled(step *Step) (bool, string) {
	for _, need := range step.Needs {
		dependency, err := r.step(need)
		if err != nil {
			return false, need
		}
		if dependency.Status != StatusSucceeded {
			return false, need
		}
	}
	return true, ""
}

// Ready lists the steps that may start now, in definition order.
//
// An approval step is never Ready: it is Blocked until Approve is called, because "waiting for a human" and "waiting for a
// dependency" need different answers from the operator.
func (r *Run) Ready() []Step {
	ready := make([]Step, 0, len(r.Steps))
	for i := range r.Steps {
		step := &r.Steps[i]
		if step.Status != StatusPending {
			continue
		}
		if settled, _ := r.dependenciesSettled(step); !settled {
			continue
		}
		ready = append(ready, *step)
	}
	return ready
}

// Approve releases a step that was waiting for a human.
func (r *Run) Approve(id string) error {
	step, err := r.step(id)
	if err != nil {
		return err
	}
	if !step.Approval {
		return &TransitionError{StepID: id, Reason: "this step does not require approval"}
	}
	if step.Status != StatusBlocked {
		return &TransitionError{StepID: id, Reason: fmt.Sprintf("cannot approve a step that is %s", step.Status)}
	}
	if settled, blocking := r.dependenciesSettled(step); !settled {
		// Approval is not a substitute for a dependency: approving early must not jump the queue.
		return &TransitionError{StepID: id, Reason: fmt.Sprintf("dependency %q has not succeeded", blocking)}
	}
	step.Status = StatusPending
	return nil
}

// Start marks a step running, counting the attempt.
func (r *Run) Start(id string) error {
	step, err := r.step(id)
	if err != nil {
		return err
	}
	if step.Status != StatusPending {
		return &TransitionError{StepID: id, Reason: fmt.Sprintf("cannot start a step that is %s", step.Status)}
	}
	if settled, blocking := r.dependenciesSettled(step); !settled {
		return &TransitionError{StepID: id, Reason: fmt.Sprintf("dependency %q has not succeeded", blocking)}
	}
	step.Status = StatusRunning
	step.Attempts++
	return nil
}

// Succeed marks a running step succeeded.
func (r *Run) Succeed(id string) error {
	step, err := r.step(id)
	if err != nil {
		return err
	}
	if step.Status != StatusRunning {
		return &TransitionError{StepID: id, Reason: fmt.Sprintf("cannot succeed a step that is %s", step.Status)}
	}
	step.Status = StatusSucceeded
	step.LastError = ""
	return nil
}

// Fail marks a running step failed and skips everything downstream of it.
//
// The cascade is part of the transition, not a separate tidy-up: a run that leaves dependents Pending after a failure reports work
// as still possible when it is not.
func (r *Run) Fail(id, reason string) error {
	step, err := r.step(id)
	if err != nil {
		return err
	}
	if step.Status != StatusRunning {
		return &TransitionError{StepID: id, Reason: fmt.Sprintf("cannot fail a step that is %s", step.Status)}
	}
	step.Status = StatusFailed
	step.LastError = reason
	r.skipDependents(id)
	return nil
}

// Skip marks a pending or blocked step skipped because a named dependency did not succeed.
func (r *Run) Skip(id, because string) error {
	step, err := r.step(id)
	if err != nil {
		return err
	}
	if step.Status != StatusPending && step.Status != StatusBlocked {
		return &TransitionError{StepID: id, Reason: fmt.Sprintf("cannot skip a step that is %s", step.Status)}
	}
	step.Status = StatusSkipped
	step.SkippedBecause = because
	r.skipDependents(id)
	return nil
}

// skipDependents walks the downstream steps and skips those that can no longer run.
func (r *Run) skipDependents(id string) {
	for {
		changed := false
		for i := range r.Steps {
			step := &r.Steps[i]
			if step.Status != StatusPending && step.Status != StatusBlocked {
				continue
			}
			settled, blocking := r.dependenciesSettled(step)
			if settled {
				continue
			}
			// Only reachable-from-a-failure dependencies cause a skip: an unsettled dependency that is merely pending leaves the
			// step pending, because it may still run.
			if blocker, err := r.step(blocking); err == nil {
				if blocker.Status == StatusFailed || blocker.Status == StatusSkipped {
					step.Status = StatusSkipped
					step.SkippedBecause = blocking
					changed = true
				}
			}
		}
		if !changed {
			return
		}
	}
}

// Progress counts settled steps: the total reported to a reader must never include work still possible.
func (r *Run) Progress() (settled, total int) {
	total = len(r.Steps)
	for i := range r.Steps {
		switch r.Steps[i].Status {
		case StatusSucceeded, StatusFailed, StatusSkipped:
			settled++
		}
	}
	return settled, total
}

// State summarises the run for a caller that wants one word.
//
// Order matters: a failed step makes the run failed even if other work could continue, because "failed" is the answer a reviewer
// needs first; "blocked" beats "running" for the same reason, since a run waiting on a human is not making progress.
func (r *Run) State() string {
	failed, blocked, running, pending := false, false, false, false
	for i := range r.Steps {
		switch r.Steps[i].Status {
		case StatusFailed:
			failed = true
		case StatusBlocked:
			blocked = true
		case StatusRunning:
			running = true
		case StatusPending:
			pending = true
		}
	}
	switch {
	case failed:
		return "failed"
	case blocked:
		return "blocked"
	case running:
		return "running"
	case pending:
		return "partial"
	default:
		return "complete"
	}
}
