package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/fileutil"
	"github.com/obentoo/bentoolkit/internal/common/procgroup"
)

// BuildRequest is one candidate's build-gate run: which staged tree to build,
// what to call it in the report, how deep to go, and where a failed run's log is
// kept.
//
// Key and Version are the REPORT's names, not the build's: Portage stamps its
// errors with the STAGING repository's name, a repository the operator never
// created, and those errors are re-labelled to Key and Version before they reach
// a report.
//
// RequireIsolation is read by RunBuildGates itself: with isolation required and
// unavailable, NOTHING IS SPAWNED and every covered gate reports SKIPPED naming
// why. A flag no code reads would leave the operator believing a build was
// refused when it in fact ran.
type BuildRequest struct {
	// StagedRoot is the staged repository Stage produced — the repo root, the
	// directory holding profiles/ and the candidate's category directory. It is
	// NOT checked for existence here; see RunBuildGates.
	StagedRoot string

	// Key is the registry key as the operator asked about the package:
	// category/package, possibly carrying ":slot" or "@label".
	Key string

	// Version is the candidate version being validated.
	Version string

	// Depth is how far up the ladder to run. It decides the single phase the one
	// invocation runs, and therefore which gates can be derived at all.
	Depth Depth

	// RequireIsolation refuses to run a build this host cannot isolate. It is
	// consumed by the isolation policy rather than by the phase derivation.
	RequireIsolation bool

	// LogDir is where a FAILED run's log is retained. An empty
	// LogDir retains nothing and says so in the gate's reason, because a log
	// nobody was told about is a log nobody will read.
	LogDir string

	// Distdir is the directory this run resolved for the build's archives. It is
	// SET on the child as DISTDIR when it is non-empty.
	//
	// It is a COMPUTED input — resolved BY this run, from --distdir or from the
	// private directory an apply's own manifest step filled — so it is assigned
	// explicitly rather than admitted through allowedBuildEnv, which filters the
	// PARENT's environment. When DISTDIR sat on that allow-list the gate ran
	// against whatever distdir the operator's shell named, and --distdir never
	// reached the fetch. The allow-list keeps meaning one thing: NOTHING crosses
	// in from outside.
	//
	// EMPTY RESOLVES TO NOTHING. No DISTDIR is set and none is invented, so
	// Portage answers from its own configuration — the honest answer for a run
	// that resolved no directory of its own.
	Distdir string
}

// The phase markers `ebuild` prints, START and DONE for each phase, measured on
// the maintainer's host. They are written out as constants because
// they are the entire evidence this file reasons from: a marker that drifts is
// not a cosmetic change, it is every gate silently reporting "no evidence".
//
// setup has no marker of its own, which is why phaseSetup below is spelled
// "setup or unpack" rather than claimed as one or the other.
const (
	markerStartUnpack    = ">>> Unpacking source"
	markerDoneUnpack     = ">>> Source unpacked in"
	markerStartPrepare   = ">>> Preparing source in"
	markerDonePrepare    = ">>> Source prepared."
	markerStartConfigure = ">>> Configuring source in"
	markerDoneConfigure  = ">>> Source configured."
	markerStartCompile   = ">>> Compiling source in"
	markerDoneCompile    = ">>> Source compiled."

	// THE TRAILING SPACE IS PART OF BOTH, and it is not a typo to be tidied
	// away. Portage interpolates ${CATEGORY}/${PF} immediately after each of
	// these, and matchPhaseMarker compares by PREFIX: without the space
	// ">>> Install" would also match a hypothetical ">>> Installing", and with
	// the package name baked in it would match nothing at all.
	//
	// Source: /usr/lib/portage/python3.14/phase-functions.sh:636 prints
	// `>>> Install ${CATEGORY}/${PF} into ${D}` and :654 prints
	// `>>> Completed installing ${CATEGORY}/${PF} into ${D}`, both through
	// __vecho, which suppresses them under __quiet_mode — so this gate must keep
	// passing no --quiet. An exported PORTAGE_QUIET does reach the child (the
	// PORTAGE_ prefix is allow-listed) and does suppress them, but that yields no
	// false green: completed() never sees the done marker, derive() takes its
	// default branch, and the gate reports SKIPPED quoting the line it needed.
	markerStartInstall = ">>> Install "
	markerDoneInstall  = ">>> Completed installing "
)

// SourcePrepareStarted reports whether a build transcript shows that src_prepare
// had BEGUN — that the run got as far as the first phase the ebuild itself
// drives.
//
// It is exported for the applier's build-fix gate, which must decide whether a
// failed build is the ebuild's fault before handing it to an agent. It asks
// HERE rather than grepping for the marker itself, so the marker is spelled in
// one package and cannot drift silently.
//
// The predicate is the START of prepare, not `>>> Source prepared.`: a failure
// in setup or unpack is a host or distfile fault, from prepare onward it is the
// ebuild's. A patch that no longer applies fails BETWEEN the two markers and is
// the ebuild's fault (derive() reports the patches gate FAILED), so keying on
// the DONE marker would refuse a repair for the most common bump breakage.
//
// The transcript is read with ANSI escapes removed (see stripANSI): Portage
// colours these markers whenever a terminal is attached, and the applier's
// compile gate attaches a real one.
func SourcePrepareStarted(transcript string) bool {
	return strings.Contains(stripANSI(transcript), markerStartPrepare)
}

// excerptLines bounds how much of a failed run's tail is quoted into findings.
// The whole log is retained and named, so this is a summary and not the
// evidence: a compile that failed after ten thousand lines must not put ten
// thousand lines into a JSON report.
const excerptLines = 12

// patchNamesShown bounds how many patch names one PASS reason lists, for the
// same reason: a package carrying thirty patches should not turn one gate's
// reason into a page. The COUNT is always exact.
const patchNamesShown = 6

// labelUnverifiedIsolation is the applier's own wording for a pass that ran
// without a proven network namespace, reused verbatim rather than reworded. The
// applier prints it as `Compile: PASS (unverified isolation)` and the
// --require-isolation flag's help text quotes it; a second phrasing here would
// have one tool describe the same fidelity two ways, and an operator grepping a
// sweep for the weaker passes would find half of them.
const labelUnverifiedIsolation = "unverified isolation"

