package autoupdate

import (
	"os"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestAnalysisCacheTTL tests Property 24: Analysis Cache TTL
// **Feature: autoupdate-analyzer, Property 24: Analysis Cache TTL**
// **Validates: Requirements 10.1**
func TestAnalysisCacheTTL(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Cache returns schema when within 24-hour TTL
	properties.Property("Cache returns schema when timestamp is within 24-hour TTL", prop.ForAll(
		func(pkg, url string, ageHours int) bool {
			tmpDir := t.TempDir()

			// Age must be positive and less than 24 hours
			if ageHours < 0 {
				ageHours = -ageHours
			}
			ageHours %= 23 // Ensure within TTL (0-22 hours)

			// Create a fixed "now" time
			fixedNow := time.Date(2026, 1, 23, 12, 0, 0, 0, time.UTC)
			entryTime := fixedNow.Add(-time.Duration(ageHours) * time.Hour)

			cache, err := NewAnalysisCache(tmpDir, WithAnalysisCacheNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create analysis cache: %v", err)
				return false
			}

			// Create a test schema
			schema := &PackageConfig{
				URL:    url,
				Parser: "json",
				Path:   "version",
			}

			// Manually set entry with specific timestamp
			cache.Entries[pkg] = AnalysisCacheEntry{
				Schema:    schema,
				Timestamp: entryTime,
				URL:       url,
			}

			// Get should return the schema since it's within TTL
			result, found := cache.Get(pkg)
			if !found {
				t.Logf("Expected cache hit for age %d hours", ageHours)
				return false
			}
			return result.URL == url && result.Parser == "json"
		},
		genPackageName(),
		genValidURL(),
		gen.IntRange(0, 22),
	))

	// Property: Cache returns miss when 24-hour TTL expired
	properties.Property("Cache returns miss when timestamp exceeds 24-hour TTL", prop.ForAll(
		func(pkg, url string, extraHours int) bool {
			tmpDir := t.TempDir()

			// Extra hours beyond TTL (1-100 hours past expiry)
			if extraHours < 1 {
				extraHours = 1
			}
			extraHours = (extraHours % 100) + 1

			// Create a fixed "now" time
			fixedNow := time.Date(2026, 1, 23, 12, 0, 0, 0, time.UTC)
			// Entry time is 24 hours + extra hours ago (expired)
			entryTime := fixedNow.Add(-DefaultAnalysisCacheTTL - time.Duration(extraHours)*time.Hour)

			cache, err := NewAnalysisCache(tmpDir, WithAnalysisCacheNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create analysis cache: %v", err)
				return false
			}

			// Create a test schema
			schema := &PackageConfig{
				URL:    url,
				Parser: "json",
				Path:   "version",
			}

			// Manually set entry with expired timestamp
			cache.Entries[pkg] = AnalysisCacheEntry{
				Schema:    schema,
				Timestamp: entryTime,
				URL:       url,
			}

			// Get should return miss since TTL expired
			_, found := cache.Get(pkg)
			if found {
				t.Logf("Expected cache miss for expired entry (extra %d hours past TTL)", extraHours)
				return false
			}
			return true
		},
		genPackageName(),
		genValidURL(),
		gen.IntRange(1, 100),
	))

	// Property: Entry at exactly 24 hours is considered expired
	properties.Property("Entry at exactly 24 hours is considered expired", prop.ForAll(
		func(pkg, url string) bool {
			tmpDir := t.TempDir()

			fixedNow := time.Date(2026, 1, 23, 12, 0, 0, 0, time.UTC)
			// Entry time is exactly 24 hours ago
			entryTime := fixedNow.Add(-DefaultAnalysisCacheTTL)

			cache, err := NewAnalysisCache(tmpDir, WithAnalysisCacheNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create analysis cache: %v", err)
				return false
			}

			schema := &PackageConfig{
				URL:    url,
				Parser: "json",
				Path:   "version",
			}

			cache.Entries[pkg] = AnalysisCacheEntry{
				Schema:    schema,
				Timestamp: entryTime,
				URL:       url,
			}

			// Get should return miss since TTL is exactly reached
			_, found := cache.Get(pkg)
			return !found
		},
		genPackageName(),
		genValidURL(),
	))

	properties.TestingRun(t)
}

// TestCacheBypass tests Property 25: Cache Bypass
// **Feature: autoupdate-analyzer, Property 25: Cache Bypass**
// **Validates: Requirements 10.3**
func TestCacheBypass(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Bypass flag always returns cache miss regardless of TTL
	properties.Property("Bypass flag always returns cache miss regardless of TTL", prop.ForAll(
		func(pkg, url string) bool {
			tmpDir := t.TempDir()

			fixedNow := time.Date(2026, 1, 23, 12, 0, 0, 0, time.UTC)

			cache, err := NewAnalysisCache(tmpDir, WithAnalysisCacheNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create analysis cache: %v", err)
				return false
			}

			// Create a test schema
			schema := &PackageConfig{
				URL:    url,
				Parser: "json",
				Path:   "version",
			}

			// Set a fresh entry (just now)
			cache.Entries[pkg] = AnalysisCacheEntry{
				Schema:    schema,
				Timestamp: fixedNow,
				URL:       url,
			}

			// GetWithBypass(bypass=true) should return miss
			_, found := cache.GetWithBypass(pkg, true)
			if found {
				t.Log("Expected cache miss when bypass=true")
				return false
			}

			// GetWithBypass(bypass=false) should return hit
			result, found := cache.GetWithBypass(pkg, false)
			if !found {
				t.Log("Expected cache hit when bypass=false")
				return false
			}
			return result.URL == url
		},
		genPackageName(),
		genValidURL(),
	))

	// Property: Bypass does not affect cache contents
	properties.Property("Bypass does not affect cache contents", prop.ForAll(
		func(pkg, url string) bool {
			tmpDir := t.TempDir()

			fixedNow := time.Date(2026, 1, 23, 12, 0, 0, 0, time.UTC)

			cache, err := NewAnalysisCache(tmpDir, WithAnalysisCacheNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create analysis cache: %v", err)
				return false
			}

			schema := &PackageConfig{
				URL:    url,
				Parser: "json",
				Path:   "version",
			}

			// Set entry
			cache.Entries[pkg] = AnalysisCacheEntry{
				Schema:    schema,
				Timestamp: fixedNow,
				URL:       url,
			}

			// Call GetWithBypass with bypass=true
			cache.GetWithBypass(pkg, true)

			// Entry should still exist in cache
			entry, exists := cache.GetEntry(pkg)
			if !exists {
				t.Log("Entry should still exist after bypass read")
				return false
			}
			return entry.Schema.URL == url
		},
		genPackageName(),
		genValidURL(),
	))

	// Property: Bypass works for both existing and non-existing entries
	properties.Property("Bypass returns miss for both existing and non-existing entries", prop.ForAll(
		func(existingPkg, nonExistingPkg, url string) bool {
			// Ensure packages are different
			if existingPkg == nonExistingPkg {
				nonExistingPkg += "-other"
			}

			tmpDir := t.TempDir()

			fixedNow := time.Date(2026, 1, 23, 12, 0, 0, 0, time.UTC)

			cache, err := NewAnalysisCache(tmpDir, WithAnalysisCacheNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create analysis cache: %v", err)
				return false
			}

			schema := &PackageConfig{
				URL:    url,
				Parser: "json",
				Path:   "version",
			}

			// Only set entry for existingPkg
			cache.Entries[existingPkg] = AnalysisCacheEntry{
				Schema:    schema,
				Timestamp: fixedNow,
				URL:       url,
			}

			// Both should return miss with bypass=true
			_, foundExisting := cache.GetWithBypass(existingPkg, true)
			_, foundNonExisting := cache.GetWithBypass(nonExistingPkg, true)

			if foundExisting {
				t.Log("Expected miss for existing entry with bypass=true")
				return false
			}
			if foundNonExisting {
				t.Log("Expected miss for non-existing entry with bypass=true")
				return false
			}
			return true
		},
		genPackageName(),
		genPackageName(),
		genValidURL(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests — AnalysisCache CRUD operations
// =============================================================================

// TestAnalysisCacheSet tests Set stores entry and persists to disk
func TestAnalysisCacheSet(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache: %v", err)
	}

	schema := &PackageConfig{URL: "https://example.com", Parser: "json", Path: "version"}
	if err := cache.Set("app-misc/hello", schema, "https://example.com"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Should be retrievable
	got, found := cache.Get("app-misc/hello")
	if !found {
		t.Fatal("Expected cache hit after Set")
	}
	if got.URL != schema.URL {
		t.Errorf("URL mismatch: got %q, want %q", got.URL, schema.URL)
	}
}

// TestAnalysisCacheSetPersistsToDisk tests that Set writes to disk
func TestAnalysisCacheSetPersistsToDisk(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache: %v", err)
	}

	schema := &PackageConfig{URL: "https://example.com", Parser: "json", Path: "version"}
	if err := cache.Set("app-misc/hello", schema, "https://example.com"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Reload from disk
	cache2, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache reload: %v", err)
	}
	_, found := cache2.Get("app-misc/hello")
	if !found {
		t.Error("Expected entry to persist to disk after Set")
	}
}

// TestAnalysisCacheSave tests Save persists current state
func TestAnalysisCacheSave(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache: %v", err)
	}

	// Manually add entry without saving
	cache.Entries["app-misc/test"] = AnalysisCacheEntry{
		Schema:    &PackageConfig{URL: "https://test.com", Parser: "json"},
		Timestamp: cache.nowFunc(),
		URL:       "https://test.com",
	}

	if err := cache.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload and verify
	cache2, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache reload: %v", err)
	}
	if _, found := cache2.Get("app-misc/test"); !found {
		t.Error("Expected entry to persist after Save")
	}
}

// TestAnalysisCacheDelete tests Delete removes entry and persists
func TestAnalysisCacheDelete(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache: %v", err)
	}

	schema := &PackageConfig{URL: "https://example.com", Parser: "json"}
	if err := cache.Set("app-misc/hello", schema, "https://example.com"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := cache.Delete("app-misc/hello"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, found := cache.Get("app-misc/hello"); found {
		t.Error("Expected cache miss after Delete")
	}
	if cache.Len() != 0 {
		t.Errorf("Expected Len=0 after Delete, got %d", cache.Len())
	}
}

// TestAnalysisCacheClear tests Clear removes all entries
func TestAnalysisCacheClear(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache: %v", err)
	}

	schema := &PackageConfig{URL: "https://example.com", Parser: "json"}
	_ = cache.Set("app-misc/pkg1", schema, "https://example.com")
	_ = cache.Set("app-misc/pkg2", schema, "https://example.com")

	if cache.Len() != 2 {
		t.Fatalf("Expected Len=2 before Clear, got %d", cache.Len())
	}

	if err := cache.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	if cache.Len() != 0 {
		t.Errorf("Expected Len=0 after Clear, got %d", cache.Len())
	}
}

// TestAnalysisCacheLen tests Len returns correct count
func TestAnalysisCacheLen(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache: %v", err)
	}

	if cache.Len() != 0 {
		t.Errorf("Expected Len=0 for empty cache, got %d", cache.Len())
	}

	schema := &PackageConfig{URL: "https://example.com", Parser: "json"}
	_ = cache.Set("app-misc/pkg1", schema, "https://example.com")
	_ = cache.Set("app-misc/pkg2", schema, "https://example.com")

	if cache.Len() != 2 {
		t.Errorf("Expected Len=2, got %d", cache.Len())
	}
}

// TestAnalysisCacheCleanup tests Cleanup removes expired entries
func TestAnalysisCacheCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	fixedNow := func() time.Time { return time.Date(2026, 1, 23, 12, 0, 0, 0, time.UTC) }
	cache, err := NewAnalysisCache(tmpDir, WithAnalysisCacheNowFunc(fixedNow))
	if err != nil {
		t.Fatalf("NewAnalysisCache: %v", err)
	}

	schema := &PackageConfig{URL: "https://example.com", Parser: "json"}

	// Add fresh entry
	cache.Entries["app-misc/fresh"] = AnalysisCacheEntry{
		Schema:    schema,
		Timestamp: fixedNow(),
		URL:       "https://example.com",
	}
	// Add expired entry (25 hours ago)
	cache.Entries["app-misc/expired"] = AnalysisCacheEntry{
		Schema:    schema,
		Timestamp: fixedNow().Add(-25 * time.Hour),
		URL:       "https://example.com",
	}

	if err := cache.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if cache.Len() != 1 {
		t.Errorf("Expected Len=1 after Cleanup, got %d", cache.Len())
	}
	if _, found := cache.Get("app-misc/fresh"); !found {
		t.Error("Expected fresh entry to survive Cleanup")
	}
	if _, found := cache.GetEntry("app-misc/expired"); found {
		t.Error("Expected expired entry to be removed by Cleanup")
	}
}

