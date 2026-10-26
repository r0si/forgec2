package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/forgec2/forgec2/internal/util"
	"github.com/gin-gonic/gin"
)

func newTagsTestServer(t *testing.T) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return &Server{db: testutil.SetupTestDB(t)}
}

func seedTagAssignment(t *testing.T, s *Server) (tag db.AgentTag, agent db.Implant) {
	t.Helper()
	now := time.Now()
	tag = db.AgentTag{ID: util.NewString(), Name: "test-tag", Color: "#3498db", CreatedAt: now, UpdatedAt: now}
	if err := s.db.Create(&tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}
	agent = db.Implant{ID: util.NewString(), Hostname: "host-a", IP: "10.0.0.1", LastSeen: now}
	if err := s.db.Create(&agent).Error; err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.db.Create(&db.AgentTagAssignment{AgentTagID: tag.ID, ImplantID: agent.ID}).Error; err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	return tag, agent
}

func TestHandleBatchAgentTags_ReturnsTags(t *testing.T) {
	s := newTagsTestServer(t)
	tag, agent := seedTagAssignment(t, s)

	body, _ := json.Marshal(map[string]interface{}{"agent_ids": []string{agent.ID}})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodPost, "/agents/batch/tags", bytes.NewReader(body))

	s.handleBatchAgentTags(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data map[string][]struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Color string `json:"color"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v; body=%s", err, w.Body.String())
	}
	agentTags := resp.Data[agent.ID]
	if len(agentTags) != 1 || agentTags[0].ID != tag.ID || agentTags[0].Name != "test-tag" {
		t.Fatalf("expected tag %s on agent, got %+v", tag.ID, agentTags)
	}
}

func TestHandleAgentTags_ReturnsTags(t *testing.T) {
	s := newTagsTestServer(t)
	tag, agent := seedTagAssignment(t, s)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Params = gin.Params{{Key: "id", Value: agent.ID}}
	c.Request, _ = http.NewRequest(http.MethodGet, "/api/agents/"+agent.ID+"/tags", nil)

	s.handleAgentTags(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Tags []db.AgentTag `json:"tags"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json: %v; body=%s", err, w.Body.String())
	}
	if len(resp.Data.Tags) != 1 || resp.Data.Tags[0].ID != tag.ID {
		t.Fatalf("expected tag %s on agent, got %+v", tag.ID, resp.Data.Tags)
	}
}

func TestHandleAPITagDelete_ClearsAssignments(t *testing.T) {
	s := newTagsTestServer(t)
	tag, _ := seedTagAssignment(t, s)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Params = gin.Params{{Key: "id", Value: tag.ID}}
	c.Request, _ = http.NewRequest(http.MethodDelete, "/api/tags/"+tag.ID, nil)

	s.handleAPITagDelete(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var count int64
	if err := s.db.Model(&db.AgentTagAssignment{}).Where("agent_tag_id = ?", tag.ID).Count(&count).Error; err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected assignments cleared, got %d remaining", count)
	}
}

func tagCreateContext(body string) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user_role", db.RoleAdmin)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/tags", bytes.NewReader([]byte(body)))
	c.Set("user_role", "operator")
	return w, c
}

func TestHandleAPITagCreate_RejectsInvalidInput(t *testing.T) {
	s := newTagsTestServer(t)

	cases := []struct {
		name string
		body string
	}{
		{"comma in name", `{"name":"a,b","color":"#3498db"}`},
		{"control char in name", `{"name":"a\x01b","color":"#3498db"}`},
		{"name too long", `{"name":"` + strings.Repeat("x", maxTagNameLen+1) + `","color":"#3498db"}`},
		{"bad color", `{"name":"ok","color":"red"}`},
		{"short color", `{"name":"ok","color":"#349"}`},
		{"spaces only name", `{"name":"   ","color":"#3498db"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, c := tagCreateContext(tc.body)
			s.handleAPITagCreate(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d; body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleAPITagCreate_AcceptsValidInput(t *testing.T) {
	s := newTagsTestServer(t)

	w, c := tagCreateContext(`{"name":"  valid-tag  ","color":"#a1B2c3"}`)
	s.handleAPITagCreate(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Tag struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Color string `json:"color"`
		} `json:"tag"`
	}
	unmarshalData(t, w.Body.Bytes(), &resp)
	if resp.Tag.Name != "valid-tag" || resp.Tag.Color != "#a1B2c3" {
		t.Fatalf("unexpected tag: %+v", resp.Tag)
	}
}
