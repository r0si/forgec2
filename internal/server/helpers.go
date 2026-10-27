package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/forgec2/forgec2/internal/config"
	"github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
	"github.com/gin-gonic/gin"
)

// respond sends the canonical success envelope:
//
//	{"success": true, "data": <payload>}
//
// Every JSON 200 body from this server uses this shape (errors use
// respondError's {"success": false, "error": "..."}). Pass a plain payload,
// never gin.H{"success": true, ...}: that would nest a second envelope, and
// TestSuccessEnvelope pins the single-wrap contract.
func respond(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// respondError sends a JSON error response with the given HTTP status and message.
func respondError(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"success": false, "error": msg})
}

// respondErrorSafe sends a JSON error response with the error message sanitized
// through sanitizeError, so raw err.Error() is never exposed to clients.
func respondErrorSafe(c *gin.Context, status int, err error, context string) {
	respondError(c, status, sanitizeError(err, context))
}

// handleQueryError logs a database/query failure and returns a 500 to the
// client. Use it instead of the "log and fall through to an empty 200"
// pattern so transient failures are never reported as successful empty lists.
func handleQueryError(c *gin.Context, err error, msg string) {
	slog.Error(msg, "err", err)
	respondError(c, http.StatusInternalServerError, "Query failed")
}

// csvSafe neutralizes spreadsheet formula injection in CSV exports: cells
// beginning with = + - @ or a tab/carriage return are prefixed with a single
// quote so spreadsheet applications treat them as text.
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// sanitizeError maps known internal errors to safe user-facing messages.
// Unknown errors return a generic message — never expose err.Error() to clients.
func sanitizeError(err error, context string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "duplicate") || strings.Contains(msg, "UNIQUE"):
		return "A record with the same value already exists"
	case strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column"):
		return "Database schema error — restart the server to apply migrations"
	case strings.Contains(msg, "foreign key") || strings.Contains(msg, "FOREIGN KEY"):
		return "Cannot complete: related records still reference this item"
	case strings.Contains(msg, "record not found") || strings.Contains(msg, "ErrNoRows"):
		return "Record not found"
	case strings.Contains(msg, "permission denied") || strings.Contains(msg, "access denied"):
		return "Permission denied"
	case strings.Contains(msg, "connection refused"):
		return "External service is unreachable"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return "Operation timed out"
	case strings.Contains(msg, "no space left"):
		return "Disk space is full"
	default:
		if context != "" {
			return context + " failed"
		}
		return "An internal error occurred"
	}
}

// sanitizeRequestHost validates the Host header before it is embedded in a URL
// or a generated command. Host is attacker-controlled: an unvalidated value can
// point a generated one-liner at another server (the operator then downloads
// and runs an attacker payload) or break out of the quoted string entirely.
// Accepts hostname[:port] and bracketed IPv6, nothing else.
func sanitizeRequestHost(raw string) (string, bool) {
	host := strings.TrimSpace(raw)
	if host == "" || len(host) > 255 {
		return "", false
	}
	hostOnly, port, err := net.SplitHostPort(host)
	if err != nil {
		// No port part. A bare IPv6 literal needs brackets to be unambiguous.
		if strings.ContainsAny(host, "[]") {
			if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") &&
				net.ParseIP(strings.Trim(host, "[]")) != nil {
				return host, true
			}
			return "", false
		}
		if strings.Contains(host, ":") {
			return "", false
		}
		hostOnly, port = host, ""
	}
	if hostOnly == "" {
		return "", false
	}
	if port != "" {
		n, convErr := strconv.Atoi(port)
		if convErr != nil || n < 1 || n > 65535 {
			return "", false
		}
	}
	if net.ParseIP(hostOnly) == nil {
		// A hostname: letters, digits, dots and hyphens only. This is what
		// blocks quotes, spaces, semicolons, '$' and backticks.
		for _, r := range hostOnly {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			default:
				return "", false
			}
		}
		if strings.HasPrefix(hostOnly, "-") || strings.HasSuffix(hostOnly, "-") {
			return "", false
		}
	}
	if port != "" {
		return net.JoinHostPort(hostOnly, port), true
	}
	return hostOnly, true
}

var sanitizeRe = regexp.MustCompile(`[^\w.\-]`)

// registerBoth registers a handler under both /api/<path> and /<path>.
func registerBoth(rg *gin.RouterGroup, method, path string, handler gin.HandlerFunc) {
	apiPath := "/api" + path
	switch method {
	case http.MethodGet:
		rg.GET(apiPath, handler)
		rg.GET(path, handler)
	case http.MethodPost:
		rg.POST(apiPath, handler)
		rg.POST(path, handler)
	case http.MethodPut:
		rg.PUT(apiPath, handler)
		rg.PUT(path, handler)
	case http.MethodDelete:
		rg.DELETE(apiPath, handler)
		rg.DELETE(path, handler)
	}
}

