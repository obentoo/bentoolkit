package main

// `overlay autoupdate --check --llm`: prove what would survive an apply, and
// apply none of it. The code is arranged around the two halves that can go wrong.
//
// THE COST. A gate above `options` unpacks and builds, so a check can silently
// cost hours and a pile of downloaded tarballs. Everything the operator needs to
// price that — how many packages, at what depth, how many distfiles, and how the
// depths are distributed — is printed BEFORE anything is asked and before the
// first gate runs, and one confirmation covers the whole run, in confirmSweep's
// shape (overlay_autoupdate_sweep.go) so the two commands read alike.
//
// THE REACH. `--check` is documented read-only and the overlay it would write
// to auto-commits and pushes. Nothing here writes to the overlay on any path:
// deps.setVersionsForCheck is the one call that could, and it exists precisely
// so that a test can watch a seam that is never used.
//
// Findings go into internal/common/report's view model, whole, and render
// measures its own columns, so this file declares no field width (a test greps
// it for one, comments included). Only events of the RUN print from here: the
// price, and a raise held at the confirmed depth.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/parse"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
)

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
	// field rather than derived at print time because the reason has to
	// travel with it: such a package is counted as skipped, not validated.
	Skipped bool
	// ConfirmedDepth is the deepest depth the operator approved for the WHOLE
	// run, handed to the gate runner with each entry. It is the ceiling for
	// escalation: a reviewer may raise a bump up to it without asking again, and a
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
	// about the easy packages and quietly say nothing about the rest.
	Entries []validationPlanEntry
	// DistfilesToFetch is how many packages this run has to hold a tarball for.
	// Every depth above `none` reads the new archive, so each such package needs
	// its distfile: on a metered connection or a laptop that number is what
	// decides whether the answer is yes, and it appears nowhere else in the
	// output.
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
// report.AutoupdateCheck.Reconciles reports whether the columns sum to the plan.
// A package counted twice, or in two columns, is worse than no tally at all —
// it turns the one number anybody remembers into a number nobody can reconcile
// with the list above it.

// buildValidationPlan prices a run: it resolves the depth for every pending
// update and nothing else.
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
			parse.NormalizeUpstreamVersion(update.NewVersion))

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
// confirmation covers, and the yardstick a reviewer's raise is measured
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
// unpack and build, and therefore the only reason to ask anything.
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
// spent: how many packages, each one's depth, which are not validated, how many
// distfiles the run needs, and how the depths are distributed.
//
// It prints no reasons: the plan's PRESENTATION is a section of the report
// (validationPlanSection), rendered once when the run is over, and printing the
// reasons here as well would put the same sentence on screen twice in one run.
//
// It is not a second renderer of the view model. It renders the PLAN, a
// producer artefact, at a moment when no report exists yet, and the run-level
// facts below — `deepest` and the distribution's ordering — need the depth
// ladder, which the model deliberately does not carry.
//
// It declares no column width: a pre-run price is a list of facts, and the
// aligned table is the report's job, measured from the packages this run
// actually produced rather than typed into a format string.
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

// confirmValidationRun takes ONE confirmation for the whole run.
//
// The three gates are confirmSweep's, in the same order and for the same
// reasons: --yes proceeds unattended because the operator asked for that in so
// many words; a non-interactive terminal without --yes runs nothing AND says how
// to proceed, because a run that silently did nothing is only marginally better
// than one that silently did the expensive thing; anything else is asked.
// deps.registryPromptIsInteractive requires BOTH stdin and stdout to be terminals, so
// `yes | bentoo overlay autoupdate --check` cannot answer for a human.
//
// A run with nothing above `options` asks nothing at all. The confirmation
// exists because a build costs hours; a plan that cannot start one has nothing
// to ask about, and a gate that asks anyway teaches the operator to answer
// without reading — which is how every confirmation gate dies.
func (ar *autoupdateRun) confirmValidationRun(plan validationPlan) bool {
	builds := plan.building()
	if builds == 0 {
		return true
	}

	deepest := plan.deepest()

	if ar.opts.yes {
		output.Warning.Printf("  --yes given: evaluating %d package(s) without a prompt — %d of them run a build gate, up to %s.\n",
			len(plan.Entries), builds, deepest)
		output.Warning.Printf("  Up to %d distfile(s) are fetched and the deepest gates can take hours. Nothing is published.\n",
			plan.DistfilesToFetch)
		return true
	}

	if !ar.deps.registryPromptIsInteractive() {
		output.Warning.Println("  Not an interactive terminal and --yes was not given: no gate ran and nothing was validated.")
		output.Info.Printf("  Re-run with --yes to evaluate these %d package(s) unattended — %d of them up to %s, fetching up to %d distfile(s).\n",
			len(plan.Entries), builds, deepest, plan.DistfilesToFetch)
		return false
	}

	fmt.Println()
	output.Warning.Printf("  %d package(s) run a gate above `options`, which unpacks and builds: this fetches up to %d distfile(s) and can take hours.\n",
		builds, plan.DistfilesToFetch)
	output.Info.Println("  Nothing is published either way — a check writes no ebuild and no version pin.")
	return ar.deps.confirmSweep(fmt.Sprintf(
		"Evaluate %d package(s), %d of them up to depth %s?", len(plan.Entries), builds, deepest))
}

