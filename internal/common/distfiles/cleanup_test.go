package distfiles

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// =============================================================================
// Test harness note
// =============================================================================
//
// The filesystem helpers these tests are built on live in quarantine_test.go
// and are shared deliberately: snapshotTree, assertTreeUnchanged,
// withoutSubtree, seedFile, dirEntryNames and lstatEntry. R2.3 is a statement
// about a directory this tool does not own, so "it did the right thing" can
// only be asserted as "every byte, mode and owner that was not ours is still
// exactly what it was" — the same shape 3.1 needed, and re-deriving it here
// would let the two drift.

// assertAbsent fails unless nothing at all exists at path, taken with Lstat so
// a leftover symlink counts as something rather than being followed into
// nothing.
func assertAbsent(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s: %q is still there (Lstat err = %v)", why, path, err)
	}
}

// assertTreeUnchangedApartFromDirSizes is assertTreeUnchanged with exactly one
// field relaxed, and only for directories: their own size.
//
// A directory's size is filesystem bookkeeping about the names it holds — on
// tmpfs, where these tests run, removing one entry changes it — so comparing it
// would make every successful removal look like collateral damage. Nothing else
// is relaxed: a directory's MODE and OWNER are still asserted byte-for-byte,
// because those are what R2.5 is about, and every file in the tree is still
// compared down to its content.
func assertTreeUnchangedApartFromDirSizes(t *testing.T, before map[string]fsEntry, root, why string) {
	t.Helper()
	assertSameTree(t, withoutDirSizes(before), withoutDirSizes(snapshotTree(t, root)), root, why)
}

// withoutDirSizes returns a copy of a snapshot with the size of every directory
// entry zeroed. fsEntry is a value type, so the originals are untouched.
func withoutDirSizes(snap map[string]fsEntry) map[string]fsEntry {
	out := make(map[string]fsEntry, len(snap))
	for name, entry := range snap {
		if entry.mode.IsDir() {
			entry.size = 0
		}
		out[name] = entry
	}
	return out
}

// assertPresent fails unless something exists at path. It is the canary half of
// every removal assertion: a test that checks a file is gone proves nothing
// unless the file was demonstrably there first, and "the fetch never wrote it"
// is a perfectly plausible way for these tests to pass while asserting nothing.
func assertPresent(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("%s: %q is not there before the call, so the test is not testing what it claims: %v", why, path, err)
	}
}

// =============================================================================
// R2.3 — only what this run created
// =============================================================================

