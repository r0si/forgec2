package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/payload"
	"github.com/forgec2/forgec2/internal/testutil"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func newStageTestServer(t *testing.T, dataDir string) (*Server, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	database := testutil.SetupTestDB(t)
	cfg := &config.Config{}
	cfg.Server.DataDir = dataDir
	s := &Server{
		db:           database,
		cfg:          cfg,
		eventManager: NewEventManager(database),
	}
	r := gin.New()
	r.GET("/stage/:token", s.handleServeStage)
	s.router = r
	return s, database
}

// stageFixture provisions a signed token whose encrypted blob is pre-written.
func stageFixture(t *testing.T, dataDir string, expiresAt time.Time) (*Server, string, string, []byte, []byte) {
	t.Helper()
	payload.SetStagerKey([]byte("0123456789abcdef0123456789abcdef"))
	s, database := newStageTestServer(t, dataDir)

	token, sig, _, err := payload.NewStageToken()
	if err != nil {
		t.Fatalf("NewStageToken: %v", err)
	}
	st := db.StagerToken{
		Token:        token,
		ListenerID:   1,
		OS:           "linux",
		Architecture: "amd64",
		Format:       "exe",
		ExpiresAt:    expiresAt,
	}
	if err := database.Create(&st).Error; err != nil {
		t.Fatalf("create stager token: %v", err)
	}

	plaintext := []byte("MZ-fake-stage2-payload-fake-stage2-payload")
	if err := payload.WriteStage2Blob(dataDir, token, plaintext); err != nil {
		t.Fatalf("WriteStage2Blob: %v", err)
	}
	blob, err := payload.LoadStage2Blob(dataDir, token)
	if err != nil {
		t.Fatalf("LoadStage2Blob: %v", err)
	}
	return s, token, sig, plaintext, blob
}

func stageGet(t *testing.T, s *Server, urlPath string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, urlPath, nil)
	s.router.ServeHTTP(w, req)
	return w
}

func TestStageServeSigned(t *testing.T) {
	s, token, sig, plaintext, blob := stageFixture(t, t.TempDir(), time.Now().Add(time.Hour))

	w := stageGet(t, s, "/stage/"+token+"?s="+sig)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", w.Code, w.Body.String())
	}
	if w.Body.String() == string(plaintext) {
		t.Fatal("stage served plaintext")
	}
	if w.Body.String() != string(blob) {
		t.Fatal("stage body does not match encrypted blob")
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
}

func TestStageServeBadSignature(t *testing.T) {
	s, token, _, _, _ := stageFixture(t, t.TempDir(), time.Now().Add(time.Hour))

	w := stageGet(t, s, "/stage/"+token+"?s=tampered")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	// Missing signature entirely must also be rejected.
	w = stageGet(t, s, "/stage/"+token)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestStageServeExpired(t *testing.T) {
	s, token, sig, _, _ := stageFixture(t, t.TempDir(), time.Now().Add(-time.Hour))

	w := stageGet(t, s, "/stage/"+token+"?s="+sig)
	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", w.Code)
	}
}

func TestStageServeUnknownToken(t *testing.T) {
	s, _, sig, _, _ := stageFixture(t, t.TempDir(), time.Now().Add(time.Hour))

	other, _, _, err := payload.NewStageToken()
	if err != nil {
		t.Fatalf("NewStageToken: %v", err)
	}
	w := stageGet(t, s, "/stage/"+other+"?s="+sig)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (signature bound to a different token)", w.Code)
	}
}

func TestStageServeNonHexToken(t *testing.T) {
	s, _, _, _, _ := stageFixture(t, t.TempDir(), time.Now().Add(time.Hour))

	w := stageGet(t, s, "/stage/abcdefgh-not-hex")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestStageServeConsumesToken(t *testing.T) {
	dataDir := t.TempDir()
	s, token, sig, _, _ := stageFixture(t, dataDir, time.Now().Add(time.Hour))

	w := stageGet(t, s, "/stage/"+token+"?s="+sig)
	if w.Code != http.StatusOK {
		t.Fatalf("first fetch status = %d, want 200; body=%q", w.Code, w.Body.String())
	}
	// If a lazy rebuild path is hit the decrypted plaintext would be rebuilt;
	// the encrypted blob must not remain on disk after consumption.
	if _, err := os.Stat(payload.Stage2BlobPath(dataDir, token)); !os.IsNotExist(err) {
		t.Fatalf("stage blob should be removed after consumption, err=%v", err)
	}

	w = stageGet(t, s, "/stage/"+token+"?s="+sig)
	if w.Code != http.StatusGone {
		t.Fatalf("second fetch status = %d, want 410 (single-use)", w.Code)
	}
}

func TestStageServeConcurrentConsumesOnce(t *testing.T) {
	dataDir := t.TempDir()
	s, token, sig, _, _ := stageFixture(t, dataDir, time.Now().Add(time.Hour))

	results := make(chan int, 2)
	done := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			w := stageGet(t, s, "/stage/"+token+"?s="+sig)
			results <- w.Code
		}()
	}
	for i := 0; i < 2; i++ {
		<-done
	}
	close(results)

	ok, gone := 0, 0
	for code := range results {
		switch code {
		case http.StatusOK:
			ok++
		case http.StatusGone:
			gone++
		}
	}
	if ok != 1 || gone != 1 {
		t.Fatalf("expected exactly one 200 + one 410, got ok=%d gone=%d", ok, gone)
	}

	// The loser must leave nothing behind. The claim now precedes every disk
	// access, so a request that served no bytes cannot observe the winner's
	// deleted blob and fall through to the lazy rebuild — which in production
	// (a listener exists, so the build succeeds) minted a second registration
	// secret and wrote a second encrypted stage-2 that nothing ever removed.
	if _, err := os.Stat(payload.Stage2BlobPath(dataDir, token)); !os.IsNotExist(err) {
		t.Fatalf("orphaned stage-2 blob left by a request that served nothing (err=%v)", err)
	}
}

// TestStageServeFailedBuildKeepsTokenUsable pins the other half of claiming
// early: the stage-2 build runs BEFORE anything is served, so a transient
// failure must release the claim instead of burning the single-use URL. The
// stager holds exactly one URL; consuming it on a request that returned no
// payload strands the implant permanently.
func TestStageServeFailedBuildKeepsTokenUsable(t *testing.T) {
	dataDir := t.TempDir()
	s, token, sig, _, _ := stageFixture(t, dataDir, time.Now().Add(time.Hour))

	// No listener row: the lazy build cannot resolve a C2URL and fails.
	if err := s.db.Where("1 = 1").Delete(&db.Listener{}).Error; err != nil {
		t.Fatalf("clear listeners: %v", err)
	}
	// Force the rebuild path by removing the pre-written blob, as a cold first
	// fetch would hit.
	if err := os.Remove(payload.Stage2BlobPath(dataDir, token)); err != nil {
		t.Fatalf("remove blob: %v", err)
	}

	w := stageGet(t, s, "/stage/"+token+"?s="+sig)
	if w.Code != http.StatusNotFound {
		t.Fatalf("failed-build status = %d, want 404; body=%q", w.Code, w.Body.String())
	}

	// The token must still be unused, so the stager's retry can still succeed
	// once the listener is restored.
	var tok db.StagerToken
	if err := s.db.First(&tok, "token = ?", token).Error; err != nil {
		t.Fatalf("reload token: %v", err)
	}
	if tok.Used {
		t.Fatal("a request that served no payload consumed the single-use token")
	}
}
