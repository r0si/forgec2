package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// TestSavedViewsAreOwnerScoped pins the isolation on the one handler pair in this package with
// no coverage at all. Saved views hold an operator's filter state, and both the list and the
// delete are scoped by `user_id` - but nothing tested it, and the whole area was untestable
// until the shared test schema gained the SavedView table (it was one of ten missing).
func TestSavedViewsAreOwnerScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := mustTenantServer(t)

	create := func(uid uint, name, state string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"page":"agents","name":"` + name + `","state":"` + state + `"}`
		c.Request = httptest.NewRequest(http.MethodPost, "/api/saved-views", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uid)
		s.handleCreateSavedView(c)
		return w
	}
	list := func(uid uint) string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/saved-views?page=agents", nil)
		c.Set("user", "op")
		c.Set("user_role", db.RoleAdmin)
		c.Set("user_id", uid)
		s.handleListSavedViews(c)
		return w.Body.String()
	}

	if w := create(1, "alice-view", "status%3Donline"); w.Code != http.StatusOK {
		t.Fatalf("create for user 1 = %d (%s)", w.Code, w.Body.String())
	}

	// The owner sees it, the other operator does not.
	if body := list(1); !bytes.Contains([]byte(body), []byte("alice-view")) {
		t.Fatalf("user 1 cannot see their own view: %s", body)
	}
	if body := list(2); bytes.Contains([]byte(body), []byte("alice-view")) {
		t.Fatalf("user 2 sees user 1's saved view: %s", body)
	}

	// And cannot delete it by id.
	var view db.SavedView
	if err := s.db.Where("name = ?", "alice-view").First(&view).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/saved-views/1", nil)
	c.Params = gin.Params{{Key: "id", Value: uintString(view.ID)}}
	c.Set("user", "op")
	c.Set("user_role", db.RoleAdmin)
	c.Set("user_id", uint(2))
	s.handleDeleteSavedView(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("user 2 deleted user 1's view: %d (%s)", w.Code, w.Body.String())
	}
	var stillThere int64
	if err := s.db.Model(&db.SavedView{}).Where("id = ?", view.ID).Count(&stillThere).Error; err != nil {
		t.Fatal(err)
	}
	if stillThere != 1 {
		t.Fatal("the row was deleted despite the 404")
	}
}
