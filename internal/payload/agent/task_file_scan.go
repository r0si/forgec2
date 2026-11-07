//go:build linux || windows || darwin
// +build linux windows darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// file_scan is the content-scanning half of file intelligence. It reads only
// bounded text files, applies an explicit detector allowlist, and returns raw
// matches in the task result. The teamserver encrypts task results at rest and
// redacts this result on every non-UI surface (AI, callbacks, exports).
//
// There is no OCR or document parsing: binary/container formats are skipped
// rather than interpreted. Detection is intentionally conservative and the
// operator must opt into every detector beyond email, phone and bank card.
const (
	fileScanDefaultMaxDepth      = 8
	fileScanHardMaxDepth         = 16
	fileScanDefaultMaxFiles      = 200
	fileScanHardMaxFiles         = 1000
	fileScanDefaultMaxFileBytes  = 1 * 1024 * 1024
	fileScanHardMaxFileBytes     = 8 * 1024 * 1024
	fileScanDefaultMaxTotalBytes = 32 * 1024 * 1024
	fileScanHardMaxTotalBytes    = 64 * 1024 * 1024
	fileScanDefaultMaxFindings   = 500
	fileScanHardMaxFindings      = 2000
	fileScanDefaultTimeout       = 30 * time.Second
	fileScanHardTimeout          = 60 * time.Second
	fileScanMaxResultBytes       = 900 * 1024
)

var fileScanRuleOrder = []string{
	"email", "phone", "bank_card", "ssn", "aws_key", "private_key", "jwt", "password",
}

// Phone is intentionally matched last: its broad shape would otherwise claim
// SSNs, card numbers and key material before the more specific detector runs.
var fileScanMatchOrder = []string{
	"email", "bank_card", "ssn", "aws_key", "private_key", "jwt", "password", "phone",
}

var fileScanDefaultRules = []string{"email", "phone", "bank_card"}

var fileScanRules = func() map[string]bool {
	out := make(map[string]bool, len(fileScanRuleOrder))
	for _, rule := range fileScanRuleOrder {
		out[rule] = true
	}
	return out
}()

// A nil regexp means the detector needs custom validation (bank card/phone).
var fileScanRegexes = map[string]*regexp.Regexp{
	"email":       regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
	"ssn":         regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
	"aws_key":     regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	"private_key": regexp.MustCompile(`-----BEGIN [A-Z0-9 ]{0,32}PRIVATE KEY-----`),
	"jwt":         regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
	"password":    regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|pass|secret|api[_-]?key|access[_-]?token|client[_-]?secret)\b\s*[:=]\s*(?:"([^"\r\n]{4,256})"|'([^'\r\n]{4,256})'|([^\s,;]{4,128}))`),
}

var (
	fileScanCardPattern  = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)
	fileScanPhonePattern = regexp.MustCompile(`\+?\d(?:[\s().-]{0,3}\d){6,14}`)
	fileScanDatePattern  = regexp.MustCompile(`^\d{1,4}[-/.]\d{1,2}[-/.]\d{1,4}$`)
	fileScanIPv4Pattern  = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}$`)
	fileScanSSNShape     = regexp.MustCompile(`^\d{3}-\d{2}-\d{4}$`)
)

// Known binary/container extensions are skipped before reading so a media or
// archive tree cannot consume the byte budget. Unknown files still get a NUL
// and control-byte check after the read.
var fileScanBinaryExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true,
	".webp": true, ".ico": true, ".tif": true, ".tiff": true, ".heic": true,
	".avif": true, ".mp3": true, ".wav": true, ".flac": true, ".aac": true,
	".ogg": true, ".m4a": true, ".wma": true, ".opus": true, ".mp4": true,
	".mkv": true, ".avi": true, ".mov": true, ".wmv": true, ".flv": true,
	".webm": true, ".m4v": true, ".mpg": true, ".mpeg": true, ".zip": true,
	".rar": true, ".7z": true, ".gz": true, ".bz2": true, ".xz": true,
	".tar": true, ".tgz": true, ".iso": true, ".cab": true, ".msi": true,
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".ppt": true, ".pptx": true, ".odt": true, ".ods": true, ".odp": true,
	".exe": true, ".dll": true, ".sys": true, ".bin": true, ".dat": true,
	".db": true, ".sqlite": true, ".sqlite3": true, ".mdb": true, ".p12": true,
	".pfx": true, ".der": true, ".jks": true, ".keystore": true, ".kdbx": true,
	".gpg": true, ".pgp": true, ".class": true, ".jar": true, ".war": true,
	".pyc": true, ".o": true, ".a": true, ".lib": true, ".so": true,
	".dylib": true, ".wasm": true,
}

type fileScanData struct {
	Patterns       []string `json:"patterns"`
	Pattern        string   `json:"pattern"`
	Rules          []string `json:"rules"`
	IncludeHidden  bool     `json:"include_hidden"`
	MaxDepth       int      `json:"max_depth"`
	MaxFiles       int      `json:"max_files"`
	MaxFileBytes   int64    `json:"max_file_bytes"`
	MaxTotalBytes  int64    `json:"max_total_bytes"`
	MaxFindings    int      `json:"max_findings"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxResultBytes int      `json:"max_result_bytes"`
}

