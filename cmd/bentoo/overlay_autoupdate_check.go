package main

// `overlay autoupdate --check --llm`: prove what would survive an apply, and
// apply none of it (S033-R9).
//
// The command has two halves that can go wrong, and the code below is arranged
// around them rather than around the data.
//
// THE COST. A gate above `options` unpacks and builds, so a check can silently
// cost hours and a pile of downloaded tarballs. Everything the operator needs to
// price that — how many packages, at what depth, how many distfiles, and how the
// depths are distributed — is printed BEFORE anything is asked and before the
// first gate runs (R9.3), and one confirmation covers the whole run (R9.4). The
// confirmation is confirmSweep's shape, gate for gate
// (overlay_autoupdate_sweep.go:241), so the two commands read alike.
//
// THE REACH. `--check` is documented read-only and the overlay it would write
// to auto-commits and pushes. Nothing here writes to the overlay on any path:
// setVersionsForCheck below is the one call that could, and it exists precisely
// so that a test can watch a seam that is never used (R9.2).
//
// WHAT IT SAYS AFTERWARDS is not built here any more (S044). This file used to
// format each section at the moment it printed it — a padded plan block, a
// verdict line per package, a three-column tally — so nothing held "what this
// run found" as a value and nothing could be exported, re-rendered or counted
// without running the command again. Now the run is assembled into
// internal/common/report's view model, whole, before any of it is displayed
// (S044-R1.4), and internal/common/report/render prints it in the mode this run
// resolved to (S044-R2). Two things still print from here, and both are events
// of the RUN rather than findings about a package: the price above, which has to
// precede the first gate, and a raise held at the confirmed depth (R9.6).
//
// The four hard-coded field widths this file used to declare are gone with that
// move. A width typed into a format string cannot be right — the correct one
// depends on the packages this run produced, which is knowable only after it has
// produced them — and the renderer measures instead (S044-R6.3).
//
// That sentence deliberately does not spell the format verb out. A test greps
// this file for one and fails on any match, comments included, which is the
// right reading: a width in a comment is one somebody copies back into code.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
)

// setVersionsForCheck is the ONE way this file could publish, held as a variable
// so R9.2 can be proved rather than asserted.
//
// It is deliberately never called. A check that promoted anything would write a
// version pin through exactly this function, so a test can keep it wired, run
// every path — a gate that passed, one that failed, one that skipped — and read
// the seam afterwards. Deleting it would not make the check safer; it would make
// the guarantee unobservable, which is how the guarantee gets removed by
// accident later.
var setVersionsForCheck = autoupdate.SetPackageVersions

// validationPlanEntry is one pending update, the depth it earned and the case
// for that depth.
//
// Reason is never empty: validate.ResolveDepth is total and always names the
// input that decided. An entry whose reason went missing would leave the
// operator approving a number they cannot check — "3 packages, one of them a
// compile" is a cost; "one of them a compile BECAUSE the registry overrides it"
// is a decision.
type validationPlanEntry struct {
	// Package is the full atom, category/package.
	Package string
	// From and Version are the two ends of the bump. Both are printed because
	// the depth follows the distance between them, and a reader checking the
	// plan against the policy needs to see the same two numbers the classifier
	// saw.
	From    string
	Version string
	// Class is how far the bump moved, as validate.Classify named it.
	Class string
	// Depth is the resolved depth, spelled the way --depth and the config key
	// spell it, so a plan line can be typed back as a flag.
	Depth string
	// Reason names the input that decided the depth, and quotes an override's
	// stated justification where one exists.
	Reason string
	// Skipped marks an entry no gate will run for — a binary record, or a
	// package policy lowered below what its class earns. It is carried as a
	// field rather than derived at print time because R9.3 requires the reason
	// to travel with it (R2.6's "counted as skipped, not validated").
	Skipped bool
	// ConfirmedDepth is the deepest depth the operator approved for the WHOLE
	// run, handed to the gate runner with each entry. It is the ceiling R9.6 is
	// about: a reviewer may raise a bump up to it without asking again, and a
	// raise past it is held here rather than spent unasked. runValidationCheck
	// fills it in; buildValidationPlan leaves it empty, because until the plan
	// is run nothing has been confirmed.
	ConfirmedDepth string

	// depth is Depth as the ladder value it came from, kept so the printer and
	// the ceiling comparison do not re-parse a string this package just
	// produced.
	depth validate.Depth
}

