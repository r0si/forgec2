package server

import (
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerCampaignRoutes(auth *gin.RouterGroup) {
	campaignsRead := auth.Group("/")
	campaignsRead.Use(middleware.RequirePermission(db.PermCampaignsRead))
	{
		campaignsRead.GET("/campaigns", s.handleCampaignsList)
		campaignsRead.GET("/campaigns/:id", s.handleCampaignGet)
		campaignsRead.GET("/campaigns/:id/mitre", s.handleCampaignMitre)
		campaignsRead.GET("/mitre/templates", s.handleMitreTemplates)
		campaignsRead.GET("/mitre/timeline", s.handleMitreTimeline)
		campaignsRead.GET("/mitre/phases", s.handleMitrePhases)
		campaignsRead.GET("/api/mitre/heatmap", s.handleMitreHeatmap)
		campaignsRead.GET("/attack/coverage", s.handleAttackCoverage)
	}
	campaignsWrite := auth.Group("/")
	campaignsWrite.Use(middleware.RequirePermission(db.PermCampaignsWrite))
	{
		campaignsWrite.POST("/campaigns", s.handleCampaignCreate)
		campaignsWrite.POST("/campaigns/:id", s.handleCampaignUpdate)
		campaignsWrite.DELETE("/campaigns/:id", s.handleCampaignDelete)
		campaignsWrite.POST("/campaigns/:id/killchain", s.handleCampaignKillChain)
	}

	notificationsRead := auth.Group("/")
	notificationsRead.Use(middleware.RequirePermission(db.PermNotificationsRead))
	{
		notificationsRead.GET("/notifications", s.handleListNotifications)
	}
	notificationsWrite := auth.Group("/")
	notificationsWrite.Use(middleware.RequirePermission(db.PermNotificationsWrite))
	{
		notificationsWrite.PUT("/notifications/:id/read", s.handleMarkNotificationRead)
		notificationsWrite.PUT("/notifications/read-all", s.handleMarkAllNotificationsRead)
		notificationsWrite.DELETE("/notifications/:id", s.handleDeleteNotification)
		notificationsWrite.DELETE("/notifications", s.handleClearAllNotifications)
		notificationRoutes := auth.Group("/")
		notificationRoutes.Use(middleware.RequirePermission(db.PermSettingsRead))
		{
			notificationRoutes.GET("/api/notification-routes", s.handleListNotificationRoutes)
			// Dry run: would a candidate notification reach anyone. Read-only, so it sits with the read group.
			notificationRoutes.POST("/api/notification-routes/preview", s.handlePreviewNotification)
		}
		notificationRouteWrites := auth.Group("/")
		notificationRouteWrites.Use(middleware.RequirePermission(db.PermSettingsWrite))
		{
			notificationRouteWrites.POST("/api/notification-routes", s.handleCreateNotificationRoute)
			notificationRouteWrites.PUT("/api/notification-routes/:id", s.handleUpdateNotificationRoute)
			notificationRouteWrites.DELETE("/api/notification-routes/:id", s.handleDeleteNotificationRoute)
			notificationRouteWrites.POST("/api/notification-routes/:id/test", s.handleTestNotificationRoute)
		}
	}

	redirectorRead := auth.Group("/")
	redirectorRead.Use(middleware.RequirePermission(db.PermSettingsRead))
	{
		redirectorRead.GET("/redirectors", s.handleRedirectorList)
	}
	redirectorWrite := auth.Group("/")
	redirectorWrite.Use(middleware.RequirePermission(db.PermSettingsWrite))
	{
		redirectorWrite.POST("/redirectors", s.handleRedirectorCreate)
		redirectorWrite.PUT("/redirectors/:id", s.handleRedirectorUpdate)
		redirectorWrite.DELETE("/redirectors/:id", s.handleRedirectorDelete)
		redirectorWrite.POST("/redirectors/test-ssh", s.handleRedirectorTestSSH)
		redirectorWrite.POST("/redirectors/generate/:type", s.handleRedirectorGenerate)
		redirectorWrite.POST("/redirectors/deploy-ssh", s.handleRedirectorDeploySSH)
	}

	rolesRead := auth.Group("/")
	rolesRead.Use(middleware.RequirePermission(db.PermRolesRead))
	{
		rolesRead.GET("/api/roles", s.handleRolesList)
	}
	rolesWrite := auth.Group("/")
	rolesWrite.Use(middleware.RequirePermission(db.PermRolesWrite))
	{
		rolesWrite.POST("/api/roles", s.handleRolesCreate)
		rolesWrite.POST("/api/roles/:id", s.handleRolesUpdate)
		rolesWrite.DELETE("/api/roles/:id", s.handleRolesDelete)
	}

	collabRead := auth.Group("/")
	collabRead.Use(middleware.RequirePermission(db.PermAgentsRead))
	{
		collabRead.GET("/collab/agents", s.handleCollabAgents)
	}
	collabWrite := auth.Group("/")
	collabWrite.Use(middleware.RequirePermission(db.PermAgentsWrite))
	{
		collabWrite.POST("/collab/agents/:id/lock", s.handleCollabLock)
		collabWrite.POST("/collab/agents/:id/unlock", s.handleCollabUnlock)
		collabWrite.POST("/collab/tasks/:taskId/claim", s.handleCollabClaimTask)
		collabWrite.POST("/collab/tasks/:taskId/release", s.handleCollabReleaseTask)
	}
}

// registerIntegrationRoutes registers BloodHound, AutoTag, OPSEC, cloud sync, Chrome agents, integrations, rportfwd, agent link, and remote desktop routes.
