package obfuscation

import (
	"strings"
	"testing"
)

// TestPresetForLevelMapping pins the operator-facing levels. This function had no test
// at all, and it decides how much obfuscation a payload actually gets: a level that
// silently mapped to the weak default would ship plain ScriptBlock base64 while the
// operator believed they had asked for "heavy".
func TestPresetForLevelMapping(t *testing.T) {
	tests := []struct {
		level string
		want  PSObfuscateOptions
	}{
		{"", PSObfuscateOptions{}},
		{"none", PSObfuscateOptions{}},
		{"NONE", PSObfuscateOptions{}},
		{"  ", PSObfuscateOptions{}},
		{"light", PSObfuscateOptions{Launcher: "encodedcommand"}},
		{"low", PSObfuscateOptions{Launcher: "encodedcommand"}},
		{" Light ", PSObfuscateOptions{Launcher: "encodedcommand"}},
		{"standard", PSObfuscateOptions{Launcher: "xor", ScrubTriggers: true, JunkComments: true}},
		{"medium", PSObfuscateOptions{Launcher: "xor", ScrubTriggers: true, JunkComments: true}},
		{"med", PSObfuscateOptions{Launcher: "xor", ScrubTriggers: true, JunkComments: true}},
		{"heavy", PSObfuscateOptions{
			Launcher: "xor", ScrubTriggers: true, JunkComments: true,
			VarRename: true, StringSplit: true, CaseRandomize: true,
		}},
		{"high", PSObfuscateOptions{
			Launcher: "xor", ScrubTriggers: true, JunkComments: true,
			VarRename: true, StringSplit: true, CaseRandomize: true,
		}},
		{"HEAVY", PSObfuscateOptions{
			Launcher: "xor", ScrubTriggers: true, JunkComments: true,
			VarRename: true, StringSplit: true, CaseRandomize: true,
		}},
		// An unknown level must not silently enable the heavy pipeline either: the
		// documented behaviour is the legacy shape, so a typo is visible in the output.
		{"maximum", PSObfuscateOptions{}},
	}
	for _, tc := range tests {
		if got := PresetForLevel(tc.level); got != tc.want {
			t.Errorf("PresetForLevel(%q) = %+v, want %+v", tc.level, got, tc.want)
		}
	}
}

// TestPresetForLevelProducesThePromisedLauncher is the end-to-end half: it takes each
// level, generates a one-liner, and checks the OUTER SHAPE matches what the level
// promises - so "standard"/"heavy" cannot silently degrade to the legacy ScriptBlock
// launcher. The payload is also decoded back to confirm the pipeline is reversible,
// because a launcher that does not decode is a payload that does not run.
func TestPresetForLevelProducesThePromisedLauncher(t *testing.T) {
	const code = "$x = Get-Process; Write-Output $x.Count"

	cases := []struct {
		level        string
		wantContains string
		wantAbsent   string
	}{
		{"none", "[ScriptBlock]::Create", "-enc"},
		{"light", "-enc", "[ScriptBlock]::Create"},
		// The XOR launcher necessarily contains [ScriptBlock]::Create inside its decoder
		// stub; what distinguishes it is the -bxor stub over an XORed blob.
		{"standard", "-bxor $k", ""},
		{"heavy", "-bxor $k", ""},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			opts := PresetForLevel(tc.level)
			if tc.level == "standard" || tc.level == "heavy" {
				// Deterministic key so the decode helper can reproduce it.
				opts.Seed = 7
				opts.SeedSet = true
			}
			oneLiner := GenerateCommandLineOneLinerWithOptions(code, opts)
			if !strings.Contains(oneLiner, tc.wantContains) {
				t.Fatalf("%s one-liner missing %q: %s", tc.level, tc.wantContains, oneLiner)
			}
			if tc.wantAbsent != "" && strings.Contains(oneLiner, tc.wantAbsent) {
				t.Fatalf("%s one-liner unexpectedly contains %q: %s", tc.level, tc.wantAbsent, oneLiner)
			}
			if tc.level == "standard" || tc.level == "heavy" {
				decoded, err := DecodeOneLinerPayload(oneLiner)
				if err != nil {
					t.Fatalf("%s one-liner does not decode: %v", tc.level, err)
				}
				// Source transforms change the text, so compare on a stable token from
				// the original rather than the whole string.
				if !strings.Contains(decoded, "Get-Process") && !strings.Contains(strings.ToLower(decoded), "get-process") {
					t.Fatalf("%s decoded payload lost the command: %s", tc.level, decoded)
				}
			}
		})
	}
}
