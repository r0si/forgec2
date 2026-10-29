package server

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// Scoping the task reports to one exercise.
//
// The task export can already be filtered by engagement, and the failure reports could not - which meant the after-action
// question ("what went wrong in THIS exercise") had to be answered by exporting and filtering by hand. The scope is a subquery
// over the implants table rather than a join, so a report stays one query per source.
//
// `engagement=none` is supported for the same reason as in the agent list: an operator needs the inverse question too, because
// hosts outside any exercise are exactly the ones no deadline will ever stop.

// taskEngagementFilter resolves `?engagement=` into the agent ids to scope by.
//
// Returns the ids and a boolean saying whether a filter applies. The second return is empty when the parameter is absent, which
// is different from "an engagement with no hosts".
func (s *Server) taskEngagementFilter(c *gin.Context) (ids []string, applies bool, err error) {
	raw := strings.TrimSpace(c.Query("engagement"))
	if raw == "" {
		return nil, false, nil
	}
	if err := validateEngagementQuery(raw); err != nil {
		return nil, false, err
	}

	query := s.db.WithContext(s.ctx).Model(&db.Implant{}).Select("id")
	query = s.tenantScope(query, c)
	if raw == "none" {
		query = query.Where("engagement_id IS NULL")
	} else {
		id, _ := strconv.ParseUint(raw, 10, 64)
		query = query.Where("engagement_id = ?", id)
	}
	if err := query.Pluck("id", &ids).Error; err != nil {
		return nil, false, fmt.Errorf("failed to resolve the engagement")
	}
	return ids, true, nil
}
