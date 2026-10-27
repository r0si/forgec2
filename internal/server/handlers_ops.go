package server

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// auditReadyChainWindow bounds the work /ready does on the audit chain: enough to
// catch tampering with recent history, cheap on a poll every few seconds.
const auditReadyChainWindow = 200

// noteAuditChainTamper records a chain-integrity failure once per bad row, so a
// health probe polling /ready does not write an audit entry (and a log line)
// every few seconds.
func (s *Server) noteAuditChainTamper(verdict auditChainVerdict) {
	if s == nil {
		return
	}
	s.auditTamperMu.Lock()
	repeated := s.auditTamperID == verdict.FirstBadID
	if !repeated {
		s.auditTamperID = verdict.FirstBadID
	}
	s.auditTamperMu.Unlock()
	if repeated {
		return
	}
	slog.Error("Audit chain integrity failure", "first_bad_id", verdict.FirstBadID, "reason", verdict.Reason)
	s.LogAuditRecord(nil, "audit_chain_tamper", "audit", "",
		fmt.Sprintf("audit chain verification failed at row %d: %s", verdict.FirstBadID, verdict.Reason), false, nil)
}

func (s *Server) handleReadyCheck(c *gin.Context) {
	checks := gin.H{}
	ready := true

	// Check database connectivity
	if s == nil || s.db == nil {
		checks["database"] = "unavailable: database is not initialized"
		ready = false
	} else if sqlDB, err := s.db.DB(); err != nil {
		checks["database"] = "error: " + sanitizeError(err, "Server operation")
		ready = false
	} else if err := sqlDB.Ping(); err != nil {
		checks["database"] = "error: " + sanitizeError(err, "Server operation")
		ready = false
	} else {
		checks["database"] = "connected"
	}

	// Check listener count
	var listenerCount int64
	if s == nil || s.db == nil {
		checks["listeners"] = "unavailable: database is not initialized"
		ready = false
	} else if err := s.db.Model(&db.Listener{}).Count(&listenerCount).Error; err != nil {
		checks["listeners"] = "error: " + sanitizeError(err, "Server operation")
		ready = false
	} else {
		checks["listeners"] = listenerCount
	}

	// Audit chain integrity. Deliberately does NOT make the server unready: a
	// tampered log is a forensic alarm about the past, and taking the C2 offline
	// over it would be the wrong response. It is reported in checks/warnings and
	// logged once per finding, so /ready polling cannot spam the log.
	var warnings []string
	if s != nil && s.db != nil {
		verdict := s.verifyAuditChain(auditReadyChainWindow)
		if verdict.OK {
			checks["audit_chain"] = fmt.Sprintf("ok (%d entries)", verdict.Checked)
		} else {
			checks["audit_chain"] = fmt.Sprintf("integrity failure at row %d: %s", verdict.FirstBadID, verdict.Reason)
			warnings = append(warnings, "audit chain integrity failure")
			s.noteAuditChainTamper(verdict)
		}
	}

	status := "ok"
	code := http.StatusOK
	if !ready {
		status = "not ready"
		code = http.StatusServiceUnavailable
	}

	payload := gin.H{
		"status": status,
		"checks": checks,
	}
	if len(warnings) > 0 {
		payload["warnings"] = warnings
	}
	c.JSON(code, payload)
}

func (s *Server) handleBuildLogs(c *gin.Context) {
	p := parsePagination(c, DefaultPageSize, MaxPageSize)
	filterStatus := c.Query("status")
	filterPlatform := c.Query("platform")
	filterListener := c.Query("listener_id")

	query := s.db.Model(&db.BuildLog{})
	if filterStatus != "" {
		query = query.Where("status = ?", filterStatus)
	}
	if filterPlatform != "" {
		query = query.Where("platform = ?", filterPlatform)
	}
	if filterListener != "" {
		query = query.Where("listener_id = ?", filterListener)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		handleQueryError(c, err, "Failed to count build logs")
		return
	}

	var logs []db.BuildLog
	if err := query.Order("created_at desc").Offset(p.Offset).Limit(p.PageSize).Find(&logs).Error; err != nil {
		handleQueryError(c, err, "Failed to query build logs")
		return
	}

	var successCount, failedCount int64
	if err := s.db.Model(&db.BuildLog{}).Where("status = ?", "success").Count(&successCount).Error; err != nil {
		handleQueryError(c, err, "Failed to count successful builds")
		return
	}
	if err := s.db.Model(&db.BuildLog{}).Where("status = ?", "failed").Count(&failedCount).Error; err != nil {
		handleQueryError(c, err, "Failed to count failed builds")
		return
	}

	totalPages := (int(total) + p.PageSize - 1) / p.PageSize
	if totalPages < 1 {
		totalPages = 1
	}
	prevPage := p.Page - 1
	nextPage := p.Page + 1
	stats := s.getNavStats(c)
	data := gin.H{
		"Title":          "ForgeC2 - Build Logs",
		"ActiveNav":      "builds",
		"Logs":           logs,
		"Page":           p.Page,
		"PrevPage":       prevPage,
		"NextPage":       nextPage,
		"PageSize":       p.PageSize,
		"TotalPages":     totalPages,
		"Total":          int(total),
		"SuccessCount":   successCount,
		"FailedCount":    failedCount,
		"FilterStatus":   filterStatus,
		"FilterPlatform": filterPlatform,
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}

// logBuild creates a build log entry
func (s *Server) logBuild(platform, format, c2URL string, listenerID uint, filename, status, errStr, outputPath string) {
	s.logBuildWithProfile(platform, format, c2URL, listenerID, filename, status, errStr, outputPath, "", "")
}

// logBuildWithProfile additionally records the malleable profile for audit.
func (s *Server) logBuildWithProfile(platform, format, c2URL string, listenerID uint, filename, status, errStr, outputPath, profileName, profileHash string) {
	user := "system"
	if err := s.db.Create(&db.BuildLog{
		Platform:    platform,
		Format:      format,
		C2URL:       c2URL,
		ListenerID:  listenerID,
		Filename:    filename,
		User:        user,
		Status:      status,
		Error:       errStr,
		OutputPath:  outputPath,
		ProfileName: profileName,
		ProfileHash: profileHash,
	}).Error; err != nil {
		slog.Error("Failed to create build log", "error", err)
	}
}