// TestFailedFetchRemovesOnlyNewlyCreatedArtefacts is the whole of R2.3 in one
// run: the truncated file this run's fetch produced goes away, and the three
// kinds of file that were never ours — one already-verified distfile this
// package expected, one belonging to another package entirely, and one another
// worker started AFTER the snapshot — are all still byte-identical afterwards.
//
// The last of those is the case the shared directory introduced. The per-run
// temporary directory this replaces made the rule free (everything in it was
// ours); in the host's DISTDIR, with a sweep running its packages concurrently
// (sweep.go:620), a file another worker is midway through writing sits right
// next to ours and looks exactly like it.
func TestFailedFetchRemovesOnlyNewlyCreatedArtefacts(t *testing.T) {
	distdir := t.TempDir()

	// Already on disk when the manifest step begins.
	seedFile(t, distdir, "already-there-1.0.tar.xz", "a complete, previously verified download", 0o644)
	seedFile(t, distdir, "another-package-2.0.tar.xz", "not this package's business at all", 0o640)

	// What the new version expects. The repeated name is deliberate: pkgdev
	// manifests a whole directory, so runManifest passes the distfiles of every
	// version left behind and the same name can arrive twice.
	expected := []string{"new-3.0.tar.xz", "already-there-1.0.tar.xz", "new-3.0.tar.xz"}

	scope, err := RecordFetchScope(distdir, expected)
	if err != nil {
		t.Fatalf("RecordFetchScope(%q, %v) error = %v", distdir, expected, err)
	}
	if want := []string{"new-3.0.tar.xz"}; !slices.Equal(scope.created, want) {
		t.Fatalf("recorded %v, want %v: only a name that was ABSENT when the step began can be this run's, and it may only be recorded once", scope.created, want)
	}

	// The fetch runs. It writes a truncated file under the final name — that is
	// what portage's FETCHCOMMAND does when it is killed midway — and, at the
	// same time, another worker starts a download of its own.
	seedFile(t, distdir, "new-3.0.tar.xz", "\x00\x00 half a tarball", 0o644)
	seedFile(t, distdir, "concurrent-4.0.tar.xz", "another worker's fetch, still in flight", 0o644)

	artefact := filepath.Join(distdir, "new-3.0.tar.xz")
	assertPresent(t, artefact, "the artefact this run created")

	before := snapshotTree(t, distdir)

	removed, err := scope.CleanupFailedFetch()
	if err != nil {
		t.Fatalf("CleanupFailedFetch() error = %v", err)
	}

	if want := []string{"new-3.0.tar.xz"}; !slices.Equal(removed, want) {
		t.Errorf("removed %v, want %v (directory now holds %v)", removed, want, dirEntryNames(t, distdir))
	}
	assertAbsent(t, artefact, "the truncated artefact this run created")

	// Everything else, to the byte, the mode and the owner. Dropping the one
	// key that is meant to change turns the rest of the directory into the
	// assertion, so a file removed that nobody named still fails this test.
	delete(before, "new-3.0.tar.xz")
	assertTreeUnchangedApartFromDirSizes(t, before, distdir, "cleanup after a failed fetch")
}

