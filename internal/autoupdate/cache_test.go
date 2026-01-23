package autoupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestCacheTTLBehavior tests Property 4: Cache TTL Behavior
// **Feature: ebuild-autoupdate, Property 4: Cache TTL Behavior**
// **Validates: Requirements 3.3, 3.4, 3.5**
func TestCacheTTLBehavior(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Cache returns value when within TTL
	properties.Property("Cache returns value when timestamp is within TTL", prop.ForAll(
		func(pkg, version, source string, ageSeconds int) bool {
			tmpDir := t.TempDir()

			// Age must be positive and less than TTL (3600 seconds = 1 hour)
			if ageSeconds < 0 {
				ageSeconds = -ageSeconds
			}
			ageSeconds = ageSeconds % 3500 // Ensure within TTL

			// Create a fixed "now" time
			fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
			entryTime := fixedNow.Add(-time.Duration(ageSeconds) * time.Second)

			cache, err := NewCache(tmpDir, WithNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create cache: %v", err)
				return false
			}

			// Manually set entry with specific timestamp
			cache.Entries[pkg] = CacheEntry{
				Version:   version,
				Timestamp: entryTime,
				Source:    source,
			}

			// Get should return the value since it's within TTL
			result, found := cache.Get(pkg)
			if !found {
				t.Logf("Expected cache hit for age %d seconds", ageSeconds)
				return false
			}
			return result == version
		},
		genPackageName(),
		genVersion(),
		genValidURL(),
		gen.IntRange(0, 3500),
	))

	// Property: Cache returns miss when TTL expired
	properties.Property("Cache returns miss when timestamp exceeds TTL", prop.ForAll(
		func(pkg, version, source string, extraSeconds int) bool {
			tmpDir := t.TempDir()

			// Extra seconds beyond TTL (1-1000 seconds past expiry)
			if extraSeconds < 1 {
				extraSeconds = 1
			}
			extraSeconds = (extraSeconds % 1000) + 1

			// Create a fixed "now" time
			fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
			// Entry time is TTL + extra seconds ago (expired)
			entryTime := fixedNow.Add(-DefaultCacheTTL - time.Duration(extraSeconds)*time.Second)

			cache, err := NewCache(tmpDir, WithNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create cache: %v", err)
				return false
			}

			// Manually set entry with expired timestamp
			cache.Entries[pkg] = CacheEntry{
				Version:   version,
				Timestamp: entryTime,
				Source:    source,
			}

			// Get should return miss since TTL expired
			_, found := cache.Get(pkg)
			if found {
				t.Logf("Expected cache miss for expired entry (extra %d seconds past TTL)", extraSeconds)
				return false
			}
			return true
		},
		genPackageName(),
		genVersion(),
		genValidURL(),
		gen.IntRange(1, 1000),
	))

	// Property: Force flag always returns cache miss
	properties.Property("Force flag always returns cache miss regardless of TTL", prop.ForAll(
		func(pkg, version, source string) bool {
			tmpDir := t.TempDir()

			fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)

			cache, err := NewCache(tmpDir, WithNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create cache: %v", err)
				return false
			}

			// Set a fresh entry (just now)
			cache.Entries[pkg] = CacheEntry{
				Version:   version,
				Timestamp: fixedNow,
				Source:    source,
			}

			// GetWithForce(force=true) should return miss
			_, found := cache.GetWithForce(pkg, true)
			if found {
				t.Log("Expected cache miss when force=true")
				return false
			}

			// GetWithForce(force=false) should return hit
			result, found := cache.GetWithForce(pkg, false)
			if !found {
				t.Log("Expected cache hit when force=false")
				return false
			}
			return result == version
		},
		genPackageName(),
		genVersion(),
		genValidURL(),
	))

	properties.TestingRun(t)
}

