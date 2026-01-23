// Package autoupdate provides cache management for version query results.
package autoupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Error variables for cache errors
var (
	// ErrCacheCorrupted is returned when the cache file cannot be parsed
	ErrCacheCorrupted = errors.New("cache file is corrupted")
	// ErrCacheMiss is returned when a cache entry is not found or expired
	ErrCacheMiss = errors.New("cache miss")
)

// DefaultCacheTTL is the default time-to-live for cache entries (1 hour)
const DefaultCacheTTL = time.Hour

// CacheEntry represents a cached version query result.
// It stores the version, when it was cached, and the source URL.
type CacheEntry struct {
	// Version is the cached version string
	Version string `json:"version"`
	// Timestamp is when this entry was cached
	Timestamp time.Time `json:"timestamp"`
	// Source is the URL that was queried to get this version
	Source string `json:"source"`
}

// cacheFile represents the JSON structure stored on disk
type cacheFile struct {
	Entries map[string]CacheEntry `json:"entries"`
}

// Cache manages version query caching with TTL-based expiration.
// It persists cache entries to disk and supports concurrent access.
type Cache struct {
	// Entries holds all cached version entries, keyed by package name
	Entries map[string]CacheEntry `json:"entries"`
	// TTL is the time-to-live for cache entries
	TTL time.Duration
	// path is the file path where cache is persisted
	path string
	// mu protects concurrent access to Entries
	mu sync.RWMutex
	// nowFunc allows injecting time for testing
	nowFunc func() time.Time
}

// CacheOption is a functional option for configuring Cache
type CacheOption func(*Cache)

// WithTTL sets a custom TTL for the cache
func WithTTL(ttl time.Duration) CacheOption {
	return func(c *Cache) {
		c.TTL = ttl
	}
}

// WithNowFunc sets a custom time function for testing
func WithNowFunc(fn func() time.Time) CacheOption {
	return func(c *Cache) {
		c.nowFunc = fn
	}
}

// NewCache creates or loads a cache from disk.
// If the cache file exists, it loads existing entries.
// If the cache file doesn't exist or is corrupted, it creates a new empty cache.
// The configDir should be the bentoo config directory (e.g., ~/.config/bentoo/autoupdate).
func NewCache(configDir string, opts ...CacheOption) (*Cache, error) {
	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	cachePath := filepath.Join(configDir, "cache.json")

	cache := &Cache{
		Entries: make(map[string]CacheEntry),
		TTL:     DefaultCacheTTL,
		path:    cachePath,
		nowFunc: time.Now,
	}

	// Apply options
	for _, opt := range opts {
		opt(cache)
	}

	// Try to load existing cache
	if err := cache.load(); err != nil {
		// If file doesn't exist, that's fine - start with empty cache
		if !os.IsNotExist(err) {
			// Log corruption but continue with empty cache
			// The corrupted file will be overwritten on next Save
			cache.Entries = make(map[string]CacheEntry)
		}
	}

	return cache, nil
}

// load reads the cache from disk
func (c *Cache) load() error {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}

	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return fmt.Errorf("%w: %v", ErrCacheCorrupted, err)
	}

	if cf.Entries != nil {
		c.Entries = cf.Entries
	}

	return nil
}

// Get retrieves a cached version if it exists and is not expired.
// Returns the version and true if found and valid, empty string and false otherwise.
func (c *Cache) Get(pkg string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.Entries[pkg]
	if !exists {
		return "", false
	}

	// Check if entry is expired
	if c.isExpired(entry) {
		return "", false
	}

	return entry.Version, true
}

// GetWithForce retrieves a cached version, optionally ignoring the cache.
// If force is true, always returns cache miss.
// Returns the version and true if found and valid (and not forced), empty string and false otherwise.
func (c *Cache) GetWithForce(pkg string, force bool) (string, bool) {
	if force {
		return "", false
	}
	return c.Get(pkg)
}

// isExpired checks if a cache entry has expired based on TTL
func (c *Cache) isExpired(entry CacheEntry) bool {
	now := c.nowFunc()
	age := now.Sub(entry.Timestamp)
	return age >= c.TTL
}

// Set stores a version in the cache with the current timestamp.
// It automatically saves the cache to disk after setting.
func (c *Cache) Set(pkg, version, source string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.Entries[pkg] = CacheEntry{
		Version:   version,
		Timestamp: c.nowFunc(),
		Source:    source,
	}

	return c.saveUnsafe()
}

// Save persists the cache to disk.
// This is thread-safe and can be called concurrently.
func (c *Cache) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveUnsafe()
}

// saveUnsafe persists the cache to disk without locking.
// Caller must hold the write lock.
func (c *Cache) saveUnsafe() error {
	cf := cacheFile{
		Entries: c.Entries,
	}

	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache: %w", err)
	}

	// Write to temp file first, then rename for atomicity
	tmpPath := c.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	if err := os.Rename(tmpPath, c.path); err != nil {
		// Clean up temp file on rename failure
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename cache file: %w", err)
	}

	return nil
}

// Delete removes a package from the cache.
// It automatically saves the cache to disk after deletion.
func (c *Cache) Delete(pkg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.Entries, pkg)
	return c.saveUnsafe()
}

// Clear removes all entries from the cache.
// It automatically saves the cache to disk after clearing.
func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.Entries = make(map[string]CacheEntry)
	return c.saveUnsafe()
}

// Len returns the number of entries in the cache.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.Entries)
}

// GetEntry retrieves the full cache entry for a package.
// Returns the entry and true if found, zero value and false otherwise.
// This does not check TTL - use Get for TTL-aware retrieval.
func (c *Cache) GetEntry(pkg string) (CacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.Entries[pkg]
	return entry, exists
}

// Cleanup removes all expired entries from the cache.
// It automatically saves the cache to disk after cleanup.
func (c *Cache) Cleanup() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for pkg, entry := range c.Entries {
		if c.isExpired(entry) {
			delete(c.Entries, pkg)
		}
	}

	return c.saveUnsafe()
}