// runPendingValidation puts every bump a check recorded as pending through the
// gates at its resolved depth — priced first, asked about once, tallied at the
// end, and published never.
//
// It returns the validation half and does not draw it. Scanned is left nil, so
// the half MUST be joined before display; runCheck, the one place holding both
// the scan and the validation, joins it, which leaves exactly one report per
// run. The second value is whether the plan was already printed, so the caller
// does not show it again under a second heading; it is true on every path past
// that print, including a declined confirmation.
//
// It is gated on --llm because a gate above `options` unpacks and builds (and
// even `options` fetches a distfile): running it on every `--check` would turn
// seconds into hours for someone typing the command they always typed. The gate
// decides validation only — without --llm it yields nothingValidated's run and
// the caller still draws the scan, so --ui, --all and --export keep working.
//
// It publishes nothing, structurally: deps.setVersionsForCheck is never called,
// and the applier runs Validate, never Apply (promotion, pin, `--clean`).
func (ar *autoupdateRun) runPendingValidation(ctx context.Context, overlayPath, configDir string, checked []autoupdate.CheckResult, llmCfg config.LLMConfig) (report.Run, bool) {
	if !ar.opts.llm {
		return nothingValidated(), false
	}

	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		ar.log().Warn("could not read the pending list, so nothing was validated", "err", err)
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

	plan := planValidation(updates, ar.validate.Policy, ar.validate.Depth,
		func(update autoupdate.PendingUpdate) string { return tier[update.Package] })

	// Printed here rather than left to runValidationCheck, because the
	// confirmation below is about this plan: a question about a cost nobody has
	// seen is not a confirmation. Printed records that it has been shown, so it is
	// not repeated.
	printValidationPrice(plan)
	plan.Printed = true
	if !ar.confirmValidationRun(plan) {
		// Nothing was validated, so there is no half to hand back. The plan is on
		// screen either way, though, which is what the second value reports: the
		// operator has just read it and answered no, and a false here would ask
		// the caller to draw it to them a second time.
		return nothingValidated(), true
	}

	opts := []autoupdate.ApplierOption{
		autoupdate.WithApplierPackagesConfig(loadPackagesConfigForApply(ar.log(), overlayPath)),
		applierFixerOption(ar.log(), llmCfg),
	}
	opts = append(opts, applierGentooPathOption())
	opts = append(opts, ar.applierDistfileOptions()...)
	opts = append(opts, ar.applierValidateOptions(configDir)...)
	opts = append(opts, applierLLMOptions(ar.log(), ar.opts.llm, llmCfg, ar.validateCfg)...)

	// Deliberately NOT WithApplierClean: `--clean` deletes published ebuilds, and
	// a read-only check has no business owning that switch even by accident.
	applier, err := autoupdate.NewApplier(overlayPath, configDir, opts...)
	if err != nil {
		ar.log().Warn("could not initialize the validator, so nothing was validated", "err", err)
		// Printed, for the same reason the declined answer above is: the price
		// reached the screen before this failed, so the caller must not repeat it.
		return nothingValidated(), true
	}

	finished := runValidationCheck(plan, func(entry validationPlanEntry) validate.EbuildResult {
		// The ceiling one confirmation covered. It travels per entry so a
		// reviewer's raise is held against what the operator approved rather than
		// against this bump's own depth. A depth the plan could not spell
		// is no ceiling at all, which is the honest reading — nothing was
		// confirmed about a number nobody printed.
		ceiling, err := validate.ParseDepth(entry.ConfirmedDepth)
		if err != nil {
			ceiling = validate.DepthNone
		}
		return applier.Validate(ctx, entry.Package, ceiling)
	})

	return finished, true
}

