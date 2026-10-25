package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func TestApprovalTaskNotClaimedUntilApproved(t *testing.T) {
	s := newTasksTestServer(t)
	seedAgent(t, s, "agent-approve")
	task := seedTask(t, s, "agent-approve", "shell", "whoami", TaskStatusPendingApproval)

	if claimed := s.fetchPendingTasks("agent-approve"); len(claimed) != 0 {
		t.Fatalf("pending_approval task must not be claimed by beacon, got %d", len(claimed))
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/tasks/1/approve", nil)
	c.Params = gin.Params{{Key: "taskId", Value: "1"}}
	c.Set("user_role", "admin")

	s.handleApproveTask(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}

	var stored db.Task
	if err := s.db.First(&stored, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.Status != "pending" {
		t.Fatalf("expected status pending after approve, got %q", stored.Status)
	}

	claimed := s.fetchPendingTasks("agent-approve")
	if len(claimed) != 1 {
		t.Fatalf("expected approved task to be claimable, got %d", len(claimed))
	}
}

func TestApproveNonPendingTaskRejected(t *testing.T) {
	s := newTasksTestServer(t)
	seedAgent(t, s, "agent-approve")
	seedTask(t, s, "agent-approve", "shell", "whoami", "completed")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/tasks/1/approve", nil)
	c.Params = gin.Params{{Key: "taskId", Value: "1"}}
	c.Set("user_role", "admin")

	s.handleApproveTask(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d; body=%s", w.Code, w.Body.String())
	}
}

func TestRejectTask(t *testing.T) {
	s := newTasksTestServer(t)
	seedAgent(t, s, "agent-reject")
	task := seedTask(t, s, "agent-reject", "shell", "del /f important.txt", TaskStatusPendingApproval)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/tasks/1/reject", nil)
	c.Params = gin.Params{{Key: "taskId", Value: "1"}}
	c.Set("user_role", "admin")

	s.handleRejectTask(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}

	var stored db.Task
	if err := s.db.First(&stored, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.Status != "cancelled" {
		t.Fatalf("expected status cancelled after reject, got %q", stored.Status)
	}

	if claimed := s.fetchPendingTasks("agent-reject"); len(claimed) != 0 {
		t.Fatalf("rejected task must not be claimable, got %d", len(claimed))
	}
}

func TestApproveTaskResponseShape(t *testing.T) {
	s := newTasksTestServer(t)
	seedAgent(t, s, "agent-approve")
	seedTask(t, s, "agent-approve", "shell", "whoami", TaskStatusPendingApproval)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/tasks/1/approve", nil)
	c.Params = gin.Params{{Key: "taskId", Value: "1"}}
	c.Set("user_role", "admin")

	s.handleApproveTask(c)

	var resp struct {
		Message string `json:"message"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Message == "" {
		t.Fatal("expected non-empty message")
	}
}

func TestApproveSelfCreatedTaskRejected(t *testing.T) {
	s := newTasksTestServer(t)
	seedAgent(t, s, "agent-approve")
	task := seedTask(t, s, "agent-approve", "shell", "whoami", TaskStatusPendingApproval)
	s.db.Model(&task).Update("created_by", "alice")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/tasks/1/approve", nil)
	c.Params = gin.Params{{Key: "taskId", Value: "1"}}
	c.Set("user_role", "admin")
	c.Set("user", "alice")

	s.handleApproveTask(c)

	// Two-man rule: the creator may not approve their own task.
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for self-approval, got %d; body=%s", w.Code, w.Body.String())
	}
	var stored db.Task
	if err := s.db.First(&stored, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.Status != TaskStatusPendingApproval {
		t.Fatalf("self-approval must not promote task, got status %q", stored.Status)
	}
}

func TestApproveRecordsSecondOperator(t *testing.T) {
	s := newTasksTestServer(t)
	seedAgent(t, s, "agent-approve")
	task := seedTask(t, s, "agent-approve", "shell", "whoami", TaskStatusPendingApproval)
	s.db.Model(&task).Update("created_by", "alice")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/tasks/1/approve", nil)
	c.Params = gin.Params{{Key: "taskId", Value: "1"}}
	c.Set("user_role", "admin")
	c.Set("user", "bob")

	s.handleApproveTask(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for second-operator approval, got %d; body=%s", w.Code, w.Body.String())
	}
	var stored db.Task
	if err := s.db.First(&stored, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.Status != "pending" {
		t.Fatalf("expected pending, got %q", stored.Status)
	}
	if stored.ApprovedBy != "bob" {
		t.Fatalf("expected approved_by=bob, got %q", stored.ApprovedBy)
	}
	if stored.ApprovedAt == nil {
		t.Fatal("expected approved_at to be set")
	}
}

func TestDangerousTaskWithApprovalEnabledIsDormant(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	s.cfg.Security.RequireApproval = true

	task, err := s.createTask("agent-danger", "uninstall", "delete self", "", "", "", 0, 0)
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	if task.Status != TaskStatusPendingApproval {
		t.Fatalf("dangerous task with require_approval must start pending_approval, got %q", task.Status)
	}
	// Beacon must not claim it until approved.
	if claimed := s.fetchPendingTasks("agent-danger"); len(claimed) != 0 {
		t.Fatalf("pending_approval dangerous task must not be claimable, got %d", len(claimed))
	}
}

func TestNonDangerousTaskUnaffectedByApproval(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	s.cfg.Security.RequireApproval = true

	task, err := s.createTask("agent-safe", "shell", "whoami", "", "", "", 0, 0)
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	if task.Status != "pending" {
		t.Fatalf("non-dangerous task should stay pending, got %q", task.Status)
	}
}

// TestExpandedDangerousTypesRequireApproval locks in the S1/S4 fix: the
// dangerous list must now cover credential-access (creds, mimikatz, kerberoast,
// dpapi_*) and destructive delete — none of these may be dispatched without a
// second approval when the two-man rule is enabled.
func TestExpandedDangerousTypesRequireApproval(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	s.cfg.Security.RequireApproval = true

	dangerous := []string{
		"creds", "mimikatz", "kerberoast",
		"dpapi_masterkey", "dpapi_blob", "dpapi_browser",
		"cookie_export", "delete", "usb_drop",
		"edr_blind", "edr_kill", "byovd_load",
	}
	for _, tt := range dangerous {
		path := ""
		if tt == "usb_drop" {
			path = `C:\temp\payload.exe`
		}
		task, err := s.createTask("agent-"+tt, tt, "op", "", path, "", 0, 0)
		if err != nil {
			t.Fatalf("createTask(%s): %v", tt, err)
		}
		if task.Status != TaskStatusPendingApproval {
			t.Fatalf("dangerous task type %q must start pending_approval, got %q", tt, task.Status)
		}
		if claimed := s.fetchPendingTasks("agent-" + tt); len(claimed) != 0 {
			t.Fatalf("dangerous task type %q must not be claimable until approved, got %d", tt, len(claimed))
		}
	}
}

func TestApprovalExpirySetOnCreate(t *testing.T) {
	s := newTasksTestServer(t)
	s.cfg = &config.Config{}
	s.cfg.Security.RequireApproval = true
	task, err := s.createTask("agent-exp", "uninstall", "delete self", "", "", "", 0, 0)
	if err != nil {
		t.Fatalf("createTask: %v", err)
	}
	if task.Status != TaskStatusPendingApproval {
		t.Fatalf("status = %q, want pending_approval", task.Status)
	}
	if task.ApprovalExpiresAt == nil {
		t.Fatal("approval-gated task must carry an expiry")
	}
	if time.Until(*task.ApprovalExpiresAt) <= 23*time.Hour {
		t.Fatalf("expiry too short: %v", *task.ApprovalExpiresAt)
	}
}

func TestRejectExpiredApprovals(t *testing.T) {
	s := newTasksTestServer(t)
	seedAgent(t, s, "agent-exp")
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	expired := seedTask(t, s, "agent-exp", "shell", "old", TaskStatusPendingApproval)
	s.db.Model(&expired).Update("approval_expires_at", past)
	fresh := seedTask(t, s, "agent-exp", "shell", "new", TaskStatusPendingApproval)
	s.db.Model(&fresh).Update("approval_expires_at", future)
	noexpiry := seedTask(t, s, "agent-exp", "shell", "legacy", TaskStatusPendingApproval)

	s.rejectExpiredApprovals()

	var reloadedExpired, reloadedFresh, reloadedLegacy db.Task
	s.db.First(&reloadedExpired, expired.ID)
	s.db.First(&reloadedFresh, fresh.ID)
	s.db.First(&reloadedLegacy, noexpiry.ID)
	if reloadedExpired.Status != "cancelled" {
		t.Fatalf("expired approval status = %q, want cancelled", reloadedExpired.Status)
	}
	if reloadedFresh.Status != TaskStatusPendingApproval {
		t.Fatalf("fresh approval status = %q, want pending_approval", reloadedFresh.Status)
	}
	if reloadedLegacy.Status != TaskStatusPendingApproval {
		t.Fatalf("legacy approval without expiry must survive, got %q", reloadedLegacy.Status)
	}
}

func TestAbortReservedSlotBeyondCap(t *testing.T) {
	s := newTasksTestServer(t)
	// Fill to the normal cap.
	for i := 0; i < MaxPendingTasksPerAgent; i++ {
		if err := s.trackPendingTask("agent-cap"); err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
	}
	if err := s.trackPendingTask("agent-cap"); err == nil {
		t.Fatal("normal tracking past cap must fail")
	}
	// Abort injection still fits in its reserved headroom.
	if err := s.trackPendingTaskReserved("agent-cap", AbortReserveSlots); err != nil {
		t.Fatalf("reserved abort slot must fit: %v", err)
	}
}
