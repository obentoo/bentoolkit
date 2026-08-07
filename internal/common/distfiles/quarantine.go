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

// Quarantine implements D3. Before pkgdev is invoked, every distfile the new
// version expects is looked at in the resolved distdir, and any one that is
// present but cannot be verified is moved aside instead of being reused.
//
// # Why this is needed at all
//
// It is the price of the distdir being the host's real DISTDIR rather than a
// directory this run made. Portage's default FETCHCOMMAND writes straight to
// ${DISTDIR}/${FILE} with no temporary name, so a fetch killed midway leaves a
// TRUNCATED file under the FINAL name. On a version bump the package's current
// Manifest does not yet list that filename, so nothing downstream holds a size
// or a checksum to compare it against, and the next run digests whatever is on
// disk. This overlay commits and pushes on its own, so that checksum is
// published before anyone looks at it (R2.2).
//
// # The three cases
//
//  1. Absent — nothing to do.
//  2. Present AND listed in the package's current Manifest — a known,
//     previously verified distfile. It is left exactly as it is, which is
//     R2.1's reuse: the download is skipped because the file is already there
//     and something has already vouched for it.
//  3. Present and NOT listed — unverifiable by construction. It is renamed to a
//     sibling quarantine name and reported, so pkgdev fetches cleanly.
//
// Case 3 moves rather than deletes because the directory is the host's, not
// ours (R2.5): a file we cannot prove we created is not ours to destroy, and a
// moved file is recoverable while a deleted one is not. Misclassifying is
// therefore an inconvenience instead of data loss. Nothing here changes a mode
// or an owner — not on the directory, not on the files — for the same reason:
// a rename carries the file's own metadata across untouched, which is precisely
// why it is the operation used.
//
// # Arguments and return
//
// distdir is the resolved directory (see Resolve). manifestNames are the
// filenames on the DIST lines of the package's CURRENT Manifest, i.e. what is
// already verified — ParseManifestDistFilenames produces exactly this list.
// expected are the distfile names the NEW version needs; on a bump those are
// typically absent from manifestNames, which is why a file already sitting
// under one of them is unverifiable.
//
// The returned slice holds the NEW name of each file that was moved, not the
// old one. That is deliberate: the quarantine name has the original filename as
// its prefix, so one string tells the caller both WHAT was moved and WHERE it
// went, and a report line built from it is enough to find the file again in a
// directory holding thousands of others. This package is a library and never
// logs; the caller reports what these names say.
//
// An error is fatal for the package and the caller must not go on to pkgdev
// after one: the point of this function is that an unverifiable file must not be
// digested, and a file we failed to move aside is still sitting there. Whatever
// was moved before the failure comes back alongside the error, because it was
// still moved and the operator still needs to hear about it. Being unable to
// even determine a file's state is treated the same way — an inspection that
// fails is not an absence, so it fails closed rather than assuming there is
// nothing there.
//
// # Untrusted names
//
// Every name, from manifestNames and expected alike, is reduced with
// filepath.Base before it is joined to distdir: DIST names come out of a parsed
// file and the expected names out of a resolved version, so both are untrusted
// input, and this directory is shared with the system package manager, where a
// traversal would write outside a directory the tool does not own. Base
// neutralises traversal by construction — "../../etc/passwd" becomes "passwd" —
// and the results of Base that are not filenames at all are refused lexically,
// before the filesystem is touched, because filepath.Join(distdir, "..") is the
// distdir's PARENT and renaming that would move /var/cache. Both sides are
// reduced with the same rule, so the "is it listed" comparison stays honest.
//
// # Symlinks and directories
//
// The lookup is os.Lstat and the move is os.Rename, so a symlink is never
// followed: both act on the link itself. That matters because
// PrepopulateFromCache puts symlinks into this very directory. A symlink under
// an expected name the Manifest does not list is quarantined like anything else
// — what it points at would be digested and we cannot verify it — and moving it
// moves the LINK, leaving the read-only cache file it points to untouched. A
// dangling one is quarantined too, which is why this is Lstat and not Stat:
// Stat reports a dangling link as absent and would leave it in place under the
// very name pkgdev is about to fetch.
//
// A directory found under an expected name is left alone. A distfile is a file,
// a distdir may hold subdirectories that belong to the host (git3-src and the
// like), and moving one aside is not this function's business.
//
// # Concurrency
//
// os.Rename is atomic within one filesystem, so two workers that reach the same
// file cannot both move it: one wins and the other's rename fails with ENOENT,
// which is read as "already gone" and reported as nothing moved. Their
// destination names cannot collide either (see quarantineInfix and
// quarantineSeq). What is NOT atomic is the Lstat-then-Rename sequence around
// it: a file that appears between the two calls is not seen on this pass, and
// one that disappears is skipped. Neither corrupts anything — the window is a
// missed observation, not a wrong write — and closing it is the per-distfile
// lock of D4, not this function's job.
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
			// Case 2: already verified, so leave it. This is R2.1's reuse.
			continue
		}

		path := filepath.Join(distdir, name)
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Case 1: nothing there, so nothing to protect against.
				continue
			}
			return moved, fmt.Errorf("failed to inspect distfile %q in %s: %w", name, distdir, err)
		}
		if info.IsDir() {
			continue
		}

		// Case 3: present, unlisted, therefore unverifiable.
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
