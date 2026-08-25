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

	"github.com/obentoo/bentoolkit/internal/common/fileutil"
	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// warnLogger adapts the package-level logger.Warn function to the
// fileutil.Logger interface, which expects a value with a Warn method.
// It is shared by the cache/pending/analysis-cache write-sites so that
// fileutil.SafeChmod can emit warnings through the standard logger.
type warnLogger struct{}

// Warn forwards to the package-level logger.Warn.
func (warnLogger) Warn(format string, args ...interface{}) {
	logger.Warn(format, args...)
}

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

// PreconditionRecord is what a build gate learned about THIS HOST while failing:
// a path the build needed and could not have. It is a fact about the machine, not
// about the package's upstream, and it is what lets a second run decline a gate
// that can only fail the same way again (S043-R3.1).
//
// It lives here — in cache.json, under ~/.config/bentoo — and it may live nowhere
// else. The obvious alternative, the package's own registry entry, is in
// packages.toml, and that file sits in an overlay that auto-commits and pushes
// within minutes: a note about one workstation's unreadable key would be
// published to every consumer of the overlay as though it were a property of the
// package.
type PreconditionRecord struct {
	// Path is the absolute path the failed build named and could not read. It is
	// never relative and never "/" — extractUnmetPrecondition refuses both,
	// because an answer that resolves differently depending on who asks is worse
	// than no answer at all.
	Path string `json:"path"`
	// RecordedAt is when the failure was observed. It is deliberately NOT an
	// expiry. The version TTL measures how stale an upstream answer is, which is
	// no evidence whatsoever about whether a key file has appeared on disk; a
	// record that timed out on a clock would simply restore the wasteful retry
	// loop it exists to stop. What ends a record is the path becoming readable.
	RecordedAt time.Time `json:"recorded_at"`
}

// cacheFile represents the JSON structure stored on disk
type cacheFile struct {
	Entries map[string]CacheEntry `json:"entries"`
	// Preconditions is a SECOND top-level map rather than a field on CacheEntry,
	// and the separation is load-bearing three times over: an entry expires on
	// the version TTL and a precondition must not (isExpired never sees this
	// map); a package with a precondition usually has no cached version at all,
	// so a shared struct would force Get/GetEntry to hand out half-populated
	// entries; and `omitempty` keeps a cache that has never recorded one byte for
	// byte what every release before this wrote. A cache.json written before this
	// change simply decodes the key as absent — nil — which load leaves alone.
	Preconditions map[string]PreconditionRecord `json:"preconditions,omitempty"`
}

// Cache manages version query caching with TTL-based expiration.
// It persists cache entries to disk and supports concurrent access.
type Cache struct {
	// Entries holds all cached version entries, keyed by package name
	Entries map[string]CacheEntry `json:"entries"`
	// Preconditions holds the host preconditions a build gate found unmet, keyed
	// by package exactly as Entries is. Every Cache carries it, including the
	// checker's, so that any writer round-trips the map instead of dropping it:
	// a save from an object that did not know the field would erase every record
	// on disk.
	Preconditions map[string]PreconditionRecord `json:"preconditions,omitempty"`
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
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	cachePath := filepath.Join(configDir, "cache.json")

	cache := &Cache{
		Entries:       make(map[string]CacheEntry),
		Preconditions: make(map[string]PreconditionRecord),
		TTL:           DefaultCacheTTL,
		path:          cachePath,
		nowFunc:       time.Now,
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
			cache.Preconditions = make(map[string]PreconditionRecord)
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

	// Absent in every cache.json written before S043 — and absent again the
	// moment the last record is cleared — so nil means "nothing recorded" and
	// leaves the freshly made map in place.
	if cf.Preconditions != nil {
		c.Preconditions = cf.Preconditions
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

// SetPrecondition records the host precondition a build gate found unmet for
// pkg, replacing any earlier record for the same package, and persists the cache.
//
// The path is expected to have come from extractUnmetPrecondition, which is where
// the "is this answer usable" judgement lives; this method is the storage and
// nothing more. It does not touch the package's version entry, so a package with
// a precondition and no cached version stays a cache MISS for version lookups —
// which is correct: nothing here is evidence about upstream.
func (c *Cache) SetPrecondition(pkg, path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Preconditions == nil {
		c.Preconditions = make(map[string]PreconditionRecord)
	}
	c.Preconditions[pkg] = PreconditionRecord{
		Path:       path,
		RecordedAt: c.nowFunc(),
	}

	return c.saveUnsafe()
}

// Precondition returns the recorded unmet precondition for pkg.
// Unlike Get, it applies no TTL — see PreconditionRecord.RecordedAt for why a
// clock is the wrong thing to expire this on.
func (c *Cache) Precondition(pkg string) (PreconditionRecord, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	rec, exists := c.Preconditions[pkg]
	return rec, exists
}

// DeletePrecondition drops the recorded precondition for pkg and persists the
// cache. Deleting a package that has no record is not an error: the caller's
// intent — "this package must not be held back" — is already satisfied.
func (c *Cache) DeletePrecondition(pkg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.Preconditions, pkg)
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
		Entries:       c.Entries,
		Preconditions: c.Preconditions,
	}

	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache: %w", err)
	}

	// Write to temp file first, then rename for atomicity. Cache files use
	// 0600 (owner-only) because they may hold sensitive upstream metadata.
	tmpPath := c.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, fileutil.CacheFileMode); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	if err := os.Rename(tmpPath, c.path); err != nil {
		// Clean up temp file on rename failure
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("failed to rename cache file: %w", err)
	}

	// os.Rename keeps the temp file's mode, which umask may have widened.
	// Re-apply the restrictive mode; tolerate filesystems without chmod.
	if err := fileutil.SafeChmod(c.path, fileutil.CacheFileMode, warnLogger{}); err != nil {
		return fmt.Errorf("failed to set cache file permissions: %w", err)
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
//
// Recorded preconditions go with them, deliberately: clearing the cache is what
// an operator does to unstick a run, and a "clear" that left a package held back
// by a diagnosis they cannot see would be the one state nothing can get out of.
func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.Entries = make(map[string]CacheEntry)
	c.Preconditions = make(map[string]PreconditionRecord)
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
//
// Preconditions are not swept here and must not be: the TTL is about how stale an
// upstream version answer is, and expiring a host diagnosis on that clock would
// hand the package back to the same failing build an hour later.
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
