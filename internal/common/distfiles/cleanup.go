package distfiles

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FetchScope is the record of which distfiles a package's manifest step may
// remove if its fetch fails, and the only thing that can authorise a removal.
//
// Only "the incomplete artefact this run created for that fetch" may go. In
// the host's real DISTDIR that cannot be worked out after the fact: sweep
// workers run concurrently, so a file another worker is writing and one this
// run truncated look alike. It is MEASURED BEFORE the fetch instead: expected
// names absent when the step began are the only names a later file can be
// ours under. The set lives in an unexported field only RecordFetchScope
// fills, so no caller can point the removal at another name; the zero value
// removes nothing, the direction every mistake must fail in.
//
//	resolve distdir -> Quarantine -> prepopulate from cache -> RECORD -> pkgdev -> on failure: CleanupFailedFetch
//
// Recording before Quarantine would class a doomed file's name as present and
// leave a truncated refetch behind. Recording before prepopulation would make
// cleanup throw away the verified cache links the reuse exists to keep. The
// per-distfile lock, held across pkgdev, stops a second worker fetching into a
// recorded name between the record and the failure.
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
// moment the manifest step begins. See FetchScope for where in the step it
// belongs: the value is a snapshot of one instant.
//
// Each expected name is recorded only when absent. A present name is never
// recorded, whoever wrote it, which keeps one worker's failure off another's
// file. A name whose inspection failed for any other reason is not recorded
// either: a name we could not read is a name we cannot claim.
//
// The lookup is os.Lstat, not os.Stat: os.Stat reports a dangling cache
// symlink as absent, which would let cleanup unlink a link it never created.
// Every name is reduced with distfileName first and dropped if it does not
// reduce to a filename, since the names are untrusted input; duplicates
// collapse. An empty distdir is refused: filepath.Join("", name) resolves
// against the working directory.
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
// names, so the caller can report them: this package never logs. Call it ONLY
// on the manifest step's failure branch; after a success the same names are
// freshly verified distfiles whose reuse must be kept.
//
// Only a recorded name with something under it now is removed, so the result
// is what actually went away, as bare filenames. A directory is never removed
// (os.Remove would rmdir an empty host subdirectory); a symlink is unlinked,
// never followed, as its target is the read-only cache. Each name is checked
// with distfileName again right before removal, because a name carrying a
// separator would pass the directory check and this is the operation with the
// worst blast radius.
//
// A failed removal does not stop the others; failures are joined into one
// error returned ALONGSIDE the removed names. The zero value and an empty set
// are no-ops, so a defer can call it on any error; an empty set is also the
// normal result when every distfile was already on disk. Names with an empty
// distdir are refused: the removal would resolve against the working directory.
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
