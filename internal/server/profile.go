package server

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/malleable"
	"github.com/gin-gonic/gin"
)

func (s *Server) applyMalleableProfile(c *gin.Context, body []byte) {
	s.configMu.RLock()
	mp := s.cfg.Malleable
	s.configMu.RUnlock()
	if !mp.Enabled {
		c.Data(http.StatusOK, "application/json", body)
		return
	}

	// Explicit operator headers first: they win over every decoy and every
	// profile branch below (previously a preset silently dropped them).
	for k, v := range mp.Headers {
		c.Header(k, v)
	}
	// Per-beacon response decoys fill the gaps the operator left open.
	s.applyDecoyHeaders(c, mp.ProfileName)

	// Apply named profile preset if set
	if mp.ProfileName != "" {
		presets := malleable.PredefinedProfiles()
		if profile, ok := presets[mp.ProfileName]; ok {
			s.applyProfilePreset(c, body, profile)
			return
		}
		// v2 file-based profile (data/profiles/<name>.json): apply its
		// ServerOutput chain + headers so custom chains actually encode.
		if out, ct, headers, ok := s.applyV2FileProfile(mp.ProfileName, body); ok {
			for k, v := range headers {
				c.Header(k, v)
			}
			c.Header("Content-Type", ct)
			c.Status(http.StatusOK)
			c.Writer.Write(out)
			return
		}
	}

	statusCode := mp.StatusCode
	if statusCode < 100 || statusCode > 599 {
		statusCode = http.StatusOK
	}

	// Agent-symmetric cover pair (never v2/preset bytes directly: the agent
	// strips exactly the pair it learned, anything else bricks parsing).
	wrapped := string(body)
	prepend, appendStr := s.effectiveCoverTokens()
	if prepend != "" {
		wrapped = prepend + wrapped
	}
	if appendStr != "" {
		wrapped = wrapped + appendStr
	}

	// Explicit headers were already written up front (shared by all branches).
	ct := mp.ContentType
	if ct == "" {
		ct = "application/json"
	}
	c.Header("Content-Type", ct)

	c.Status(statusCode)
	c.Writer.WriteString(wrapped)
}

// filterPoolURI keeps a rotation URI only if it stays path-only on the
// already-connected C2 host. Defense in depth: profile validation rejects
// these first, but rotation must never steer beacons at absolute URLs even
// if validation ever relaxes.
func filterPoolURI(u string) (string, bool) {
	u = strings.TrimSpace(u)
	if u == "" || !strings.HasPrefix(u, "/") || strings.ContainsAny(u, " \t\r\n") {
		return "", false
	}
	return u, true
}

// filterPoolUA drops blank or header-injecting User-Agent pool entries.
func filterPoolUA(ua string) (string, bool) {
	ua = strings.TrimSpace(ua)
	if ua == "" || strings.ContainsAny(ua, "\r\n") {
		return "", false
	}
	return ua, true
}

// profileBeaconPools returns the per-beacon rotation pools from the active
// profile: request URIs and User-Agent strings. Empty pools disable that
// rotation axis (agent keeps its fixed value). Entries are sanitized so a
// hostile profile file cannot steer beacons at exfiltration targets — URIs
// stay path-only on the already-connected C2 host.
func (s *Server) profileBeaconPools() (uris []string, userAgents []string) {
	s.configMu.RLock()
	profileName := s.cfg.Malleable.ProfileName
	s.configMu.RUnlock()
	if profileName == "" {
		return nil, nil
	}
	seenURI := make(map[string]bool)
	addURI := func(u string) {
		u, ok := filterPoolURI(u)
		if !ok {
			return
		}
		if !seenURI[u] {
			seenURI[u] = true
			uris = append(uris, u)
		}
	}
	seenUA := make(map[string]bool)
	addUA := func(ua string) {
		ua, ok := filterPoolUA(ua)
		if !ok {
			return
		}
		if !seenUA[ua] {
			seenUA[ua] = true
			userAgents = append(userAgents, ua)
		}
	}
	if profile, ok := malleable.PredefinedProfiles()[profileName]; ok {
		for _, u := range profile.HTTPPost.URI {
			addURI(u)
		}
		for _, u := range profile.HTTPGet.URI {
			addURI(u)
		}
		addUA(profile.HTTPPost.Headers["User-Agent"])
		addUA(profile.HTTPGet.Headers["User-Agent"])
		return uris, userAgents
	}
	if v2 := s.loadV2Profile(profileName); v2 != nil {
		for _, u := range v2.BeaconURIs {
			addURI(u)
		}
		for _, u := range v2.URIs {
			addURI(u)
		}
		if v2.BeaconURI != "" {
			addURI(v2.BeaconURI)
		}
		for _, ua := range v2.UserAgents {
			addUA(ua)
		}
		if v2.UserAgent != "" {
			addUA(v2.UserAgent)
		}
	}
	return uris, userAgents
}