// validationPlan is the whole run, priced before it is paid for.
type validationPlan struct {
	// Entries holds EVERY pending update, including the ones that resolve to
	// depth none. A plan that dropped them would produce a reassuring tally
	// about the easy packages and quietly say nothing about the rest (R9.1).
	Entries []validationPlanEntry
	// DistfilesToFetch is how many packages this run has to hold a tarball for.
	// Every depth above `none` reads the new archive, so each such package needs
	// its distfile: on a metered connection or a laptop that number is what
	// decides whether the answer is yes, and it appears nowhere else in the
	// output (R9.3).
	//
	// It is an upper bound per package, not a download count: a distfile the
	// host already holds is not fetched again, and a package with several
	// SRC_URI entries still counts once. The printed line says so.
	DistfilesToFetch int
	// DepthDistribution counts the packages at each depth, keyed by the depth's
	// name. Together with len(Entries) it is the story's "reach is measured,
	// never claimed" metric — a distribution WITH its denominator — and
	// `--check` is the only place it is produced. A depth nothing resolved to is
	// absent rather than present with a zero, so the map reads as what the run
	// actually contains.
	DepthDistribution map[string]int
	// Printed reports that the caller has ALREADY put this plan in front of the
	// operator, which the confirmation path must do: a question about a cost
	// nobody has seen is not a confirmation. runValidationCheck prints the plan
	// itself when this is false, so "the whole plan precedes the first gate"
	// holds even for a run that had nothing to confirm — and is not printed
	// twice for one that did.
	Printed bool
}

// The tally this file used to declare is report.Tally, and it is produced by
// report.Classify through buildReport rather than by a switch here. The
// invariant it protected is unchanged and is now checkable rather than merely
// intended: each planned package lands in exactly one column, and
// report.Report.Reconciles reports whether the columns sum to the plan (R5.5).
// A package counted twice, or in two columns, is worse than no tally at all —
// it turns the one number anybody remembers into a number nobody can reconcile
// with the list above it.

// buildValidationPlan prices a run: it resolves the depth for every pending
// update and nothing else (R9.1, R9.3).
//
// It performs no I/O. The plan is what the operator approves, so producing it
// must cost nothing and must give the same answer twice — and a plan that had
// already touched the network would have started spending what it is asking
// about.
func buildValidationPlan(updates []autoupdate.PendingUpdate, policy validate.DepthPolicy) validationPlan {
	return planValidation(updates, policy, nil, nil)
}

// planValidation is buildValidationPlan with the two inputs the command has and
// a test fixture does not: the operator's `--depth`, and the RESOLVED package
// tier from the check that just ran.
//
// tierOf may be nil, in which case the tier is guessed from the package name —
// see packageTierFromName for why that is a fallback and not the rule.
func planValidation(
	updates []autoupdate.PendingUpdate,
	policy validate.DepthPolicy,
	flagDepth *validate.Depth,
	tierOf func(autoupdate.PendingUpdate) string,
) validationPlan {
	if tierOf == nil {
		tierOf = packageTierFromName
	}

	plan := validationPlan{
		Entries:           make([]validationPlanEntry, 0, len(updates)),
		DepthDistribution: map[string]int{},
	}

	for _, update := range updates {
		// ClassifyForDepth returns a NOTE, not an error: a version it cannot
		// read is charged the deepest class, and the note says so. Dropping the
		// note would leave the deepest depth looking like a policy choice.
		//
		// The pending value is normalized with the SAME strip Validate applies
		// before ITS classification (applier_check.go): a pending entry written
		// by an older binary can still carry the upstream tag prefix
		// ("v3.2.3"), and classifying the raw value here would price the bump
		// as major in the plan the operator confirms while the run executes it
		// as patch — a plan that lies about its own cost.
		class, note := validate.ClassifyForDepth(update.CurrentVersion,
			autoupdate.NormalizeUpstreamVersion(update.NewVersion))

		decision := validate.ResolveDepth(validate.DepthRequest{
			Package: update.Package,
			Class:   class,
			// The RESOLVED tier, never PackageConfig.Type verbatim — that field
			// is empty for most records, so a resolver reading it would see ""
			// for almost every binary package in the registry and schedule a
			// compile for a prebuilt blob (validate.DepthRequest.ResolvedType).
			ResolvedType: tierOf(update),
			Policy:       policy,
			FlagDepth:    flagDepth,
		})

		reason := decision.Reason
		if note != "" {
			reason = note + "; " + reason
		}

		entry := validationPlanEntry{
			Package: update.Package,
			From:    update.CurrentVersion,
			Version: update.NewVersion,
			Class:   class.String(),
			Depth:   decision.Depth.String(),
			Reason:  reason,
			Skipped: decision.Depth == validate.DepthNone || decision.SkippedByPolicy,
			depth:   decision.Depth,
		}

		plan.Entries = append(plan.Entries, entry)
		plan.DepthDistribution[entry.Depth]++
		if entry.depth > validate.DepthNone {
			// Every depth above `none` reads the new archive, so this package
			// needs its distfile on disk before a gate can say anything.
			plan.DistfilesToFetch++
		}
	}

	return plan
}