// buildEnvAllowed is the whole set of environment variables that crosses into
// the build child by NAME, and buildEnvAllowedPrefix the one family that crosses
// by prefix.
//
// An allow-list, not a deny-list: a stray variable exported in an interactive
// shell once broke a configure inside `emerge`. A deny-list removes only the
// variables somebody knew to name, so the next stray one makes the gate report a
// FAILED the operator's shell manufactured; an allow-list fails reproducibly on
// every host instead. It also keeps a token exported in the shell out of a child
// whose log this package retains on disk.
//
// PATH finds the toolchain; HOME holds Portage's and the compilers' caches; TERM
// keeps attached output legible; PORTAGE_* carries the staged-build
// configuration (PORTAGE_TMPDIR, PORTAGE_REPOSITORIES); FEATURES and MAKEOPTS
// are the two knobs an operator legitimately overrides per run.
//
// DISTDIR is NOT here: the list filters the PARENT's environment, so listing it
// would let the invoking shell's value compete with BuildRequest.Distdir and
// leave exec.Cmd's duplicate-key behaviour to choose between them.
var buildEnvAllowed = []string{"PATH", "HOME", "TERM", "FEATURES", "MAKEOPTS"}

// buildEnvAllowedPrefix is the family that crosses whole: every variable Portage
// itself reads is spelled PORTAGE_something, and enumerating them would be a
// list that goes stale the next time Portage adds one.
const buildEnvAllowedPrefix = "PORTAGE_"

// privilegeTools are the escalation tools, doas before sudo — the same order and
// the same preference as the applier's detectPrivilegeTool, so the two cannot
// name different tools on one host.
var privilegeTools = []string{"doas", "sudo"}

