//go:build windows

package main

import (
	"bytes"
	"testing"
)

// TestGenerateNTLMChallengeIsRandom guards the fix that the NTLM server
// challenge is a per-session CSPRNG nonce. It previously shipped a FIXED
// constant ("pseudo-random for now"), so every relay session emitted the same
// 8-byte nonce -- predictable, and two concurrent relays collided so a response
// captured for one session could be tied to / replayed into another. Because
// the rest of the CHALLENGE_MESSAGE is built deterministically, two invocations
// differing proves the challenge bytes varied; equality to the old constant
// would be a silent regression back to the fixed value.
func TestGenerateNTLMChallengeIsRandom(t *testing.T) {
	const n = 32
	seen := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		c := generateNTLMChallenge()
		if len(c) == 0 {
			t.Fatal("generateNTLMChallenge returned empty")
		}
		for _, prev := range seen {
			if bytes.Equal(prev, c) {
				t.Fatalf("challenge repeated across calls (%d) -> the nonce is not session-random (regression to fixed value)", i)
			}
		}
		seen = append(seen, c)
	}
}
