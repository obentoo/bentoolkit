// Package distfiles resolves and prepares the directory pkgdev is given as
// --distdir, and holds the single implementation every caller shares.
//
// It exists because the two callers had drifted: `overlay manifest` took a
// configurable, persistent directory while the autoupdate sweep hardcoded a
// temporary one. Keeping the resolution here — below both feature packages,
// rather than exported from one of them — is what stops that divergence from
// arising again.
//
// The directory this package hands back may be the host's real DISTDIR: the
// same directory portage downloads into, shared with emerge and with every
// other package operation on the machine. It is therefore NOT necessarily
// ours to delete. A resolved Dir carries its own provenance in Created, and
// Cleanup removes the directory only when this process is the one that made
// it; a directory the caller named — or the host's own DISTDIR — survives the
// run untouched.
//
// There are two entry points because the two commands promise their users
// different defaults, not because the resolution differs:
//
//   - Resolve is the autoupdate path. With nothing configured it lands on the
//     DISTDIR the host itself names, and never on a temporary directory.
//   - ResolveOrTemp is `overlay manifest`. With nothing configured it makes a
//     throwaway temporary directory, which is exactly what that command's
//     --help promises ("no sudo is required").
//
// Both share one expansion-and-creation helper, so the drift this package was
// created to end cannot come back through the parts that are genuinely common.
//
// Only Resolve ends with the writability pre-flight (Probe). That is not an
// oversight in ResolveOrTemp: `overlay manifest` ships today without such a
// check, its default distdir is one it has just created, and turning a
// non-writable named --distdir into a hard failure there would change the
// behaviour of a command this package was only supposed to move.
//
// The package depends only on the Go standard library.
package distfiles

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// ErrDistdirNotWritable reports that the resolved distdir could not be prepared
// for this run: it does not exist and could not be created, or it exists and the
// invoking user cannot write to it. Callers test it with errors.Is; the
// specifics — which directory, and what the operating system said — travel in
// the wrapped error Probe or Resolve builds around it.
//
// The two conditions share one sentinel because R1.4 groups them ("does not
// exist, cannot be created, or is not writable") and because every caller acts
// on them identically: fail the manifest step naming the path, and do not hand
// the failure to the LLM fixer (R3.1). Splitting them would make the gate in
// internal/autoupdate ask two questions to reach one answer, and a gate that
// misses a rung invokes an agent against a machine fault — the defect S030
// exists to remove.
var ErrDistdirNotWritable = errors.New("distdir is not writable")

// DefaultCache is the documented system path this package falls back to, and
// it plays two roles:
//
//   - the default for the --distfiles-cache flag: the read-only portage cache
//     consulted to skip re-downloading distfiles already on disk;
//   - the last resort of Resolve's precedence (R1.2) — used only when the host
//     cannot be asked where its DISTDIR is.
//
// internal/overlay re-exports it as DefaultDistfilesCache so the CLI keeps the
// name it has always used.
const DefaultCache = "/var/cache/distfiles"

// execCommand is the seam over os/exec that lets tests answer — or refuse to
// answer — the portageq query without a portage installation. It is a variable
// for that reason alone; production always runs exec.CommandContext.
var execCommand = exec.CommandContext

// portageqTimeout bounds the DISTDIR query. `portageq distdir` parses make.conf
// and prints one line, so a couple of seconds is already generous; the bound
// exists so a wedged portage cannot stall a whole sweep behind a question whose
// answer we can live without. Tests shorten it to exercise the timeout branch.
var portageqTimeout = 2 * time.Second

