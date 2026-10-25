package server

import (
	"log/slog"
	"net/http"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// handleTemplatesPage renders the command templates page
func (s *Server) handleTemplatesPage(c *gin.Context) {
	stats := s.getNavStats(c)

	// Get all templates
	var templates []db.CommandTemplate
	if err := s.db.Order("category, name").Find(&templates).Error; err != nil {
		slog.Error("Failed to list command templates", "err", err)
	}

	// Group by category
	categories := make(map[string][]db.CommandTemplate)
	for _, t := range templates {
		categories[t.Category] = append(categories[t.Category], t)
	}

	data := gin.H{
		"Title":      "ForgeC2 - Command Templates",
		"ActiveNav":  "templates",
		"Stats":      stats,
		"Templates":  templates,
		"Categories": categories,
	}
	s.renderPageOrJSON(c, data)
}

// handleCreateTemplate creates a new command template
func (s *Server) handleCreateTemplate(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required"`
		Category    string `json:"category" binding:"required"`
		Command     string `json:"command" binding:"required"`
		Description string `json:"description"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}

	template := db.CommandTemplate{
		Name:        req.Name,
		Category:    req.Category,
		Command:     req.Command,
		Description: req.Description,
	}

	if err := s.db.Create(&template).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to create template")
		return
	}

	respond(c, gin.H{

		"template": template,
	})
}

// handleDeleteTemplate deletes a command template
func (s *Server) handleDeleteTemplate(c *gin.Context) {
	templateID := c.Param("id")

	if err := s.db.Delete(&db.CommandTemplate{}, templateID).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to delete template")
		return
	}

	respond(c, gin.H{})
}

// handleGetTemplatesByCategory returns templates by category
func (s *Server) handleGetTemplatesByCategory(c *gin.Context) {
	category := c.Param("category")

	var templates []db.CommandTemplate
	if err := s.db.Where("category = ?", category).Order("name").Find(&templates).Error; err != nil {
		slog.Error("Failed to query templates by category", "err", err)
	}

	respond(c, gin.H{
		"templates": templates,
		"total":     len(templates),
	})
}

// handleUpdateTemplate updates an existing command template
// PUT /api/templates/:id
func (s *Server) handleUpdateTemplate(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Name        string `json:"name" binding:"required"`
		Category    string `json:"category" binding:"required"`
		Command     string `json:"command" binding:"required"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if err := s.db.Model(&db.CommandTemplate{}).Where("id = ?", id).Updates(map[string]interface{}{
		"name":        req.Name,
		"category":    req.Category,
		"command":     req.Command,
		"description": req.Description,
	}).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to update template")
		return
	}
	respond(c, gin.H{})
}

// handleListTemplatesJSON returns all templates as JSON
func (s *Server) handleListTemplatesJSON(c *gin.Context) {
	var templates []db.CommandTemplate
	if err := s.db.Order("category, name").Find(&templates).Error; err != nil {
		// An empty 200 here reads as "no command templates exist".
		handleQueryError(c, err, "Failed to list templates JSON")
		return
	}
	respond(c, gin.H{
		"templates": templates,
		"total":     len(templates),
	})
}
