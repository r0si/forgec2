package server

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (s *Server) handleSettingsPage(c *gin.Context) {
	type settingsCounts struct {
		TotalAgents    int64
		OnlineAgents   int64
		PendingTasks   int64
		CompletedTasks int64
		FailedTasks    int64
		TotalAudits    int64
		TotalCreds     int64
		TotalTokens    int64
		TotalSocks     int64
		TotalListeners int64
	}

	// One read window: every s.cfg field this page renders is copied under
	// configMu.RLock, so the page shows a coherent snapshot and the beacon /
	// reloader paths that hold the write lock cannot tear a value mid-render
	// (notably the Malleable header maps, which used to be handed to the
	// template by reference while a save replaced them).
	s.configMu.RLock()
	cfgSnap := struct {
		dbPath, jwtSecret, certFile                    string
		defaultInterval, defaultJitter                 int
		defaultSkipTLS                                 bool
		defaultUA, workingStart, workingEnd, workingTZ string
		serverPort                                     int
		serverHost                                     string
		tlsEnabled, tcpEnabled                         bool
		tcpAddr, logLevel                              string
		offlineThreshold, sessionMaxAge, cleanupRet    int
		malEnabled                                     bool
		malStatus                                      int
		malCT, malPrepend, malAppend                   string
		malReqPrepend, malReqAppend                    string
		malHeaders, malReqHeaders                      map[string]string
	}{
		dbPath:           s.cfg.Database.Path,
		jwtSecret:        s.cfg.Server.JWTSecret,
		certFile:         s.cfg.Server.CertFile,
		defaultInterval:  s.cfg.Implant.DefaultInterval,
		defaultJitter:    s.cfg.Implant.DefaultJitter,
		defaultSkipTLS:   s.cfg.Implant.DefaultSkipTLS,
		defaultUA:        s.cfg.Implant.DefaultUA,
		workingStart:     s.cfg.Implant.DefaultWorkingStart,
		workingEnd:       s.cfg.Implant.DefaultWorkingEnd,
		workingTZ:        s.cfg.Implant.DefaultWorkingTZ,
		serverPort:       s.cfg.Server.Port,
		serverHost:       s.cfg.Server.Host,
		tlsEnabled:       s.cfg.Server.TLSEnabled,
		tcpEnabled:       s.cfg.Server.TCPEnabled,
		tcpAddr:          s.cfg.Server.TCPAddr,
		logLevel:         s.cfg.Logging.Level,
		offlineThreshold: s.cfg.Server.OfflineThreshold,
		sessionMaxAge:    s.cfg.Server.SessionMaxAgeHours,
		cleanupRet:       s.cfg.Server.CleanupRetentionDays,
		malEnabled:       s.cfg.Malleable.Enabled,
		malStatus:        s.cfg.Malleable.StatusCode,
		malCT:            s.cfg.Malleable.ContentType,
		malPrepend:       s.cfg.Malleable.Prepend,
		malAppend:        s.cfg.Malleable.Append,
		malReqPrepend:    s.cfg.Malleable.RequestPrepend,
		malReqAppend:     s.cfg.Malleable.RequestAppend,
		malHeaders:       s.cfg.Malleable.Headers,
		malReqHeaders:    s.cfg.Malleable.RequestHeaders,
	}
	// Copied out so the template never reads a map the next save could replace.
	malHeaders := make(map[string]string, len(cfgSnap.malHeaders))
	for k, v := range cfgSnap.malHeaders {
		malHeaders[k] = v
	}
	malReqHeaders := make(map[string]string, len(cfgSnap.malReqHeaders))
	for k, v := range cfgSnap.malReqHeaders {
		malReqHeaders[k] = v
	}
	s.configMu.RUnlock()

	var counts settingsCounts
	offlineCutoff := time.Now().Add(-s.offlineThreshold())
	s.db.Raw(`
		SELECT
			(SELECT COUNT(*) FROM implants WHERE deleted_at IS NULL) AS total_agents,
			(SELECT COUNT(*) FROM implants WHERE last_seen > ? AND deleted_at IS NULL) AS online_agents,
			(SELECT COUNT(*) FROM tasks WHERE status = 'pending') AS pending_tasks,
			(SELECT COUNT(*) FROM tasks WHERE status = 'completed') AS completed_tasks,
			(SELECT COUNT(*) FROM tasks WHERE status = 'failed') AS failed_tasks,
			(SELECT COUNT(*) FROM audit_logs) AS total_audits,
			(SELECT COUNT(*) FROM credential_entries) AS total_creds,
			(SELECT COUNT(*) FROM token_entries) AS total_tokens,
			(SELECT COUNT(*) FROM socks_sessions) AS total_socks,
			(SELECT COUNT(*) FROM listeners) AS total_listeners
	`, offlineCutoff).Scan(&counts)

	var dbSize int64
	if fi, err := os.Stat(cfgSnap.dbPath); err == nil {
		dbSize = fi.Size()
	}

	uptime := time.Since(s.startTime)
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	jwtMasked := ""
	if len(cfgSnap.jwtSecret) > 0 {
		jwtMasked = "****"
	}

	currentUser, _ := c.Get("user")
	currentRole, _ := c.Get("user_role")
	currentUserID, _ := c.Get("user_id")

	var totalUsers int64
	if err := s.db.Model(&db.User{}).Count(&totalUsers).Error; err != nil {
		slog.Error("Failed to count users", "err", err)
	}

	var profileUser db.User
	profileInfo := gin.H{}
	if currentUserID != nil {
		if uid, ok := currentUserID.(uint); ok {
			if err := s.db.First(&profileUser, uid).Error; err == nil {
				profileInfo = gin.H{
					"username":      profileUser.Username,
					"role":          profileUser.Role,
					"last_activity": profileUser.LastActivity,
					"last_login":    profileUser.LastLogin,
					"created_at":    profileUser.CreatedAt,
				}
			}
		}
	}

	stats := s.getNavStats(c)
	data := gin.H{
		"Title":                   "ForgeC2 - Settings",
		"ActiveNav":               "settings",
		"CurrentUsername":         currentUser,
		"CurrentUserRole":         currentRole,
		"CurrentUserId":           currentUserID,
		"ProfileInfo":             profileInfo,
		"TotalUsers":              totalUsers,
		"DefaultInterval":         cfgSnap.defaultInterval,
		"DefaultJitter":           cfgSnap.defaultJitter,
		"DefaultSkipTLS":          cfgSnap.defaultSkipTLS,
		"DefaultUA":               cfgSnap.defaultUA,
		"ServerPort":              cfgSnap.serverPort,
		"ServerHost":              cfgSnap.serverHost,
		"TLSEnabled":              cfgSnap.tlsEnabled,
		"TCPEnabled":              cfgSnap.tcpEnabled,
		"TCPAddr":                 cfgSnap.tcpAddr,
		"LogLevel":                cfgSnap.logLevel,
		"ServerAddress":           fmt.Sprintf("%s:%d", cfgSnap.serverHost, cfgSnap.serverPort),
		"DatabasePath":            "***",
		"DatabaseSize":            dbSize,
		"DataDir":                 "***",
		"JWTMasked":               jwtMasked,
		"TotalAgents":             counts.TotalAgents,
		"OnlineAgents":            counts.OnlineAgents,
		"PendingTasks":            counts.PendingTasks,
		"CompletedTasks":          counts.CompletedTasks,
		"FailedTasks":             counts.FailedTasks,
		"TotalAudits":             counts.TotalAudits,
		"TotalCredentials":        counts.TotalCreds,
		"TotalTokens":             counts.TotalTokens,
		"TotalSocks":              counts.TotalSocks,
		"TotalListeners":          counts.TotalListeners,
		"Uptime":                  uptime,
		"GoVersion":               runtime.Version(),
		"Goroutines":              runtime.NumGoroutine(),
		"AllocMem":                int64(m.Alloc),
		"TotalAllocMem":           int64(m.TotalAlloc),
		"NumCPU":                  runtime.NumCPU(),
		"GOOS":                    runtime.GOOS,
		"GOARCH":                  runtime.GOARCH,
		"OfflineThreshold":        cfgSnap.offlineThreshold,
		"SessionMaxAge":           cfgSnap.sessionMaxAge,
		"CleanupRetention":        cfgSnap.cleanupRet,
		"MalleableEnabled":        cfgSnap.malEnabled,
		"MalleableStatus":         cfgSnap.malStatus,
		"MalleableCT":             cfgSnap.malCT,
		"MalleableHeaders":        malHeaders,
		"MalleablePrepend":        cfgSnap.malPrepend,
		"MalleableAppend":         cfgSnap.malAppend,
		"MalleableRequestPrepend": cfgSnap.malReqPrepend,
		"MalleableRequestAppend":  cfgSnap.malReqAppend,
		"MalleableRequestHeaders": malReqHeaders,
		"WorkingStart":            cfgSnap.workingStart,
		"WorkingEnd":              cfgSnap.workingEnd,
		"WorkingTZ":               cfgSnap.workingTZ,
	}
	for k, v := range stats {
		data[k] = v
	}

	certPath := cfgSnap.certFile
	if certPath == "" {
		certPath = "data/certs/server.crt"
	}
	if certPEM, err := os.ReadFile(certPath); err == nil {
		if block, _ := pem.Decode(certPEM); block != nil {
			if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
				data["CertSubject"] = cert.Subject.CommonName
				data["CertIssuer"] = cert.Issuer.CommonName
				data["CertExpiresAt"] = cert.NotAfter
				data["CertExpiresIn"] = int(time.Until(cert.NotAfter).Hours() / 24)
				data["CertDNSNames"] = cert.DNSNames
				data["CertSelfSigned"] = cert.Subject.CommonName == cert.Issuer.CommonName
			}
		}
	}

	s.renderPageOrJSON(c, data)
}