// Dir is a resolved distfiles directory together with the provenance that
// decides who may delete it.
type Dir struct {
	// Path is the absolute path pkgdev is given as --distdir.
	Path string
	// Created reports whether this process created the directory for this
	// run. It is the whole of R1.5: only a directory we made is a directory
	// we may remove.
	//
	// What counts as "we made it" differs between the two entry points, and
	// the difference is deliberate — each matches the promise its command
	// makes to its users. Do not unify them:
	//
	//   - ResolveOrTemp sets Created only for the temporary directory it
	//     makes when no path was supplied. A directory the caller NAMED is
	//     never Created, whether it already existed or had to be mkdir'd: a
	//     named distdir is a persistent download cache the caller expects to
	//     find again next run, and it may well be the host's own DISTDIR.
	//   - Resolve sets Created for any path it had to create, because on that
	//     path the directory was chosen by this process rather than named by
	//     the user (D2). A directory this run conjured is one it must take
	//     away again.
	Created bool
}

// Cleanup removes the directory when this process created it, and is a no-op
// otherwise. It is safe on a zero Dir and safe to call more than once, so
// `defer dir.Cleanup()` can be written immediately after Resolve returns.
//
// A removal failure is deliberately ignored: cleanup runs when the outcome of
// the run is already decided, and a leftover temporary directory is
// housekeeping noise the caller cannot act on.
func (d Dir) Cleanup() {
	if !d.Created || d.Path == "" {
		return
	}
	_ = os.RemoveAll(d.Path)
}

// Resolve returns the directory the autoupdate path gives pkgdev as --distdir,
// chosen by D2's precedence — highest first:
//
//  1. explicit — the --distdir flag, the same flag name `overlay manifest`
//     uses and with the same meaning (R1.3);
//  2. configured — autoupdate.distdir from the config file;
//  3. the DISTDIR the host's own package manager reports at runtime
//     (`portageq distdir`), which is the default (R1.2);
//  4. DefaultCache, and only when that query cannot be answered.
//
// os.TempDir() is consulted nowhere in that list, and removing it is the whole
// reason this function exists (R1.1). The defect it replaces hardcoded the
// distdir to a temporary directory, which on the machine that hit it is a 31 GB
// tmpfs: every distfile a sweep fetched was written into RAM on a host already
// deep into swap, and the large ones died with wget's "Cannot write to '…'
// (Success)" — the disguise ENOSPC wears on tmpfs. A distdir has to be backed
// by a disk, and the host already names one.
//
// Created follows D2 literally here: a directory that already existed is used
// as-is with Created = false, and only a path this call had to make gets
// Created = true, so Cleanup removes exactly what this run conjured and nothing
// else. That is STRICTER than ResolveOrTemp, on purpose — see Dir.Created.
//
// Resolution ends with Probe, the pre-flight of D5: the chosen directory is not
// returned until it has been proved writable. There is deliberately NO fallback
// branch here — a directory that fails the probe is returned as an error naming
// it, never swapped for another one. Falling back is precisely the defect this
// story removes: the code being replaced retreated to a temporary directory,
// which on the host measured is a 31 GB tmpfs (M2). See Probe for why the answer
// depends on the invoking user's groups rather than on the path.
//
// On failure — whether the directory could not be created or could not be
// written to — the returned Dir carries the chosen Path with Created = false:
// Cleanup stays a no-op, and the caller can name the directory it could not
// prepare in the diagnostic instead of failing anonymously (R1.4). A non-nil
// error means the path is for the message only; it is never a directory to use.
func Resolve(explicit, configured string) (Dir, error) {
	candidate := explicit
	if candidate == "" {
		candidate = configured
	}
	if candidate == "" {
		candidate = hostDistdir()
	}
	if candidate == "" {
		candidate = DefaultCache
	}

	abs, created, err := expandAndCreate(candidate)
	if err != nil {
		// Carries the same sentinel Probe raises, because "this machine cannot
		// give us a usable distdir" is one condition to act on and R1.4 states
		// it as one. The wrap lives HERE and not in expandAndCreate, which
		// ResolveOrTemp shares: `overlay manifest`'s behaviour is frozen by the
		// Unchanged Behavior section, and a sentinel appearing in its errors
		// would be a change to it.
		return Dir{Path: abs}, fmt.Errorf("%w: %w", ErrDistdirNotWritable, err)
	}
	if err := Probe(abs); err != nil {
		return Dir{Path: abs}, err
	}
	return Dir{Path: abs, Created: created}, nil
}

