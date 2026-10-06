// applier_gates_environment.go holds both halves of the unmet-precondition
// record.
//
// The RECORDING half is at the top: once build_failure.go has decided that a
// failed build belongs to the machine (ErrBuildEnvironment), this file answers
// the follow-up question — WHAT did the machine not have — and writes the answer
// against the package in cache.json.
//
// The RE-CHECKING half is at the bottom: on a later run, before any child is
// spawned, it asks whether that answer still holds — as the BUILD USER, which is
// a different question from the one the caller's own `stat` answers — and either
// declines the gate or clears the record and lets it run.
//
// Without the record that answer was thrown away thirteen times over two days:
// mt7927-dkms and edk2 failed in pkg_setup on a key file the `portage` uid could
// not read, the failure was correctly classified as the host's, and the next run
// started the same build again because nothing survived to say what was missing.
//
// # Fail open is a DIRECTION, not defensive coding
//
// Everything below refuses to answer far more readily than it answers, and that
// asymmetry is the whole design. The two outcomes are not symmetric:
//
//   - Record nothing when something was missing → the package is retried, which
//     costs a build that was going to be spent anyway. That is today's behaviour,
//     exactly, and it is the floor this change cannot fall below.
//   - Record the WRONG thing → the re-check asks about a path that
//     nothing on the host will ever satisfy, and the package is suppressed
//     FOREVER, silently, on the strength of a parse that failed.
//
// So a message this file does not understand yields "", never a placeholder. The
// direction is the one build_failure.go inherited from the manifest path,
// pointed at a different cost: a wrong classification must cost a wasted
// invocation, never a lost repair. Here, a wrong extraction must cost a wasted
// build, never a lost package.
//
// # Reading a log is brittle, and it is admitted rather than hidden
//
// Portage's messages are the ebuild author's prose. A wording change breaks the
// extraction, and when it does, the extraction returns "" and the feature simply
// stops helping — it does not start lying. That trade is accepted explicitly.
package autoupdate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
)

// buildPhaseReport matches Portage's own "which phase died" line, e.g.
// `* ERROR: net-wireless/mt7927-dkms-2.14 failed (setup phase):`. It is matched
// against the lowercased transcript so a phase name's case cannot decide this.
var buildPhaseReport = regexp.MustCompile(`failed \(([a-z_]+) phase\)`)

// hostCheckPhases are the phases whose JOB is to check the host, and they are the
// only ones an unmet-precondition record may be extracted from.
//
// pkg_pretend and pkg_setup run before a single source file is touched: they are
// where an ebuild asserts that a kernel option, a signing key or a tool is
// present. From src_prepare onward the ebuild is exercising ITS OWN sources, and
// a missing file there is the ebuild's fault — recording it would suppress a
// package whose ebuild is genuinely broken, hiding the bug this project exists to
// find. That is the same prepare boundary buildFaultVerdict's rung 2 draws, drawn
// again here because a transcript is all this function is given.
var hostCheckPhases = map[string]bool{
	"pretend": true,
	"setup":   true,
}

// unmetPreconditionCues are the reports that a NAMED thing was absent or
// unreadable. A cue is required — the path is never taken from just any line —
// because a build transcript is full of paths that are perfectly fine, and the
// record must mean "this is what was missing", not "this is a path I saw".
//
// The list is kept short on purpose: every cue added is another way to grab the
// wrong path, and the wrong path is the expensive direction.
var unmetPreconditionCues = []string{
	"not found",
	"no such file",
	"could not open",
	"cannot open",
	"permission denied",
}

// portageBuildRoot is where Portage builds, and a path under it is worthless as a
// record: the directory is created for one build and removed after it, so a
// pre-check asking whether it exists would answer "no" forever and freeze the
// package on a directory that is SUPPOSED to be absent.
const portageBuildRoot = "/var/tmp/portage/"