// validateAndSaveConfigLocked validates the running config before persisting
// it so an invalid value entered via the UI cannot be written to disk and
// break startup. The caller MUST hold s.configMu for writing: the mutation,
// the validation and the save then form one atomic window, and the file
// watcher's reload (which takes the same lock) can never observe a
// half-applied form of the operator's edit.
func (s *Server) validateAndSaveConfigLocked(c *gin.Context, action string) bool { // config-lock: caller holds the write lock
	if err := s.cfg.Validate(); err != nil {
		slog.Warn("Config validation failed before save", "action", action, "err", err)
		respondError(c, http.StatusBadRequest, "Config validation failed")
		return false
	}
	if err := s.cfg.Save(s.configPath); err != nil {
		slog.Error("Failed to save config", "action", action, "err", err)
		respondError(c, http.StatusInternalServerError, "Failed to save config")
		return false
	}
	// The file watcher applies the change asynchronously; audit the operator
	// intent now so saves are traceable even if reload rejects them.
	s.LogAuditRecord(c, action, "settings", "", "saved via console", true, nil)
	return true
}

func (s *Server) handleSaveAgentConfig(c *gin.Context) {
	interval := c.PostForm("interval")
	jitter := c.PostForm("jitter")
	userAgent := c.PostForm("user_agent")
	skipTLS := c.PostForm("skip_tls")
	workingStart := c.PostForm("working_start")
	workingEnd := c.PostForm("working_end")
	workingTZ := c.PostForm("working_tz")

	s.configMu.Lock()
	if interval != "" {
		var intInterval int
		if _, err := fmt.Sscanf(interval, "%d", &intInterval); err == nil && intInterval >= 0 {
			s.cfg.Implant.DefaultInterval = intInterval
		}
	}

	if jitter != "" {
		var intJitter int
		if _, err := fmt.Sscanf(jitter, "%d", &intJitter); err == nil && intJitter >= 0 && intJitter <= 100 {
			s.cfg.Implant.DefaultJitter = intJitter
		}
	}

	if userAgent != "" {
		if len(userAgent) > 256 || !isPrintableASCII(userAgent) {
			s.configMu.Unlock()
			respondError(c, http.StatusBadRequest, "Invalid user agent")
			return
		}
		s.cfg.Implant.DefaultUA = userAgent
	}

	s.cfg.Implant.DefaultSkipTLS = skipTLS == "true" || skipTLS == "1"

	s.cfg.Implant.DefaultWorkingStart = workingStart
	s.cfg.Implant.DefaultWorkingEnd = workingEnd
	s.cfg.Implant.DefaultWorkingTZ = workingTZ

	if !s.validateAndSaveConfigLocked(c, "agent_config") {
		s.configMu.Unlock()
		return
	}
	intervalOut, jitterOut, skipOut := s.cfg.Implant.DefaultInterval, s.cfg.Implant.DefaultJitter, s.cfg.Implant.DefaultSkipTLS
	s.configMu.Unlock()

	slog.Info("Agent config updated", "interval", intervalOut, "jitter", jitterOut, "skip_tls", skipOut)
	respond(c, gin.H{"message": "Agent configuration saved successfully"})
}

