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
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrDistdirNotWritable reports that the resolved distdir could not be prepared
// for this run: it does not exist and could not be created, or it exists and the
// invoking user cannot write to it. Callers test it with errors.Is; the
// specifics — which directory, and what the operating system said — travel in
// the wrapped error Probe or Resolve builds around it.
//
// The two conditions share one sentinel because they are one condition ("does
// not exist, cannot be created, or is not writable") and every caller acts on
// them identically: fail the manifest step naming the path, and do not hand
// the failure to the LLM fixer. Splitting them would make the gate in
// internal/autoupdate ask two questions to reach one answer, and a gate that
// misses a rung invokes an agent against a machine fault.
var ErrDistdirNotWritable = errors.New("distdir is not writable")

// ErrUnsupportedHomeForm is returned (wrapped) for a path whose first element is
// "~" followed by a user name, such as "~alice/distfiles". Only "~" and "~/..."
// are expanded; reading "~alice" as "$HOME/alice" would silently point at a
// directory under the CURRENT user's home, so the form is refused instead.
var ErrUnsupportedHomeForm = errors.New("unsupported home directory form")

// DefaultCache is the documented system path this package falls back to, and
// it plays two roles:
//
//   - the default for the --distfiles-cache flag: the read-only portage cache
//     consulted to skip re-downloading distfiles already on disk;
//   - the last resort of Resolve's precedence — used only when the host
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
	// run: only a directory we made is a directory we may remove.
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
	//     the user. A directory this run conjured is one it must take
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
// by this precedence, highest first:
//
//  1. explicit — the --distdir flag, with `overlay manifest`'s meaning;
//  2. configured — autoupdate.distdir from the config file;
//  3. the DISTDIR the host's package manager reports (`portageq distdir`);
//  4. DefaultCache, and only when that query cannot be answered.
//
// os.TempDir() is nowhere in that list on purpose: on the host that hit the
// defect it is a 31 GB tmpfs, and large fetches died there with wget's
// "Cannot write to '…' (Success)", the disguise ENOSPC wears on tmpfs. A
// directory that already existed has Created = false; only a path this call
// made is Created (stricter than ResolveOrTemp, see Dir.Created).
//
// The chosen directory is returned only after Probe proved it writable, with
// NO fallback: retreating elsewhere is the defect being removed. On failure the
// Dir carries the chosen Path, for the message only, with Created = false. A
// done ctx on the host-query rung returns the cancellation wrapped with %w
// rather than falling through to DefaultCache.
func Resolve(ctx context.Context, explicit, configured string) (Dir, error) {
	candidate := explicit
	if candidate == "" {
		candidate = configured
	}
	if candidate == "" {
		candidate = hostDistdir(ctx)
		if err := ctx.Err(); err != nil {
			return Dir{}, fmt.Errorf("resolving the distdir: %w", err)
		}
	}
	if candidate == "" {
		candidate = DefaultCache
	}

	abs, created, err := expandAndCreate(candidate)
	if err != nil {
		// Carries the same sentinel Probe raises, because "this machine cannot
		// give us a usable distdir" is one condition to act on. The wrap lives
		// HERE and not in expandAndCreate, which ResolveOrTemp shares:
		// `overlay manifest`'s errors must stay as they are, and a sentinel
		// appearing in them would be a change to it.
		return Dir{Path: abs}, fmt.Errorf("%w: %w", ErrDistdirNotWritable, err)
	}
	if err := Probe(abs); err != nil {
		return Dir{Path: abs}, err
	}
	return Dir{Path: abs, Created: created}, nil
}

