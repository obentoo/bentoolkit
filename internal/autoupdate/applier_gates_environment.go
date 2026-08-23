// applier_gates_environment.go is story 043's D3, and it holds both of its
// halves.
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
// Today that answer is thrown away thirteen times over two days: mt7927-dkms and
// edk2 fail in pkg_setup on a key file the `portage` uid cannot read, the failure
// is correctly classified as the host's, and the next run starts the same build
// again because nothing survives to say what was missing.
//
// # Fail open is a DIRECTION, not defensive coding
//
// Everything below refuses to answer far more readily than it answers, and that
// asymmetry is the whole design. The two outcomes are not symmetric:
//
//   - Record nothing when something was missing → the package is retried, which
//     costs a build that was going to be spent anyway. That is today's behaviour,
//     exactly, and it is the floor this change cannot fall below.
//   - Record the WRONG thing → sub-task 3.2's pre-check asks about a path that
//     nothing on the host will ever satisfy, and the package is suppressed
//     FOREVER, silently, on the strength of a parse that failed.
//
// So a message this file does not understand yields "", never a placeholder. The
// direction is the one build_failure.go inherited from story 030 (applier.go:1462)
// pointed at a different cost: a wrong classification must cost a wasted
// invocation, never a lost repair. Here, a wrong extraction must cost a wasted
// build, never a lost package.
//
// # Reading a log is brittle, and it is admitted rather than hidden
//
// Portage's messages are the ebuild author's prose. A wording change breaks the
// extraction, and when it does, the extraction returns "" and the feature simply
// stops helping — it does not start lying. That is the trade design D3 named
// explicitly and accepted.
package autoupdate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/obentoo/bentoolkit/internal/common/logger"
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
// can stand behind (S043-R3.1).
//
// The two failures this was built from, verbatim from the run of 2026-08-22:
//
//	… USE=modules-sign is set but the private key '/etc/kernel/keys/module-signing.key' was not found
//	Could not open file or uri for loading private key from /var/lib/sbctl/keys/db/db.key
//
// One quotes the path inside the ERROR block, the other prints it bare, several
// lines before the ERROR block, next to an OpenSSL diagnostic that carries a
// RELATIVE path (`../openssl-3.6.3/crypto/bio/bss_file.c`) which must not be
// mistaken for it.
//
// Three filters stand between a transcript and an answer, and each one exists
// because passing it wrongly freezes a package:
//
//  1. The phase must be the host's (hostCheckPhases). No phase line at all is a
//     refusal, not a pass: an unrecognised transcript is one this cannot reason
//     about.
//  2. The line must carry a cue that something was missing
//     (unmetPreconditionCues), not merely contain a path.
//  3. The candidate must be usable as a precondition (usableAsPrecondition):
//     absolute, not the root directory, not ephemeral.
//
// The first candidate that survives all three wins, reading top-down, so the
// earliest complaint — the one that caused the ones below it — is the one
// recorded.
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
// for the identical failure again (S043-R3.1).
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
		logger.Debug("%s: the build failed on the environment but the transcript did not name a path; nothing recorded, so the package will be retried", pkg)
		return
	}

	// An Applier built without a config directory has nowhere to write. Only
	// tests construct one that way; production goes through NewApplier, which is
	// always given the directory cache.json lives in.
	if a.configDir == "" {
		return
	}

	cache, err := NewCache(a.configDir)
	if err != nil {
		warnLogf("%s: could not open the cache to record the unmet precondition %s: %v", pkg, required, err)
		return
	}

	if err := cache.SetPrecondition(pkg, required); err != nil {
		warnLogf("%s: could not record the unmet precondition %s: %v", pkg, required, err)
		return
	}

	logger.Info("%s: recorded the unmet precondition %s — the build needs it and this host does not give it to the portage user", pkg, required)
}

// --- the RE-CHECKING half (S043-R3.2, R3.3, R3.4) ---------------------------
//
// Everything above WRITES a record. Everything below decides, on a later run,
// whether that record still holds — which is the only thing that ever ends one.
// There is no TTL and no flag, deliberately: see PreconditionRecord.RecordedAt.

// buildUserCanRead reports whether the build could read path, asked from the
// BUILD's vantage point rather than from this process's (S043-R3.2, Constraint).
//
// # Why this is not os.Stat
//
// The gate escalates to `sudo ebuild`, and running as root is precisely what
// makes Portage honour FEATURES="userpriv userfetch": from that moment the build
// reads as uid `portage`, not as the operator who started the sweep
// (portage_access.go says this at length). A stat from the caller answers a
// DIFFERENT question — "is it there" — and both failures this was written from
// are files that ARE there: /etc/kernel/keys/module-signing.key behind a 0700
// root:root directory, and /var/lib/sbctl/keys/db/db.key at 0400 root:root.
// Existence tells those two apart not at all. The mode bits do.
//
// # The two wrong answers do not cost the same
//
// A wrong "cannot read" freezes the package FOREVER: nothing but this function
// clears a record (R3.3), so a false negative is a package that silently stops
// being updated. A wrong "can read" costs one build that fails the way it failed
// yesterday — which is today's behaviour exactly, and the floor this change
// cannot fall below. So everything this cannot decide answers TRUE:
//
//   - No `portage` group on this host. The build user's identity is then not one
//     this can reason about at all, and its group bits are unreadable to us.
//     portageGroupID's own note says such a host is one where this package's
//     grants are a no-op; its refusals must be a no-op there for the same reason.
//   - A stat that failed for any reason other than "not there" — typically THIS
//     process cannot traverse to it, which says nothing about who can.
//
// Absence is the one negative that is not a guess: a path that is not there is
// unreadable by everyone. It is also the mt7927 case, where the signing key was
// never created at all.
//
// # What it deliberately does not model
//
// Owner bits. There is no uid seam here, only portageGroupID, so a file owned BY
// uid `portage` and closed to everyone else reads as unreadable. That shape
// cannot arise from a record this system writes — the record exists BECAUSE the
// build user could not read the path — but an operator who "fixes" it with
// `chown portage: <key>` and no g+r would keep the package held. The remedy is
// the mode, and the decline names the path so that it is the next thing the
// operator looks at.
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
// is STILL unmet, and clears the record when it is not (S043-R3.2, R3.3).
//
// Clearing here, rather than in a sweep or on a timer, is the whole of R3.3: the
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

	cache, err := NewCache(a.configDir)
	if err != nil {
		logger.Debug("%s: could not open the cache to check for a recorded precondition, so the build gate runs: %v", pkg, err)
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
		warnLogf("%s: the precondition %s is satisfied again but the record could not be cleared: %v", pkg, rec.Path, err)
		return "", false
	}

	logger.Info("%s: the precondition %s is readable by the build user again; the record is cleared and the build gate runs", pkg, rec.Path)
	return "", false
}
