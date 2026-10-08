// build_failure.go decides, after a staged build exits non-zero, whether the
// failure belongs to the machine or to the ebuild: the gate that keeps a
// machine fault away from the build fixer. manifest_failure.go answers the same
// question for `pkgdev manifest`.
//
// environmentVerdict (applier.go) must not be reused here. It keys on
// distfiles.ErrDistdirNotWritable, ErrDistfileLocked and *manifestRunError; a
// failed build carries none of them, so it would answer "repairable" even for
// a compile that died on a full disk, reinstating a defect the manifest path
// removed. What is reused is that path's lesson: mark the PHASE, do not
// enumerate causes, because the cause somebody forgets to list is the one that
// spends an agent invocation on a correct ebuild.
//
// Dependencies and PORTAGE_TMPDIR writability are observed, not read from
// messages. Rung 3a is the deliberate exception: out-of-space cannot be
// observed afterwards (the aborted writes free the space), so the transcript
// is the only evidence; its cost is stated at reportsNoSpaceLeft.

package autoupdate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
)

// ErrBuildEnvironment reports that a failed build failed on the machine rather
// than on the ebuild: the host is missing build dependencies, the run died before
// the ebuild's own phases began, the build device ran out of space, or Portage's
// build directory is not writable.
//
// It is the sentinel the build-fix gate keys on, exactly as ErrManifestEnvironment
// is for the manifest step. An agent handed such a failure has one repair
// available to it — edit the ebuild — and the ebuild is not what failed, so it
// spends turns and money concluding that a correct file is wrong. Callers test
// this with errors.Is; the reason and the build's own error are always wrapped
// alongside, so the operator still reads what actually happened.
var ErrBuildEnvironment = errors.New("the build failed on the environment, not on the ebuild")

// The reason a verdict was reached, wrapped next to ErrBuildEnvironment. They are
// unexported for the reason manifest_failure.go gives about its own: the gate
// keys on ErrBuildEnvironment alone, and these exist for the operator reading the
// message and for the test that pins WHICH rung fired.
var (
	// errBuildDependenciesMissing marks the dependency verdict (rung 1).
	errBuildDependenciesMissing = errors.New("this host does not have build dependencies the candidate needs")
	// errBuildBeforePrepare marks the phase verdict (rung 2).
	errBuildBeforePrepare = errors.New("the build died before src_prepare began, in setup, fetch or unpack, so nothing about the ebuild had been exercised yet")
	// errBuildDeviceFull marks the out-of-space verdict (rung 3a).
	errBuildDeviceFull = errors.New("the build ran out of space on the device it was building on")
	// errBuildTmpdirUnwritable marks the build-directory verdict (rung 3b).
	errBuildTmpdirUnwritable = errors.New("the build directory Portage was told to use (PORTAGE_TMPDIR) is not writable")
)

// enospcReport is strerror(ENOSPC) as a C-locale child prints it. Matched
// lowercased, so the compiler's "No space left on device" and a shell's "no space
// left on device" are the same evidence.
const enospcReport = "no space left on device"

// buildDependencyAnswer is rung 1's input: validate.DependenciesSatisfied's
// three-state answer, flattened to the two facts this gate needs.
//
// The three states do not collapse into a bool, and that is the whole reason this
// type exists. `determined == false` means the host could not be ASKED — no
// Portage, no staged tree, a resolve that failed — and an unanswerable question is
// not a machine fault. Reading a bare `!satisfied` as "a dependency is missing"
// would refuse a repair on the strength of knowing nothing, which is the opposite
// of the bargain the manifest path struck: a wrong classification must cost a
// wasted fixer invocation, never a lost repair.
type buildDependencyAnswer struct {
	// determined reports that Portage answered at all.
	determined bool
	// satisfied is Portage's answer, meaningful only when determined is true.
	satisfied bool
	// missing names the atoms that would have to be installed first. Non-empty
	// only when determined is true and satisfied is false.
	missing []string
}