// RunBuildGates runs the staged candidate's build phases ONCE and reports every
// build gate the depth covers from that single run.
//
// It invokes the DEEPEST phase the depth requires exactly once and derives every
// shallower gate from the markers `ebuild` prints: the patch gate passes at
// `>>> Source prepared.` (src_prepare applies the patches the way the build
// will), the configure gate at `>>> Source configured.`, the compile gate on a
// zero exit. A failure is attributed to the last phase that STARTED; a run that
// dies before prepare finished proved nothing about the patches and reports
// SKIPPED. Every covered gate carries its own outcome, never silence.
//
// The build runs AS THE INVOKING USER, with an allow-listed environment (see
// buildEnvAllowed): escalation would stop a sweep at a password prompt. When
// isolation is REQUIRED and unavailable nothing is spawned and every gate
// reports SKIPPED; otherwise every PASS says `unverified isolation`.
//
// The error is about the REQUEST (a malformed atom, no version, no staged
// tree), never the build: a failed build, or a host with no `ebuild`, returns
// gates and a nil error. No timeout: a legitimate compile can run for hours.
func RunBuildGates(ctx context.Context, req BuildRequest, deps BuildDeps) ([]GateResult, error) {
	phase, runs := deepestPhaseFor(req.Depth)
	if !runs {
		// Below DepthPatches nothing is built, so there is no build gate to
		// report — an empty list, not a hollow pass.
		return nil, nil
	}

	atom := strings.TrimSpace(req.Key)
	version := strings.TrimSpace(req.Version)
	stagedRoot := strings.TrimSpace(req.StagedRoot)

	// The atom is split first because the split is also the validator: a
	// malformed atom must fail as a malformed atom rather than as an `ebuild`
	// invocation pointed somewhere unintended. splitContentAtom, because this
	// split names the candidate INSIDE the staged repository — Stage writes that
	// content from the suffix-stripped key (role B), so a registry
	// key's ":slot" or "@label" reaching the path handed to `ebuild` below would
	// name a file that was never staged.
	category, pkg, err := splitContentAtom(atom)
	if err != nil {
		// Wrapped rather than passed through: the split's own words name the key,
		// and this names what was being attempted with it.
		return nil, fmt.Errorf("running the build gates: %w", err)
	}
	// Role A's half of the same key: the staged repository's NAME was written
	// from the SUFFIXED package (stage.go), so the recomputing fallback in
	// stagedRepoNameAt must be handed the same one — a clean package here would
	// have a tree missing its repo_name file resolve under a name Stage never
	// wrote. splitStagedAtom words its errors for Stage, hence the same wrap.
	_, suffixedPkg, err := splitStagedAtom(atom)
	if err != nil {
		return nil, fmt.Errorf("running the build gates: %w", err)
	}
	if version == "" {
		return nil, fmt.Errorf("running the build gates for %s: no version given, so no candidate ebuild can be named", atom)
	}
	if stagedRoot == "" {
		return nil, fmt.Errorf("running the build gates for %s-%s: no staged tree given to build from", atom, version)
	}

	// label.pv keeps the FULL key: it is the report's name for the operator,
	// who knows the bump by its registry spelling, suffix and all.
	label := reportLabel{pv: atom + "-" + version, stagedRepo: stagedRepoNameAt(stagedRoot, suffixedPkg, version)}

	// Measured BEFORE anything is looked up or spawned: a build
	// --require-isolation will refuse must not first cost this host a lookup, a
	// fetch or a question. The answer is needed on both paths —
	// it refuses the run on one and labels the pass on the other.
	isolated, isolationReason := deps.isolationProbe()()
	if !isolated && req.RequireIsolation {
		refused := isolationRefusedReason(label.pv, isolationReason, availablePrivilegeTool(deps.binaryLookup()))
		// DeclineHost: an isolation refusal is this machine saying it
		// has no privilege to build under the conditions the caller demanded. It
		// is not a fact about the candidate, so it must not withdraw the bump.
		return declinedGates(req.Depth, label.clean(refused), DeclineHost), nil
	}

	// A host with no Portage produces NO ANSWER, never a cheerful one — the same
	// rule DependenciesSatisfied applies to `emerge`, and the reason the lookup
	// is a seam of its own.
	if _, err := deps.binaryLookup()("ebuild"); err != nil {
		// DeclineHost, and the reason says so in its own words too: a machine
		// with no Portage cannot answer for any ebuild. Every candidate would
		// decline here identically, which is the definition of a host cause.
		return declinedGates(req.Depth, label.clean(fmt.Sprintf(
			"ebuild was not found on PATH, so no build phase could be run for %s on this host: %v", label.pv, err)), DeclineHost), nil
	}

	// `ebuild` is invoked DIRECTLY, as the user who started the sweep — no sudo,
	// no doas. Measured: membership in `portage` is enough
	// for the whole unpack → prepare → configure cycle, while `sudo -n` on that
	// same host answers "interactive authentication is required". So escalating
	// would not buy a build that works, it would buy a sweep that stops at a
	// password prompt nobody is there to answer.
	//
	// `ebuild` discovers the repository from the path it is given and from its
	// working directory, so Dir is what decides which tree is built: the staged
	// one, never the published overlay.
	cmd := deps.commandFactory()(ctx, "ebuild",
		filepath.Join(stagedRoot, category, pkg, pkg+"-"+version+".ebuild"), "clean", phase.String())
	cmd.Dir = stagedRoot

	// The build runs in its OWN process group, so a cancelled
	// context stops all of it — make, gcc and every helper that inherited the
	// output pipe — and not `ebuild` alone, whose orphans held that pipe open and
	// kept this call blocked until the last of them finished. procgroup.Group sets
	// cmd.Cancel, which os/exec accepts only on a command exec.CommandContext
	// built: the shape commandFactory's signature already promises.
	//
	// Leaving the terminal's foreground group has a price: a child that
	// reads the terminal from a background group is stopped by SIGTTIN, and a
	// build stopped that way never ends. So its standard input is EMPTY and
	// `ebuild` reads EOF at once. It is an empty reader rather than nil because
	// the attached-run convention (tui.RunAttached, and the CLI's runner around
	// it) hands a nil Stdin the terminal and keeps one the caller set.
	procgroup.Group(cmd)
	cmd.Stdin = strings.NewReader("")

	// Set HERE rather than left to the runner, because a nil cmd.Env means
	// "inherit os.Environ() wholesale" — the allow-list has to be installed on the
	// command itself or it is not installed at all. It also survives a runner that
	// rebinds the child's streams, which is what the TUI's RunAttached does
	// (overlay_autoupdate.go): that override touches Stdout and Stderr, Stdin only
	// when it was left nil, and never Env, so what is set here is what the child
	// gets.
	cmd.Env = allowedBuildEnv(os.Environ())

	// src_test is disabled for the INSTALL PHASE ONLY, so that runs which do not
	// ask for that rung keep their environment.
	//
	// src_test runs between compile and install, so a -test imposed on a
	// patches, configure or compile run subtracts a feature that phase would
	// never have reached: provably inert, and still a change to the environment
	// of every existing gate. The ladder promises that a run which does not ask
	// for the new rung behaves exactly as it did before, in cost AND in output,
	// and the cheapest way to keep that promise is to not touch those runs at
	// all. deepestPhaseFor has already resolved the phase by the time we get
	// here, so the condition costs one comparison.
	if phase == phaseInstall {
		cmd.Env = withSrcTestDisabled(cmd.Env)
	}

	// The resolved distdir is ASSIGNED, not allow-listed, and the two are
	// different mechanisms on purpose: the filter above exists so the parent's
	// environment cannot leak in, while this is an input THIS RUN computed. With
	// DISTDIR off buildEnvAllowed, the line below is the single source of the
	// variable — the child gets the directory the operator resolved, and the
	// invoking shell's own DISTDIR cannot compete with it.
	//
	// Empty resolves to NOTHING: no assignment, no invented default, the
	// environment left exactly as the allow-list built it. Appending
	// `DISTDIR=` instead would point the fetch at the process's working
	// directory, which is a value this package made up.
	if req.Distdir != "" {
		cmd.Env = append(cmd.Env, "DISTDIR="+req.Distdir)
	}

	output, runErr := deps.attachedRunner()(cmd)
	// Group mode sets WaitDelay, so a build that exited 0 while a helper it left
	// behind still held the output pipe comes back as exec.ErrWaitDelay. That is
	// a success, and procgroup.Result says so; every other error stays.
	runErr = procgroup.Result(cmd, runErr)

	// An interrupted run is not a verdict on the ebuild — and it is not a SKIP
	// either. IT IS AN ERROR.
	//
	// A cancelled context kills the child's group, and derive's attribution rule
	// would then report FAILED: the operator pressed Ctrl-C and was told their
	// ebuild is broken. Returning SkippedGates is worse: PromotionDecision
	// promotes on PASS-or-SKIPPED, so an interrupted `--apply --depth=compile`
	// would PUBLISH the bump. Every gate list is a statement about the candidate,
	// and there is nothing to state.
	//
	// So the error travels, wrapped for errors.Is. The applier fails the apply,
	// realign's Prove returns without a verdict, and validate's Run aborts the
	// sweep; none can publish on it. The retained log is still written: a partial
	// transcript of an interrupted compile is evidence someone may want.
	if ctxErr := ctx.Err(); ctxErr != nil {
		note := retainedLogNote(req.LogDir, atom, version, output)
		return nil, fmt.Errorf("the run was interrupted while %s was building, so no phase reached a verdict "+
			"and nothing here says anything about this ebuild%s: %w", label.pv, note, ctxErr)
	}

	// The transcript is read with ANSI escapes removed and the LOG is not.
	// Portage colours einfo through a TTY, and an attached run therefore carries
	// escapes around the very bullets and markers every gate below greps for; a
	// tracer that missed them would report "no evidence" for a perfectly good
	// build. The retained bytes stay exactly as the child produced them — see
	// reportLabel for the same report/evidence asymmetry.
	transcript := stripANSI(string(output))
	run := buildRun{
		label:        label,
		phase:        phase,
		trace:        tracePhases(transcript),
		transcript:   transcript,
		runErr:       runErr,
		fidelityNote: IsolationFidelityNote(isolated, isolationReason),
		distdirNote:  DistdirEvidenceNote(req.Distdir),
	}
	if runErr != nil {
		run.logNote = retainedLogNote(req.LogDir, atom, version, output)
	}

	var gates []GateResult
	eachBuildGate(req.Depth, func(gate string, gatePhase buildPhase) {
		gates = append(gates, run.gateFor(gate, gatePhase))
	})
	return gates, nil
}

// eachBuildGate calls visit for every build gate depth d covers, in the order
// the phases run, naming the phase each gate reports on.
//
// It is shared with skippedGates because both have to answer the same question —
// which gates does this depth owe an outcome for — and two walks of the ladder
// would be two places to update when a rung is added, with the bug being a gate
// that is reported when it skips and absent when it runs.
//
// THE LADDER IS CUMULATIVE, so a compile-deep request visits patches, configure
// AND compile. It walks depthLadder rather than switching on d, so a rung added
// to the ladder is a rung this covers, and the ladder's shallowest-first
// declaration is what makes the visit order the order the phases ran in.
//
// A depth below DepthPatches builds nothing and correctly visits nothing.
func eachBuildGate(d Depth, visit func(gate string, phase buildPhase)) {
	for _, rung := range depthLadder {
		if rung > d {
			// depthLadder is declared shallowest first, and that ordering is the
			// contract Depth states; see depth.go.
			break
		}
		gate, isBuild := buildGates[rung]
		phase, builds := deepestPhaseFor(rung)
		if !isBuild || !builds {
			continue
		}
		visit(gate, phase)
	}
}

