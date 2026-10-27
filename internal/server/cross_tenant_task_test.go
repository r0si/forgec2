package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// TestCrossTenantTaskCreationIsRefused covers three handlers that take agent_id from the
// REQUEST BODY and queue a task for it: the REST automation endpoint, the BloodHound collect
// endpoint and the cloud-credential steal endpoint. createTask deliberately has no gin
// context (background callers), so it cannot scope by tenant itself - and none of the three
// validated the agent, which let a tenant-1 operator queue a shell/collection task on a
// tenant-2 implant just by knowing its id.
func TestCrossTenantTaskCreationIsRefused(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name string
		path string
		body func(agentID string) string
		call func(s *Server) gin.HandlerFunc
	}{
		{
			name: "rest-api",
			path: "/api/v1/tasks",
			body: func(id string) string {
				return `{"agent_id":"` + id + `","type":"shell","command":"whoami"}`
			},
			call: func(s *Server) gin.HandlerFunc { return s.apiCreateTask },
		},
		{
			name: "bloodhound",
			path: "/api/bloodhound/collect",
			body: func(id string) string {
				return `{"agent_id":"` + id + `","method":"all"}`
			},
			call: func(s *Server) gin.HandlerFunc { return s.handleBloodHoundCollect },
		},
		{
			name: "cloud-steal",
			path: "/api/cloud/steal",
			body: func(id string) string {
				return `{"agent_id":"` + id + `","provider":"aws"}`
			},
			call: func(s *Server) gin.HandlerFunc { return s.handleCloudSteal },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustTenantServer(t)
			agent := db.Implant{ID: "tenant-one-agent", Hostname: "h1", Status: "online", TenantID: 1, LastSeen: time.Now()}
			if err := s.db.Create(&agent).Error; err != nil {
				t.Fatal(err)
			}

			// Tenant 2, with the permissions each route asks for.
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body(agent.ID)))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("user", "outsider")
			c.Set("user_role", db.RoleAdmin)
			c.Set("user_id", uint(7))
			c.Set(middleware.TenantIDContextKey, uint(2))

			tc.call(s)(c)

			if w.Code == http.StatusOK || w.Code == http.StatusCreated {
				t.Fatalf("tenant 2 queued a task on tenant 1's agent: %d %s", w.Code, w.Body.String())
			}

			var queued int64
			if err := s.db.Model(&db.Task{}).Where("agent_id = ?", agent.ID).Count(&queued).Error; err != nil {
				t.Fatal(err)
			}
			if queued != 0 {
				t.Fatalf("%d task row(s) were created on a foreign agent", queued)
			}
		})
	}
}
