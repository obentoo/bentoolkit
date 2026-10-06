package distfiles

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// quarantineInfix is the marker every quarantined name carries, and it is doing
// three jobs at once.
//
// It cannot collide with a real distfile: distfile names are built from a
// package name, a version and an archive suffix, and none of them contains this
// string. It states its own provenance: whoever finds one of these in
// /var/cache/distfiles months later reads "bentoo" and "quarantine" out of the
// filename itself, with no log to correlate against. And because it is a
// SUFFIX, the original filename survives as the prefix — the record of what was
// moved aside travels with the file rather than only in the report.
//
// Portage does the same thing for the same reason: a distfile whose checksum
// fails is renamed to <name>._checksum_failure_.<random>, and one of those was
// sitting in this host's DISTDIR while this was written.
const quarantineInfix = "._bentoo_quarantine_."

// quarantineStamp is the timestamp layout appended after the infix. UTC and
// lexicographically sortable, so a directory listing puts the quarantines in
// the order they happened, and an operator can tell at a glance whether one is
// from this run or from last month. Second resolution is enough because the
// clock is here for the audit trail — it is the PID and the counter below that
// keep two names apart.
const quarantineStamp = "20060102T150405Z"

// quarantineSeq numbers the quarantines this process performs, for exactly the
// reason probeSeq numbers the probes: the PID separates two RUNS and says
// nothing about two goroutines inside one, and a sweep runs its packages in
// parallel. Two workers quarantining within the same second would otherwise
// compute the same name, and os.Rename replaces its destination silently — one
// worker's evidence would overwrite the other's.
var quarantineSeq atomic.Uint64

// Quarantine runs before pkgdev: every distfile the new version expects is
// looked at in the resolved distdir, and one that is present but cannot be
// verified is moved aside instead of being reused. Portage's default
// FETCHCOMMAND writes straight to the final name, so a killed fetch leaves a
// TRUNCATED file there; on a bump the current Manifest does not list it, and
// the next run would digest it and the overlay publish that checksum.
//
// An absent name needs nothing. A present name listed in manifestNames (the
// DIST filenames of the CURRENT Manifest) is reused as is. A present unlisted
// name is renamed to a sibling quarantine name and reported: moved, not
// deleted, because the host's directory is not ours and a move is recoverable.
// It returns each file's NEW name (the original is its prefix). On an error the
// caller must not run pkgdev; what was moved still comes back. A failed
// inspection fails closed rather than meaning absent.
//
// Names are untrusted and reduced with distfileName (Base, then a lexical
// refusal of "..", "." and "/") before joining. Lookup is os.Lstat and the move
// os.Rename, so symlinks, dangling ones included, are moved, never followed;
// directories are left alone. A concurrent rename's loser sees ENOENT.
func Quarantine(distdir string, manifestNames, expected []string) ([]string, error) {
	if distdir == "" {
		// filepath.Join("", name) resolves against the WORKING directory, so a
		// missing distdir would have this function inspecting — and renaming —
		// files somewhere nobody asked about. Probe refuses an empty path for
		// the same reason.
		return nil, errors.New("cannot quarantine distfiles: no distdir was resolved")
	}

	verified := make(map[string]struct{}, len(manifestNames))
	for _, raw := range manifestNames {
		if name, ok := distfileName(raw); ok {
			verified[name] = struct{}{}
		}
	}

	var moved []string
	for _, raw := range expected {
		name, ok := distfileName(raw)
		if !ok {
			continue
		}
		if _, listed := verified[name]; listed {
			// Already verified, so leave it and reuse it.
			continue
		}

		path := filepath.Join(distdir, name)
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Absent: nothing there, so nothing to protect against.
				continue
			}
			return moved, fmt.Errorf("failed to inspect distfile %q in %s: %w", name, distdir, err)
		}
		if info.IsDir() {
			continue
		}

		// Present and unlisted, therefore unverifiable.
		quarantined := quarantineName(name)
		if err := os.Rename(path, filepath.Join(distdir, quarantined)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// It went away between the Lstat and the Rename — another
				// worker quarantined it, or the host's package manager moved
				// it. Either way nothing unverifiable is left under this name,
				// which is the outcome this function exists to produce, and
				// whoever did move it reports it.
				continue
			}
			return moved, fmt.Errorf("failed to quarantine unverifiable distfile %q in %s: %w", name, distdir, err)
		}
		moved = append(moved, quarantined)
	}
	return moved, nil
}

// quarantineName builds the sibling name an unverifiable distfile is moved to.
//
// The three components answer three different collision questions. The infix
// separates a quarantine from a real distfile. The PID separates two runs, and
// the counter separates two goroutines inside one run — a sweep quarantines in
// parallel. The timestamp is there for the audit trail, and closes the one gap
// the other two leave: a PID is reused eventually, and without a clock a much
// later run could compute a name an older quarantine already holds, which
// os.Rename would replace without a word.
func quarantineName(base string) string {
	return fmt.Sprintf("%s%s.%d-%d",
		base+quarantineInfix,
		time.Now().UTC().Format(quarantineStamp),
		os.Getpid(),
		quarantineSeq.Add(1),
	)
}

// distfileName reduces one untrusted name to the single filename this package
// is willing to join to the distdir, and reports whether what is left names a
// file at all.
//
// filepath.Base is what neutralises traversal, but it does not answer "is this
// a filename" — it returns "." for an empty path, returns "." and ".."
// unchanged, and returns the separator for "/". Joining any of those to the
// distdir names a DIRECTORY rather than a file in it: filepath.Join cleans
// distdir + ".." into the distdir's parent, which on the default distdir is
// /var/cache. They are refused here, lexically, before anything touches the
// filesystem — a lexical guard has no window for a later check to lose.
//
// The separator test is belt and braces: on this platform Base cannot return a
// name containing one except for "/" itself, which the switch already catches.
// It is written down because "what we join is a single path element" is the
// invariant the whole function exists to hold, and a reader should be able to
// see it enforced rather than infer it from Base's contract.
// ParseManifestDistFilenames rejects the same two characters in the same
// spirit.
func distfileName(raw string) (string, bool) {
	base := filepath.Base(raw)
	switch base {
	case "", ".", "..", string(filepath.Separator):
		return "", false
	}
	if strings.ContainsAny(base, `/\`) {
		return "", false
	}
	return base, true
}
