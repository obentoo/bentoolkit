package distfiles

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FetchScope is the record of which distfiles a package's manifest step may
// remove if its fetch fails, and it is the only thing that can authorise such a
// removal.
//
// # Why the set is recorded and not computed
//
// R2.3 licenses removing "the incomplete artefact this run created for that
// fetch", and nothing else. In the per-run temporary directory this story
// replaces, that rule was free: everything in the directory was ours by
// construction, so `rm -rf` on it was correct. In the host's real DISTDIR the
// same rule cannot be worked out after the fact — at cleanup time a file
// another worker is midway through writing and a file this run truncated look
// exactly alike, and a sweep runs its packages concurrently
// (internal/autoupdate/sweep.go:620), so both are genuinely present at once.
//
// "What this run created" therefore has to be MEASURED BEFORE the fetch: the
// expected names that were absent when the step began are the only names a file
// appearing later can belong to. Everything else in that directory belongs to
// somebody — another worker, another bentoo run, or the host's own package
// manager — and R2.3 says it is not ours to delete.
//
// # The shape, and what it makes impossible
//
// The set lives in an unexported field that only RecordFetchScope fills, and
// the removal is a method on the value carrying it. There is deliberately no
// exported function that takes a list of files and deletes them, so a caller
// cannot point the removal at a name the snapshot never produced — not by
// mistake, and not by passing the wrong slice.
//
// A caller that skips the snapshot altogether holds the zero FetchScope, whose
// cleanup removes nothing. That is the direction every mistake here has to fail
// in. Under-removing leaves a partial file behind, and the next run's
// Quarantine moves it aside before anything digests it (R2.2); over-removing
// deletes a file belonging to another worker or to the host, which nothing
// undoes.
//
// # Where this goes in the manifest step, and why the order is not free
//
//	resolve distdir -> Quarantine -> prepopulate from cache -> RECORD -> pkgdev -> on failure: CleanupFailedFetch
//
// AFTER Quarantine, not before. Quarantine's whole job is to move an
// unverifiable file out of the way so pkgdev fetches cleanly, which makes that
// name absent — and the file that appears under it next is this run's download.
// Recording first would see the doomed file still sitting there, class the name
// as pre-existing, and then refuse to clean up the truncated refetch: a partial
// file left under a name the Manifest does not list, which is exactly the R2.2
// hazard Quarantine exists to prevent, reintroduced one step later.
//
// AFTER prepopulation, not before. PrepopulateFromCache symlinks verified cache
// files into this directory under the very names being recorded. A link this
// run put there on purpose, pointing at a file something has already vouched
// for, is not an incomplete artefact of a failed fetch; recording it as our
// creation would have cleanup throw away the reuse R2.1 exists to keep. Doing
// it the other way round would still be SAFE — a removal unlinks the link and
// never the read-only file it points at — but safe is not the same as right.
//
// The claim "absent after our own quarantine, therefore ours" is only as strong
// as the per-distfile lock of D4 (R2.4): without it a second worker can fetch
// into that name between the record and the failure. The lock is held for the
// duration of the pkgdev invocation for exactly this reason.
type FetchScope struct {
	// distdir is the resolved directory the recorded names live in. It is kept
	// with the set rather than passed to the removal, so the two halves cannot
	// be given different directories.
	distdir string
	// created holds the reduced, deduplicated names that were absent when the
	// snapshot was taken — the complete list of what this run is allowed to
	// remove. Unexported so that nothing outside this file can add to it.
	created []string
}

// RecordFetchScope takes the measurement CleanupFailedFetch later spends: which
// of the distfile names the new version expects are NOT in distdir at the
// moment the manifest step begins.
//
// It is named Record rather than New because the value is a snapshot of one
// instant and calling it at the wrong moment is the only way to get a wrong
// answer — see FetchScope for where in the step it belongs.
//
// Each expected name lands in one of three states, and the caution runs the
// OPPOSITE way from Quarantine's:
//
//   - absent — recorded. A file that appears under this name later is this
//     run's, because nothing was there when the run started.
//   - present — not recorded, whoever wrote it. This is R2.3's whole rule, and
//     it is what keeps one worker's failure off another worker's file.
//   - unknowable, an inspection that failed for any reason other than the file
//     not existing — not recorded. Quarantine fails closed by treating an
//     unreadable entry as something that must be dealt with; here the same
//     caution points the other way, because the outcome to avoid is deleting a
//     file we did not create. A name we could not read is a name we cannot
//     claim.
//
// The lookup is os.Lstat rather than os.Stat, and that is load-bearing:
// PrepopulateFromCache leaves symlinks in this directory, and os.Stat reports a
// DANGLING one as "does not exist". Under Stat such a link would be recorded as
// absent and then unlinked by a cleanup that never created it — precisely the
// R2.3 violation this function exists to make impossible.
//
// Every name is reduced with distfileName before it is looked up, and one that
// does not reduce to a filename is dropped rather than recorded: the expected
// names come from a resolved version, so they are untrusted input, and a name
// that never enters the set can never reach os.Remove. Duplicates collapse, so
// one file cannot be reported as removed twice.
//
// An empty distdir is refused, as in Probe and Quarantine: filepath.Join("",
// name) resolves against the WORKING directory, so a set recorded there would
// describe files nobody asked about — and would later authorise deleting them.
func RecordFetchScope(distdir string, expected []string) (FetchScope, error) {
	if distdir == "" {
		return FetchScope{}, errors.New("cannot record what a fetch creates: no distdir was resolved")
	}

	scope := FetchScope{distdir: distdir}
	seen := make(map[string]struct{}, len(expected))
	for _, raw := range expected {
		name, ok := distfileName(raw)
		if !ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}

		_, err := os.Lstat(filepath.Join(distdir, name))
		if err == nil {
			// Present before pkgdev ran. Not ours, whoever put it there.
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			// "We could not find out" is not "there is nothing there", and the
			// safe reading here is the pessimistic one: leave it alone.
			continue
		}
		scope.created = append(scope.created, name)
	}
	return scope, nil
}