// profileJitterPools returns the per-beacon jitter pools from the active
// profile: junk-query parameter names and decoy "Name: value" headers.
// Sources: preset JitterCfg.ParameterNames, v2 parameter_names /
// request_header_pool. Empty pools disable that axis.
func (s *Server) profileJitterPools() (params []string, headers []string) {
	s.configMu.RLock()
	profileName := s.cfg.Malleable.ProfileName
	s.configMu.RUnlock()
	if profileName == "" {
		return nil, nil
	}
	seenParam := make(map[string]bool)
	addParam := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || strings.ContainsAny(p, " \t\r\n=&;") {
			return
		}
		if !seenParam[p] {
			seenParam[p] = true
			params = append(params, p)
		}
	}
	seenHeader := make(map[string]bool)
	addHeader := func(line string) {
		name, value, ok := splitPoolHeader(line)
		if !ok || !fieldSafePoolValue(value) {
			return
		}
		// Functional headers never rotate (framing/auth/routing); the agent
		// enforces the same skip list, this keeps the delivered pool clean.
		switch strings.ToLower(name) {
		case "content-type", "content-length", "host", "cookie", "user-agent",
			"authorization", "proxy-authorization":
			return
		}
		if !seenHeader[line] {
			seenHeader[line] = true
			headers = append(headers, name+": "+value)
		}
	}
	if profile, ok := malleable.PredefinedProfiles()[profileName]; ok {
		for _, p := range profile.Jitter.ParameterNames {
			addParam(p)
		}
		return params, headers
	}
	if v2 := s.loadV2Profile(profileName); v2 != nil {
		for _, p := range v2.ParameterNames {
			addParam(p)
		}
		for _, h := range v2.RequestHeaderPool {
			addHeader(h)
		}
	}
	return params, headers
}

// splitPoolHeader splits a "Name: value" pool line. Shared with the agent
// parser convention (same wire, newline-joined).
func splitPoolHeader(line string) (name, value string, ok bool) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}
	name, value = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
	if name == "" || value == "" {
		return "", "", false
	}
	return name, value, true
}

// fieldSafePoolValue mirrors the agent-safe value check: no CTLs that could
// split headers on the wire (validation already rejects these at load).
func fieldSafePoolValue(v string) bool {
	return !strings.ContainsAny(v, "\r\n")
}

// decoyServerPools maps profile families to plausible Server banners. A
// microsoft profile answering as nginx is worse camouflage than none.
var decoyServerPools = map[string][]string{
	"microsoft":  {"Microsoft-IIS/10.0", "Microsoft-IIS/8.5", "Microsoft-HTTPAPI/2.0"},
	"cloudflare": {"cloudflare"},
	"google":     {"gws", "Google-Analytics"},
	"akamai":     {"AkamaiGHost"},
	"":           {"nginx/1.24.0", "Apache/2.4.58", "Microsoft-IIS/10.0", "cloudflare"},
}

var decoyCacheControls = []string{"no-store", "private, max-age=0"}

// applyDecoyHeaders sets per-beacon randomized response headers: a family
// Server banner, an X-Request-Id, and a Cache-Control variant. Keys already
// present on the wire (explicit operator headers, written first) are left
// untouched. No profile active means no decoys (bare static behavior).
func (s *Server) applyDecoyHeaders(c *gin.Context, profileName string) {
	if profileName == "" {
		return
	}
	has := func(name string) bool {
		return c.Writer.Header().Get(name) != ""
	}
	lower := strings.ToLower(profileName)
	pool := decoyServerPools[""]
	for family, candidates := range decoyServerPools {
		if family != "" && strings.Contains(lower, family) {
			pool = candidates
			break
		}
	}
	if !has("Server") {
		c.Header("Server", pool[rand.Intn(len(pool))])
	}
	if !has("X-Request-Id") {
		c.Header("X-Request-Id", fmt.Sprintf("%016x", rand.Uint64()))
	}
	if !has("Cache-Control") {
		c.Header("Cache-Control", decoyCacheControls[rand.Intn(len(decoyCacheControls))])
	}
}

