package fetch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- orchestrator-authored, Run mode ----------------------------------------
//
// The storage layer arrived with no test of its own — sub-task 3.1's two
// pre-authored tests both stop at the extractor. R3.1 says the system SHALL
// RECORD the precondition, and "records it" was the untested half.
//
// The two below are the ones the sub-task's own report named as the ones it
// would most want pinned before this ships. Recorded in .draft/deviations.yaml.

// R3.1 — a precondition must survive being written and read back, and must NOT
// be reachable through the version cache. The two facts share a file and nothing
// else: a path that is unreadable says nothing about upstream, and a package
// carrying one but no cached version must still be a plain version-cache MISS.
func TestPreconditionRoundTripsWithoutTouchingTheVersionCache(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewCache(dir)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	if err := cache.SetPrecondition("net-wireless/mt7927-dkms", "/etc/kernel/keys/module-signing.key"); err != nil {
		t.Fatalf("SetPrecondition: %v", err)
	}

	// Read back through a SECOND cache object, so the assertion is about the file
	// and not about the map still in memory.
	reopened, err := NewCache(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rec, ok := reopened.Precondition("net-wireless/mt7927-dkms")
	if !ok || rec.Path != "/etc/kernel/keys/module-signing.key" {
		t.Errorf("Precondition = %+v, %v; want the recorded path", rec, ok)
	}

	// The version cache must not have learned anything. A CacheEntry with an
	// empty Version handed to a version caller is the corruption this separation
	// exists to prevent.
	if _, hit := reopened.Get("net-wireless/mt7927-dkms"); hit {
		t.Error("the version cache reports a hit for a package that only has a precondition")
	}
	if _, hit := reopened.GetEntry("net-wireless/mt7927-dkms"); hit {
		t.Error("GetEntry answers for a package that only has a precondition")
	}

	if err := reopened.DeletePrecondition("net-wireless/mt7927-dkms"); err != nil {
		t.Fatalf("DeletePrecondition: %v", err)
	}
	if _, ok := reopened.Precondition("net-wireless/mt7927-dkms"); ok {
		t.Error("the record survived its own deletion")
	}
}

// R3.1 + R3.3 — the record has NO expiry, and that is a decision rather than an
// omission. A clock is no evidence about whether a key file appeared; what ends
// a record is the path becoming readable, which is 3.2's business. A record the
// version TTL swept would resurrect thirteen identical builds a day later.
//
// The old-file row beside it is the deployment case: every cache.json already on
// a user's disk was written before this key existed, and a schema change that
// could not decode one would lose their whole version cache on upgrade.
func TestPreconditionSurvivesTheVersionTTLAndAnOldCacheFile(t *testing.T) {
	t.Run("the version TTL does not reach it", func(t *testing.T) {
		dir := t.TempDir()
		// A cache whose entries are already stale on arrival: TTL of one
		// nanosecond, and a clock that has moved on by the time anything is read.
		cache, err := NewCache(dir, WithTTL(time.Nanosecond))
		if err != nil {
			t.Fatalf("NewCache: %v", err)
		}
		if err := cache.Set("net-wireless/mt7927-dkms", "2.14", "https://example.com"); err != nil {
			t.Fatalf("Set: %v", err)
		}
		if err := cache.SetPrecondition("net-wireless/mt7927-dkms", "/etc/kernel/keys/module-signing.key"); err != nil {
			t.Fatalf("SetPrecondition: %v", err)
		}

		if _, hit := cache.Get("net-wireless/mt7927-dkms"); hit {
			t.Fatal("the version entry did not expire; this test cannot fail for its own reason")
		}
		if _, ok := cache.Precondition("net-wireless/mt7927-dkms"); !ok {
			t.Error("the precondition expired with the version entry — a clock is no evidence about whether a key file appeared")
		}

		// Cleanup sweeps expired VERSION entries. It must not sweep this.
		if err := cache.Cleanup(); err != nil {
			t.Fatalf("Cleanup: %v", err)
		}
		if _, ok := cache.Precondition("net-wireless/mt7927-dkms"); !ok {
			t.Error("Cleanup removed the precondition along with the expired version entry")
		}
	})

	t.Run("a cache.json written before this key still loads", func(t *testing.T) {
		dir := t.TempDir()
		old := `{"entries":{"dev-libs/x":{"version":"1.2.3","timestamp":"2026-08-22T10:00:00Z","source":"https://example.com"}}}`
		if err := os.WriteFile(filepath.Join(dir, "cache.json"), []byte(old), 0o600); err != nil {
			t.Fatalf("writing the old-schema cache: %v", err)
		}

		cache, err := NewCache(dir, WithTTL(24*time.Hour), WithNowFunc(func() time.Time {
			return time.Date(2026, 8, 22, 11, 0, 0, 0, time.UTC)
		}))
		if err != nil {
			t.Fatalf("NewCache over an old-schema file: %v", err)
		}
		if v, ok := cache.Get("dev-libs/x"); !ok || v != "1.2.3" {
			t.Errorf("the pre-existing version entry was lost on upgrade: %q, %v", v, ok)
		}
		if _, ok := cache.Precondition("dev-libs/x"); ok {
			t.Error("a file with no preconditions key produced a record")
		}

		// And the new key can then be written into that same file without
		// disturbing what was already there.
		if err := cache.SetPrecondition("dev-libs/x", "/etc/kernel/keys/module-signing.key"); err != nil {
			t.Fatalf("SetPrecondition over an upgraded file: %v", err)
		}
		reopened, err := NewCache(dir, WithTTL(24*time.Hour), WithNowFunc(func() time.Time {
			return time.Date(2026, 8, 22, 11, 0, 0, 0, time.UTC)
		}))
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if v, ok := reopened.Get("dev-libs/x"); !ok || v != "1.2.3" {
			t.Errorf("writing a precondition dropped the version entry: %q, %v", v, ok)
		}
		if rec, ok := reopened.Precondition("dev-libs/x"); !ok || rec.Path == "" {
			t.Errorf("the precondition did not survive: %+v, %v", rec, ok)
		}
	})
}