// --- #5: File upload helper ---

// readFileUpload reads a multipart file upload, validates size, and returns the
// bytes and filename. Sends an error response and returns nil + false on failure.
func readFileUpload(c *gin.Context, fieldName string) ([]byte, string, bool) {
	file, err := c.FormFile(fieldName)
	if err != nil {
		respondError(c, http.StatusBadRequest, fieldName+" file required")
		return nil, "", false
	}
	if file.Size > MaxUploadSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("file too large (max %d bytes)", MaxUploadSize))
		return nil, "", false
	}
	f, err := file.Open()
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to open uploaded file")
		return nil, "", false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to read uploaded file")
		return nil, "", false
	}
	return data, file.Filename, true
}

// --- #6: DB lookup helper ---

// requireAdmin checks that the current user is an admin. Returns false and sends
// a 403 response if not.
func (s *Server) requireAdmin(c *gin.Context) bool {
	role, _ := c.Get("user_role")
	if role != "admin" {
		respondError(c, http.StatusForbidden, "admin role required")
		return false
	}
	return true
}

// requireOperator checks that the current user holds a role that exists and is
// not a read-only legacy role. It used to be a blocklist ("viewer" and ""),
// which meant any unknown or mistyped role string silently gained operator
// rights; a role must now be a built-in one (admin/user), the legacy
// "operator" alias that MigrateOldRoles rewrites to "user", or a custom role
// that actually exists in custom_roles.
func (s *Server) requireOperator(c *gin.Context) bool {
	role, _ := c.Get("user_role")
	roleStr, _ := role.(string)
	if !s.roleGrantsOperator(roleStr) {
		respondError(c, http.StatusForbidden, "operator role required")
		return false
	}
	return true
}

