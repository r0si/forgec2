package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	aiSessionListLimit       = 50
	aiSessionMessageLimit    = 200
	aiSessionMessageMaxBytes = 96 * 1024
	aiSessionBatchMaxBytes   = 384 * 1024
	aiSessionBatchMaxItems   = 64
	aiSessionTitleMaxRunes   = 255
	aiSessionToolMaxRunes    = 100
	aiSessionMetadataMaxBody = 128 * 1024
	aiSessionMessageMaxBody  = 512 * 1024
)

type aiSessionMessageInput struct {
	Role     string `json:"role"`
	Content  string `json:"content"`
	ToolName string `json:"tool_name"`
	ClientID string `json:"client_id"`
}

type aiSessionMessageRequest struct {
	aiSessionMessageInput
	Messages []aiSessionMessageInput `json:"messages"`
}

func bindLimitedAISessionJSON(c *gin.Context, maxBytes int64, dest interface{}) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	return c.ShouldBindJSON(dest)
}

func respondAISessionBindError(c *gin.Context, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		respondError(c, http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	respondError(c, http.StatusBadRequest, "invalid request")
}

// handleAISessionsList returns all AI chat sessions for the current user.
func (s *Server) handleAISessionsList(c *gin.Context) {
	principal, ok := s.currentAIPrincipal(c)
	if !ok {
		respondError(c, http.StatusForbidden, "AI use permission required")
		return
	}

	var sessions []db.AIChatSession
	query := s.db.Where("owner_id = ? AND owner = ? AND tenant_id = ?", principal.UserID, principal.Username, principal.TenantID)
	if search := strings.TrimSpace(c.Query("q")); search != "" {
		query = query.Where("title LIKE ?", "%"+search+"%")
	}
	switch strings.ToLower(strings.TrimSpace(c.Query("archived"))) {
	case "true", "1":
		query = query.Where("archived = ?", true)
	case "all":
	default:
		query = query.Where("archived = ?", false)
	}
	if cursor, err := strconv.ParseUint(c.Query("cursor"), 10, 64); err == nil && cursor > 0 {
		query = query.Where("id < ?", cursor)
	}
	limit := aiSessionListLimit
	if requested, err := strconv.Atoi(c.Query("limit")); err == nil && requested > 0 && requested <= aiSessionListLimit {
		limit = requested
	}
	if err := query.Order("pinned DESC, updated_at DESC, id DESC").Limit(limit + 1).Find(&sessions).Error; err != nil {
		slog.Error("Failed to list AI sessions", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to list sessions")
		return
	}
	nextCursor := ""
	if len(sessions) > limit {
		nextCursor = strconv.FormatUint(uint64(sessions[limit-1].ID), 10)
		sessions = sessions[:limit]
	}
	respond(c, gin.H{"data": sessions, "next_cursor": nextCursor})
}

// handleAISessionsCreate creates a new AI chat session.
func (s *Server) handleAISessionsCreate(c *gin.Context) {
	principal, ok := s.currentAIPrincipal(c)
	if !ok {
		respondError(c, http.StatusForbidden, "AI use permission required")
		return
	}

	var req struct {
		Title          string `json:"title"`
		ProfileID      *uint  `json:"profile_id"`
		ContextAgentID string `json:"context_agent_id"`
		WritePolicy    string `json:"write_policy"`
	}
	if err := bindLimitedAISessionJSON(c, aiSessionMetadataMaxBody, &req); err != nil {
		respondAISessionBindError(c, err)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "New Chat"
	}
	if utf8.RuneCountInString(title) > aiSessionTitleMaxRunes {
		respondError(c, http.StatusBadRequest, "title too long")
		return
	}

	session := db.AIChatSession{
		TenantID: principal.TenantID, OwnerID: principal.UserID,
		Title: title, Owner: principal.Username, ProfileID: req.ProfileID,
		ContextAgentID: strings.TrimSpace(req.ContextAgentID), WritePolicy: "approval",
	}
	if req.WritePolicy == "low_risk_auto" {
		session.WritePolicy = req.WritePolicy
	}
	if err := s.db.Create(&session).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to create session")
		return
	}
	s.LogAuditRecord(c, "ai_session_create", "ai", "", "Created AI chat session: "+title, true, nil)
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": session})
}

// handleAISessionsGet returns messages for a specific AI chat session.
func (s *Server) handleAISessionsGet(c *gin.Context) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid session id")
		return
	}

	principal, ok := s.currentAIPrincipal(c)
	if !ok {
		respondError(c, http.StatusForbidden, "AI use permission required")
		return
	}
	var session db.AIChatSession
	if err := s.db.Where("id = ? AND owner_id = ? AND tenant_id = ?", sessionID, principal.UserID, principal.TenantID).First(&session).Error; err != nil {
		respondError(c, http.StatusNotFound, "session not found")
		return
	}

	// Fetch newest first so long-running sessions do not get stuck showing
	// their oldest messages, then reverse for chronological rendering.
	var messages []db.AIChatMessage
	messageQuery := s.db.Where("session_id = ?", sessionID)
	if before, err := strconv.ParseUint(c.Query("before"), 10, 64); err == nil && before > 0 {
		messageQuery = messageQuery.Where("id < ?", before)
	}
	limit := aiSessionMessageLimit
	if requested, err := strconv.Atoi(c.Query("limit")); err == nil && requested > 0 && requested <= aiSessionMessageLimit {
		limit = requested
	}
	if err := messageQuery.Order("id desc").Limit(limit + 1).Find(&messages).Error; err != nil {
		slog.Error("Failed to list AI messages", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to list messages")
		return
	}
	nextCursor := ""
	if len(messages) > limit {
		nextCursor = strconv.FormatUint(uint64(messages[limit-1].ID), 10)
		messages = messages[:limit]
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	respond(c, gin.H{"data": messages, "next_cursor": nextCursor})
}

// handleAISessionsMessages creates a new message in an AI chat session.
func (s *Server) handleAISessionsMessages(c *gin.Context) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid session id")
		return
	}

	principal, ok := s.currentAIPrincipal(c)
	if !ok {
		respondError(c, http.StatusForbidden, "AI use permission required")
		return
	}
	var session db.AIChatSession
	if err := s.db.Where("id = ? AND owner_id = ? AND tenant_id = ?", sessionID, principal.UserID, principal.TenantID).First(&session).Error; err != nil {
		respondError(c, http.StatusNotFound, "session not found")
		return
	}

	var req aiSessionMessageRequest
	if err := bindLimitedAISessionJSON(c, aiSessionMessageMaxBody, &req); err != nil {
		respondAISessionBindError(c, err)
		return
	}
	inputs := req.Messages
	batch := len(inputs) > 0
	if !batch {
		inputs = []aiSessionMessageInput{req.aiSessionMessageInput}
	}
	if len(inputs) > aiSessionBatchMaxItems {
		respondError(c, http.StatusRequestEntityTooLarge, "too many messages")
		return
	}
	messages := make([]db.AIChatMessage, 0, len(inputs))
	totalBytes := 0
	for _, input := range inputs {
		if input.Role != "user" && input.Role != "assistant" && input.Role != "tool" {
			respondError(c, http.StatusBadRequest, "invalid message role")
			return
		}
		if len(input.Content) > aiSessionMessageMaxBytes {
			respondError(c, http.StatusRequestEntityTooLarge, "message too large")
			return
		}
		totalBytes += len(input.Content)
		if totalBytes > aiSessionBatchMaxBytes {
			respondError(c, http.StatusRequestEntityTooLarge, "message batch too large")
			return
		}
		if utf8.RuneCountInString(input.ToolName) > aiSessionToolMaxRunes {
			respondError(c, http.StatusBadRequest, "tool name too long")
			return
		}
		messages = append(messages, db.AIChatMessage{
			SessionID: uint(sessionID),
			Role:      input.Role,
			Content:   input.Content,
			ToolName:  input.ToolName,
			ClientID:  truncateString(strings.TrimSpace(input.ClientID), 64),
		})
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&messages).Error; err != nil {
			return err
		}
		return tx.Model(&db.AIChatSession{}).Where("id = ?", sessionID).Update("updated_at", time.Now()).Error
	}); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to save message")
		return
	}
	messageIDs := make([]uint, 0, len(messages))
	for _, message := range messages {
		messageIDs = append(messageIDs, message.ID)
	}
	var storedMessages []db.AIChatMessage
	if err := s.db.Where("id IN ?", messageIDs).Order("id ASC").Find(&storedMessages).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to reload saved message")
		return
	}
	if batch {
		c.JSON(http.StatusCreated, gin.H{"success": true, "data": storedMessages})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": storedMessages[0]})
}