// allowedBuildEnv is the environment the build child receives: the allow-listed
// variables of the parent's, and nothing else.
//
// It NEVER returns nil, and that is the whole contract. os/exec reads a nil Env
// as "inherit the parent's", so a filter that returned nil on a host where none
// of the allow-listed variables happened to be set would hand the child exactly
// the shell this function exists to keep out of it — the failure would be silent
// and would look like the feature working.
//
// The parent's own environment is read and not touched. A gate that exported or
// unset anything here would be changing the process every other gate, and the
// operator's own next command, runs in.
func allowedBuildEnv(parent []string) []string {
	env := make([]string, 0, len(buildEnvAllowed))
	for _, kv := range parent {
		name, _, assigned := strings.Cut(kv, "=")
		if assigned && buildEnvAllows(name) {
			env = append(env, kv)
		}
	}
	return env
}

// withSrcTestDisabled returns env with src_test subtracted from FEATURES, as
// exactly ONE assignment.
//
// The value is composed rather than appended as a second entry: allowedBuildEnv
// may already have placed a FEATURES entry, and two would leave exec.Cmd's
// duplicate-key behaviour to choose between them.
//
// Appending ` -test` SUBTRACTS rather than overwrites because FEATURES is
// INCREMENTAL — measured on the maintainer's host: 42 features at baseline, and
// FEATURES="-userpriv" yields 41 with only that one gone (measured against a
// feature the host HAS, since it does not carry `test`). So sandbox,
// network-sandbox, userpriv, ccache and the rest of the host's configuration
// survive.
//
// The LAST assignment is the one read, matching os/exec's own dedupEnv:
// composing from an earlier duplicate would disable src_test in a value the
// child never sees.
func withSrcTestDisabled(env []string) []string {
	const key = "FEATURES="

	value, found := "", false
	for _, kv := range env {
		if after, ok := strings.CutPrefix(kv, key); ok {
			value, found = after, true
		}
	}

	// TrimSpace so a parent carrying no value yields `FEATURES=-test` rather
	// than `FEATURES= -test`, whose empty first token is a value nobody wrote.
	composed := key + strings.TrimSpace(value+" -test")

	out := make([]string, 0, len(env)+1)
	if !found {
		return append(append(out, env...), composed)
	}

	// Replaced IN THE FIRST ENTRY'S POSITION, and any further duplicate the
	// parent carried is dropped: the contract is "exactly one".
	replaced := false
	for _, kv := range env {
		if !strings.HasPrefix(kv, key) {
			out = append(out, kv)
			continue
		}
		if !replaced {
			out, replaced = append(out, composed), true
		}
	}
	return out
}

// buildEnvAllows reports whether one variable NAME crosses into the build.
func buildEnvAllows(name string) bool {
	if strings.HasPrefix(name, buildEnvAllowedPrefix) {
		return true
	}
	return slices.Contains(buildEnvAllowed, name)
}

// availablePrivilegeTool names the escalation tool this host has, or "" when it
// has none.
//
// It is asked ONLY on the path that has already decided to skip, and it is asked
// through the LookPath seam rather than by running anything: `sudo -n` was
// measured to PROMPT on the maintainer's host, so a probe that ran
// it would be the very interactive stop this gate exists to avoid. What the
// answer buys is the operator's next step — "install one" and "run this
// attended" are different instructions — not a different outcome.
func availablePrivilegeTool(look func(name string) (string, error)) string {
	for _, tool := range privilegeTools {
		if _, err := look(tool); err == nil {
			return tool
		}
	}
	return ""
}

// isolationRefusedReason is the sentence every gate carries when the run asked
// for isolation this host cannot provide.
//
// It is a SKIP and never a FAILED: the ebuild was not measured and nothing about
// it is known, so reporting a failure would blame a bump for a kernel policy.
// And it is never a silent one — applier.go's runCompile returns ("", nil) here,
// which a caller cannot tell from a pass, and that silence is the defect this
// package removes rather than reproduces.
//
// The reason names three things, because an operator who cannot act on it will
// simply turn the flag off: what was demanded, what the kernel actually said,
// and the two ways forward.
func isolationRefusedReason(pv, probeReason, privTool string) string {
	reason := fmt.Sprintf("isolation was required for %s and this host could not provide it, so no build phase was run", pv)
	if probeReason = strings.TrimSpace(probeReason); probeReason != "" {
		reason += ": " + probeReason
	}

	reason += "; creating a network namespace needs privilege this process does not have"
	if privTool == "" {
		reason += ", and neither doas nor sudo is installed here, so it could not be obtained at all"
	} else {
		reason += fmt.Sprintf(", and the gate does not run %s for it: an unattended sweep cannot answer a password prompt", privTool)
	}

	return reason + fmt.Sprintf(". Either run this where the namespace can be created, or drop --require-isolation to build anyway with every pass labelled %q", labelUnverifiedIsolation)
}

// IsolationFidelityNote is the sentence a PASS carries when the run happened
// without a verified network namespace — the applier's `unverified isolation`
// label, applied to the build gates.
//
// The run happens at all because the default does not move: `unshare --net` is
// denied to an ordinary user on the maintainer's host, so requiring isolation by
// default would turn every build gate there into a SKIPPED. The pass is kept AND
// qualified instead, which makes the weaker default honest rather than quiet.
// It returns "" on a verified run, so the label stays a signal.
//
// It is exported because the applier's privileged `--compile` gate calls it
// directly, while the ladder reaches it through gateFor. Both paths must share
// the SENTENCE: one word — PASS — cannot describe two amounts of evidence, and a
// copy in the applier would drift the first time someone reworded one.
func IsolationFidelityNote(isolated bool, probeReason string) string {
	if isolated {
		return ""
	}

	note := "; this ran with " + labelUnverifiedIsolation +
		", so the build could reach the network and the pass does not prove the sources came from DISTDIR alone"
	if probeReason = strings.TrimSpace(probeReason); probeReason != "" {
		note += " — " + probeReason
	}
	return note
}