// packageTierFromName is the tier fallback for a plan built without a check
// beside it: a package whose name ends in `-bin` is a prebuilt record.
//
// It is a FALLBACK and never the authority. The real answer is
// autoupdate.CheckResult.Type, which reads the current ebuild (RESTRICT=bindist,
// a binary SRC_URI, the name), and the command path passes it in. This exists so
// that a plan built from nothing but a pending list still puts the obvious
// prebuilt records where they belong instead of scheduling a compile for a blob.
//
// It answers "" rather than "source" when it does not recognise a name, because
// "" means "nobody resolved this" and keeps every gate the class earns. Losing
// gates must never be something that happens by omission.
func packageTierFromName(update autoupdate.PendingUpdate) string {
	name := update.Package
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	if strings.HasSuffix(name, "-bin") {
		return "bin"
	}
	return ""
}

// deepest is the deepest depth anywhere in the plan — the ceiling one
// confirmation covers, and the yardstick R9.6 measures a reviewer's raise
// against.
func (p validationPlan) deepest() validate.Depth {
	deepest := validate.DepthNone
	for _, entry := range p.Entries {
		if entry.depth > deepest {
			deepest = entry.depth
		}
	}
	return deepest
}

// building counts the packages that run a gate above `options` — the ones that
// unpack and build, and therefore the only reason to ask anything (R9.4).
func (p validationPlan) building() int {
	building := 0
	for _, entry := range p.Entries {
		if entry.depth > validate.DepthOptions {
			building++
		}
	}
	return building
}

// printValidationPrice puts the PRICE of the run on screen before any of it is
// spent (S033-R9.3): how many packages, each one's depth, which are not
// validated, how many distfiles the run needs, and how the depths are
// distributed.
//
// # What it no longer prints, and why that is the point
//
// This is what printValidationPlan used to be. It has lost the three-line
// block per package — the padded package line, the class line and the whole
// 230-character reason — because the plan's PRESENTATION is now a section of
// the report (render's validationPlanSection), rendered once when the run is
// over, aligned to columns measured from the packages this run actually
// produced. Printing the reasons here as well would put the same sentence on
// screen twice in one run, which is the defect R7.2 exists to remove.
//
// # It is not a second renderer of the view model
//
// It renders the PLAN, which is a producer artefact, at a moment when the
// model of the run cannot exist yet: nothing has been evaluated, so there is
// no report to take a value from. R1.3 binds a renderer that displays the view
// model, and the run-level facts below are precisely the ones the model cannot
// answer for — `deepest` and the distribution's ordering both need the depth
// ladder, which the model deliberately does not carry.
//
// # There is no width here to get wrong
//
// The old lines padded the package to a typed 45 cells, which was too narrow
// for `gst-plugins-adaptivedemux2@stable` and too wide for everything else.
// The replacement is not a measured column: it is no column at all. A pre-run
// price is a list of facts, and the aligned table is the report's job — so
// this function has nothing left to declare a width for (R6.3).
func printValidationPrice(plan validationPlan) {
	fmt.Println()
	output.Header.Println("Validation Plan")
	fmt.Println()

	if len(plan.Entries) == 0 {
		output.Info.Println("  No pending update to validate.")
		return
	}

	output.Info.Printf("  %d package(s) to evaluate, deepest depth %s.\n\n", len(plan.Entries), plan.deepest())

	for _, entry := range plan.Entries {
		tag := ""
		if entry.Skipped {
			// A package that will not be validated has to say so on its own
			// line: the operator reads a shorter list of results as progress
			// unless the plan already told them it would be shorter.
			tag = " [not validated]"
		}
		fmt.Printf("  %s %s → %s at depth %s%s\n", entry.Package, entry.From, entry.Version, entry.Depth, tag)
	}
	fmt.Println()

	// The two numbers an operator actually decides on. The first appears
	// nowhere else on screen, and it is the one that decides the answer on a
	// metered connection; it travels into the report as well
	// (report.Report.DistfilesToFetch) so the export carries it too.
	output.Info.Printf("  Distfiles: up to %d to fetch — one per package validated above depth none; anything already in DISTDIR is not fetched again.\n",
		plan.DistfilesToFetch)
	output.Info.Printf("  Depth distribution (of %d package(s)): %s\n", len(plan.Entries), depthDistributionLine(plan))
}