// Locate reports the distdir to READ from, and whether there is one at all.
//
// Resolve's two side effects are wrong for a read-only gate: expandAndCreate
// CREATES the directory and Probe WRITES into it. A gate that only opens
// archives on disk needs no write permission, so a portage-owned DISTDIR the
// user cannot write is usable to it; and a DISTDIR that does not exist is an
// ANSWER ("nothing here to read"), not a directory to create.
//
// So Locate shares Resolve's precedence (explicit, configured, then the host's
// `portageq distdir`) and nothing else. It has no DefaultCache rung and no
// temporary fallback: each would turn "I could not look" into "I looked and
// found nothing". Do not fold it back into Resolve.
//
// found is false when no rung named a candidate, when the candidate does not
// exist or is not a directory, or on any other Stat error: the caller's
// question is "can I read here", not "why not". ctx bounds the host query on
// the last rung, as it does for Resolve.
func Locate(ctx context.Context, explicit, configured string) (string, bool) {
	candidate := explicit
	if candidate == "" {
		candidate = configured
	}
	// Asked only once the two rungs above are silent. A higher rung consulting
	// the host is a precedence bug even when it happens to return the right
	// path, which is why the tests count this call rather than only checking it.
	if candidate == "" {
		candidate = hostDistdir(ctx)
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
// are different questions and the defect behind Probe is the second one. On a
// full tmpfs the create succeeds and the write is what fails with ENOSPC — the
// error wget reported as "Cannot write to '…' (Success)".
const probePayload = "bentoo distdir writability probe\n"

// Probe verifies that dir can actually be written to, by creating a small file
// inside it and removing it again. It returns nil when the directory is usable
// and an error wrapping ErrDistdirNotWritable when it is not.
//
// It runs before pkgdev, so an unwritable distdir is an environment failure
// reported at once, not a download failure handed to an LLM fixer that cannot
// repair a permission. The default is the host's DISTDIR, portage:portage
// 0775, so writability depends on the invoking user's GROUPS; where they lack
// portage the answer must be this error naming the directory, never a retreat
// to somewhere writable such as a tmpfs.
//
// The probe file's name is built from the PID and a counter, never from input,
// so concurrent calls never collide. It is opened 0600 without O_EXCL, so a
// leftover from a killed predecessor with a reused PID does not read as
// unwritable, but with O_NOFOLLOW, so a planted symlink fails with ELOOP and
// its target is untouched. The file is removed by the same call, including when
// the write fails. An empty dir is refused: filepath.Join would probe the
// working directory.
func Probe(dir string) error {
	if dir == "" {
		return fmt.Errorf("%w: no distdir was resolved", ErrDistdirNotWritable)
	}

	name := fmt.Sprintf(".bentoo-distdir-probe-%d-%d", os.Getpid(), probeSeq.Add(1))
	path := filepath.Join(dir, name)

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: path is a generated probe name joined to the distdir resolved from the host's Portage configuration
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrDistdirNotWritable, dir, err)
	}
	// From here the file exists and is ours, so it goes away on every exit from
	// this function. A removal failure is not reported: the write already
	// answered the question this call asks, and having created the file we hold
	// the directory permission needed to unlink it.
	defer func() { _ = os.Remove(path) }() //nolint:gosec // G703: path is a generated probe name joined to the distdir resolved from the host's Portage configuration

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
// precedence, not a failed run. Requiring an absolute path is what keeps
// a diagnostic, a warning or a stray word from being mistaken for a directory:
// a relative path is meaningless here anyway, since we do not know portage's
// working directory.
//
// This is a library: the absence is reported by returning "", never logged.
func hostDistdir(ctx context.Context) string {
	return portageqPath(ctx, "distdir")
}

// TempRoot returns the directory a THROWAWAY distdir should be created under —
// the host's own PORTAGE_TMPDIR — or "" when the question cannot be answered.
// "" is exactly what os.MkdirTemp reads as "use os.TempDir()", so
//
//	os.MkdirTemp(distfiles.TempRoot(ctx), "…")
//
// is disk-backed where possible and today's behaviour elsewhere.
//
// Resolve's answer is shared, persistent and not ours to delete; this one is a
// directory that is ours, will be deleted, and must not be RAM. The LLM
// manifest fixer needs it for a private distdir. os.TempDir() on the measured
// host is a 31 GB tmpfs, the same defect Resolve removes from the manifest
// step. PORTAGE_TMPDIR is set in make.conf and NOT exported to a user process,
// so os.Getenv would return "" and land back on the tmpfs: only portageq can
// answer.
func TempRoot(ctx context.Context) string {
	return portageqPath(ctx, "envvar", "PORTAGE_TMPDIR")
}

// portageqPath runs one portageq query that is expected to print a single
// absolute path, and returns "" for every way that question can go unanswered:
// portageq absent, a non-zero exit, the timeout expiring, the caller's ctx
// being done, empty output, or output that is not an absolute path.
//
// The timeout is derived from ctx, so a cancelled caller stops the query at once
// instead of waiting out portageqTimeout.
//
// None of those is an error. "Unanswered" is a legitimate state — a non-portage
// host has no portageq at all — and its consequence is the caller's next rung,
// not a failed run. Requiring an absolute path is what keeps a
// diagnostic, a warning or a stray word from being mistaken for a directory: a
// relative path is meaningless here anyway, since we do not know portage's
// working directory.
//
// This is a library: the absence is reported by returning "", never logged.
func portageqPath(ctx context.Context, arg ...string) string {
	opCtx, cancel := context.WithTimeout(ctx, portageqTimeout)
	defer cancel()

	out, err := execCommand(opCtx, "portageq", arg...).Output()
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
	expanded, err := expandHome(userDir)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("failed to resolve distdir %q: %w", userDir, err)
	}
	return abs, nil
}