// consumePasswordChangeSlot claims the caller's password-change cooldown. It
// returns the remaining wait and false while a previous attempt is still inside
// the window.
//
// Every attempt claims the slot, not just successful ones. Arming the window
// only on success meant a caller holding a valid session but not the current
// password could guess it an unlimited number of times: each wrong guess was
// rejected, yet nothing ever recorded the attempt.
func (s *Server) consumePasswordChangeSlot(userID uint) (time.Duration, bool) {
	now := time.Now()
	s.pwdChangeTimesMu.Lock()
	defer s.pwdChangeTimesMu.Unlock()
	if lastChange, exists := s.pwdChangeTimes[userID]; exists {
		if elapsed := now.Sub(lastChange); elapsed < PasswordChangeCooldown {
			return PasswordChangeCooldown - elapsed, false
		}
	}
	s.pwdChangeTimes[userID] = now
	return 0, true
}

func (s *Server) handleChangePassword(c *gin.Context) {
	// A missing user_id used to leave the lookup unscoped (First(&user, nil)
	// matched the first row), so the "current password" was checked against an
	// arbitrary account and that account's password was the one rotated.
	uid, ok := currentUserID(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "unauthorized")
		return
	}

	if remaining, ok := s.consumePasswordChangeSlot(uid); !ok {
		respondError(c, http.StatusTooManyRequests,
			fmt.Sprintf("Password change rate limited. Try again in %d seconds.", int(remaining.Seconds())))
		return
	}

	current := c.PostForm("current_password")
	newPass := c.PostForm("new_password")
	confirm := c.PostForm("confirm_password")

	if newPass != confirm {
		respondError(c, http.StatusBadRequest, "Passwords do not match")
		return
	}
	if err := s.validatePasswordComplexity(newPass); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}

	userID := uid
	var user db.User
	if err := s.db.First(&user, userID).Error; err != nil {
		respondError(c, http.StatusUnauthorized, "User not found")
		return
	}

	if !middleware.CheckPassword(user.PasswordHash, current) {
		respondError(c, http.StatusUnauthorized, "Current password incorrect")
		return
	}

	var recentHistory []db.PasswordHistory
	if err := s.db.Where("user_id = ?", user.ID).Order("created_at DESC").Limit(5).Find(&recentHistory).Error; err != nil {
		slog.Error("Failed to query password history", "err", err)
	}
	for _, entry := range recentHistory {
		if middleware.CheckPassword(entry.PasswordHash, newPass) {
			respondError(c, http.StatusBadRequest, "Cannot reuse a recent password")
			return
		}
	}

	hash, err := middleware.HashPassword(newPass)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "Hash failed")
		return
	}

	if err := s.db.Model(&user).Update("password_hash", hash).Error; err != nil {
		slog.Error("Failed to update password hash", "user_id", user.ID, "err", err)
		respondError(c, http.StatusInternalServerError, "Failed to update password")
		return
	}

	if err := s.db.Create(&db.PasswordHistory{
		UserID:       user.ID,
		PasswordHash: hash,
		CreatedAt:    time.Now(),
	}).Error; err != nil {
		slog.Error("Failed to record password history", "user_id", user.ID, "err", err)
	}

	var count int64
	if err := s.db.Model(&db.PasswordHistory{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
		slog.Error("Failed to count password history", "user_id", user.ID, "err", err)
	}
	if count > PasswordHistoryMax {
		var oldest []db.PasswordHistory
		if err := s.db.Where("user_id = ?", user.ID).Order("created_at ASC").Limit(int(count - PasswordHistoryMax)).Find(&oldest).Error; err != nil {
			slog.Error("Failed to query oldest password history", "err", err)
		}
		ids := make([]uint, len(oldest))
		for i, h := range oldest {
			ids[i] = h.ID
		}
		s.db.Delete(&db.PasswordHistory{}, ids)
	}

	s.LogAuditRecord(c, "password_change", "auth", user.Username, "Password changed", true, nil)

	if err := s.revokeAllUserSessions(user.ID); err != nil {
		// The password is already rotated and audited at this point, so a bare
		// 500 would tell the operator the change failed and invite a retry
		// against a password that no longer exists. Say exactly what landed.
		s.LogAuditRecord(c, "password_change_sessions_revoked", "auth", user.Username,
			"Password changed but existing sessions could not be revoked: "+err.Error(), false, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"success":          false,
			"error":            "password changed, but existing sessions could not be revoked",
			"password_changed": true,
			"sessions_revoked": false,
		})
		return
	}

	respond(c, gin.H{})
}

