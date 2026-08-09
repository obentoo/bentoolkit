package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/fileutil"
)

// BuildRequest is one candidate's build-gate run: which staged tree to build,
// what to call it in the report, how deep to go, and where a failed run's log is
// kept.
//
// # Atom and Version are the REPORT's names, not the build's
//
// The build reads the staged tree, and Portage will stamp its own errors with
// the STAGING repository's name — a repository the operator never created. Atom
// and Version are what those errors are re-labelled to before any of it reaches
// a report (D13). They are therefore not decoration: without them this package
// could only quote Portage, and quoting Portage here means showing somebody a
// repository they have never heard of.
//
// # RequireIsolation is declared here and honoured by the isolation policy
//
// It is present from the start, unread by this file, for the same reason all
// four of BuildDeps' seams are: the policy that consumes it lands next, and a
// struct that grows a field later is a struct whose every call site has to be
// revisited. Declaring it now costs nothing and keeps the request's shape fixed.
type BuildRequest struct {
	// StagedRoot is the staged repository Stage produced — the repo root, the
	// directory holding profiles/ and the candidate's category directory. It is
	// NOT checked for existence here; see RunBuildGates.
	StagedRoot string

	// Atom is the real package, category/package, as the operator asked about it.
	Atom string

	// Version is the candidate version being validated.
	Version string

	// Depth is how far up the ladder to run. It decides the single phase the one
	// invocation runs, and therefore which gates can be derived at all.
	Depth Depth

	// RequireIsolation refuses to run a build this host cannot isolate. It is
	// consumed by the isolation policy rather than by the phase derivation.
	RequireIsolation bool

	// LogDir is where a FAILED run's log is retained (R5.1, R6.5). An empty
	// LogDir retains nothing and says so in the gate's reason, because a log
	// nobody was told about is a log nobody will read.
	LogDir string
}

// The phase markers `ebuild` prints, START and DONE for each phase, measured on
// the maintainer's host (design M-A). They are written out as constants because
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
)

// excerptLines bounds how much of a failed run's tail is quoted into findings.
// The whole log is retained and named, so this is a summary and not the
// evidence: a compile that failed after ten thousand lines must not put ten
// thousand lines into a JSON report.
const excerptLines = 12

// patchNamesShown bounds how many patch names one PASS reason lists, for the
// same reason: a package carrying thirty patches should not turn one gate's
// reason into a page. The COUNT is always exact.
const patchNamesShown = 6