// TestFailedFetchLeavesPreexistingFilesUntouched is R2.3's prohibition, which
// is the half with the blast radius: removing a file this run did not create
// destroys somebody else's download in a directory holding every distfile the
// machine has ever fetched, and nothing undoes it.
//
// "Pre-existing" is decided once, at the snapshot, and nothing that happens
// afterwards promotes a name into the removable set — not the file changing,
// not the file being exactly one of the names this package expects.
func TestFailedFetchLeavesPreexistingFilesUntouched(t *testing.T) {
	t.Run("a file already on disk is never a candidate, even when it changes mid-run", func(t *testing.T) {
		distdir := t.TempDir()
		seedFile(t, distdir, "reused-1.0.tar.xz", "verified, and reused instead of downloaded", 0o644)
		seedFile(t, distdir, "refetched-2.0.tar.xz", "an older copy", 0o644)

		expected := []string{"reused-1.0.tar.xz", "refetched-2.0.tar.xz"}
		scope, err := RecordFetchScope(distdir, expected)
		if err != nil {
			t.Fatalf("RecordFetchScope error = %v", err)
		}
		if len(scope.created) != 0 {
			t.Fatalf("recorded %v: every expected distfile was already on disk, so this run created nothing", scope.created)
		}

		// Another writer replaces one of them while pkgdev runs — portage
		// refetching it, or a second bentoo run. Having changed since the
		// snapshot is not a licence to delete it.
		seedFile(t, distdir, "refetched-2.0.tar.xz", "a fresh copy written by somebody else", 0o644)

		before := snapshotTree(t, distdir)

		removed, err := scope.CleanupFailedFetch()
		if err != nil {
			t.Fatalf("CleanupFailedFetch() error = %v", err)
		}
		if len(removed) != 0 {
			t.Errorf("removed %v: not one of those files was created by this run", removed)
		}
		assertTreeUnchanged(t, before, distdir, "cleanup with nothing of its own to remove")
	})

	t.Run("a dangling symlink counts as present, not as absent", func(t *testing.T) {
		// PrepopulateFromCache fills this directory with symlinks into a
		// read-only cache, and one whose target has since gone is exactly the
		// entry os.Stat calls "does not exist". Recorded on the strength of
		// that answer, the link would be unlinked by a cleanup that never
		// created it. os.Lstat is what keeps the two apart.
		distdir := t.TempDir()
		link := filepath.Join(distdir, "dangling-1.0.tar.xz")
		if err := os.Symlink(filepath.Join(t.TempDir(), "target-that-is-not-there"), link); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		scope, err := RecordFetchScope(distdir, []string{"dangling-1.0.tar.xz"})
		if err != nil {
			t.Fatalf("RecordFetchScope error = %v", err)
		}
		if len(scope.created) != 0 {
			t.Fatalf("recorded %v: a dangling symlink is something somebody put there, not an absence", scope.created)
		}

		before := snapshotTree(t, distdir)
		removed, err := scope.CleanupFailedFetch()
		if err != nil {
			t.Fatalf("CleanupFailedFetch() error = %v", err)
		}
		if len(removed) != 0 {
			t.Errorf("removed %v: the link was there before this run started", removed)
		}
		assertTreeUnchanged(t, before, distdir, "cleanup over a pre-existing dangling symlink")
	})

	t.Run("a symlink prepopulated before the snapshot is not this run's artefact", func(t *testing.T) {
		// The recommended ordering: prepopulate, then record. The link is
		// present at the snapshot, so it is not a candidate, and a failed fetch
		// leaves the reuse R2.1 exists to keep exactly where it was.
		cache := t.TempDir()
		seedFile(t, cache, "cached-1.0.tar.xz", "the verified copy in the read-only cache", 0o644)
		distdir := t.TempDir()

		if reused := PrepopulateFromCache(distdir, cache, []string{"cached-1.0.tar.xz"}); reused != 1 {
			t.Fatalf("PrepopulateFromCache linked %d files, want 1", reused)
		}

		scope, err := RecordFetchScope(distdir, []string{"cached-1.0.tar.xz"})
		if err != nil {
			t.Fatalf("RecordFetchScope error = %v", err)
		}
		if len(scope.created) != 0 {
			t.Fatalf("recorded %v: the link was already in place when the snapshot was taken", scope.created)
		}

		beforeDist := snapshotTree(t, distdir)
		beforeCache := snapshotTree(t, cache)

		if removed, err := scope.CleanupFailedFetch(); err != nil || len(removed) != 0 {
			t.Errorf("CleanupFailedFetch() = %v, %v; want nothing removed and no error", removed, err)
		}
		assertTreeUnchanged(t, beforeDist, distdir, "cleanup over a prepopulated link")
		assertTreeUnchanged(t, beforeCache, cache, "cleanup must not reach into the read-only cache")
	})
}