type fileScanOpts struct {
	root           string
	patterns       []string
	rules          map[string]bool
	ruleList       []string
	includeHidden  bool
	maxDepth       int
	maxFiles       int
	maxFileBytes   int64
	maxTotalBytes  int64
	maxFindings    int
	timeout        time.Duration
	maxResultBytes int
}

type fileScanFinding struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Rule        string `json:"rule"`
	Value       string `json:"value"`
	Line        int    `json:"line"`
	Occurrences int    `json:"occurrences"`
}

type fileScanResult struct {
	Root            string            `json:"root"`
	Patterns        []string          `json:"patterns"`
	Rules           []string          `json:"rules"`
	FilesScanned    int               `json:"files_scanned"`
	FilesSkipped    int               `json:"files_skipped"`
	SkippedBinary   int               `json:"skipped_binary"`
	SkippedLarge    int               `json:"skipped_large"`
	BytesScanned    int64             `json:"bytes_scanned"`
	Findings        []fileScanFinding `json:"findings"`
	Counts          map[string]int    `json:"counts"`
	Errors          int               `json:"errors"`
	Truncated       bool              `json:"truncated"`
	TruncatedReason string            `json:"truncated_reason,omitempty"`
	ElapsedMS       int64             `json:"elapsed_ms"`
}

type fileScanSpan struct {
	start int
	end   int
}

func handleFileScan(task Task, res *TaskResult) {
	opts, err := parseFileScanOpts(task.Path, task.Command, task.Data)
	if err != nil {
		res.Error = err.Error()
		return
	}
	out, err := runFileScan(opts)
	if err != nil {
		res.Error = err.Error()
		return
	}
	res.Output = out
}

func parseFileScanOpts(path, command, data string) (fileScanOpts, error) {
	opts := fileScanOpts{
		root:           strings.TrimSpace(path),
		patterns:       splitFileSearchPatterns(command),
		includeHidden:  false,
		maxDepth:       fileScanDefaultMaxDepth,
		maxFiles:       fileScanDefaultMaxFiles,
		maxFileBytes:   fileScanDefaultMaxFileBytes,
		maxTotalBytes:  fileScanDefaultMaxTotalBytes,
		maxFindings:    fileScanDefaultMaxFindings,
		timeout:        fileScanDefaultTimeout,
		maxResultBytes: fileScanMaxResultBytes,
	}
	if opts.root == "" {
		opts.root = huntDefaultRoot()
	}
	if len(opts.patterns) == 0 {
		opts.patterns = []string{"*"}
	}

	raw := strings.TrimSpace(data)
	if raw != "" {
		if !strings.HasPrefix(raw, "{") {
			return opts, fmt.Errorf("file_scan: invalid options: expected a JSON object")
		}
		var d fileScanData
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return opts, fmt.Errorf("file_scan: invalid options: %w", err)
		}
		if len(d.Patterns) > 0 {
			opts.patterns = normalizeFileSearchPatterns(d.Patterns)
		} else if strings.TrimSpace(d.Pattern) != "" {
			opts.patterns = splitFileSearchPatterns(d.Pattern)
		}
		if len(d.Rules) > 0 {
			rules, err := normalizeFileScanRules(d.Rules)
			if err != nil {
				return opts, err
			}
			opts.rules = rules
		}
		opts.includeHidden = d.IncludeHidden
		if d.MaxDepth > 0 {
			opts.maxDepth = d.MaxDepth
		}
		if d.MaxFiles > 0 {
			opts.maxFiles = d.MaxFiles
		}
		if d.MaxFileBytes > 0 {
			opts.maxFileBytes = d.MaxFileBytes
		}
		if d.MaxTotalBytes > 0 {
			opts.maxTotalBytes = d.MaxTotalBytes
		}
		if d.MaxFindings > 0 {
			opts.maxFindings = d.MaxFindings
		}
		if d.TimeoutSeconds > 0 {
			opts.timeout = time.Duration(d.TimeoutSeconds) * time.Second
		}
		if d.MaxResultBytes > 0 {
			opts.maxResultBytes = d.MaxResultBytes
		}
	}

	if opts.rules == nil {
		rules, _ := normalizeFileScanRules(fileScanDefaultRules)
		opts.rules = rules
	}
	opts.ruleList = sortedFileScanRules(opts.rules)
	if opts.maxDepth > fileScanHardMaxDepth {
		opts.maxDepth = fileScanHardMaxDepth
	}
	if opts.maxFiles > fileScanHardMaxFiles {
		opts.maxFiles = fileScanHardMaxFiles
	}
	if opts.maxFileBytes > fileScanHardMaxFileBytes {
		opts.maxFileBytes = fileScanHardMaxFileBytes
	}
	if opts.maxTotalBytes > fileScanHardMaxTotalBytes {
		opts.maxTotalBytes = fileScanHardMaxTotalBytes
	}
	if opts.maxTotalBytes < opts.maxFileBytes {
		opts.maxTotalBytes = opts.maxFileBytes
	}
	if opts.maxFindings > fileScanHardMaxFindings {
		opts.maxFindings = fileScanHardMaxFindings
	}
	if opts.timeout > fileScanHardTimeout {
		opts.timeout = fileScanHardTimeout
	}
	if opts.maxResultBytes > fileScanMaxResultBytes {
		opts.maxResultBytes = fileScanMaxResultBytes
	}
	if opts.maxResultBytes < 1024 {
		opts.maxResultBytes = 1024
	}
	return opts, nil
}