// depthDistributionLine renders the distribution along the ladder, shallowest
// first, naming only the depths this run actually contains.
//
// The denominator is printed by the caller, and it is the half that makes the
// number mean anything: "12 at compile" is a fact about nothing until it is "12
// of 300".
//
// The order comes from ParseDepth rather than from a list of rung names spelled
// out here. A second copy of the ladder would be a copy that can go stale: add a
// rung to validate and this line would silently drop the packages that resolved
// to it. Every key was produced by Depth.String(), so parsing it back always
// succeeds; the alphabetical fallback exists only so an impossible key still
// prints somewhere deterministic instead of moving between runs.
func depthDistributionLine(plan validationPlan) string {
	names := make([]string, 0, len(plan.DepthDistribution))
	for name, n := range plan.DepthDistribution {
		if n > 0 {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		left, leftErr := validate.ParseDepth(names[i])
		right, rightErr := validate.ParseDepth(names[j])
		if leftErr != nil || rightErr != nil {
			return names[i] < names[j]
		}
		return left < right
	})

	if len(names) == 0 {
		return "(nothing planned)"
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s %d", name, plan.DepthDistribution[name]))
	}
	return strings.Join(parts, ", ")
}

// confirmValidationRun takes ONE confirmation for the whole run (R9.4).
//
// The three gates are confirmSweep's, in the same order and for the same
// reasons: --yes proceeds unattended because the operator asked for that in so
// many words; a non-interactive terminal without --yes runs nothing AND says how
// to proceed, because a run that silently did nothing is only marginally better
// than one that silently did the expensive thing; anything else is asked.
// registryPromptIsInteractive requires BOTH stdin and stdout to be terminals, so
// `yes | bentoo overlay autoupdate --check` cannot answer for a human.
//
// A run with nothing above `options` asks nothing at all. The confirmation
// exists because a build costs hours; a plan that cannot start one has nothing
// to ask about, and a gate that asks anyway teaches the operator to answer
// without reading — which is how every confirmation gate dies.
func confirmValidationRun(plan validationPlan) bool {
	builds := plan.building()
	if builds == 0 {
		return true
	}

	deepest := plan.deepest()

	if autoupdateYes {
		output.Warning.Printf("  --yes given: evaluating %d package(s) without a prompt — %d of them run a build gate, up to %s.\n",
			len(plan.Entries), builds, deepest)
		output.Warning.Printf("  Up to %d distfile(s) are fetched and the deepest gates can take hours. Nothing is published.\n",
			plan.DistfilesToFetch)
		return true
	}

	if !registryPromptIsInteractive() {
		output.Warning.Println("  Not an interactive terminal and --yes was not given: no gate ran and nothing was validated.")
		output.Info.Printf("  Re-run with --yes to evaluate these %d package(s) unattended — %d of them up to %s, fetching up to %d distfile(s).\n",
			len(plan.Entries), builds, deepest, plan.DistfilesToFetch)
		return false
	}

	fmt.Println()
	output.Warning.Printf("  %d package(s) run a gate above `options`, which unpacks and builds: this fetches up to %d distfile(s) and can take hours.\n",
		builds, plan.DistfilesToFetch)
	output.Info.Println("  Nothing is published either way — a check writes no ebuild and no version pin.")
	return confirmSweepFn(fmt.Sprintf(
		"Evaluate %d package(s), %d of them up to depth %s?", len(plan.Entries), builds, deepest))
}