// TestCacheUpdateOnQuery tests Property 5: Cache Update on Query
// **Feature: ebuild-autoupdate, Property 5: Cache Update on Query**
// **Validates: Requirements 3.6**
func TestCacheUpdateOnQuery(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Set stores entry with correct version and timestamp
	properties.Property("Set stores entry with version and timestamp within bounds", prop.ForAll(
		func(pkg, version, source string) bool {
			tmpDir := t.TempDir()

			fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)

			cache, err := NewCache(tmpDir, WithNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create cache: %v", err)
				return false
			}

			// Set the entry
			if err := cache.Set(pkg, version, source); err != nil {
				t.Logf("Failed to set cache entry: %v", err)
				return false
			}

			// Verify entry exists with correct values
			entry, exists := cache.GetEntry(pkg)
			if !exists {
				t.Log("Entry not found after Set")
				return false
			}

			if entry.Version != version {
				t.Logf("Version mismatch: expected %q, got %q", version, entry.Version)
				return false
			}

			if entry.Source != source {
				t.Logf("Source mismatch: expected %q, got %q", source, entry.Source)
				return false
			}

			// Timestamp should be exactly fixedNow
			if !entry.Timestamp.Equal(fixedNow) {
				t.Logf("Timestamp mismatch: expected %v, got %v", fixedNow, entry.Timestamp)
				return false
			}

			return true
		},
		genPackageName(),
		genVersion(),
		genValidURL(),
	))

	// Property: Set persists to disk
	properties.Property("Set persists entry to disk", prop.ForAll(
		func(pkg, version, source string) bool {
			tmpDir := t.TempDir()

			fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)

			cache, err := NewCache(tmpDir, WithNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create cache: %v", err)
				return false
			}

			// Set the entry
			if err := cache.Set(pkg, version, source); err != nil {
				t.Logf("Failed to set cache entry: %v", err)
				return false
			}

			// Create a new cache instance to verify persistence
			cache2, err := NewCache(tmpDir, WithNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create second cache: %v", err)
				return false
			}

			// Verify entry exists in new cache
			result, found := cache2.Get(pkg)
			if !found {
				t.Log("Entry not found in reloaded cache")
				return false
			}

			return result == version
		},
		genPackageName(),
		genVersion(),
		genValidURL(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests
// =============================================================================

// TestNewCacheCreatesDirectory tests that NewCache creates the config directory
func TestNewCacheCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "subdir", "autoupdate")

	cache, err := NewCache(configDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify directory was created
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatalf("Config directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("Expected directory, got file")
	}

	// Verify cache is empty
	if cache.Len() != 0 {
		t.Errorf("Expected empty cache, got %d entries", cache.Len())
	}
}

// TestNewCacheLoadsExisting tests that NewCache loads existing cache file
func TestNewCacheLoadsExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Create existing cache file
	cacheData := `{
		"entries": {
			"net-misc/test-pkg": {
				"version": "1.2.3",
				"timestamp": "2026-01-22T12:00:00Z",
				"source": "https://example.com/api"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(tmpDir, "cache.json"), []byte(cacheData), 0644); err != nil {
		t.Fatalf("Failed to write cache file: %v", err)
	}

	cache, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify entry was loaded
	entry, exists := cache.GetEntry("net-misc/test-pkg")
	if !exists {
		t.Fatal("Expected entry to exist")
	}
	if entry.Version != "1.2.3" {
		t.Errorf("Expected version '1.2.3', got %q", entry.Version)
	}
}

// TestNewCacheHandlesCorruptedFile tests that NewCache handles corrupted cache file
func TestNewCacheHandlesCorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create corrupted cache file
	if err := os.WriteFile(filepath.Join(tmpDir, "cache.json"), []byte("{invalid json"), 0644); err != nil {
		t.Fatalf("Failed to write cache file: %v", err)
	}

	// Should not return error, just start with empty cache
	cache, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify cache is empty
	if cache.Len() != 0 {
		t.Errorf("Expected empty cache after corruption, got %d entries", cache.Len())
	}
}

// TestCacheGetMiss tests Get returns false for non-existent entry
func TestCacheGetMiss(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	_, found := cache.Get("non-existent/pkg")
	if found {
		t.Error("Expected cache miss for non-existent entry")
	}
}

// TestCacheGetExpired tests Get returns false for expired entry
func TestCacheGetExpired(t *testing.T) {
	tmpDir := t.TempDir()

	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
	cache, err := NewCache(tmpDir, WithNowFunc(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Set entry with old timestamp (2 hours ago)
	cache.Entries["test/pkg"] = CacheEntry{
		Version:   "1.0.0",
		Timestamp: fixedNow.Add(-2 * time.Hour),
		Source:    "https://example.com",
	}

	_, found := cache.Get("test/pkg")
	if found {
		t.Error("Expected cache miss for expired entry")
	}
}

// TestCacheGetValid tests Get returns true for valid entry
func TestCacheGetValid(t *testing.T) {
	tmpDir := t.TempDir()

	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
	cache, err := NewCache(tmpDir, WithNowFunc(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Set entry with recent timestamp (30 minutes ago)
	cache.Entries["test/pkg"] = CacheEntry{
		Version:   "1.0.0",
		Timestamp: fixedNow.Add(-30 * time.Minute),
		Source:    "https://example.com",
	}

	version, found := cache.Get("test/pkg")
	if !found {
		t.Error("Expected cache hit for valid entry")
	}
	if version != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got %q", version)
	}
}

// TestCacheSetAndGet tests Set followed by Get
func TestCacheSetAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if err := cache.Set("test/pkg", "2.0.0", "https://example.com/api"); err != nil {
		t.Fatalf("Failed to set: %v", err)
	}

	version, found := cache.Get("test/pkg")
	if !found {
		t.Error("Expected cache hit after Set")
	}
	if version != "2.0.0" {
		t.Errorf("Expected version '2.0.0', got %q", version)
	}
}

// TestCacheSetPersists tests that Set persists to disk
func TestCacheSetPersists(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if err := cache.Set("test/pkg", "3.0.0", "https://example.com/api"); err != nil {
		t.Fatalf("Failed to set: %v", err)
	}

	// Read cache file directly
	data, err := os.ReadFile(filepath.Join(tmpDir, "cache.json"))
	if err != nil {
		t.Fatalf("Failed to read cache file: %v", err)
	}

	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		t.Fatalf("Failed to parse cache file: %v", err)
	}

	entry, exists := cf.Entries["test/pkg"]
	if !exists {
		t.Fatal("Entry not found in cache file")
	}
	if entry.Version != "3.0.0" {
		t.Errorf("Expected version '3.0.0' in file, got %q", entry.Version)
	}
}

// TestCacheDelete tests Delete removes entry
func TestCacheDelete(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if err := cache.Set("test/pkg", "1.0.0", "https://example.com"); err != nil {
		t.Fatalf("Failed to set: %v", err)
	}

	if err := cache.Delete("test/pkg"); err != nil {
		t.Fatalf("Failed to delete: %v", err)
	}

	_, found := cache.Get("test/pkg")
	if found {
		t.Error("Expected cache miss after Delete")
	}
}

// TestCacheClear tests Clear removes all entries
func TestCacheClear(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add multiple entries
	cache.Set("test/pkg1", "1.0.0", "https://example.com/1")
	cache.Set("test/pkg2", "2.0.0", "https://example.com/2")

	if cache.Len() != 2 {
		t.Errorf("Expected 2 entries, got %d", cache.Len())
	}

	if err := cache.Clear(); err != nil {
		t.Fatalf("Failed to clear: %v", err)
	}

	if cache.Len() != 0 {
		t.Errorf("Expected 0 entries after Clear, got %d", cache.Len())
	}
}

// TestCacheCleanup tests Cleanup removes expired entries
func TestCacheCleanup(t *testing.T) {
	tmpDir := t.TempDir()

	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
	cache, err := NewCache(tmpDir, WithNowFunc(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add expired entry
	cache.Entries["test/expired"] = CacheEntry{
		Version:   "1.0.0",
		Timestamp: fixedNow.Add(-2 * time.Hour),
		Source:    "https://example.com",
	}

	// Add valid entry
	cache.Entries["test/valid"] = CacheEntry{
		Version:   "2.0.0",
		Timestamp: fixedNow.Add(-30 * time.Minute),
		Source:    "https://example.com",
	}

	if err := cache.Cleanup(); err != nil {
		t.Fatalf("Failed to cleanup: %v", err)
	}

	// Expired entry should be gone
	_, exists := cache.GetEntry("test/expired")
	if exists {
		t.Error("Expected expired entry to be removed")
	}

	// Valid entry should remain
	_, exists = cache.GetEntry("test/valid")
	if !exists {
		t.Error("Expected valid entry to remain")
	}
}

// TestCacheWithCustomTTL tests WithTTL option
func TestCacheWithCustomTTL(t *testing.T) {
	tmpDir := t.TempDir()

	customTTL := 5 * time.Minute
	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)

	cache, err := NewCache(tmpDir,
		WithTTL(customTTL),
		WithNowFunc(func() time.Time { return fixedNow }),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Entry 4 minutes ago should be valid
	cache.Entries["test/valid"] = CacheEntry{
		Version:   "1.0.0",
		Timestamp: fixedNow.Add(-4 * time.Minute),
		Source:    "https://example.com",
	}

	_, found := cache.Get("test/valid")
	if !found {
		t.Error("Expected cache hit for entry within custom TTL")
	}

	// Entry 6 minutes ago should be expired
	cache.Entries["test/expired"] = CacheEntry{
		Version:   "2.0.0",
		Timestamp: fixedNow.Add(-6 * time.Minute),
		Source:    "https://example.com",
	}

	_, found = cache.Get("test/expired")
	if found {
		t.Error("Expected cache miss for entry beyond custom TTL")
	}
}

// TestCacheGetWithForce tests GetWithForce behavior
func TestCacheGetWithForce(t *testing.T) {
	tmpDir := t.TempDir()

	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
	cache, err := NewCache(tmpDir, WithNowFunc(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Set a fresh entry
	cache.Entries["test/pkg"] = CacheEntry{
		Version:   "1.0.0",
		Timestamp: fixedNow,
		Source:    "https://example.com",
	}

	// Without force, should return hit
	version, found := cache.GetWithForce("test/pkg", false)
	if !found {
		t.Error("Expected cache hit without force")
	}
	if version != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got %q", version)
	}

	// With force, should return miss
	_, found = cache.GetWithForce("test/pkg", true)
	if found {
		t.Error("Expected cache miss with force")
	}
}

// TestCacheAtomicWrite tests that cache writes are atomic
func TestCacheAtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Set an entry
	if err := cache.Set("test/pkg", "1.0.0", "https://example.com"); err != nil {
		t.Fatalf("Failed to set: %v", err)
	}

	// Verify no temp file remains
	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read dir: %v", err)
	}

	for _, f := range files {
		if f.Name() == "cache.json.tmp" {
			t.Error("Temp file should not remain after successful write")
		}
	}
}
