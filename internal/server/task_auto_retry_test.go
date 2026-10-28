package server

import (
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

// TestAutoRetryOnlyReadOnlyTasks pins the boundary that makes automatic retries safe.
//
// A read-only enumeration that failed is worth one more ask. A command that changes state is not: it may have half-succeeded
// before the timeout, and re-issuing it is how one action becomes two. The allowlist is the mechanism, so both directions are
// asserted - and the cap is asserted too, because a task that failed twice is a finding rather than a scheduling problem.
func TestAutoRetryOnlyReadOnlyTasks(t *testing.T) {
	// 1. The classification.
	retryable := []string{"ps", "LS", "files_ls", "whoami", "hostinfo", "netstat", "users"}
	for _, taskType := range retryable {
		if !autoRetryable(taskType) {
			t.Fatalf("%q should be retryable", taskType)
		}
	}
	// Anything that can change the host is excluded, including the ones an operator runs most often.
	mutating := []string{"shell", "powershell", "upload", "download", "persist", "credential_dump", "uac_bypass",
		"lateral_psexec", "screenshot", "run_script", "inject", ""}
	for _, taskType := range mutating {
		if autoRetryable(taskType) {
			t.Fatalf("%q must NOT be retried automatically", taskType)
		}
	}

	// 2. The cap and the write.
	server := mustTenantServer(t)
	if err := server.db.Create(&db.Implant{ID: "a-1", TenantID: 1, Hostname: "web-01", Status: "online"}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	if _, created := server.maybeAutoRetryTask(1, "a-1", "shell", "whoami", "", "op", 0, 1); created {
		t.Fatal("a shell task was retried automatically")
	}
	retryID, created := server.maybeAutoRetryTask(2, "a-1", "ps", "ps", "", "op", 0, 1)
	if !created || retryID == 0 {
		t.Fatalf("a ps task was not retried (created=%v id=%d)", created, retryID)
	}
	var retried db.Task
	if err := server.db.First(&retried, retryID).Error; err != nil {
		t.Fatalf("read the retry: %v", err)
	}
	// The counter is what stops a third attempt, so it must be written and not left at the column default.
	if retried.RetryCount != 1 {
		t.Fatalf("retry_count = %d, want 1", retried.RetryCount)
	}
	if retried.AgentID != "a-1" || retried.Type != "ps" || retried.Status != "pending" {
		t.Fatalf("the retry is not a copy of the original: %+v", retried)
	}

	// 3. A task that has already been retried once is not retried again.
	if _, created := server.maybeAutoRetryTask(3, "a-1", "ps", "ps", "", "op", 1, 1); created {
		t.Fatal("a task was retried twice")
	}

	// 4. The retry is audited, so "this ran twice" is explainable.
	var audited int64
	if err := server.db.Model(&db.AuditLog{}).Where("action = ?", "task_auto_retry").Count(&audited).Error; err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if audited != 1 {
		t.Fatalf("task_auto_retry rows = %d, want 1", audited)
	}
}
