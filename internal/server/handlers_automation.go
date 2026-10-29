package server

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// validateExternalURL guards server-initiated fetches against SSRF. It accepts
// http(s) URLs whose resolved destination is a public IP: loopback, RFC1918/
// ULA-private, link-local, multicast, unspecified and IPv4-mapped-v6 addresses
// are all rejected. Resolving the hostname covers encoded/decimal-octet
// literals and DNS rebinding on the lookup the server itself performs.
func validateExternalURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("URL must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("URL has no host")
	}
	// Port policy, which the test below pins exactly:
	//   - the allowlist (80/443 and the common AI gateway ports) always passes;
	//   - a handful of sensitive service ports (SSH, SMTP, the databases,
	//     Redis, Memcached) is refused;
	//   - privileged ports below 1024 are refused unless they are 80/443;
	//   - every other port above 1024 is ALLOWED. It is not a default deny:
	//     AI gateways legitimately listen on arbitrary high ports, so tightening
	//     this to an allowlist would break working configurations.
	if port := u.Port(); port != "" {
		allowed := map[string]bool{"80": true, "443": true, "8000": true, "8080": true, "8443": true, "11434": true, "3000": true, "5000": true, "9000": true}
		if !allowed[port] {
			// For custom ports, still allow if explicitly in range 8000-9000 is covered; otherwise block.
			p, err := strconv.Atoi(port)
			if err != nil || p < 1 || p > 65535 {
				return fmt.Errorf("invalid port %s", port)
			}
			// Block well-known sensitive ports
			blocked := map[int]bool{22: true, 23: true, 25: true, 3306: true, 5432: true, 6379: true, 27017: true, 11211: true}
			if blocked[p] {
				return fmt.Errorf("blocked port %s", port)
			}
			if p < 1024 && p != 80 && p != 443 {
				return fmt.Errorf("blocked privileged port %s", port)
			}
		}
	}
	var addrs []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		addrs = append(addrs, ip.Unmap())
	} else {
		// Mitigate DNS rebinding: resolve with timeout and reject if any record is private
		resolved, lerr := net.LookupIP(host)
		if lerr != nil {
			return fmt.Errorf("host resolution failed: %w", lerr)
		}
		for _, ip := range resolved {
			if a, ok := netip.AddrFromSlice(ip); ok {
				addrs = append(addrs, a.Unmap())
			}
		}
		if len(addrs) == 0 {
			return errors.New("host resolved to no addresses")
		}
	}
	for _, a := range addrs {
		if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
			a.IsLinkLocalMulticast() || a.IsUnspecified() || a.IsMulticast() {
			return fmt.Errorf("blocked non-public address %s", a)
		}
		// Extra: block 0.0.0.0/8 and CGNAT 100.64.0.0/10 which IsPrivate may miss on some Go versions
		if a.Is4() {
			ip4 := a.As4()
			if ip4[0] == 0 || (ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127) {
				return fmt.Errorf("blocked non-public address %s", a)
			}
		}
	}
	return nil
}

// ssrfSafeClient returns an HTTP client that validates every redirect hop before following it, so
// a public fetch cannot pivot into internal targets. The shared guardedClient supplies the redirect
// cap and the timeout fallback.
func ssrfSafeClient(base *http.Client) *http.Client {
	return guardedClient(base, HTTPClientShortTimeout, validateExternalURL)
}

func (s *Server) handleAutomationPage(c *gin.Context) {
	rules := s.loadAutomationRules()
	// The page embeds rule action params (webhook URLs carry secrets):
	// scoped operators only render their own tenant's rules.
	if tid := s.currentTenantID(c); tid != 0 {
		kept := make([]AutomationRule, 0, len(rules))
		for _, r := range rules {
			if r.TenantID == tid {
				kept = append(kept, r)
			}
		}
		rules = kept
	}
	var webhooks []db.WebhookConfig
	if err := s.db.Limit(500).Find(&webhooks).Error; err != nil {
		slog.Error("Failed to list webhooks for automation page", "err", err)
	}
	s.renderPageOrJSON(c, gin.H{
		"Title":     "Automation",
		"ActiveNav": "automation",
		"Rules":     rules,
		"Webhooks":  webhooks,
	})
}