// Locate reports the distdir to READ from, and whether there is one at all.
//
// # Why this exists next to Resolve rather than inside it
//
// Resolve answers a different question — "where does this host keep its
// distfiles, and may we write there" — and answers it with two side effects:
// expandAndCreate CREATES the directory, and Probe PROVES it writable by
// writing a file into it. Both are right for `overlay manifest`, which is about
// to download into that directory. Both are wrong for a read-only gate.
//
// A gate that only opens archives already on disk needs no write permission, so
// a DISTDIR owned by portage and not writable by the invoking user is perfectly
// usable to it — and this repository has already had to remove three tests that
// assumed otherwise. Nor may it conjure a directory: a DISTDIR that does not
// exist is an ANSWER ("there is nothing here to read"), which the caller turns
// into an outcome that says so, rather than a directory to create and then find
// empty.
//
// So this shares Resolve's PRECEDENCE — explicit, then configured, then the
// host's own `portageq distdir` — and shares nothing else. There is deliberately
// no DefaultCache rung and no temporary-directory fallback: each would name a
// directory holding no distfiles and turn "I could not look" into "I looked and
// found nothing", which is the class of silent pass this gate exists to remove.
//
// Do not fold this back into Resolve (design D2). They differ in exactly the two
// side effects that matter, and unifying them would either stop the manifest
// step creating its distdir or start the validate gate writing to one it was
// asked only to read.
//
// found is false when no rung named a candidate, when the candidate does not
// exist, or when it is not a directory. A Stat error other than not-exist is
// false too: the caller's question is "can I read here", not "why not".
func Locate(explicit, configured string) (string, bool) {
	candidate := explicit
	if candidate == "" {
		candidate = configured
	}
	// Asked only once the two rungs above are silent. A higher rung consulting
	// the host is a precedence bug even when it happens to return the right
	// path, which is why the tests count this call rather than only checking it.
	if candidate == "" {
		candidate = hostDistdir()
	}
	if candidate == "" {
		return "", false
	}

	abs, err := expandPath(candidate)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return abs, true
}

// ResolveOrTemp returns the directory `overlay manifest` gives pkgdev as
// --distdir, keeping the default that command documents in its own --help: an
// unset --distdir means a temporary directory discarded after the run, which is
// why that command needs no sudo (see cmd/bentoo/overlay_manifest.go).
//
// When userDir is empty, a temporary directory is created and the returned Dir
// is Created, so Cleanup removes it when the run finishes. When userDir is set
// it is expanded (~ and relative paths) and created if missing, and the
// returned Dir is never Created: the caller asked for that specific path, so
// it is preserved across runs as a persistent download cache.
//
// That last rule is why this is not Resolve. Resolve marks a directory it had
// to create as Created; here a named --distdir is a cache by contract and never
// ours to delete. Both rules are correct for their own caller (see Dir.Created).
//
// On failure the returned Dir is the zero value, whose Cleanup does nothing.
func ResolveOrTemp(userDir string) (Dir, error) {
	if userDir == "" {
		tmp, err := os.MkdirTemp("", "bentoo-distfiles-")
		if err != nil {
			return Dir{}, fmt.Errorf("failed to create temp distdir: %w", err)
		}
		return Dir{Path: tmp, Created: true}, nil
	}

	abs, _, err := expandAndCreate(userDir)
	if err != nil {
		return Dir{}, err
	}
	return Dir{Path: abs}, nil
}

// probeSeq numbers the probe files this process writes. The PID keeps two
// concurrent RUNS apart; it says nothing about two goroutines inside one run,
// and Probe is called concurrently — once as Resolve's pre-flight, and again by
// the post-failure classifier for every package that fails. A per-process
// counter closes that gap, so the name is unique both across processes and
// within one.
var probeSeq atomic.Uint64