// TestFailedFetchOnAnEmptyCreatedSetRemovesNothing pins the states this
// function is reached in when things went wrong early. It runs on a failure
// path, often from a defer, so "the scope was never recorded" and "the scope
// recorded nothing" both have to be quiet no-ops rather than panics — and both
// have to remove nothing, which is the only outcome that is safe when the
// record is missing.
func TestFailedFetchOnAnEmptyCreatedSetRemovesNothing(t *testing.T) {
	t.Run("the zero scope, reached from a defer after an early error", func(t *testing.T) {
		// Nothing was resolved, nothing was recorded: the manifest step failed
		// before it got that far. The working directory stands in for
		// "wherever an unguarded filepath.Join would have pointed".
		work := t.TempDir()
		t.Chdir(work)
		seedFile(t, work, "innocent-1.0.tar.xz", "a file in the working directory", 0o644)
		before := snapshotTree(t, work)

		var scope FetchScope
		removed, err := scope.CleanupFailedFetch()
		if err != nil {
			t.Errorf("CleanupFailedFetch() on the zero value error = %v; a defer that fires on any failure must not be punished for one that happened earlier", err)
		}
		if len(removed) != 0 {
			t.Errorf("removed %v from a scope that recorded nothing", removed)
		}
		assertTreeUnchanged(t, before, work, "cleanup on the zero scope")
	})

	t.Run("a scope that recorded nothing because everything was already on disk", func(t *testing.T) {
		distdir := t.TempDir()
		seedFile(t, distdir, "reused-1.0.tar.xz", "already here", 0o644)

		scope, err := RecordFetchScope(distdir, []string{"reused-1.0.tar.xz"})
		if err != nil {
			t.Fatalf("RecordFetchScope error = %v", err)
		}
		before := snapshotTree(t, distdir)

		removed, err := scope.CleanupFailedFetch()
		if err != nil || len(removed) != 0 {
			t.Errorf("CleanupFailedFetch() = %v, %v; want nothing removed and no error", removed, err)
		}
		assertTreeUnchanged(t, before, distdir, "cleanup on an empty recorded set")
	})

	t.Run("a scope recorded from an empty expected list", func(t *testing.T) {
		distdir := t.TempDir()
		seedFile(t, distdir, "someone-elses-1.0.tar.xz", "not expected by anybody here", 0o644)

		scope, err := RecordFetchScope(distdir, nil)
		if err != nil {
			t.Fatalf("RecordFetchScope(%q, nil) error = %v", distdir, err)
		}
		before := snapshotTree(t, distdir)

		removed, err := scope.CleanupFailedFetch()
		if err != nil || len(removed) != 0 {
			t.Errorf("CleanupFailedFetch() = %v, %v; want nothing removed and no error", removed, err)
		}
		assertTreeUnchanged(t, before, distdir, "cleanup for a package with no distfiles")
	})

	t.Run("an empty distdir is refused at both ends", func(t *testing.T) {
		// RecordFetchScope cannot produce a set without a directory, so the
		// only way to reach that state is to build one — and the state has to
		// be refused rather than acted on, because filepath.Join("", name)
		// deletes relative to the WORKING directory.
		if _, err := RecordFetchScope("", []string{"anything-1.0.tar.xz"}); err == nil {
			t.Error("RecordFetchScope(\"\", ...) returned no error; a set recorded against the working directory describes files nobody asked about")
		}

		work := t.TempDir()
		t.Chdir(work)
		seedFile(t, work, "innocent-1.0.tar.xz", "a file in the working directory", 0o644)
		before := snapshotTree(t, work)

		orphan := FetchScope{created: []string{"innocent-1.0.tar.xz"}}
		removed, err := orphan.CleanupFailedFetch()
		if err == nil {
			t.Error("CleanupFailedFetch() with names but no distdir returned no error")
		}
		if len(removed) != 0 {
			t.Errorf("removed %v with no distdir resolved", removed)
		}
		assertTreeUnchanged(t, before, work, "cleanup with names but no distdir")
	})
}

// =============================================================================
// Ordering — the snapshot belongs after the quarantine
// =============================================================================