func (s *Server) handleListAutomationRules(c *gin.Context) {
	p := parsePagination(c, 50, 200)
	var total int64
	if err := s.tenantScope(s.db.Model(&db.AutomationRule{}), c).Count(&total).Error; err != nil {
		handleQueryError(c, err, "Failed to count automation rules")
		return
	}
	var dbRules []db.AutomationRule
	if err := s.tenantScope(s.db, c).Offset(p.Offset).Limit(p.PageSize).Find(&dbRules).Error; err != nil {
		handleQueryError(c, err, "Failed to list automation rules")
		return
	}
	var rules []AutomationRule
	for _, dr := range dbRules {
		var conditions []RuleCondition
		if dr.Conditions != "" {
			if err := json.Unmarshal([]byte(dr.Conditions), &conditions); err != nil {
				slog.Warn("Automation: unmarshal conditions", "rule", dr.Name, "error", err)
			}
		}
		var actions []RuleAction
		if dr.Actions != "" {
			if err := json.Unmarshal([]byte(dr.Actions), &actions); err != nil {
				slog.Warn("Automation: unmarshal actions", "rule", dr.Name, "error", err)
			}
		}
		rules = append(rules, AutomationRule{
			ID:         dr.ID,
			Name:       dr.Name,
			Enabled:    dr.Enabled,
			EventType:  dr.EventType,
			Conditions: conditions,
			Actions:    actions,
			CreatedAt:  dr.CreatedAt.Format(time.RFC3339),
			TenantID:   dr.TenantID,
		})
	}
	// Keyed "rules" (not "data"): the UI reads `rules ?? data` off the unwrapped
	// payload, and an unwrapped array has neither key — which is why this list
	// rendered empty before.
	respond(c, gin.H{"rules": rules, "total": total, "page": p.Page, "page_size": p.PageSize})
}

func (s *Server) handleSaveAutomationRule(c *gin.Context) {
	var rule AutomationRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Rule operation"))
		return
	}
	if rule.ID == "" {
		// Same clock-collision guard as the AI tool path: two operators saving a
		// rule in the same tick must not collide on the primary key.
		rule.ID = "rule_" + uuid.NewString()
	}
	// New rules belong to the operator's tenant; the body must not set it.
	rule.TenantID = s.currentTenantID(c)
	if rule.CreatedAt == "" {
		rule.CreatedAt = time.Now().Format(time.RFC3339)
	}
	if err := s.saveAutomationRule(rule); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Rule operation"))
		return
	}
	respond(c, rule)
}

// loadAutomationRuleScoped reads one rule from the automation cache and
// enforces tenant visibility: legacy operators (tid 0) see all, scoped
// operators only their own tenant's. ok=false doubles as "not found".
func (s *Server) loadAutomationRuleScoped(c *gin.Context, ruleID string) (AutomationRule, bool) {
	tid := s.currentTenantID(c)
	for _, r := range s.loadAutomationRules() {
		if r.ID == ruleID {
			if tid != 0 && r.TenantID != tid {
				return AutomationRule{}, false
			}
			return r, true
		}
	}
	return AutomationRule{}, false
}

func (s *Server) handleUpdateAutomationRule(c *gin.Context) {
	ruleID := c.Param("id")
	var rule AutomationRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Rule operation"))
		return
	}
	existing, ok := s.loadAutomationRuleScoped(c, ruleID)
	if !ok {
		respondError(c, http.StatusNotFound, "Rule not found")
		return
	}
	rule.ID = ruleID
	// Tenant is immutable from the API: keep the stored value so an
	// omitted/zeroed body field cannot move the rule across tenants.
	rule.TenantID = existing.TenantID
	if err := s.saveAutomationRule(rule); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Rule operation"))
		return
	}
	respond(c, rule)
}

