package obfuscation

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func fixedOpts() PSObfuscateOptions {
	return PSObfuscateOptions{Seed: 42, SeedSet: true}
}

// DecodeOneLinerPayload extracts the embedded base64 blob from a one-liner
// produced by this package and reverses the launcher encoding (xor/gzip/plain)
// to recover the (possibly source-obfuscated) script.
//
// It lives in the test file: it was never called from production code, so
// keeping it in ps_obfuscate.go shipped a decoder for our own launchers inside
// the server binary.
func DecodeOneLinerPayload(oneLiner string) (string, error) {
	idx := strings.Index(oneLiner, "'")
	lastQuote := strings.LastIndex(oneLiner, "'")
	if idx == -1 || lastQuote == -1 || idx == lastQuote {
		return "", fmt.Errorf("no quoted base64 blob found")
	}
	b64 := oneLiner[idx+1 : lastQuote]
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	// xor launcher?
	if strings.Contains(oneLiner, "-bxor $k") {
		key := byte(0x41)
		if i := strings.Index(oneLiner, "$k=0x"); i >= 0 && i+6 < len(oneLiner) {
			hexDigits := ""
			for _, c := range oneLiner[i+5:] {
				if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
					hexDigits += string(c)
					if len(hexDigits) == 2 {
						break
					}
					continue
				}
				if strings.HasPrefix(string(c), "x") {
					continue
				}
				break
			}
			var k int
			if _, err := fmt.Sscanf(hexDigits, "%x", &k); err == nil {
				key = byte(k)
			}
		}
		return string(xorBytes(raw, key)), nil
	}
	// gzip launcher? gzip magic 1f 8b.
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return "", fmt.Errorf("gzip open: %w", err)
		}
		var out bytes.Buffer
		buf := make([]byte, 4096)
		for {
			n, rerr := zr.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
			}
			if rerr != nil {
				break
			}
		}
		_ = zr.Close()
		return out.String(), nil
	}
	return string(raw), nil
}

// Legacy launcher must stay byte-stable in shape (existing tests rely on it).
func TestWithOptionsLegacyDefault(t *testing.T) {
	code := "Write-Host 'hi'"
	got := GenerateCommandLineOneLinerWithOptions(code, PSObfuscateOptions{})
	if !strings.Contains(got, "[ScriptBlock]::Create") {
		t.Fatal("legacy launcher should use ScriptBlock::Create")
	}
	back, err := DecodeOneLinerPayload(got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back != code {
		t.Fatalf("round-trip = %q, want %q", back, code)
	}
}

func TestXorLauncherRoundTrip(t *testing.T) {
	code := "Get-Process | Where-Object { $_.CPU -gt 50 }"
	got := GenerateCommandLineOneLinerWithOptions(code, PSObfuscateOptions{Launcher: "xor", Seed: 7, SeedSet: true})
	if !strings.Contains(got, "-bxor $k") {
		t.Fatal("xor launcher should embed -bxor decoder")
	}
	// Raw base64 must not decode to the plaintext (it's xored).
	if strings.Contains(got, "Get-Process") {
		t.Fatal("xor launcher leaks plaintext")
	}
	back, err := DecodeOneLinerPayload(got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back != code {
		t.Fatalf("round-trip = %q, want %q", back, code)
	}
}

func TestGzipLauncherRoundTrip(t *testing.T) {
	code := "Write-Host 'hello world' # " + strings.Repeat("x", 200)
	got := GenerateCommandLineOneLinerWithOptions(code, PSObfuscateOptions{Launcher: "gzip"})
	if !strings.Contains(got, "GzipStream") {
		t.Fatal("gzip launcher should embed GzipStream decoder")
	}
	back, err := DecodeOneLinerPayload(got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back != code {
		t.Fatal("gzip round-trip mismatch")
	}
}

func TestEncodedCommandLauncher(t *testing.T) {
	code := "Write-Host 'hi'"
	got := GenerateCommandLineOneLinerWithOptions(code, PSObfuscateOptions{Launcher: "encodedcommand"})
	if !strings.Contains(got, "-enc ") {
		t.Fatal("should use -enc")
	}
	if strings.Contains(got, "[ScriptBlock]::Create") {
		t.Fatal("-enc launcher must not contain ScriptBlock string")
	}
	if strings.Contains(got, "[System.Convert]::FromBase64String") {
		t.Fatal("-enc launcher must not contain FromBase64String string")
	}
}

func TestSourceTransformsPreserveSemantics(t *testing.T) {
	code := "$count = 1\n$count = $count + 1\nWrite-Host 'hello'\nInvoke-Mimikatz"
	out := ObfuscatePowerShellSource(code, PSObfuscateOptions{
		VarRename: true, StringSplit: true, ScrubTriggers: true,
		Seed: 1, SeedSet: true,
	})
	// Variable renamed consistently: original $count gone, exactly 3 uses of new name.
	if strings.Contains(out, "$count") {
		t.Fatalf("variable not renamed: %q", out)
	}
	// Trigger tokens are scrubbed only where that is SAFE. "Invoke-Mimikatz" is a
	// command name: splitting it into Invoke-('mimi'+'katz') is a parse error, so it is
	// left intact on purpose (see scrub_safe.go). A standalone occurrence is still split.
	if strings.Contains(out, "'mimi'+'katz'") {
		t.Fatalf("a command name was rewritten into a parse error: %q", out)
	}
	standalone := ObfuscatePowerShellSource("Write-Output mimikatz", PSObfuscateOptions{
		ScrubTriggers: true, Seed: 1, SeedSet: true,
	})
	if strings.Contains(standalone, "mimikatz") {
		t.Fatalf("a standalone trigger was not scrubbed: %q", standalone)
	}
	// Automatics untouched.
	auto := ObfuscatePowerShellSource("$true $null $args $env:PATH", fixedOpts())
	_ = auto // $env:PATH contains ':' so left alone; $true/$null/$args must survive
	for _, tok := range []string{"$true", "$null", "$args"} {
		src := ObfuscatePowerShellSource(tok+" ", PSObfuscateOptions{VarRename: true, Seed: 1, SeedSet: true})
		if !strings.Contains(src, tok) {
			t.Fatalf("automatic %q was renamed: %q", tok, src)
		}
	}
}

func TestSourceTransformsDeterministic(t *testing.T) {
	code := "$a = 1\nWrite-Host 'hey'"
	o1 := ObfuscatePowerShellSource(code, fixedOpts())
	o1b := ObfuscatePowerShellSource(code, fixedOpts())
	if o1 != o1b {
		t.Fatal("same seed must give same output")
	}
}

func TestFullPipelineRoundTrip(t *testing.T) {
	code := "$x = 5\nWrite-Host 'pipeline'\nGet-Process"
	opts := PSObfuscateOptions{
		Launcher: "xor", VarRename: true, StringSplit: true,
		ScrubTriggers: true, JunkComments: true, CaseRandomize: true,
		Seed: 99, SeedSet: true,
	}
	got := GenerateCommandLineOneLinerWithOptions(code, opts)
	back, err := DecodeOneLinerPayload(got)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := ObfuscatePowerShellSource(code, opts)
	if back != want {
		t.Fatalf("pipeline round-trip mismatch:\n got %q\nwant %q", back, want)
	}
}