// beaconJitterSleep sleeps between attempts. Overridden in tests to avoid
// real-time waits.
var beaconJitterSleep = time.Sleep

// jitterBeaconResponse decorrelates reply timing with a random 0..N ms delay
// (N = malleable.resp_jitter_ms, 0 disables). Identical envelopes then differ
// in arrival timing beacon to beacon. Cheap: one timer per reply.
func (s *Server) jitterBeaconResponse() {
	maxMs := 0
	if s != nil && s.cfg != nil {
		s.configMu.RLock()
		maxMs = s.cfg.Malleable.RespJitterMs
		s.configMu.RUnlock()
	}
	if maxMs <= 0 {
		return
	}
	beaconJitterSleep(time.Duration(rand.Intn(maxMs+1)) * time.Millisecond)
}

// sanitizeProfileName strips a profile name to filesystem-safe characters
// (shared by every data/profiles/<name>.json lookup).
func sanitizeProfileName(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, strings.TrimSpace(name))
}

// loadV2Profile reads and migrates a v2/v1 profile file by name. Nil on any
// failure (missing file, bad JSON, bad chain) — callers treat nil as
// "no file profile" and fall back to presets or bare config.
func (s *Server) loadV2Profile(name string) *malleable.ProfileV2 {
	sanitized := sanitizeProfileName(name)
	if sanitized == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(s.profileDataDir(), "profiles", sanitized+".json"))
	if err != nil {
		// Missing file is the normal case for preset names: silent.
		return nil
	}
	v2, err := malleable.MigrateProfileJSON(raw, sanitized)
	if err != nil || v2 == nil {
		// Present-but-broken file: every beacon would hit this, so count
		// every failure but warn throttled — otherwise a bad deploy either
		// floods the log or fails silently fleet-wide.
		s.noteMalleableEvent("profile_load_fail", sanitized)
		return nil
	}
	return v2
}

// malleableWarnThrottle bounds failure warnings: a broken profile fails on
// every beacon, so unthrottled warns would flood the log.
var (
	malleableWarnMu sync.Mutex
	malleableWarnAt = map[string]time.Time{}
)

// noteMalleableEvent counts a malleable failure for observability and warns
// throttled (5 min per outcome+profile) so broken profiles are visible
// without log-flooding the beacon hot path.
func (s *Server) noteMalleableEvent(outcome, profile string) {
	if s.metrics != nil && s.metrics.MalleableEventsTotal != nil {
		s.metrics.MalleableEventsTotal.WithLabelValues(outcome, profile).Inc()
	}
	key := outcome + "\x00" + profile
	now := time.Now()
	malleableWarnMu.Lock()
	last, ok := malleableWarnAt[key]
	if !ok || now.Sub(last) >= 5*time.Minute {
		malleableWarnAt[key] = now
		ok = false
	}
	malleableWarnMu.Unlock()
	if !ok {
		slog.Warn("Malleable profile failure", "outcome", outcome, "profile", profile)
	}
}

// effectiveCoverTokens returns the prepend/append pair the server wraps
// around beacon bodies AND the agent strips on read. Single source for all
// four sites (HTTP wrap, raw wrap, registration push, build bake): the pair
// is atomic because the agent strips exactly one pair — mixing mp.Prepend
// with a v2 file's Append (or vice versa) bricks live agents.
//
// Precedence: explicit mp.Prepend/mp.Append win as a pair whenever either is
// set; otherwise the active v2 file's pair; named presets define no raw
// bytes (cover via transform chains the agent decodes on HTTP) so they
// resolve empty — raw transports under a preset stay bare by construction.
func (s *Server) effectiveCoverTokens() (prepend, appendStr string) {
	s.configMu.RLock()
	mpPre, mpApp, profileName := s.cfg.Malleable.Prepend, s.cfg.Malleable.Append, s.cfg.Malleable.ProfileName
	s.configMu.RUnlock()
	if mpPre != "" || mpApp != "" {
		return mpPre, mpApp
	}
	if v2 := s.loadV2Profile(profileName); v2 != nil {
		return v2.Prepend, v2.Append
	}
	return "", ""
}

// profileDataDir resolves the profiles directory without panicking on a
// partially-constructed Server (tests). It replaces the silent recover()
// guards previously wrapped around implantDataDir().
func (s *Server) profileDataDir() string {
	if s != nil && s.cfg != nil {
		if d := s.implantDataDir(); d != "" {
			return d
		}
	}
	return "data"
}