// RunBuildGates runs the staged candidate's build phases ONCE and reports the
// patches, configure and compile gates from that single run (R4, R5, R6).
//
// # One invocation, three gates (D4)
//
// `ebuild <path> clean configure` runs setup, unpack, prepare AND configure, and
// prints a marker as each one starts and finishes. Running a patches gate and
// then a configure gate as two invocations would unpack the same 6 MB tarball
// twice and learn nothing the first run had not already printed. So this invokes
// the DEEPEST phase the depth requires, exactly once, and derives every
// shallower gate from the markers: the patch gate passes at
// `>>> Source prepared.`, the configure gate at `>>> Source configured.`, and
// the compile gate on a zero exit.
//
// D4 also settles what the "patches gate" IS. Story 031 deferred it because
// `patch --dry-run` needs an unpacked tree, which the static gate existed to
// avoid; here the tree is unpacked anyway, and src_prepare is a strictly better
// test — it applies the patches the way the build will, in the ebuild's own
// order, through eapply/eapply_user.
//
// # A failure attributes itself to the last phase that STARTED
//
// A run that dies before `>>> Source prepared.` has proved nothing about the
// patches: it is a host or distfile fault (design D6), so that gate reports
// SKIPPED naming the phase, because PASS and FAILED would both be lies in
// opposite directions. A run that dies after prepare finished but inside
// configure blames configure alone, so the operator is not sent to the wrong
// file.
//
// # Every gate carries its own outcome
//
// There is no path out of here that reports nothing for a gate the depth
// covered. Applier.runCompile returns ("", nil) when it declines to build, and a
// caller cannot tell that from a pass — that silence is the defect this package
// exists to remove, and it is deliberately not reproduced.
//
// # The error return is about the REQUEST, never about the build
//
// A build that fails is a reported outcome, not an aborted run: it returns gates
// and a nil error. The error is reserved for a request that cannot be attempted
// at all — a malformed atom, no version, no staged tree — because those are a
// caller's bug and reporting them as a failed bump would blame the ebuild for
// them. A host that simply has no `ebuild` is neither: it is reported as every
// covered gate SKIPPED, naming why.
//
// # What is deliberately not checked
//
// The staged tree is not stat'ed. Stage already returned it, and `ebuild`'s own
// error is better than a duplicate check — it names the file it wanted. A tree
// that is not there fails before any phase marker, which the attribution rule
// above already renders as SKIPPED rather than as a verdict on the bump.
//
// There is also no timeout: a legitimate compile can run for hours, and a bound
// that fired would report a failure this host manufactured. The caller's context
// governs, exactly as it does for a signal.
func RunBuildGates(ctx context.Context, req BuildRequest, deps BuildDeps) ([]GateResult, error) {
	phase, runs := deepestPhaseFor(req.Depth)
	if !runs {
		// Below DepthPatches nothing is built, so there is no build gate to
		// report — an empty list, not a hollow pass.
		return nil, nil
	}

	atom := strings.TrimSpace(req.Atom)
	version := strings.TrimSpace(req.Version)
	stagedRoot := strings.TrimSpace(req.StagedRoot)

	// The atom is split first because splitStagedAtom is also the validator: a
	// malformed atom must fail as a malformed atom rather than as an `ebuild`
	// invocation pointed somewhere unintended.
	category, pkg, err := splitStagedAtom(atom)
	if err != nil {
		// Wrapped rather than passed through: splitStagedAtom words its errors
		// for Stage ("staging …"), and an operator reading one here is not
		// staging anything.
		return nil, fmt.Errorf("running the build gates: %w", err)
	}
	if version == "" {
		return nil, fmt.Errorf("running the build gates for %s: no version given, so no candidate ebuild can be named", atom)
	}
	if stagedRoot == "" {
		return nil, fmt.Errorf("running the build gates for %s-%s: no staged tree given to build from", atom, version)
	}

	label := reportLabel{pv: atom + "-" + version, stagedRepo: stagedRepoNameAt(stagedRoot, pkg, version)}

	// A host with no Portage produces NO ANSWER, never a cheerful one — the same
	// rule DependenciesSatisfied applies to `emerge`, and the reason the lookup
	// is a seam of its own.
	if _, err := deps.binaryLookup()("ebuild"); err != nil {
		return skippedGates(req.Depth, label.clean(fmt.Sprintf(
			"ebuild was not found on PATH, so no build phase could be run for %s on this host: %v", label.pv, err))), nil
	}

	// `ebuild` discovers the repository from the path it is given and from its
	// working directory, so Dir is what decides which tree is built: the staged
	// one, never the published overlay (R3.2).
	cmd := deps.commandFactory()(ctx, "ebuild",
		filepath.Join(stagedRoot, category, pkg, pkg+"-"+version+".ebuild"), "clean", phase.String())
	cmd.Dir = stagedRoot

	output, runErr := deps.attachedRunner()(cmd)

	// The transcript is read with ANSI escapes removed and the LOG is not.
	// Portage colours einfo through a TTY, and an attached run therefore carries
	// escapes around the very bullets and markers every gate below greps for; a
	// tracer that missed them would report "no evidence" for a perfectly good
	// build. The retained bytes stay exactly as the child produced them — see
	// reportLabel for the same report/evidence asymmetry stated for D13.
	transcript := stripANSI(string(output))
	run := buildRun{label: label, trace: tracePhases(transcript), transcript: transcript, runErr: runErr}
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

// buildPhase is one `ebuild` phase, ordered as Portage runs them so that "the
// last phase that started" is a comparison rather than a table.
type buildPhase int

const (
	// phaseSetup prints no marker of its own, so it is also what "nothing
	// started" resolves to. Its name says both, because a failure there and a
	// failure in unpack are the same answer to the operator: not the bump's
	// fault (D6).
	phaseSetup buildPhase = iota
	phaseUnpack
	phasePrepare
	phaseConfigure
	phaseCompile
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
// exactly the second invocation D4 exists to prevent.
func deepestPhaseFor(d Depth) (buildPhase, bool) {
	switch {
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
	for p := phaseCompile; p > phaseSetup; p-- {
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
// distinction R4.3 asks this gate to keep.
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

	// logNote is the sentence naming the retained log — or saying why there is
	// none. It is computed once for the run, because one invocation produces one
	// log however many gates cite it.
	logNote string
}

// gateFor derives one gate's outcome from the run's markers, and is the ONE
// place the D13 re-labelling is applied.
//
// It is a funnel rather than a call in each branch for the same reason Stage
// applies its sentinel at a single boundary: a branch added later cannot forget
// what it never had to remember. Every reason and every finding leaves this
// function translated, on the clean path as well as the failing one.
func (r buildRun) gateFor(gate string, phase buildPhase) GateResult {
	result := r.derive(gate, phase)

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
		return GateResult{Gate: gate, Outcome: OutcomeSkipped, Reason: r.notReachedReason(gate, phase)}

	default:
		// A zero exit with no marker to prove the phase finished. Rare, and
		// deliberately not read as a pass.
		return GateResult{Gate: gate, Outcome: OutcomeSkipped, Reason: r.unmeasuredReason(phase)}
	}
}

// completed reports whether a phase finished successfully.
//
// compile is the exception and the exception is the rule R6 states: it is the
// deepest phase a compile-depth request invokes, so the child's own EXIT STATUS
// is the authority on it. Every shallower phase is judged by its marker instead,
// because a zero exit at the end says nothing about which of the phases before
// it ran.
func (r buildRun) completed(p buildPhase) bool {
	if p == phaseCompile {
		return r.runErr == nil
	}
	return r.trace.completed[p]
}

// passReason states what a pass covered AND what it did not. An outcome names
// its own reach, which is why none of these is empty: a green that does not say
// where it stops reads as "it builds", and that overclaim is what this story
// removes.
func (r buildRun) passReason(gate string) string {
	switch gate {
	case GatePatches:
		// R4.3: "every patch applied" and "there were no patches" are different
		// answers and must not render alike.
		if len(r.trace.patches) == 0 {
			return fmt.Sprintf("%s applies no patch, so src_prepare had nothing to apply: this gate passed with nothing to do, not because a patch still applies", r.label.pv)
		}
		return fmt.Sprintf("src_prepare applied the ebuild's %s cleanly to the new sources of %s: %s",
			patchCount(len(r.trace.patches)), r.label.pv, namedPatches(r.trace.patches))

	case GateConfigure:
		// R5.2.
		return fmt.Sprintf("the configure phase completed for %s; a configure pass does not cover compilation, which this depth never ran", r.label.pv)

	case GateCompile:
		// R6.4.
		return fmt.Sprintf("the compile phase completed for %s; a compile pass does not cover src_install, which this ladder deliberately stops short of", r.label.pv)

	default:
		return fmt.Sprintf("the %s gate passed for %s", gate, r.label.pv)
	}
}

// failure is a gate the run died in: FAILED, with the log named (R5.1, R6.5) and
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
		// D6, said out loud: before `>>> Source prepared.` the failure is the
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

// failureExcerpt is the tail of the transcript after the last phase marker: the
// lines the failing phase itself produced, which is where the option upstream
// removed or the header that went missing is actually named.
//
// It is a SUMMARY. The full log is retained and named by the reason beside it,
// so this quotes the end — where the error is — and stops.
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
	if len(excerpt) > excerptLines {
		excerpt = excerpt[len(excerpt)-excerptLines:]
	}
	return excerpt
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
// for the gates that have to cite it (R5.1, R6.5).
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
// # Verbatim, and that is the point (D13)
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
// it reaches a report (D13).
//
// # The repository the operator never created
//
// The build runs from a STAGED repository, so Portage stamps its failure with
// that repository's name — measured:
//
//	media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (configure phase)
//
// Nobody asked about bentoo-staging. They asked about media-plugins/gst-plugins-qt6,
// and should not have to learn the staging tree's name to read the answer.
//
// # Why the qualifier is stripped rather than substituted
//
// The `::` qualifier is removed and the PACKAGE TOKEN IS LEFT ALONE, which keeps
// the VERSION: replacing the whole `<atom>-<version>::<repo>` token with the bare
// atom would read correctly and lose which version failed, and inside a sweep
// that is the part the operator needs most.
//
// It applies to EVERY reason and EVERY finding, on the clean path as well as the
// failing one, because a re-labelling implemented on the failure path alone is
// one that leaks the first time a PASS quotes anything.
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
