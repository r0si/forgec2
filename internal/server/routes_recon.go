package server

import (
	"net/http"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerReconRoutes(auth *gin.RouterGroup) {
	reconRead := auth.Group("/")
	reconRead.Use(middleware.RequirePermission(db.PermAgentsRead))
	{
		reconRead.GET("/pivoting", s.handlePivoting)
		reconRead.GET("/topology", s.handleTopologyPage)
		reconRead.GET("/api/topology/data", s.handleTopologyData)
		reconRead.GET("/loot", s.handleLootPage)
		reconRead.GET("/scanner", s.handleScannerPage)
		reconRead.GET("/api/scan/results/:taskId", s.handleScanResults)
		reconRead.GET("/api/scan/agent/:agentId", s.handleScanResultsByAgent)
		reconRead.GET("/api/scan/export/:taskId", s.handleExportScanResults)
		reconRead.GET("/privesc", s.handlePrivescPage)
		reconRead.GET("/api/privesc/history/:id", s.handlePrivescHistory)
		reconRead.GET("/toolkit", s.handleToolkitPage)
		reconRead.GET("/toolkit/results", s.handleToolkitRecentResults)
		reconRead.GET("/toolkit/agents/:id/info", s.handleToolkitAgentInfo)
		reconRead.GET("/toolkit/agents/:id/tasks", s.handleToolkitAgentTasks)
		reconRead.GET("/timeline", s.handleTimelinePage)
		reconRead.GET("/api/timeline/data", s.handleTimelineData)
		reconRead.GET("/api/timeline/export", s.handleTimelineExport)
		reconRead.POST("/api/timeline/export", s.handleTimelineExport)
		reconRead.GET("/report", s.handleReportPage)
		reconRead.GET("/api/report/agents", s.handleAPIGetReportAgents)
		reconRead.GET("/api/report/tasks", s.handleAPIGetReportTasks)
		reconRead.GET("/api/report/credentials", s.handleAPIGetReportCredentials)
		reconRead.GET("/api/report/network", s.handleAPIGetReportNetwork)
		reconRead.GET("/api/report/findings", s.handleAPIGetReportFindings)
		reconRead.GET("/api/report/history", s.handleAPIGetReportHistory)
		reconRead.GET("/api/report/generated/:id", s.handleAPIGetGeneratedReport)
		reconRead.GET("/api/report/generated/:id/download", s.handleAPIExportGeneratedReport)
		reconRead.GET("/api/report/export/html", s.handleAPIExportReportHTML)
		reconRead.GET("/lateral", s.handleLateralPage)
		reconRead.GET("/api/lateral/history/:id", s.handleLateralHistory)
		reconRead.GET("/templates", s.handleTemplatesPage)
		reconRead.GET("/api/templates", s.handleListTemplatesJSON)
		reconRead.GET("/api/templates/category/:category", s.handleGetTemplatesByCategory)
		macrosRead := auth.Group("/")
		macrosRead.Use(middleware.RequirePermission(db.PermAgentsRead))
		{
			macrosRead.GET("/api/macros", s.handleListMacros)
			macrosRead.GET("/api/macro-runs", s.handleListMacroRuns)
		}
	}
	reconWrite := auth.Group("/")
	reconWrite.Use(middleware.RequirePermission(db.PermAgentsWrite))
	{
		reconWrite.POST("/loot/bulk-delete", s.handleLootBulkDelete)
		reconWrite.POST("/api/scan", s.handleScanTask)
		reconWrite.POST("/api/scan/result", s.handleProcessScanResult)
		reconWrite.POST("/api/browser/result", s.handleProcessBrowserResult)
		reconWrite.POST("/api/wifi/result", s.handleProcessWifiResult)
		reconWrite.POST("/api/lateral/result", s.handleProcessLateralResult)
		reconWrite.POST("/api/privesc/result", s.handleProcessPrivescResult)
		reconWrite.POST("/api/privesc/run", s.handlePrivescRun)
		reconWrite.POST("/toolkit/agents/:id/action", s.handleToolkitQuickAction)
		reconWrite.POST("/api/report/generate", s.handleGenerateReport)
		reconWrite.DELETE("/api/report/:id", s.handleAPIDeleteReport)
		iocRead := auth.Group("/")
		iocRead.Use(middleware.RequirePermission(db.PermAgentsRead))
		{
			iocRead.GET("/api/ioc", s.handleListIOCs)
			iocRead.GET("/api/ioc/export", s.handleExportIOCs)
		}
		reconWrite.POST("/api/lateral/execute", s.handleAPILateralExecute)
		reconWrite.POST("/api/templates", s.handleCreateTemplate)
		reconWrite.PUT("/api/templates/:id", s.handleUpdateTemplate)
		reconWrite.DELETE("/api/templates/:id", s.handleDeleteTemplate)
		reconWrite.POST("/api/macros", s.handleCreateMacro)
		reconWrite.PUT("/api/macros/:id", s.handleUpdateMacro)
		reconWrite.DELETE("/api/macros/:id", s.handleDeleteMacro)
		reconWrite.POST("/api/macros/:id/run", s.handleRunMacro)
		reconWrite.POST("/api/macro-runs/:id/stop", s.handleStopMacroRun)
		reconWrite.POST("/api/scheduler/oneshot", s.handleCreateOneShotTask)
		reconWrite.DELETE("/api/scheduler/oneshot/:id", s.handleCancelOneShotTask)
	}

	auditRead := auth.Group("/")
	auditRead.Use(middleware.RequirePermission(db.PermAuditRead))
	{
		auditRead.GET("/audit", s.handleAuditLogPage)
		auditRead.GET("/audit/logs", s.handleGetAuditLogs)
		auditRead.GET("/audit/verify", s.handleVerifyAuditChain)
		auditRead.GET("/audit/export", s.handleExportAuditChain)
	}
}

func (s *Server) registerDebugRoutes(auth *gin.RouterGroup) {
	// Always registered; enabled flags are evaluated per request so
	// toggling server.enable_metrics takes effect on reload without a
	// restart. Auth stays enforced regardless of the toggle.
	auth.GET("/metrics", middleware.RequirePermission(db.PermSettingsRead), s.gatedMetricsHandler())
	// The /debug/pprof/* subtree was removed. It had no consumer (openapi
	// omitted it by design, no script or test hit it) and it dumps process
	// memory, which includes the JWT secret, the loot/extc2/credential keys and
	// decrypted credentials. Investigate performance locally with
	// `go test -cpuprofile` / `go tool pprof` instead of over HTTP.
}

// gatedMetricsHandler wraps the prometheus handler with the live toggle.
func (s *Server) gatedMetricsHandler() gin.HandlerFunc {
	inner := metricsPromHandler()
	return func(c *gin.Context) {
		s.configMu.RLock()
		enabled := s.cfg.Server.EnableMetrics
		s.configMu.RUnlock()
		if !enabled {
			c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "metrics disabled"})
			c.Abort()
			return
		}
		inner(c)
	}
}

// registerSettingsRoutes registers settings and i18n routes.