func (s *Server) handleDeleteAutomationRule(c *gin.Context) {
	ruleID := c.Param("id")
	if _, ok := s.loadAutomationRuleScoped(c, ruleID); !ok {
		respondError(c, http.StatusNotFound, "Rule not found")
		return
	}
	if err := s.deleteAutomationRule(ruleID); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Rule operation"))
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handleToggleAutomationRule(c *gin.Context) {
	ruleID := c.Param("id")
	r, ok := s.loadAutomationRuleScoped(c, ruleID)
	if !ok {
		respondError(c, http.StatusNotFound, "rule not found")
		return
	}
	r.Enabled = !r.Enabled
	if err := s.saveAutomationRule(r); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Rule operation"))
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handleListWebhooks(c *gin.Context) {
	var webhooks []db.WebhookConfig
	if err := s.tenantScope(s.db, c).Limit(500).Find(&webhooks).Error; err != nil {
		// An empty 200 here reads as "no webhooks configured": alerting
		// silently looks disabled.
		handleQueryError(c, err, "Failed to list webhooks")
		return
	}
	respond(c, gin.H{"webhooks": webhooks})
}

func (s *Server) handleCreateWebhook(c *gin.Context) {
	var wh db.WebhookConfig
	if err := c.ShouldBindJSON(&wh); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Webhook operation"))
		return
	}
	if err := validateWebhookURL(wh.URL); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Webhook operation"))
		return
	}
	// New webhooks belong to the operator's tenant; the body must not set it.
	wh.TenantID = s.currentTenantID(c)
	if err := s.db.Create(&wh).Error; err != nil {
		slog.Error("Failed to create webhook", "err", err)
		respondError(c, http.StatusInternalServerError, "failed to create webhook")
		return
	}
	respond(c, wh)
}

