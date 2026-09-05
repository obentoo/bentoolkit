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
// (`func confirmSweep` in overlay_autoupdate_sweep.go), so the two commands
// read alike.
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
// report.AutoupdateCheck.Reconciles reports whether the columns sum to the plan (R5.5).
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
// the report (validationPlanSection, below), rendered once when the run is
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
	// (report.AutoupdateCheck.DistfilesToFetch) so the export carries it too.
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
// # It returns the validation half; it does not draw it
//
// What comes back is the run buildReport assembles from the plan and the
// results — the half of it only this function sees. Scanned is left nil in its
// payload deliberately, and that nil is not harmless: a report rendered with it
// exports `"scanned": null` and draws an empty version-check section. So it is a
// half that MUST be joined before anything is displayed, and it is joined by
// runCheck, the one place in the command that holds both the scan that ran and
// the validation that followed it. Joining there rather than here is what leaves
// exactly one report per run (S045-R1.1); filling Scanned in on both sides would
// turn the join into a question of which copy wins.
//
// The second value is whether the plan has already been printed — whether the
// pre-confirmation printValidationPrice below ran. It travels back so the caller
// can set report.SectionOptions.SkipPlan and not show the operator the same plan again
// under a second heading (S045-R2.3). It is true on every path that got past
// that print, INCLUDING the ones that then gave up: a declined confirmation saw
// the plan, and a false here would redraw it for the operator who has just read
// it and said no.
//
// # Why it is gated on --llm, and what that gate no longer decides
//
// R9's command is `--check --llm`, and the gate is not a technicality. A gate
// above `options` unpacks and builds, and even `options` fetches a distfile, so
// running this on every `--check` would turn a network read that takes seconds
// into one that takes hours the first time somebody typed the command they have
// always typed. `--llm` is the flag that already means "spend real resources on
// validating this run", so it is the flag that turns the gates on here too; the
// confirmation below still asks before anything builds.
//
// The gate decides validation and nothing else. It used to decide the drawing
// too — not by saying so, but because the only path to the render ran through
// it, which is what made --ui, --all and --export silent no-ops on a run without
// --llm (S045-R3.1, S045-R3.2, S045-R3.3). It still means "do not validate", and
// it says so by yielding nothingValidated's run and "nothing printed": there is
// no half to contribute, so the caller draws the scan it already holds.
//
// # It publishes nothing, and the guarantee is structural
//
// The one function in this file that could write to the overlay,
// setVersionsForCheck, is never called — from here or from anywhere. The applier
// built below runs Validate and never Apply: promotion, the version pin and the
// `--clean` sweep all live in Apply, which this path does not reach.
func runPendingValidation(ctx context.Context, overlayPath, configDir string, checked []autoupdate.CheckResult, llmCfg config.LLMConfig) (report.Run, bool) {
	if !autoupdateLLM {
		return nothingValidated(), false
	}

	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		logger.Warn("could not read the pending list, so nothing was validated: %v", err)
		return nothingValidated(), false
	}
	updates := pending.List()
	if len(updates) == 0 {
		// Silence is right for an empty plan: printing "0 packages to evaluate"
		// after a check that found nothing is a line about nothing.
		return nothingValidated(), false
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
		// Nothing was validated, so there is no half to hand back. The plan is on
		// screen either way, though, which is what the second value reports: the
		// operator has just read it and answered no, and a false here would ask
		// the caller to draw it to them a second time.
		return nothingValidated(), true
	}

	opts := []autoupdate.ApplierOption{
		autoupdate.WithApplierContext(ctx),
		autoupdate.WithApplierPackagesConfig(loadPackagesConfigForApply(overlayPath)),
		applierFixerOption(llmCfg),
	}
	opts = append(opts, applierGentooPathOption())
	opts = append(opts, applierDistfileOptions()...)
	opts = append(opts, applierValidateOptions(configDir)...)
	opts = append(opts, applierLLMOptions(autoupdateLLM, llmCfg, autoupdateValidateCfg)...)

	// Deliberately NOT WithApplierClean: `--clean` deletes published ebuilds, and
	// a read-only check has no business owning that switch even by accident.
	applier, err := autoupdate.NewApplier(overlayPath, configDir, opts...)
	if err != nil {
		logger.Warn("could not initialize the validator, so nothing was validated: %v", err)
		// Printed, for the same reason the declined answer above is: the price
		// reached the screen before this failed, so the caller must not repeat it.
		return nothingValidated(), true
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

	return finished, true
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
func runValidationCheck(plan validationPlan, run func(validationPlanEntry) validate.EbuildResult) report.Run {
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
// reportModeOrPlain is a pure function of the flags, the configuration and the
// terminal, so asking here gives the same answer any other caller gets — and
// --ui is the root's flag now, so the answer is the CLI's rather than one
// command's (R3.1).
//
// What stood here was resolveAutoupdateUIMode and a claim that its error was
// unreachable, because runAutoupdate rejected an unusable --ui before any
// package work (S044-R3.9). That rule names the FLAG and nothing else; the root
// has enforced it for all 30 commands since Task 4, and the gate this function
// was trusting has stopped exiting on the sources it never governed. So every
// error that reaches this line is an AMBIENT one — a BENTOO_UI or a ui.mode,
// inherited from a shell profile or a config file rather than typed for this
// run — and it is refused at this call: the render falls back to plain, the
// refusal is STATED naming the source, the value and the mode used instead, and
// the exit status does not move (R3.7). The rule and the measurements behind it
// are on reportModeOrPlain, which the manifest and snapshot producers call too,
// so a typo answered on one command cannot be swallowed on this one.
//
// This is also the ONLY voice the run has for that refusal, which is R3.6. The
// gate in runAutoupdate resolves the same value to route the downgrade sentence
// and records its own failure at debug level; a second sentence from there would
// answer one typo twice, in two voices, about a value the operator typed once.
//
// # The `--list` hint is printed from here, and that placement IS the decision
//
// S045-R5.2's hint names a bentoo subcommand. The section that lists the very
// updates it is about — versionCheckSection — builds a report.Section, which
// internal/common/report/render prints for every command in the toolkit: put
// the sentence in the section and every future report carries this one
// command's advice. So the section lists what is
// pending and this function says what to do about it (S045-D5).
//
// S045-R1.4 permits it, because a hint is none of the four things that rule
// reserves for the report: it is not a package name, not a version, not a plan
// entry and not a tally. R5.1's `Checked N source, M bin` IS a count over the
// scanned packages, which is exactly why that one went inside the section
// instead (sub-task 2.1).
//
// # planPrinted is the ONE thing the caller knows and this function cannot
//
// Whether the operator has already read this plan is a fact about what reached
// the screen earlier in the run — the pre-confirmation printValidationPrice —
// and nothing in a report records it. So it is a parameter rather than a field:
// putting it on the model would make "has this been shown" a property of the
// run's findings, and the file the same report is exported to would then carry
// an answer about a terminal it never touched (S045-R2.4).
//
// It reaches the SCREEN only. The export below is built by renderExport, which
// gives Markdown and JSON no Options at all and builds the plain export its own
// — so a plan skipped here is still stated in full in the file (S045-R2.4).
//
// # The run arrives BUILT, and that is R1.3
//
// What comes in is the whole run — the envelope naming its kind and stating how
// far down its plan it got, around the payload holding what it found — assembled
// by the adapter before this function is entered. So the screen and the file are
// two renderings of ONE value rather than two wraps that could disagree about
// whether the run finished, and "the report is complete before any part of it is
// rendered" is a property of the call rather than of what happens next.
//
// # A run that scanned nothing reaches the file, and not the screen
//
// The one report this function does NOT draw is the empty one, and the reason
// is `--quiet` (S045-R5.3, S045-D4). Its entire effect is
// logger.SetQuiet(true), applied in `func main` in cmd/bentoo/main.go: it
// reaches the logger and
// reaches neither the output package nor os.Stdout. So today a `--check
// --quiet` over an empty registry prints nothing at all, while one with
// packages prints the whole table anyway — an asymmetry that already exists,
// and which drawing the report on the empty scan would remove by making the
// silent case newly noisy. The sentence stays where it can still be silenced.
//
// The EXPORT is unconditional even so, because `--export` has no empty-scan
// carve-out (S045-R3.3) and a file that records "this run scanned nothing" is
// the absence carried honestly (S045-R4.3). No file at all would be
// indistinguishable from a command that never ran.
func presentCheckReport(run report.Run, planPrinted bool) {
	// This command's own facts, read back out of the run that carries them: the
	// three decisions below — whether to render at all, whether to point at
	// `--list`, whether to announce the registry write — are all about packages,
	// and the envelope deliberately knows nothing about packages.
	r := checkPayload(run)

	// Silent only when the report holds NOTHING, which is a conjunction rather
	// than the scan alone. CheckAll skips disabled and held entries and
	// DisableOrphans auto-disables an entry whose ebuild has vanished, so
	// "every entry disabled, and a pending.json still on disk from an earlier
	// run" yields an empty Scanned beside a plan that was built, confirmed and
	// evaluated. Keying on the scan alone would discard that report — hours of
	// gate work the operator waited for and approved. S045-R5.3's subject is a
	// run with nothing to say, and a run holding results has something to say
	// however its scan came out.
	if len(r.Scanned) == 0 && len(r.Plan) == 0 {
		// Verbatim the sentence the retired legacy check printer emitted at
		// its own `len(results) == 0` guard, on the same channel. The
		// wording is load-bearing in both directions: it is what an operator
		// already greps for, and it is deliberately NOT the report's own lead
		// for this case ("No package is configured for autoupdate.",
		// versionCheckSection below), which the paragraph above explains this
		// path must never reach.
		logger.Info("No packages configured for autoupdate")
	} else {
		// Two questions, and keeping them apart is the whole of story 046's
		// Task 2. What the report should SAY — list every up-to-date package,
		// state the plan — is report.SectionOptions, answered here from the
		// flags. What the DEVICE allows is render.Options, and its Width is
		// left at zero: "ask the device". A number typed here would be a
		// hard-coded field width in the one path R6.3 binds.
		//
		// SkipPlan omits the report's own plan section on a run whose price
		// was already printed to ask the confirmation question: the operator
		// has just read that list, and drawing it again under a second heading
		// is the duplication S045-R2.3 removes.
		content := report.SectionOptions{ShowAll: autoupdateAll, SkipPlan: planPrinted}
		device := render.Options{}

		mode := reportModeOrPlain(autoupdateUIConfig)

		// The sections are built ONCE, here, and every mode below is handed the
		// same slice — which is what makes "the three modes differ in
		// presentation and not in content" a fact about the call rather than a
		// promise about three renderers (R2.1).
		if err := renderCheckReportIn(mode, run.Sections(content), device); err != nil {
			logger.Warn("the report could not be rendered: %v", err)
		}

		// S045-R5.2: a run that found something pending names the command
		// that lists it. This is the last of the three facts that have to
		// outlive the retired legacy check printer — R5.1's tally went into
		// the section (sub-task 2.1) and R5.3's empty-scan sentence into the
		// arm above (3.3).
		//
		// AFTER the render, deliberately: the render is the answer, and this
		// is a footnote about what to do next.
		//
		// Once per run, because the gate asks about the scan as a whole and
		// not about a row — neither the number of sections nor --all can
		// multiply it — and never on the empty arm, which does not reach this
		// line and would find nothing to point at if it did.
		//
		// output.Info is the channel the sentence came out on before, and it
		// is not one --quiet can reach. Keeping it is the parity S045-R5.4
		// exists for: moving a line to a quieter channel while reporting no
		// behaviour change is precisely the silent regression that requirement
		// is there to catch.
		//
		// A render that failed above does not withhold it. R5.2 is about what
		// the run FOUND, not about whether the terminal accepted the table,
		// and that failure has already been reported on its own line.
		if scanFoundPendingUpdate(r.Scanned) {
			output.Info.Println("Use 'bentoo overlay autoupdate --list' to see pending updates")
		}

		// The registry was WRITTEN, and that is not something the report can
		// say. CheckAll auto-disables an entry whose ebuild has vanished and
		// warns only when the write fails, so a successful one would otherwise
		// mutate a hand-maintained packages.toml in silence — the retired
		// legacy check printer was what said it, and its last caller went away
		// in this story (S045-R5.4).
		//
		// It belongs here for D5's reason, one step stronger than the hint's:
		// internal/common/report/render must not name a bentoo subcommand, and
		// it must certainly not know that packages.toml exists. The report
		// already states the orphan COUNT; this states the consequence.
		//
		// One sentence for the run, because DisableOrphans performs one batched
		// write. The single-package path never lost this — it says the same
		// thing at overlay_autoupdate.go and returns before reaching here, so
		// the two cannot both fire.
		if orphans := scanDisabledOrphans(r.Scanned); orphans > 0 {
			output.Warning.Printf("%d package(s) had no ebuild and were disabled in packages.toml (enabled = false)\n", orphans)
		}
	}

	// Reached on BOTH arms above, and the `else` is what makes that true: the
	// empty scan skips the render and nothing else. An early return there would
	// have made `--export` silently conditional on the scan finding something,
	// which S045-R3.3 does not allow and S045-R4.3 asks for the opposite of.
	//
	// LAST, after the render, and that ordering is R3.5's: an export is an
	// additional copy of an answer already delivered — rendered above, or stated
	// by the logger on the empty scan — so a path that cannot be written must
	// cost neither that answer nor the exit status. exportReport is the CLI's
	// one export path (report_export.go); this command supplies a report.Run and
	// decides nothing else about it, which is what lets `overlay manifest` and
	// `snapshot run` reach the same behaviour with the same one line.
	exportReport(run)
}

// exportContent is what an EXPORT asks the report to say (R9.3, R2.4).
//
// # It is a caller's decision, which is why it is in cmd and not in the model
//
// report.AutoupdateCheck.Sections ANSWERS these two questions; nothing in the model gets
// to choose them. Whether a record lists every scanned package or counts them,
// and whether it states the plan, is a property of the artefact being produced
// — and cmd/bentoo is the layer that knows it is producing a file rather than a
// screen. It is no longer this COMMAND's decision either: --export is the root's
// now, so the answer below is the CLI's and is the same for every producer.
//
// # It is built here rather than in overlay_autoupdate_ui.go, where the export lives
//
// Constructing a report.SectionOptions means writing down the field that omits
// the plan, and a source-text guard over that file forbids the name there —
// precisely so an export can never acquire one. The value is keepThePlan,
// declared beside the export it belongs to: a record missing the plan answers no
// question later, because the plan is where a package's reason is stated at all
// (R7.2).
//
// # It takes no argument, and that is R3.4 written as a signature
//
// It used to take a listEvery bool, because the Markdown export LISTED every
// package a run found up to date while the plain export COUNTED them. That
// disagreement was inherited verbatim from the renderers story 046 replaced —
// preserved deliberately at the time, since changing it then would have moved a
// render nobody had asked to move, and left open.
//
// R3.4 closes it: the file carries the complete report, every unit, nothing
// shortened, whatever the terminal was told. Two answers cannot both be that,
// and the plain export was the one that was not — an operator who opened a
// .log instead of a .md got a record that could not answer "was this package
// checked at all". Both formats now ask for everyScannedPackage.
//
// The parameter went with it rather than being passed the same constant twice.
// A knob that shortens an export is a knob an export can be shortened by, and a
// function with no parameter cannot be handed one in a hurry.
func exportContent() report.SectionOptions {
	return report.SectionOptions{ShowAll: everyScannedPackage, SkipPlan: keepThePlan}
}

// scanDisabledOrphans counts the packages this run auto-disabled: an entry
// whose ebuild has vanished from the overlay. CheckAll writes them back in one
// batch (internal/autoupdate/checker.go, DisableOrphans) and warns only when
// that write FAILS, so a successful one is otherwise silent.
func scanDisabledOrphans(scanned []report.PackageResult) int {
	orphans := 0
	for _, result := range scanned {
		if result.Orphaned {
			orphans++
		}
	}
	return orphans
}

// scanFoundPendingUpdate reports whether the scan turned up at least one
// pending update — the condition S045-R5.2 gates its hint on.
//
// # It reads HasUpdate, and never compares the two version strings
//
// report.PackageResult.HasUpdate is false whenever the candidate could not be
// ordered against the current version, and NotComparable is carried separately
// so a broken parser is never read as "up to date". A `candidate != current`
// comparison would count exactly those packages as pending: the operator would
// be sent to a list the package does not appear in, because a version nothing
// could order was never recorded as an update in the first place.
//
// # It answers a boolean rather than a count, and that is S045-R1.4
//
// The hint states no number. The count over these same packages is the
// report's own — the section's lead says "N package(s) checked, M with a
// pending update" — and a second one produced out here would be a tally
// printed from outside the report, which R1.4 does not allow.
//
// An empty scan therefore answers false by construction, which is what keeps
// presentCheckReport's empty arm silent (S045-R5.3): a run that looked at
// nothing found nothing, and there is no branch to get that wrong in.
func scanFoundPendingUpdate(scanned []report.PackageResult) bool {
	for _, pkg := range scanned {
		if pkg.HasUpdate {
			return true
		}
	}
	return false
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

// The tally that used to be printed here is validationSummarySection,
// and it now has FOUR counts rather than three: the old "not validated" column
// held both the packages policy excluded and the packages the toolkit could not
// evaluate, so a defect in the toolkit was reported in the same number as the
// operator's own choice (R5.1). Proved and errored count exactly what they
// counted before (R5.7). The reminder that a check publishes nothing moved with
// it, so it is still the last sentence a reader sees.