// runPendingValidation is R9.1 reaching the CLI: after a check has recorded what
// is pending, every one of those bumps is put through the gates at its resolved
// depth — priced first (R9.3), asked about once (R9.4), tallied at the end
// (R9.5), and published never (R9.2).
//
// # Why it is gated on --llm
//
// R9's command is `--check --llm`, and the gate is not a technicality. A gate
// above `options` unpacks and builds, and even `options` fetches a distfile, so
// running this on every `--check` would turn a network read that takes seconds
// into one that takes hours the first time somebody typed the command they have
// always typed. `--llm` is the flag that already means "spend real resources on
// validating this run", so it is the flag that turns the gates on here too; the
// confirmation below still asks before anything builds.
//
// # It publishes nothing, and the guarantee is structural
//
// The one function in this file that could write to the overlay,
// setVersionsForCheck, is never called — from here or from anywhere. The applier
// built below runs Validate and never Apply: promotion, the version pin and the
// `--clean` sweep all live in Apply, which this path does not reach.
func runPendingValidation(ctx context.Context, overlayPath, configDir string, checked []autoupdate.CheckResult, llmCfg config.LLMConfig) {
	if !autoupdateLLM {
		return
	}

	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		logger.Warn("could not read the pending list, so nothing was validated: %v", err)
		return
	}
	updates := pending.List()
	if len(updates) == 0 {
		// Silence is right for an empty plan: printing "0 packages to evaluate"
		// after a check that found nothing is a line about nothing.
		return
	}

	// The RESOLVED tier from the check that just ran, not a guess from the
	// package's name: CheckResult.Type is read from the current ebuild
	// (RESTRICT=bindist, a binary SRC_URI, the name), and a resolver without it
	// schedules a compile for a prebuilt blob.
	tier := make(map[string]string, len(checked))
	for _, item := range checked {
		if item.Type != "" {
			tier[item.Package] = item.Type
		}
	}

	plan := planValidation(updates, autoupdateValidate.Policy, autoupdateValidate.Depth,
		func(update autoupdate.PendingUpdate) string { return tier[update.Package] })

	// Printed here rather than left to runValidationCheck, because the
	// confirmation below is about this plan: a question about a cost nobody has
	// seen is not a confirmation. Printed records that it has been shown, so it is
	// not repeated.
	printValidationPrice(plan)
	plan.Printed = true
	if !confirmValidationRun(plan) {
		return
	}

	opts := []autoupdate.ApplierOption{
		autoupdate.WithApplierContext(ctx),
		autoupdate.WithApplierPackagesConfig(loadPackagesConfigForApply(overlayPath)),
		applierFixerOption(llmCfg),
	}
	opts = append(opts, applierDistfileOptions()...)
	opts = append(opts, applierValidateOptions(configDir)...)
	opts = append(opts, applierLLMOptions(autoupdateLLM, llmCfg, autoupdateValidateCfg)...)

	// Deliberately NOT WithApplierClean: `--clean` deletes published ebuilds, and
	// a read-only check has no business owning that switch even by accident.
	applier, err := autoupdate.NewApplier(overlayPath, configDir, opts...)
	if err != nil {
		logger.Warn("could not initialize the validator, so nothing was validated: %v", err)
		return
	}

	//nolint:contextcheck // ctx is propagated into every spawned child through
	// WithApplierContext (a.ctx); Validate takes no ctx parameter, by the same
	// single-source wiring Apply uses.
	finished := runValidationCheck(plan, func(entry validationPlanEntry) validate.EbuildResult {
		// The ceiling one confirmation covered. It travels per entry so a
		// reviewer's raise is held against what the operator approved rather than
		// against this bump's own depth (R9.6). A depth the plan could not spell
		// is no ceiling at all, which is the honest reading — nothing was
		// confirmed about a number nobody printed.
		ceiling, err := validate.ParseDepth(entry.ConfirmedDepth)
		if err != nil {
			ceiling = validate.DepthNone
		}
		return applier.Validate(entry.Package, ceiling)
	})

	// The half buildReport is never handed: it is given the plan and its
	// results and never sees the scan, so a nil Scanned would mean the JSON
	// export said `"scanned": null` and the version-check section rendered
	// empty. This is the one place in the command that holds both — the scan
	// that ran and the validation that followed it — so this is where the two
	// are joined, after the run and before anything is displayed.
	finished.Scanned = scannedFacts(checked)

	presentCheckReport(finished)
}

