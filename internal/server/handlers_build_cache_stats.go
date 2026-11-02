package server

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// handleBuildCacheStats reports build cache statistics: entry count, total
// size, hit/miss counters, and hit rate. This is the observability surface for
// the build cache: an operator can see whether the cache is actually helping
// (high hit rate) or just consuming disk (many entries, zero hits).
// GET /api/builds/cache/stats
func (s *Server) handleBuildCacheStats(c *gin.Context) {
	if s.buildCache == nil {
		respond(c, gin.H{
			"enabled": false,
			"message": "build cache is not initialized",
		})
		return
	}

	stats := s.buildCache.Stats()
	respond(c, gin.H{
		"enabled":     true,
		"entries":     stats.Entries,
		"total_size":  stats.TotalSize,
		"max_size":    stats.MaxSize,
		"max_entries": stats.MaxEntries,
		"hit_count":   stats.HitCount,
		"miss_count":  stats.MissCount,
		"hit_rate":    stats.HitRate,
	})
}

// handleBuildCacheClear clears all entries from the build cache.
// POST /api/builds/cache/clear
func (s *Server) handleBuildCacheClear(c *gin.Context) {
	if s.buildCache == nil {
		respondError(c, http.StatusOK, "build cache is not initialized")
		return
	}
	if err := s.buildCache.Clear(); err != nil {
		slog.Error("Failed to clear build cache", "error", err)
		respondError(c, http.StatusInternalServerError, sanitizeError(err, "Clear cache"))
		return
	}
	respond(c, gin.H{"message": "cache cleared"})
}
