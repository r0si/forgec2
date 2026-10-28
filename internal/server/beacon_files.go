package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// 鈹€鈹€ Screenshots, path/IP/geo helpers, ls normalize 鈹€鈹€鈹€

func (s *Server) saveScreenshot(dataDir, agentID string, taskID uint, b64Data string) {
	if s.IsScreenMonitoring(agentID) {
		return // do not retain files during live screen monitoring
	}
	s.writeScreenshotFile(dataDir, agentID, taskID, b64Data)
}

func (s *Server) writeScreenshotFile(dataDir, agentID string, taskID uint, b64Data string) {
	if dataDir == "" {
		dataDir = "data"
	}
	base := filepath.Join(dataDir, "screenshots")
	dir := safeJoin(base, agentID)
	if dir == "" {
		slog.Error("Invalid agent ID for screenshot path", "agent_id", agentID)
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		slog.Error("Failed to create screenshots dir", "agent_id", agentID, "error", err)
		return
	}
	data, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		return
	}
	filename := fmt.Sprintf("screenshot_%d_%d.png", taskID, time.Now().Unix())
	filePath := safeJoin(dir, filename)
	if filePath == "" {
		slog.Error("Invalid screenshot filename", "filename", filename)
		return
	}
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		slog.Error("Failed to save screenshot", "file", filename, "error", err)
	}
}

func (s *Server) handleServeScreenshot(c *gin.Context) {
	agentID := c.Param("agent_id")
	filename := c.Param("filename")

	// Tenant gate: screenshots are stored per agent dir on disk 鈥?verify the
	// caller can see this agent before serving anything under its directory.
	if _, ok := s.getAgentOrFail(c, agentID); !ok {
		return
	}

	// Validate screenshot extension to prevent serving arbitrary files
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".gif" && ext != ".webp" {
		c.String(http.StatusBadRequest, "invalid screenshot file type")
		return
	}

	screenshotRoot := filepath.Clean(filepath.Join(s.rawDataDir(), "screenshots"))
	requested := safeJoin(safeJoin(screenshotRoot, agentID), filename)
	if requested == "" {
		c.String(http.StatusBadRequest, "invalid path")
		return
	}

	serveFileSafe(c, requested, screenshotRoot, "")
}