// DistdirEvidenceNote is the sentence a PASS carries about the distdir the run
// read from.
//
// A pass asserts hermeticity, and a reader cannot tell a distdir this run
// enforced from the ambient one the host's own Portage configuration names
// unless the pass SAYS which one it exported. It states the ACT, not where the
// archives actually came from, so it can sit in front of the isolation note
// without contradicting it: this sentence says the gate set DISTDIR, that one
// says an unisolated build could have reached past it.
//
// BOTH directions speak: if only the enforced case had a sentence, its absence
// would be ambiguous between "no distdir enforced" and "this reason predates the
// change", so the empty case says the build answered from the host's own
// configuration.
//
// It is exported, like IsolationFidelityNote, so the applier's privileged
// `--compile` gate shares ONE copy of each sentence for the two states it shares
// with this path. Its third state, a directory the privilege tool could not
// carry, exists only there and is worded there.
func DistdirEvidenceNote(distdir string) string {
	if distdir = strings.TrimSpace(distdir); distdir == "" {
		return "; the gate exported no DISTDIR, so the build read whatever the host's own Portage configuration names"
	}
	return fmt.Sprintf("; the gate exported DISTDIR=%s for this build, rather than inheriting one from the invoking shell", distdir)
}

// buildPhase is one `ebuild` phase, ordered as Portage runs them so that "the
// last phase that started" is a comparison rather than a table.
type buildPhase int

const (
	// phaseSetup prints no marker of its own, so it is also what "nothing
	// started" resolves to. Its name says both, because a failure there and a
	// failure in unpack are the same answer to the operator: not the bump's
	// fault.
	phaseSetup buildPhase = iota
	phaseUnpack
	phasePrepare
	phaseConfigure
	phaseCompile

	// phaseInstall runs src_install, which assembles the package image under
	// ${D}. It is the deepest phase this ladder invokes: qmerge is a different
	// activity and is out permanently.
	phaseInstall

	phaseCount
)

// String is both the operator-facing name of a phase AND the argument `ebuild`
// takes for it, which is why prepare, configure and compile are spelled exactly
// as Portage spells them.
func (p buildPhase) String() string {
	switch p {
	case phaseUnpack:
		return "unpack"
	case phasePrepare:
		return "prepare"
	case phaseConfigure:
		return "configure"
	case phaseCompile:
		return "compile"
	case phaseInstall:
		return "install"
	default:
		return "setup or unpack"
	}
}

// deepestPhaseFor answers which single phase a depth has to run, and whether it
// builds anything at all.
//
// THE LADDER IS CUMULATIVE, so this is the only place a depth is turned into a
// phase: a configure-deep request runs `clean configure`, which has already run
// prepare by the time it gets there. Asking for a shallower phase separately is
// exactly the second invocation the one-run design exists to prevent.
func deepestPhaseFor(d Depth) (buildPhase, bool) {
	switch {
	// FIRST, because the switch reads deepest-first: a case placed below
	// `>= DepthCompile` is unreachable, and an install request would quietly
	// receive a compile run.
	case d >= DepthInstall:
		return phaseInstall, true
	case d >= DepthCompile:
		return phaseCompile, true
	case d >= DepthConfigure:
		return phaseConfigure, true
	case d >= DepthPatches:
		return phasePrepare, true
	default:
		return phaseSetup, false
	}
}

// phaseMarker binds one line `ebuild` prints to the phase it reports on, and to
// whether that phase STARTED or FINISHED.
type phaseMarker struct {
	marker string
	phase  buildPhase
	done   bool
}

// phaseMarkers is the whole vocabulary, in the order the phases run. A done
// marker implies a started one — Portage cannot finish a phase it never began —
// so tracePhases records both from it.
var phaseMarkers = []phaseMarker{
	{markerStartUnpack, phaseUnpack, false},
	{markerDoneUnpack, phaseUnpack, true},
	{markerStartPrepare, phasePrepare, false},
	{markerDonePrepare, phasePrepare, true},
	{markerStartConfigure, phaseConfigure, false},
	{markerDoneConfigure, phaseConfigure, true},
	{markerStartCompile, phaseCompile, false},
	{markerDoneCompile, phaseCompile, true},
	{markerStartInstall, phaseInstall, false},
	{markerDoneInstall, phaseInstall, true},
}

// matchPhaseMarker reads one transcript line as a phase marker, if it is one.
func matchPhaseMarker(line string) (phaseMarker, bool) {
	for _, m := range phaseMarkers {
		if strings.HasPrefix(line, m.marker) {
			return m, true
		}
	}
	return phaseMarker{}, false
}

// doneMarker is the line that would have proved a phase finished. It is quoted
// back at the operator when a run exits 0 without printing it, so "this gate
// could not be derived" names the exact evidence that was missing.
func doneMarker(p buildPhase) string {
	for _, m := range phaseMarkers {
		if m.phase == p && m.done {
			return m.marker
		}
	}
	return ""
}

// phaseTrace is what one invocation's output says happened: which phases began,
// which finished, and which patches src_prepare applied.
type phaseTrace struct {
	started   [phaseCount]bool
	completed [phaseCount]bool
	patches   []string
}

// lastStarted is the deepest phase that began — which is the phase a failure
// belongs to. Nothing started resolves to phaseSetup, whose name covers setup
// and unpack together because setup prints no marker to tell them apart.
func (t phaseTrace) lastStarted() buildPhase {
	// The bound is phaseCount-1, NOT the deepest phase spelled by name. It once
	// read `phaseCompile`, and when a rung was added above it the loop would have
	// stopped one phase short and attributed an install failure
	// to compile. Deriving the ceiling from the enum is what keeps the next rung
	// from re-introducing that silently.
	for p := phaseCount - 1; p > phaseSetup; p-- {
		if t.started[p] {
			return p
		}
	}
	return phaseSetup
}

// tracePhases walks the transcript once and records every marker it carries.
//
// Patch names are collected ONLY inside the prepare window — between
// `>>> Preparing source in` and `>>> Source prepared.` — rather than by grepping
// the whole log for "Applying". A compile log is full of sentences, and one of
// them saying "Applying" somewhere would otherwise turn an ebuild that applies
// no patch into one that reports patches it never had, which is precisely the
// distinction this gate must keep.
func tracePhases(transcript string) phaseTrace {
	var trace phaseTrace
	current := phaseSetup

	for raw := range strings.Lines(transcript) {
		line := strings.TrimSpace(raw)

		if marker, ok := matchPhaseMarker(line); ok {
			trace.started[marker.phase] = true
			if marker.done {
				trace.completed[marker.phase] = true
			}
			current = marker.phase
			continue
		}

		if current == phasePrepare && !trace.completed[phasePrepare] {
			if name, ok := appliedPatch(line); ok {
				trace.patches = append(trace.patches, name)
			}
		}
	}
	return trace
}

// appliedPatch reads eapply's own announcement — ` * Applying foo.patch ...` —
// and returns the patch it names. The leading bullet is einfo's, and the
// trailing ellipsis is eapply's; neither is part of the file's name.
func appliedPatch(line string) (string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "*"))
	rest, announced := strings.CutPrefix(rest, "Applying ")
	if !announced {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "..."))
	return name, name != ""
}