// extractUnmetPrecondition returns the absolute path a host-caused build failure
// named as missing or unreadable, or "" when the transcript does not yield one it
// can stand behind. Built from two failures of 2026-08-22:
//
//	… USE=modules-sign is set but the private key '/etc/kernel/keys/module-signing.key' was not found
//	Could not open file or uri for loading private key from /var/lib/sbctl/keys/db/db.key
//
// The second prints its path bare, before the ERROR block, next to an OpenSSL
// RELATIVE path (`../openssl-3.6.3/crypto/bio/bss_file.c`) that must not win.
// Each filter exists because passing it wrongly freezes a package:
//
//  1. The phase must be the host's (hostCheckPhases); no phase line is a refusal.
//  2. The line must carry a cue that something was missing
//     (unmetPreconditionCues), not merely contain a path.
//  3. The candidate must pass usableAsPrecondition: absolute, not the root
//     directory, not ephemeral.
//
// The first survivor reading top-down wins: the earliest complaint caused the
// ones below it.
func extractUnmetPrecondition(log string) string {
	if !failedOnAHostPhase(log) {
		return ""
	}

	for _, line := range strings.Split(log, "\n") {
		if !reportsSomethingMissing(line) {
			continue
		}
		for _, candidate := range pathCandidates(line) {
			if usableAsPrecondition(candidate) {
				return candidate
			}
		}
	}

	return ""
}

// failedOnAHostPhase reports whether every phase this transcript says failed is
// one whose job was to check the host.
//
// "Every", not "any": a transcript naming both a setup failure and a compile
// failure is one this cannot attribute, and the safe answer to an ambiguous
// transcript is no record. A transcript naming no phase at all — the empty log,
// or a Portage that changed its wording — is likewise a refusal.
func failedOnAHostPhase(log string) bool {
	marks := buildPhaseReport.FindAllStringSubmatch(strings.ToLower(log), -1)
	if len(marks) == 0 {
		return false
	}

	for _, m := range marks {
		if !hostCheckPhases[m[1]] {
			return false
		}
	}
	return true
}

