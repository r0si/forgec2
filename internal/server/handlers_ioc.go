package server

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forgec2/forgec2/internal/db"
	"github.com/forgec2/forgec2/pkg/protocol"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// IOC extraction: scan task results/commands plus credential and network-host
// records for observables (IPs, domains, URLs, hashes) so operators can hand
// a clean indicator list to the blue team or export it as STIX 2.1.

var (
	iocIPv4Re   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	iocDomainRe = regexp.MustCompile(`\b(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[A-Za-z]{2,24}\b`)
	iocURLRe    = regexp.MustCompile(`https?://[^\s"'<>()\\]+`)
	iocMD5Re    = regexp.MustCompile(`\b[a-fA-F0-9]{32}\b`)
	iocSHA1Re   = regexp.MustCompile(`\b[a-fA-F0-9]{40}\b`)
	iocSHA256Re = regexp.MustCompile(`\b[a-fA-F0-9]{64}\b`)
)

type iocEntry struct {
	Type      string   `json:"type"`
	Value     string   `json:"value"`
	Count     int      `json:"count"`
	FirstSeen string   `json:"first_seen"`
	LastSeen  string   `json:"last_seen"`
	Sources   []string `json:"sources"`
}

type iocAccumulator struct {
	items map[string]*iocEntry
	order []string
}

func (a *iocAccumulator) add(kind, value string, at time.Time, source string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	key := kind + "|" + strings.ToLower(value)
	e, ok := a.items[key]
	if !ok {
		e = &iocEntry{
			Type:      kind,
			Value:     value,
			FirstSeen: at.UTC().Format(time.RFC3339),
			LastSeen:  at.UTC().Format(time.RFC3339),
		}
		a.items[key] = e
		a.order = append(a.order, key)
	}
	e.Count++
	if at.UTC().Format(time.RFC3339) < e.FirstSeen {
		e.FirstSeen = at.UTC().Format(time.RFC3339)
	}
	if at.UTC().Format(time.RFC3339) > e.LastSeen {
		e.LastSeen = at.UTC().Format(time.RFC3339)
	}
	found := false
	for _, s := range e.Sources {
		if s == source {
			found = true
			break
		}
	}
	if !found && len(e.Sources) < 5 {
		e.Sources = append(e.Sources, source)
	}
}

func isPublicIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	private := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "0.0.0.0/8"}
	for _, cidr := range private {
		if _, network, err := net.ParseCIDR(cidr); err == nil && network.Contains(ip) {
			return false
		}
	}
	return true
}

// iocExtractText runs every extractor over one text blob.
func iocExtractText(a *iocAccumulator, text string, at time.Time, includePrivate bool) {
	if text == "" {
		return
	}
	for _, u := range iocURLRe.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".,;:")
		a.add("url", u, at, "tasks")
	}
	for _, h := range iocSHA256Re.FindAllString(text, -1) {
		a.add("sha256", strings.ToLower(h), at, "tasks")
	}
	for _, h := range iocSHA1Re.FindAllString(text, -1) {
		a.add("sha1", strings.ToLower(h), at, "tasks")
	}
	for _, h := range iocMD5Re.FindAllString(text, -1) {
		a.add("md5", strings.ToLower(h), at, "tasks")
	}
	for _, ip := range iocIPv4Re.FindAllString(text, -1) {
		if !includePrivate && !isPublicIP(ip) {
			continue
		}
		a.add("ipv4", ip, at, "tasks")
	}
	for _, d := range iocDomainRe.FindAllString(text, -1) {
		lower := strings.ToLower(d)
		// Skip common non-indicator matches (version strings, filenames).
		for _, suffix := range []string{".png", ".jpg", ".exe", ".dll", ".bin", ".so", ".zip", ".tmp", ".log", ".dat", ".sys", ".local"} {
			if strings.HasSuffix(lower, suffix) {
				goto next
			}
		}
		if !includePrivate && (strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".corp")) {
			goto next
		}
		a.add("domain", lower, at, "tasks")
	next:
	}
}