// buildRun is one invocation and everything derived from it, so that each gate
// is a reading of the same evidence rather than a re-run.
type buildRun struct {
	label      reportLabel
	trace      phaseTrace
	transcript string
	runErr     error

	// phase is the deepest phase THIS invocation asked for — what
	// deepestPhaseFor resolved. `completed` needs it to know whether the child's
	// exit status is evidence about a given phase or merely evidence that
	// something after it went wrong.
	phase buildPhase

	// logNote is the sentence naming the retained log — or saying why there is
	// none. It is computed once for the run, because one invocation produces one
	// log however many gates cite it.
	logNote string

	// fidelityNote is the sentence a PASS from an unisolated run carries, empty
	// when isolation was verified. One invocation has ONE fidelity, so every gate
	// derived from it says the same thing about it.
	fidelityNote string

	// distdirNote is the sentence a PASS carries about the distdir this run
	// exported, and it is NEVER empty: both directions speak, because an absent
	// sentence cannot be told from a reason written before the gate said this at
	// all. One invocation has ONE distdir, so — like fidelityNote — it is
	// computed once here rather than per gate.
	distdirNote string
}

// gateFor derives one gate's outcome from the run's markers, and is the ONE
// place the staging-name re-labelling, the isolation label and the distdir
// evidence are applied — a funnel, like Stage's single sentinel boundary, so a
// branch added later cannot forget what it never had to remember.
//
// Only a PASS is labelled, because both labels answer an OVERCLAIM and a pass is
// the only outcome that claims to have proved anything. A build that ran with
// the network reachable proved less than one that did not, and a pass asserts
// hermeticity, so it owes the DISTDIR it exported (or that it exported none). A
// FAILED gate is not made less true by either, and a SKIPPED gate measured
// nothing to qualify; a sentence on every outcome would be decoration.
func (r buildRun) gateFor(gate string, phase buildPhase) GateResult {
	result := r.derive(gate, phase)
	if result.Outcome == OutcomePass {
		// ORDER IS DELIBERATE. The isolation note ENDS by qualifying the claim
		// the distdir note makes — "the pass does not prove the sources came from
		// DISTDIR alone" — so the sentence naming the directory has to be read
		// first for that caveat to have an antecedent.
		result.Reason += r.distdirNote
		result.Reason += r.fidelityNote
	}

	result.Reason = r.label.clean(result.Reason)
	for i := range result.Findings {
		result.Findings[i].Detail = r.label.clean(result.Findings[i].Detail)
	}
	return result
}

// derive is gateFor's body, minus the re-labelling: it reports in Portage's own
// words and gateFor is its only caller.
//
// Every branch returns an outcome AND a reason. There is no silent skip here,
// because a caller cannot tell one from a pass.
func (r buildRun) derive(gate string, phase buildPhase) GateResult {
	switch {
	case r.completed(phase):
		return GateResult{Gate: gate, Outcome: OutcomePass, Reason: r.passReason(gate)}

	case r.runErr != nil && r.trace.started[phase]:
		// The phase began and never finished, and the run failed: this is where
		// the bump died.
		return r.failure(gate, phase)

	case r.runErr != nil:
		// The run died before this phase began, so this gate measured nothing.
		//
		// Declined is LEFT UNRECORDED, deliberately. notReachedReason
		// itself says why: a death before `>>> Source prepared.` is "a host or
		// distfile fault" — the ebuild's SRC_URI and the box's network are both
		// live possibilities and this code cannot tell them apart. Guessing
		// `candidate` would withdraw bumps over a flaky mirror; guessing `host`
		// would publish a bump whose sources do not exist. An unrecorded cause
		// keeps today's answer, and the phase that DID die reports FAILED in its
		// own gate whenever that phase is one the depth covers.
		return GateResult{Gate: gate, Outcome: OutcomeSkipped, Reason: r.notReachedReason(gate, phase)}

	default:
		// A zero exit with no marker to prove the phase finished. Rare, and
		// deliberately not read as a pass.
		//
		// Unrecorded for the same reason: unmeasuredReason describes evidence
		// that did not arrive, which names neither the candidate nor the host.
		return GateResult{Gate: gate, Outcome: OutcomeSkipped, Reason: r.unmeasuredReason(phase)}
	}
}

// completed reports whether a phase finished successfully.
//
// compile is the exception: when it is the deepest phase THIS invocation ran,
// the child's own EXIT STATUS is the authority on it. Every shallower phase is
// judged by its marker, because a zero exit at the end says nothing about which
// phases before it ran.
//
// The exception is scoped to r.phase, not to compile alone: on an install-depth
// run a dying src_install makes runErr non-nil, and an unscoped exception would
// report compile FAILED while its own `>>> Source compiled.` marker sits in the
// transcript, sending the operator to the wrong file.
//
// install is NOT given the same exception, deliberately: a zero exit without
// `>>> Completed installing ` is a gate that could not be derived, never a pass.
func (r buildRun) completed(p buildPhase) bool {
	if p == phaseCompile && r.phase == phaseCompile {
		return r.runErr == nil
	}
	return r.trace.completed[p]
}

// passReason states what a pass covered AND what it did not. An outcome names
// its own reach, which is why none of these is empty: a green that does not say
// where it stops reads as "it builds", and that overclaim is what this package
// removes.
func (r buildRun) passReason(gate string) string {
	switch gate {
	case GatePatches:
		// "every patch applied" and "there were no patches" are different
		// answers and must not render alike.
		if len(r.trace.patches) == 0 {
			return fmt.Sprintf("%s applies no patch, so src_prepare had nothing to apply: this gate passed with nothing to do, not because a patch still applies", r.label.pv)
		}
		return fmt.Sprintf("src_prepare applied the ebuild's %s cleanly to the new sources of %s: %s",
			patchCount(len(r.trace.patches)), r.label.pv, namedPatches(r.trace.patches))

	case GateConfigure:
		return fmt.Sprintf("the configure phase completed for %s; a configure pass does not cover compilation, which this depth never ran", r.label.pv)

	case GateCompile:
		// STILL TRUE at compile depth: a compile-depth run does stop there. The
		// install gate's sentence below covers the rung that does not.
		return fmt.Sprintf("the compile phase completed for %s; a compile pass does not cover src_install, which this ladder deliberately stops short of", r.label.pv)

	case GateInstall:
		// Two omissions in one sentence, because they have DIFFERENT CAUSES and an
		// operator reading a green should need neither the ladder's history nor
		// its source to learn what the green bought: qmerge is out of this ladder
		// permanently, while src_test is a subtraction THIS GATE MADE for
		// determinism — one the operator did
		// not ask for, which is exactly why it is stated.
		//
		// It says "assembling the package image under ${D}" and never that
		// anything was installed anywhere: src_install writes an image inside
		// PORTAGE_TMPDIR and merges nothing onto any system.
		return fmt.Sprintf("src_install completed for %s, assembling the package image under ${D}: a pass here does not "+
			"cover qmerge, which stays out of this ladder, and src_test did not run because this gate disables it so the "+
			"verdict is a fact about the candidate rather than about the host", r.label.pv)

	default:
		return fmt.Sprintf("the %s gate passed for %s", gate, r.label.pv)
	}
}