// probePayload is written into the probe file instead of leaving it empty,
// because "the directory accepted an inode" and "the filesystem accepted bytes"
// are different questions and this story exists because of the second one. On a
// full tmpfs the create succeeds and the write is what fails with ENOSPC — the
// error wget reported as "Cannot write to '…' (Success)".
const probePayload = "bentoo distdir writability probe\n"

// Probe verifies that dir can actually be written to, by creating a small file
// inside it and removing it again. It returns nil when the directory is usable
// and an error wrapping ErrDistdirNotWritable when it is not.
//
// This is R3.1's pre-flight. It runs before pkgdev is invoked, so a distdir the
// invoking user cannot write to is an environment failure reported at once,
// rather than a download failure handed to an LLM fixer that cannot repair a
// filesystem permission.
//
// Why this is not a theoretical check: the default distdir is the host's own
// DISTDIR, which on a Gentoo system is portage:portage 0775 (M2). Whether it is
// writable is therefore a property of the invoking user's GROUPS, not of the
// path. On the machine this story was measured on the user is in group portage
// and the default works; on a host where they are not, the very same default is
// unwritable — and the answer there must be this error, naming the directory.
// Retreating to somewhere writable, which is what the code being replaced did,
// lands the download in a tmpfs and reintroduces the defect (R1.4).
//
// The probe file is named for this process and this call, is written with mode
// 0600, and is removed by the same call that created it — including when the
// write fails. Its name is built here and never derived from external input, so
// it cannot be steered outside dir. Concurrent calls, from any number of
// goroutines or processes, never choose the same name and so never disturb each
// other; the classifier added later re-uses Probe for exactly that reason.
//
// The file is opened without O_EXCL on purpose. A predecessor killed between
// creating and removing its probe can leave one behind, and a later process that
// happens to reuse its PID would otherwise be told a perfectly writable
// directory is not writable. Overwriting a file that can only be our own name
// is both safe and self-healing.
//
// An empty dir is refused rather than probed: filepath.Join would resolve the
// probe against the working directory, which is not the directory anyone asked
// about, and writing there would answer the wrong question.
func Probe(dir string) error {
	if dir == "" {
		return fmt.Errorf("%w: no distdir was resolved", ErrDistdirNotWritable)
	}

	name := fmt.Sprintf(".bentoo-distdir-probe-%d-%d", os.Getpid(), probeSeq.Add(1))
	path := filepath.Join(dir, name)

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrDistdirNotWritable, dir, err)
	}
	// From here the file exists and is ours, so it goes away on every exit from
	// this function. A removal failure is not reported: the write already
	// answered the question this call asks, and having created the file we hold
	// the directory permission needed to unlink it.
	defer func() { _ = os.Remove(path) }()

	if _, err := file.WriteString(probePayload); err != nil {
		// Close before returning; the deferred Remove still runs.
		_ = file.Close()
		return fmt.Errorf("%w: %s: %w", ErrDistdirNotWritable, dir, err)
	}
	// Close is checked rather than deferred-and-discarded: some filesystems
	// only surface a write error here, and a probe that ignored it would
	// declare a directory writable on the strength of a write that never
	// landed.
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrDistdirNotWritable, dir, err)
	}
	return nil
}

// hostDistdir asks the host's package manager where it downloads to, and
// returns "" for every way that question can go unanswered: portageq absent,
// a non-zero exit, the timeout expiring, empty output, or output that is not an
// absolute path.
//
// None of those is an error. "Unanswered" is a legitimate state — a non-portage
// host has no portageq at all — and its consequence is the next rung of the
// precedence, not a failed run (R1.2). Requiring an absolute path is what keeps
// a diagnostic, a warning or a stray word from being mistaken for a directory:
// a relative path is meaningless here anyway, since we do not know portage's
// working directory.
//
// This is a library: the absence is reported by returning "", never logged.
func hostDistdir() string {
	return portageqPath("distdir")
}