func (s *Server) handleSaveServerConfig(c *gin.Context) {
	logLevel := c.PostForm("log_level")
	tcpEnabled := c.PostForm("tcp_enabled")
	tcpAddr := c.PostForm("tcp_addr")
	offlineThreshold := c.PostForm("offline_threshold")
	sessionMaxAge := c.PostForm("session_max_age")
	cleanupRetention := c.PostForm("cleanup_retention")

	s.configMu.Lock()
	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if logLevel != "" && validLevels[logLevel] {
		s.cfg.Logging.Level = logLevel
	}

	if tcpEnabled != "" {
		s.cfg.Server.TCPEnabled = tcpEnabled == "true" || tcpEnabled == "1"
	}

	if tcpAddr != "" {
		s.cfg.Server.TCPAddr = tcpAddr
	}

	if offlineThreshold != "" {
		if v, err := strconv.Atoi(offlineThreshold); err == nil && v >= MinOfflineThresholdSec && v <= MaxOfflineThresholdSec {
			s.cfg.Server.OfflineThreshold = v
		}
	}

	if sessionMaxAge != "" {
		if v, err := strconv.Atoi(sessionMaxAge); err == nil && v >= MinSessionMaxAgeHours && v <= MaxSessionMaxAgeHours {
			s.cfg.Server.SessionMaxAgeHours = v
		}
	}

	if cleanupRetention != "" {
		if v, err := strconv.Atoi(cleanupRetention); err == nil && v >= MinCleanupRetentionDays && v <= MaxCleanupRetentionDays {
			s.cfg.Server.CleanupRetentionDays = v
		}
	}

	if !s.validateAndSaveConfigLocked(c, "server_config") {
		s.configMu.Unlock()
		return
	}
	levelOut, tcpOut := s.cfg.Logging.Level, s.cfg.Server.TCPEnabled
	s.configMu.Unlock()

	slog.Info("Server config updated", "log_level", levelOut, "tcp_enabled", tcpOut)
	respond(c, gin.H{"message": "Server configuration saved"})
}