// runValidationCheck evaluates every planned package through `run` and returns
// the whole run as the view model (R9.1, R1.4).
//
// THE PRICE IS PRINTED HERE, not left to the caller. "A plan is printed" is
// satisfied by printing it beside the results, which is worth nothing: by then
// the hours are spent. Printing it as the first thing this function does makes
// "the whole plan precedes the first gate" a property of the function rather
// than of a calling convention somebody can forget. A caller that already showed
// the price in order to ask about it sets plan.Printed and is not repeated.
//
// # It RETURNS the report; it does not render it
//
// The report is assembled whole, from the plan and every result, and handed
// back — so a run that then fails to render, or is interrupted on its way to
// the screen, still holds a complete description of what it found (R1.4). It is
// also what lets the caller fill in the half this function never sees (the
// scan) before anything is displayed, and what keeps `--export` a decision of
// the command rather than of the loop.
//
// The counts come from that report and are computed nowhere else: the switch
// this function used to run over WorstOutcome is now report.Classify's, reached
// through buildReport, so the tally on screen and the tally in the JSON export
// cannot disagree (R1.3).
//
// It publishes nothing, on every path — see setVersionsForCheck.
func runValidationCheck(plan validationPlan, run func(validationPlanEntry) validate.EbuildResult) report.Report {
	if !plan.Printed {
		printValidationPrice(plan)
		fmt.Println()
	}

	// The ceiling the confirmation covered. Every entry carries it into the
	// runner so a reviewer's raise is measured against what the operator
	// approved rather than against the entry's own depth (R9.6).
	confirmed := plan.deepest()

	// results[i] answers plan.Entries[i]. buildReport's contract is positional,
	// and appending exactly once per entry in plan order is what honours it.
	results := make([]validate.EbuildResult, 0, len(plan.Entries))
	for _, entry := range plan.Entries {
		entry.ConfirmedDepth = confirmed.String()

		result := run(entry)
		reportDepthEscalation(entry, result, confirmed)
		results = append(results, result)
	}

	return buildReport(plan, results)
}

// presentCheckReport puts the finished report in front of the operator: the
// terminal first, then the export (R2, R9.1, R9.5).
//
// # The terminal render happens first, and that ordering is R9.5
//
// An export is a convenience; the report on the terminal is the answer. Writing
// the file first would make a bad path — a directory that does not exist, a
// read-only mount — able to cost the operator the report itself. Rendering
// first makes that impossible rather than merely unlikely, and the export
// failure is then reported as a warning: it changes no verdict, no count and no
// exit status (R9.6), because a run's findings do not depend on whether a copy
// of them could be filed.
//
// # A render that fails is reported and does not stop the export
//
// The two are independent answers to the same report. A terminal that went away
// mid-write is no reason to also withhold the file, which may be the only copy
// left.
//
// # The mode is resolved here, not passed in
//
// resolveAutoupdateUIMode is a pure function of the flags, the config and the
// terminal, so asking here gives the same answer any other caller gets. An
// error is unreachable in a real run — runAutoupdate rejects an unusable --ui
// before any package work (R3.9) — so it falls back to plain, which is the mode
// that always works, and says so at debug level rather than telling the
// operator a second time in a second voice.
func presentCheckReport(r report.Report) {
	// Width is left at zero: "ask the device" (render.Options). A number typed
	// here would be a hard-coded field width in the one path R6.3 binds.
	opts := render.Options{ShowAll: autoupdateAll}

	mode, err := resolveAutoupdateUIMode(autoupdateUIConfig)
	if err != nil {
		logger.Debug("check: the UI mode did not resolve, rendering in plain: %v", err)
		mode = report.ModePlain
	}

	if err := renderCheckReportIn(mode, r, opts); err != nil {
		logger.Warn("the report could not be rendered: %v", err)
	}

	if autoupdateExport == "" {
		return
	}
	if err := writeExport(autoupdateExport, r); err != nil {
		// Warn, never fatal: the answer is already on screen, and an export
		// that changed the exit status would make a display flag decide whether
		// a run counted as successful.
		logger.Warn("%v", err)
	}
}