// stripMalleableRequest removes the request-side malleable prepend/append that
// the agent wraps around its OUTGOING beacon body (see wrapMalleableRequest on
// the agent). Mirrors stripMalleableWrapping but uses the request-side tokens
// from the server's malleable config. The operation is the inverse of the
// agent's wrap, so the enclosed JSON envelope is recovered unchanged. When no
// request-side transform is configured the body is returned untouched.
func (s *Server) stripMalleableRequest(raw []byte) []byte {
	s.configMu.RLock()
	prepend := s.cfg.Malleable.RequestPrepend
	appendStr := s.cfg.Malleable.RequestAppend
	s.configMu.RUnlock()

	switch {
	case prepend == "" && appendStr == "":
		return raw
	case prepend == "":
		return bytes.TrimSuffix(raw, []byte(appendStr))
	case appendStr == "":
		return bytes.TrimPrefix(raw, []byte(prepend))
	default:
		raw = bytes.TrimPrefix(raw, []byte(prepend))
		return bytes.TrimSuffix(raw, []byte(appendStr))
	}
}

// activePlacements returns the server-side placement list: global config
// Placements JSON first, else the active profile file's placements.
func (s *Server) activePlacements() []malleable.PlacementV2 {
	return s.activePlacementSet()
}

// extractPlacedBody tries each configured placement location and returns the
// decoded envelope bytes when one yields a plausible beacon frame. It returns
// nil when no placement is configured or none decodes, in which case the
// caller falls back to the request body. Cover-copy model: the placed value
// decodes (chain reverse) to the padded+wrapped body, so strip+unpad apply
// before the cheap '{' plausibility check — no decrypt attempted here.
func (s *Server) extractPlacedBody(c *gin.Context) []byte {
	pls := s.activePlacements()
	if len(pls) == 0 {
		return nil
	}
	tryDecode := func(encoded string, chain string) []byte {
		if strings.TrimSpace(encoded) == "" {
			return nil
		}
		var steps []malleable.Transform
		for _, st := range malleable.ParseWire(chain) {
			steps = append(steps, malleable.Transform{Type: st.Type, Value: st.Value})
		}
		decoded := []byte(encoded)
		if len(steps) > 0 {
			tb := &malleable.TransformBlock{Transforms: steps}
			out, err := tb.Apply(decoded, false)
			if err != nil {
				return nil
			}
			decoded = out
		}
		decoded = s.stripMalleableRequest(decoded)
		decoded = s.stripBodyPadding(decoded)
		trimmed := bytes.TrimSpace(decoded)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			return decoded
		}
		return nil
	}
	for _, pl := range pls {
		kind, name, ok := malleable.ParsePlacementTarget(pl.Target)
		if !ok {
			continue
		}
		switch kind {
		case "query":
			// Scan every query value: the param name may rotate per
			// beacon through the profile's parameter pool.
			for _, vals := range c.Request.URL.Query() {
				for _, v := range vals {
					if dec := tryDecode(v, pl.Chain); dec != nil {
						return dec
					}
				}
			}
			_ = name
		case "cookie":
			// Same scan-all policy as query for rotated names.
			for _, ck := range c.Request.Cookies() {
				if dec := tryDecode(ck.Value, pl.Chain); dec != nil {
					return dec
				}
			}
		case "header":
			if dec := tryDecode(c.GetHeader(name), pl.Chain); dec != nil {
				return dec
			}
		default:
			continue
		}
	}
	return nil
}

// stripBodyPadding removes the agent's ContentLengthJitter padding (8-byte
// big-endian length prefix + random trailing bytes, see padBeaconBody on the
// agent) from an HTTP/WS beacon body. Bodies without the prefix — plain
// envelopes and any future transport that does not pad — decode the prefix to
// an absurd length and are returned untouched, so the strip is safe to apply
// unconditionally on every inbound beacon.
func (s *Server) stripBodyPadding(raw []byte) []byte {
	const prefixLen = 8
	if len(raw) < prefixLen+16 {
		return raw
	}
	n := binary.BigEndian.Uint64(raw[:prefixLen])
	// Plausibility bounds: the enclosed envelope is at least 16 bytes and at
	// most the whole body minus the prefix. A raw JSON envelope starts with
	// '{' (0x7b) whose uint64 interpretation is ~8.8e18 — far outside these
	// bounds — so valid unpadded frames are never mis-stripped.
	if n < 16 || n > uint64(len(raw)-prefixLen) {
		return raw
	}
	return raw[prefixLen : prefixLen+int(n)]
}