// roleGrantsOperator reports whether role may act as an operator. Unknown
// roles fail closed (a DB error also denies) so a forged or future role name
// cannot inherit operator rights by default.
func (s *Server) roleGrantsOperator(role string) bool {
	switch role {
	case "", "viewer", "guest":
		return false
	case db.RoleAdmin, db.RoleUser, "operator":
		return true
	}
	if s.db == nil {
		return false
	}
	var count int64
	if err := s.db.Model(&db.CustomRole{}).Where("name = ?", role).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

// findOrFail fetches a record by primary key. Returns true if found, or sends a
// 404 response and returns false.
func (s *Server) findOrFail(c *gin.Context, dest interface{}, id, entityName string) bool {
	if err := s.db.First(dest, "id = ?", id).Error; err != nil {
		respondError(c, http.StatusNotFound, entityName+" not found")
		return false
	}
	return true
}

// findOrFailPreload fetches a record with preloaded associations by primary key.
func (s *Server) findOrFailPreload(c *gin.Context, dest interface{}, id, entityName string, preloads ...string) bool {
	q := s.db
	for _, p := range preloads {
		q = q.Preload(p)
	}
	if err := q.First(dest, "id = ?", id).Error; err != nil {
		respondError(c, http.StatusNotFound, entityName+" not found")
		return false
	}
	return true
}

// currentUserID extracts the authenticated operator's user id. Returns false
// (no response written — the caller decides the status) when the auth
// middleware didn't populate it or stored an unexpected type. Prefer this
// over bare userID.(uint) assertions, which panic the handler goroutine.
func currentUserID(c *gin.Context) (uint, bool) {
	v, ok := c.Get("user_id")
	if !ok {
		return 0, false
	}
	uid, ok := v.(uint)
	if !ok || uid == 0 {
		return 0, false
	}
	return uid, true
}

// findTenantOrFail fetches a tenant-owned record by primary key, applying
// tenantScope. A tenant-scoped operator gets 404 for other tenants' rows
// (closing a cross-tenant IDOR on single-item reads); legacy/unscoped
// operators (tenant 0) keep global visibility, mirroring list behavior.
// Use this for every entity carrying TenantID instead of findOrFail.
func (s *Server) findTenantOrFail(c *gin.Context, dest interface{}, id, entityName string) bool {
	if err := s.tenantScope(s.db, c).First(dest, "id = ?", id).Error; err != nil {
		respondError(c, http.StatusNotFound, entityName+" not found")
		return false
	}
	return true
}

// findVisibleTask loads a task by ID and verifies its owning agent is visible
// to the caller. Tasks don't carry a reliable TenantID at creation time, so
// the gate goes through the agent (the same pattern the AI tools use with
// agent_id subqueries). Returns 404 when either the task or its agent is
// outside the caller's tenant.
func (s *Server) findVisibleTask(c *gin.Context, taskID uint) (*db.Task, bool) {
	var task db.Task
	if err := s.db.First(&task, taskID).Error; err != nil {
		respondError(c, http.StatusNotFound, "task not found")
		return nil, false
	}
	if _, ok := s.getAgentOrFail(c, task.AgentID); !ok {
		return nil, false
	}
	return &task, true
}

// agentVisible reports whether an agent is visible to the caller WITHOUT
// writing any response — for bulk loops where a 404 per item would corrupt
// the single JSON response. tenantScope keeps legacy (tenant 0) global.
func (s *Server) agentVisible(c *gin.Context, agentID string) bool {
	if agentID == "" {
		return false
	}
	var n int64
	if err := s.tenantScope(s.db.Model(&db.Implant{}), c).Where("id = ?", agentID).Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// --- #7: Pagination helper ---

// paginationParams holds parsed pagination query parameters.
type paginationParams struct {
	Page     int
	PageSize int
	Offset   int
}

// parsePagination reads page/page_size query parameters with sane defaults.
// Accepts both "page_size" and "pageSize" query params for backward compatibility.
func parsePagination(c *gin.Context, defaultPageSize, maxPageSize int) paginationParams {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSizeStr := c.DefaultQuery("page_size", "")
	if pageSizeStr == "" {
		pageSizeStr = c.DefaultQuery("pageSize", strconv.Itoa(defaultPageSize))
	}
	pageSize, _ := strconv.Atoi(pageSizeStr)
	if pageSize < 1 {
		pageSize = defaultPageSize
	} else if pageSize > maxPageSize {
		// Clamp, don't reset: a client asking for "as many as you'll give
		// me" (e.g. full-fleet dashboards) must get the max, not 20.
		pageSize = maxPageSize
	}
	return paginationParams{
		Page:     page,
		PageSize: pageSize,
		Offset:   (page - 1) * pageSize,
	}
}

// csvSanitize prevents CSV injection by stripping dangerous leading characters.
func csvSanitize(s string) string {
	s = strings.ReplaceAll(s, "\"", "\"\"")
	if len(s) > 0 && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@' || s[0] == '\t' || s[0] == '\r' || s[0] == '\n') {
		s = "'" + s
	}
	return s
}

// serveFileSafe serves a file after validating it falls within the base directory.
// If downloadName is non-empty, it triggers a download (Content-Disposition).
func serveFileSafe(c *gin.Context, absPath, baseDir, downloadName string) {
	if err := validateFilePath(absPath, baseDir); err != nil {
		respondError(c, http.StatusNotFound, "file not found")
		return
	}
	// Resolve symlinks and re-validate containment: a symlink living inside
	// baseDir must not be used to serve a file outside it.
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		respondError(c, http.StatusNotFound, "file not found")
		return
	}
	if err := validateFilePath(resolved, baseDir); err != nil {
		respondError(c, http.StatusNotFound, "file not found")
		return
	}
	if downloadName != "" {
		c.FileAttachment(resolved, downloadName)
	} else {
		c.File(resolved)
	}
}

// allowedOrigin checks whether an HTTP request's Origin is permitted based on
// the server's AllowedOrigins config. When empty, only localhost origins pass.
func allowedOrigin(cfg *config.Config, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	originHost := u.Hostname()
	if len(cfg.Server.AllowedOrigins) == 0 {
		return originHost == "localhost" || originHost == "127.0.0.1" || originHost == "::1"
	}
	for _, allowed := range cfg.Server.AllowedOrigins {
		if originHost == allowed {
			return true
		}
	}
	return false
}

// sanitizeFilename strips characters that could enable Content-Disposition header injection
// and prevents path traversal by using filepath.Base and removing leading dots.
func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.TrimLeft(name, ".")
	name = sanitizeRe.ReplaceAllString(name, "_")
	if name == "" || name == "." {
		return "download"
	}
	return name
}

// keyHexTo32 decodes a 64-character hex key string into 32 raw key bytes.
// There is intentionally NO fallback derivation: credentials must be bound to
// an explicit independent key (crypto.totp_key, the credential key), never to
// the JWT secret.
func keyHexTo32(keyHex string) ([]byte, error) {
	if len(keyHex) != 64 {
		return nil, fmt.Errorf("key must be a 64-character hex string (32 bytes), got %d chars", len(keyHex))
	}
	b, err := hex.DecodeString(keyHex)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("key must be a valid 64-character hex string (32 bytes)")
	}
	return b, nil
}

