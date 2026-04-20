package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// genVersionString generates a valid ebuild version string (e.g., "1.0", "2.3.1")
// Uses bounded integers to avoid extremely long strings that won't match the ebuild regex.
func genVersionString() gopter.Gen { //nolint:unused // helper for future PBT tests
	return gopter.CombineGens(
		gen.IntRange(1, 99),
		gen.IntRange(0, 99),
	).Map(func(vals []interface{}) string {
		major := vals[0].(int)
		minor := vals[1].(int)
		return fmt.Sprintf("%d.%d", major, minor)
	})
}

// genVersionList generates a non-empty slice of unique version strings (2-4 items)
func genVersionList() gopter.Gen {
	return gopter.CombineGens(
		gen.IntRange(1, 9),
		gen.IntRange(0, 9),
		gen.IntRange(10, 19),
		gen.IntRange(20, 29),
	).Map(func(vals []interface{}) []string {
		return []string{
			fmt.Sprintf("%d.%d", vals[0].(int), vals[1].(int)),
			fmt.Sprintf("%d.%d", vals[2].(int), vals[3].(int)),
		}
	})
}

// genValidPkgName generates a valid ebuild package name (lowercase letters only, no digits)
func genValidPkgName() gopter.Gen {
	names := []string{"hello", "vim", "gcc", "bash", "curl", "wget", "nano", "htop", "tmux", "zsh"}
	return gen.IntRange(0, len(names)-1).Map(func(i int) string {
		return names[i]
	})
}

// TestProviderCacheRoundTrip tests Property 7: Provider cache round-trip
// **Feature: test-coverage-improvement, Property 7: Provider cache round-trip**
// **Validates: Requirements 8.7**
func TestProviderCacheRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	properties.Property("GitHub cache round-trip preserves versions", prop.ForAll(
		func(versions []string) bool {
			cacheDir := t.TempDir()

			repoInfo := &RepositoryInfo{Name: "test", URL: "test/repo"}
			prov, err := NewGitHubProvider(repoInfo)
			if err != nil {
				return false
			}
			prov.CacheDir = cacheDir
			prov.CacheTTL = 1 * time.Hour

			// Save to cache
			prov.saveToCache("app-misc", "testpkg", versions)

			// Load from cache
			loaded, ok := prov.loadFromCache("app-misc", "testpkg")
			if !ok {
				return false
			}

			if len(loaded) != len(versions) {
				return false
			}
			for i, v := range versions {
				if loaded[i] != v {
					return false
				}
			}
			return true
		},
		genVersionList(),
	))

	properties.Property("GitLab cache round-trip preserves versions", prop.ForAll(
		func(versions []string) bool {
			cacheDir := t.TempDir()

			repoInfo := &RepositoryInfo{Name: "test", Provider: "gitlab", URL: "test/repo"}
			prov, err := NewGitLabProvider(repoInfo)
			if err != nil {
				return false
			}
			prov.CacheDir = cacheDir
			prov.CacheTTL = 1 * time.Hour

			// Save to cache
			prov.saveToCache("app-misc", "testpkg", versions)

			// Load from cache
			loaded, ok := prov.loadFromCache("app-misc", "testpkg")
			if !ok {
				return false
			}

			if len(loaded) != len(versions) {
				return false
			}
			for i, v := range versions {
				if loaded[i] != v {
					return false
				}
			}
			return true
		},
		genVersionList(),
	))

	properties.TestingRun(t)
}

// TestProviderCacheExpiredNotLoaded tests that expired cache entries are not returned
func TestProviderCacheExpiredNotLoaded(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 50

	properties := gopter.NewProperties(parameters)

	properties.Property("expired cache entry is not returned", prop.ForAll(
		func(versions []string) bool {
			cacheDir := t.TempDir()

			// Write an expired cache entry directly
			entry := CacheEntry{
				Versions:  versions,
				Timestamp: time.Now().Add(-48 * time.Hour),
			}
			data, err := json.Marshal(entry)
			if err != nil {
				return false
			}
			cacheFile := filepath.Join(cacheDir, "app-misc_testpkg.json")
			if err := os.WriteFile(cacheFile, data, 0644); err != nil {
				return false
			}

			repoInfo := &RepositoryInfo{Name: "test", URL: "test/repo"}
			prov, _ := NewGitHubProvider(repoInfo)
			prov.CacheDir = cacheDir
			prov.CacheTTL = 24 * time.Hour

			_, ok := prov.loadFromCache("app-misc", "testpkg")
			// Expired cache must NOT be returned
			return !ok
		},
		genVersionList(),
	))

	properties.TestingRun(t)
}

// TestScanLocalPackageVersionExtraction tests Property 8: scanLocalPackage version extraction
// **Feature: test-coverage-improvement, Property 8: scanLocalPackage version extraction**
// **Validates: Requirements 8.8**
func TestScanLocalPackageVersionExtraction(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	properties.Property("scanLocalPackage extracts exactly the versions from ebuild files", prop.ForAll(
		func(pkgName string, versions []string) bool {
			tmpDir := t.TempDir()

			// Create ebuild files for each version
			for _, v := range versions {
				filename := pkgName + "-" + v + ".ebuild"
				path := filepath.Join(tmpDir, filename)
				if err := os.WriteFile(path, []byte("# mock ebuild"), 0644); err != nil {
					return false
				}
			}

			// Add a non-ebuild file to ensure it's ignored
			_ = os.WriteFile(filepath.Join(tmpDir, "metadata.xml"), []byte("<pkgmetadata/>"), 0644)

			prov := &GitCloneProvider{LocalPath: tmpDir, RepoName: "test"}
			found, err := prov.scanLocalPackage(tmpDir, pkgName)
			if err != nil {
				return false
			}

			if len(found) != len(versions) {
				return false
			}

			// All versions must be present (order may differ)
			versionSet := map[string]bool{}
			for _, v := range versions {
				versionSet[v] = true
			}
			for _, v := range found {
				if !versionSet[v] {
					return false
				}
			}
			return true
		},
		genValidPkgName(),
		genVersionList(),
	))

	properties.TestingRun(t)
}

// TestRepositoryInfoDeepCopyIndependence tests Property 9: RepositoryInfo deep copy independence
// **Feature: test-coverage-improvement, Property 9: RepositoryInfo deep copy independence**
// **Validates: Requirements 8.10**
func TestRepositoryInfoDeepCopyIndependence(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	// Generator for non-empty strings
	genNonEmpty := gen.RegexMatch(`[a-z][a-z0-9]{1,10}`)

	properties.Property("Clone returns equal fields and mutations do not affect original", prop.ForAll(
		func(name, provider, url, token, branch string) bool {
			original := &RepositoryInfo{
				Name:     name,
				Provider: provider,
				URL:      url,
				Token:    token,
				Branch:   branch,
			}

			clone := original.Clone()

			// Fields must be equal
			if clone.Name != original.Name ||
				clone.Provider != original.Provider ||
				clone.URL != original.URL ||
				clone.Token != original.Token ||
				clone.Branch != original.Branch {
				return false
			}

			// Mutate clone — original must be unaffected
			clone.Name = strings.ToUpper(clone.Name) + "_mutated"
			clone.Token = "mutated_token"
			clone.URL = "mutated/url"

			return original.Name == name &&
				original.Token == token &&
				original.URL == url
		},
		genNonEmpty,
		genNonEmpty,
		genNonEmpty,
		genNonEmpty,
		genNonEmpty,
	))

	properties.TestingRun(t)
}
