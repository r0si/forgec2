package server

import (
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerExtendedRoutes(auth *gin.RouterGroup) {
	extRead := auth.Group("/")
	extRead.Use(middleware.RequirePermission(db.PermAgentsRead))
	{
		extRead.GET("/packer/templates", s.handleAPIPackerTemplates)
		extRead.GET("/packer/info", s.handleAPIPackerInfo)
		// Exposes server configuration internals — settings.read.
		extRead.GET("/api/settings", middleware.RequirePermission(db.PermSettingsRead), s.handleAPISettings)
		extRead.GET("/mesh/topology", s.handleAPIMeshTopology)
		extRead.GET("/api/topology/network", s.handleAPINetworkTopology)
		extRead.GET("/translations/stats", s.handleAPITranslationsStats)
		extRead.GET("/api/privesc/results", s.handleAPIPrivesc)
		extRead.GET("/timeline/events", s.handleAPITimelineData)
		extRead.GET("/chat/history", s.handleAPIChatHistory)
		extRead.GET("/chat/channels", s.handleAPIChatChannels)
		extRead.GET("/chain/graph", s.handleAPIChainGraph)
		extRead.GET("/chain", s.handleAPIChainList)
		extRead.GET("/domain-fronting", s.handleAPIDomainFronting)
		// The front list only returns in-memory config; requiring agents.write
		// here answered 403 for every read-only role (the demo observer) even
		// though the page grants agents.read. Mutations (config) stay in
		// extWrite.
		extRead.POST("/infra/front/list", s.handleAPIInfraFrontList)
		extRead.GET("/rportfwd/sessions", s.handleAPIRPortFwdStatus)
		extRead.GET("/stager/tokens", s.handleAPIStagerTokens)
		extRead.GET("/ntlm/relay_status", s.handleNTLMRelayStatus)
		extRead.GET("/api/container/status", s.handleContainerStatus)
		extRead.GET("/api/container/agents", s.handleContainerAgents)
		extRead.GET("/extc2/channels", s.handleListExtC2Channels)
		extRead.GET("/extc2/configs", s.handleListExtC2Configs)
	}
	extWrite := auth.Group("/")
	extWrite.Use(middleware.RequirePermission(db.PermAgentsWrite))
	{
		extWrite.POST("/groups", s.handleAPICreateGroup)
		extWrite.PUT("/groups/:id", s.handleAPIUpdateGroup)
		extWrite.DELETE("/groups/:id", s.handleAPIDeleteGroup)
		extWrite.POST("/packer/artifact", s.handlePackerArtifact)
		extWrite.POST("/payload/bundle", s.handlePackerBundle)
		extWrite.POST("/mesh/route/:agentId", s.handleMeshRoute)
		extWrite.POST("/chat/send", s.handleAPISendChatMessage)
		extWrite.POST("/infra/front/check", s.handleAPIInfraFrontCheck)
		extWrite.POST("/infra/front/config", s.handleAPIInfraFrontConfig)
		extWrite.POST("/stager/register", s.handleAPIStagerRegister)
		extWrite.DELETE("/stager/:id", s.handleAPIStagerDelete)
		extWrite.POST("/token/revert", s.handleAPITokenRevert)
		extWrite.POST("/extc2/discord", s.handleConfigureDiscordC2)
		extWrite.POST("/extc2/slack", s.handleConfigureSlackC2)
		extWrite.POST("/extc2/telegram", s.handleConfigureTelegramC2)
		extWrite.DELETE("/extc2/configs/:id", s.handleDeleteExtC2Config)
	}

	siemRead := auth.Group("/")
	siemRead.Use(middleware.RequirePermission(db.PermSettingsRead))
	{
		siemRead.GET("/siem/rules", s.handleListSIEMRules)
	}
	siemWrite := auth.Group("/")
	siemWrite.Use(middleware.RequirePermission(db.PermSettingsWrite))
	{
		siemWrite.POST("/siem/rules", s.handleCreateSIEMRule)
		siemWrite.PUT("/siem/rules/:id", s.handleUpdateSIEMRule)
		siemWrite.DELETE("/siem/rules/:id", s.handleDeleteSIEMRule)
		siemWrite.POST("/siem/rules/:id/toggle", s.handleToggleSIEMRule)
	}

	// The group list is a read: it only SELECTs groups and counts. Requiring
	// groups.write here made the page 403 for every read-only role (the demo
	// observer holds groups.read), and the nav-perms gate could not see it
	// because the path is bare /groups, not /api/groups.
	groupsRead := auth.Group("/")
	groupsRead.Use(middleware.RequirePermission(db.PermGroupsRead))
	{
		groupsRead.GET("/groups", s.handleAPIGroups)
	}

	workflowsRead := auth.Group("/")
	workflowsRead.Use(middleware.RequirePermission(db.PermAutomationRead))
	{
		workflowsRead.GET("/workflows", s.handleAPIWorkflows)
		workflowsRead.GET("/workflows/:id", s.handleAPIWorkflowsDetail)
		workflowsRead.GET("/workflows/:id/executions", s.handleListWorkflowExecutions)
		workflowsRead.GET("/workflows/:id/executions/:executionId", s.handleGetWorkflowExecution)
	}
	workflowsWrite := auth.Group("/")
	workflowsWrite.Use(middleware.RequirePermission(db.PermAutomationWrite))
	{
		workflowsWrite.POST("/workflows", s.handleAPICreateWorkflow)
		workflowsWrite.PUT("/workflows/:id", s.handleAPIUpdateWorkflow)
		workflowsWrite.DELETE("/workflows/:id", s.handleAPIDeleteWorkflow)
		workflowsWrite.POST("/workflows/:id/toggle", s.handleAPIWorkflowsToggle)
		workflowsWrite.POST("/workflows/:id/execute", s.handleAPIWorkflowsExecute)
	}

	phishingRead := auth.Group("/")
	phishingRead.Use(middleware.RequirePermission(db.PermCampaignsRead))
	{
		phishingRead.GET("/phishing/templates", s.handleAPIPhishingTemplates)
		phishingRead.GET("/phishing/campaigns", s.handleAPIPhishingCampaigns)
		phishingRead.GET("/phishing/captures", s.handleAPIPhishingCaptures)
	}
	phishingWrite := auth.Group("/")
	phishingWrite.Use(middleware.RequirePermission(db.PermCampaignsWrite))
	{
		phishingWrite.POST("/phishing/templates", s.handleAPICreatePhishingTemplate)
		phishingWrite.PUT("/phishing/templates/:id", s.handleAPIUpdatePhishingTemplate)
		phishingWrite.DELETE("/phishing/templates/:id", s.handleAPIDeletePhishingTemplate)
		phishingWrite.POST("/phishing/campaigns", s.handleAPICreatePhishingCampaign)
		phishingWrite.POST("/phishing/campaigns/:id/launch", s.handleAPILaunchPhishingCampaign)
		phishingWrite.POST("/phishing/campaigns/:id/stop", s.handleAPIStopPhishingCampaign)
		phishingWrite.DELETE("/phishing/campaigns/:id", s.handleAPIDeletePhishingCampaign)
		phishingWrite.POST("/api/identity/device-code", s.handleDeviceCodeStart)
		phishingWrite.POST("/api/identity/device-code/:id/poll", s.handleDeviceCodePoll)
		phishingWrite.POST("/api/identity/consent", s.handleConsentStart)
		phishingWrite.GET("/api/identity/consent/:id", s.handleConsentStatus)
		phishingWrite.POST("/api/identity/consent/:id/exchange", s.handleConsentExchange)
	}

	cbRead := auth.Group("/")
	cbRead.Use(middleware.RequirePermission(db.PermOpsecRead))
	{
		cbRead.GET("/circuit-breaker/detail", s.handleAPICircuitBreakerDetail)
		cbRead.GET("/circuit-breaker/config", s.handleAPICircuitBreakerConfig)
		cbRead.GET("/circuit-breaker/events", s.handleAPICircuitBreakerEvents)
	}
	cbWrite := auth.Group("/")
	cbWrite.Use(middleware.RequirePermission(db.PermOpsecWrite))
	{
		cbWrite.POST("/circuit-breaker/config", s.handleAPICircuitBreakerSaveConfig)
		cbWrite.POST("/circuit-breaker/reset/:id", s.handleAPICircuitBreakerReset)
		cbWrite.POST("/circuit-breaker/toggle/:id", s.handleAPICircuitBreakerToggle)
	}

	// Tag reads live under agents.read: the tag inventory names nothing
	// sensitive and the nav entry for /tags already promises read-only roles
	// can browse it. Keeping the GETs in the write group 403s a read-only
	// role that reaches the page by URL while its sidebar shows the link.
	tagsRead := auth.Group("/")
	tagsRead.Use(middleware.RequirePermission(db.PermAgentsRead))
	{
		tagsRead.GET("/api/tags", s.handleAPITagList)
		tagsRead.GET("/api/agents/:id/tags", s.handleAgentTags)
	}

	tagsWrite := auth.Group("/")
	tagsWrite.Use(middleware.RequirePermission(db.PermAgentsWrite))
	{
		tagsWrite.POST("/api/tags", s.handleAPITagCreate)
		tagsWrite.PUT("/api/tags/:id", s.handleAPITagUpdate)
		tagsWrite.DELETE("/api/tags/:id", s.handleAPITagDelete)
		tagsWrite.POST("/agents/batch/tags", s.handleBatchAgentTags)
	}
}

// registerMonitorRoutes registers traffic, monitor/alert, and opsec guard routes.
