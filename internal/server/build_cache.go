package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"log/slog"

	"github.com/forgec2/forgec2/internal/payload"
)

// BuildCacheEntry represents a cached build artifact.
type BuildCacheEntry struct {
	// Key is the hash of the build configuration
	Key string
	// OutputPath is the path to the cached artifact
	OutputPath string
	// CreatedAt is when the entry was created
	CreatedAt time.Time
	// LastAccessed is when the entry was last accessed
	LastAccessed time.Time
	// AccessCount is how many times this entry has been used
	AccessCount int64
	// Size is the size of the cached artifact in bytes
	Size int64
}

// BuildCache manages cached build artifacts.
// It uses a hash of the build configuration as the key, so that
// repeated builds with the same configuration can reuse the cached artifact.
type BuildCache struct {
	mu          sync.RWMutex
	entries     map[string]*BuildCacheEntry
	cacheDir    string
	maxSize     int64  // maximum total cache size in bytes
	maxEntries  int    // maximum number of entries
	currentSize int64  // current total size in bytes
	ttl         time.Duration // time-to-live for cache entries
}

// BuildCacheConfig holds configuration for the build cache.
type BuildCacheConfig struct {
	CacheDir    string
	MaxSize     int64  // maximum total cache size in bytes (default: 10GB)
	MaxEntries  int    // maximum number of entries (default: 1000)
	TTL         time.Duration // time-to-live for cache entries (default: 7 days)
}

// DefaultBuildCacheConfig returns the default build cache configuration.
func DefaultBuildCacheConfig() BuildCacheConfig {
	return BuildCacheConfig{
		CacheDir:   filepath.Join(os.TempDir(), "forgec2-build-cache"),
		MaxSize:    10 * 1024 * 1024 * 1024, // 10GB
		MaxEntries: 1000,
		TTL:        7 * 24 * time.Hour, // 7 days
	}
}

