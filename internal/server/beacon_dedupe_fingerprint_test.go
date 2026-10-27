package server

import (
	"strings"
	"testing"
	"time"
)

// TestBeaconFingerprintMixesEveryField pins what the beacon dedupe key depends
// on. A field that is missing from the fingerprint means two genuinely different
// beacons collide and one is dropped — losing its results and its tasks.
func TestBeaconFingerprintMixesEveryField(t *testing.T) {
	base := beaconRequest{
		UUID: "agent-1", Seq: 10,
		Results: []taskResult{{
			Type: "shell", TaskID: 3, ResultID: "rid", MAC: "mac", Encoding: "base64",
			Output: "out", Error: "err", Filename: "f.txt", Size: 42, Offset: 7, Path: "/tmp/f",
		}},
		AckTaskIDs: []uint{5, 6},
	}
	baseFP := beaconFingerprint(base)
	if baseFP == "" || !strings.HasPrefix(baseFP, "agent-1:") {
		t.Fatalf("fingerprint = %q", baseFP)
	}
	if again := beaconFingerprint(base); again != baseFP {
		t.Fatal("fingerprint is not deterministic")
	}

	variants := map[string]beaconRequest{}
	mutate := func(name string, fn func(r *beaconRequest)) {
		r := beaconRequest{UUID: base.UUID, Seq: base.Seq, Results: append([]taskResult{}, base.Results...), AckTaskIDs: append([]uint{}, base.AckTaskIDs...)}
		fn(&r)
		variants[name] = r
	}
	mutate("seq", func(r *beaconRequest) { r.Seq = 11 })
	mutate("uuid", func(r *beaconRequest) { r.UUID = "agent-2" })
	mutate("result output", func(r *beaconRequest) { r.Results[0].Output = "different" })
	mutate("result error", func(r *beaconRequest) { r.Results[0].Error = "different" })
	mutate("result id", func(r *beaconRequest) { r.Results[0].ResultID = "other" })
	mutate("task id", func(r *beaconRequest) { r.Results[0].TaskID = 4 })
	mutate("mac", func(r *beaconRequest) { r.Results[0].MAC = "other" })
	mutate("encoding", func(r *beaconRequest) { r.Results[0].Encoding = "raw" })
	mutate("filename", func(r *beaconRequest) { r.Results[0].Filename = "g.txt" })
	mutate("size", func(r *beaconRequest) { r.Results[0].Size = 43 })
	mutate("offset", func(r *beaconRequest) { r.Results[0].Offset = 8 })
	mutate("path", func(r *beaconRequest) { r.Results[0].Path = "/tmp/g" })
	mutate("ack", func(r *beaconRequest) { r.AckTaskIDs = []uint{5} })
	mutate("socks", func(r *beaconRequest) {
		r.SocksData = []socksFrame{{ConnID: 1, Action: "open", Data: []byte("x")}}
	})
	mutate("relayed", func(r *beaconRequest) {
		r.Relayed = []relayedData{{AgentID: "child", Results: []taskResult{{Type: "shell", TaskID: 1, Output: "o"}}}}
	})

	for name, r := range variants {
		if fp := beaconFingerprint(r); fp == baseFP {
			t.Errorf("%s: changing the field did not change the fingerprint", name)
		}
	}

	// Long payloads are summarized by a 1 KiB prefix plus the length. Content
	// past the prefix is NOT hashed, which is safe only because the result MAC
	// is part of the fingerprint: two large results that differ in content carry
	// different MACs. Both halves of that contract are pinned here.
	longX := beaconRequest{UUID: "agent-1", Seq: 1, Results: []taskResult{{Type: "shell", TaskID: 1, ResultID: "r", MAC: "mac-a", Output: strings.Repeat("a", 2048) + "X"}}}
	longY := beaconRequest{UUID: "agent-1", Seq: 1, Results: []taskResult{{Type: "shell", TaskID: 1, ResultID: "r", MAC: "mac-a", Output: strings.Repeat("a", 2048) + "Y"}}}
	if beaconFingerprint(longX) != beaconFingerprint(longY) {
		t.Fatal("content past the 1 KiB prefix is hashed; the fingerprint no longer depends only on the documented fields")
	}
	// A different length is detected.
	longShort := beaconRequest{UUID: "agent-1", Seq: 1, Results: []taskResult{{Type: "shell", TaskID: 1, ResultID: "r", MAC: "mac-a", Output: strings.Repeat("a", 1024)}}}
	if beaconFingerprint(longX) == beaconFingerprint(longShort) {
		t.Error("a length difference was not detected")
	}
	// And a different MAC (the real content integrity signal) is detected.
	longY.Results[0].MAC = "mac-b"
	if beaconFingerprint(longX) == beaconFingerprint(longY) {
		t.Error("a MAC difference was not detected")
	}
}

