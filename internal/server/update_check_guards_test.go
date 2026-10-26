package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestCheckForUpdateFailurePath covers the branch that records a failed check.
// The repository value is made unparsable so no network call is attempted.
func TestCheckForUpdateFailurePath(t *testing.T) {
	s := mustTenantServer(t)
	s.cfg.Server.UpdateCheckRepo = "bad\x00repo"

	s.checkForUpdate()

	s.updateState.mu.RLock()
	checkedAt := s.updateState.CheckedAt
	checkOK := s.updateState.CheckOK
	errMsg := s.updateState.Error
	available := s.updateState.Available
	s.updateState.mu.RUnlock()

	if checkedAt.IsZero() {
		t.Fatal("CheckedAt not stamped on a failed check")
	}
	if checkOK {
		t.Fatal("CheckOK true after a failed check")
	}
	if errMsg == "" {
		t.Fatal("Error not recorded")
	}
	if available {
		t.Fatal("Available true after a failed check")
	}
}

// TestHotUpdateGuards pins the three refusals that happen before any download.
func TestHotUpdateGuards(t *testing.T) {
	newCtx := func() (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/update-check/hot-update", nil)
		c.Set("user", "admin")
		c.Set("user_role", "admin")
		c.Set("user_id", uint(1))
		return c, w
	}

	t.Run("in flight", func(t *testing.T) {
		s := mustTenantServer(t)
		s.updateState.mu.Lock()
		s.updateState.InFlight = true
		s.updateState.mu.Unlock()
		c, w := newCtx()
		s.handleHotUpdate(c)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already running") {
			t.Fatalf("in-flight = %d %s", w.Code, w.Body.String())
		}
		// The guard must stay set: the running update owns it.
		s.updateState.mu.RLock()
		stillInFlight := s.updateState.InFlight
		s.updateState.mu.RUnlock()
		if !stillInFlight {
			t.Fatal("a second request cleared another update's in-flight flag")
		}
	})

	t.Run("no update available", func(t *testing.T) {
		s := mustTenantServer(t)
		c, w := newCtx()
		s.handleHotUpdate(c)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "No updates available") {
			t.Fatalf("no update = %d %s", w.Code, w.Body.String())
		}
		s.updateState.mu.RLock()
		inFlight := s.updateState.InFlight
		s.updateState.mu.RUnlock()
		if inFlight {
			t.Fatal("in-flight flag not cleared after the refusal")
		}
	})

	t.Run("already up to date", func(t *testing.T) {
		s := mustTenantServer(t)
		s.updateState.mu.Lock()
		s.updateState.Available = true
		s.updateState.LatestVersion = ServerVersion
		s.updateState.mu.Unlock()
		c, w := newCtx()
		s.handleHotUpdate(c)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Already up to date") {
			t.Fatalf("up to date = %d %s", w.Code, w.Body.String())
		}
		s.updateState.mu.RLock()
		inFlight := s.updateState.InFlight
		s.updateState.mu.RUnlock()
		if inFlight {
			t.Fatal("in-flight flag not cleared after the up-to-date refusal")
		}
	})
}