func normalizeFileScanRules(in []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, raw := range in {
		for _, rule := range strings.Split(raw, ",") {
			rule = strings.ToLower(strings.TrimSpace(rule))
			if rule == "" {
				continue
			}
			if rule == "all" {
				for _, name := range fileScanRuleOrder {
					out[name] = true
				}
				continue
			}
			if !fileScanRules[rule] {
				return nil, fmt.Errorf("file_scan: unknown rule %q", rule)
			}
			out[rule] = true
		}
	}
	if len(out) == 0 {
		for _, rule := range fileScanDefaultRules {
			out[rule] = true
		}
	}
	return out, nil
}

func sortedFileScanRules(rules map[string]bool) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range fileScanRuleOrder {
		if rules[rule] {
			out = append(out, rule)
		}
	}
	return out
}

func runFileScan(opts fileScanOpts) (string, error) {
	root, err := filepath.Abs(opts.root)
	if err != nil {
		return "", fmt.Errorf("file_scan: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("file_scan: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("file_scan: %s is not a directory", root)
	}

	start := time.Now()
	deadline := start.Add(opts.timeout)
	ctx := currentExecCtx()
	result := fileScanResult{
		Root:     root,
		Patterns: append([]string{}, opts.patterns...),
		Rules:    append([]string{}, opts.ruleList...),
		Findings: []fileScanFinding{},
		Counts:   map[string]int{},
	}
	stopReason := ""

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if ctx != nil && ctx.Err() != nil {
			stopReason = "cancelled"
			return fs.SkipAll
		}
		if time.Now().After(deadline) {
			stopReason = "timeout"
			return fs.SkipAll
		}
		if walkErr != nil {
			result.Errors++
			if d != nil && d.IsDir() && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		if d == nil {
			return nil
		}
		if d.IsDir() {
			if p != root {
				if skipFileSearchDir(d.Name()) {
					return filepath.SkipDir
				}
				if !opts.includeHidden && fileSearchIsHidden(p, d.Name()) {
					return filepath.SkipDir
				}
				if huntDepth(root, p) >= opts.maxDepth {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		if !opts.includeHidden && fileSearchIsHidden(p, d.Name()) {
			return nil
		}
		if huntDepth(root, p) > opts.maxDepth {
			return nil
		}
		if !matchHuntName(d.Name(), opts.patterns) {
			return nil
		}
		if result.FilesScanned >= opts.maxFiles {
			stopReason = "max_files"
			return fs.SkipAll
		}
		fi, err := d.Info()
		if err != nil {
			result.Errors++
			return nil
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		if fileScanBinaryExts[strings.ToLower(filepath.Ext(d.Name()))] {
			result.FilesSkipped++
			result.SkippedBinary++
			return nil
		}
		if fi.Size() > opts.maxFileBytes {
			result.FilesSkipped++
			result.SkippedLarge++
			return nil
		}
		if result.BytesScanned+fi.Size() > opts.maxTotalBytes {
			stopReason = "max_total_bytes"
			return fs.SkipAll
		}

		// #nosec G122 -- the scan runs as the victim user over its own tree; the race window is its own files.
		data, err := os.ReadFile(p) // #nosec G122
		if err != nil {
			result.Errors++
			return nil
		}
		result.FilesScanned++
		result.BytesScanned += int64(len(data))

		text, ok := fileScanDecodeText(data)
		if !ok {
			result.FilesSkipped++
			result.SkippedBinary++
			return nil
		}
		scanFileScanText(&result, p, text, opts.rules, opts.maxFindings)
		if len(result.Findings) >= opts.maxFindings {
			stopReason = "max_findings"
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && walkErr != fs.SkipAll && !isFileScanStopError(walkErr) {
		result.Errors++
	}
	if stopReason != "" {
		result.Truncated = true
		result.TruncatedReason = stopReason
	}

	// Counts describe the findings actually returned after any byte-budget
	// trimming, so the UI summary never claims values it cannot show.
	var payload []byte
	for {
		result.Counts = map[string]int{}
		for _, finding := range result.Findings {
			result.Counts[finding.Rule]++
		}
		result.ElapsedMS = time.Since(start).Milliseconds()
		payload, err = json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("file_scan: encode result: %w", err)
		}
		if len(payload) <= opts.maxResultBytes || len(result.Findings) == 0 {
			break
		}
		result.Findings = result.Findings[:len(result.Findings)-1]
		result.Truncated = true
		result.TruncatedReason = "max_bytes"
	}
	return string(payload), nil
}

func isFileScanStopError(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}

func scanFileScanText(result *fileScanResult, path, text string, rules map[string]bool, maxFindings int) {
	lines := strings.Split(text, "\n")
	seen := map[string]int{}
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		lineNo := i + 1
		occupied := []fileScanSpan{}
		for _, rule := range fileScanMatchOrder {
			if !rules[rule] {
				continue
			}
			candidates := fileScanCandidates(rule, line)
			for _, candidate := range candidates {
				if candidate.value == "" {
					continue
				}
				if fileScanOverlaps(occupied, candidate.span) {
					continue
				}
				occupied = append(occupied, candidate.span)
				key := rule + "\x00" + fileScanDedupKey(rule, candidate.value)
				if idx, ok := seen[key]; ok {
					result.Findings[idx].Occurrences++
					continue
				}
				if len(result.Findings) >= maxFindings {
					return
				}
				finding := fileScanFinding{
					Path:        path,
					Name:        filepath.Base(path),
					Rule:        rule,
					Value:       candidate.value,
					Line:        lineNo,
					Occurrences: 1,
				}
				result.Findings = append(result.Findings, finding)
				seen[key] = len(result.Findings) - 1
			}
		}
	}
	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].Path != result.Findings[j].Path {
			return result.Findings[i].Path < result.Findings[j].Path
		}
		if result.Findings[i].Line != result.Findings[j].Line {
			return result.Findings[i].Line < result.Findings[j].Line
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})
}

type fileScanCandidate struct {
	value string
	span  fileScanSpan
}

func fileScanCandidates(rule, line string) []fileScanCandidate {
	if rule == "bank_card" {
		return fileScanCardCandidates(line)
	}
	if rule == "phone" {
		return fileScanPhoneCandidates(line)
	}
	re := fileScanRegexes[rule]
	if re == nil {
		return nil
	}
	matches := re.FindAllStringSubmatchIndex(line, -1)
	out := make([]fileScanCandidate, 0, len(matches))
	for _, m := range matches {
		start, end := m[0], m[1]
		value := line[start:end]
		if rule == "password" {
			groupStart, groupEnd := -1, -1
			for g := 2; g+1 < len(m); g += 2 {
				if m[g] >= 0 {
					groupStart, groupEnd = m[g], m[g+1]
					break
				}
			}
			if groupStart < 0 || !fileScanUsefulPassword(line[groupStart:groupEnd]) {
				continue
			}
			value = line[groupStart:groupEnd]
			start, end = groupStart, groupEnd
		}
		if rule == "email" && !fileScanValidEmail(value) {
			continue
		}
		if rule == "ssn" && !fileScanValidSSN(value) {
			continue
		}
		if rule == "jwt" && len(value) > 2048 {
			continue
		}
		if len(value) > 512 {
			value = value[:512]
			end = start + len(value)
		}
		out = append(out, fileScanCandidate{value: value, span: fileScanSpan{start: start, end: end}})
	}
	return out
}

func fileScanCardCandidates(line string) []fileScanCandidate {
	matches := fileScanCardPattern.FindAllStringIndex(line, -1)
	out := make([]fileScanCandidate, 0, len(matches))
	for _, m := range matches {
		raw := line[m[0]:m[1]]
		digits := fileScanDigits(raw)
		if len(digits) < 13 || len(digits) > 19 || !fileScanLuhn(digits) {
			continue
		}
		out = append(out, fileScanCandidate{value: raw, span: fileScanSpan{start: m[0], end: m[1]}})
	}
	return out
}

func fileScanPhoneCandidates(line string) []fileScanCandidate {
	matches := fileScanPhonePattern.FindAllStringIndex(line, -1)
	out := make([]fileScanCandidate, 0, len(matches))
	for _, m := range matches {
		start, end := m[0], m[1]
		if start > 0 && fileScanWordByte(line[start-1]) {
			continue
		}
		if end < len(line) && fileScanWordByte(line[end]) {
			continue
		}
		raw := line[start:end]
		digits := fileScanDigits(raw)
		if len(digits) < 7 || len(digits) > 15 {
			continue
		}
		if fileScanDatePattern.MatchString(raw) || fileScanIPv4Pattern.MatchString(raw) || fileScanSSNShape.MatchString(raw) {
			continue
		}
		if len(digits) >= 13 && !strings.HasPrefix(strings.TrimSpace(raw), "+") && !strings.ContainsAny(raw, "()") {
			continue
		}
		allSame := true
		for i := 1; i < len(digits); i++ {
			if digits[i] != digits[0] {
				allSame = false
				break
			}
		}
		if allSame {
			continue
		}
		out = append(out, fileScanCandidate{value: raw, span: fileScanSpan{start: start, end: end}})
	}
	return out
}

func fileScanWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func fileScanOverlaps(spans []fileScanSpan, span fileScanSpan) bool {
	for _, s := range spans {
		if span.start < s.end && s.start < span.end {
			return true
		}
	}
	return false
}

func fileScanDigits(raw string) string {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] >= '0' && raw[i] <= '9' {
			out = append(out, raw[i])
		}
	}
	return string(out)
}

func fileScanLuhn(digits string) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}

