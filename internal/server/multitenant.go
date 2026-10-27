package server

import (
	"errors"
	"log/slog"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// tenantIDContextKey caches the resolved tenant ID for the request so repeated
// tenantScope calls (e.g. fan-out detail queries) cost one user lookup total.
const tenantIDContextKey = "forgec2_tenant_id"

// tenantResolution is the cached tenant lookup: tid 0 stays legacy-unscoped,
// while ok=false marks a failed lookup (fail closed, never fail open).
type tenantResolution struct {
	tid uint
	ok  bool
}

// currentTenantID returns the tenant ID for the authenticated operator, or 0 if
// the user is unscoped (legacy / pre-multi-tenant). The result is cached in
// the gin context: tenantScope-heavy handlers previously paid one SELECT per
// call (4-5 per agent-detail page).
func (s *Server) currentTenantID(c *gin.Context) uint {
	tid, _ := s.resolveTenant(c)
	return tid
}

func (s *Server) resolveTenant(c *gin.Context) (uint, bool) {
	if v, ok := c.Get(tenantIDContextKey); ok {
		if r, ok := v.(tenantResolution); ok {
			return r.tid, r.ok
		}
	}
	tid, ok := s.lookupTenantID(c)
	c.Set(tenantIDContextKey, tenantResolution{tid: tid, ok: ok})
	return tid, ok
}

func (s *Server) lookupTenantID(c *gin.Context) (uint, bool) {
	// The auth middleware already loaded the user row (and for sessions caches it for
	// five minutes), so it hands the tenant over directly. Without this every scoped
	// request on the single-writer database paid an extra
	// "SELECT tenant_id FROM users WHERE username = ?".
	if v, ok := c.Get(middleware.TenantIDContextKey); ok {
		if tid, ok := v.(uint); ok {
			return tid, true
		}
	}
	if u, ok := c.Get("user"); ok {
		if name, ok := u.(string); ok && name != "" {
			var user db.User
			err := s.db.Select("tenant_id").Where("username = ?", name).First(&user).Error
			if err == nil {
				return user.TenantID, true
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// Named identity with no user row (hand-built test contexts;
				// production auth always loads the row first): legacy.
				return 0, true
			}
			// Authenticated identity whose lookup errored (DB hiccup): fail
			// closed. Returning legacy 0 here silently expanded a broken
			// lookup into global visibility.
			slog.Warn("Tenant lookup failed, denying scoped query", "user", name, "error", err)
			return 0, false
		}
	}
	// No identity in context (tests, system paths): legacy-unscoped.
	return 0, true
}

// tenantScope restricts a query to the caller's tenant. A tenant ID of 0
// (legacy / unscoped operators) is intentionally NOT restricted, preserving
// pre-multi-tenant behavior until operators are explicitly assigned tenants.
// A FAILED lookup denies everything (Where 1=0) instead of degrading to
// global visibility.
func (s *Server) tenantScope(query *gorm.DB, c *gin.Context) *gorm.DB {
	tid, ok := s.resolveTenant(c)
	if !ok {
		return query.Where("1 = 0")
	}
	if tid != 0 {
		return query.Where("tenant_id = ?", tid)
	}
	return query
}

// auditTenantScope restricts audit-log reads to the caller's own tenant plus
// legacy (tenant_id = 0) entries, which predate tenant stamping and stay
// visible to every tenant. Unscoped operators (tenant 0, e.g. legacy admin)
// keep the global view, and a failed tenant lookup denies everything.
func (s *Server) auditTenantScope(query *gorm.DB, c *gin.Context) *gorm.DB {
	tid, ok := s.resolveTenant(c)
	if !ok {
		return query.Where("1 = 0")
	}
	if tid != 0 {
		return query.Where("tenant_id = ? OR tenant_id = 0", tid)
	}
	return query
}

// aiTenantScope restricts a query to the AI principal's tenant, mirroring
// resolveAIAgentID semantics: authenticated principals (UserID != 0) see
// only their own tenant's rows — including tenant 0, which resolveAIAgentID
// treats as a real scope (not legacy fail-open). Anonymous/system paths
// (nil ctx or UserID 0) stay unscoped.
func (s *Server) aiTenantScope(q *gorm.DB, reqCtx *aiReqCtx) *gorm.DB {
	if reqCtx != nil && reqCtx.Principal.UserID != 0 {
		return q.Where("tenant_id = ?", reqCtx.Principal.TenantID)
	}
	return q
}

// agentTenantOf resolves an agent's tenant for event attribution. ok=false
// when the row is missing.
func (s *Server) agentTenantOf(agentID string) (uint, bool) {
	if agentID == "" {
		return 0, false
	}
	var tid uint
	if err := s.db.Model(&db.Implant{}).Select("tenant_id").Where("id = ?", agentID).First(&tid).Error; err != nil {
		return 0, false
	}
	return tid, true
}

// ruleMayFireOn decides whether an automation rule may fire for an event
// attributed to agentID. Strict equality (0==0 legacy included): a rule
// must never read another tenant's event data (webhook payloads carry
// hostnames, creds findings, task outputs) nor act on its agents.
func (s *Server) ruleMayFireOn(ruleTenantID uint, agentID string) bool {
	if agentID == "" {
		// Agent-less events (schedules without target): fire only legacy
		// rules, which predate tenants.
		return ruleTenantID == 0
	}
	agentTenant, ok := s.agentTenantOf(agentID)
	if !ok {
		return false
	}
	return ruleTenantID == agentTenant
}

// automationTarget resolves the final target agent for an automation action
// and enforces tenant containment: the target must belong to the event's
// tenant. Returns "" when the action must be skipped (no target, missing
// agent, or cross-tenant reference). Legacy events (tenant 0) stay unscoped.
func (s *Server) automationTarget(evt Event, paramAgentID string) string {
	target := evt.AgentID
	if paramAgentID != "" {
		target = paramAgentID
	}
	if target == "" {
		return ""
	}
	if evt.TenantID != 0 {
		tid, ok := s.agentTenantOf(target)
		if !ok || tid != evt.TenantID {
			slog.Warn("Automation: cross-tenant target blocked", "agent", target, "tenant", evt.TenantID)
			s.LogAuditRecord(nil, "automation_blocked_cross_tenant", "agent", target,
				"action referenced foreign-tenant agent", true, nil)
			return ""
		}
	}
	return target
}

// principalAgentIDs scopes table scans that carry no tenant column of
// their own (joined through agent_id) to an AI principal's tenant.
// Returns nil for anonymous/system paths (global view, unchanged).
func (s *Server) principalAgentIDs(reqCtx *aiReqCtx) *gorm.DB {
	if reqCtx != nil && reqCtx.Principal.UserID != 0 {
		return s.db.Model(&db.Implant{}).Select("id").Where("tenant_id = ?", reqCtx.Principal.TenantID)
	}
	return nil
}

// tenantIDForUser resolves a username to its tenant ID outside a gin
// context (WebSocket registration). Missing rows and lookup errors both
// yield legacy 0 — the socket layer cannot fail closed here without
// breaking pre-tenant operators, and per-agent fan-out still applies.
func (s *Server) tenantIDForUser(username string) uint {
	var user db.User
	if err := s.db.Select("tenant_id").Where("username = ?", username).First(&user).Error; err != nil {
		return 0
	}
	return user.TenantID
}

// resolveVisibleAgentID resolves an id-or-hostname within the caller's tenant
// scope. The shared resolveAgentID stays unscoped for system paths (beacons,
// sweeps); interactive handlers must use this so one tenant's operator cannot
// pull another tenant's agent profile into AI prompts and pages.
func (s *Server) resolveVisibleAgentID(c *gin.Context, idOrHost string) string {
	var agent db.Implant
	if err := s.tenantScope(s.db, c).Where("id = ? OR hostname = ?", idOrHost, idOrHost).First(&agent).Error; err != nil {
		return ""
	}
	return agent.ID
}

// defaultTenantID returns the ID of the bootstrap "default" tenant that all
// legacy/freshly-registered assets are assigned to.
func (s *Server) defaultTenantID() uint {
	var t db.Tenant
	if err := s.db.Where("name = ?", db.DefaultTenantName).First(&t).Error; err == nil {
		return t.ID
	}
	return 0
}