// failure is a gate the run died in: FAILED, with the log named and
// the child's own last words quoted as findings.
//
// The summary finding is always emitted, and always at error severity, so a
// FAILED gate can never carry zero error findings — Report.ExitCode counts those,
// and a failure that exits 0 is the silent pass in another costume.
func (r buildRun) failure(gate string, phase buildPhase) GateResult {
	findings := []Finding{{
		Gate:     gate,
		Severity: SeverityError,
		Detail:   fmt.Sprintf("the %s phase failed for %s: %v", phase, r.label.pv, r.runErr),
	}}
	for _, line := range r.failureExcerpt() {
		findings = append(findings, Finding{Gate: gate, Severity: SeverityError, Detail: line})
	}

	return GateResult{Gate: gate, Outcome: OutcomeFailed, Reason: r.failReason(gate), Findings: findings}
}

// failReason names the phase that failed and where the whole log is.
func (r buildRun) failReason(gate string) string {
	switch gate {
	case GatePatches:
		return fmt.Sprintf("src_prepare failed for %s: a patch the ebuild carries no longer applies to the new sources%s", r.label.pv, r.logNote)
	case GateConfigure:
		return fmt.Sprintf("the configure phase failed for %s%s", r.label.pv, r.logNote)
	case GateCompile:
		return fmt.Sprintf("the compile phase failed for %s%s", r.label.pv, r.logNote)
	default:
		return fmt.Sprintf("the %s gate failed for %s%s", gate, r.label.pv, r.logNote)
	}
}

// notReachedReason explains a gate whose phase never began because the run died
// earlier — and names the phase that did die, so nobody is sent to the wrong
// file.
func (r buildRun) notReachedReason(gate string, phase buildPhase) string {
	reason := fmt.Sprintf("the run failed in the %s phase, before the %s phase started, so this gate measured nothing about %s%s",
		r.trace.lastStarted(), phase, r.label.pv, r.logNote)

	if gate == GatePatches {
		// Said out loud: before `>>> Source prepared.` the failure is the
		// host's or the distfile's, and reporting it as the ebuild's would send a
		// maintainer to fix a patch that is fine.
		reason += fmt.Sprintf("; a failure before `%s` is a host or distfile fault rather than a statement about the ebuild's patches", markerDonePrepare)
	}
	return reason
}

// unmeasuredReason covers the run that exited 0 without printing the marker its
// phase finishes with. It reports SKIPPED and quotes the missing line, because a
// gate with no evidence is not a gate that passed.
func (r buildRun) unmeasuredReason(phase buildPhase) string {
	return fmt.Sprintf("the run exited 0 but its output carries no `%s`, so the %s phase's outcome could not be derived from it",
		doneMarker(phase), phase)
}

// failureExcerpt is the part of the transcript after the last phase marker that
// carries the CAUSE: the lines the failing phase itself produced, where the
// option upstream removed or the header that went missing is actually named. It
// is a SUMMARY; the full log is retained and named by the reason beside it.
//
// The tail is the wrong thing to quote: `die` prints its epilogue (call stack,
// snippet, boilerplate) AFTER the error. On the gst-plugins-qt6-1.29.2 configure
// failure, `meson.build:1:0: ERROR: Unknown option: "aalib".` was the 7th of 24
// non-empty lines and the epilogue the last 16, so the last 12 lines quoted
// none of the cause.
//
// So the window ENDS at die's banner, and the banner is appended: it names the
// atom and the phase, and it is the line `clean` scrubs the staging name from.
// The message `die` was CALLED with, printed right after the banner, is appended
// too — for `emake || die "emake failed"` it is the entire cause. A transcript
// with no banner keeps the old behaviour: the last excerptLines lines.
func (r buildRun) failureExcerpt() []string {
	lines := strings.Split(r.transcript, "\n")

	start := 0
	for i, line := range lines {
		if _, isMarker := matchPhaseMarker(strings.TrimSpace(line)); isMarker {
			start = i + 1
		}
	}

	var excerpt []string
	for _, line := range lines[start:] {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			excerpt = append(excerpt, trimmed)
		}
	}

	// The cause lives before die's banner; the banner names what failed; and
	// die's own message is the line AFTER it.
	if at := dieBannerIndex(excerpt); at >= 0 {
		cause := excerpt[:at]
		if len(cause) > excerptLines {
			cause = cause[len(cause)-excerptLines:]
		}
		quoted := append(append([]string(nil), cause...), excerpt[at])
		return append(quoted, dieMessage(excerpt[at+1:])...)
	}

	if len(excerpt) > excerptLines {
		excerpt = excerpt[len(excerpt)-excerptLines:]
	}
	return excerpt
}

// dieBannerIndex is where Portage's failure epilogue begins, or -1.
//
// The banner is the `ERROR: <atom>::<repo> failed (<phase> phase):` line `die`
// prints before its call stack. It is matched on shape rather than on the atom,
// because the atom in it carries the staging repository's name — the very thing
// `clean` rewrites downstream — so matching the atom here would mean matching a
// string this package deliberately does not let escape.
//
// The FIRST match wins: everything after it is epilogue by construction, and a
// second banner would only appear in a transcript that already failed twice.
func dieBannerIndex(lines []string) int {
	for i, line := range lines {
		if isDieBanner(line) {
			return i
		}
	}
	return -1
}

// isDieBanner is dieBannerIndex's predicate, named so that dieMessage can stop
// at a banner without re-stating the shape it matches on.
func isDieBanner(line string) bool {
	return strings.Contains(line, "ERROR: ") && strings.Contains(line, " failed (") && strings.Contains(line, "phase)")
}