func iocSortEntries(a *iocAccumulator) []iocEntry {
	out := make([]iocEntry, 0, len(a.items))
	for _, k := range a.order {
		out = append(out, *a.items[k])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// handleListIOCs returns extracted indicators for the time range.
// GET /api/ioc?days=30&type=url&include_private=false
func (s *Server) handleListIOCs(c *gin.Context) {
	days := atoiDefault(c.Query("days"), 30)
	if days < 1 || days > 365 {
		days = 30
	}
	typeFilter := c.Query("type")
	includePrivate := c.Query("include_private") == "true"

	entries, totalScanned, err := s.extractIOCs(days, includePrivate, s.reportAgentIDs(c))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to extract indicators")
		return
	}
	filtered := make([]iocEntry, 0, len(entries))
	for _, e := range entries {
		if typeFilter != "" && e.Type != typeFilter {
			continue
		}
		filtered = append(filtered, e)
	}

	respond(c, gin.H{
		"iocs":          filtered,
		"total":         len(filtered),
		"tasks_scanned": totalScanned,
	})
}

// extractIOCs scans task results/commands in the window and returns merged,
// count-ranked indicators.
func (s *Server) extractIOCs(days int, includePrivate bool, agentScope *gorm.DB) ([]iocEntry, int, error) {
	since := time.Now().AddDate(0, 0, -days)
	acc := &iocAccumulator{items: map[string]*iocEntry{}}

	// Dest is db.Task so Task.AfterFind decrypts Result/Error. The indicator
	// regexes below ran over FC2ENC: ciphertext before, which silently lost
	// every IOC that only appeared in command output. Type is selected as well
	// because the hook uses it to decide whether Command needs decrypting too.
	//
	// Read in chunks rather than truncating. Task.Result is capped at
	// MaxResultSize (1 MiB) per row, so the old single 20k-row read could
	// materialise ~20 GiB before a single regex ran. SQL-side SUBSTR is not an
	// option: the column is ciphertext and cutting it makes it undecryptable.
	// Chunking bounds the resident set while still scanning every byte, so no
	// indicator is lost to a size cut.
	var scanned int
	for offset := 0; offset < IOCScanTaskLimit; offset += IOCScanChunk {
		var rows []db.Task
		taskQuery := s.db.Table("tasks").
			Select("id, agent_id, type, command, result, created_at").
			Where("created_at >= ?", since).
			Order("created_at desc, id desc").
			Offset(offset).Limit(IOCScanChunk)
		if agentScope != nil {
			taskQuery = taskQuery.Where("agent_id IN (?)", agentScope)
		}
		if err := taskQuery.Find(&rows).Error; err != nil {
			return nil, 0, fmt.Errorf("scan task indicators: %w", err)
		}
		for _, r := range rows {
			// file_scan findings are operator-selected sensitive values, not
			// indicators. Feeding them here would copy raw matches into the
			// IOC/STIX export and AI query surfaces.
			if r.Type == protocol.TaskTypeFileScan {
				continue
			}
			at := r.CreatedAt
			iocExtractText(acc, r.Result, at, includePrivate)
			// Commands can contain target URLs/IPs (download_url, portscan…).
			if r.Command != "" && len(r.Command) < 2048 {
				iocExtractText(acc, r.Command, at, includePrivate)
			}
		}
		scanned += len(rows)
		if len(rows) < IOCScanChunk {
			break
		}
	}

	// Network hosts discovered by scanning/recon. Chunked for the same reason
	// as the task sweep, and so the whole table is never resident at once.
	for offset := 0; offset < IOCScanHostLimit; offset += IOCScanHostChunk {
		var hosts []struct {
			IP        string
			Hostname  string
			CreatedAt time.Time
		}
		hostQuery := s.db.Table("network_hosts").
			Select("ip, hostname, created_at").
			Order("created_at desc, id desc").
			Offset(offset).Limit(IOCScanHostChunk)
		if agentScope != nil {
			hostQuery = hostQuery.Where("agent_id IN (?)", agentScope)
		}
		if err := hostQuery.Scan(&hosts).Error; err != nil {
			return nil, 0, fmt.Errorf("scan network-host indicators: %w", err)
		}
		for _, h := range hosts {
			if includePrivate || isPublicIP(h.IP) {
				acc.add("ipv4", h.IP, h.CreatedAt, "network_hosts")
			}
			if h.Hostname != "" && strings.Contains(h.Hostname, ".") {
				acc.add("domain", strings.ToLower(h.Hostname), h.CreatedAt, "network_hosts")
			}
		}
		if len(hosts) < IOCScanHostChunk {
			break
		}
	}

	return iocSortEntries(acc), scanned, nil
}

// handleExportIOCs downloads the indicator list as STIX 2.1 or CSV.
// GET /api/ioc/export?format=stix2|csv&days=30
func (s *Server) handleExportIOCs(c *gin.Context) {
	format := c.DefaultQuery("format", "stix2")
	days := atoiDefault(c.Query("days"), 30)
	if days < 1 || days > 365 {
		days = 30
	}
	includePrivate := c.Query("include_private") == "true"

	entries, _, err := s.extractIOCs(days, includePrivate, s.reportAgentIDs(c))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "failed to extract indicators")
		return
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")

	switch format {
	case "csv":
		var b strings.Builder
		b.WriteString("type,value,count,first_seen,last_seen\n")
		for _, e := range entries {
			b.WriteString(fmt.Sprintf("%s,%s,%d,%s,%s\n",
				csvSafe(e.Type), csvSafe(e.Value), e.Count, e.FirstSeen, e.LastSeen))
		}
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="forgec2-iocs-%s.csv"`, stamp))
		c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(b.String()))
	default: // stix2
		bundle := buildStixBundle(entries, stamp)
		body, err := marshalJSONSafe(bundle)
		if !err {
			respondError(c, http.StatusInternalServerError, "failed to marshal bundle")
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="forgec2-iocs-%s.stix.json"`, stamp))
		c.Data(http.StatusOK, "application/stix+json; charset=utf-8", body)
	}
}

