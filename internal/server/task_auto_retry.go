package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"strings"
)

// Automatic retry of READ-ONLY tasks.
//
// A ps or a directory listing that fails because the beacon timed out is worth one more ask: the host is there, the answer is
// the same whenever it arrives, and an operator will otherwise press the button again by hand. That is the whole scope.
//
// **Only enumerations, never anything that changes state.** A `shell` command, a persistence install or a credential dump
// may have half-succeeded before the timeout, and re-issuing it is how one action becomes two. The allowlist below is the
// mechanism that keeps that true, and the test pins both directions: a retryable type is retried, a mutating one is not.
//
// One retry, not a policy: a task that failed twice is a finding, not a scheduling problem.

// autoRetryMaxAttempts bounds how many times a failed task is re-issued.
const autoRetryMaxAttempts = 1

// autoRetryableTaskTypes are the read-only task types a failure may be retried for.
//
// Deliberately short and lower-cased: these are the types whose repetition cannot change the host's state.
var autoRetryableTaskTypes = map[string]bool{
	"ps":           true,
	"ls":           true,
	"files_ls":     true,
	"whoami":       true,
	"hostinfo":     true,
	"sysinfo":      true,
	"netstat":      true,
	"arp":          true,
	"env":          true,
	"drives":       true,
	"users":        true,
	"net_user":     true,
	"process_list": true,
}

// autoRetryable reports whether a task type may be re-issued after a failure.
func autoRetryable(taskType string) bool {
	return autoRetryableTaskTypes[strings.ToLower(strings.TrimSpace(taskType))]
}

// maybeAutoRetryTask re-issues one read-only task after a failure.
//
// Returns the new task when one was created. The caller is the result path, so failures here must never affect the result
// that was just stored: everything is logged and swallowed.
func (s *Server) maybeAutoRetryTask(taskID uint, agentID, taskType, command, shell, createdBy string, retryCount int, tenantID uint) (uint, bool) {
	if !autoRetryable(taskType) {
		return 0, false
	}
	if retryCount >= autoRetryMaxAttempts {
		// Already retried once: a second failure is a finding, and re-issuing it again would hide that.
		return 0, false
	}

	retry := &dbTaskRetry{
		AgentID:    agentID,
		Type:       taskType,
		Command:    command,
		Shell:      shell,
		Status:     "pending",
		CreatedBy:  createdBy,
		RetryCount: retryCount + 1,
		TenantID:   tenantID,
	}
	id, err := s.createRetryTask(retry)
	if err != nil {
		return 0, false
	}

	recordAutoRetry()
	s.LogAuditRecord(nil, "task_auto_retry", "task", fmt.Sprintf("%d", id),
		fmt.Sprintf("re-issued failed read-only task %q (attempt %d) for agent %s", taskType, retryCount+1, agentID),
		true, nil)
	return id, true
}

// dbTaskRetry is the shape a retry needs; it mirrors the columns the creation helper writes.
type dbTaskRetry struct {
	AgentID    string
	Type       string
	Command    string
	Shell      string
	Status     string
	CreatedBy  string
	RetryCount int
	TenantID   uint
}

// createRetryTask persists and dispatches one retry.
func (s *Server) createRetryTask(retry *dbTaskRetry) (uint, error) {
	if retry == nil {
		return 0, fmt.Errorf("nothing to retry")
	}
	task, err := s.createSystemTask(retry.AgentID, retry.Type, retry.Command, retry.Shell, retry.Status, retry.CreatedBy)
	if err != nil {
		return 0, err
	}
	if task == nil {
		return 0, fmt.Errorf("the retry task was not created")
	}
	// The counter is written explicitly: the column's default would otherwise mask it on the insert.
	if err := s.db.WithContext(s.ctx).Model(&db.Task{}).Where("id = ?", task.ID).
		Update("retry_count", retry.RetryCount).Error; err != nil {
		return task.ID, nil // the task exists; a miscounted attempt is not worth failing the result path over
	}
	return task.ID, nil
}

// autoRetryCount and autoRetryLastAt track automatic retries since process start.
//
// A readiness probe cannot count them from the database without a query per poll, and the number that matters operationally is
// "are we retrying a lot right now" - which the in-memory rate answers. It resets with the process, like the delivery counters.
var (
	autoRetryMu     sync.Mutex
	autoRetryCount  int
	autoRetryLastAt time.Time
)

// recordAutoRetry bumps the counter.
func recordAutoRetry() {
	autoRetryMu.Lock()
	defer autoRetryMu.Unlock()
	autoRetryCount++
	autoRetryLastAt = time.Now()
}

// autoRetrySnapshot returns the counters for the readiness probe.
func autoRetrySnapshot() (int, time.Time) {
	autoRetryMu.Lock()
	defer autoRetryMu.Unlock()
	return autoRetryCount, autoRetryLastAt
}

// resetAutoRetryCounter clears the counters. Tests use it.
func resetAutoRetryCounter() {
	autoRetryMu.Lock()
	defer autoRetryMu.Unlock()
	autoRetryCount = 0
	autoRetryLastAt = time.Time{}
}
