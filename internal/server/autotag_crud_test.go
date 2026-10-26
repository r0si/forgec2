package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func autoTagCtx(method, body, id string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/autotag/rules", nil)
	} else {
		req = httptest.NewRequest(method, "/api/autotag/rules", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	c.Request = req
	c.Set("user", "admin1")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(1))
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	return c, w
}

// TestAutoTagRuleCRUD covers the rule lifecycle and its validation: create
// requires a name and a tag, the ID is generated, update replaces the rule and
// 404s for an unknown id, toggle flips `enabled`, and delete removes it.
func TestAutoTagRuleCRUD(t *testing.T) {
	s := mustTenantServer(t)
	tag := db.AgentTag{ID: "tag-1", Name: "crown-jewel", Color: "red"}
	if err := s.db.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}

	// Missing name/tag is refused.
	c, w := autoTagCtx(http.MethodPost, `{"condition":"[]"}`, "")
	s.handleAutoTagCreate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing fields = %d %s", w.Code, w.Body.String())
	}

	// Create with an explicit id and condition.
	c, w = autoTagCtx(http.MethodPost,
		`{"name":"dc hosts","tag_id":"tag-1","condition":"[{\"field\":\"hostname\",\"op\":\"contains\",\"value\":\"DC\"}]","priority":5}`,
		"")
	s.handleAutoTagCreate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var rules []db.AutoTagRule
	if err := s.db.Find(&rules).Error; err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(rules))
	}
	created := rules[0]
	if created.ID == "" {
		t.Fatal("create did not generate an id")
	}
	if created.Priority != 5 || created.TagID != "tag-1" {
		t.Fatalf("created rule = %+v", created)
	}
	if !created.Enabled {
		t.Fatal("a new rule should be enabled by default")
	}

	// Update replaces the name and priority; the tag is preserved.
	c, w = autoTagCtx(http.MethodPut,
		`{"name":"dc hosts v2","tag_id":"tag-1","condition":"[]","priority":9}`, created.ID)
	s.handleAutoTagUpdate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("update = %d %s", w.Code, w.Body.String())
	}
	var reloaded db.AutoTagRule
	if err := s.db.Where("id = ?", created.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Name != "dc hosts v2" || reloaded.Priority != 9 {
		t.Fatalf("updated rule = %+v", reloaded)
	}

	// Updating an unknown rule is a 404.
	c, w = autoTagCtx(http.MethodPut, `{"name":"nope","tag_id":"tag-1","condition":"[]"}`, "does-not-exist")
	s.handleAutoTagUpdate(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown update = %d %s", w.Code, w.Body.String())
	}

	// Toggle flips the flag.
	c, w = autoTagCtx(http.MethodPost, "", created.ID)
	s.handleAutoTagToggle(c)
	if w.Code != http.StatusOK {
		t.Fatalf("toggle = %d %s", w.Code, w.Body.String())
	}
	if err := s.db.Where("id = ?", created.ID).First(&reloaded).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.Enabled {
		t.Fatal("toggle did not disable the rule")
	}

	// Delete removes it, and a second delete is a 404.
	c, w = autoTagCtx(http.MethodDelete, "", created.ID)
	s.handleAutoTagDelete(c)
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", w.Code, w.Body.String())
	}
	var count int64
	s.db.Model(&db.AutoTagRule{}).Where("id = ?", created.ID).Count(&count)
	if count != 0 {
		t.Fatal("rule survived the delete")
	}
	c, w = autoTagCtx(http.MethodDelete, "", created.ID)
	s.handleAutoTagDelete(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d %s", w.Code, w.Body.String())
	}
}