// TestFailedFetchIsRecordedAfterQuarantineSoTheRefetchIsOurs makes the ordering
// decision executable rather than a comment, because it is a decision the
// caller (5.1) has to honour and nothing in the signatures enforces it.
//
// Quarantine moves an unverifiable file aside precisely so pkgdev fetches
// cleanly, which makes that name absent — so the file appearing under it next
// is this run's download and is ours to remove. Recording first sees the doomed
// file still in place, calls the name pre-existing, and then refuses to clean up
// the truncated refetch: a partial file left under a name the Manifest does not
// list, which is the R2.2 hazard Quarantine exists to prevent, reintroduced one
// step later. The second subtest holds that consequence still, so the ordering
// cannot be reversed on the strength of it looking harmless.
func TestFailedFetchIsRecordedAfterQuarantineSoTheRefetchIsOurs(t *testing.T) {
	const name = "bumped-2.0.tar.xz"

	t.Run("recorded after the quarantine: the refetch is cleaned up", func(t *testing.T) {
		distdir := t.TempDir()
		seedFile(t, distdir, name, "truncated by a killed fetch, and unlisted", 0o644)

		// manifestNames is empty: on a bump the current Manifest does not list
		// the new version's distfile, which is what makes it unverifiable.
		moved, err := Quarantine(distdir, nil, []string{name})
		if err != nil {
			t.Fatalf("Quarantine error = %v", err)
		}
		if len(moved) != 1 {
			t.Fatalf("Quarantine moved %v, want exactly one file aside", moved)
		}

		scope, err := RecordFetchScope(distdir, []string{name})
		if err != nil {
			t.Fatalf("RecordFetchScope error = %v", err)
		}
		if want := []string{name}; !slices.Equal(scope.created, want) {
			t.Fatalf("recorded %v, want %v: the quarantine made the name absent, so what lands there next is this run's", scope.created, want)
		}

		// pkgdev refetches, and is killed again.
		seedFile(t, distdir, name, "\x00 truncated a second time", 0o644)
		before := snapshotTree(t, distdir)

		removed, err := scope.CleanupFailedFetch()
		if err != nil {
			t.Fatalf("CleanupFailedFetch() error = %v", err)
		}
		if want := []string{name}; !slices.Equal(removed, want) {
			t.Errorf("removed %v, want %v (directory holds %v)", removed, want, dirEntryNames(t, distdir))
		}
		assertAbsent(t, filepath.Join(distdir, name), "the refetched partial file")

		// The quarantined evidence is untouched: cleanup removes the artefact,
		// not the record of what was moved aside.
		delete(before, name)
		assertTreeUnchangedApartFromDirSizes(t, before, distdir, "cleanup after a quarantine")
	})

	t.Run("recorded before the quarantine, the partial file survives", func(t *testing.T) {
		distdir := t.TempDir()
		seedFile(t, distdir, name, "truncated by a killed fetch, and unlisted", 0o644)

		early, err := RecordFetchScope(distdir, []string{name})
		if err != nil {
			t.Fatalf("RecordFetchScope error = %v", err)
		}
		if len(early.created) != 0 {
			t.Fatalf("recorded %v: at this instant the name is occupied, so the snapshot cannot claim it", early.created)
		}

		if _, err := Quarantine(distdir, nil, []string{name}); err != nil {
			t.Fatalf("Quarantine error = %v", err)
		}
		seedFile(t, distdir, name, "\x00 truncated a second time", 0o644)

		removed, err := early.CleanupFailedFetch()
		if err != nil {
			t.Fatalf("CleanupFailedFetch() error = %v", err)
		}
		if len(removed) != 0 {
			t.Errorf("removed %v from a snapshot taken before the quarantine; if this ever passes, the ordering rule in FetchScope's doc is no longer the reason for the ordering", removed)
		}
		assertPresent(t, filepath.Join(distdir, name),
			"the partial file a too-early snapshot leaves behind (this is the hazard the ordering avoids, asserted so it cannot be waved away)")
	})
}

// =============================================================================
// Symlinks, directories, and names that are not names
// =============================================================================

