package server

import (
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// creatorScope restricts a table that has NO tenant column to the rows the caller's tenant owns,
// using the row's creator column. Three tables are scoped this way so far (`scripts`,
// `bof_files`, `phishing_templates`/`phishing_campaigns`), and they were each found the hard way:
// a list that returned every tenant's rows, or a lookup by id alone that let one tenant edit or
// delete another's.
//
// Semantics, shared with tenantScope:
//   - tenant 0 (legacy/unscoped operator) sees everything, as before multi-tenancy;
//   - a failed tenant lookup denies EVERYTHING rather than widening;
//   - rows with an empty creator are hidden from scoped tenants: assigning them to whoever asks
//     first would let any tenant claim a legacy row.
func (s *Server) creatorScope(c *gin.Context, model any, column string) (*gorm.DB, bool) {
	tid, ok := s.resolveTenant(c)
	if !ok {
		return nil, false
	}
	q := s.db.Model(model)
	if tid == 0 {
		return q, true
	}
	users := s.db.Model(&db.User{}).Select("username").Where("tenant_id = ?", tid)
	return q.Where(column+" IN (?)", users), true
}
