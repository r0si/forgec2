package server

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/forgec2/forgec2/internal/malleable"
	"github.com/forgec2/forgec2/internal/payload"
	"github.com/gin-gonic/gin"
)

func (s *Server) handleImportProfile(c *gin.Context) {
	file, err := c.FormFile("profile")
	if err != nil {
		respondError(c, http.StatusBadRequest, "profile file required")
		return
	}
	if file.Size > MaxUploadSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("profile file too large (max %d bytes)", MaxUploadSize))
		return
	}
	f, err := file.Open()
	if err != nil {
		respondError(c, http.StatusBadRequest, "failed to open profile file")
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		respondError(c, http.StatusBadRequest, "failed to read profile file")
		return
	}
	name := ""
	if looksLikeCS(string(raw)) {
		base := filepath.Base(file.Filename)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	profile, err := payload.SaveImportedProfileNamed(s.implantDataDir(), name, raw)
	if err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Profile import"))
		return
	}
	respond(c, gin.H{"profile": profile})
}

// profileListEntry is the list response used by the online editor. The
// compiled MalleableProfile fields are promoted so existing consumers that
// expect profile presets keep working unchanged.
type profileListEntry struct {
	payload.MalleableProfile
	Source  string `json:"source"`
	Builtin bool   `json:"builtin"`
}

func (s *Server) handleListProfiles(c *gin.Context) {
	presets := payload.ListProfilePresets(s.implantDataDir())
	out := make([]profileListEntry, 0, len(presets))
	for _, p := range presets {
		source, err := payload.ProfileSource(s.implantDataDir(), p.Name)
		if err != nil {
			source = ""
		}
		out = append(out, profileListEntry{
			MalleableProfile: p,
			Source:           source,
			Builtin:          payload.IsBuiltinProfile(p.Name),
		})
	}
	respond(c, gin.H{"profiles": out})
}

// handleSaveProfile persists a profile body (create or overwrite a custom
// profile in data/profiles/). JSON bodies keep the legacy behavior; CS text is
// saved by ?name=<profile> and stores both compiled JSON and .profile source.
func (s *Server) handleSaveProfile(c *gin.Context) {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, MaxUploadSize+1))
	if err != nil {
		respondError(c, http.StatusBadRequest, "failed to read request body")
		return
	}
	if len(raw) > MaxUploadSize {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("profile too large (max %d bytes)", MaxUploadSize))
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if looksLikeCS(string(raw)) {
		if name == "" {
			respondError(c, http.StatusBadRequest, "profile name is required")
			return
		}
		res := malleable.ParseCSProfile(name, strings.TrimSpace(string(raw)))
		if res.HasErrors() {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false, "error": strings.Join(res.ErrorMessages(), "; "),
				"diagnostics": res.Diagnostics,
			})
			return
		}
		profile, err := payload.SaveCSProfile(s.implantDataDir(), name, strings.TrimSpace(string(raw)))
		if err != nil {
			respondError(c, http.StatusBadRequest, sanitizeError(err, "Profile save"))
			return
		}
		respond(c, gin.H{"profile": profile, "diagnostics": res.Diagnostics, "warnings": res.WarningMessages()})
		return
	}
	profile, err := payload.SaveImportedProfileNamed(s.implantDataDir(), name, raw)
	if err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Profile save"))
		return
	}
	respond(c, gin.H{"profile": profile})
}

