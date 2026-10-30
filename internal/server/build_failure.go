package server

import (
	"errors"
	"regexp"
	"strings"
)

// buildFailureText is how a build failure reaches the operator.
//
// It exists because the obvious call - sanitizeError - is the wrong tool here. That helper REPLACES an unrecognised error with a
// fallback sentence, which is right for a database error that might leak schema internals and exactly wrong for a build: the
// toolchain's own output is the diagnosis, and replacing it produced a UI that said "Build failed" and nothing else.
//
// So this keeps the message, and applies the three things a build error actually needs:
//
//  1. **Secrets out.** ldflags carry the config blob and the signing key; a compiler error can echo a whole command line.
//  2. **A bounded size.** A linking failure can emit thousands of lines; the useful part is at the start and the end.
//  3. **An actionable hint** for the two failures that are about the environment rather than the code.
const (
	buildFailureMaxChars = 4000
	buildFailureHeadKeep = 2400
	buildFailureTailKeep = 1200
)

// ldflagSecretPattern redacts `-X <pkg.Var>=<value>` and any explicit key material, whether or not we know the flag list here.
//
// The build helpers scrub with the exact ldflags string; this one is the belt for the braces, because a failure can also come from a
// path where the flags were assembled locally.
var ldflagSecretPattern = regexp.MustCompile(`(-X\s+[A-Za-z0-9_./]+\.[A-Za-z0-9_]+\s*=\s*)\S+`)

// buildKeyPattern catches a hex or base64 key that appears bare in a message.
// The prefix is captured and the VALUE is what gets replaced: an earlier version substituted the whole match plus a marker, which
// kept the secret and merely decorated it.
var buildKeyPattern = regexp.MustCompile(`(?i)\b((?:beacon[_-]?key|signing[_-]?key|secret)\s*[=:]\s*)\S+`)

// buildFailureText renders err for the operator, with secrets removed, a size bound and a hint where it helps.
func buildFailureText(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "Build failed for an unknown reason"
	}
	message = buildKeyPattern.ReplaceAllString(message, "${1}[redacted]")
	message = ldflagSecretPattern.ReplaceAllString(message, "${1}[redacted]")
	message = truncateBuildFailure(message)

	// The two environment failures whose raw text does not say what to DO. Matched on the message rather than on an exit code because
	// both surface as an ordinary build error with the reason buried in the first line.
	lowered := strings.ToLower(message)
	switch {
	case strings.Contains(lowered, "executable file not found") || strings.Contains(lowered, "exec: \"go\"") ||
		strings.Contains(lowered, "cannot find go") || strings.Contains(lowered, "garble: executable file not found"):
		return "The build toolchain is not available to the server process: " + message +
			"\n(hint: the server needs `go` on its PATH; a service account often has a shorter PATH than your shell)"
	case strings.Contains(lowered, "permission denied") && strings.Contains(lowered, "go-build"):
		return "The build cache is not writable for the server process: " + message +
			"\n(hint: set a writable GOCACHE for the server's account, or run it as the operator who owns the cache)"
	case strings.Contains(lowered, "no space left on device"):
		return "The disk ran out of space while building: " + message
	case strings.Contains(lowered, "module lookup") || strings.Contains(lowered, "module proxy") ||
		strings.Contains(lowered, "network error") || strings.Contains(lowered, "unreachable") ||
		strings.Contains(lowered, "timeout") && strings.Contains(lowered, "proxy"):
		return "The module proxy is unreachable while building: " + message +
			"\n(hint: check GOPROXY or set implant.goproxy in config.yaml; the server may not reach proxy.golang.org)"
	case strings.Contains(lowered, "missing go.sum entry") || strings.Contains(lowered, "go.sum") &&
		strings.Contains(lowered, "missing"):
		return "The Go module sum file is incomplete while building: " + message +
			"\n(hint: run `go mod tidy` in the forgec2 root to regenerate go.sum)"
	}
	return message
}

// truncateBuildFailure keeps the head and the tail of a long failure.
//
// Both ends matter: the toolchain puts "why it started failing" first and the summarising `exit status`/undefined symbol last, and a
// single head truncation loses the part a reader scans for.
func truncateBuildFailure(message string) string {
	if len(message) <= buildFailureMaxChars {
		return message
	}
	head := message[:buildFailureHeadKeep]
	tail := message[len(message)-buildFailureTailKeep:]
	removed := len(message) - buildFailureHeadKeep - buildFailureTailKeep
	return head + "\n… (" + itoaInt(removed) + " characters omitted) …\n" + tail
}

