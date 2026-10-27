package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forgec2/forgec2/internal/malleable"
)

// Bounds on the placed-body scan (extractPlacedBody). A real beacon sends one value of a
// few KiB; the caps exist so an unauthenticated request cannot buy unbounded decode work by
// carrying thousands of parameters.
const (
	maxPlacementCandidates   = 64
	maxPlacementValueBytes   = 32 << 10
	maxPlacementDecodeBudget = 256 << 10
	// maxPlacementQueryBytes caps the whole query string before it is parsed.
	maxPlacementQueryBytes = 64 << 10
)

// placementsStatInterval bounds how often the cache re-stats the profile file.
const placementsStatInterval = 200 * time.Millisecond

// placementsCacheKey identifies one resolved placement set. Both halves of the
// configuration participate, plus the profile file's modification time when the
// placements come from disk, so an edit by the console (which rewrites the file) or a
// config reload invalidates the entry without any explicit signalling.
type placementsCacheKey struct {
	raw      string
	name     string
	file     string
	modTime  time.Time
	fileSize int64
}

type placementsCache struct {
	mu    sync.Mutex
	key   placementsCacheKey
	pls   []malleable.PlacementV2
	empty bool
	// lastStat throttles the file stat that keeps the key fresh. Stat-ing the profile
	// on every check-in cost roughly 14 microseconds of syscall per beacon; 200ms of
	// staleness is invisible to an operator editing a profile and far below the time it
	// takes the console to rewrite the file.
	lastStat time.Time
	// hits/misses exist so the tests can prove the cache is used, not just present.
	hits   int
	misses int
}

// resolvePlacements does the real work: inline JSON first, then the profile file.
func (s *Server) resolvePlacements(raw, name string) []malleable.PlacementV2 {
	if strings.TrimSpace(raw) != "" {
		var pls []malleable.PlacementV2
		if err := json.Unmarshal([]byte(raw), &pls); err == nil && len(pls) > 0 {
			return pls
		}
	}
	if strings.TrimSpace(name) == "" {
		return nil
	}
	dir := s.profileDataDir()
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, strings.TrimSpace(name))
	if sanitized == "" {
		return nil
	}
	rawBytes, err := os.ReadFile(filepath.Join(dir, "profiles", sanitized+".json"))
	if err != nil {
		return nil
	}
	v2, err := malleable.MigrateProfileJSON(rawBytes, sanitized)
	if err != nil || v2 == nil {
		return nil
	}
	return v2.Placements
}

// activePlacementSet is activePlacements with the per-request resolution cached.
//
// The beacon path reads this on every check-in when a profile placement is
// configured, and the uncached version opened and parsed the profile JSON (or
// unmarshalled the inline copy) per request. The key includes the file's mtime and
// size, so a profile edited through the console - which rewrites that file - is picked
// up immediately, and a reload that changes malleable.profile_name changes the key.
func (s *Server) activePlacementSet() []malleable.PlacementV2 {
	s.configMu.RLock()
	raw := s.cfg.Malleable.Placements
	name := s.cfg.Malleable.ProfileName
	s.configMu.RUnlock()

	key := placementsCacheKey{raw: raw, name: name}
	c := &s.placementsCache

	// Reuse the previous file metadata while the stat is still fresh: the syscall is the
	// expensive part of a cache hit, and nothing can write this file inside the window
	// that an operator would notice.
	c.mu.Lock()
	reuse := time.Since(c.lastStat) < placementsStatInterval &&
		c.key.raw == raw && c.key.name == name && c.key.file != ""
	cachedKey := c.key
	c.mu.Unlock()

	if reuse {
		key = cachedKey
	} else if strings.TrimSpace(raw) == "" && strings.TrimSpace(name) != "" && s != nil {
		sanitized := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
				return r
			}
			return '_'
		}, strings.TrimSpace(name))
		if sanitized != "" {
			path := filepath.Join(s.profileDataDir(), "profiles", sanitized+".json")
			key.file = path
			if info, err := os.Stat(path); err == nil {
				key.modTime = info.ModTime()
				key.fileSize = info.Size()
			}
		}
	}

	c.mu.Lock()
	if c.key == key && (c.pls != nil || c.empty) {
		pls := c.pls
		c.hits++
		c.mu.Unlock()
		return pls
	}
	c.mu.Unlock()

	pls := s.resolvePlacements(raw, name)

	c.mu.Lock()
	c.key = key
	c.pls = pls
	c.empty = pls == nil
	c.lastStat = time.Now()
	c.misses++
	c.mu.Unlock()
	return pls
}