// Returns empty string if the path escapes the base directory.
func safeJoin(base, name string) string {
	// Normalize backslashes to slashes before any filepath operation so that
	// traversal attempts using Windows-style separators cannot bypass the
	// prefix check on a Linux host (or vice versa).
	base = strings.ReplaceAll(base, `\`, `/`)
	name = strings.ReplaceAll(name, `\`, `/`)
	cleanBase := filepath.Clean(base)
	target := filepath.Clean(filepath.Join(cleanBase, name))
	if !strings.HasPrefix(target, cleanBase+string(filepath.Separator)) && target != cleanBase {
		return ""
	}
	return target
}

// isPrivateIP reports whether an address must not be dialled by an outbound
// fetch (the scripting HTTP bridge and the GeoIP lookup).
//
// It parses the address instead of matching string prefixes: prefixes missed
// "0.0.0.0" (which connects to the local host on Linux), the IPv4-mapped
// "::ffff:127.0.0.1", bracketed literals and the "::" unspecified address.
// netip also unmaps the mapped forms, so IsLoopback/IsUnspecified/IsPrivate see
// the real address.
func isPrivateIP(ip string) bool {
	host := strings.Trim(strings.TrimSpace(ip), "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		// Not an IP literal (a bare hostname): the caller resolves it and
		// re-checks every answer, so this is not the place to guess.
		return false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsUnspecified() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return true
	}
	// 100.64.0.0/10 (CGNAT) is neither private nor loopback by the stdlib
	// definition but must not be dialled either.
	if addr.Is4() {
		b := addr.As4()
		if b[0] == 100 && (b[1]&0xC0) == 64 {
			return true
		}
	}
	return false
}

// lookupGeoIP queries ip-api.com for geolocation data
func (s *Server) lookupGeoIP(ctx context.Context, ip string) (country, city string, lat, lon float64) {
	if isPrivateIP(ip) {
		return "", "", 0, 0
	}
	url := "https://ip-api.com/json/" + ip + "?fields=country,city,lat,lon"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", 0, 0
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", "", 0, 0
	}
	defer resp.Body.Close()
	// ip-api answers 200 with {"status":"fail","message":"..."} for a rejected lookup and 429 once the
	// free tier rate limit (45/min) is exhausted, so the status and the body status are both checked.
	// Without this, "geolocation stopped working" had no explanation anywhere: the decode simply
	// produced empty fields.
	var result struct {
		Status  string  `json:"status"`
		Message string  `json:"message"`
		Country string  `json:"country"`
		City    string  `json:"city"`
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Debug("GeoIP: response was not JSON", "ip", ip, "status", resp.StatusCode)
		return "", "", 0, 0
	}
	if resp.StatusCode != http.StatusOK || result.Status == "fail" {
		slog.Debug("GeoIP: lookup did not return a location",
			"ip", ip, "status", resp.StatusCode, "reason", truncateString(result.Message, 120))
		return "", "", 0, 0
	}
	return result.Country, result.City, result.Lat, result.Lon
}

// normalizeLsResult converts a tab-separated "dir /s" style listing
// into a JSON array.  Returns the original string unchanged when there
// are no data lines (header/separator only).
func normalizeLsResult(raw string) string {
	lines := strings.Split(raw, "\n")

	type lsEntry struct {
		Name    string `json:"name"`
		IsDir   bool   `json:"is_dir"`
		Size    int64  `json:"size"`
		ModTime string `json:"mod_time"`
	}

	var entries []lsEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Type") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "鈹€") {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		isDir := strings.EqualFold(parts[0], "DIR")
		size, _ := strconv.ParseInt(parts[2], 10, 64)
		entries = append(entries, lsEntry{
			Name:    parts[1],
			IsDir:   isDir,
			Size:    size,
			ModTime: parts[3],
		})
	}

	if len(entries) == 0 {
		return raw
	}
	b, ok := marshalJSONSafe(entries)
	if !ok {
		return raw
	}
	return string(b)
}

// executeTaskCallback POSTs task completion results to the configured callback URL.
func (s *Server) executeTaskCallback(task db.Task, agentID string) {
	// Same SSRF gate as webhook delivery (and the same test seam): a callback
	// carries command output to an operator-supplied URL, so it must never be
	// the one outbound path that skips the address policy.
	if err := webhookTargetValidator(task.CallbackURL); err != nil {
		slog.Error("Callback URL rejected by SSRF filter", "task_id", task.ID, "url", redactURLForLog(task.CallbackURL), "error", redactURLFromMessage(err.Error(), task.CallbackURL))
		return
	}

	payload := map[string]interface{}{
		"task_id":   task.ID,
		"agent_id":  agentID,
		"type":      task.Type,
		"command":   task.Command,
		"status":    task.Status,
		"result":    redactSensitiveResultForAuxiliary(task.Type, task.Result),
		"error":     task.Error,
		"completed": task.UpdatedAt.Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Failed to marshal callback payload", "task_id", task.ID, "error", err)
		return
	}

	method := strings.ToUpper(task.CallbackMethod)
	if method == "" {
		method = "POST"
	}

	req, err := http.NewRequest(method, task.CallbackURL, strings.NewReader(string(body)))
	if err != nil {
		slog.Error("Failed to create callback request", "task_id", task.ID, "url", redactURLForLog(task.CallbackURL), "error", redactURLFromMessage(err.Error(), task.CallbackURL))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ForgeC2-Callback/1.0")

	// ssrfSafeClient re-validates every redirect hop (P1-2): the plain
	// httpClient silently follows up to 10 redirects with no per-hop check,
	// letting an attacker URL 302 into cloud metadata / internal services
	// and carries the task result payload there.
	resp, err := ssrfSafeClient(s.httpClient).Do(req)
	if err != nil {
		slog.Error("Callback request failed", "task_id", task.ID, "url", redactURLForLog(task.CallbackURL), "error", redactURLFromMessage(err.Error(), task.CallbackURL))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := s.db.Model(&task).Update("callback_sent", true).Error; err != nil {
			slog.Error("Failed to mark callback as sent", "task_id", task.ID, "error", err)
		}
		slog.Info("Task callback delivered", "task_id", task.ID, "url", task.CallbackURL, "status", resp.StatusCode)
	} else {
		slog.Warn("Task callback returned non-2xx", "task_id", task.ID, "url", redactURLForLog(task.CallbackURL), "status", resp.StatusCode)
	}
}