// TempRoot returns the directory a THROWAWAY distdir should be created under —
// the host's own PORTAGE_TMPDIR — or "" when the question cannot be answered.
//
// "" is not a failure and not a sentinel: it is exactly what os.MkdirTemp takes
// to mean "use os.TempDir()", so a caller writes
//
//	os.MkdirTemp(distfiles.TempRoot(), "…")
//
// and gets the disk-backed answer where one exists and today's behaviour where
// it does not.
//
// # Why this exists next to Resolve rather than inside it
//
// Resolve answers "where does this host KEEP its distfiles" (R1.2) and its
// answer is shared, persistent and not ours to delete. This answers a different
// question — "where may we make a directory that is ours, that we will delete,
// and that must not be RAM". The LLM manifest fixer needs the second: it is
// given a private distdir so its self-verification never touches the system
// DISTDIR, and that privacy is the point, so Resolve's answer would be wrong.
//
// What was wrong was the ROOT. os.MkdirTemp("") means os.TempDir(), which on the
// host S030 was measured on is a 31 GB tmpfs — the same defect R1.1 removes from
// the manifest step, on a second path the story's tasks did not reach. Note that
// reading the environment is not enough: PORTAGE_TMPDIR is set in make.conf and
// is NOT exported into an ordinary user process, so os.Getenv returns "" here
// and would silently land back on the tmpfs. Only portageq can answer.
func TempRoot() string {
	return portageqPath("envvar", "PORTAGE_TMPDIR")
}

// portageqPath runs one portageq query that is expected to print a single
// absolute path, and returns "" for every way that question can go unanswered:
// portageq absent, a non-zero exit, the timeout expiring, empty output, or
// output that is not an absolute path.
//
// None of those is an error. "Unanswered" is a legitimate state — a non-portage
// host has no portageq at all — and its consequence is the caller's next rung,
// not a failed run (R1.2). Requiring an absolute path is what keeps a
// diagnostic, a warning or a stray word from being mistaken for a directory: a
// relative path is meaningless here anyway, since we do not know portage's
// working directory.
//
// This is a library: the absence is reported by returning "", never logged.
func portageqPath(arg ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), portageqTimeout)
	defer cancel()

	out, err := execCommand(ctx, "portageq", arg...).Output()
	if err != nil {
		return ""
	}
	// TrimSpace covers the trailing newline portageq always prints; the
	// IsAbs check below covers empty and whitespace-only output too, since
	// "" is not absolute.
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		return ""
	}
	return path
}

// expandPath turns a user-written distdir into an absolute one, expanding "~"
// and resolving a relative path against the working directory. It touches the
// filesystem only to ask where "~" is.
//
// It is split out of expandAndCreate so that Locate can share the expansion
// without sharing the creation. Two notions of what "~/distfiles" means would
// be a bug waiting for the day they disagree, and the error strings stay here
// so both callers keep wording a failure the same way.
func expandPath(userDir string) (string, error) {
	expanded := userDir
	if strings.HasPrefix(expanded, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to expand %q: %w", userDir, err)
		}
		expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~"))
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("failed to resolve distdir %q: %w", userDir, err)
	}
	return abs, nil
}

// expandAndCreate is the one implementation both entry points share: it expands
// "~" and relative paths, makes sure the directory exists, and reports whether
// this call is the one that created it.
//
// The caller decides what to do with that last fact — Resolve turns it into
// Created, ResolveOrTemp discards it — which is exactly why the two rules can
// differ without the path handling differing with them.
//
// On failure it returns the absolute path when it got that far, so the caller
// can name the directory it could not prepare.
func expandAndCreate(userDir string) (string, bool, error) {
	abs, err := expandPath(userDir)
	if err != nil {
		return "", false, err
	}
	// Whether the directory is already there has to be asked BEFORE creating
	// it: this is the only moment the answer exists, and it is what decides
	// whether Cleanup may later delete it.
	_, statErr := os.Stat(abs)
	existed := statErr == nil
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return abs, false, fmt.Errorf("failed to create distdir %q: %w", abs, err)
	}
	return abs, !existed, nil
}