// TestWithAnalysisCacheTTL tests the TTL option
func TestWithAnalysisCacheTTL(t *testing.T) {
	tmpDir := t.TempDir()
	customTTL := 1 * time.Hour
	cache, err := NewAnalysisCache(tmpDir, WithAnalysisCacheTTL(customTTL))
	if err != nil {
		t.Fatalf("NewAnalysisCache: %v", err)
	}

	if cache.TTL != customTTL {
		t.Errorf("Expected TTL=%v, got %v", customTTL, cache.TTL)
	}
}

// TestAnalysisCacheCorruptedFile tests that corrupted cache file is handled gracefully
func TestAnalysisCacheCorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Write corrupted JSON
	cachePath := tmpDir + "/analysis_cache.json"
	if err := os.WriteFile(cachePath, []byte("{invalid json"), 0644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	// Should not error — corrupted cache starts fresh
	cache, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("Expected no error for corrupted cache, got: %v", err)
	}
	if cache.Len() != 0 {
		t.Errorf("Expected empty cache after corruption, got Len=%d", cache.Len())
	}
}

// TestAnalysisCacheWrite_FinalModeIs0600 verifies that the analysis cache file
// persisted by AnalysisCache.Set ends up with owner-only (0600) permissions
// end-to-end. The save path writes a temp file then renames it, so the final
// mode depends on the post-rename SafeChmod call repairing umask-widened bits.
func TestAnalysisCacheWrite_FinalModeIs0600(t *testing.T) {
	tmpDir := t.TempDir()

	cache, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache failed: %v", err)
	}

	schema := &PackageConfig{URL: "https://example.com", Parser: "json", Path: "version"}
	if err := cache.Set("app-misc/hello", schema, "https://example.com"); err != nil {
		t.Fatalf("AnalysisCache.Set failed: %v", err)
	}

	info, err := os.Stat(tmpDir + "/analysis_cache.json")
	if err != nil {
		t.Fatalf("os.Stat on analysis cache file failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("analysis cache file mode = %#o, want %#o", got, 0o600)
	}
}
