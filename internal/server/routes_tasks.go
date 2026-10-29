package server

import (
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerTaskRoutes(auth *gin.RouterGroup) {
	tasksRead := auth.Group("/")
	tasksRead.Use(middleware.RequirePermission(db.PermTasksRead))
	{
		tasksRead.GET("/tasks", s.handleTaskHistory)
		tasksRead.GET("/tasks/export", s.handleExportTasks)
		// What the download surface accepts, derived from the code that enforces it.
		tasksRead.GET("/api/exports/capabilities", s.handleExportCapabilities)
		// Why tasks fail, grouped by reason, type or agent.
		tasksRead.GET("/api/stats/tasks/failures", s.handleTaskFailureStats)
		// The same normalisation, across tasks and notifications: what shape of failure dominates right now.
		tasksRead.GET("/api/stats/failures/signatures", s.handleFailureSignatures)
		tasksRead.GET("/tasks/:taskId", s.handleGetTaskStatus)
		// A file is what an operator wants for a large result; the JSON detail stays for the UI.
		tasksRead.GET("/tasks/:taskId/output", s.handleDownloadTaskOutput)
		tasksRead.POST("/tasks/batch-status", s.handleBatchTaskStatus)
	}

	tasksWrite := auth.Group("/")
	tasksWrite.Use(middleware.RequirePermission(db.PermAgentsWrite))
	{
		tasksWrite.POST("/tasks/:taskId/approve", s.handleApproveTask)
		tasksWrite.POST("/tasks/:taskId/reject", s.handleRejectTask)
		// Re-run only the failures that share one shape: after fixing a link, "the timeouts" is the useful set.
		tasksWrite.POST("/tasks/rerun-by-signature", s.handleRerunBySignature)
	}

	auth.POST("/logout", s.handleLogout)
}

// registerCredentialRoutes registers credential management routes.

func (s *Server) registerCredentialRoutes(auth *gin.RouterGroup) {
	credsRead := auth.Group("/")
	credsRead.Use(middleware.RequirePermission(db.PermCredsRead))
	{
		credsRead.GET("/credentials", s.handleCredentialsPage)
		credsRead.GET("/credentials/export", s.handleExportCredentials)
		credsRead.GET("/credentials/:cred_id", s.handleGetCredential)
	}
	credsWrite := auth.Group("/")
	credsWrite.Use(middleware.RequirePermission(db.PermCredsWrite))
	{
		credsWrite.POST("/credentials/add", s.handleAddCredential)
		credsWrite.PUT("/credentials/:cred_id", s.handleUpdateCredential)
		credsWrite.POST("/credentials/batch/tags", s.handleBatchAddTags)
		credsWrite.POST("/credentials/batch/verify", s.handleBatchVerifyCredentials)
		credsWrite.POST("/credentials/:cred_id/confirm", s.handleToggleConfirmed)
		credsWrite.POST("/credentials/:cred_id/usage", s.apiRecordUsage)
	}
	credsDelete := auth.Group("/")
	credsDelete.Use(middleware.RequirePermission(db.PermCredsDelete))
	{
		credsDelete.DELETE("/credentials/:cred_id", s.handleDeleteCredential)
	}
}

// registerUserRoutes registers documentation, AI, WebSocket, tokens, user management, SOCKS sessions, and scripting routes.