// TestFailedFetchUnlinksTheSymlinkAndNeverItsTarget pins the operation, not
// just the outcome. If prepopulation runs after the snapshot, the recorded name
// holds a symlink into the read-only portage cache; removing it must unlink the
// LINK. A removal that resolved the path first would delete a verified distfile
// out of a cache this tool does not own — the same distinction 3.1 made with
// Lstat and Rename.
func TestFailedFetchUnlinksTheSymlinkAndNeverItsTarget(t *testing.T) {
	cache := t.TempDir()
	seedFile(t, cache, "cached-1.0.tar.xz", "the verified copy in the read-only cache", 0o644)
	distdir := t.TempDir()

	scope, err := RecordFetchScope(distdir, []string{"cached-1.0.tar.xz"})
	if err != nil {
		t.Fatalf("RecordFetchScope error = %v", err)
	}
	if len(scope.created) != 1 {
		t.Fatalf("recorded %v, want the one absent name", scope.created)
	}

	// Prepopulation lands after the snapshot, so the link is inside the
	// recorded set.
	if reused := PrepopulateFromCache(distdir, cache, []string{"cached-1.0.tar.xz"}); reused != 1 {
		t.Fatalf("PrepopulateFromCache linked %d files, want 1", reused)
	}
	link := filepath.Join(distdir, "cached-1.0.tar.xz")
	assertPresent(t, link, "the prepopulated link")

	beforeCache := snapshotTree(t, cache)

	removed, err := scope.CleanupFailedFetch()
	if err != nil {
		t.Fatalf("CleanupFailedFetch() error = %v", err)
	}
	if want := []string{"cached-1.0.tar.xz"}; !slices.Equal(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	assertAbsent(t, link, "the link this run created")
	assertTreeUnchanged(t, beforeCache, cache, "removing a link must not touch the file it points at")
}

// TestFailedFetchReportsWhatItCouldNotRemoveAndKeepsGoing covers the two halves
// of the failure contract at once.
//
// A directory is never removed: os.Remove falls back to rmdir when unlink says
// EISDIR, so an empty host-owned subdirectory that appeared under an expected
// name would otherwise be taken away by a cleanup with no business doing it. A
// fetch produces files, never directories.
//
// And that refusal does not stop the sweep of the rest of the set, nor is it
// swallowed: the artefacts on either side of it are still removed, and the
// refusal comes back as an error naming the entry, alongside the names that
// were removed. Leaving two real artefacts behind over one entry that could not
// be touched helps nobody; neither does deleting them and saying nothing.
func TestFailedFetchReportsWhatItCouldNotRemoveAndKeepsGoing(t *testing.T) {
	distdir := t.TempDir()

	expected := []string{"first-1.0.tar.xz", "occupied-2.0.tar.xz", "last-3.0.tar.xz"}
	scope, err := RecordFetchScope(distdir, expected)
	if err != nil {
		t.Fatalf("RecordFetchScope error = %v", err)
	}
	if !slices.Equal(scope.created, expected) {
		t.Fatalf("recorded %v, want %v: the directory was empty, so every expected name is absent", scope.created, expected)
	}

	seedFile(t, distdir, "first-1.0.tar.xz", "\x00 partial", 0o644)
	seedFile(t, distdir, "last-3.0.tar.xz", "\x00 partial", 0o644)
	// An EMPTY directory, because that is the case os.Remove would succeed on.
	occupied := filepath.Join(distdir, "occupied-2.0.tar.xz")
	if err := os.Mkdir(occupied, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	before := snapshotTree(t, distdir)

	removed, err := scope.CleanupFailedFetch()
	if err == nil {
		t.Fatal("CleanupFailedFetch() returned no error; the entry it refused to remove must not be swallowed — the caller is writing the failure report")
	}
	if !strings.Contains(err.Error(), "occupied-2.0.tar.xz") {
		t.Errorf("error %q does not name the entry that was refused; the report cannot say what was left behind", err)
	}
	if want := []string{"first-1.0.tar.xz", "last-3.0.tar.xz"}; !slices.Equal(removed, want) {
		t.Errorf("removed %v, want %v: an entry that cannot be removed must not abort the rest, and must not be reported as removed", removed, want)
	}

	if info, statErr := os.Lstat(occupied); statErr != nil || !info.IsDir() {
		t.Errorf("the directory under an expected name is gone or changed (err = %v): a fetch does not create directories, so this one is the host's", statErr)
	}
	delete(before, "first-1.0.tar.xz")
	delete(before, "last-3.0.tar.xz")
	assertTreeUnchangedApartFromDirSizes(t, before, distdir, "cleanup that had to refuse one entry")
}

// TestFailedFetchNeverRemovesOutsideTheDistdir states the security property as
// an outcome: whatever an expected name looks like, nothing outside the distdir
// is removed and the distdir itself survives.
//
// The names reaching RecordFetchScope come from a resolved version, so they are
// untrusted, and this directory is shared with the host's package manager.
// filepath.Base neutralises the classic traversal by construction —
// "../victim" becomes "victim", which names a file INSIDE the distdir — but
// Base alone is not enough: it returns "." and ".." unchanged, and
// filepath.Join(distdir, "..") is the distdir's PARENT, which under the default
// distdir is /var/cache.
//
// As in 3.1, the lexical refusal and the second guard behind it overlap for
// most of these names, and measurement says so: every name Base leaves as ".",
// ".." or "/" joins to a directory that EXISTS, so the snapshot skips it as
// present and the removal skips it as a directory even with the lexical guard
// taken out. The one input on Linux that separates them is a name carrying a
// backslash — a perfectly legal filename here, which Base returns unchanged and
// distfileName refuses, and which therefore proves the guard is doing something
// no other check does. It is pinned below for that reason.
//
// The guards are still not the same guard. The lexical one runs before any
// syscall, so it has no window for a racing mkdir or rename to slip through,
// and it is the only one that runs at the moment the name is RECORDED — which
// is what keeps a bad name out of the set in the first place, rather than
// catching it with an unlink already loaded.
func TestFailedFetchNeverRemovesOutsideTheDistdir(t *testing.T) {
	parent := t.TempDir()
	distdir := filepath.Join(parent, "distdir")
	if err := os.Mkdir(distdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seedFile(t, parent, "victim", "a file one level above the distdir", 0o644)

	hostile := []string{"../victim", "..", ".", "/", "", `weird\name.tar.xz`, "sub/../../victim", "../../etc/passwd"}
	scope, err := RecordFetchScope(distdir, hostile)
	if err != nil {
		t.Fatalf("RecordFetchScope error = %v", err)
	}
	// Only the two Base-reduced filenames may survive, deduplicated, and both
	// of them name entries inside the distdir. The backslash name is the one
	// that would exist on disk if it were let through, so it is the one that
	// shows the lexical refusal is load-bearing rather than decorative.
	if want := []string{"victim", "passwd"}; !slices.Equal(scope.created, want) {
		t.Fatalf("recorded %v, want %v: '.', '..', '/', '' and a name carrying a separator are not filenames this package will act on, and must never enter the set", scope.created, want)
	}

	// The "fetch" writes under the reduced names, inside the distdir.
	seedFile(t, distdir, "victim", "\x00 partial, and genuinely ours", 0o644)

	before := snapshotTree(t, parent)

	removed, err := scope.CleanupFailedFetch()
	if err != nil {
		t.Fatalf("CleanupFailedFetch() error = %v", err)
	}
	if want := []string{"victim"}; !slices.Equal(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}

	delete(before, filepath.Join("distdir", "victim"))
	assertTreeUnchangedApartFromDirSizes(t, before, parent, "cleanup handed traversal names")

	t.Run("even a set poisoned past the recorder cannot leave the directory", func(t *testing.T) {
		// RecordFetchScope cannot produce these, since it reduces every name
		// first. The removal re-checks anyway: it is the operation with the
		// worst blast radius, and this is the last thing between a future edit
		// and an unlink the recorder never sanctioned.
		//
		// The backslash entry is the one with a real file behind it, so this
		// subtest asserts an unlink that did NOT happen rather than only an
		// error string: the other three resolve to directories and would be
		// stopped by the IsDir branch even without the check under test.
		seedFile(t, distdir, `weird\name.tar.xz`, "a legal Linux filename this package will not act on", 0o644)
		poisoned := FetchScope{distdir: distdir, created: []string{"..", ".", "/", `weird\name.tar.xz`, "sub/../victim"}}
		before := snapshotTree(t, parent)

		removed, err := poisoned.CleanupFailedFetch()
		if err == nil {
			t.Error("CleanupFailedFetch() accepted names that are not filenames without a word")
		}
		if len(removed) != 0 {
			t.Errorf("removed %v from a poisoned set", removed)
		}
		if _, statErr := os.Lstat(distdir); statErr != nil {
			t.Fatalf("the distdir itself is gone: %v", statErr)
		}
		assertTreeUnchanged(t, before, parent, "cleanup over a poisoned set")
	})
}
