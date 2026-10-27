package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// allowlisted suffixes for web_search. Plain string suffix check prevents
// bypass via subdomains; exact host validation after ssrfSafeClient redirect check.
var aiWebSearchAllowlist = []string{
	"wikipedia.org",
	"mitre.org",
	"attack.mitre.org",
	"github.com",
	"microsoft.com",
	"duckduckgo.com",
	"nvd.nist.gov",
	"cve.mitre.org",
	"lite.duckduckgo.com",
}

var tagRE = regexp.MustCompile(`<[^>]*>`)
var wsRE = regexp.MustCompile(`\s+`)

// aiToolErrorJSON renders a tool error payload with proper escaping. The old
// Sprintf form embedded err.Error() raw, so a message containing a quote (a URL,
// for example) produced invalid JSON for the tool-result parser.
func aiToolErrorJSON(msg string) string {
	b, err := json.Marshal(map[string]string{"error": msg})
	if err != nil {
		return `{"error":"internal error"}`
	}
	return string(b)
}

func aiWebSearchAllowedHost(host string) bool {
	h := strings.ToLower(host)
	for _, suf := range aiWebSearchAllowlist {
		if h == suf || strings.HasSuffix(h, "."+suf) {
			return true
		}
	}
	return false
}

func (s *Server) executeAIWebSearchTool(reqCtx *aiReqCtx, argsJSON string) string {
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &req); err != nil {
		return `{"error":"invalid arguments"}`
	}
	q := strings.TrimSpace(req.Query)
	if q == "" || len(q) > AIWebSearchMaxQueryLen {
		return `{"error":"query must be 1-200 chars"}`
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 3
	}
	if limit > AIWebSearchMaxResults {
		limit = AIWebSearchMaxResults
	}
	// Build DuckDuckGo lite search URL (proxied, still SSRF-checked)
	params := url.Values{}
	params.Set("q", q)
	searchURL := "https://lite.duckduckgo.com/lite/?" + params.Encode()
	if err := validateExternalURL(searchURL); err != nil {
		return aiToolErrorJSON("search URL blocked: " + truncateString(err.Error(), 200))
	}
	if !aiWebSearchAllowedHost("lite.duckduckgo.com") {
		return `{"error":"search host not allowlisted"}`
	}
	ctx, cancel := context.WithTimeout(context.Background(), AIWebSearchTimeout)
	defer cancel()
	client := ssrfSafeClient(&http.Client{Timeout: AIWebSearchTimeout, Transport: http.DefaultTransport})
	httpReq, _ := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	httpReq.Header.Set("User-Agent", "ForgeC2-AI/2.5")
	httpReq.Header.Set("Accept", "text/html")
	resp, err := client.Do(httpReq)
	if err != nil {
		return aiToolErrorJSON("web search failed: " + truncateString(err.Error(), 300))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Sprintf(`{"error":"search returned HTTP %d"}`, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return `{"error":"failed to read search results"}`
	}
	text := string(body)
	// Very lightweight parse: extract <a href> and snippet between <td> etc.
	// Fallback: strip tags and chunk.
	clean := tagRE.ReplaceAllString(text, " ")
	clean = wsRE.ReplaceAllString(clean, " ")
	clean = strings.TrimSpace(clean)
	if len(clean) > 8000 {
		clean = clean[:8000]
	}
	// Build results: single aggregated result with citation
	results := []map[string]string{
		{
			"title":    fmt.Sprintf("Search: %s", q),
			"url":      searchURL,
			"snippet":  clean,
			"citation": fmt.Sprintf("[web: %s]", searchURL),
		},
	}
	// Also try to enrich with allowed domain filter note
	payload := map[string]interface{}{
		"query":   q,
		"results": results[:1],
		"note":    "Results proxied via allowlisted lite.duckduckgo.com; cite as [web: url].",
	}
	b, _ := json.Marshal(payload)
	_ = limit // future: parse multiple result blocks if needed
	_ = time.Now()
	return string(b)
}