// ResolveCache validates the configured cache directory once per run.
// Returns the absolute path when the directory exists and is a real directory
// distinct from distdir, or "" when prepopulation should be skipped (cache
// disabled, missing, unreadable, or pointing at the same path as distdir).
func ResolveCache(userDir, distdir string) string {
	if userDir == "" {
		return ""
	}
	expanded := userDir
	if strings.HasPrefix(expanded, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~"))
		}
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return ""
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return ""
	}
	if distAbs, err := filepath.Abs(distdir); err == nil && distAbs == abs {
		// Cache and working distdir are the same path — pkgdev already
		// reuses files in place, no symlinks needed.
		return ""
	}
	return abs
}

// ParseManifestDistFilenames extracts the filenames listed on `DIST <name> ...`
// lines of a Gentoo Manifest. Missing files, read errors, or malformed lines
// yield an empty slice — prepopulation treats this as "nothing to reuse".
func ParseManifestDistFilenames(manifestPath string) []string {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "DIST" {
			continue
		}
		name := fields[1]
		// Reject path traversal: filenames in Manifest are basenames by
		// spec — anything else means the file is malformed (or hostile).
		if name == "" || strings.ContainsAny(name, "/\\") {
			continue
		}
		names = append(names, name)
	}
	return names
}

// PrepopulateFromCache symlinks each cached distfile into distdir so pkgdev
// can validate it locally instead of re-downloading. Returns the count of
// successfully linked files. Files already present in distdir, missing from
// the cache, or causing symlink errors are silently skipped — pkgdev will
// download them as a fallback.
func PrepopulateFromCache(distdir, cacheDir string, names []string) int {
	reused := 0
	for _, name := range names {
		src := filepath.Join(cacheDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(distdir, name)
		if _, err := os.Lstat(dst); err == nil {
			// Already present (concurrent worker, or persistent distdir).
			continue
		}
		if err := os.Symlink(src, dst); err != nil {
			continue
		}
		reused++
	}
	return reused
}

// ManifestDistLines keeps the DIST records of a Manifest and drops everything
// else, each surviving line byte-for-byte.
//
// # Why a caller wants this
//
// A staged validation tree holds one candidate ebuild, no metadata.xml and none
// of the package's siblings. The published Manifest's EBUILD, AUX and MISC
// records all name files that tree does not have, so handing it over whole
// describes a repository that does not exist. Only the DIST records describe
// something the staged tree genuinely shares with the published one: the
// upstream archive on disk.
//
// This is NOT what makes such a tree buildable on its own — a non-thin
// repository refuses a candidate carrying no EBUILD record of its own, whatever
// the Manifest says. Staging imposes thin-manifests for that. This is the other
// half: not handing over records that describe absent files.
//
// # Why lines and not a parser
//
// A Manifest is line-oriented and its record type is the first field, so a line
// is kept or it is not — and a kept line is the bytes that were read. Portage
// verifies these digests against the archive it finds, so a DIST line that
// survived a round-trip through some intermediate form is a line the build
// fails on. The trailing newline is re-established rather than preserved per
// line, which is the one normalisation here and the one no digest is read
// through.
//
// Empty in, or no DIST record found, yields nil: a Manifest naming no archive
// has answered, and its caller reads nil as "this describes nothing".
func ManifestDistLines(body []byte) []byte {
	var kept []string
	for _, line := range strings.Split(string(body), "\n") {
		// The SAME test ParseManifestDistFilenames applies, and that is the whole
		// point of spelling it this way rather than as HasPrefix("DIST ").
		//
		// The two functions read the same file for the same run: one decides
		// which archives the option gate looks for, the other decides which
		// records the staged Manifest carries. A line that is a DIST record to
		// one and not to the other — an indented one, or one separated by a tab —
		// produces a report that proves and denies the same file at once. The
		// field split is the more permissive of the two, so it is the one both
		// converge on.
		//
		// The line is appended UNTOUCHED whatever its shape: Portage verifies
		// these digests against the archive on disk, and a record that survived a
		// round-trip through some normalised form is a record the build gates
		// fail on.
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "DIST" {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return []byte(strings.Join(kept, "\n") + "\n")
}
