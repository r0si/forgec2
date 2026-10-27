package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func approveCtx(t *testing.T, taskID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/approve", nil)
	c.Params = gin.Params{{Key: "taskId", Value: taskID}}
	c.Set("user", "approver")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(2))
	return c, w
}

// TestApproveRejectsExpiredPrompt pins the fix for a real window: the sweeper rejects
// expired approvals every five minutes, but the approve handler did not check the
// expiry at all - so between expiry and the next sweep an expired prompt could still
// be confirmed, which defeats the reason the window exists.
func TestApproveRejectsExpiredPrompt(t *testing.T) {
	s := mustTenantServer(t)
	const agentID = "77777777-7777-7777-7777-777777777777"
	seedIOCAgent(t, s, agentID)

	past := time.Now().Add(-time.Minute)
	task := db.Task{
		AgentID: agentID, Type: "shell", Command: "whoami", Status: TaskStatusPendingApproval,
		CreatedBy: "someone-else", ApprovalExpiresAt: &past, CreatedAt: time.Now(),
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	c, w := approveCtx(t, strconv.FormatUint(uint64(task.ID), 10))
	s.handleApproveTask(c)
	if w.Code != http.StatusGone {
		t.Fatalf("approving an expired prompt = %d, want 410 (%s)", w.Code, w.Body.String())
	}

	var after db.Task
	if err := s.db.First(&after, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != "cancelled" {
		t.Fatalf("the expired task is still %q; a second approve attempt could resurrect it", after.Status)
	}
	if after.Error == "" {
		t.Fatal("the cancellation carries no reason")
	}
}

// TestApproveAcceptsPromptInsideTheWindow keeps the fix honest: a prompt that has not
// expired is still approvable, and the two-man rule still applies.
func TestApproveAcceptsPromptInsideTheWindow(t *testing.T) {
	s := mustTenantServer(t)
	const agentID = "88888888-8888-8888-8888-888888888888"
	seedIOCAgent(t, s, agentID)

	future := time.Now().Add(time.Hour)
	task := db.Task{
		AgentID: agentID, Type: "shell", Command: "whoami", Status: TaskStatusPendingApproval,
		CreatedBy: "someone-else", ApprovalExpiresAt: &future, CreatedAt: time.Now(),
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	c, w := approveCtx(t, strconv.FormatUint(uint64(task.ID), 10))
	s.handleApproveTask(c)
	if w.Code != http.StatusOK {
		t.Fatalf("a live prompt was refused: %d (%s)", w.Code, w.Body.String())
	}
	var after db.Task
	if err := s.db.First(&after, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != "pending" || after.ApprovedBy != "approver" {
		t.Fatalf("task after approval = %+v", after)
	}
}

// TestApproveSelfIsStillRefused makes sure the expiry change did not disturb the
// two-man rule, which is the other half of what this handler protects.
func TestApproveSelfIsStillRefused(t *testing.T) {
	s := mustTenantServer(t)
	const agentID = "99999999-8888-7777-6666-555555555555"
	seedIOCAgent(t, s, agentID)

	future := time.Now().Add(time.Hour)
	task := db.Task{
		AgentID: agentID, Type: "shell", Command: "whoami", Status: TaskStatusPendingApproval,
		CreatedBy: "approver", ApprovalExpiresAt: &future, CreatedAt: time.Now(),
	}
	if err := s.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}

	c, w := approveCtx(t, strconv.FormatUint(uint64(task.ID), 10))
	s.handleApproveTask(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("self-approval = %d, want 403", w.Code)
	}
}