// TestIsDuplicateBeacon covers the dedupe window: the same frame twice is a
// duplicate, a new seq is not, and an entry past the window is accepted again.
func TestIsDuplicateBeacon(t *testing.T) {
	s := mustTenantServer(t)
	req := beaconRequest{UUID: "dup-agent", Seq: 1, Results: []taskResult{{Type: "shell", TaskID: 1, Output: "o"}}}

	if s.isDuplicateBeacon(req) {
		t.Fatal("the first delivery was reported as a duplicate")
	}
	if !s.isDuplicateBeacon(req) {
		t.Fatal("the second delivery of the same frame was not deduplicated")
	}

	// A retransmission with a new seq is real work: its tasks must be served.
	next := req
	next.Seq = 2
	if s.isDuplicateBeacon(next) {
		t.Fatal("a new seq was treated as a duplicate")
	}

	// Different agents never collide.
	other := req
	other.UUID = "dup-agent-2"
	if s.isDuplicateBeacon(other) {
		t.Fatal("another agent's identical frame was deduplicated")
	}

	// Past the window the same frame is accepted again.
	fp := beaconFingerprint(req)
	s.beaconDedupMu.Lock()
	s.beaconDedupCache[fp] = time.Now().Add(-2 * BeaconDedupWindow)
	s.beaconDedupMu.Unlock()
	if s.isDuplicateBeacon(req) {
		t.Fatal("an entry past the dedupe window was still treated as a duplicate")
	}
}

// TestIsDuplicateBeaconWithoutCacheIsPassThrough covers the nil-cache guard: a
// server that never initialised the cache must process every beacon.
func TestIsDuplicateBeaconWithoutCacheIsPassThrough(t *testing.T) {
	s := &Server{}
	req := beaconRequest{UUID: "x", Seq: 1}
	if s.isDuplicateBeacon(req) {
		t.Fatal("a server without a dedupe cache dropped a beacon")
	}
}

// TestIsDuplicateBeaconCacheIsBounded proves the beacon cache cannot grow
// without limit even under an all-fresh flood.
func TestIsDuplicateBeaconCacheIsBounded(t *testing.T) {
	s := mustTenantServer(t)
	old := time.Now().Add(-2 * BeaconDedupWindow)
	s.beaconDedupMu.Lock()
	s.beaconDedupCache = make(map[string]time.Time, MaxBeaconDedupEntries+5)
	for i := 0; i < MaxBeaconDedupEntries+5; i++ {
		s.beaconDedupCache["k"+itoa(i)] = old
	}
	s.beaconDedupSweep = time.Now() // no sweep due
	s.beaconDedupMu.Unlock()

	s.isDuplicateBeacon(beaconRequest{UUID: "fresh", Seq: 1})

	s.beaconDedupMu.Lock()
	size := len(s.beaconDedupCache)
	s.beaconDedupMu.Unlock()
	if size > MaxBeaconDedupEntries+1 {
		t.Fatalf("beacon dedupe cache grew to %d, cap is %d", size, MaxBeaconDedupEntries)
	}
}