// applyMalleableWrapping wraps a raw (non-HTTP) beacon response body with the
// configured malleable prepend/append bytes so raw TCP/SMB links get the same
// cover as the HTTP transport. Headers/status are intentionally omitted because
// a raw socket has no HTTP semantics; the agent strips the identical bytes on
// read via stripMalleableWrapping. The operation is a no-op when malleable is
// disabled or no prepend/append is configured, preserving backward-compatible
// framing for links that do not use a profile.
func (s *Server) applyMalleableWrapping(body []byte) []byte {
	s.configMu.RLock()
	enabled := s.cfg.Malleable.Enabled
	s.configMu.RUnlock()
	if !enabled {
		return body
	}
	// Agent-symmetric pair: raw agents strip exactly these bytes. Never v2 or
	// preset bytes directly — the agent never learns those strip tokens.
	prepend, appendStr := s.effectiveCoverTokens()
	wrapped := string(body)
	if prepend != "" {
		wrapped = prepend + wrapped
	}
	if appendStr != "" {
		wrapped = wrapped + appendStr
	}
	return []byte(wrapped)
}

// applyV2FileProfile loads a v2/v1 profile file by name and applies its
// ServerOutput chain. Returns (body, contentType, headers, ok).
func (s *Server) applyV2FileProfile(name string, body []byte) ([]byte, string, map[string]string, bool) {
	v2 := s.loadV2Profile(name)
	if v2 == nil || sanitizeProfileName(name) == "default" {
		return nil, "", nil, false
	}
	out := body
	if len(v2.ServerOutput) > 0 {
		tb := &malleable.TransformBlock{}
		for _, st := range v2.ServerOutput {
			tb.Transforms = append(tb.Transforms, malleable.Transform{Type: st.Type, Value: st.Value})
		}
		if enc, err := tb.Apply(body, true); err == nil {
			out = enc
		} else {
			// The agent cannot decode what was never encoded: count it and
			// fall back to the symmetric cover pair instead of a raw body.
			s.noteMalleableEvent("encode_fail", sanitizeProfileName(name))
			prepend, appendStr := s.effectiveCoverTokens()
			if prepend != "" {
				out = append([]byte(prepend), out...)
			}
			if appendStr != "" {
				out = append(out, []byte(appendStr)...)
			}
		}
	} else {
		// Agent-symmetric pair, never v2.Prepend/Append raw: the agent strips
		// exactly effectiveCoverTokens (see its doc). A v2 file whose own
		// pair differs only takes effect when mp.* are both empty — then the
		// helper returns the v2 pair and everything stays symmetric.
		prepend, appendStr := s.effectiveCoverTokens()
		if prepend != "" {
			out = append([]byte(prepend), out...)
		}
		if appendStr != "" {
			out = append(out, []byte(appendStr)...)
		}
	}
	headers := map[string]string{}
	for k, v := range v2.Headers {
		headers[k] = v
	}
	ct := headers["Content-Type"]
	if ct == "" {
		ct = "text/plain"
	}
	return out, ct, headers, true
}

// v2RespDecodeWire returns the agent wire form of a file profile's ServerOutput.
func (s *Server) v2RespDecodeWire(name string) string {
	v2 := s.loadV2Profile(name)
	if v2 == nil {
		return ""
	}
	return malleable.StepsToWire(v2.ServerOutput)
}

func (s *Server) applyProfilePreset(c *gin.Context, body []byte, profile *malleable.Profile) {
	// Apply output transforms
	if profile.HTTPPost.Output != nil {
		transformed, err := profile.HTTPPost.Output.Apply(body, true)
		if err == nil {
			body = transformed
		} else {
			s.noteMalleableEvent("encode_fail", profile.Name)
			prepend, appendStr := s.effectiveCoverTokens()
			if prepend != "" {
				body = append([]byte(prepend), body...)
			}
			if appendStr != "" {
				body = append(body, []byte(appendStr)...)
			}
		}
	}

	wrapped := string(body)

	for k, v := range profile.HTTPPost.Headers {
		c.Header(k, v)
	}

	ct := profile.HTTPPost.Headers["Content-Type"]
	if ct == "" {
		ct = "text/plain"
	}
	c.Header("Content-Type", ct)

	c.Status(http.StatusOK)
	c.Writer.WriteString(wrapped)
}