// handleAISessionsUpdate renames an AI chat session.
func (s *Server) handleAISessionsUpdate(c *gin.Context) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid session id")
		return
	}

	principal, ok := s.currentAIPrincipal(c)
	if !ok {
		respondError(c, http.StatusForbidden, "AI use permission required")
		return
	}
	var session db.AIChatSession
	if err := s.db.Where("id = ? AND owner_id = ? AND tenant_id = ?", sessionID, principal.UserID, principal.TenantID).First(&session).Error; err != nil {
		respondError(c, http.StatusNotFound, "session not found")
		return
	}

	var req struct {
		Title          *string `json:"title"`
		ProfileID      *uint   `json:"profile_id"`
		ClearProfile   bool    `json:"clear_profile"`
		ContextAgentID *string `json:"context_agent_id"`
		WritePolicy    *string `json:"write_policy"`
		Draft          *string `json:"draft"`
		Pinned         *bool   `json:"pinned"`
		Archived       *bool   `json:"archived"`
	}
	if err := bindLimitedAISessionJSON(c, aiSessionMetadataMaxBody, &req); err != nil {
		respondAISessionBindError(c, err)
		return
	}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" || utf8.RuneCountInString(title) > aiSessionTitleMaxRunes {
			respondError(c, http.StatusBadRequest, "invalid title")
			return
		}
		session.Title = title
	}
	if req.ClearProfile {
		session.ProfileID = nil
	} else if req.ProfileID != nil {
		session.ProfileID = req.ProfileID
	}
	if req.ContextAgentID != nil {
		session.ContextAgentID = truncateString(strings.TrimSpace(*req.ContextAgentID), 64)
	}
	if req.WritePolicy != nil {
		if *req.WritePolicy != "approval" && *req.WritePolicy != "low_risk_auto" {
			respondError(c, http.StatusBadRequest, "invalid write policy")
			return
		}
		session.WritePolicy = *req.WritePolicy
	}
	if req.Draft != nil {
		if len(*req.Draft) > 96*1024 {
			respondError(c, http.StatusRequestEntityTooLarge, "draft too large")
			return
		}
		session.Draft = *req.Draft
	}
	if req.Pinned != nil {
		session.Pinned = *req.Pinned
	}
	if req.Archived != nil {
		session.Archived = *req.Archived
	}

	if err := s.db.Save(&session).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to update session")
		return
	}
	if err := s.db.Where("id = ?", session.ID).First(&session).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to reload session")
		return
	}
	respond(c, session)
}

