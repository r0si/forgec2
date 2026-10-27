package malleable

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseWireCachedMatchesParseWire pins the only property that matters for a memo:
// the cached parser returns exactly what the plain one does, for strings that hit and
// for strings that miss.
func TestParseWireCachedMatchesParseWire(t *testing.T) {
	resetParsedWireCache()
	chains := []string{
		"",
		"   ",
		"base64",
		"base64;prepend:xyz",
		"prepend:xy;base64;append:z",
		"  base64 ;  append:tail  ",
		"unknown_step:value",
		";;;;",
		"strrep:a:b;xor:key",
	}
	for _, chain := range chains {
		want := ParseWire(chain)
		got := ParseWireCached(chain) // miss
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: first call = %+v, want %+v", chain, got, want)
		}
		again := ParseWireCached(chain) // hit
		if !reflect.DeepEqual(again, want) {
			t.Fatalf("%q: cached call = %+v, want %+v", chain, again, want)
		}
	}
}

// TestParseWireCachedIsBounded keeps the memo from becoming a memory sink fed by
// request data: past the cap it drops everything rather than growing without limit.
func TestParseWireCachedIsBounded(t *testing.T) {
	resetParsedWireCache()
	for i := 0; i < parsedWireCacheMax+50; i++ {
		ParseWireCached("step" + strings.Repeat("x", i%17) + ":" + string(rune('a'+i%26)))
	}
	if size := parsedWireCacheSize(); size > parsedWireCacheMax {
		t.Fatalf("cache grew to %d entries, cap is %d", size, parsedWireCacheMax)
	}
}

// BenchmarkParseWireCached shows what the beacon path saves per decode.
func BenchmarkParseWireCached(b *testing.B) {
	chain := "prepend:eyJ;base64;append:==;xor:0x5a"
	resetParsedWireCache()
	ParseWireCached(chain)

	b.Run("cached", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if len(ParseWireCached(chain)) == 0 {
				b.Fatal("no steps")
			}
		}
	})
	b.Run("uncached", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if len(ParseWire(chain)) == 0 {
				b.Fatal("no steps")
			}
		}
	})
}
