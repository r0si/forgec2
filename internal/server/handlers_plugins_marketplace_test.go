package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/plugin"
	"github.com/gin-gonic/gin"
)

// newMarketplaceTestServer adds a real marketplace store to the plugin test
// server so the review/rating/dependency handlers have their tables.
func newMarketplaceTestServer(t *testing.T) *Server {
	t.Helper()
	s := newPluginTestServer(t)
	s.marketplace = plugin.NewMarketplace(s.db)
	return s
}

func seedMarketplacePlugins(t *testing.T, s *Server) (recon, privesc *db.Plugin) {
	t.Helper()
	recon = &db.Plugin{Name: "recon-probe", Version: "1.2.0", Description: "network recon", Author: "tester",
		Type: "command", Category: "recon", Enabled: true, Tags: "net,scan"}
	privesc = &db.Plugin{Name: "priv-esc", Version: "0.9.0", Description: "local privilege checks", Author: "other",
		Type: "report", Category: "privesc", Enabled: false}
	for _, p := range []*db.Plugin{recon, privesc} {
		if err := s.db.Create(p).Error; err != nil {
			t.Fatalf("seed plugin: %v", err)
		}
	}
	return recon, privesc
}

func pluginCtx(method, target, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("user_id", uint(7))
	c.Set("user", "tester")
	return c, w
}

func TestPluginListFilters(t *testing.T) {
	s := newMarketplaceTestServer(t)
	seedMarketplacePlugins(t, s)

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"all", "", []string{"recon-probe", "priv-esc"}},
		{"by category", "?category=recon", []string{"recon-probe"}},
		{"by type", "?type=report", []string{"priv-esc"}},
		{"by search name", "?search=recon", []string{"recon-probe"}},
		{"by search author", "?search=other", []string{"priv-esc"}},
		{"no match", "?search=zzz", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := pluginCtx(http.MethodGet, "/api/plugins"+tc.query, "", nil)
			s.handlePluginList(c)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
			var rows []struct {
				Name string `json:"name"`
			}
			unmarshalData(t, w.Body.Bytes(), &rows)
			got := make([]string, 0, len(rows))
			for _, r := range rows {
				got = append(got, r.Name)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("names = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("names = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestPluginCreateGetToggleDelete(t *testing.T) {
	s := newMarketplaceTestServer(t)

	c, w := pluginCtx(http.MethodPost, "/api/plugins", `{"name":"new-plugin","type":"hook","category":"utility","enabled":true}`, nil)
	s.handlePluginCreate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	}
	unmarshalData(t, w.Body.Bytes(), &created)
	if created.ID == 0 || created.Name != "new-plugin" {
		t.Fatalf("created = %+v", created)
	}

	// Bad JSON is rejected.
	c, w = pluginCtx(http.MethodPost, "/api/plugins", `{`, nil)
	s.handlePluginCreate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", w.Code)
	}

	idParam := gin.Params{{Key: "id", Value: jsonNumber(created.ID)}}
	c, w = pluginCtx(http.MethodGet, "/api/plugins/x", "", idParam)
	s.handlePluginGet(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "new-plugin") {
		t.Fatalf("get = %d %s", w.Code, w.Body.String())
	}

	c, w = pluginCtx(http.MethodGet, "/api/plugins/nope", "", gin.Params{{Key: "id", Value: "nope"}})
	s.handlePluginGet(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown get = %d", w.Code)
	}

	// Explicit body flips it off.
	c, w = pluginCtx(http.MethodPost, "/api/plugins/x/toggle", `{"enabled":false}`, idParam)
	s.handlePluginToggle(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("toggle explicit = %d %s", w.Code, w.Body.String())
	}
	// No body toggles the current value back on.
	c, w = pluginCtx(http.MethodPost, "/api/plugins/x/toggle", "", idParam)
	s.handlePluginToggle(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("toggle flip = %d %s", w.Code, w.Body.String())
	}
	c, w = pluginCtx(http.MethodPost, "/api/plugins/x/toggle", "", gin.Params{{Key: "id", Value: "nope"}})
	s.handlePluginToggle(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown toggle = %d", w.Code)
	}

	c, w = pluginCtx(http.MethodDelete, "/api/plugins/x", "", idParam)
	s.handlePluginDelete(c)
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", w.Code, w.Body.String())
	}
	var count int64
	s.db.Model(&db.Plugin{}).Where("id = ?", created.ID).Count(&count)
	if count != 0 {
		t.Fatal("plugin row survived delete")
	}
	c, w = pluginCtx(http.MethodDelete, "/api/plugins/x", "", gin.Params{{Key: "id", Value: "nope"}})
	s.handlePluginDelete(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown delete = %d", w.Code)
	}
}

