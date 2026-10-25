package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

type apiKeyResponse struct {
	ID           uint   `json:"id"`
	Name         string `json:"name"`
	Prefix       string `json:"prefix"`
	LastUsed     string `json:"last_used,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Active       bool   `json:"active"`
	Scopes       string `json:"scopes,omitempty"`
	AllowedCIDRs string `json:"allowed_cidrs,omitempty"`
	// LegacyFullAccess flags pre-scope keys (empty scopes = owner's full
	// powers) so operators and automation can spot and rotate them.
	// UnrestrictedSource flags keys with no CIDR allowlist (any source).
	LegacyFullAccess   bool   `json:"legacy_full_access"`
	UnrestrictedSource bool   `json:"unrestricted_source"`
	CreatedAt          string `json:"created_at"`
}

func generateAPIKey() (plaintext string, hash string, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("generate random key: %w", err)
	}
	plaintext = "fc2_" + hex.EncodeToString(b)
	h := sha256.Sum256([]byte(plaintext))
	hash = hex.EncodeToString(h[:])
	prefix = plaintext[:12]
	return plaintext, hash, prefix, nil
}

// keyCreatorMayGrant ensures a key never exceeds its creator's own reach:
// every scope must be in the creator's role grants. bulk_export is a
// key-admin privilege (the route already requires settings.write), so any
// creator reaching this handler may grant it. Admins may grant everything.
func (s *Server) keyCreatorMayGrant(c *gin.Context, scopesCSV string) bool {
	if c.GetString("user_role") == db.RoleAdmin {
		return true
	}
	role := c.GetString("user_role")
	for _, p := range strings.Split(scopesCSV, ",") {
		if p == "" || p == db.PermBulkExport {
			continue
		}
		if !db.RoleHasPermissionDB(s.db, role, p) {
			return false
		}
	}
	return true
}

func (s *Server) handleCreateAPIKey(c *gin.Context) {
	var req struct {
		Name         string   `json:"name"`
		ExpiresAt    string   `json:"expires_at,omitempty"`
		Scopes       []string `json:"scopes,omitempty"`
		AllowedCIDRs string   `json:"allowed_cidrs,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, "name is required")
		return
	}
	// Scopes are a subset of Perm* values; empty = legacy full-access key.
	// A key can never grant more than its creator holds.
	scopes, err := db.NormalizeKeyScopes(req.Scopes)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	if scopes != "" && !s.keyCreatorMayGrant(c, scopes) {
		respondError(c, http.StatusForbidden, "cannot grant scopes the creator lacks")
		return
	}
	cidrs, err := db.NormalizeKeyCIDRs(req.AllowedCIDRs)
	if err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	userID, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}

	plaintext, hash, prefix, err := generateAPIKey()
	if err != nil {
		slog.Error("Failed to generate API key", "error", err)
		respondError(c, http.StatusInternalServerError, "failed to generate key")
		return
	}

	apiKey := db.APIKey{
		UserID:       userID,
		Name:         req.Name,
		KeyHash:      hash,
		Prefix:       prefix,
		Scopes:       scopes,
		AllowedCIDRs: cidrs,
		Active:       true,
		CreatedAt:    time.Now(),
	}

	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			respondError(c, http.StatusBadRequest, "invalid expires_at format (use RFC3339)")
			return
		}
		apiKey.ExpiresAt = t
	}

	if err := s.db.Create(&apiKey).Error; err != nil {
		slog.Error("Failed to create API key", "error", err)
		respondError(c, http.StatusInternalServerError, "failed to create key")
		return
	}

	s.LogAuditRecord(c, "api_key_create", "user", fmt.Sprintf("%d", userID), fmt.Sprintf("API key created: %s", req.Name), true, nil)

	respond(c, gin.H{
		"id":            apiKey.ID,
		"name":          apiKey.Name,
		"key":           plaintext,
		"prefix":        prefix,
		"scopes":        apiKey.Scopes,
		"allowed_cidrs": apiKey.AllowedCIDRs,
		"created_at":    apiKey.CreatedAt.Format(time.RFC3339),
		"expires_at":    apiKey.ExpiresAt.Format(time.RFC3339),
		"message":       "Store this key securely - it will not be shown again",
	})
}

