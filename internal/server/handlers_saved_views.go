package server

import (
	"net/http"
	"strconv"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// savedViewPages whitelists the pages that can own views, so a stale or
// malicious page name cannot pollute the table.
var savedViewPages = map[string]bool{
	"agents":      true,
	"tasks":       true,
	"credentials": true,
	"timeline":    true,
	"builds":      true,
}

type savedViewRequest struct {
	Page  string `json:"page" binding:"required"`
	Name  string `json:"name" binding:"required"`
	State string `json:"state" binding:"required"`
}

func (s *Server) handleListSavedViews(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)

	page := c.Query("page")
	if page != "" && !savedViewPages[page] {
		page = ""
	}

	q := s.db.Where("user_id = ?", uid)
	if page != "" {
		q = q.Where("page = ?", page)
	}
	var views []db.SavedView
	if err := q.Order("page, name").Limit(200).Find(&views).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "query failed")
		return
	}
	respond(c, gin.H{"views": views})
}

func (s *Server) handleCreateSavedView(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)

	var req savedViewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "page, name and state required")
		return
	}
	if !savedViewPages[req.Page] {
		respondError(c, http.StatusBadRequest, "unknown page")
		return
	}
	if len(req.State) > 8192 {
		respondError(c, http.StatusBadRequest, "state too large")
		return
	}
	// Replace an existing view with the same name on the same page instead of
	// accumulating duplicates.
	var existing db.SavedView
	if err := s.db.Where("user_id = ? AND page = ? AND name = ?", uid, req.Page, req.Name).First(&existing).Error; err == nil {
		if err := s.db.Model(&existing).Update("state", req.State).Error; err != nil {
			respondError(c, http.StatusInternalServerError, "failed to update view")
			return
		}
		respond(c, gin.H{"view": existing})
		return
	}

	view := db.SavedView{
		UserID: uid,
		Page:   req.Page,
		Name:   req.Name,
		State:  req.State,
	}
	if err := s.db.Create(&view).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to create view")
		return
	}
	s.LogAuditRecord(c, "saved_view_create", "settings", strconv.FormatUint(uint64(view.ID), 10), req.Page+"/"+req.Name, true, nil)
	respond(c, gin.H{"view": view})
}

func (s *Server) handleDeleteSavedView(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)
	id := c.Param("id")

	res := s.db.Where("id = ? AND user_id = ?", id, uid).Delete(&db.SavedView{})
	if res.Error != nil {
		respondError(c, http.StatusInternalServerError, "failed to delete view")
		return
	}
	if res.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, "view not found")
		return
	}
	respond(c, gin.H{})
}