// buildFailureIsToolchainMissing reports whether err is the "no compiler" case, for callers that want to react differently.
func buildFailureIsToolchainMissing(err error) bool {
	if err == nil {
		return false
	}
	lowered := strings.ToLower(err.Error())
	return strings.Contains(lowered, "executable file not found") ||
		strings.Contains(lowered, "exec: \"go\"") ||
		errors.Is(err, errToolchainMissing)
}
// errToolchainMissing exists so a caller can wrap the case explicitly instead of relying on message matching.
var errToolchainMissing = errors.New("build toolchain is not available")

// itoaInt is a local helper so this file does not pull strconv in for one call site that runs once per failed build.
func itoaInt(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		index--
		digits[index] = '-'
	}
	return string(digits[index:])
}

// BuildFailureHint is the machine-readable next step for a failed build.
//
// The error text is for a human; this is for the console and for any client that wants to offer an action ("install Go", "free space")
// without parsing English prose. The codes are a closed set so a caller can switch on them exhaustively, and every code carries the same
// sentence the text version appends, so the two cannot drift apart in meaning.
type BuildFailureHint struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// Hint codes. Stable identifiers: a client may key styling or a link on them.
const (
	HintToolchainMissing = "toolchain_missing"
	HintCacheUnwritable  = "cache_unwritable"
	HintDiskFull         = "disk_full"
	HintCompileError     = "compile_error"
	HintModuleProxy      = "module_proxy"
	HintGoSumMissing     = "go_sum_missing"
	HintUnknown          = "unknown"
)

// Bounded so a hint cannot become a second copy of the log.
const hintMaxChars = 200

// buildFailureHint classifies err into the next step an operator should take.
func buildFailureHint(err error) BuildFailureHint {
	if err == nil {
		return BuildFailureHint{Code: HintUnknown, Text: ""}
	}
	lowered := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lowered, "executable file not found") || strings.Contains(lowered, "exec: \"go\"") ||
		strings.Contains(lowered, "cannot find go"):
		return BuildFailureHint{
			Code: HintToolchainMissing,
			Text: "Install Go on the server host and make sure the server process's PATH includes it; check with GET /api/generate/toolchain.",
		}
	case strings.Contains(lowered, "go-build") && strings.Contains(lowered, "permission denied"):
		return BuildFailureHint{
			Code: HintCacheUnwritable,
			Text: "Point GOCACHE at a directory the server's account can write; check with GET /api/generate/toolchain.",
		}
	case strings.Contains(lowered, "no space left on device"):
		return BuildFailureHint{
			Code: HintDiskFull,
			Text: "Free space on the build volume (the binary plus the module cache can be hundreds of megabytes).",
		}
	case strings.Contains(lowered, "module lookup") || strings.Contains(lowered, "module proxy") ||
		strings.Contains(lowered, "network error") || strings.Contains(lowered, "unreachable"):
		return BuildFailureHint{
			Code: HintModuleProxy,
			Text: "The module proxy is unreachable while building; check GOPROXY or set implant.goproxy in config.yaml.",
		}
	case strings.Contains(lowered, "missing go.sum entry") || strings.Contains(lowered, "go.sum") &&
		strings.Contains(lowered, "missing"):
		return BuildFailureHint{
			Code: HintGoSumMissing,
			Text: "The Go module sum file is incomplete; run `go mod tidy` in the forgec2 root to regenerate go.sum.",
		}
	case strings.Contains(lowered, "undefined:") || strings.Contains(lowered, "syntax error") ||
		strings.Contains(lowered, "cannot use") || strings.Contains(lowered, "exit status 1"):
		// The toolchain ran and rejected the code: the compiler output in the message IS the next step.
		return BuildFailureHint{
			Code: HintCompileError,
			Text: "The compiler rejected the generated source - the message above names the file and line.",
		}
	}
	return BuildFailureHint{Code: HintUnknown, Text: ""}
}

// hintOrEmpty truncates a hint text, so the JSON stays small even if a code's text grows.
func hintOrEmpty(hint BuildFailureHint) BuildFailureHint {
	if len(hint.Text) > hintMaxChars {
		hint.Text = hint.Text[:hintMaxChars]
	}
	return hint
}