func (s *Server) handleSaveMalleableProfile(c *gin.Context) {
	s.configMu.Lock()
	s.cfg.Malleable.Enabled = c.PostForm("enabled") == "true"
	if sc := c.PostForm("status_code"); sc != "" {
		if v, err := strconv.Atoi(sc); err == nil && v >= MinHTTPStatusCode && v <= MaxHTTPStatusCode {
			s.cfg.Malleable.StatusCode = v
		}
	}
	if ct := c.PostForm("content_type"); ct != "" {
		s.cfg.Malleable.ContentType = ct
	}
	s.cfg.Malleable.Prepend = c.PostForm("prepend")
	s.cfg.Malleable.Append = c.PostForm("append")
	s.cfg.Malleable.RequestPrepend = c.PostForm("request_prepend")
	s.cfg.Malleable.RequestAppend = c.PostForm("request_append")

	if headersText := c.PostForm("headers_text"); headersText != "" {
		headers := make(map[string]string)
		for _, line := range strings.Split(headersText, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if idx := strings.Index(line, ":"); idx > 0 {
				k := strings.TrimSpace(line[:idx])
				v := strings.TrimSpace(line[idx+1:])
				if k != "" {
					headers[k] = v
				}
			}
		}
		if len(headers) > 0 {
			s.cfg.Malleable.Headers = headers
		}
	}

	if reqHeadersText := c.PostForm("request_headers_text"); reqHeadersText != "" {
		headers := make(map[string]string)
		for _, line := range strings.Split(reqHeadersText, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if idx := strings.Index(line, ":"); idx > 0 {
				k := strings.TrimSpace(line[:idx])
				v := strings.TrimSpace(line[idx+1:])
				if k != "" {
					headers[k] = v
				}
			}
		}
		if len(headers) > 0 {
			s.cfg.Malleable.RequestHeaders = headers
		}
	}

	if !s.validateAndSaveConfigLocked(c, "malleable_profile") {
		s.configMu.Unlock()
		return
	}
	enabledOut, statusOut := s.cfg.Malleable.Enabled, s.cfg.Malleable.StatusCode
	s.configMu.Unlock()

	slog.Info("Malleable profile saved", "enabled", enabledOut, "status_code", statusOut)
	respond(c, gin.H{"message": "Malleable C2 profile saved"})
}