func (s *Server) handleDeleteWebhook(c *gin.Context) {
	id := c.Param("id")
	var existing db.WebhookConfig
	if err := s.tenantScope(s.db, c).First(&existing, "id = ?", id).Error; err != nil {
		respondError(c, http.StatusNotFound, "webhook not found")
		return
	}
	res := s.tenantScope(s.db, c).Delete(&db.WebhookConfig{}, id)
	if res.Error != nil {
		slog.Error("Failed to delete webhook", "id", id, "err", res.Error)
		respondError(c, http.StatusInternalServerError, "failed to delete webhook")
		return
	}
	if res.RowsAffected == 0 {
		respondError(c, http.StatusNotFound, "webhook not found")
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handleTestWebhook(c *gin.Context) {
	var req struct {
		URL    string `json:"url"`
		Method string `json:"method"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Webhook operation"))
		return
	}
	if req.Method == "" {
		req.Method = "POST"
	}
	if err := validateWebhookURL(req.URL); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Webhook operation"))
		return
	}
	evt := Event{
		Type:      EventImplantCheckin,
		AgentID:   "test",
		AgentHost: "test-host",
		Timestamp: time.Now(),
		Data:      map[string]interface{}{"test": true},
	}
	if err := s.fireWebhook(db.WebhookConfig{
		Name:   "test",
		URL:    req.URL,
		Method: req.Method,
	}, evt); err != nil {
		respondError(c, http.StatusBadGateway, sanitizeError(err, "Webhook delivery"))
		return
	}
	respond(c, gin.H{"message": "webhook test delivered"})
}

func (s *Server) handlePluginList(c *gin.Context) {
	search := c.Query("search")
	category := c.Query("category")
	pluginType := c.Query("type")

	var plugins []db.Plugin
	query := s.db.Model(&db.Plugin{})

	if search != "" {
		query = query.Where("(name LIKE ? ESCAPE '\\' OR description LIKE ? ESCAPE '\\' OR author LIKE ? ESCAPE '\\')",
			"%"+escapeLike(search)+"%", "%"+escapeLike(search)+"%", "%"+escapeLike(search)+"%")
	}
	if category != "" {
		query = query.Where("category = ?", category)
	}
	if pluginType != "" {
		query = query.Where("type = ?", pluginType)
	}

	if err := query.Limit(200).Find(&plugins).Error; err != nil {
		slog.Error("Failed to list marketplace plugins", "err", err)
	}

	// The marketplace enriches ratings and download counts; a server built without it (a partial init, or a test) still has an
	// inventory to list, so a nil guard here keeps the list usable rather than panicking.
	if s.marketplace != nil {
		s.marketplace.BatchEnrichPlugins(plugins)
	}

	// ?format=csv takes the inventory away. Every other list in the console can be exported, and the plugin set is the one an
	// operator is asked to produce for a review ("what was installed, and what version").
	if strings.EqualFold(strings.TrimSpace(c.Query("format")), "csv") {
		var buf strings.Builder
		writer := csv.NewWriter(&buf)
		header := []string{"ID", "Name", "Version", "Type", "Category", "Author", "Enabled"}
		if err := writer.Write(header); err != nil {
			handleQueryError(c, err, "Failed to encode the plugin inventory")
			return
		}
		for _, plugin := range plugins {
			if err := writer.Write([]string{
				strconv.FormatUint(uint64(plugin.ID), 10),
				plugin.Name,
				plugin.Version,
				plugin.Type,
				plugin.Category,
				plugin.Author,
				strconv.FormatBool(plugin.Enabled),
			}); err != nil {
				handleQueryError(c, err, "Failed to encode the plugin inventory")
				return
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			handleQueryError(c, err, "Failed to encode the plugin inventory")
			return
		}
		name := "forgec2_plugins_" + time.Now().Format("2006-01-02_150405") + ".csv"
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		// The list is capped at 200 rows, and a capped inventory that looks complete is the same lie as a capped export.
		if len(plugins) >= 200 {
			c.Header("X-Export-Truncated", "true")
		}
		if scope := taskExportScope(c); scope != "" {
			c.Header(taskExportScopeHeader, scope)
			applyExportScope(c, scope)
		} else {
			c.Header(taskExportScopeHeader, "all")
			applyExportScope(c, "all")
		}
		c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(buf.String()))
		return
	}

	respond(c, plugins)
}

func (s *Server) handlePluginToggle(c *gin.Context) {
	p, err := s.resolvePluginRecord(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusNotFound, "plugin not found")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	_ = c.ShouldBindJSON(&body)
	if body.Enabled != nil {
		p.Enabled = *body.Enabled
	} else {
		p.Enabled = !p.Enabled
	}
	if err := s.db.Save(p).Error; err != nil {
		slog.Error("Failed to save plugin toggle", "plugin_id", p.ID, "err", err)
		respondError(c, http.StatusInternalServerError, "failed to toggle plugin")
		return
	}
	if p.Name != "" && s.pluginManager != nil {
		if err := s.pluginManager.SetEnabled(p.Name, p.Enabled); err != nil {
			// The row already carries the new state, but the runtime refused it.
			// Reporting success would leave the console claiming a state the
			// manager never adopted, so revert the row and fail the request.
			revert := !p.Enabled
			if rbErr := s.db.Model(&db.Plugin{}).Where("id = ?", p.ID).Update("enabled", revert).Error; rbErr != nil {
				slog.Error("Automation: failed to revert plugin toggle", "plugin_id", p.ID, "error", rbErr)
			}
			slog.Error("Automation: failed to set plugin enabled state", "plugin_name", p.Name, "enabled", p.Enabled, "error", err)
			respondError(c, http.StatusConflict, "plugin manager rejected the change: "+sanitizeError(err, "Plugin toggle"))
			return
		}
	}
	respond(c, gin.H{"enabled": p.Enabled})
}

func (s *Server) handlePluginDelete(c *gin.Context) {
	p, err := s.resolvePluginRecord(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusNotFound, "plugin not found")
		return
	}

	if p.Name != "" && s.pluginManager != nil {
		pluginDir := s.pluginManager.PluginDir(p.Name)
		// Quarantine under the CONFIGURED data directory: the hardcoded "data" was relative
		// to the process working directory, so a deleted plugin's source was moved somewhere
		// else entirely, and any cleanup walking the configured tree never saw it.
		quarantineRoot := s.rawDataDir()
		if quarantineRoot == "" {
			quarantineRoot = "data"
		}
		quarantineDir := filepath.Join(quarantineRoot, "quarantine", "plugins", p.Name)
		if pluginDir != "" {
			if _, statErr := os.Stat(pluginDir); statErr == nil {
				if mkErr := os.MkdirAll(filepath.Dir(quarantineDir), 0o755); mkErr != nil {
					slog.Error("Failed to create quarantine directory", "dir", quarantineDir, "err", mkErr)
				} else if mvErr := os.Rename(pluginDir, quarantineDir); mvErr != nil {
					slog.Warn("Failed to quarantine plugin source, removing instead", "dir", pluginDir, "err", mvErr)
					_ = os.RemoveAll(pluginDir)
				} else {
					slog.Info("Plugin source quarantined", "name", p.Name, "quarantine", quarantineDir)
				}
			}
		}
		if err := s.pluginManager.Unregister(p.Name); err != nil {
			slog.Error("Automation: failed to unregister plugin", "plugin_name", p.Name, "error", err)
		}
	}
	if err := s.db.Delete(p).Error; err != nil {
		slog.Error("Failed to delete plugin", "plugin_id", p.ID, "err", err)
		respondError(c, http.StatusInternalServerError, "failed to delete plugin")
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handlePluginCreate(c *gin.Context) {
	var p db.Plugin
	if err := c.ShouldBindJSON(&p); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Plugin operation"))
		return
	}
	if err := s.db.Create(&p).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Plugin operation"))
		return
	}
	s.tryRegisterPluginFromDisk(p.Name)
	respond(c, p)
}

func (s *Server) handlePluginsPage(c *gin.Context) {
	s.renderPageOrJSON(c, gin.H{"Title": "Plugin Marketplace", "ActiveNav": "plugins"})
}

func (s *Server) handlePluginGet(c *gin.Context) {
	id := c.Param("id")
	var plugin db.Plugin
	if err := s.db.First(&plugin, id).Error; err != nil {
		respondError(c, http.StatusNotFound, "plugin not found")
		return
	}
	respond(c, plugin)
}

func (s *Server) handlePluginRating(c *gin.Context) {
	id := c.Param("id")
	var pluginID uint
	if _, err := fmt.Sscanf(id, "%d", &pluginID); err != nil {
		respondError(c, http.StatusBadRequest, "invalid plugin id")
		return
	}

	summary, err := s.marketplace.GetRatingSummary(pluginID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Plugin operation"))
		return
	}
	respond(c, summary)
}

func (s *Server) handlePluginReviews(c *gin.Context) {
	id := c.Param("id")
	var pluginID uint
	if _, err := fmt.Sscanf(id, "%d", &pluginID); err != nil {
		respondError(c, http.StatusBadRequest, "invalid plugin id")
		return
	}

	reviews, err := s.marketplace.GetReviews(pluginID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Plugin operation"))
		return
	}
	respond(c, reviews)
}

func (s *Server) handlePluginAddReview(c *gin.Context) {
	id := c.Param("id")
	var pluginID uint
	if _, err := fmt.Sscanf(id, "%d", &pluginID); err != nil {
		respondError(c, http.StatusBadRequest, "invalid plugin id")
		return
	}

	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)
	username, _ := c.Get("user")
	uname, _ := username.(string)

	var req struct {
		Rating  int    `json:"rating"`
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Plugin operation"))
		return
	}

	if err := s.marketplace.AddReview(pluginID, uid, uname, req.Rating, req.Comment); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Plugin operation"))
		return
	}
	respond(c, gin.H{})
}

// handlePluginRate submits a rating for a plugin.
func (s *Server) handlePluginRate(c *gin.Context) {
	id := c.Param("id")
	var pluginID uint
	if _, err := fmt.Sscanf(id, "%d", &pluginID); err != nil {
		respondError(c, http.StatusBadRequest, "invalid plugin id")
		return
	}

	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)
	username, _ := c.Get("user")
	uname, _ := username.(string)

	var req struct {
		Rating int `json:"rating"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Plugin operation"))
		return
	}
	if req.Rating < 1 || req.Rating > 5 {
		respondError(c, http.StatusBadRequest, "rating must be 1-5")
		return
	}

	if err := s.marketplace.AddReview(pluginID, uid, uname, req.Rating, ""); err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Plugin operation"))
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handlePluginDependencies(c *gin.Context) {
	id := c.Param("id")
	var pluginID uint
	if _, err := fmt.Sscanf(id, "%d", &pluginID); err != nil {
		respondError(c, http.StatusBadRequest, "invalid plugin id")
		return
	}

	deps, err := s.marketplace.GetDependencies(pluginID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Plugin operation"))
		return
	}
	respond(c, deps)
}

func (s *Server) handlePluginUpdateStatus(c *gin.Context) {
	id := c.Param("id")
	var pluginID uint
	if _, err := fmt.Sscanf(id, "%d", &pluginID); err != nil {
		respondError(c, http.StatusBadRequest, "invalid plugin id")
		return
	}

	status, err := s.marketplace.GetUpdateStatus(pluginID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Plugin operation"))
		return
	}

	var plugin db.Plugin
	if err := s.db.First(&plugin, pluginID).Error; err != nil {
		respondError(c, http.StatusNotFound, "plugin not found")
		return
	}

	respond(c, gin.H{
		"plugin_id":        status.PluginID,
		"latest_version":   status.LatestVersion,
		"current_version":  plugin.Version,
		"update_available": status.UpdateAvailable,
		"update_url":       status.UpdateURL,
		"release_notes":    status.ReleaseNotes,
		"last_checked_at":  status.LastCheckedAt,
	})
}

func (s *Server) handlePluginUpdate(c *gin.Context) {
	id := c.Param("id")
	var pluginID uint
	if _, err := fmt.Sscanf(id, "%d", &pluginID); err != nil {
		respondError(c, http.StatusBadRequest, "invalid plugin id")
		return
	}

	if err := s.marketplace.UpdatePlugin(pluginID); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Plugin operation"))
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handlePluginExport(c *gin.Context) {
	id := c.Param("id")
	var pluginID uint
	if _, err := fmt.Sscanf(id, "%d", &pluginID); err != nil {
		respondError(c, http.StatusBadRequest, "invalid plugin id")
		return
	}

	data, err := s.marketplace.ExportPlugin(pluginID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Plugin operation"))
		return
	}

	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", "attachment; filename=plugin-"+id+".json")
	c.Data(http.StatusOK, "application/json", data)
}

func (s *Server) handlePluginImport(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		respondError(c, http.StatusBadRequest, "no file provided")
		return
	}
	if file.Size > MaxUploadSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("file too large (max %d bytes)", MaxUploadSize))
		return
	}

	f, err := file.Open()
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Plugin operation"))
		return
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Plugin operation"))
		return
	}

	plugin, err := s.marketplace.ImportPlugin(data)
	if err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Plugin operation"))
		return
	}
	s.tryRegisterPluginFromDisk(plugin.Name)
	respond(c, plugin)
}

