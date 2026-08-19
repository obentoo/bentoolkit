package main

// `overlay autoupdate --check --llm`: prove what would survive an apply, and
// apply none of it (S033-R9).
//
// The command has two halves that can go wrong, and the code below is arranged
// around them rather than around the data.
//
// THE COST. A gate above `options` unpacks and builds, so a check can silently
// cost hours and a pile of downloaded tarballs. Everything the operator needs to
// price that — how many packages, at what depth, why, how many distfiles, and
// how the depths are distributed — is printed BEFORE anything is asked and
// before the first gate runs (R9.3), and one confirmation covers the whole run
// (R9.4). The confirmation is confirmSweep's shape, gate for gate
// (overlay_autoupdate_sweep.go:241), so the two commands read alike.
//
// THE REACH. `--check` is documented read-only and the overlay it would write
// to auto-commits and pushes. Nothing here writes to the overlay on any path:
// setVersionsForCheck below is the one call that could, and it exists precisely
// so that a test can watch a seam that is never used (R9.2).

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

// validationTally is what the operator reads and acts on (R9.5).
//
// Each planned package lands in exactly one column. A package counted twice, or
// in two columns, is worse than no tally at all: it turns the one number anybody
// remembers into a number nobody can reconcile with the list above it.
type validationTally struct {
	// Proved counts packages whose deciding gates all passed.
	Proved int
	// Errored counts packages with at least one failing gate.
	Errored int
	// Skipped counts packages no deciding gate answered for — depth none, an
	// unpreparable tree, a missing dependency. "Nothing was said" is never
	// folded into Proved.
	Skipped int
}

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
		class, note := validate.ClassifyForDepth(update.CurrentVersion, update.NewVersion)

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

// printValidationPlan puts the whole cost on screen before any of it is spent
// (R9.3): how many packages, each one's depth and the case for it, which are
// skipped and why, how many distfiles the run needs, and how the depths are
// distributed across the run.
func printValidationPlan(plan validationPlan) {
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
			tag = "  [not validated]"
		}
		fmt.Printf("  %-45s %s → %s\n", entry.Package, entry.From, entry.Version)
		fmt.Printf("      %s at depth %s%s\n", entry.Class, entry.Depth, tag)
		fmt.Printf("      %s\n", entry.Reason)
	}
	fmt.Println()

	// The two numbers an operator actually decides on.
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
	printValidationPlan(plan)
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
	runValidationCheck(plan, func(entry validationPlanEntry) validate.EbuildResult {
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
}

// runValidationCheck evaluates every planned package through `run` and reports
// what each one said (R9.1, R9.5).
//
// THE PLAN IS PRINTED HERE, not left to the caller. "A plan is printed" is
// satisfied by printing it beside the results, which is worth nothing: by then
// the hours are spent. Printing it as the first thing this function does makes
// "the whole plan precedes the first gate" a property of the function rather
// than of a calling convention somebody can forget. A caller that already showed
// the plan in order to ask about it sets plan.Printed and is not repeated.
//
// It publishes nothing, on every path — see setVersionsForCheck.
func runValidationCheck(plan validationPlan, run func(validationPlanEntry) validate.EbuildResult) validationTally {
	if !plan.Printed {
		printValidationPlan(plan)
		fmt.Println()
	}

	// The ceiling the confirmation covered. Every entry carries it into the
	// runner so a reviewer's raise is measured against what the operator
	// approved rather than against the entry's own depth (R9.6).
	confirmed := plan.deepest()

	var tally validationTally
	for _, entry := range plan.Entries {
		entry.ConfirmedDepth = confirmed.String()

		result := run(entry)
		reportValidationOutcome(entry, result, confirmed)

		// Exactly one column per package. WorstOutcome answers SKIPPED for a
		// result carrying no deciding gate at all, which is the honest answer:
		// nothing was said about that bump, and "nothing" read as a pass is the
		// defect this whole story exists to remove.
		switch result.WorstOutcome() {
		case validate.OutcomePass:
			tally.Proved++
		case validate.OutcomeFailed:
			tally.Errored++
		default:
			tally.Skipped++
		}
	}

	printValidationTally(plan, tally)
	return tally
}

// reportValidationOutcome prints one package's result, and — when the depth that
// ran is past the depth the operator confirmed — says what happens to that raise
// (R9.6).
//
// THE CHOICE THIS IMPLEMENTATION MAKES IS TO HOLD. R9.6 allows either answer,
// and holding is the one that keeps the promise the plan made: a run whose plan
// resolved entirely to `options` asks for nothing, so a reviewer raising a bump
// to `compile` afterwards would spend hours the operator was never shown. The
// ceiling travels with every entry (validationPlanEntry.ConfirmedDepth) so the
// runner can hold there, and the line below names the raise and the ceiling so
// the hold is never silent — a held bump the operator cannot see is just a
// missing result.
func reportValidationOutcome(entry validationPlanEntry, result validate.EbuildResult, confirmed validate.Depth) {
	outcome := result.WorstOutcome()
	switch outcome {
	case validate.OutcomePass:
		output.Success.Printf("  %-45s %s proved at %s\n", entry.Package, entry.Version, reportedDepth(result, entry))
	case validate.OutcomeFailed:
		output.Error.Printf("  %-45s %s FAILED at %s\n", entry.Package, entry.Version, reportedDepth(result, entry))
		for _, gate := range result.Gates {
			for _, finding := range gate.Findings {
				fmt.Printf("      %s: %s\n", gate.Gate, finding.Detail)
			}
		}
	default:
		output.Warning.Printf("  %-45s %s not validated (%s)\n", entry.Package, entry.Version, skipReason(result, entry))
	}

	// R9.6. ParseDepth failing means the runner did not report a depth at all,
	// which is not an escalation — only a depth it NAMED can be compared against
	// the ceiling.
	actual, err := validate.ParseDepth(result.Depth)
	if err != nil || actual <= confirmed {
		return
	}
	output.Warning.Printf("      the reviewer raised this bump to %s, past the %s this run's plan confirmed.\n", actual, confirmed)
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

// printValidationTally is the last thing on screen (R9.5): the three counts, and
// the reminder that none of it left this machine.
func printValidationTally(plan validationPlan, tally validationTally) {
	fmt.Println()
	output.Header.Println("Validation Result")
	fmt.Println()
	output.Info.Printf("  %d package(s) evaluated: %d proved, %d errored, %d not validated.\n",
		len(plan.Entries), tally.Proved, tally.Errored, tally.Skipped)
	output.Info.Println("  Nothing was published: a check writes no ebuild and no version pin (R9.2).")
}