// NewBuildCache creates a new build cache with the given configuration.
func NewBuildCache(config BuildCacheConfig) (*BuildCache, error) {
	if config.CacheDir == "" {
		config = DefaultBuildCacheConfig()
	}
	if config.MaxSize <= 0 {
		config.MaxSize = DefaultBuildCacheConfig().MaxSize
	}
	if config.MaxEntries <= 0 {
		config.MaxEntries = DefaultBuildCacheConfig().MaxEntries
	}
	if config.TTL <= 0 {
		config.TTL = DefaultBuildCacheConfig().TTL
	}

	if err := os.MkdirAll(config.CacheDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	cache := &BuildCache{
		entries:  make(map[string]*BuildCacheEntry),
		cacheDir: config.CacheDir,
		maxSize:  config.MaxSize,
		maxEntries: config.MaxEntries,
		ttl:      config.TTL,
	}

	// Load existing cache entries from disk
	if err := cache.loadFromDisk(); err != nil {
		// Log error but continue with empty cache
		slog.Warn("Failed to load build cache from disk", "error", err)
	}

	// Start cleanup ticker
	go cache.cleanupLoop()

	return cache, nil
}

// GenerateCacheKey generates a cache key from the build configuration.
// The key is a hash of the build configuration that uniquely identifies
// the build output.
func GenerateCacheKey(cfg *payload.ImplantConfig, platform, format, goVersion, garbleVersion string) string {
	h := sha256.New()
	// Include all relevant configuration fields that affect the build output
	fmt.Fprintf(h, "platform=%s\n", platform)
	fmt.Fprintf(h, "format=%s\n", format)
	fmt.Fprintf(h, "goos=%s\n", runtime.GOOS)
	fmt.Fprintf(h, "goarch=%s\n", runtime.GOARCH)
	fmt.Fprintf(h, "go_version=%s\n", goVersion)
	fmt.Fprintf(h, "garble_version=%s\n", garbleVersion)
	
	// Include relevant config fields that affect the build output
	if cfg != nil {
		fmt.Fprintf(h, "c2_url=%s\n", cfg.C2URL)
		fmt.Fprintf(h, "protocol=%s\n", cfg.Protocol)
		fmt.Fprintf(h, "interval=%d\n", cfg.Interval)
		fmt.Fprintf(h, "jitter=%d\n", cfg.Jitter)
		fmt.Fprintf(h, "user_agent=%s\n", cfg.UserAgent)
		fmt.Fprintf(h, "persist=%v\n", cfg.Persist)
		fmt.Fprintf(h, "skip_tls_verify=%v\n", cfg.SkipTLSVerify)
		fmt.Fprintf(h, "filename=%s\n", cfg.Filename)
		fmt.Fprintf(h, "profile=%s\n", cfg.Profile)
		fmt.Fprintf(h, "listener_id=%d\n", cfg.ListenerID)
		fmt.Fprintf(h, "p2p_mode=%s\n", cfg.P2PMode)
		fmt.Fprintf(h, "p2p_parent=%s\n", cfg.P2PParent)
		fmt.Fprintf(h, "p2p_listen_addr=%s\n", cfg.P2PListenAddr)
		fmt.Fprintf(h, "dns_domain=%s\n", cfg.DNSDomain)
		fmt.Fprintf(h, "dns_server=%s\n", cfg.DNSServer)
		fmt.Fprintf(h, "proxy=%s\n", cfg.Proxy)
		fmt.Fprintf(h, "crypto_key=%s\n", cfg.CryptoKey)
		fmt.Fprintf(h, "beacon_key=%s\n", cfg.BeaconKey)
		fmt.Fprintf(h, "reg_secret_id=%s\n", cfg.RegSecretID)
		fmt.Fprintf(h, "reg_secret=%s\n", cfg.RegSecret)
		fmt.Fprintf(h, "expiry_date=%s\n", cfg.ExpiryDate)
		fmt.Fprintf(h, "self_check=%v\n", cfg.SelfCheck)
		fmt.Fprintf(h, "malleable_prepend=%s\n", cfg.MalleablePrepend)
		fmt.Fprintf(h, "malleable_append=%s\n", cfg.MalleableAppend)
		fmt.Fprintf(h, "malleable_request_prepend=%s\n", cfg.MalleableRequestPrepend)
		fmt.Fprintf(h, "malleable_request_append=%s\n", cfg.MalleableRequestAppend)
		fmt.Fprintf(h, "obfuscate=%v\n", cfg.Obfuscate)
		fmt.Fprintf(h, "slim=%v\n", cfg.Slim)
		fmt.Fprintf(h, "upx=%v\n", cfg.UPX)
		fmt.Fprintf(h, "win7_compat=%v\n", cfg.Win7Compat)
		fmt.Fprintf(h, "sign_mode=%s\n", cfg.SignMode)
		fmt.Fprintf(h, "sign_org=%s\n", cfg.SignOrg)
		fmt.Fprintf(h, "sign_pfx=%v\n", len(cfg.SignPFX) > 0)
		fmt.Fprintf(h, "sign_pfx_password=%s\n", cfg.SignPFXPassword)
		fmt.Fprintf(h, "sign_cert_pem=%v\n", len(cfg.SignCertPEM) > 0)
		fmt.Fprintf(h, "sign_key_pem=%v\n", len(cfg.SignKeyPEM) > 0)
		fmt.Fprintf(h, "modules=%v\n", cfg.Modules)
	}
	
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:32] // Use first 32 chars of hash
}

// Get retrieves a cached build artifact if it exists and is valid.
func (c *BuildCache) Get(key string) (string, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		return "", false
	}

	// Check if entry has expired
	if time.Since(entry.CreatedAt) > c.ttl {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		// Clean up file
		os.Remove(entry.OutputPath)
		return "", false
	}

	// Check if file still exists
	if _, err := os.Stat(entry.OutputPath); os.IsNotExist(err) {
		c.mu.Lock()
		delete(c.entries, key)
		c.currentSize -= entry.Size
		c.mu.Unlock()
		return "", false
	}

	// Update access time and count
	c.mu.Lock()
	entry.LastAccessed = time.Now()
	entry.AccessCount++
	c.mu.Unlock()

	return entry.OutputPath, true
}