func fileScanValidEmail(value string) bool {
	if len(value) < 6 || len(value) > 320 {
		return false
	}
	at := strings.LastIndexByte(value, '@')
	if at <= 0 || at == len(value)-1 {
		return false
	}
	local, domain := value[:at], value[at+1:]
	if len(local) > 64 || len(domain) > 255 {
		return false
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return false
	}
	if strings.Contains(domain, "..") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	return true
}

func fileScanValidSSN(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) != 3 || len(parts[0]) != 3 || len(parts[1]) != 2 || len(parts[2]) != 4 {
		return false
	}
	area, _ := strconv.Atoi(parts[0])
	group, _ := strconv.Atoi(parts[1])
	serial, _ := strconv.Atoi(parts[2])
	if area == 0 || area == 666 || area >= 900 {
		return false
	}
	return group != 0 && serial != 0
}

func fileScanUsefulPassword(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 {
		return false
	}
	switch strings.ToLower(value) {
	case "null", "none", "true", "false", "nil", "undefined", "password", "secret", "token", "redacted", "changeme":
		return false
	}
	if strings.Contains(value, "${") || strings.HasPrefix(value, "%") || strings.HasPrefix(value, "<") || strings.HasPrefix(value, "your_") {
		return false
	}
	return true
}

func fileScanDedupKey(rule, value string) string {
	switch rule {
	case "email":
		return strings.ToLower(value)
	case "bank_card", "phone", "ssn":
		return fileScanDigits(value)
	default:
		return value
	}
}

func fileScanDecodeText(data []byte) (string, bool) {
	if len(data) >= 2 {
		if data[0] == 0xff && data[1] == 0xfe {
			return fileScanDecodeUTF16(data[2:], false)
		}
		if data[0] == 0xfe && data[1] == 0xff {
			return fileScanDecodeUTF16(data[2:], true)
		}
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", false
	}
	text := strings.ToValidUTF8(string(data), "\uFFFD")
	if !fileScanLooksLikeText(text) {
		return "", false
	}
	return text, true
}

func fileScanDecodeUTF16(data []byte, bigEndian bool) (string, bool) {
	if len(data)%2 != 0 {
		return "", false
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if bigEndian {
			units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
		} else {
			units = append(units, uint16(data[i+1])<<8|uint16(data[i]))
		}
	}
	text := string(utf16.Decode(units))
	if !fileScanLooksLikeText(text) {
		return "", false
	}
	return text, true
}

func fileScanLooksLikeText(text string) bool {
	if text == "" {
		return true
	}
	controls := 0
	total := 0
	for _, r := range text {
		total++
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r == utf8.RuneError || r < 0x20 || r == 0x7f {
			controls++
		}
	}
	return total == 0 || controls*20 <= total
}