// dieMessageLines bounds how much of die's own message is quoted. One line is
// the overwhelmingly common shape; the cap exists so that a `die` called with an
// unbounded string cannot push the cause out of a summary that is meant to stay
// readable.
const dieMessageLines = 4

// dieMessage is the message `die` was CALLED with: the lines Portage prints
// between its banner and the rest of the epilogue.
//
// # Where it stops, and why each boundary is there
//
// Portage prints the message, then a bare `*` separator, then `Call stack:` and
// the boilerplate this excerpt exists to exclude. Any of the three ends the
// message. A second banner ends it too, because Portage emits the whole banner
// and message twice — once unprefixed and once with the ` * ` prefix — and the
// first copy's message must not swallow the second copy's banner.
//
// Empty is a legitimate answer: a `die` with no message prints none, and the
// banner alone is then genuinely all there is.
func dieMessage(after []string) []string {
	var msg []string
	for _, line := range after {
		if line == "*" || strings.Contains(line, "Call stack:") || isDieBanner(line) {
			break
		}
		msg = append(msg, line)
		if len(msg) == dieMessageLines {
			break
		}
	}
	return msg
}

// patchCount renders the exact number of patches applied, singular or plural.
func patchCount(n int) string {
	if n == 1 {
		return "1 patch"
	}
	return fmt.Sprintf("%d patches", n)
}

// namedPatches lists the patches by name, capped at patchNamesShown so one
// gate's reason stays a sentence. The count beside it is never capped.
func namedPatches(patches []string) string {
	if len(patches) <= patchNamesShown {
		return strings.Join(patches, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(patches[:patchNamesShown], ", "), len(patches)-patchNamesShown)
}

// retainedLogNote retains a failed run's log and returns the sentence naming it,
// for the gates that have to cite it.
//
// It always returns a sentence. A log that could not be written, and a run with
// nowhere to write one, are both said out loud rather than rendered as a report
// that quietly cites nothing — the operator would otherwise go looking for a
// file that was never created.
func retainedLogNote(dir, atom, version string, output []byte) string {
	path, err := retainBuildLog(dir, atom, version, output)
	switch {
	case err != nil:
		return "; the build log could NOT be retained: " + err.Error()
	case path == "":
		return "; no log directory was configured, so the build log was not retained"
	default:
		return "; the full build log is retained at " + path
	}
}

// retainBuildLog writes the child's output, VERBATIM, to one file per
// invocation.
//
// # Verbatim, and that is the point
//
// The report is re-labelled and this is not. The log is the evidence pasted into
// an upstream bug or a pkgdev question, and a log bentoo edited is one nobody can
// compare against their own run — a reader would find text Portage never emitted.
// So the re-labelling belongs to reportLabel, and these bytes are the child's.
//
// The mode is 0600, unchanged from the compile log the applier already writes: a
// build log carries paths, environment and occasionally credentials, and none of
// that is other local users' business.
func retainBuildLog(dir, atom, version string, output []byte) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, stagedDirMode); err != nil {
		return "", fmt.Errorf("creating the log directory %s: %w", dir, err)
	}

	name := fmt.Sprintf("%s-%s-%s.log", strings.ReplaceAll(atom, "/", "_"), version, time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, output, fileutil.CacheFileMode); err != nil {
		return "", fmt.Errorf("writing the build log %s: %w", path, err)
	}
	return path, nil
}

// reportLabel translates Portage's own naming into the operator's before any of
// it reaches a report.
//
// The build runs from a STAGED repository, so Portage stamps its failure with
// that repository's name — measured:
//
//	media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (configure phase)
//
// Nobody asked about bentoo-staging, and nobody should have to learn the staging
// tree's name to read the answer. The `::` qualifier is stripped and the PACKAGE
// TOKEN IS LEFT ALONE, which keeps the VERSION — inside a sweep, the part the
// operator needs most.
//
// It applies to EVERY reason and EVERY finding, on the clean path as well as the
// failing one, because a re-labelling on the failure path alone leaks the first
// time a PASS quotes anything.
type reportLabel struct {
	// pv is the real package and version, `category/package-version`.
	pv string
	// stagedRepo is the name Portage knows the staged tree by, when it can be
	// determined. It is empty on a tree that names itself nothing, and clean is
	// written so that is not a special case.
	stagedRepo string
}

// clean returns text with every staging-repository name replaced by the real
// package's own.
func (l reportLabel) clean(text string) string {
	out := stripRepoQualifiers(text)
	if l.stagedRepo != "" {
		// Whatever survived without a `::` in front of it — Portage names the
		// repository in prose too.
		out = strings.ReplaceAll(out, l.stagedRepo, l.pv)
	}
	return out
}

// stripRepoQualifiers removes every `::<repository>` suffix, leaving the package
// token it was attached to intact.
//
// It matches on SHAPE rather than on the staged repository's name because the
// name in the message is Portage's to choose: a report must not depend on
// bentoolkit having guessed it correctly, and no repository qualifier at all is
// a better answer than one the operator cannot place.
func stripRepoQualifiers(text string) string {
	if !strings.Contains(text, "::") {
		return text
	}

	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] == ':' && i+1 < len(text) && text[i+1] == ':' {
			i += 2
			for i < len(text) && isRepoNameByte(text[i]) {
				i++
			}
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

// isRepoNameByte reports whether a byte can appear in a Portage repository name.
// It is the same set stagedRepoName folds every other character into, so the two
// cannot disagree about where a repository's name ends.
func isRepoNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-':
		return true
	default:
		return false
	}
}

// escByte is the escape character that opens an ANSI control sequence.
const escByte = 0x1b

// stripANSI removes ANSI escape sequences, so that markers and bullets can be
// matched as text.
//
// It exists because Portage COLOURS its output when a terminal is attached:
// einfo's bullet and the phase markers arrive wrapped in escape sequences, and a
// tracer matching on `>>> Source prepared.` would then find nothing and report
// every gate unmeasured. It is applied to the copy this file READS; the retained
// log keeps the raw bytes.
func stripANSI(text string) string {
	if !strings.ContainsRune(text, escByte) {
		return text
	}

	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] != escByte {
			out.WriteByte(text[i])
			i++
			continue
		}
		i++
		if i >= len(text) || text[i] != '[' {
			// Not a CSI sequence: the escape alone is dropped and whatever
			// follows is kept, because guessing at a sequence this does not know
			// would eat text somebody wrote on purpose.
			continue
		}
		i++
		for i < len(text) && text[i] >= 0x20 && text[i] <= 0x3f {
			i++
		}
		if i < len(text) {
			// The final byte, 0x40–0x7e, which ends the sequence.
			i++
		}
	}
	return out.String()
}
