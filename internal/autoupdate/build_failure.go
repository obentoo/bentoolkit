// build_failure.go decides, after a staged build has exited non-zero, whether
// the failure belongs to the machine or to the ebuild. It is the gate that keeps
// a machine fault away from the build fixer (S033-R8.5), and it is the sibling of
// manifest_failure.go, which answers the same question about `pkgdev manifest`.
//
// # Why environmentVerdict is NOT reused, and must not be "simplified" back into
//
// applier.go:1487's environmentVerdict answers this question for the manifest
// step, and it cannot answer it here. It keys on three things — distfiles.
// ErrDistdirNotWritable, distfiles.ErrDistfileLocked and *manifestRunError — and
// a failed BUILD carries none of them: it carries an exit status and a
// transcript. Handed one, environmentVerdict matches nothing and falls straight
// through to nil, which means "repairable" — INCLUDING for a compile that died
// because the build device filled up. That is precisely the answer story 030
// removed from the manifest path, so routing build failures through it would
// reinstate the defect on a new path while looking like tidy code reuse.
//
// What IS reused is story 030's recorded lesson (applier.go:1511): mark the
// PHASE, do not enumerate causes. An enumeration means every future way a build
// can die on the host is a clause somebody must remember to add, and the clause
// that gets forgotten is the one that spends an agent invocation on a correct
// ebuild.
//
// # Observation where it is possible, the transcript where it is not
//
// manifest_failure.go reads no message at all, on the grounds that a message is
// locale-dependent and therefore not evidence. Three of the four rungs below keep
// that discipline: dependencies are Portage's own answer, and PORTAGE_TMPDIR's
// writability is a probe that either writes or does not.
//
// Rung 3a is the exception and it is a deliberate one. An out-of-space build
// cannot be observed after the fact: the failing build's own aborted writes and
// Portage's cleanup give the space back, so by the time anything could statfs the
// device it has room again, and the only surviving evidence is what the child
// printed. The cost of the compromise is stated plainly at reportsNoSpaceLeft.
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
// of the bargain story 030 struck (applier.go:1462): a wrong classification must
// cost a wasted fixer invocation, never a lost repair.
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
// The rungs are checked in the order design D6 states them, and the order is the
// order of CONFIDENCE, not of cost: Portage's own answer about dependencies
// outranks a phase marker, and a phase marker outranks a string in a log.
//
//  1. Unsatisfied dependencies. `ebuild … compile` never installs anything, so a
//     candidate whose build dependencies are simply absent here fails for a reason
//     that has nothing to do with the bump.
//
//  2. The phase. A run that never got INTO src_prepare died in setup, in the
//     fetch or in unpack — the host's and the distfile's business. From prepare
//     onward it is the ebuild's, which is why the marker asked about is the one
//     that opens prepare and not the one that closes it; see
//     validate.SourcePrepareStarted for the full argument.
//
//  3. The two sentinels for host faults a phase cannot distinguish, because both
//     strike from prepare onward and would otherwise read as the ebuild's fault:
//     the build device running out of space, and an unwritable PORTAGE_TMPDIR.
//
// It never spawns anything and never writes anything except the probe file
// distfiles.Probe creates and removes; every input it reasons from was gathered
// by the caller. A nil-safe zero evidence value yields nil — no evidence is not a
// verdict.
func buildFaultVerdict(ev buildFaultEvidence) error {
	// Rung 1 — Portage's own answer, and only when it gave one.
	if ev.deps.determined && !ev.deps.satisfied {
		return fmt.Errorf("%w: %w: %s", ErrBuildEnvironment, errBuildDependenciesMissing,
			strings.Join(ev.deps.missing, ", "))
	}

	// Rung 2 — the phase, marked rather than enumerated (story 030's lesson).
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
// # Why this reads a message when manifest_failure.go bans reading messages
//
// Because here there is nothing else left to read. A full device is the one host
// fault that ERASES ITS OWN EVIDENCE: the failing build's partial objects are
// removed and Portage cleans its work directory, so a statfs taken after the exit
// finds a device with room and would answer "repairable" on a machine that just
// failed for exactly this. The transcript is the only witness that survives.
//
// # What it costs, and in which direction
//
// The match is the C-locale strerror, so a build child running under a translated
// locale is NOT recognised — the applier's compile gate inherits the invoking
// environment through sudo/doas, so that is a real case and not a theoretical
// one. Its consequence is one wasted fixer invocation on a machine that was out
// of space, which is the direction story 030 chose deliberately (applier.go:1462):
// a wrong classification must cost a wasted invocation, never a lost repair.
//
// The other direction — an upstream source tree that happens to contain this
// exact sentence, in an error-message table, being read as a full device — costs
// a repair that was available. It is accepted because the message names itself in
// the operator's error, so the misdiagnosis is visible rather than silent, and
// because the string is only ever looked for in a transcript that ALREADY failed.
func reportsNoSpaceLeft(transcript string) bool {
	return strings.Contains(strings.ToLower(transcript), enospcReport)
}