func (s *Server) handlePluginCheckUpdates(c *gin.Context) {
	s.marketplace.CheckAllUpdates()
	respond(c, gin.H{})
}

func (s *Server) handlePluginUpdateSummary(c *gin.Context) {
	var plugins []db.Plugin
	if err := s.db.Limit(200).Find(&plugins).Error; err != nil {
		slog.Error("Failed to list plugins for update summary", "err", err)
	}

	var availableCount int
	var lastChecked time.Time

	for _, p := range plugins {
		status, err := s.marketplace.GetUpdateStatus(p.ID)
		if err == nil {
			if status.UpdateAvailable {
				availableCount++
			}
			if status.LastCheckedAt.After(lastChecked) {
				lastChecked = status.LastCheckedAt
			}
		}
	}

	respond(c, gin.H{
		"available_count": availableCount,
		"total_plugins":   len(plugins),
		"last_checked":    lastChecked,
	})
}

func (s *Server) handleBOFRepoIndex(c *gin.Context) {
	// Fetch trusted BOF repos from config
	type BOFRepoEntry struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		Description string `json:"description"`
	}
	repos := []BOFRepoEntry{
		{Name: "TrustedSec BOF", URL: "https://github.com/trustedsec/CS-Remote-OPs-BOF", Description: "TrustedSec BOF collection"},
		{Name: "Rafael BOF", URL: "https://github.com/outflanknl/CS-Remote-OPs-BOF", Description: "Outflank BOF collection"},
		{Name: "Encode BOF", URL: "https://github.com/anthemtotheego/BOFs", Description: "Anthem BOF collection"},
	}
	respond(c, repos)
}

