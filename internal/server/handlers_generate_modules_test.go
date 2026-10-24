package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/payload"
)

// moduleForm builds a minimal valid binary generation form (DNS path, no
// listener needed) with an optional agent module selection.
func moduleForm(modules string) url.Values {
	form := url.Values{
		"c2_url":     {"https://127.0.0.1:8443"},
		"protocol":   {"https"},
		"interval":   {"60"},
		"dns_domain": {"c2.example.com"},
	}
	if modules != "" {
		form.Set("modules", modules)
	}
	return form
}

// TestParseBinaryFormAgentModuleSelection pins the API boundary for the
// module selector: catalogue names parse into a trimmed list (so the payload
// layer sees exactly what the operator picked), unknown names are refused
// with 400 before any build work, and an absent field means "every module".
func TestParseBinaryFormAgentModuleSelection(t *testing.T) {
	s := mustTenantServer(t)

	c, w := tenantCtx(http.MethodPost, "/api/generate/exe", "modules-op", moduleForm("").Encode())
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form, ok := s.parseBinaryForm(c)
	if !ok {
		t.Fatalf("default form rejected: %s", w.Body.String())
	}
	if got := parseModuleSelection(form.Modules); len(got) != 0 {
		t.Fatalf("empty field must mean every module, got %v", got)
	}

	c, w = tenantCtx(http.MethodPost, "/api/generate/exe", "modules-op", moduleForm(" core , screen,screen ").Encode())
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	form, ok = s.parseBinaryForm(c)
	if !ok {
		t.Fatalf("valid selection rejected: %s", w.Body.String())
	}
	sel := parseModuleSelection(form.Modules)
	if len(sel) != 2 || sel[0] != "core" || sel[1] != "screen" {
		t.Fatalf("selection = %v, want [core screen]", sel)
	}
	// The selection must survive the payload-side normalization (unknowns,
	// dependencies, slim interplay) that gates the build.
	if _, err := payload.NormalizeAgentModules(sel, false); err != nil {
		t.Fatalf("selection rejected by NormalizeAgentModules: %v", err)
	}

	c, w = tenantCtx(http.MethodPost, "/api/generate/exe", "modules-op", moduleForm("screen,no_such_module").Encode())
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if parsed, ok := s.parseBinaryForm(c); ok || parsed != nil {
		t.Fatal("unknown module name was accepted")
	}
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "agent module") {
		t.Fatalf("unknown module: status=%d body=%s", w.Code, w.Body.String())
	}
}

// TestHandleAgentModulesListsCatalogue pins the read endpoint the Generate
// page uses to render the selector without hardcoding the catalogue.
func TestHandleAgentModulesListsCatalogue(t *testing.T) {
	s := mustTenantServer(t)
	c, w := tenantCtx(http.MethodGet, "/api/generate/modules", "modules-op", "")
	s.handleAgentModules(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Modules []struct {
			Name        string   `json:"name"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Tasks       []string `json:"tasks"`
		} `json:"modules"`
	}
	unmarshalData(t, w.Body.Bytes(), &body)
	if len(body.Modules) != len(payload.AgentModuleNames()) {
		t.Fatalf("module count = %d, want %d", len(body.Modules), len(payload.AgentModuleNames()))
	}
	names := map[string]bool{}
	for _, m := range body.Modules {
		if m.Name == "" || m.Title == "" || m.Description == "" {
			t.Fatalf("incomplete catalogue entry: %+v", m)
		}
		names[m.Name] = true
	}
	for _, want := range []string{"transports_advanced", "screen", "p2p"} {
		if !names[want] {
			t.Fatalf("catalogue missing %q", want)
		}
	}
}
