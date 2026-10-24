package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// handleUsersPage shows user management
func (s *Server) handleUsersPage(c *gin.Context) {
	var users []db.User
	if err := s.db.Order("created_at desc").Limit(1000).Find(&users).Error; err != nil {
		slog.Error("Failed to list users", "err", err)
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":           "ForgeC2 - User Management",
		"ActiveNav":       "settings",
		"Users":           users,
		"AllRoles":        db.GetAllRoles(),
		"AllPermissions":  db.GetAllPermissions(),
		"RolePermissions": db.RolePermissionsMap,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// handleAddUser creates a new user (admin only)
func (s *Server) handleAddUser(c *gin.Context) {
	if !s.requireAdmin(c) {
		return
	}
	username := c.PostForm("username")
	password := c.PostForm("password")
	role := c.PostForm("role")

	if username == "" || password == "" {
		respondError(c, http.StatusBadRequest, "Username and password required")
		return
	}
	if err := s.validatePasswordComplexity(password); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	validRoles := map[string]bool{
		db.RoleAdmin: true,
		db.RoleUser:  true,
	}
	if !validRoles[role] {
		role = db.RoleUser
	}

	hash, err := middleware.HashPassword(password)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "Failed to hash password")
		return
	}

	var user db.User
	err = s.db.Transaction(func(tx *gorm.DB) error {
		user = db.User{
			Username:     username,
			PasswordHash: hash,
			Role:         role,
			IsActive:     true,
		}
		return tx.Create(&user).Error
	})
	if err != nil {
		respondError(c, http.StatusConflict, "Username already exists")
		return
	}

	currentUser, _ := c.Get("user")
	usernameCU, _ := currentUser.(string)
	s.LogAuditRecord(c, "user_create", "auth", usernameCU,
		fmt.Sprintf("Created user %s with role %s", username, role), true, nil)
	slog.Info("User created", "username", username, "role", role, "user", currentUser)

	respond(c, gin.H{"message": fmt.Sprintf("User %s created", username)})
}

// handleToggleUser enables/disables a user
func (s *Server) handleToggleUser(c *gin.Context) {
	// Only admins can toggle users
	if !s.requireAdmin(c) {
		return
	}
	idStr := c.Param("id")
	var user db.User
	if !s.findOrFail(c, &user, idStr, "User") {
		return
	}

	currentUser, _ := c.Get("user")

	// Prevent disabling yourself
	if currentUser == user.Username {
		respondError(c, http.StatusBadRequest, "Cannot disable your own account")
		return
	}

	if err := s.db.Model(&user).Update("is_active", gorm.Expr("NOT is_active")).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to toggle user")
		return
	}
	user.IsActive = !user.IsActive
	// Evict the shared auth cache so the change takes effect on the next
	// request instead of at TTL expiry (up to 5 minutes of stale access).
	middleware.InvalidateUserCache(user.ID)
	if !user.IsActive {
		// Bump ForceLogoutAt AND revoke live session rows in one
		// transaction. Without the session revoke, a later re-enable +
		// login clears force_logout_at and pre-disable JWTs would
		// resurrect (session row never marked revoked).
		now := time.Now()
		err := s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&user).Update("force_logout_at", now).Error; err != nil {
				return err
			}
			return tx.Model(&db.UserSession{}).
				Where("user_id = ? AND revoked_at = ?", user.ID, time.Time{}).
				Update("revoked_at", now).Error
		})
		if err != nil {
			slog.Error("Failed to bump force_logout_at / revoke sessions on disable", "user_id", user.ID, "err", err)
			respondError(c, http.StatusInternalServerError, "failed to disable user")
			return
		}
		user.ForceLogoutAt = now
	}
	status := "enabled"
	if !user.IsActive {
		status = "disabled"
	}
	usernameTU, _ := currentUser.(string)
	s.LogAuditRecord(c, "user_toggle", "auth", usernameTU,
		fmt.Sprintf("%s account %s", status, user.Username), true, nil)
	slog.Info("User toggled", "username", user.Username, "active", !user.IsActive)

	respond(c, gin.H{"message": fmt.Sprintf("User %s", status)})
}

// handleEditUser updates username/role (admin only)
func (s *Server) handleEditUser(c *gin.Context) {
	idStr := c.Param("id")
	if !s.requireAdmin(c) {
		return
	}

	var user db.User
	if !s.findOrFail(c, &user, idStr, "User") {
		return
	}

	username := c.PostForm("username")
	role := c.PostForm("role")

	updates := make(map[string]interface{})
	if username != "" && username != user.Username {
		// Check uniqueness
		var dup db.User
		if s.db.Where("username = ?", username).First(&dup).Error == nil {
			respondError(c, http.StatusConflict, "Username already taken")
			return
		}
		updates["username"] = username
	}
	if role != "" && role != user.Role {
		validRoles := map[string]bool{
			db.RoleAdmin: true,
			db.RoleUser:  true,
		}
		if !validRoles[role] {
			respondError(c, http.StatusBadRequest, "Invalid role")
			return
		}
		updates["role"] = role
	}

	if len(updates) == 0 {
		respond(c, gin.H{"message": "No changes"})
		return
	}

	if err := s.db.Model(&user).Updates(updates).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to update user")
		return
	}
	// Role/username changes must not linger in the shared auth cache.
	middleware.InvalidateUserCache(user.ID)
	currentUser, _ := c.Get("user")
	usernameEU, _ := currentUser.(string)
	s.LogAuditRecord(c, "user_edit", "auth", usernameEU,
		fmt.Sprintf("Edited user %s: %v", user.Username, updates), true, nil)
	slog.Info("User edited", "user_id", idStr, "updates", updates, "user", currentUser)

	respond(c, gin.H{"message": "User updated"})
}