func (s *Server) handleBOFRepoImport(c *gin.Context) {
	var req struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "BOF operation"))
		return
	}
	if req.URL == "" || req.Filename == "" {
		respondError(c, http.StatusBadRequest, "url and filename are required")
		return
	}

	// Validate URL to prevent SSRF: scheme + full IP-space check of the
	// resolved host (loopback/RFC1918/ULA/link-local/multicast all blocked).
	if err := validateExternalURL(req.URL); err != nil {
		respondError(c, http.StatusBadRequest, "url rejected: "+err.Error())
		return
	}

	resp, err := ssrfSafeClient(s.httpClient).Get(req.URL)
	if err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "BOF operation"))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("download failed: HTTP %d", resp.StatusCode))
		return
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxUploadSize+1))
	if err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "BOF operation"))
		return
	}
	if int64(len(data)) > MaxUploadSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("file too large (max %d bytes)", MaxUploadSize))
		return
	}

	name := req.Filename
	if !strings.HasSuffix(strings.ToLower(name), ".o") {
		name += ".o"
	}

	bof := db.BOFFile{
		Name:        name,
		Data:        data,
		Size:        int64(len(data)),
		Description: "Imported from " + req.URL,
		CreatedBy:   c.GetString("user"),
	}
	if err := s.db.Create(&bof).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "BOF operation"))
		return
	}

	s.LogAuditRecord(c, "bof_import", "bof", "", fmt.Sprintf("BOF imported: %s (%d bytes) from %s", name, len(data), req.URL), true, nil)
	respond(c, gin.H{
		"message": fmt.Sprintf("Imported %s (%d bytes)", name, len(data)),
		"bof_id":  bof.ID,
	})
}

// bofRatings stores in-memory ratings for BOF repo items
var (
	bofRatings   = map[string]int{}
	bofRatingsMu sync.RWMutex
)

// handleBOFRepoRate rates a BOF repo item.
func (s *Server) handleBOFRepoRate(c *gin.Context) {
	itemID := c.Param("id")
	if itemID == "" {
		respondError(c, http.StatusBadRequest, "item id required")
		return
	}

	var req struct {
		Rating int `json:"rating"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Rating < 0 || req.Rating > 5 {
		respondError(c, http.StatusBadRequest, "rating must be 0-5")
		return
	}

	bofRatingsMu.Lock()
	bofRatings[itemID] = req.Rating
	bofRatingsMu.Unlock()
	respond(c, gin.H{})
}
