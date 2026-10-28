package server

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// TestUnscopedAIToolsAreGlobalOnly keeps the AI tool surface from becoming a cross-tenant read.
//
// A tool body that queries tenant-bearing tables must either scope itself (a `Principal.TenantID`
// filter, `resolveAIAgentID`, `principalAgentIDs` or `tenantScope`) or be marked `GlobalOnly` in
// aiToolPolicies, which makes authorizeAITool refuse it for every non-zero tenant. Six bodies have no
// scoping marker today and all six are GlobalOnly - three of them (`create_automation_rule`,
// `save_engagement_note`, `generate_report`) WRITE or aggregate, so "it happens to be safe" is exactly
// the kind of property that should fail loudly when someone adds the seventh.
//
// `get_situation` is the one exception: its body hands reqCtx to a collector that scopes inside, which
// is why the marker set above is deliberately narrow rather than "mentions reqCtx".
func TestUnscopedAIToolsAreGlobalOnly(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("ai_tools_exec.go"))
	if err != nil {
		t.Fatalf("read ai_tools_exec.go: %v", err)
	}

	// Split the source into case blocks.
	caseStart := regexp.MustCompile(`(?m)^\tcase "([a-z_]+)":`)
	matches := caseStart.FindAllStringSubmatchIndex(string(source), -1)
	if len(matches) == 0 {
		t.Fatal("no tool cases found; the pattern no longer matches the switch")
	}

	scoping := regexp.MustCompile(`Principal\.TenantID|resolveAIAgentID|principalAgentIDs|tenantScope`)
	// Tools whose scoping lives in a helper they call. Each entry states where.
	scopedElsewhere := map[string]string{
		"get_situation": "collectAISituation(reqCtx) applies tenant scoping inside the collector",
	}

	var leaked []string
	for i, m := range matches {
		name := string(source[m[2]:m[3]])
		end := len(source)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		body := string(source[m[1]:end])
		if scoping.MatchString(body) {
			continue
		}
		if _, ok := scopedElsewhere[name]; ok {
			continue
		}
		policy, known := aiToolPolicies[name]
		if !known {
			// The parity test reports this; do not double-report here.
			continue
		}
		if !policy.GlobalOnly {
			leaked = append(leaked, name)
		}
	}
	sort.Strings(leaked)
	if len(leaked) > 0 {
		t.Fatalf("%d AI tool(s) neither scope by tenant nor are GlobalOnly, so a tenant operator can "+
			"reach another tenant's rows through them:\n  %v\n"+
			"Fix: filter by reqCtx.Principal.TenantID, use resolveAIAgentID/principalAgentIDs, or mark the "+
			"policy GlobalOnly and document where the scoping happens instead.",
			len(leaked), leaked)
	}
}