// handleImportProfileText accepts pasted profile content (JSON or CS text).
// Body: {name, format: auto|json|cs, content}.
func (s *Server) handleImportProfileText(c *gin.Context) {
	var req struct {
		Name    string `json:"name"`
		Format  string `json:"format"`
		Content string `json:"content"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxUploadSize)
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "name, format and content are required")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		respondError(c, http.StatusBadRequest, "content is required")
		return
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "auto"
	}
	name := strings.TrimSpace(req.Name)
	if format == "cs" || (format == "auto" && looksLikeCS(content)) {
		if name == "" {
			name = "imported"
		}
		res := malleable.ParseCSProfile(name, content)
		if res.HasErrors() {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false, "error": strings.Join(res.ErrorMessages(), "; "),
				"diagnostics": res.Diagnostics,
			})
			return
		}
		profile, err := payload.SaveCSProfile(s.implantDataDir(), name, content)
		if err != nil {
			respondError(c, http.StatusBadRequest, sanitizeError(err, "Profile import"))
			return
		}
		respond(c, gin.H{
			"profile": profile, "diagnostics": res.Diagnostics,
			"warnings": res.WarningMessages(), "ignored": res.Ignored,
		})
		return
	}
	profile, err := payload.SaveImportedProfileNamed(s.implantDataDir(), name, []byte(content))
	if err != nil {
		respondError(c, http.StatusBadRequest, sanitizeError(err, "Profile import"))
		return
	}
	respond(c, gin.H{"profile": profile})
}

func looksLikeCS(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return false
	}
	low := strings.ToLower(trimmed)
	return strings.Contains(low, "http-get") || strings.Contains(low, "http-post") ||
		strings.Contains(low, "http-config") || strings.Contains(low, "forgec2") ||
		strings.Contains(low, "set sleeptime") || strings.Contains(low, "set useragent") ||
		strings.Contains(low, "set jitter")
}

// handleValidateProfile dry-runs a profile without saving.
// Body: {name, format: auto|json|cs, content} -> {valid, profile(v2), wire, diagnostics, warnings}.
func (s *Server) handleValidateProfile(c *gin.Context) {
	var req struct {
		Name    string `json:"name"`
		Format  string `json:"format"`
		Content string `json:"content"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxUploadSize)
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "name, format and content are required")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		respondError(c, http.StatusBadRequest, "content is required")
		return
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "auto"
	}
	isCS := format == "cs" || (format == "auto" && looksLikeCS(content))
	var v2 *malleable.ProfileV2
	warnings := make([]string, 0)
	diagnostics := make([]malleable.Diagnostic, 0)
	ignored := make([]string, 0)
	valid := true
	if isCS {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = "imported"
		}
		res := malleable.ParseCSProfile(name, content)
		v2 = res.Profile
		diagnostics = append(diagnostics, res.Diagnostics...)
		warnings = append(warnings, res.WarningMessages()...)
		ignored = append(ignored, res.Ignored...)
		valid = !res.HasErrors()
		if v2 != nil && req.Name != "" {
			v2.Name = strings.TrimSpace(req.Name)
			if err := malleable.ValidateProfileV2(v2); err != nil {
				msg := err.Error()
				found := false
				for _, d := range diagnostics {
					if d.Message == msg {
						found = true
						break
					}
				}
				if !found {
					diagnostics = append(diagnostics, malleable.Diagnostic{
						Line: 1, Column: 1, Severity: "error", Code: "profile.invalid", Message: msg,
					})
				}
				valid = false
			}
		}
		// ParseCSProfile already ran validation, so valid reflects both syntax
		// and semantic errors. Do not turn a CS diagnostic into a 400: the
		// editor needs the partial profile to show a live compiled preview.
	} else {
		p, err := malleable.MigrateProfileJSON([]byte(content), strings.TrimSpace(req.Name))
		if err != nil {
			respondError(c, http.StatusBadRequest, sanitizeError(err, "Profile validation"))
			return
		}
		v2 = p
		if err := malleable.ValidateProfileV2(v2); err != nil {
			respondError(c, http.StatusBadRequest, sanitizeError(err, "Profile validation"))
			return
		}
	}
	if v2 == nil {
		respond(c, gin.H{
			"valid": false, "profile": nil, "wire": nil, "sample": nil,
			"diagnostics": diagnostics, "warnings": warnings, "compiled": nil,
		})
		return
	}
	// Round-trip samples through the ServerOutput chain. Two samples: a
	// lowercase one plus a mixed-case/base64-alphabet one shaped like a real
	// encrypted envelope. Lossy chains (notably "case", which uppercases on
	// encode and lowercases on decode) pass the first and corrupt the second.
	samples := []string{
		`{"type":"beacon","id":"abc"}`,
		`{"type":"beacon","id":"AbC123","data":"XyZ+/==9q8w7E"}`,
	}
	encoded := samples[0]
	if valid && len(v2.ServerOutput) > 0 {
		tb := &malleable.TransformBlock{}
		for _, st := range v2.ServerOutput {
			tb.Transforms = append(tb.Transforms, malleable.Transform{Type: st.Type, Value: st.Value})
		}
		if enc, err := tb.Apply([]byte(samples[0]), true); err == nil {
			encoded = string(enc)
			if dec, err := tb.Apply(enc, false); err != nil || string(dec) != samples[0] {
				warnings = append(warnings, "server_output chain does not round-trip")
			} else if enc2, err := tb.Apply([]byte(samples[1]), true); err != nil {
				warnings = append(warnings, "server_output encode failed on envelope-shaped input: "+err.Error())
			} else if dec2, err := tb.Apply(enc2, false); err != nil || string(dec2) != samples[1] {
				warnings = append(warnings, "server_output chain is lossy for mixed-case envelopes (e.g. contains 'case'): it validates on lowercase input but will corrupt real beacon responses")
			}
		} else {
			warnings = append(warnings, "server_output encode failed: "+err.Error())
		}
		// The C implant decodes no server_output transforms: any non-empty
		// chain bricks C beacons once the server encodes with it.
		warnings = append(warnings, "server_output is not decoded by the C implant: C beacons will fail against a server encoding responses with this chain — keep C implants on plain profiles")
	}
	// Round-trip each placement chain against both samples as well: a chain
	// that is lossy on envelope-shaped input must not silently ship.
	if valid {
		for _, pl := range v2.Placements {
			tb := &malleable.TransformBlock{}
			for _, st := range malleable.ParseWire(pl.Chain) {
				tb.Transforms = append(tb.Transforms, malleable.Transform{Type: st.Type, Value: st.Value})
			}
			if len(tb.Transforms) == 0 {
				continue
			}
			for _, s := range samples {
				enc, err := tb.Apply([]byte(s), true)
				if err != nil {
					warnings = append(warnings, "placement "+pl.Target+" encode failed: "+err.Error())
					break
				}
				if dec, err := tb.Apply(enc, false); err != nil || string(dec) != s {
					warnings = append(warnings, "placement "+pl.Target+" chain does not round-trip")
					break
				}
			}
		}
	}
	respond(c, gin.H{
		"valid":    valid,
		"profile":  v2,
		"compiled": compiledProfileSummary(v2, ignored),
		"wire": gin.H{
			"server_output":   malleable.StepsToWire(v2.ServerOutput),
			"client_metadata": malleable.StepsToWire(v2.ClientMetadata),
			"client_id":       malleable.StepsToWire(v2.ClientID),
		},
		"sample":      gin.H{"input": samples[0], "encoded": encoded},
		"diagnostics": diagnostics,
		"warnings":    warnings,
	})
}

