package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func seedSessionUser(t *testing.T, s *Server, name, role string, tenant uint) db.User {
	t.Helper()
	u := db.User{Username: name, Role: role, TenantID: tenant, IsActive: true}
	if err := s.db.Create(&u).Error; err != nil {
		t.Fatalf("seed user %s: %v", name, err)
	}
	return u
}

func seedSession(t *testing.T, s *Server, userID uint, token string) db.UserSession {
	t.Helper()
	row := db.UserSession{
		UserID:    userID,
		TokenHash: middleware.TokenHash(token),
		IP:        "10.0.0.7",
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(&row).Error; err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return row
}

func sessionCtx(method string, params gin.Params, uid uint, role string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/x", nil)
	if uid != 0 {
		c.Set("user_id", uid)
	}
	c.Set("user_role", role)
	c.Set("user", "tester")
	c.Params = params
	return c, w
}

func TestListUserSessionsAuthorization(t *testing.T) {
	s := mustTenantServer(t)
	mine := seedSessionUser(t, s, "mine", db.RoleUser, 1)
	theirs := seedSessionUser(t, s, "theirs", db.RoleUser, 1)
	seedSession(t, s, mine.ID, "token-mine")
	seedSession(t, s, theirs.ID, "token-theirs")

	// Own list: allowed and only own sessions.
	c, w := sessionCtx(http.MethodGet, gin.Params{{Key: "id", Value: jsonNumber(mine.ID)}}, mine.ID, db.RoleUser)
	s.handleListUserSessions(c)
	if w.Code != http.StatusOK {
		t.Fatalf("own list = %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Sessions []db.UserSession `json:"sessions"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if len(out.Sessions) != 1 || out.Sessions[0].UserID != mine.ID {
		t.Fatalf("own sessions = %+v", out.Sessions)
	}

	// Someone else's list: 403 for a plain user.
	c, w = sessionCtx(http.MethodGet, gin.Params{{Key: "id", Value: jsonNumber(theirs.ID)}}, mine.ID, db.RoleUser)
	s.handleListUserSessions(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign list = %d", w.Code)
	}

	// Admin sees anyone.
	c, w = sessionCtx(http.MethodGet, gin.Params{{Key: "id", Value: jsonNumber(theirs.ID)}}, mine.ID, db.RoleAdmin)
	s.handleListUserSessions(c)
	if w.Code != http.StatusOK {
		t.Fatalf("admin list = %d %s", w.Code, w.Body.String())
	}

	// A non-numeric id is a 400 for the non-admin path.
	c, w = sessionCtx(http.MethodGet, gin.Params{{Key: "id", Value: "abc"}}, mine.ID, db.RoleUser)
	s.handleListUserSessions(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d", w.Code)
	}
}

func TestRevokeSessionAuthorizationAndEffect(t *testing.T) {
	s := mustTenantServer(t)
	mine := seedSessionUser(t, s, "mine", db.RoleUser, 1)
	theirs := seedSessionUser(t, s, "theirs", db.RoleUser, 1)
	mySession := seedSession(t, s, mine.ID, "my-live-token")
	theirSession := seedSession(t, s, theirs.ID, "their-live-token")

	// A user cannot revoke another user's session, even when pointing the
	// :id path at themselves.
	c, w := sessionCtx(http.MethodPost, gin.Params{
		{Key: "id", Value: jsonNumber(mine.ID)},
		{Key: "sessionId", Value: jsonNumber(theirSession.ID)},
	}, mine.ID, db.RoleUser)
	s.handleRevokeSession(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-user revoke = %d %s", w.Code, w.Body.String())
	}
	revoked, err := s.isSessionRevoked("their-live-token")
	if err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Fatal("another user's session was revoked")
	}

	// Nor can they revoke the other user's session by claiming that user id.
	c, w = sessionCtx(http.MethodPost, gin.Params{
		{Key: "id", Value: jsonNumber(theirs.ID)},
		{Key: "sessionId", Value: jsonNumber(theirSession.ID)},
	}, mine.ID, db.RoleUser)
	s.handleRevokeSession(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("spoofed user id revoke = %d %s", w.Code, w.Body.String())
	}

	// Revoking your own session works and the token is now rejected.
	c, w = sessionCtx(http.MethodPost, gin.Params{
		{Key: "id", Value: jsonNumber(mine.ID)},
		{Key: "sessionId", Value: jsonNumber(mySession.ID)},
	}, mine.ID, db.RoleUser)
	s.handleRevokeSession(c)
	if w.Code != http.StatusOK {
		t.Fatalf("own revoke = %d %s", w.Code, w.Body.String())
	}
	revoked, err = s.isSessionRevoked("my-live-token")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("revoked session still accepted by isSessionRevoked")
	}
	// And it drops out of the active list.
	c, w = sessionCtx(http.MethodGet, gin.Params{{Key: "id", Value: jsonNumber(mine.ID)}}, mine.ID, db.RoleUser)
	s.handleListUserSessions(c)
	var out struct {
		Sessions []db.UserSession `json:"sessions"`
	}
	unmarshalData(t, w.Body.Bytes(), &out)
	if len(out.Sessions) != 0 {
		t.Fatalf("revoked session still listed: %+v", out.Sessions)
	}

	// An admin may revoke another user's session.
	c, w = sessionCtx(http.MethodPost, gin.Params{
		{Key: "id", Value: jsonNumber(theirs.ID)},
		{Key: "sessionId", Value: jsonNumber(theirSession.ID)},
	}, 1, db.RoleAdmin)
	s.handleRevokeSession(c)
	if w.Code != http.StatusOK {
		t.Fatalf("admin cross-user revoke = %d %s", w.Code, w.Body.String())
	}
	revoked, err = s.isSessionRevoked("their-live-token")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("admin revoke did not take effect")
	}

	// Unknown session id is a 404, not a silent 200.
	c, w = sessionCtx(http.MethodPost, gin.Params{
		{Key: "id", Value: jsonNumber(mine.ID)},
		{Key: "sessionId", Value: "999999"},
	}, mine.ID, db.RoleUser)
	s.handleRevokeSession(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown session = %d", w.Code)
	}
}

