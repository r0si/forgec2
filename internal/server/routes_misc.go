package server

import (
	"context"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerMiscRoutes(auth *gin.RouterGroup) {
	auth.GET("/api/update-check", s.handleUpdateCheck)
	auth.GET("/api/update-check/version", s.handleCheckVersion)
	// Refresh forces an outbound GitHub API call and rewrites the server's
	// update state, so it needs a write permission: a viewer used to be able
	// to trigger unlimited egress with it. Reading the status stays open to
	// every authenticated operator.
	auth.POST("/api/update-check/refresh",
		middleware.RequirePermission(db.PermSettingsWrite), s.handleRefreshUpdateCheck)
	auth.GET("/api/update-progress", s.handleUpdateProgress)

	auth.POST("/api/update-check/hot-update", middleware.RequireRole(db.RoleAdmin), s.handleHotUpdate)

	miscWrite := auth.Group("/")
	miscWrite.Use(middleware.RequirePermission(db.PermAgentsWrite))
	{
		miscWrite.POST("/api/agents/:id/profile-rotate", s.handleProfileRotate)
	}

	stageDownloadRate := middleware.NewRateLimiter(s.ctx, PublicDownloadRate, time.Minute)
	s.router.GET("/stage/:token", stageDownloadRate.Limit(), s.handleServeStage)
	// Screenshots sit on s.router (blob path) rather than the auth group, so
	// they must re-apply the operator-plane guard and a dedicated rate limit —
	// AuthRequired alone left CIDR/mTLS allowlist and per-user budget bypassed.
	screenshotRate := middleware.NewRateLimiter(s.ctx, 120, time.Minute)
	s.router.GET("/screenshots/:agent_id/:filename",
		s.operatorPlaneGuard(),
		middleware.AuthRequired(s.db),
		middleware.RequirePermission(db.PermAgentsRead),
		screenshotRate.Limit(),
		s.handleServeScreenshot)
}

// registerAutomationRoutes registers automation rules and BOF repository routes.

func (s *Server) registerAutomationRoutes(auth *gin.RouterGroup) {
	autoRead := auth.Group("/")
	autoRead.Use(middleware.RequirePermission(db.PermAutomationRead))
	{
		autoRead.GET("/automation", s.handleAutomationPage)
		autoRead.GET("/api/automation/rules", s.handleListAutomationRules)
		autoRead.GET("/api/webhooks", s.handleListWebhooks)
		autoRead.GET("/bof_repo", func(c *gin.Context) {
			s.renderPageOrJSON(c, gin.H{"Title": "BOF Repository", "ActiveNav": "bof_repo"})
		})
		autoRead.GET("/api/bof/repos", s.handleBOFRepoIndex)
	}
	// The diagnostics family scans the rule and route tables and writes an audit row per call. It is cheap per request
	// and expensive in a loop, which is exactly what a retry-happy script does - so the family gets its own budget,
	// separate from the write endpoints it shares a permission with.
	autoDiagnose := auth.Group("/")
	autoDiagnose.Use(middleware.RequirePermission(db.PermAutomationWrite))
	autoDiagnose.Use(middleware.NewRateLimiter(context.Background(), diagnosticRateLimitRequests, diagnosticRateLimitWindow).Limit())
	{
		// Static health of the rule set: unconditional mutating actions, dangling targets, duplicates.
		autoDiagnose.GET("/api/automation/health", s.handleAutomationHealth)
	}

	autoWrite := auth.Group("/")
	autoWrite.Use(middleware.RequirePermission(db.PermAutomationWrite))
	{
		autoWrite.POST("/api/automation/rules", s.handleSaveAutomationRule)
		// Clearing a rule's action counters: a fixed rule must not keep an error-level finding forever.
		autoWrite.POST("/api/automation/action-stats/reset", s.handleResetAutomationActionStats)
		autoWrite.PUT("/api/automation/rules/:id", s.handleUpdateAutomationRule)
		autoWrite.DELETE("/api/automation/rules/:id", s.handleDeleteAutomationRule)
		autoWrite.POST("/api/automation/rules/:id/toggle", s.handleToggleAutomationRule)
		// Dry run: evaluate one rule against a synthetic event and explain the verdict, executing nothing.
		autoWrite.POST("/api/automation/rules/:id/preview", s.handlePreviewAutomationRule)
		// The other direction: which rules would fire for one event.
		autoDiagnose.POST("/api/automation/preview", s.handlePreviewAutomationBatch)
		// One call: what fires for this event, and who would be told.
		autoDiagnose.POST("/api/automation/diagnose", s.handleAutomationDiagnose)
		autoWrite.POST("/api/webhooks", s.handleCreateWebhook)
		autoWrite.DELETE("/api/webhooks/:id", s.handleDeleteWebhook)
		autoWrite.POST("/api/webhooks/test", s.handleTestWebhook)
		autoWrite.POST("/api/bof/repos/import", s.handleBOFRepoImport)
	}
}

// registerPluginRoutes registers plugin management and execution routes.

func (s *Server) registerPluginRoutes(auth *gin.RouterGroup) {
	pluginsRead := auth.Group("/api/plugins")
	pluginsRead.Use(middleware.RequirePermission(db.PermPluginsRead))
	pluginsRead.GET("", s.handlePluginList)
	pluginsRead.GET("/update-summary", s.handlePluginUpdateSummary)
	// The installed set as a verifiable document (digest per package); registered before :id like the
	// other static siblings.
	pluginsRead.GET("/index", s.handlePluginIndex)
	pluginsRead.GET("/:id", s.handlePluginGet)
	pluginsRead.GET("/:id/rating", s.handlePluginRating)
	pluginsRead.GET("/:id/reviews", s.handlePluginReviews)
	pluginsRead.GET("/:id/dependencies", s.handlePluginDependencies)
	pluginsRead.GET("/:id/update-status", s.handlePluginUpdateStatus)
	pluginsRead.GET("/:id/export", s.handlePluginExport)
	pluginsRead.GET("/:id/execute", s.handlePluginExecuteInfo)

	pluginsWrite := auth.Group("/api/plugins")
	pluginsWrite.Use(middleware.RequirePermission(db.PermPluginsWrite))
	pluginsWrite.POST("", s.handlePluginCreate)
	// Comparing an upload against the installation installs nothing: it needs no trust decision. It sits
	// on the write group only because it takes a request body, not because it changes state.
	pluginsWrite.POST("/index/verify", s.handlePluginIndexVerify)
	pluginsWrite.POST("/check-updates", s.handlePluginCheckUpdates)
	pluginsWrite.POST("/import", s.handlePluginImport)
	pluginsWrite.POST("/:id/reviews", s.handlePluginAddReview)
	pluginsWrite.POST("/:id/rating", s.handlePluginRate)
	pluginsWrite.POST("/:id/update", s.handlePluginUpdate)
	pluginsWrite.POST("/:id/toggle", s.handlePluginToggle)
	pluginsWrite.POST("/:id/install", s.handlePluginInstall)
	pluginsWrite.POST("/:id/enable", s.handlePluginEnable)
	pluginsWrite.POST("/:id/disable", s.handlePluginDisable)
	pluginsWrite.POST("/:id/report", s.handlePluginReport)

	pluginsExecute := auth.Group("/api/plugins")
	pluginsExecute.Use(middleware.RequirePermission(db.PermPluginsExecute))
	pluginsExecute.POST("/:id/execute", s.handlePluginExecute)

	pluginsDelete := auth.Group("/api/plugins")
	pluginsDelete.Use(middleware.RequirePermission(db.PermPluginsDelete))
	pluginsDelete.DELETE("/:id", s.handlePluginDelete)

	auth.GET("/plugins", s.handlePluginsPage)
}

// registerTaskRoutes registers task history and logout routes.

// The diagnostics budget, stated once so the limiter and the capabilities document cannot disagree about it.
//
// A client that knows the ceiling can back off before it gets a 429, which is the difference between a polite integration
// and one that spends its first minute being throttled.
const (
	diagnosticRateLimitRequests = 120
	diagnosticRateLimitWindow   = time.Minute
)