// handleForceLogoutUser invalidates all sessions for a user (admin only)
func (s *Server) handleForceLogoutUser(c *gin.Context) {
	idStr := c.Param("id")
	if !s.requireAdmin(c) {
		return
	}

	currentUser, _ := c.Get("user")
	var target db.User
	if !s.findOrFail(c, &target, idStr, "User") {
		return
	}

	if currentUser == target.Username {
		respondError(c, http.StatusBadRequest, "Cannot force-logout yourself")
		return
	}

	// Set ForceLogoutAt AND revoke every live session row atomically.
	// Without the session revoke, a later login clears force_logout_at
	// (handlers_auth.go) and pre-force-logout JWTs would resurrect: the
	// auth middleware checks force_logout_at before isSessionRevoked, and
	// the session row was never marked revoked.
	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&target).Update("force_logout_at", now).Error; err != nil {
			return err
		}
		return tx.Model(&db.UserSession{}).
			Where("user_id = ? AND revoked_at = ?", target.ID, time.Time{}).
			Update("revoked_at", now).Error
	})
	if err != nil {
		slog.Error("Force logout transaction failed", "user_id", target.ID, "err", err)
		respondError(c, http.StatusInternalServerError, "failed to force logout user")
		return
	}
	// Evict the shared auth cache: without this the stale cached record
	// (with the old ForceLogoutAt) keeps sessions alive until TTL expiry.
	middleware.InvalidateUserCache(target.ID)

	usernameFL, _ := currentUser.(string)
	s.LogAuditRecord(c, "user_force_logout", "auth", usernameFL,
		fmt.Sprintf("Force logged out user %s", target.Username), true, nil)
	slog.Info("User force logged out", "username", target.Username, "user", currentUser)

	respond(c, gin.H{"message": fmt.Sprintf("Force logged out %s", target.Username)})
}

// handleDeleteUser removes a user (admin only)
func (s *Server) handleDeleteUser(c *gin.Context) {
	idStr := c.Param("id")
	currentUser, _ := c.Get("user")

	if !s.requireAdmin(c) {
		return
	}

	var user db.User
	if !s.findOrFail(c, &user, idStr, "User") {
		return
	}

	// Prevent deleting yourself
	if currentUser == user.Username {
		respondError(c, http.StatusBadRequest, "Cannot delete your own account")
		return
	}

	// Use transaction to clean up associated data
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// Clean up associated data
		if err := tx.Where("user_id = ?", user.ID).Delete(&db.UserSession{}).Error; err != nil {
			slog.Error("Failed to clean up user sessions", "user_id", user.ID, "err", err)
			return err
		}
		if err := tx.Where("user_id = ?", user.ID).Delete(&db.APIKey{}).Error; err != nil {
			slog.Error("Failed to clean up API keys", "user_id", user.ID, "err", err)
			return err
		}

		// Delete the user
		return tx.Delete(&user).Error
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, "Failed to delete user")
		return
	}
	// Evict the shared auth cache: the deleted user's cached record would
	// otherwise keep their sessions alive until TTL expiry.
	middleware.InvalidateUserCache(user.ID)

	usernameDU, _ := currentUser.(string)
	s.LogAuditRecord(c, "user_delete", "auth", usernameDU,
		fmt.Sprintf("Deleted user %s", user.Username), true, nil)
	slog.Info("User deleted", "username", user.Username, "user", currentUser)

	respond(c, gin.H{"message": fmt.Sprintf("User %s deleted", user.Username)})
}

// handleSetUserPassword (admin sets password for user)
func (s *Server) handleSetUserPassword(c *gin.Context) {
	idStr := c.Param("id")
	password := c.PostForm("password")

	if !s.requireAdmin(c) {
		return
	}
	if err := s.validatePasswordComplexity(password); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := middleware.HashPassword(password)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "Hash failed")
		return
	}

	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid user ID")
		return
	}
	result := s.db.Model(&db.User{}).Where("id = ?", id).Update("password_hash", hash)
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, "Database error")
		return
	}
	if result.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, "User not found")
		return
	}

	if err := s.revokeAllUserSessions(uint(id)); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to revoke sessions")
		return
	}
	currentUser, _ := c.Get("user")
	usernameSU, _ := currentUser.(string)
	s.LogAuditRecord(c, "user_password_set", "auth", usernameSU,
		fmt.Sprintf("Set password for user ID %d", id), true, nil)

	respond(c, gin.H{"message": "Password updated"})
}
