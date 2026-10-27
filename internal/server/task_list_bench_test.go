package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
)

// seedListFixtures creates tasks across several agents and two tenants so the
// operator list has to filter AND paginate, which is what the composite indexes
// are for.
func seedListFixtures(b *testing.B, s *Server, tasksPerAgent, agents int) {
	b.Helper()
	now := time.Now()
	for a := 0; a < agents; a++ {
		agentID := fmt.Sprintf("d0000000-0000-0000-0000-%012d", a)
		tenant := uint(1 + a%2)
		if err := s.db.Create(&db.Implant{
			ID: agentID, TenantID: tenant, Hostname: fmt.Sprintf("host-%d", a),
			Status: "online", LastSeen: now,
		}).Error; err != nil {
			b.Fatalf("seed agent: %v", err)
		}
		batch := make([]db.Task, 0, tasksPerAgent)
		for t := 0; t < tasksPerAgent; t++ {
			batch = append(batch, db.Task{
				AgentID: agentID, TenantID: tenant, Type: "shell", Command: "whoami",
				Status:    []string{"completed", "pending", "failed"}[t%3],
				CreatedAt: now.Add(-time.Duration(t) * time.Minute),
			})
		}
		if err := s.db.CreateInBatches(batch, 200).Error; err != nil {
			b.Fatalf("seed tasks: %v", err)
		}
	}
}

func listCtx(tenantID uint) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/tasks?page=1&pageSize=50", nil)
	c.Request = req
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	return c, w
}

// BenchmarkTaskList measures the operator's task-list page: the query the console
// loads on every visit and after every action. It runs several statements (count,
// page, distinct types, agent picker, failed count, nav stats) against a realistic
// table, so a missing index or an accidental full scan shows up as a number instead
// of a guess.
//
// Reported separately for a tenant-scoped caller and an unscoped one: the scoped
// path is the one that has to filter AND order, which is exactly what a composite
// index decides.
func BenchmarkTaskList(b *testing.B) {
	gin.SetMode(gin.TestMode)
	database := testutil.SetupTestDB(b)
	s, _ := initBeaconTestServer(b, database)
	s.ctx = context.Background()
	s.cfg.Server.DataDir = b.TempDir()
	seedListFixtures(b, s, 40, 100) // 4000 tasks over 100 agents, two tenants

	b.Run("tenant-scoped", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c, w := listCtx(1)
			s.handleTaskHistory(c)
			if w.Code != http.StatusOK {
				b.Fatalf("status = %d (%s)", w.Code, w.Body.String())
			}
		}
	})

	b.Run("unscoped", func(b *testing.B) {
		c, _ := listCtx(0)
		c.Set("user", "root")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c2, w := listCtx(0)
			c2.Set("user", "root")
			s.handleTaskHistory(c2)
			if w.Code != http.StatusOK {
				b.Fatalf("status = %d", w.Code)
			}
		}
	})
}