// reportsSomethingMissing reports whether a single transcript line says that a
// named thing was absent or could not be read. Matched lowercased, for the reason
// build_failure.go's enospcReport gives: the same complaint reaches us in
// whatever case the child printed it.
func reportsSomethingMissing(line string) bool {
	lower := strings.ToLower(line)
	for _, cue := range unmetPreconditionCues {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

// pathCandidates returns the things on a line that might be the path it is
// complaining about, most-likely first.
//
// Quoted segments come first because a quoted string is the author saying "this
// is one token": `'/etc/kernel/keys/module-signing.key'` needs no guessing about
// where it ends. Bare whitespace-delimited fields that start with "/" come after,
// which is how the OpenSSL message's trailing path is found. Trailing punctuation
// is stripped from bare fields — a message ends its sentence, a filename does not.
func pathCandidates(line string) []string {
	candidates := quotedSegments(line)

	for _, field := range strings.Fields(line) {
		if strings.HasPrefix(field, "/") {
			candidates = append(candidates, strings.TrimRight(field, `.,;:'"`+"`)]"))
		}
	}

	return candidates
}

// quotedSegments returns the substrings a line wraps in ' , " or ` , in that
// order of quote character and in the order they appear. Text between the first
// and second quote of a kind is a segment, between the third and fourth is the
// next, and an unpaired trailing quote yields nothing — which is what makes an
// apostrophe in prose harmless here: the segment it opens is either never closed
// or is prose, and prose fails usableAsPrecondition.
func quotedSegments(line string) []string {
	var segments []string
	for _, quote := range []string{"'", `"`, "`"} {
		parts := strings.Split(line, quote)
		for i := 1; i < len(parts); i += 2 {
			segments = append(segments, parts[i])
		}
	}
	return segments
}

// usableAsPrecondition reports whether a candidate is something a later run can
// actually ASK ABOUT. Each rejection is a way a recorded answer would be wrong
// forever rather than merely unhelpful:
//
//   - Relative ("keys/module-signing.key"): the pre-check would resolve it
//     against ITS OWN working directory, which is not the build's, so it would
//     answer about a different file every time it was asked. This also excludes
//     a bare word like "signing", which is not a path at all.
//   - The root directory: "/" always exists and always will, so a record naming
//     it says nothing and would clear itself on the first check.
//   - A variable or glob ("$", "*", "?"): a message that printed ${KEYDIR}
//     unexpanded named a path nobody can stat.
//   - Under Portage's build root: created per build and removed after it, so it
//     is absent precisely when nothing is wrong.
func usableAsPrecondition(candidate string) bool {
	switch {
	case !strings.HasPrefix(candidate, "/"):
		return false
	case strings.Trim(candidate, "/") == "":
		return false
	case strings.ContainsAny(candidate, "$*?"):
		return false
	case strings.HasPrefix(candidate, portageBuildRoot):
		return false
	}
	return true
}

// recordUnmetPrecondition stores what a host-caused build failure said was
// missing, so a later run has something to decline the gate on instead of paying
// for the identical failure again.
//
// Nothing here can fail the apply, and that is the point twice over. The apply
// has ALREADY failed — the build died — so a cache that could not be written is
// not a second reason to fail it, and a transcript that yielded no path is the
// fail-open case: the package is retried next time, exactly as it is today.
// Both are logged, because a diagnosis nobody can see is a diagnosis nobody can
// act on, and the operator's action here is on the host (chmod, or a key that
// needs creating), not on the ebuild.
func (a *Applier) recordUnmetPrecondition(pkg, transcript string) {
	required := extractUnmetPrecondition(transcript)
	if required == "" {
		a.logger().Debug("the build failed on the environment but the transcript did not name a path; nothing recorded, so the package will be retried",
			"package", pkg)
		return
	}

	// An Applier built without a config directory has nowhere to write. Only
	// tests construct one that way; production goes through NewApplier, which is
	// always given the directory cache.json lives in.
	if a.configDir == "" {
		return
	}

	cache, err := fetch.NewCache(a.configDir)
	if err != nil {
		a.logger().Warn("could not open the cache to record the unmet precondition", "package", pkg, "precondition", required, "err", err)
		return
	}

	if err := cache.SetPrecondition(pkg, required); err != nil {
		a.logger().Warn("could not record the unmet precondition", "package", pkg, "precondition", required, "err", err)
		return
	}

	a.logger().Info("recorded the unmet precondition — the build needs it and this host does not give it to the portage user",
		"package", pkg, "precondition", required)
}

// --- the RE-CHECKING half ----------------------------------------------------
//
// Everything above WRITES a record. Everything below decides, on a later run,
// whether that record still holds — which is the only thing that ever ends one.
// There is no TTL and no flag, deliberately: see PreconditionRecord.RecordedAt.

// buildUserCanRead reports whether the build could read path, asked from the
// BUILD's vantage point rather than from this process's. It is not os.Stat: the
// gate escalates to `sudo ebuild`, so Portage's FEATURES="userpriv userfetch"
// makes the build read as uid `portage` (see portage_access.go). Both failures
// it was written from are files that EXIST — a key behind a 0700 root:root
// directory and one at 0400 root:root — so only the mode bits tell them apart.
//
// A wrong "cannot read" freezes the package FOREVER, since only this function
// clears a record; a wrong "can read" costs one build that fails as it did
// before. So whatever it cannot decide answers TRUE: no `portage` group on the
// host (the grants are a no-op there, so its refusals must be too), or a stat
// that failed other than "not there" (THIS process cannot traverse it, which
// says nothing about who can). Absence is the one sure negative.
//
// Owner bits are not modelled — there is only portageGroupID, no uid seam — so
// a file owned BY `portage` and closed to others reads as unreadable. A record
// never yields that shape, but an operator "fixing" it with `chown portage:`
// and no g+r keeps the package held; the decline names the path for them.
func buildUserCanRead(path string) bool {
	gid, ok := portageGroupID()
	if !ok {
		return true
	}

	// The containing directory is checked FIRST. A file behind a directory the
	// build user cannot enter is unreadable whatever its own mode says, and
	// /etc/kernel/keys is exactly that: 0700 root:root. The answer is decided
	// there, which matters because a non-root caller cannot stat through it
	// either — asking about the file would yield EACCES and no information.
	if !buildUserCanTraverse(filepath.Dir(path), gid) {
		return false
	}

	// Following the link, not lstat: what a build reads is the target, and the
	// target's mode is what refuses it.
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false
	case err != nil:
		return true
	}
	return modeGrants(info, gid, 0o004, 0o040)
}

// buildUserCanTraverse reports whether the build user could pass THROUGH dir. A
// directory that is not there is not traversable — the path below it does not
// exist either — while any other stat failure is this process's problem and
// answers permissively, for buildUserCanRead's reasons.
//
// # It checks ONE directory, and stopping there is a decision
//
// The parent is the ancestor that is about THIS path: it is the directory the
// key was put in, and in the case this story was written from it is the defect
// itself. Every level above it is a fact about a whole subtree — /etc, /var/lib
// — and climbing to / would buy one more chance per level of answering "unmet"
// wrongly, which is the direction that freezes a package forever, in exchange
// for detecting a shape no observed failure has: a closed grandparent under an
// open parent.
//
// Getting that shape wrong is cheap and self-correcting. The gate runs, the
// build fails on the same unreachable path, and the failure is recorded again —
// the feature simply does not help there, which is where it started. Getting the
// other direction wrong is not recoverable by anything the operator can see.
func buildUserCanTraverse(dir string, gid int) bool {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false
	case err != nil:
		return true
	}
	return modeGrants(info, gid, 0o001, 0o010)
}

