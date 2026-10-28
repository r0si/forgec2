package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// workflowScope restricts workflows to the caller's tenant through their creator. A workflow is
// executable automation: running one creates tasks on the agents it targets, so an unscoped
// execute was the worst instance of this family - a tenant-2 operator could run tenant-1's
// workflow against tenant-1's implants.
func (s *Server) workflowScope(c *gin.Context) (*gorm.DB, bool) {
	return s.creatorScope(c, &db.Workflow{}, "created_by")
}

// findVisibleWorkflow loads a workflow (with its steps) inside the caller's scope.
func (s *Server) findVisibleWorkflow(c *gin.Context, id string) (db.Workflow, bool) {
	var wf db.Workflow
	scope, ok := s.workflowScope(c)
	if !ok {
		respondError(c, http.StatusNotFound, "workflow not found")
		return wf, false
	}
	if err := scope.Preload("Steps").Where("id = ?", id).First(&wf).Error; err != nil {
		respondError(c, http.StatusNotFound, "workflow not found")
		return wf, false
	}
	return wf, true
}

// workflowTargetAgents resolves the implants a workflow would run against, always inside the
// caller's tenant scope. Before this the four branches (ids, tags, groups, recent) queried every
// tenant, so the workflow's own scope_ids decided which tenant's hosts got the tasks.
func (s *Server) workflowTargetAgents(c *gin.Context, wf db.Workflow) []db.Implant {
	var targetAgents []db.Implant
	base := func() *gorm.DB {
		return s.tenantScope(s.db.Model(&db.Implant{}), c)
	}
	switch wf.ScopeType {
	case "tags", "groups", "agents":
		if wf.ScopeIDs == "" {
			return nil
		}
		var ids []string
		if err := json.Unmarshal([]byte(wf.ScopeIDs), &ids); err != nil {
			return nil
		}
		var err error
		switch wf.ScopeType {
		case "agents":
			err = base().Where("id IN ?", ids).Limit(AgentQueryLimit).Find(&targetAgents).Error
		case "tags":
			err = base().Distinct("implants.*").
				Joins("JOIN agent_tag_assignments ON agent_tag_assignments.implant_id = implants.id").
				Where("agent_tag_assignments.agent_tag_id IN ?", ids).
				Limit(AgentQueryLimit).Find(&targetAgents).Error
		default:
			err = base().Distinct("implants.*").
				Joins("JOIN agent_group_assignments ON agent_group_assignments.implant_id = implants.id").
				Where("agent_group_assignments.agent_group_id IN ?", ids).
				Limit(AgentQueryLimit).Find(&targetAgents).Error
		}
		if err != nil {
			return nil
		}
	default:
		if err := base().Where("last_seen > ?", time.Now().Add(-30*time.Minute)).
			Limit(AgentQueryLimit).Find(&targetAgents).Error; err != nil {
			return nil
		}
	}
	return targetAgents
}
