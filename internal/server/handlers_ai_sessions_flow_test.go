package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// aiSessionCtx builds a JSON request context for an AI session handler.
func aiSessionCtx(method, target, body string, uid uint, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", "ai-user")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uid)
	c.Params = params
	return c, w
}

func seedAISession(t *testing.T, s *Server, uid uint, title string) db.AIChatSession {
	t.Helper()
	session := db.AIChatSession{TenantID: 1, OwnerID: uid, Owner: "ai-user", Title: title, WritePolicy: "approval"}
	if err := s.db.Create(&session).Error; err != nil {
		t.Fatalf("seed AI session: %v", err)
	}
	return session
}

// TestAISessionsOwnership covers the ownership contract: a user only ever sees
// and mutates their own sessions, even when they guess an id.
func TestAISessionsOwnership(t *testing.T) {
	s := mustTenantServer(t)
	// The delete path cascades through the AI tables; the minimal test DB does
	// not migrate all of them.
	if err := s.db.AutoMigrate(&db.AIChatRun{}, &db.AIChatRunEvent{}, &db.AIAttachment{}, &db.AIExecutionIntent{}, &db.AIChatMessage{}); err != nil {
		t.Fatalf("migrate AI tables: %v", err)
	}
	me := seedAIUser(t, s, "ai-user", 1)
	other := seedAIUser(t, s, "other-user", 1)
	mine := seedAISession(t, s, me.ID, "mine")
	theirs := seedAISession(t, s, other.ID, "theirs")

	// List: only the caller's sessions come back.
	c, w := aiSessionCtx(http.MethodGet, "/api/ai/sessions", "", me.ID, nil)
	s.handleAISessionsList(c)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "theirs") {
		t.Fatalf("list leaked another user's session: %s", body)
	}
	if !strings.Contains(body, "mine") {
		t.Fatalf("list missing the caller's session: %s", body)
	}

	// Get another user's session: 404, not 403 with data.
	c, w = aiSessionCtx(http.MethodGet, "/api/ai/sessions/9", "", me.ID, gin.Params{{Key: "id", Value: jsonNumber(theirs.ID)}})
	s.handleAISessionsGet(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign get = %d %s", w.Code, w.Body.String())
	}

	// Update another user's session: 404 and unchanged.
	c, w = aiSessionCtx(http.MethodPatch, "/api/ai/sessions/9", `{"title":"hijacked"}`, me.ID, gin.Params{{Key: "id", Value: jsonNumber(theirs.ID)}})
	s.handleAISessionsUpdate(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign update = %d %s", w.Code, w.Body.String())
	}
	var reloaded db.AIChatSession
	if err := s.db.Where("id = ?", theirs.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Title != "theirs" {
		t.Fatalf("foreign session was renamed to %q", reloaded.Title)
	}

	// Delete another user's session: 404 and it survives.
	c, w = aiSessionCtx(http.MethodDelete, "/api/ai/sessions/9", "", me.ID, gin.Params{{Key: "id", Value: jsonNumber(theirs.ID)}})
	s.handleAISessionsDelete(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign delete = %d %s", w.Code, w.Body.String())
	}
	var count int64
	s.db.Model(&db.AIChatSession{}).Where("id = ?", theirs.ID).Count(&count)
	if count != 1 {
		t.Fatal("foreign session was deleted")
	}

	// The owner can rename and delete their own session.
	c, w = aiSessionCtx(http.MethodPatch, "/api/ai/sessions/1", `{"title":"renamed","pinned":true}`, me.ID, gin.Params{{Key: "id", Value: jsonNumber(mine.ID)}})
	s.handleAISessionsUpdate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("own update = %d %s", w.Code, w.Body.String())
	}
	// Reset the destination: GORM adds a non-zero primary key from the struct
	// to the WHERE clause, so reusing a populated struct finds nothing.
	reloaded = db.AIChatSession{}
	if err := s.db.Where("id = ?", mine.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Title != "renamed" || !reloaded.Pinned {
		t.Fatalf("own update did not apply: %+v", reloaded)
	}

	c, w = aiSessionCtx(http.MethodDelete, "/api/ai/sessions/1", "", me.ID, gin.Params{{Key: "id", Value: jsonNumber(mine.ID)}})
	s.handleAISessionsDelete(c)
	if w.Code != http.StatusOK {
		t.Fatalf("own delete = %d %s", w.Code, w.Body.String())
	}
	s.db.Model(&db.AIChatSession{}).Where("id = ?", mine.ID).Count(&count)
	if count != 0 {
		t.Fatal("own session survived delete")
	}
}

// TestAISessionsCreateAndValidation covers creation defaults and the bounds.
func TestAISessionsCreateAndValidation(t *testing.T) {
	s := mustTenantServer(t)
	me := seedAIUser(t, s, "ai-user", 1)

	// An empty title defaults and the session is stamped with the caller.
	c, w := aiSessionCtx(http.MethodPost, "/api/ai/sessions", `{}`, me.ID, nil)
	s.handleAISessionsCreate(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created db.AIChatSession
	if err := s.db.Where("owner_id = ?", me.ID).First(&created).Error; err != nil {
		t.Fatalf("created session missing: %v", err)
	}
	if created.Title != "New Chat" || created.Owner != "ai-user" || created.TenantID != 1 {
		t.Fatalf("created session = %+v", created)
	}
	if created.WritePolicy != "approval" {
		t.Fatalf("default write policy = %q", created.WritePolicy)
	}

	// A non-allowlisted write policy falls back to approval.
	c, w = aiSessionCtx(http.MethodPost, "/api/ai/sessions", `{"title":"t2","write_policy":"auto_everything"}`, me.ID, nil)
	s.handleAISessionsCreate(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create with bad policy = %d %s", w.Code, w.Body.String())
	}
	var second db.AIChatSession
	s.db.Where("title = ?", "t2").First(&second)
	if second.WritePolicy != "approval" {
		t.Fatalf("escalated write policy = %q", second.WritePolicy)
	}

	// An over-long title is rejected.
	long := strings.Repeat("x", aiSessionTitleMaxRunes+1)
	c, w = aiSessionCtx(http.MethodPost, "/api/ai/sessions", `{"title":"`+long+`"}`, me.ID, nil)
	s.handleAISessionsCreate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("long title = %d", w.Code)
	}

	// Invalid JSON is a 400.
	c, w = aiSessionCtx(http.MethodPost, "/api/ai/sessions", `{`, me.ID, nil)
	s.handleAISessionsCreate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid json = %d", w.Code)
	}
}
