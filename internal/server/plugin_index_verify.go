package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// Comparing an installation against an index document.
//
// The index endpoint (round 15) exports what is installed; this is the other half an operator needs and
// it needs no trust decision, because nothing is installed or fetched: an index document is uploaded
// and the server reports the DIFFERENCE. That answers the questions an index alone cannot:
//
//   - is this deployment still the set the team agreed on (missing / extra),
//   - did someone's package drift under the same name (digest changed),
//   - is the index even checkable (entries without a digest are reported as unverifiable, not as equal).
//
// Installing FROM an index stays unimplemented on purpose: "whose index, over what transport" is a trust
// question that must not be answered by a convenience endpoint.
//
// POST /api/plugins/index/verify   {"plugins":[{"name":"x","version":"1.0.0","digest":"sha256:..."}]}

const (
	// pluginIndexMaxBytes bounds the uploaded document. An index is a list of names and hashes; a
	// multi-megabyte body is a mistake or an attack, not a curated set.
	pluginIndexMaxBytes = 4 << 20
	// pluginIndexMaxEntries bounds how many entries are compared, so a hostile document cannot pin the
	// single-writer box with a million comparisons.
	pluginIndexMaxEntries = 2000
)

// pluginIndexVerifyRequest is the uploaded document.
type pluginIndexVerifyRequest struct {
	Plugins []pluginIndexEntry `json:"plugins"`
}

// pluginIndexDifference is one way the installation differs from the index.
type pluginIndexDifference struct {
	Name      string `json:"name"`
	Installed string `json:"installed,omitempty"`
	Expected  string `json:"expected,omitempty"`
	Reason    string `json:"reason"`
}

// handlePluginIndexVerify compares the uploaded index with what is installed.
func (s *Server) handlePluginIndexVerify(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, pluginIndexMaxBytes)

	var req pluginIndexVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid index document (expected {\"plugins\":[...]})")
		return
	}
	if len(req.Plugins) == 0 {
		respondError(c, http.StatusBadRequest, "the index is empty; upload the document produced by GET /api/plugins/index")
		return
	}
	if len(req.Plugins) > pluginIndexMaxEntries {
		respondError(c, http.StatusBadRequest, fmt.Sprintf("the index has too many entries (max %d)", pluginIndexMaxEntries))
		return
	}

	installed := s.pluginIndexByName(c)

	var missing, extra, changed, unverifiable []pluginIndexDifference
	claimed := make(map[string]bool, len(req.Plugins))

	for _, want := range req.Plugins {
		name := strings.TrimSpace(want.Name)
		if name == "" {
			unverifiable = append(unverifiable, pluginIndexDifference{Reason: "entry has no name"})
			continue
		}
		claimed[name] = true

		have, ok := installed[name]
		if !ok {
			missing = append(missing, pluginIndexDifference{Name: name, Expected: describeIndexEntry(want), Reason: "not installed"})
			continue
		}
		// A digest is the only field that proves the package is the same one; version can be edited and
		// is compared separately as a weaker signal.
		if strings.TrimSpace(want.Digest) == "" {
			unverifiable = append(unverifiable, pluginIndexDifference{Name: name, Installed: have.Digest, Reason: "the index entry carries no digest"})
			continue
		}
		if have.Digest != want.Digest {
			changed = append(changed, pluginIndexDifference{
				Name: name, Installed: have.Digest, Expected: want.Digest, Reason: "package digest differs",
			})
			continue
		}
		if strings.TrimSpace(want.Version) != "" && want.Version != have.Version {
			changed = append(changed, pluginIndexDifference{
				Name: name, Installed: have.Version, Expected: want.Version, Reason: "version differs (digest matches)",
			})
		}
	}

	for name, have := range installed {
		if claimed[name] {
			continue
		}
		extra = append(extra, pluginIndexDifference{Name: name, Installed: describeInstalledEntry(have), Reason: "installed but absent from the index"})
	}

	sortDifferences(missing)
	sortDifferences(extra)
	sortDifferences(changed)
	sortDifferences(unverifiable)

	// "In sync" must mean CHECKED and equal. Leaving unverifiable out of this expression reported
	// in_sync:true while listing the same entry as unverifiable - a false all-clear, which is worse
	// than no answer. Found by the test, not by reading the code.
	inSync := len(missing) == 0 && len(extra) == 0 && len(changed) == 0 && len(unverifiable) == 0
	// Reported as an audit record: a comparison that finds drift is a fact about the deployment, and
	// the trail should say who asked and what came back.
	details := fmt.Sprintf("plugin index verified: %d checked, %d missing, %d extra, %d changed, %d unverifiable",
		len(req.Plugins), len(missing), len(extra), len(changed), len(unverifiable))
	s.LogAuditRecord(c, "plugin_index_verify", "plugin", "", details, true, nil)

	respond(c, gin.H{
		"in_sync":      inSync,
		"checked":      len(req.Plugins),
		"missing":      differencesOrEmpty(missing),
		"extra":        differencesOrEmpty(extra),
		"changed":      differencesOrEmpty(changed),
		"unverifiable": differencesOrEmpty(unverifiable),
	})
}

// pluginIndexByName builds the installed set, reusing the index export so the two answers cannot
// diverge in how they compute a digest.
func (s *Server) pluginIndexByName(c *gin.Context) map[string]pluginIndexEntry {
	out := make(map[string]pluginIndexEntry, 32)
	for _, entry := range s.buildPluginIndex(c) {
		out[entry.Name] = entry
	}
	return out
}

func describeIndexEntry(entry pluginIndexEntry) string {
	parts := make([]string, 0, 2)
	if entry.Version != "" {
		parts = append(parts, "version "+entry.Version)
	}
	if entry.Digest != "" {
		parts = append(parts, entry.Digest)
	}
	if len(parts) == 0 {
		return "unspecified"
	}
	return strings.Join(parts, ", ")
}

func describeInstalledEntry(entry pluginIndexEntry) string {
	parts := make([]string, 0, 2)
	if entry.Version != "" {
		parts = append(parts, "version "+entry.Version)
	}
	if entry.Digest != "" {
		parts = append(parts, entry.Digest)
	}
	if len(parts) == 0 {
		return "unspecified"
	}
	return strings.Join(parts, ", ")
}

func sortDifferences(items []pluginIndexDifference) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
}

// differencesOrEmpty keeps the JSON shape stable: a nil slice marshals as null, and a client reading
// `missing.length` should not have to special-case it.
func differencesOrEmpty(items []pluginIndexDifference) []pluginIndexDifference {
	if items == nil {
		return []pluginIndexDifference{}
	}
	return items
}