// buildFaultEvidence is everything the attribution gate was able to LOOK AT, and
// the zero value of each field means "this rung could not be asked", never "this
// rung says no".
//
// It exists so the caller — not the classifier — decides how much evidence is
// worth paying for. Two of the four rungs are free: they read a transcript the
// run already holds. The other two cost a process (a pretend `emerge -p`) and a
// probe write, and spending those is only justified when the alternative is
// spending an agent invocation. So the caller runs the classifier once on the
// free evidence, and again with the paid evidence attached only if a fixer is
// actually about to be invoked. The ORDER of the rungs never changes; only which
// of them have anything to say.
type buildFaultEvidence struct {
	// transcript is the failed build child's captured output.
	transcript string
	// deps is what Portage said about this host's build dependencies.
	deps buildDependencyAnswer
	// buildTmpdir is PORTAGE_TMPDIR as this host reports it, or "" when the host
	// could not be asked (no portageq). Empty means the rung is skipped, never
	// that the directory is fine.
	buildTmpdir string
}

// buildFaultVerdict returns an ErrBuildEnvironment-wrapped verdict when a failed
// build belongs to the machine, and nil when the ebuild may still be at fault.
//
// The rungs run in order of CONFIDENCE, not of cost:
//
//  1. Unsatisfied dependencies: `ebuild … compile` installs nothing, so absent
//     build dependencies fail the build for a reason unrelated to the bump.
//  2. The phase: a run that never got INTO src_prepare died in setup, fetch or
//     unpack — the host's business; from prepare onward it is the ebuild's (see
//     validate.SourcePrepareStarted).
//  3. Host faults a phase cannot tell apart, because both strike after prepare:
//     the build device running out of space, and an unwritable PORTAGE_TMPDIR.
//
// It spawns nothing and writes only the probe file distfiles.Probe creates and
// removes; the caller gathered every input. A zero evidence value yields nil —
// no evidence is not a verdict.
func buildFaultVerdict(ev buildFaultEvidence) error {
	// Rung 1 — Portage's own answer, and only when it gave one.
	if ev.deps.determined && !ev.deps.satisfied {
		return fmt.Errorf("%w: %w: %s", ErrBuildEnvironment, errBuildDependenciesMissing,
			strings.Join(ev.deps.missing, ", "))
	}

	// Rung 2 — the phase, marked rather than enumerated.
	if !validate.SourcePrepareStarted(ev.transcript) {
		return fmt.Errorf("%w: %w", ErrBuildEnvironment, errBuildBeforePrepare)
	}

	// Rung 3a — the build device filled up.
	if reportsNoSpaceLeft(ev.transcript) {
		return fmt.Errorf("%w: %w (the build reported %q)", ErrBuildEnvironment, errBuildDeviceFull, enospcReport)
	}

	// Rung 3b — Portage's build directory stopped being writable. Probe is the
	// same writability proof the manifest classifier uses on the distdir, and it
	// is safe to repeat and to run concurrently by construction.
	if ev.buildTmpdir != "" {
		if probeErr := distfiles.Probe(ev.buildTmpdir); probeErr != nil {
			return fmt.Errorf("%w: %w: %s: %w", ErrBuildEnvironment, errBuildTmpdirUnwritable, ev.buildTmpdir, probeErr)
		}
	}

	return nil
}

// reportsNoSpaceLeft reports whether a build transcript carries the kernel's own
// out-of-space report.
//
// It reads a message, which manifest_failure.go bans, because nothing else is
// left: a full device ERASES ITS OWN EVIDENCE (partial objects are removed and
// Portage cleans its work directory), so a later statfs finds room. The
// transcript is the only witness that survives.
//
// The match is the C-locale strerror, so a child under a translated locale is
// NOT recognised — a real case, since the compile gate inherits the invoking
// environment through sudo/doas. That costs one wasted fixer invocation, the
// chosen direction: a wrong classification must cost a wasted invocation, never
// a lost repair. The reverse — a source tree quoting this sentence, read as a
// full device — costs an available repair; it is accepted because the operator's
// error names the message, and only a transcript that ALREADY failed is read.
func reportsNoSpaceLeft(transcript string) bool {
	return strings.Contains(strings.ToLower(transcript), enospcReport)
}