func (s *Server) handlePurgeTasks(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	daysStr := c.PostForm("days")
	days, err := strconv.Atoi(daysStr)
	if err != nil || days < 1 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	result := s.db.Where("created_at < ?", cutoff).
		Where("status IN ?", []string{"completed", "failed"}).
		Delete(&db.Task{})
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, "Failed to purge tasks")
		return
	}
	slog.Info("Purged old tasks", "count", result.RowsAffected, "older_than_days", days)
	respond(c, gin.H{"message": fmt.Sprintf("Purged %d old tasks", result.RowsAffected)})
}

func (s *Server) handlePurgeAuditLogs(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	daysStr := c.PostForm("days")
	days, err := strconv.Atoi(daysStr)
	if err != nil || days < 1 {
		days = 90
	}
	if days < 90 {
		days = 90
	}
	if days > 730 {
		days = 730
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	scope := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c)

	// Count first so the purge can describe itself: destroying audit history is
	// exactly the operation that must leave a record of its own.
	var toPurge int64
	if err := scope.Where("created_at < ?", cutoff).Count(&toPurge).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "Failed to count purgable audit logs")
		return
	}
	if toPurge > 0 {
		// Written before the delete so it cannot be removed by it: the marker is
		// newer than the cutoff.
		s.LogAuditRecord(c, "audit_purge", "audit", "",
			fmt.Sprintf("purged %d audit rows older than %d days", toPurge, days), true, nil)
	}

	result := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c).
		Where("created_at < ?", cutoff).Delete(&db.AuditLog{})
	if result.Error != nil {
		respondError(c, http.StatusInternalServerError, "Failed to purge audit logs")
		return
	}
	slog.Info("Purged old audit logs", "count", result.RowsAffected, "older_than_days", days)
	respond(c, gin.H{"message": fmt.Sprintf("Purged %d old audit logs", result.RowsAffected)})
}

func (s *Server) handleRegenerateJWT(c *gin.Context) {
	// All storage keys (loot/ExtC2/backup/CSRF and the credential key) are
	// independent of the JWT secret, so rotating the JWT cannot invalidate
	// FC2ENC:/FC2EXT: ciphertext — no data re-encryption is required.

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		slog.Error("Failed to generate JWT secret", "err", err)
		respondError(c, http.StatusInternalServerError, "Failed to generate JWT secret")
		return
	}
	s.configMu.Lock()
	s.cfg.Server.JWTSecret = hex.EncodeToString(b)
	if !s.validateAndSaveConfigLocked(c, "regenerate_jwt") {
		s.configMu.Unlock()
		return
	}
	// InitJWTSecret reads (and, for a short/empty secret, rewrites) the live
	// config, so it runs inside the same lock window as the rotation.
	if err := middleware.InitJWTSecret(s.cfg, s.configPath); err != nil {
		s.configMu.Unlock()
		respondError(c, http.StatusInternalServerError, "Failed to reinitialize JWT secret")
		return
	}
	s.configMu.Unlock()

	slog.Info("JWT secret regenerated (independent encryption keys left untouched)")
	respond(c, gin.H{"message": "JWT secret regenerated successfully"})
}

func (s *Server) handleDBVacuum(c *gin.Context) {
	if !s.requireSQLiteDB(c) {
		return
	}
	var dbSize int64
	rawDB, err := s.db.DB()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "Failed to get database connection")
		return
	}
	if _, err := rawDB.Exec("VACUUM"); err != nil {
		respondError(c, http.StatusInternalServerError, "VACUUM failed")
		return
	}
	if err := s.db.Raw("SELECT page_count * page_size as size FROM pragma_page_count, pragma_page_size").Scan(&dbSize).Error; err != nil {
		slog.Error("Failed to query database size after vacuum", "err", err)
	}
	slog.Info("Database vacuum completed")
	respond(c, gin.H{"message": "Database vacuum completed", "size": dbSize})
}