func TestRevokeAllUserSessionsAuthorization(t *testing.T) {
	s := mustTenantServer(t)
	mine := seedSessionUser(t, s, "mine", db.RoleUser, 1)
	seedSession(t, s, mine.ID, "tok-a")
	seedSession(t, s, mine.ID, "tok-b")

	// Plain users cannot mass-revoke.
	c, w := sessionCtx(http.MethodPost, gin.Params{{Key: "id", Value: jsonNumber(mine.ID)}}, mine.ID, db.RoleUser)
	s.handleRevokeAllUserSessions(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("user revoke-all = %d", w.Code)
	}

	// Admins can, and every live session is revoked.
	c, w = sessionCtx(http.MethodPost, gin.Params{{Key: "id", Value: jsonNumber(mine.ID)}}, 1, db.RoleAdmin)
	s.handleRevokeAllUserSessions(c)
	if w.Code != http.StatusOK {
		t.Fatalf("admin revoke-all = %d %s", w.Code, w.Body.String())
	}
	for _, tok := range []string{"tok-a", "tok-b"} {
		revoked, err := s.isSessionRevoked(tok)
		if err != nil {
			t.Fatal(err)
		}
		if !revoked {
			t.Fatalf("session %s survived revoke-all", tok)
		}
	}
	// The underlying helper reports failures so security-critical callers can
	// fail closed.
	if err := s.revokeAllUserSessions(mine.ID); err != nil {
		t.Fatalf("second revoke-all: %v", err)
	}
}

// TestEnforceSessionCapKeepsNewest pins the eviction order: the cap must drop
// the oldest sessions, never the login that just happened.
func TestEnforceSessionCapKeepsNewest(t *testing.T) {
	s := mustTenantServer(t)
	u := seedSessionUser(t, s, "capped", db.RoleUser, 1)

	// Four live sessions with increasing ids.
	for i := 0; i < 4; i++ {
		seedSession(t, s, u.ID, "cap-token-"+jsonNumber(uint(i)))
	}
	if evicted := s.enforceSessionCap(u.ID, 2); evicted != 2 {
		t.Fatalf("evicted = %d, want 2", evicted)
	}

	var rows []db.UserSession
	if err := s.db.Where("user_id = ?", u.ID).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	live := map[uint]bool{}
	for _, r := range rows {
		if r.RevokedAt.IsZero() {
			live[r.ID] = true
		}
	}
	if len(live) != 2 {
		t.Fatalf("live sessions = %d, want 2", len(live))
	}
	for _, r := range rows[len(rows)-2:] {
		if !live[r.ID] {
			t.Fatalf("newest session %d was evicted", r.ID)
		}
	}
	for _, r := range rows[:len(rows)-2] {
		if live[r.ID] {
			t.Fatalf("oldest session %d survived", r.ID)
		}
	}

	// maxConcurrent <= 0 disables the cap entirely.
	if evicted := s.enforceSessionCap(u.ID, 0); evicted != 0 {
		t.Fatalf("cap 0 evicted %d", evicted)
	}
}
