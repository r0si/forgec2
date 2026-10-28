package server

import (
	"math"
	"testing"
	"time"
)

// TestDiscordRetryDelayIsBounded pins the bound on a server-controlled sleep.
//
// Discord's 429 carries retry_after, which the sender honoured verbatim with time.Sleep. A missing,
// zero, NaN or absurd value therefore either spun immediately or pinned the sender goroutine for as
// long as the number said - and because it was a plain sleep, a shutdown could not interrupt it
// either (the call site now waits on the stop channel instead).
func TestDiscordRetryDelayIsBounded(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want time.Duration
	}{
		{"missing", 0, discordRetryDefault},
		{"negative", -3, discordRetryDefault},
		{"NaN", math.NaN(), discordRetryDefault},
		{"+Inf", math.Inf(1), discordRetryDefault},
		{"short", 1.5, 1500 * time.Millisecond},
		{"long", 60, 60 * time.Second},
		{"absurd", 1e9, discordRetryMax},
	}
	for _, tc := range cases {
		if got := discordRetryDelay(tc.in); got != tc.want {
			t.Errorf("%s: discordRetryDelay(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}
