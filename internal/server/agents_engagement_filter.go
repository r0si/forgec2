package server

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Engagement filtering for agent lists.
//
// One implementation, because the page handler and the JSON list each build their own query and a filter
// added to only one of them is the kind of drift this package has already paid for (the discarded-result
// and ETag guards exist for the same reason).
//
// Semantics:
//   - `engagement=<id>` selects the implants belonging to that engagement;
//   - `engagement=none` selects the ones belonging to none - the inverse question an operator needs,
//     because an implant with no engagement has no window and no deadline;
//   - an empty value is no filter (additive: the parameter never narrows silently);
//   - a non-numeric value is an error, not an ignored filter.
func applyEngagementFilter(query *gorm.DB, c *gin.Context) (*gorm.DB, error) {
	raw := strings.TrimSpace(c.Query("engagement"))
	if err := validateEngagementQuery(raw); err != nil {
		return nil, err
	}
	if raw == "" {
		return query, nil
	}
	if raw == "none" {
		return query.Where("engagement_id IS NULL"), nil
	}
	id, _ := strconv.ParseUint(raw, 10, 64)
	return query.Where("engagement_id = ?", id), nil
}

// validateEngagementQuery reports an unusable "engagement" parameter before any query is built.
//
// The JSON list calls this so a typo answers 400 instead of silently returning the whole fleet - a filter
// that quietly does nothing is worse than an error, which is the rule this package follows elsewhere.
func validateEngagementQuery(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "none" {
		return nil
	}
	if _, err := strconv.ParseUint(raw, 10, 64); err != nil {
		return fmt.Errorf(`invalid engagement (expected a numeric engagement id or "none")`)
	}
	return nil
}