func compiledProfileSummary(v2 *malleable.ProfileV2, ignored []string) gin.H {
	if v2 == nil {
		return nil
	}
	uris := v2.BeaconURIs
	if len(uris) == 0 {
		uris = v2.URIs
	}
	if len(uris) == 0 && v2.BeaconURI != "" {
		uris = []string{v2.BeaconURI}
	}
	return gin.H{
		"name":                  v2.Name,
		"method":                v2.PrimaryMethod(),
		"uris":                  uris,
		"sleep":                 v2.Sleep,
		"jitter":                v2.Jitter,
		"user_agent":            v2.UserAgent,
		"headers":               v2.Headers,
		"client_metadata":       malleable.StepsToWire(v2.ClientMetadata),
		"client_id":             malleable.StepsToWire(v2.ClientID),
		"server_output":         malleable.StepsToWire(v2.ServerOutput),
		"parameter":             v2.Parameter,
		"content_length_jitter": v2.ContentLengthJitter,
		"jitter_uri":            v2.JitterURI,
		"jitter_parameter":      v2.JitterParameter,
		"placements":            v2.Placements,
		"ignored":               ignored,
	}
}

func (s *Server) handleDeleteProfile(c *gin.Context) {
	name, _ := url.PathUnescape(c.Param("name"))
	if name == "" {
		respondError(c, http.StatusBadRequest, "profile name is required")
		return
	}
	if payload.IsBuiltinProfile(name) {
		respondError(c, http.StatusBadRequest, "cannot delete built-in profile")
		return
	}
	if err := payload.DeleteProfile(s.implantDataDir(), name); err != nil {
		respondError(c, http.StatusNotFound, sanitizeError(err, "Profile deletion"))
		return
	}
	respond(c, gin.H{})
}

func (s *Server) handleGeneratePage(c *gin.Context) {
	listeners := s.getListeners()
	presetsJSON, ok := marshalJSONSafe(payload.ListProfilePresets(s.implantDataDir()))
	if !ok {
		respondError(c, http.StatusInternalServerError, "failed to marshal presets")
		return
	}

	stats := s.getNavStats(c)
	s.configMu.RLock()
	defInt, defJitter, defUA, defSkipTLS := s.cfg.Implant.DefaultInterval, s.cfg.Implant.DefaultJitter, s.cfg.Implant.DefaultUA, s.cfg.Implant.DefaultSkipTLS
	s.configMu.RUnlock()
	data := gin.H{
		"Title":              "ForgeC2 - Generate Agent",
		"ActiveNav":          "generate",
		"DefaultInt":         defInt,
		"DefaultJitter":      defJitter,
		"DefaultUA":          defUA,
		"DefaultSkipTLS":     defSkipTLS,
		"Listeners":          listeners,
		"ProfilePresetsJSON": template.JS(strings.ReplaceAll(string(presetsJSON), "</script>", "<\\/script>")),
	}
	for k, v := range stats {
		data[k] = v
	}

	s.renderPageOrJSON(c, data)
}