// reportDepthEscalation says what happened to a bump the reviewer took past the
// depth the operator confirmed (R9.6).
//
// # It is what remains of reportValidationOutcome, and the rest is the report's
//
// The three verdict lines this function used to print — proved, FAILED, not
// validated, each padded to a typed 45 cells — are now rows of the report's
// results section, rendered once at the end from values the model carries. What
// could not move is this: a raise past the ceiling is an event of the RUN, not
// a finding about the package. The model describes what a run found; it has no
// field for what the run declined to spend, and inventing one would put a
// sentence about the operator's confirmation into a record about packages.
//
// THE CHOICE THIS IMPLEMENTATION MAKES IS TO HOLD. R9.6 allows either answer,
// and holding is the one that keeps the promise the plan made: a run whose plan
// resolved entirely to `options` asks for nothing, so a reviewer raising a bump
// to `compile` afterwards would spend hours the operator was never shown. The
// ceiling travels with every entry (validationPlanEntry.ConfirmedDepth) so the
// runner can hold there, and the lines below name the package, the raise and
// the ceiling so the hold is never silent — a held bump the operator cannot see
// is just a missing result.
//
// It names the package itself, because it no longer prints under a verdict line
// that did. A warning about "this bump" with no atom in it is unactionable in a
// run of forty packages.
func reportDepthEscalation(entry validationPlanEntry, result validate.EbuildResult, confirmed validate.Depth) {
	// ParseDepth failing means the runner did not report a depth at all, which
	// is not an escalation — only a depth it NAMED can be compared against the
	// ceiling.
	actual, err := validate.ParseDepth(result.Depth)
	if err != nil || actual <= confirmed {
		return
	}
	output.Warning.Printf("  %s: the reviewer raised this bump to %s, past the %s this run's plan confirmed.\n",
		entry.Package, actual, confirmed)
	output.Info.Printf("      A raise past the confirmed depth is held at %s rather than spent unasked (R9.6); re-run with --yes to approve the deeper gates for the whole run.\n",
		confirmed)
}

// reportedDepth is the depth the runner says it ran at, falling back to the
// planned one when it said nothing. The planned depth is the honest fallback: it
// is what was asked for, and claiming a depth nobody reported would put a number
// in the report that no gate stands behind.
func reportedDepth(result validate.EbuildResult, entry validationPlanEntry) string {
	if result.Depth != "" {
		return result.Depth
	}
	return entry.Depth
}

// skipReason is why a package produced no verdict. A skip ALWAYS carries a
// reason — the gate's own where there is one, the plan's depth reason otherwise
// — because a skipped package with no reason reads as a result.
//
// The last return is that promise kept rather than assumed. All three sources
// are free-form strings nothing forces to be populated, so the cascade can run
// out; when it does, the caller prints "not validated ()" and an operator reads
// the empty parenthesis as "checked, nothing to say" — the exact misreading the
// paragraph above forbids. Naming the silence instead is honest: it says no
// verdict was produced AND that nothing explained why, which is a reportable
// defect in whatever left every reason blank.
func skipReason(result validate.EbuildResult, entry validationPlanEntry) string {
	for _, gate := range result.Gates {
		if gate.Outcome == validate.OutcomeSkipped && gate.Reason != "" {
			return gate.Reason
		}
	}
	if result.DepthReason != "" {
		return result.DepthReason
	}
	if entry.Reason != "" {
		return entry.Reason
	}
	return "no reason reported: neither the gates, the depth nor the plan stated one"
}

// The tally that used to be printed here is render's validationSummarySection,
// and it now has FOUR counts rather than three: the old "not validated" column
// held both the packages policy excluded and the packages the toolkit could not
// evaluate, so a defect in the toolkit was reported in the same number as the
// operator's own choice (R5.1). Proved and errored count exactly what they
// counted before (R5.7). The reminder that a check publishes nothing moved with
// it, so it is still the last sentence a reader sees.