// modeGrants reports whether an entry's mode hands the build user the bit it
// needs: the OTHER bit unconditionally, or the GROUP bit when the entry belongs
// to the `portage` group — the one group uid `portage` is in (portageGroupName).
func modeGrants(info fs.FileInfo, gid int, other, group fs.FileMode) bool {
	mode := info.Mode().Perm()
	if mode&other != 0 {
		return true
	}
	if mode&group == 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// Unreachable on the platforms this ships to: os.Stat's FileInfo carries
		// a *syscall.Stat_t on every linux build (manifest_failure.go makes the
		// same argument about build tags). It is kept, and answers the permissive
		// way, so that "the group may read it and I cannot tell whose group"
		// can never become "held back forever".
		return true
	}
	return int64(st.Gid) == int64(gid)
}

// unmetPrecondition answers whether pkg is held back by a host precondition that
// is STILL unmet, and clears the record when it is not.
//
// Clearing here, rather than in a sweep or on a timer, is deliberate: the
// event that ends a record is the path becoming readable, and this is the only
// place that ever asks. No flag to pass, no expiry to wait out — the next run
// after the operator's `chmod` runs the gate.
//
// Every failure it meets answers "not held": a cache that will not open, a
// record with no path, an Applier with no config directory. That is the same
// fail-open direction recordUnmetPrecondition takes for the same reason — the
// worst outcome available here is a package suppressed on evidence this run
// could not read.
func (a *Applier) unmetPrecondition(pkg string) (string, bool) {
	// Only tests build an Applier without one; production goes through
	// NewApplier, which is always given the directory cache.json lives in.
	if a.configDir == "" {
		return "", false
	}

	cache, err := fetch.NewCache(a.configDir)
	if err != nil {
		a.logger().Debug("could not open the cache to check for a recorded precondition, so the build gate runs", "package", pkg, "err", err)
		return "", false
	}

	rec, ok := cache.Precondition(pkg)
	if !ok || rec.Path == "" {
		return "", false
	}
	if !buildUserCanRead(rec.Path) {
		return rec.Path, true
	}

	if err := cache.DeletePrecondition(pkg); err != nil {
		// The gate still runs — the precondition IS satisfied, and holding the
		// package back because a cache file could not be rewritten would be the
		// suppression this whole file is written to avoid. The stale record is
		// re-examined, and cleared again, on the next run.
		a.logger().Warn("the precondition is satisfied again but the record could not be cleared",
			"package", pkg, "precondition", rec.Path, "err", err)
		return "", false
	}

	a.logger().Info("the precondition is readable by the build user again; the record is cleared and the build gate runs",
		"package", pkg, "precondition", rec.Path)
	return "", false
}
