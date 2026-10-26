package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func TestHandleWindowCloseRequiresTarget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newTasksTestServer(t)
	if err := s.db.Create(&db.Implant{ID: "a1", Hostname: "TEST"}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	c.Request, _ = http.NewRequest(http.MethodPost, "/agents/a1/window/close", nil)
	s.handleWindowClose(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without target, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleWindowCloseDispatches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newTasksTestServer(t)
	if err := s.db.Create(&db.Implant{ID: "a1", Hostname: "TEST"}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	c.Request, _ = http.NewRequest(http.MethodPost, "/agents/a1/window/close", strings.NewReader("command=1234"))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleWindowClose(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var task db.Task
	if err := s.db.Where("agent_id = ? AND type = ?", "a1", "window_close").First(&task).Error; err != nil {
		t.Fatalf("query task: %v", err)
	}
	if task.Command != "1234" {
		t.Fatalf("command = %q, want %q", task.Command, "1234")
	}
}

func TestHandleWindowListDispatches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := newTasksTestServer(t)
	if err := s.db.Create(&db.Implant{ID: "a1", Hostname: "TEST"}).Error; err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	c.Request, _ = http.NewRequest(http.MethodPost, "/agents/a1/window/list", nil)
	s.handleWindowList(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var n int64
	if err := s.db.Model(&db.Task{}).Where("agent_id = ? AND type = ?", "a1", "window_list").Count(&n).Error; err != nil {
		t.Fatalf("query tasks: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 window_list task, got %d", n)
	}
}