// runValidationCheck evaluates every planned package through `run` and returns
// the whole run as the view model.
//
// THE PRICE IS PRINTED HERE, not left to the caller: a plan printed beside the
// results is worth nothing, because by then the hours are spent. Printing it
// first makes "the whole plan precedes the first gate" a property of the
// function rather than of a calling convention somebody can forget. A caller
// that already showed the price to ask about it sets plan.Printed.
//
// It RETURNS the report and does not render it: a run that then fails to
// render, or is interrupted on its way to the screen, still holds a complete
// description of what it found, the caller can join the scan half before
// anything is displayed, and `--export` stays the command's decision. The
// counts come from report.Classify through buildReport and nowhere else, so the
// tally on screen and in the JSON export cannot disagree.
//
// It publishes nothing, on every path — see deps.setVersionsForCheck.
func runValidationCheck(plan validationPlan, run func(validationPlanEntry) validate.EbuildResult) report.Run {
	if !plan.Printed {
		printValidationPrice(plan)
		fmt.Println()
	}

	// The ceiling the confirmation covered. Every entry carries it into the
	// runner so a reviewer's raise is measured against what the operator
	// approved rather than against the entry's own depth.
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
// terminal first, then the export. A bad export path must never cost the
// operator the report itself, and it changes no verdict, count or exit status;
// a failed render does not withhold the file, which may be the only copy left.
//
// The mode is resolved here through reportModeOrPlain, so every error reaching
// it is an AMBIENT one (BENTOO_UI or ui.mode — the root already stopped a bad
// --ui): it falls back to plain, states the refusal once, and the exit status
// does not move. This is the ONLY voice for that refusal; the gate in
// runAutoupdate records its own failure at debug level.
//
// The `--list` hint is printed from here, not from versionCheckSection: render
// prints sections for every command, so a hint in the section would make every
// report carry this one command's advice. planPrinted is a parameter, not a
// model field, because "has the plan been shown" is about this terminal and
// must not reach the export, which always states the plan in full. An empty
// scan is not drawn — --quiet reaches only the logger, so the logged sentence
// keeps an empty `--check --quiet` silent — but it is still exported: no file
// at all would be indistinguishable from a command that never ran.
func (ar *autoupdateRun) presentCheckReport(run report.Run, planPrinted bool) {
	// This command's own facts, read back out of the run that carries them: the
	// three decisions below — whether to render at all, whether to point at
	// `--list`, whether to announce the registry write — are all about packages,
	// and the envelope deliberately knows nothing about packages.
	r := checkPayload(ar.log(), run)

	// Silent only when the report holds NOTHING, which is a conjunction rather
	// than the scan alone. CheckAll skips disabled and held entries and
	// DisableOrphans auto-disables an entry whose ebuild has vanished, so
	// "every entry disabled, and a pending.json still on disk from an earlier
	// run" yields an empty Scanned beside a plan that was built, confirmed and
	// evaluated. Keying on the scan alone would discard that report — hours of
	// gate work the operator waited for and approved. Silence is for a run with
	// nothing to say, and a run holding results has something to say however
	// its scan came out.
	if len(r.Scanned) == 0 && len(r.Plan) == 0 {
		// Verbatim the sentence the retired legacy check printer emitted at
		// its own `len(results) == 0` guard, on the same channel. The
		// wording is load-bearing in both directions: it is what an operator
		// already greps for, and it is deliberately NOT the report's own lead
		// for this case ("No package is configured for autoupdate.",
		// versionCheckSection below), which the paragraph above explains this
		// path must never reach.
		ar.log().Info("No packages configured for autoupdate")
	} else {
		// Two questions, kept apart. What the report should SAY — list every
		// up-to-date package, state the plan — is report.SectionOptions,
		// answered here from the flags. What the DEVICE allows is
		// render.Options, and its Width is left at zero: "ask the device". A
		// number typed here would be a hard-coded field width.
		//
		// SkipPlan omits the report's own plan section on a run whose price
		// was already printed to ask the confirmation question: the operator
		// has just read that list, and drawing it again under a second heading
		// would say the same thing twice.
		content := report.SectionOptions{ShowAll: autoupdateAll, SkipPlan: planPrinted}
		device := render.Options{}

		mode := reportModeOrPlain(ar.log(), ar.uiConfig, ar.opts.noTUI, ar.deps.uiIsTerminal)

		// The sections are built ONCE, here, and every mode below is handed the
		// same slice — which is what makes "the three modes differ in
		// presentation and not in content" a fact about the call rather than a
		// promise about three renderers.
		if err := renderCheckReportIn(mode, run.Sections(content), device); err != nil {
			ar.log().Warn("the report could not be rendered", "err", err)
		}

		// A run that found something pending names the command that lists it.
		//
		// AFTER the render, deliberately: the render is the answer, and this
		// is a footnote about what to do next. Once per run, because the gate
		// asks about the scan as a whole and not about a row — neither the
		// number of sections nor --all can multiply it — and never on the
		// empty arm, which does not reach this line.
		//
		// output.Info is the channel the sentence always came out on, and it
		// is not one --quiet can reach; moving it to a quieter channel would
		// be a silent behaviour change.
		//
		// A render that failed above does not withhold it: the hint is about
		// what the run FOUND, not about whether the terminal accepted the
		// table, and that failure has already been reported on its own line.
		if scanFoundPendingUpdate(r.Scanned) {
			output.Info.Println("Use 'bentoo overlay autoupdate --list' to see pending updates")
		}

		// The registry was WRITTEN, and that is not something the report can
		// say. CheckAll auto-disables an entry whose ebuild has vanished and
		// warns only when the write fails, so a successful one would otherwise
		// mutate a hand-maintained packages.toml in silence.
		//
		// It belongs here for the hint's reason, one step stronger:
		// internal/common/report/render must not name a bentoo subcommand, and
		// it must certainly not know that packages.toml exists. The report
		// already states the orphan COUNT; this states the consequence.
		//
		// One sentence for the run, because DisableOrphans performs one batched
		// write. The single-package path says the same thing at
		// overlay_autoupdate.go and returns before reaching here, so the two
		// cannot both fire.
		if orphans := scanDisabledOrphans(r.Scanned); orphans > 0 {
			output.Warning.Printf("%d package(s) had no ebuild and were disabled in packages.toml (enabled = false)\n", orphans)
		}
	}

	// Reached on BOTH arms above, and the `else` is what makes that true: the
	// empty scan skips the render and nothing else. An early return there would
	// have made `--export` silently conditional on the scan finding something,
	// while a file recording "this run scanned nothing" is the absence carried
	// honestly.
	//
	// LAST, after the render: an export is an additional copy of an answer
	// already delivered — rendered above, or stated by the logger on the empty
	// scan — so a path that cannot be written must cost neither that answer nor
	// the exit status. exportReport is the CLI's one export path
	// (report_export.go); this command supplies a report.Run and decides nothing
	// else about it, which is what lets `overlay manifest` and `snapshot run`
	// reach the same behaviour with the same one line.
	exportReport(ar.log(), run)
}

// exportContent is what an EXPORT asks the report to say.
//
// It is a caller's decision, which is why it is in cmd and not in the model:
// report.AutoupdateCheck.Sections ANSWERS these two questions, and whether a
// record lists every scanned package and states the plan is a property of the
// artefact being produced. --export is the root's flag, so the answer is the
// CLI's and the same for every producer.
//
// It is built here rather than in overlay_autoupdate_ui.go, where the export
// lives, because a report.SectionOptions names the field that omits the plan
// and a source-text guard over that file forbids the name there — precisely so
// an export can never acquire one. keepThePlan says why the plan stays.
//
// It takes no argument: the file carries the complete report whatever the
// terminal was told, so both formats ask for everyScannedPackage. A knob that
// shortens an export is a knob an export can be shortened by, and a function
// with no parameter cannot be handed one in a hurry.
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
// pending update — the condition the `--list` hint is gated on.
//
// It reads HasUpdate and never compares the two version strings:
// report.PackageResult.HasUpdate is false whenever the candidate could not be
// ordered against the current version, so a `candidate != current` comparison
// would send the operator to a list the package does not appear in.
//
// It answers a boolean rather than a count because the hint states no number:
// the count is the report's own ("N package(s) checked, M with a pending
// update"), and a second one printed from outside the report would be a tally
// the report does not own. An empty scan answers false by construction, which
// keeps presentCheckReport's empty arm silent.
func scanFoundPendingUpdate(scanned []report.PackageResult) bool {
	for _, pkg := range scanned {
		if pkg.HasUpdate {
			return true
		}
	}
	return false
}

// reportDepthEscalation says what happened to a bump the reviewer took past the
// depth the operator confirmed.
//
// It is printed here, not in the report, because a raise past the ceiling is an
// event of the RUN, not a finding about the package: the model describes what a
// run found and has no field for what the run declined to spend.
//
// The choice is to HOLD. Holding keeps the promise the plan made: a run whose
// plan resolved entirely to `options` asks for nothing, so a reviewer raising a
// bump to `compile` afterwards would spend hours the operator was never shown.
// The ceiling travels with every entry (validationPlanEntry.ConfirmedDepth) so
// the runner can hold there, and the lines below name the package, the raise
// and the ceiling so the hold is never silent — a held bump the operator cannot
// see is just a missing result. The atom is named because a warning about "this
// bump" is unactionable in a run of forty packages.
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
	output.Info.Printf("      A raise past the confirmed depth is held at %s rather than spent unasked; re-run with --yes to approve the deeper gates for the whole run.\n",
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

// The tally is validationSummarySection, with FOUR counts: "not validated" is
// split so that the packages policy excluded and the packages the toolkit could
// not evaluate are no longer one number — a toolkit defect must not be reported
// as the operator's own choice. Proved and errored count what they always did.
// The reminder that a check publishes nothing moved with it, so it is still the
// last sentence a reader sees.