// buildStixBundle renders indicators as a minimal but valid STIX 2.1 bundle.
func buildStixBundle(entries []iocEntry, stamp string) map[string]interface{} {
	now := time.Now().UTC().Format(time.RFC3339)
	objects := []map[string]interface{}{
		{
			"type":            "marking-definition",
			"spec_version":    "2.1",
			"id":              "marking-definition--613f2e26-407d-48c7-9eca-b8e91df99dc9",
			"created":         "2017-01-20T00:00:00.000Z",
			"definition_type": "tlp",
			"definition":      map[string]string{"tlp": "amber"},
		},
	}
	idSeq := 0
	newID := func() string {
		idSeq++
		return fmt.Sprintf("indicator--%08x-7355-4a6e-b28a-fc2%010d", idSeq, idSeq)
	}
	for _, e := range entries {
		pattern := ""
		name := ""
		switch e.Type {
		case "ipv4":
			pattern = fmt.Sprintf("[ipv4-addr:value = '%s']", e.Value)
			name = "IP: " + e.Value
		case "domain":
			pattern = fmt.Sprintf("[domain-name:value = '%s']", e.Value)
			name = "Domain: " + e.Value
		case "url":
			pattern = fmt.Sprintf("[url:value = '%s']", e.Value)
			name = "URL: " + e.Value
		case "md5":
			pattern = fmt.Sprintf("[file:hashes.'MD5' = '%s']", e.Value)
			name = "File MD5: " + e.Value
		case "sha1":
			pattern = fmt.Sprintf("[file:hashes.'SHA-1' = '%s']", e.Value)
			name = "File SHA-1: " + e.Value
		case "sha256":
			pattern = fmt.Sprintf("[file:hashes.'SHA-256' = '%s']", e.Value)
			name = "File SHA-256: " + e.Value
		default:
			continue
		}
		objects = append(objects, map[string]interface{}{
			"type":                "indicator",
			"spec_version":        "2.1",
			"id":                  newID(),
			"created":             e.FirstSeen,
			"modified":            e.LastSeen,
			"name":                name,
			"description":         fmt.Sprintf("Extracted from ForgeC2 task data (%d occurrences)", e.Count),
			"pattern":             pattern,
			"valid_from":          e.FirstSeen,
			"labels":              []string{"malicious-activity"},
			"confidence":          confidenceForCount(e.Count),
			"object_marking_refs": []string{"marking-definition--613f2e26-407d-48c7-9eca-b8e91df99dc9"},
		})
	}
	return map[string]interface{}{
		"type":            "bundle",
		"id":              fmt.Sprintf("bundle--fc2a0000-0000-4000-8000-%s", strconv.FormatInt(time.Now().UnixNano()%1e12, 10)),
		"timestamp":       now,
		"x_forgec2_stamp": stamp,
		"objects":         objects,
	}
}

func confidenceForCount(n int) int {
	switch {
	case n >= 10:
		return 90
	case n >= 3:
		return 70
	default:
		return 50
	}
}