// handleAISessionsDelete deletes an AI chat session and its messages.
func (s *Server) handleAISessionsDelete(c *gin.Context) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid session id")
		return
	}

	principal, ok := s.currentAIPrincipal(c)
	if !ok {
		respondError(c, http.StatusForbidden, "AI use permission required")
		return
	}
	var session db.AIChatSession
	if err := s.db.Where("id = ? AND owner_id = ? AND tenant_id = ?", sessionID, principal.UserID, principal.TenantID).First(&session).Error; err != nil {
		respondError(c, http.StatusNotFound, "session not found")
		return
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var runIDs []string
		if err := tx.Model(&db.AIChatRun{}).Where("session_id = ?", sessionID).Pluck("id", &runIDs).Error; err != nil {
			return err
		}
		if len(runIDs) > 0 {
			if err := tx.Where("run_id IN ?", runIDs).Delete(&db.AIChatRunEvent{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("session_id = ?", sessionID).Delete(&db.AIAttachment{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ?", sessionID).Delete(&db.AIExecutionIntent{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ?", sessionID).Delete(&db.AIChatMessage{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ?", sessionID).Delete(&db.AIChatRun{}).Error; err != nil {
			return err
		}
		return tx.Delete(&session).Error
	}); err != nil {
		respondError(c, http.StatusInternalServerError, "failed to delete session")
		return
	}
	s.LogAuditRecord(c, "ai_session_delete", "ai", "", "Deleted AI chat session", true, nil)
	respond(c, gin.H{})
}