// expandHome expands a leading "~" or "~/" to the current user's home and
// returns any other path unchanged. A "~name" form is refused with
// ErrUnsupportedHomeForm rather than read as "~/name". It is the one home
// expansion expandPath and ResolveCache share.
func expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to expand %q: %w", p, err)
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	}
	if strings.HasPrefix(p, "~") {
		return "", fmt.Errorf("%w: %q (only \"~\" and \"~/...\" are expanded)", ErrUnsupportedHomeForm, p)
	}
	return p, nil
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
// disabled, missing, unreadable, a "~name" path, a home that cannot be
// resolved, or pointing at the same path as distdir).
func ResolveCache(userDir, distdir string) string {
	if userDir == "" {
		return ""
	}
	expanded, err := expandHome(userDir)
	if err != nil {
		return ""
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

// eachManifestDistRecord walks a Manifest body and hands every DIST record to
// visit, as the untouched line AND its whitespace-split fields.
//
// This is the ONE place in this package that decides what a DIST record is.
// Three answers are derived from that decision — the archive names the option
// gate looks for (ParseManifestDistFilenames), the same names plus whether the
// file could be read at all (ReadManifestDistFilenames), and the records a
// staged Manifest carries (ManifestDistLines) — and they read the same file for
// the same run. A line that is a DIST record to one and not to another produces
// a report that proves and denies the same file at once, so the test lives here
// once rather than being spelled out beside each answer.
//
// The test is a field split rather than HasPrefix("DIST "), which is the more
// permissive of the two shapes: an indented record, or one separated by a tab,
// is still a record. The line is handed over UNTOUCHED because Portage verifies
// these digests against the archive on disk, and a record that survived a
// round-trip through some normalised form is a record the build gates fail on.
func eachManifestDistRecord(body []byte, visit func(line string, fields []string)) {
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "DIST" {
			continue
		}
		visit(line, fields)
	}
}

// manifestDistFilenames names the archives a Manifest body declares.
//
// A record with no second field names nothing, so it is skipped rather than
// reported: a Manifest is read here to learn which files to look for, and a
// malformed line simply contributes no file.
func manifestDistFilenames(body []byte) []string {
	var names []string
	eachManifestDistRecord(body, func(_ string, fields []string) {
		if len(fields) < 2 {
			return
		}
		name := fields[1]
		if !isManifestFilename(name) {
			return
		}
		names = append(names, name)
	})
	return names
}

// isManifestFilename reports whether name can be a Manifest DIST filename.
// Filenames in a Manifest are basenames by spec, so anything else means the
// file is malformed (or hostile). "." and ".." carry no separator yet, joined
// onto the distdir, name the distdir itself or its parent.
func isManifestFilename(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}

// ParseManifestDistFilenames extracts the filenames listed on `DIST <name> ...`
// lines of a Gentoo Manifest. Missing files, read errors, or malformed lines
// yield an empty slice — prepopulation treats this as "nothing to reuse".
func ParseManifestDistFilenames(manifestPath string) []string {
	names, err := ReadManifestDistFilenames(manifestPath)
	if err != nil {
		return nil
	}
	return names
}

// ReadManifestDistFilenames is ParseManifestDistFilenames' error-returning
// sibling: the same names for a file that can be read, and the read failure
// itself for one that cannot.
//
// ParseManifestDistFilenames answers an unreadable Manifest with nil, which is
// indistinguishable from "readable, and declaring no DIST". That is wrong for a
// caller whose empty slice is AUTHORITATIVE ("this package publishes no
// archive"): it would report "I could not look" as a fact. It is one read, not
// a probe plus a parse, because a Manifest replaced between two reads would
// answer for two different files.
//
// The error travels unwrapped: each call site words this failure itself, to
// the byte, because operators read those reports. Unwrapped also keeps
// errors.Is(err, fs.ErrNotExist) answerable for callers that tell an absent
// Manifest from an unreadable one.
func ReadManifestDistFilenames(manifestPath string) ([]string, error) {
	body, err := os.ReadFile(manifestPath) //nolint:gosec // G304: manifestPath is <package dir>/Manifest in the user's overlay or staged tree, under a package directory a confined key or the overlay scan names
	if err != nil {
		return nil, err
	}
	return manifestDistFilenames(body), nil
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
// A staged validation tree holds one candidate ebuild and none of the
// package's other files, so the published Manifest's EBUILD, AUX and MISC
// records name files it does not have. Only the DIST records describe what the
// two trees genuinely share: the upstream archive. (A non-thin repository still
// refuses a candidate with no EBUILD record; staging imposes thin-manifests for
// that half.)
//
// Lines, not a parser: Portage verifies these digests, so a kept line must be
// the bytes that were read. The trailing newline is re-established rather than
// preserved per line, the one normalisation, and no digest is read through it.
// Empty in, or no DIST record found, yields nil: "this describes nothing".
func ManifestDistLines(body []byte) []byte {
	var kept []string
	// The SAME test the name-producing answers apply, because it is literally
	// the same code: eachManifestDistRecord holds this package's one copy of the
	// DIST-line grammar, and the reasons the two cannot be allowed to drift are
	// recorded there. The line is kept UNTOUCHED, digests and spacing included.
	eachManifestDistRecord(body, func(line string, _ []string) {
		kept = append(kept, line)
	})
	if len(kept) == 0 {
		return nil
	}
	return []byte(strings.Join(kept, "\n") + "\n")
}

// ManifestMerge counts what MergeManifestDist did: published records kept,
// staged records written, published records displaced by a staged record of
// the same filename, and published records dropped for having no usable
// filename.
type ManifestMerge struct {
	Kept, Written, Replaced, Dropped int
}

// MergeManifestDist returns the DIST records of staged plus those of published
// whose filename staged does not declare, ordered by filename, each line
// exactly as read. A filename published declares twice keeps its first record.
// Non-DIST records are written from neither side; nil means no record at all.
func MergeManifestDist(published, staged []byte) ([]byte, ManifestMerge) {
	type record struct{ name, line string }
	var (
		merge   ManifestMerge
		records []record
	)
	declared := map[string]bool{}
	eachManifestDistRecord(staged, func(line string, fields []string) {
		name := ""
		if len(fields) > 1 {
			name = fields[1]
		}
		declared[name] = true
		records = append(records, record{name, line})
		merge.Written++
	})
	seen := map[string]bool{}
	eachManifestDistRecord(published, func(line string, fields []string) {
		switch {
		case len(fields) < 2 || !isManifestFilename(fields[1]):
			merge.Dropped++
		case declared[fields[1]]:
			merge.Replaced++
		case seen[fields[1]]:
		default:
			seen[fields[1]] = true
			records = append(records, record{fields[1], line})
			merge.Kept++
		}
	})
	if len(records) == 0 {
		return nil, merge
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].name < records[j].name })
	var b strings.Builder
	for _, r := range records {
		b.WriteString(r.line)
		b.WriteByte('\n')
	}
	return []byte(b.String()), merge
}
