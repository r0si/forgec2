package server

import (
	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerGenerateRoutes(auth *gin.RouterGroup) {
	genRead := auth.Group("/")
	genRead.Use(middleware.RequirePermission(db.PermAgentsRead))
	{
		genRead.GET("/generate", s.handleGeneratePage)
		genRead.GET("/api/generate/profiles", s.handleListProfiles)
		genRead.GET("/api/generate/modules", s.handleAgentModules)
		genRead.GET("/generate/builds", s.handleBuildList)
		genRead.GET("/generate/builds/:id", s.handleBuildStatus)
		genRead.GET("/generate/builds/:id/download", s.handleBuildDownload)
		genRead.GET("/api/builds/effectiveness", s.handleBuildEffectiveness)
		// What the SERVER needs in order to build, checked as the server's own user: these environment failures otherwise surface only
		// as a failed compile.
		genRead.GET("/api/generate/toolchain", s.handleBuildToolchain)
	}
	genWrite := auth.Group("/")
	genWrite.Use(middleware.RequirePermission(db.PermAgentsWrite))
	{
		genWrite.POST("/api/generate/profile", s.handleSaveProfile)
		genWrite.POST("/api/generate/profile/import", s.handleImportProfile)
		genWrite.POST("/api/generate/profile/import-text", s.handleImportProfileText)
		genWrite.POST("/api/generate/profile/validate", s.handleValidateProfile)
		genWrite.DELETE("/api/generate/profile/:name", s.handleDeleteProfile)
		genWrite.POST("/generate/exe", s.handleGenerateEXE)
		genWrite.POST("/generate/dll", s.handleGenerateDLL)
		genWrite.POST("/generate/ps1", s.handleGeneratePS1)
		genWrite.POST("/generate/linux", s.handleGenerateLinux)
		genWrite.POST("/generate/macos", s.handleGenerateMacOS)
		genWrite.POST("/generate/stager", s.handleGenerateStager)
		genWrite.POST("/generate/stager_linux", s.handleGenerateStagerLinux)
		genWrite.POST("/generate/one-liner", s.handleGenerateOneLiner)
		genWrite.POST("/generate/donut", s.handleGenerateDonut)
		genWrite.POST("/generate/shellcode", s.handleGenerateShellcode)
		genWrite.POST("/generate/delivery", s.handleGenerateDelivery)
	}
}

// registerListenerRoutes registers listener CRUD and infrastructure routes.

func (s *Server) registerListenerRoutes(auth *gin.RouterGroup) {
	listenersRead := auth.Group("/")
	listenersRead.Use(middleware.RequirePermission(db.PermListenersRead))
	{
		listenersRead.GET("/listeners", s.handleListenersPage)
		listenersRead.GET("/listeners/:id", s.handleListenerDetail)
		listenersRead.GET("/api/listeners", s.handleListListeners)
		listenersRead.GET("/api/listeners/:id", s.handleAPIGetListener)
	}
	listenersWrite := auth.Group("/")
	listenersWrite.Use(middleware.RequirePermission(db.PermListenersWrite))
	{
		listenersWrite.POST("/api/listeners", s.handleCreateListener)
		listenersWrite.PUT("/api/listeners/:id", s.handleUpdateListener)
		listenersWrite.POST("/api/listeners/:id/enable", s.handleEnableListener)
		listenersWrite.POST("/api/listeners/:id/disable", s.handleDisableListener)
	}
	listenersDelete := auth.Group("/")
	listenersDelete.Use(middleware.RequirePermission(db.PermListenersDelete))
	{
		listenersDelete.DELETE("/api/listeners/:id", s.handleDeleteListener)
	}

	auth.GET("/infrastructure", s.handleInfrastructurePage)
	auth.POST("/infrastructure/generate/nginx", middleware.RequirePermission(db.PermSettingsWrite), s.handleGenerateNginx)
	auth.POST("/infrastructure/generate/apache", middleware.RequirePermission(db.PermSettingsWrite), s.handleGenerateApache)
	auth.POST("/infrastructure/generate/haproxy", middleware.RequirePermission(db.PermSettingsWrite), s.handleGenerateHAProxy)
	// ACME provisioning writes certificates to disk and makes outbound network
	// requests — admin-only.
	auth.POST("/infrastructure/acme/provision", middleware.RequireRole(db.RoleAdmin), s.handleACMECertProvision)
	auth.GET("/infrastructure/profile/export", middleware.RequirePermission(db.PermSettingsWrite), s.handleProfileExport)
}

// registerReconRoutes registers pivoting, topology, loot, scanner, toolkit, timeline, report, lateral, templates, audit routes.