func (s *Server) handleDownloadConfig(c *gin.Context) {
	role, _ := c.Get("user_role")
	if role != "admin" {
		respondError(c, http.StatusForbidden, "insufficient permissions")
		return
	}
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "Failed to read config")
		return
	}
	redacted := string(data)
	// Exhaustive list of every secret material in config.yaml. The server
	// holds the crown-jewel keys (loot/credential/CSRF/ExtC2/backup encryption, the
	// v2 fleet beacon key, SSH password, extc2 API token) — none may leak via
	// the admin "Download Config" endpoint (S2). Keep this list in sync with
	// config.go's secret fields.
	//
	// The snapshot runs under the config read lock: JWT regeneration mutates
	// Server.JWTSecret under the write lock, and a torn read here would put a
	// half-old/half-new value in the redaction list — leaking the real secret
	// into the download.
	s.configMu.RLock()
	secrets := []string{
		s.cfg.Server.JWTSecret,
		s.cfg.Server.BeaconKey,
		s.cfg.Server.SSHPassword,
		s.cfg.Auth.PasswordHash,
		s.cfg.AI.APIKey,
		s.cfg.Integrations.Slack.BotToken,
		s.cfg.Integrations.Slack.AppToken,
		s.cfg.Integrations.Slack.SigningSecret,
		s.cfg.Crypto.Key,
		s.cfg.Crypto.LootKey,
		s.cfg.Crypto.TotpKey,
		s.cfg.Crypto.CsrfKey,
		s.cfg.Crypto.ExtC2Key,
		s.cfg.Crypto.BackupKey,
		s.cfg.RateLimit.ExtC2.APIToken,
	}
	s.configMu.RUnlock()
	for _, secret := range secrets {
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "****")
		}
	}
	c.Header("Content-Disposition", "attachment; filename=config.yaml")
	c.Data(http.StatusOK, "application/x-yaml", []byte(redacted))
}

// authConfigured reports whether a bootstrap admin password hash exists. The
// field is rewritten by a hot config reload under the write lock, so this
// reads it under the read lock.
func (s *Server) authConfigured() bool {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.cfg != nil && s.cfg.Auth.PasswordHash != ""
}