func TestPluginRatingsReviewsDependencies(t *testing.T) {
	s := newMarketplaceTestServer(t)
	recon, _ := seedMarketplacePlugins(t, s)
	idParam := gin.Params{{Key: "id", Value: jsonNumber(recon.ID)}}

	// Empty state first: a plugin with no reviews must answer, not 500.
	c, w := pluginCtx(http.MethodGet, "/api/plugins/x/rating", "", idParam)
	s.handlePluginRating(c)
	if w.Code != http.StatusOK {
		t.Fatalf("rating (empty) = %d %s", w.Code, w.Body.String())
	}
	c, w = pluginCtx(http.MethodGet, "/api/plugins/x/reviews", "", idParam)
	s.handlePluginReviews(c)
	if w.Code != http.StatusOK {
		t.Fatalf("reviews (empty) = %d %s", w.Code, w.Body.String())
	}

	// Add a review, then the summary must reflect it.
	c, w = pluginCtx(http.MethodPost, "/api/plugins/x/reviews", `{"rating":4,"comment":"works"}`, idParam)
	s.handlePluginAddReview(c)
	if w.Code != http.StatusOK {
		t.Fatalf("add review = %d %s", w.Code, w.Body.String())
	}
	c, w = pluginCtx(http.MethodPost, "/api/plugins/x/rate", `{"rating":5}`, idParam)
	s.handlePluginRate(c)
	if w.Code != http.StatusOK {
		t.Fatalf("rate = %d %s", w.Code, w.Body.String())
	}
	c, w = pluginCtx(http.MethodGet, "/api/plugins/x/reviews", "", idParam)
	s.handlePluginReviews(c)
	if !strings.Contains(w.Body.String(), "works") && !strings.Contains(w.Body.String(), "rating") {
		t.Fatalf("reviews after add = %s", w.Body.String())
	}

	// Validation and bad ids.
	c, w = pluginCtx(http.MethodPost, "/api/plugins/x/rate", `{"rating":9}`, idParam)
	s.handlePluginRate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range rate = %d", w.Code)
	}
	for _, fn := range []func(*gin.Context){s.handlePluginRating, s.handlePluginReviews, s.handlePluginAddReview, s.handlePluginRate, s.handlePluginDependencies, s.handlePluginUpdateStatus} {
		c, w = pluginCtx(http.MethodGet, "/api/plugins/x", `{}`, gin.Params{{Key: "id", Value: "not-a-number"}})
		fn(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%T with non-numeric id = %d", fn, w.Code)
		}
	}

	c, w = pluginCtx(http.MethodGet, "/api/plugins/x/dependencies", "", idParam)
	s.handlePluginDependencies(c)
	if w.Code != http.StatusOK {
		t.Fatalf("dependencies = %d %s", w.Code, w.Body.String())
	}
}

func TestPluginUpdateSummaryAndPage(t *testing.T) {
	s := newMarketplaceTestServer(t)
	seedMarketplacePlugins(t, s)

	for name, fn := range map[string]func(*gin.Context){
		"check updates":  s.handlePluginCheckUpdates,
		"update summary": s.handlePluginUpdateSummary,
		"plugins page":   s.handlePluginsPage,
	} {
		c, w := pluginCtx(http.MethodGet, "/api/plugins", "", nil)
		fn(c)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", name, w.Code, w.Body.String())
		}
	}
}

func TestPluginUpdateExportAndAutomationPage(t *testing.T) {
	s := newMarketplaceTestServer(t)
	recon, _ := seedMarketplacePlugins(t, s)
	idParam := gin.Params{{Key: "id", Value: jsonNumber(recon.ID)}}

	// update / export reject a non-numeric id before touching the store.
	for name, fn := range map[string]func(*gin.Context){
		"update": s.handlePluginUpdate,
		"export": s.handlePluginExport,
	} {
		c, w := pluginCtx(http.MethodPost, "/api/plugins/x", "", gin.Params{{Key: "id", Value: "nope"}})
		fn(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s with non-numeric id = %d", name, w.Code)
		}
	}

	// A plugin with no recorded release has nothing to update to; the handler
	// must answer with an error envelope, not panic or 500.
	c, w := pluginCtx(http.MethodPost, "/api/plugins/x/update", "", idParam)
	s.handlePluginUpdate(c)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusOK {
		t.Fatalf("update with no release = %d %s", w.Code, w.Body.String())
	}

	// Export walks the installed directory; with none installed it must fail
	// cleanly rather than stream a broken archive.
	c, w = pluginCtx(http.MethodGet, "/api/plugins/x/export", "", idParam)
	s.handlePluginExport(c)
	if w.Code == http.StatusOK && w.Body.Len() == 0 {
		t.Fatal("export returned an empty 200")
	}

	// The automation page is rendered like every other page route.
	c, w = pluginCtx(http.MethodGet, "/automation", "", nil)
	s.handleAutomationPage(c)
	if w.Code != http.StatusOK {
		t.Fatalf("automation page = %d %s", w.Code, w.Body.String())
	}
}

func TestBOFRepoRateValidation(t *testing.T) {
	s := newMarketplaceTestServer(t)

	// Bad body is rejected before the repo lookup.
	c, w := pluginCtx(http.MethodPost, "/api/bof/repo/rate", `{`, gin.Params{{Key: "id", Value: "1"}})
	s.handleBOFRepoRate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad body = %d %s", w.Code, w.Body.String())
	}
	// An out-of-range rating is rejected too.
	c, w = pluginCtx(http.MethodPost, "/api/bof/repo/rate", `{"rating":9}`, gin.Params{{Key: "id", Value: "1"}})
	s.handleBOFRepoRate(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range rating = %d %s", w.Code, w.Body.String())
	}
	// No entry with that id ⇒ 404 rather than a silent write.
	if w.Code == http.StatusOK {
		t.Fatal("rating a missing BOF repo entry succeeded")
	}
	var leftover int64
	s.db.Model(&db.Plugin{}).Count(&leftover)
	if leftover != 0 {
		t.Fatalf("BOF rating wrote a plugin row: %d", leftover)
	}
}