// CleanupFailedFetch removes the artefacts this run created and returns their
// names. It is R2.3's other half, and the names come back so the caller can
// report what it cleaned up — this package is a library and never logs.
//
// It must be called ONLY from the failure branch of the manifest step. After a
// success those same names are the distfiles pkgdev has just fetched and
// digested, and removing them would delete verified files out of the host's
// DISTDIR and throw away the reuse R2.1 exists to keep. The name says "failed"
// because there is no way for the type to enforce that.
//
// A recorded name is removed only if something exists under it now; a name that
// never materialised is simply skipped, which is the ordinary case when the
// fetch failed before it reached that file. The returned slice is therefore the
// artefacts that actually went away, not the set that was recorded, and it is
// bare filenames — the caller joins them to the distdir if it needs paths, as
// with Quarantine.
//
// # What is refused, and why
//
// A directory is never removed. os.Remove falls back to rmdir when unlink
// reports EISDIR, so without this check an empty host-owned subdirectory that
// happened to appear under an expected name would be taken away by a cleanup
// with no business doing it — and a distdir does hold host subdirectories
// (git3-src and the like). A fetch produces files, never directories.
//
// A symlink is unlinked, never followed: os.Remove unlinks the LINK, leaving
// the file it points at untouched. That is the same choice Quarantine makes
// with Lstat and Rename, for the same reason — the links in this directory come
// from PrepopulateFromCache and point into a read-only cache that is not ours.
//
// A recorded name is re-checked with distfileName immediately before the
// removal. Nothing can put a bad name in the set today, since only
// RecordFetchScope fills it and that reduces every name first; the check is
// here because this is the operation with the worst blast radius, and a lexical
// test costing one call is the last thing standing between a future edit and an
// unlink the recorder never sanctioned. It is not merely belt and braces
// either: "." , ".." and "/" would also be caught by the directory branch
// above, but a name carrying a separator would not — that is a legal filename
// on this platform, and refusing it is the invariant distfileName exists to
// hold.
//
// # Failures
//
// A removal that fails — a permission, a race with another cleanup, a directory
// where a file was expected — does not abort the rest: the remaining names are
// still processed, because leaving four artefacts behind over one that could
// not be removed helps nobody. Nor is it swallowed. Each failure is collected
// and the whole lot is returned as one joined error ALONGSIDE the names that
// were removed, so the caller — already reporting a failed package — can say
// both what it cleaned up and what it could not.
//
// # Empty and zero states
//
// Safe on the zero value, and safe on a scope that recorded nothing. A step
// that failed before the scope was ever recorded leaves a FetchScope with an
// empty set, and cleaning that up is a no-op rather than a panic or an error,
// so it can be reached from a defer that fires on any error. An empty set is
// also the legitimate outcome when every expected distfile was already on disk
// (R2.1's reuse): this run created nothing, so there is nothing to remove.
//
// A set that holds names while the directory is empty is the one combination
// that is refused outright. RecordFetchScope cannot produce it, and it is the
// dangerous shape — filepath.Join("", name) would delete relative to the
// working directory.
func (s FetchScope) CleanupFailedFetch() ([]string, error) {
	if len(s.created) == 0 {
		return nil, nil
	}
	if s.distdir == "" {
		return nil, fmt.Errorf("refusing to clean up %d recorded distfile(s): no distdir was resolved", len(s.created))
	}

	var removed []string
	var problems []error
	for _, name := range s.created {
		safe, ok := distfileName(name)
		if !ok {
			problems = append(problems, fmt.Errorf("refusing to remove %q: not a single filename", name))
			continue
		}

		path := filepath.Join(s.distdir, safe)
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Nothing was created under this name — the fetch never got
				// this far, or another cleanup has already been through.
				continue
			}
			problems = append(problems, fmt.Errorf("failed to inspect distfile %q in %s: %w", safe, s.distdir, err))
			continue
		}
		if info.IsDir() {
			problems = append(problems, fmt.Errorf("refusing to remove %q from %s: it is a directory, and a fetch does not create directories", safe, s.distdir))
			continue
		}

		if err := os.Remove(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// It went away between the Lstat and the Remove. The outcome
				// this call wanted is the outcome on disk, so it is not a
				// failure — but it was not removed BY US, so it is not reported
				// as removed either.
				continue
			}
			problems = append(problems, fmt.Errorf("failed to remove incomplete distfile %q from %s: %w", safe, s.distdir, err))
			continue
		}
		removed = append(removed, safe)
	}
	return removed, errors.Join(problems...)
}
