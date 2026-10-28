package server

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/util"
	"github.com/gin-gonic/gin"
)

const (
	maxTagNameLen   = 64
	tagColorPattern = `^#[0-9a-fA-F]{6}$`
)

var tagColorRe = regexp.MustCompile(tagColorPattern)

// validTagName rejects empty, overlong, comma-containing (tags are stored
// comma-separated in some flows) or control-character tag names.
func validTagName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > maxTagNameLen {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == ',' {
			return false
		}
	}
	return true
}

func validTagColor(color string) bool {
	return tagColorRe.MatchString(color)
}

func (s *Server) handleAPITagList(c *gin.Context) {
	var tags []db.AgentTag
	if err := s.db.Order("name asc").Find(&tags).Error; err != nil {
		slog.Error("Failed to list tags", "err", err)
	}
	respond(c, tags)
}

func (s *Server) handleAPITagCreate(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		respondError(c, http.StatusBadRequest, "name required")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validTagName(req.Name) {
		respondError(c, http.StatusBadRequest, "invalid tag name (max 64 chars, no commas or control characters)")
		return
	}
	if req.Color == "" {
		req.Color = "#3498db"
	}
	if !validTagColor(req.Color) {
		respondError(c, http.StatusBadRequest, "invalid color (expected #RRGGBB)")
		return
	}
	tag := db.AgentTag{
		ID:        util.NewString(),
		Name:      req.Name,
		Color:     req.Color,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.db.Create(&tag).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Tag creation"))
		return
	}
	s.LogAuditRecord(c, "tag_create", "tag", tag.ID, "Tag "+tag.Name+" created", true, nil)
	respond(c, gin.H{"tag": tag})
}

func (s *Server) handleAPITagUpdate(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	var tag db.AgentTag
	if !s.findOrFail(c, &tag, id, "tag") {
		return
	}
	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name != "" {
		req.Name = strings.TrimSpace(req.Name)
		if !validTagName(req.Name) {
			respondError(c, http.StatusBadRequest, "invalid tag name (max 64 chars, no commas or control characters)")
			return
		}
		tag.Name = req.Name
	}
	if req.Color != "" {
		if !validTagColor(req.Color) {
			respondError(c, http.StatusBadRequest, "invalid color (expected #RRGGBB)")
			return
		}
		tag.Color = req.Color
	}
	tag.UpdatedAt = time.Now()
	if err := s.db.Save(&tag).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to update tag")
		return
	}
	s.LogAuditRecord(c, "tag_update", "tag", tag.ID, "Tag "+tag.Name+" updated", true, nil)
	respond(c, gin.H{"tag": tag})
}

func (s *Server) handleAPITagDelete(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	var tag db.AgentTag
	if !s.findOrFail(c, &tag, id, "tag") {
		return
	}
	_ = s.db.Model(&tag).Association("Agents").Clear() // the tag delete below is the source of truth for the cascade
	if err := s.db.Delete(&tag).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Tag deletion"))
		return
	}
	s.LogAuditRecord(c, "tag_delete", "tag", id, "Tag "+tag.Name+" deleted", true, nil)
	respond(c, gin.H{})
}

func (s *Server) handleAgentTags(c *gin.Context) {
	id := c.Param("id")
	// The agent id comes from the URL: verify it belongs to the caller before disclosing which
	// tags (i.e. which classification) another tenant put on their host.
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	var tags []db.AgentTag
	if err := s.db.Table("agent_tags").
		Joins("JOIN agent_tag_assignments ON agent_tag_assignments.agent_tag_id = agent_tags.id").
		Where("agent_tag_assignments.implant_id = ?", id).
		Order("agent_tags.name asc").
		Find(&tags).Error; err != nil {
		slog.Error("Failed to load agent tags", "agent_id", id, "err", err)
	}
	respond(c, gin.H{"tags": tags})
}

// handleBatchAgentTags returns tags for multiple agents in a single request,
// eliminating the N+1 problem on the agents list page.
func (s *Server) handleBatchAgentTags(c *gin.Context) {
	var req struct {
		AgentIDs []string `json:"agent_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.AgentIDs) == 0 {
		respondError(c, http.StatusBadRequest, "agent_ids required")
		return
	}

	type agentTagsRow struct {
		AgentID string `gorm:"column:agent_id"`
		TagID   string `gorm:"column:tag_id"`
		Name    string `gorm:"column:name"`
		Color   string `gorm:"column:color"`
	}
	// Same rule for the batch form: the id list is caller-supplied, so narrow it to the agents
	// this caller can see before reading their tags.
	visible := s.db.Model(&db.Implant{}).Select("id")
	visible = s.tenantScope(visible, c)
	var rows []agentTagsRow
	if err := s.db.Table("agent_tag_assignments").
		Select("agent_tag_assignments.implant_id as agent_id, agent_tags.id as tag_id, agent_tags.name, agent_tags.color").
		Joins("JOIN agent_tags ON agent_tags.id = agent_tag_assignments.agent_tag_id").
		Where("agent_tag_assignments.implant_id IN ?", req.AgentIDs).
		Where("agent_tag_assignments.implant_id IN (?)", visible).
		Find(&rows).Error; err != nil {
		slog.Error("Failed to batch-load agent tags", "agent_ids", req.AgentIDs, "err", err)
	}

	tagsByAgent := make(map[string][]gin.H)
	for _, r := range rows {
		tagsByAgent[r.AgentID] = append(tagsByAgent[r.AgentID], gin.H{
			"id":    r.TagID,
			"name":  r.Name,
			"color": r.Color,
		})
	}

	respond(c, tagsByAgent)
}