// encryptSecret encrypts a plaintext string using AES-256-GCM with the
// dedicated credential key (crypto.totp_key).
// Output format: base64(nonce(12) + ciphertext).
func encryptSecret(plaintext, keyHex string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	key, err := keyHexTo32(keyHex)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptSecret decrypts a base64-encoded AES-256-GCM ciphertext with the
// dedicated credential key (crypto.totp_key).
func decryptSecret(encoded, keyHex string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	key, err := keyHexTo32(keyHex)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// validateFilePath ensures a file path is contained within the expected base directory.
func validateFilePath(filePath, baseDir string) error {
	cleaned := filepath.Clean(filePath)
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return fmt.Errorf("resolve base dir: %w", err)
	}
	absFile, err := filepath.Abs(cleaned)
	if err != nil {
		return fmt.Errorf("resolve file path: %w", err)
	}
	if !strings.HasPrefix(absFile, absBase+string(filepath.Separator)) && absFile != absBase {
		return fmt.Errorf("path traversal detected: %s", filePath)
	}
	return nil
}

// validateWebhookURL checks that a webhook URL does not target private/internal IPs.
// webhookTargetValidator is the SSRF gate for outbound webhook deliveries.
// It is a var so tests can point a delivery at a local receiver; production
// uses validateWebhookURL unchanged (same pattern as releaseAssetsURLFmt).
var webhookTargetValidator = validateWebhookURL

// nonPublicHostError reports why host must not be dialled by an outbound
// request, or nil when every address it denotes is public. It is shared by the
// webhook validator and the scripting bridge so the address policy cannot drift
// between them (it did: one copy ignored resolution failures).
func nonPublicHostError(host string) error {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	check := func(a netip.Addr) error {
		a = a.Unmap()
		if a.IsLoopback() || a.IsUnspecified() || a.IsPrivate() ||
			a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() {
			return fmt.Errorf("non-public address %s", a)
		}
		if a.Is4() {
			b := a.As4()
			if b[0] == 0 || (b[0] == 100 && b[1] >= 64 && b[1] <= 127) {
				return fmt.Errorf("non-public address %s", a)
			}
		}
		return nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return check(addr)
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("non-public address %s", host)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%s resolved to no addresses", host)
	}
	for _, ip := range ips {
		a, ok := netip.AddrFromSlice(ip)
		if !ok {
			continue
		}
		if err := check(a); err != nil {
			return fmt.Errorf("%s resolves to %w", host, err)
		}
	}
	return nil
}

func validateWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL has no hostname")
	}

	// Same address policy as validateExternalURL, including CGNAT and
	// multicast, and a resolution FAILURE is an error rather than a pass: a
	// host that cannot be resolved now must not be trusted when it later can.
	if err := nonPublicHostError(host); err != nil {
		return fmt.Errorf("webhook URL targets a non-public host: %w", err)
	}

	// Block known cloud metadata hostnames
	blockedHosts := []string{
		"169.254.169.254", "metadata.google.internal",
		"169.254.169.254.nip.io", "localhost",
	}
	for _, h := range blockedHosts {
		if strings.EqualFold(host, h) {
			return fmt.Errorf("webhook URL targets blocked hostname: %s", host)
		}
	}

	// Only allow http/https schemes
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook URL must use http or https scheme, got %s", u.Scheme)
	}

	return nil
}

// escapeLike escapes SQL LIKE wildcard characters in user input
// so they are treated as literal text rather than pattern characters.
// Usage: .Where("field LIKE ? ESCAPE '\\'", "%"+escapeLike(input)+"%")
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	s = strings.ReplaceAll(s, `[`, `\[`)
	s = strings.ReplaceAll(s, `]`, `\]`)
	return s
}

// isValidHost checks whether a host string is a valid IP address or hostname.
func isValidHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	return len(host) > 0 && len(host) <= 255 &&
		!strings.HasPrefix(host, "-") && !strings.HasSuffix(host, "-") &&
		!strings.Contains(host, "..") &&
		strings.Contains(host, ".")
}

// marshalJSONSafe wraps json.Marshal with error logging.
// Returns the JSON bytes and a boolean indicating success.
func marshalJSONSafe(v interface{}) ([]byte, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("json.Marshal failed", "error", err, "type", fmt.Sprintf("%T", v))
		return nil, false
	}
	return b, true
}

// encryptCredNotes encrypts sensitive credential notes with the loot key
// before storage. Fail-closed: on vault failure it returns an error and the
// caller must skip the write. The old behaviour stored the notes as plaintext
// with no error anywhere, which defeats at-rest encryption for harvested
// browser credentials.
func encryptCredNotes(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	enc, err := crypto.EncryptLoot(s)
	if err != nil {
		return "", err
	}
	return enc, nil
}

// decryptCredNotes decrypts credential notes stored via encryptCredNotes.
// Legacy plaintext values pass through unchanged.
func decryptCredNotes(s string) string {
	if s == "" {
		return ""
	}
	plain, err := crypto.DecryptLoot(s)
	if err != nil {
		return s
	}
	return plain
}