// Put stores a build artifact in the cache.
func (c *BuildCache) Put(key, sourcePath string) error {
	// Check if source file exists
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("source file does not exist: %w", err)
	}

	// Check if we need to evict entries to make space
	if err := c.ensureSpace(info.Size()); err != nil {
		return err
	}

	// Copy file to cache directory
	destPath := filepath.Join(c.cacheDir, key+filepath.Ext(sourcePath))
	if err := copyFile(sourcePath, destPath); err != nil {
		return fmt.Errorf("failed to copy file to cache: %w", err)
	}

	entry := &BuildCacheEntry{
		Key:          key,
		OutputPath:   destPath,
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
		AccessCount:  0,
		Size:         info.Size(),
	}

	c.mu.Lock()
	c.entries[key] = entry
	c.currentSize += info.Size()
	c.mu.Unlock()

	// Persist to disk asynchronously
	go c.saveToDisk()

	return nil
}

// ensureSpace ensures there's enough space in the cache for a new entry.
// It evicts old entries if necessary.
func (c *BuildCache) ensureSpace(requiredSize int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if we need to evict entries
	for c.currentSize+requiredSize > c.maxSize || len(c.entries) >= c.maxEntries {
		if len(c.entries) == 0 {
			return fmt.Errorf("cache is full and cannot evict any entries")
		}

		// Find the least recently used entry
		var oldestKey string
		var oldestTime time.Time
		for key, entry := range c.entries {
			if oldestTime.IsZero() || entry.LastAccessed.Before(oldestTime) {
				oldestTime = entry.LastAccessed
				oldestKey = key
			}
		}

		if oldestKey != "" {
			entry := c.entries[oldestKey]
			os.Remove(entry.OutputPath)
			c.currentSize -= entry.Size
			delete(c.entries, oldestKey)
		}
	}

	return nil
}

// copyFile is defined in handlers_backup.go

// saveToDisk saves the cache index to disk.
func (c *BuildCache) saveToDisk() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	indexPath := filepath.Join(c.cacheDir, "index.json")
	data, err := json.MarshalIndent(c.entries, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(indexPath, data, 0644)
}

// loadFromDisk loads the cache index from disk.
func (c *BuildCache) loadFromDisk() error {
	indexPath := filepath.Join(c.cacheDir, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No existing cache, start fresh
		}
		return err
	}

	var entries map[string]*BuildCacheEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for key, entry := range entries {
		// Verify file still exists
		if _, err := os.Stat(entry.OutputPath); err == nil {
			c.entries[key] = entry
			c.currentSize += entry.Size
		}
	}

	return nil
}

// cleanupLoop periodically cleans up expired entries.
func (c *BuildCache) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		c.cleanupExpired()
	}
}

// cleanupExpired removes expired entries from the cache.
func (c *BuildCache) cleanupExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.entries {
		if now.Sub(entry.CreatedAt) > c.ttl {
			os.Remove(entry.OutputPath)
			c.currentSize -= entry.Size
			delete(c.entries, key)
		}
	}

	// Save updated index
	go c.saveToDisk()
}

// Stats returns cache statistics.
type BuildCacheStats struct {
	Entries      int
	TotalSize    int64
	MaxSize      int64
	MaxEntries   int
	HitCount     int64
	MissCount    int64
	HitRate      float64
}

func (c *BuildCache) Stats() BuildCacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var totalHits, totalMisses int64
	// Note: We don't track hits/misses in this simple implementation
	// In a production system, you'd want to add atomic counters for hits/misses

	return BuildCacheStats{
		Entries:    len(c.entries),
		TotalSize:  c.currentSize,
		MaxSize:    c.maxSize,
		MaxEntries: c.maxEntries,
		HitCount:   totalHits,
		MissCount:  totalMisses,
		HitRate:    0,
	}
}