func (s *Server) handleAuditLogPage(c *gin.Context) {
	stats := s.getNavStats(c)
	var users []db.User
	if err := s.db.Model(&db.User{}).Select("username, role").Where("is_active = ?", true).Limit(100).Find(&users).Error; err != nil {
		slog.Error("Settings: failed to query users for audit page", "err", err)
	}

	data := gin.H{
		"Title":     "ForgeC2 - Security Audit",
		"ActiveNav": "audit",
		"Online":    s.authConfigured(),
		"UserList":  users,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

func (s *Server) handleGetAuditLogs(c *gin.Context) {
	var logs []db.AuditLog
	p := parsePagination(c, 50, 200)
	search := c.DefaultQuery("search", "")
	action := c.DefaultQuery("action", "")
	user := c.DefaultQuery("user", "")

	query := s.auditTenantScope(s.db.Model(&db.AuditLog{}), c).Order("created_at DESC")

	if search != "" {
		query = query.Where("(user LIKE ? ESCAPE '\\' OR resource LIKE ? ESCAPE '\\' OR details LIKE ? ESCAPE '\\')",
			"%"+escapeLike(search)+"%", "%"+escapeLike(search)+"%", "%"+escapeLike(search)+"%")
	}

	if action != "" {
		query = query.Where("action = ?", action)
	}

	if user != "" {
		query = query.Where("user = ?", user)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to count audit logs")
		return
	}

	if err := query.Limit(p.PageSize).Offset(p.Offset).Find(&logs).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to query audit logs")
		return
	}

	respond(c, gin.H{
		"logs":  logs,
		"total": total,
	})
}

func (s *Server) handleGetSettingsWebhooks(c *gin.Context) {
	var cfg db.ServerConfig
	s.db.Where("key = ?", "notification_targets").First(&cfg)
	if cfg.Value == "" {
		respond(c, gin.H{"notifications": []interface{}{}})
		return
	}
	var targets []interface{}
	if err := json.Unmarshal([]byte(cfg.Value), &targets); err != nil {
		slog.Error("Failed to parse stored notification targets", "err", err)
		respond(c, gin.H{"notifications": []interface{}{}})
		return
	}
	respond(c, gin.H{"notifications": targets})
}

func (s *Server) handleSaveSettingsWebhooks(c *gin.Context) {
	var req struct {
		Notifications interface{} `json:"notifications"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	data, err := json.Marshal(req.Notifications)
	if err != nil {
		respondError(c, http.StatusBadRequest, "failed to marshal notifications")
		return
	}
	cfg := db.ServerConfig{Key: "notification_targets", Value: string(data), UpdatedAt: time.Now()}
	if err := s.db.Save(&cfg).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Notification config"))
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handleTestSettingsWebhook(c *gin.Context) {
	var req struct {
		Type     string `json:"type"`
		URL      string `json:"url"`
		Secret   string `json:"secret"`
		To       string `json:"to"`
		SMTPHost string `json:"smtp_host"`
		SMTPPort int    `json:"smtp_port"`
		SMTPUser string `json:"smtp_user"`
		SMTPPass string `json:"smtp_pass"`
		From     string `json:"from"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	if req.Type == "email" {
		cfg := EmailConfig{
			SMTPHost: req.SMTPHost,
			SMTPPort: req.SMTPPort,
			SMTPUser: req.SMTPUser,
			SMTPPass: req.SMTPPass,
			From:     req.From,
			To:       req.To,
		}
		msg := buildMIMEMessage(cfg.From, cfg.To, "ForgeC2 Test Notification", "<h1>ForgeC2 Test Notification</h1><p>This is a test email from ForgeC2.</p>")
		if err := sendEmailWithRetry(cfg, msg); err != nil {
			respondError(c, http.StatusInternalServerError, sanitizeError(err, "Email test"))
			return
		}
		respond(c, gin.H{"message": "Test email sent successfully"})
		return
	}

	if req.URL == "" {
		respondError(c, http.StatusBadRequest, "webhook URL is required")
		return
	}

	// Full SSRF validation (scheme, ports, DNS resolution, cloud metadata):
	// the old isPrivateIP-literal check missed DNS rebinding and redirects.
	if err := validateExternalURL(req.URL); err != nil {
		respondError(c, http.StatusBadRequest, "webhook URL blocked: "+sanitizeError(err, "webhook URL"))
		return
	}

	payload := gin.H{"text": "ForgeC2 test notification", "content": "This is a test notification from ForgeC2."}
	body, ok := marshalJSONSafe(payload)
	if !ok {
		respondError(c, http.StatusInternalServerError, "failed to marshal payload")
		return
	}
	reqHTTP, err := http.NewRequest("POST", req.URL, strings.NewReader(string(body)))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "Failed to create request")
		return
	}
	reqHTTP.Header.Set("Content-Type", "application/json")
	resp, err := ssrfSafeClient(s.httpClient).Do(reqHTTP)
	if err != nil {
		respondError(c, http.StatusBadGateway, sanitizeError(err, "Settings save"))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		respond(c, gin.H{})
	} else {
		respondError(c, http.StatusBadGateway, fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode))
	}
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 32 || s[i] > 126 {
			return false
		}
	}
	return true
}

func (s *Server) validatePasswordComplexity(password string) error {
	// The policy is mutated by a hot config reload (CopyFrom under the write
	// lock), so read it under the read lock. Callers hold no config lock.
	s.configMu.RLock()
	policy := &s.cfg.PasswordPolicy
	minLen := 8
	requireUpper := true
	requireLower := true
	requireDigit := true
	requireSymbol := false
	if policy != nil {
		if policy.MinLength > 0 {
			minLen = policy.MinLength
		}
		requireUpper = policy.RequireUpper
		requireLower = policy.RequireLower
		requireDigit = policy.RequireDigit
		requireSymbol = policy.RequireSymbol
	}
	s.configMu.RUnlock()
	if len(password) < minLen {
		return fmt.Errorf("password must be at least %d characters", minLen)
	}
	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, ch := range password {
		switch {
		case unicode.IsUpper(ch):
			hasUpper = true
		case unicode.IsLower(ch):
			hasLower = true
		case unicode.IsDigit(ch):
			hasDigit = true
		case unicode.IsPunct(ch) || unicode.IsSymbol(ch):
			hasSymbol = true
		}
	}
	if requireUpper && !hasUpper {
		return fmt.Errorf("password must contain at least one uppercase letter")
	}
	if requireLower && !hasLower {
		return fmt.Errorf("password must contain at least one lowercase letter")
	}
	if requireDigit && !hasDigit {
		return fmt.Errorf("password must contain at least one digit")
	}
	if requireSymbol && !hasSymbol {
		return fmt.Errorf("password must contain at least one special character")
	}
	return nil
}