func (s *Server) handleListAPIKeys(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}

	var keys []db.APIKey
	if err := s.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&keys).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to list keys")
		return
	}

	resp := make([]apiKeyResponse, len(keys))
	for i, k := range keys {
		resp[i] = apiKeyResponse{
			ID:                 k.ID,
			Name:               k.Name,
			Prefix:             k.Prefix,
			Active:             k.Active,
			Scopes:             k.Scopes,
			AllowedCIDRs:       k.AllowedCIDRs,
			LegacyFullAccess:   strings.TrimSpace(k.Scopes) == "",
			UnrestrictedSource: strings.TrimSpace(k.AllowedCIDRs) == "",
			CreatedAt:          k.CreatedAt.Format(time.RFC3339),
		}
		if !k.LastUsed.IsZero() {
			resp[i].LastUsed = k.LastUsed.Format(time.RFC3339)
		}
		if !k.ExpiresAt.IsZero() {
			resp[i].ExpiresAt = k.ExpiresAt.Format(time.RFC3339)
		}
	}

	respond(c, resp)
}

func (s *Server) handleRevokeAPIKey(c *gin.Context) {
	id := c.Param("id")
	userID, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}

	var key db.APIKey
	if err := s.db.Where("id = ? AND user_id = ?", id, userID).First(&key).Error; err != nil {
		respondError(c, http.StatusNotFound, "API key not found")
		return
	}

	if err := s.db.Model(&key).Update("active", false).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to revoke key")
		return
	}

	s.LogAuditRecord(c, "api_key_revoke", "user", fmt.Sprintf("%d", userID), fmt.Sprintf("API key revoked: %s (id=%s)", key.Name, id), true, nil)

	respond(c, gin.H{"message": "API key revoked"})
}

func (s *Server) handleRotateAPIKey(c *gin.Context) {
	id := c.Param("id")
	userID, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}

	var oldKey db.APIKey
	if err := s.db.Where("id = ? AND user_id = ?", id, userID).First(&oldKey).Error; err != nil {
		respondError(c, http.StatusNotFound, "API key not found")
		return
	}

	if err := s.db.Model(&oldKey).Update("active", false).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to deactivate old key")
		return
	}

	plaintext, hash, prefix, err := generateAPIKey()
	if err != nil {
		slog.Error("Failed to generate rotated API key", "error", err)
		respondError(c, http.StatusInternalServerError, "failed to generate key")
		return
	}

	newKey := db.APIKey{
		UserID:       oldKey.UserID,
		Name:         oldKey.Name,
		KeyHash:      hash,
		Prefix:       prefix,
		Scopes:       oldKey.Scopes,
		AllowedCIDRs: oldKey.AllowedCIDRs,
		ExpiresAt:    oldKey.ExpiresAt,
		Active:       true,
		CreatedAt:    time.Now(),
	}

	if err := s.db.Create(&newKey).Error; err != nil {
		slog.Error("Failed to create rotated API key", "error", err)
		respondError(c, http.StatusInternalServerError, "failed to create rotated key")
		return
	}

	s.LogAuditRecord(c, "api_key_rotate", "user", fmt.Sprintf("%d", userID), fmt.Sprintf("API key rotated: %s (old_id=%s, new_id=%d)", oldKey.Name, id, newKey.ID), true, nil)

	respond(c, gin.H{
		"id":         newKey.ID,
		"name":       newKey.Name,
		"key":        plaintext,
		"prefix":     prefix,
		"created_at": newKey.CreatedAt.Format(time.RFC3339),
		"expires_at": newKey.ExpiresAt.Format(time.RFC3339),
		"message":    "Store this key securely - it will not be shown again. Old key has been revoked.",
	})
}
