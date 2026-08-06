package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

// This file is `bentoo overlay prune`: the command that removes overlay
// packages ::gentoo already ships, and — much more often — explains why it will
// not.
//
// # Where the operator-facing text goes
//
// Everything the operator must read is printed on STDOUT, through fmt and the
// output/* colours, and not through logger. logger binds its io.Writer once at
// first use and it is os.Stderr (logger.go:44-52), so a message sent there
// lands on a different stream from the plan it belongs to — including the
// failures, which exist precisely to explain the plan that is missing. Splitting
// one report across two streams costs the operator the ordering between them the
// moment either is redirected. logger is still used for the one thing that is
// genuinely an aside: a registry key that is not a category/package atom, which
// leaves the plan complete and merely leaves that key unattributed. A registry
// that cannot be read AT ALL is the opposite of an aside — it decides the whole
// run — so it is reported on stdout with everything else, by
// reportPruneRegistryUnreadable.
//
// # Flags declared here, behaviour split across the story
//
// The four flag variables and the three seams below are the whole surface. The
// plan-only path is complete; the --apply path (confirmation, execution,
// registry edit, exit codes) is wired into runPrune's marked tail.

// prunePlannerFn, pruneExecutorFn and confirmPruneFn are the seams the CLI tests
// drive, defaulting to the real implementations so a caller that supplies none
// gets production behaviour. Same shape as the sweep's
// (overlay_autoupdate_sweep.go:19-23), and for the same reason: a test has to be
// able to prove that the executor was NOT REACHED, which is only observable if
// reaching it goes through a replaceable name.
// pruneInteractiveFn is the fourth seam, and it exists because the thing it
// reports cannot be faked from a test at all: stdinIsTerminal asks the operating
// system whether stdin is a character device, and under `go test` the answer is
// always no. Without a seam, every interactive requirement — R6.1's confirmation
// and R4.4's refusal to accept one from a script — would be untestable in the
// only direction that matters, since the non-interactive answer is the one the
// runner hands out for free.
var (
	prunePlannerFn     = overlay.PlanPrune
	pruneExecutorFn    = overlay.ExecutePrune
	confirmPruneFn     = confirmAction
	pruneInteractiveFn = stdinIsTerminal
)

var (
	// pruneApply is --apply: without it the command plans and prints, and the
	// executor is unreachable (R1.1).
	pruneApply bool
	// pruneIncludePatched is --include-patched: the operator saying out loud that
	// they accept discarding local work (R4.1). It never moves a package between
	// buckets — it only decides whether the diverging bucket may be acted on.
	//
	// Its name is wider than its reach, and the gap is worth stating once. The
	// diverging bucket holds UNDECLARED divergence only: a registry entry declaring
	// `patched` is one of the two axes deriveVerdict reads (compare.go:193-201), so
	// a declared package's verdict is `keep` or `needs-rebase` and PlanPrune refuses
	// it at the verdict gate before comparing any content (R2.5). R2.4's "refuse a
	// declared package unless --include-patched" is therefore defence in depth
	// rather than a live path — it holds the line if the verdict ever stops
	// disqualifying a declaration. Clearing a stale declaration is `overlay
	// analyze`'s business, and doing it there leaves a record this flag would not.
	pruneIncludePatched bool
	// pruneKeepRegistry is --keep-registry: remove the package directories and
	// leave .autoupdate/packages.toml alone. The plan says which of the two it is,
	// because "these entries would be deleted" is false under this flag.
	pruneKeepRegistry bool
	// pruneYes is --yes: skip the identical batch's confirmation, never the
	// diverging one.
	pruneYes bool
)

// pruneNoLocalTreeRefusal is what an API-only run is told, in the CLI's own
// words. internal/overlay refuses the same case with a reason about the PROVIDER
// (prune.go's pruneNoLocalTreeReason) and deliberately names no flag; naming the
// way out is this layer's job, because only this layer knows what the operator
// can type.
const pruneNoLocalTreeRefusal = "no local ::gentoo tree; re-run with --clone or a local provider"

var pruneCmd = &cobra.Command{
	Use:   "prune [category[/package]]",
	Short: "Remove overlay packages ::gentoo already ships identically",
	Long: `Plan the removal of overlay packages ::gentoo already ships, and with
--apply carry that plan out.

Only CONTENT may authorise a removal. The verdict from 'bentoo overlay compare'
selects the candidates, and then every version the two trees share, plus the
whole files/ tree, must match byte for byte. The two disagree in practice:
measured on the live overlay, 8 of the 74 packages the verdict calls redundant
carry real local changes, so a removal driven by the verdict alone deletes work.

Without --apply nothing is removed, modified or disabled: the plan is printed
and the run ends. The plan lists, per package, every file that would go and
every .autoupdate/packages.toml entry that would go with it.

Packages carrying an UNDECLARED difference — one the byte comparison found and
nobody wrote down — are printed under their own heading and left alone unless
--include-patched says otherwise.

A package whose registry entry DECLARES 'patched' is refused outright, and no
flag here reaches it. The declaration already makes 'overlay compare' call it
'keep' rather than 'redundant', and this command never removes a package the
verdict refused. If such a copy is genuinely obsolete, clear its declaration
first — that is 'overlay analyze''s business, and it leaves a record of the
decision that a flag on this command would not.

Packages no run may remove are printed last, each with its reason.

A local ::gentoo tree is required, because an API provider has no content to
authorise anything with. Configure one in ~/.config/bentoo/config.yaml:

  repositories:
    gentoo:
      provider: local
      path: /var/db/repos/gentoo

Examples:
  bentoo overlay prune                          # plan the whole overlay
  bentoo overlay prune app-editors              # plan one category
  bentoo overlay prune app-editors/zed          # plan one package
  bentoo overlay prune --include-patched        # consider the diverging ones too
  bentoo overlay prune --apply                  # carry the plan out
  bentoo overlay prune --apply --keep-registry  # remove the files, keep the registry`,
	Args: cobra.MaximumNArgs(1),
	Run:  runPruneCmd,
}

func init() {
	pruneCmd.Flags().BoolVar(&pruneApply, "apply", false, "Carry out the plan (default: plan only, remove nothing)")
	pruneCmd.Flags().BoolVar(&pruneIncludePatched, "include-patched", false, "Also remove packages carrying an UNDECLARED difference, discarding that work (a declared 'patched' entry is refused regardless)")
	pruneCmd.Flags().BoolVar(&pruneKeepRegistry, "keep-registry", false, "Leave .autoupdate/packages.toml untouched")
	pruneCmd.Flags().BoolVar(&pruneYes, "yes", false, "Skip the identical batch's confirmation (never the diverging one)")
	overlayCmd.AddCommand(pruneCmd)
}

// runPruneCmd is the cobra half: signals, config, overlay path. Everything that
// can be decided without them lives in runPrune, which takes both as parameters
// so the whole flow is drivable from a test — the same split runRevive and
// runSweep use.
func runPruneCmd(cmd *cobra.Command, args []string) {
	// SIGINT/SIGTERM reach the comparison through the context, so an interrupted
	// run stops looking at packages instead of finishing the scan first.
	ctx, stop := signalContext(cmd.Context())
	defer stop()

	appCtx, err := loadAppContext()
	if err != nil {
		output.Error.Printf("  loading config: %v\n", err)
		osExit(1)
		return
	}

	runPrune(ctx, appCtx.OverlayPath, args, appCtx.Config)
}

// runPrune plans what may leave the overlay, prints it, and — with --apply —
// carries it out.
//
// # The order is the safety property (design D4)
//
// Plan, print, confirm, execute, with the executor UNREACHABLE when there is no
// --apply or the confirmation is declined. R1.1 therefore holds by construction
// rather than by a well-placed condition: a plan-only run has no path to a
// removal at all. This is the sweep's own argument (overlay_autoupdate_sweep.go)
// and it is here for the same reason — the overlay auto-commits and pushes, so a
// wrong removal is a published removal within minutes.
//
// R6.3 holds the same way, one step further in: consent does not return a
// boolean the executor is then trusted to respect, it BUILDS THE BATCH the
// executor is given. A declined batch is not in the struct that reaches it, so
// there is no plan for it to ignore.
//
// # Two confirmations, and they are not symmetric (design D4)
//
// The identical batch loses nothing that is ours — every byte of it is in
// ::gentoo — so one prompt covers it and --yes may answer that prompt (R6.1,
// R6.2). The diverging batch discards work that exists nowhere else, so it asks
// separately, names every package, and refuses a session with no terminal
// whether or not --yes was given (R4.3, R4.4).
//
// # What the exit code means (design D7)
//
//	plan printed, nothing applied ........ 0
//	every planned removal succeeded ...... 0
//	no package qualified ................. 0
//	a confirmation was declined .......... 0  — the operator was asked and answered
//	one or more removals failed .......... 1
//	no confirmation could be obtained .... 1  — non-interactive without --yes (R6.2),
//	                                            or the diverging batch with no
//	                                            terminal (R4.4)
//
// The last line is the one worth stating out loud: a refusal is not a decline. A
// declined run did exactly what the operator told it to; a refused one did not do
// what they asked, and a script reading 0 would record the removals as done.
//
// # Why the local checks come first
//
// The overlay scan and the target check run before the provider is resolved.
// Resolving it can fetch the repository registry, and a typo in the target must
// not cost a network round trip before it is reported. The scan is also the
// authority the target is checked against: it is the set of packages that
// actually exist here.
func runPrune(ctx context.Context, overlayPath string, args []string, cfg *config.Config) {
	fmt.Println()
	output.Header.Println("Overlay Prune")
	fmt.Println()

	scan, err := overlay.ScanOverlay(overlayPath)
	if err != nil {
		output.Error.Printf("  cannot scan the overlay at %s: %v\n", overlayPath, err)
		osExit(1)
		return
	}
	if len(scan.Errors) > 0 {
		// An incomplete scan makes the plan incomplete, and a plan that silently
		// understates its own coverage is the failure R1.5 is about one level up:
		// a package absent from the report may simply never have been seen.
		output.Warning.Printf("  %d path(s) could not be scanned; a package missing below may simply not have been seen.\n", len(scan.Errors))
		for _, e := range scan.Errors {
			output.Info.Printf("    %s: %s\n", e.Path, e.Message)
		}
		fmt.Println()
	}

	var target string
	if len(args) > 0 {
		target = args[0]
	}

	packages, err := selectPrunePackages(scan.Packages, target)
	if err != nil {
		// R1.3. The quiet failure this prevents: an unmatched restriction yields an
		// empty plan, an empty plan reads as "the overlay is clean", and the operator
		// walks away from a misspelled category believing they were told something.
		output.Error.Printf("  %v\n", err)
		output.Info.Println("  Give a category (app-editors), a category/package (app-editors/zed), or no argument at all.")
		osExit(1)
		return
	}

	if len(packages) == 0 {
		// Reachable only without a target — with one, no match is the error above.
		// It is the "nothing was EXAMINED" side of R1.5 and not the "nothing
		// qualified" side: no package was compared, because there was none.
		reportPruneNothingExamined("  Nothing was examined: this overlay holds no package to compare.")
		return
	}

	prov, err := resolveGentooProviderFn(cfg)
	if err != nil {
		output.Error.Printf("  %v\n", err)
		osExit(1)
		return
	}
	defer prov.Close() //nolint:errcheck // closing a read-only provider cannot invalidate a plan already printed

	// D6/R2.6, and it is a GATE rather than a note printed afterwards: an API-only
	// provider can authorise nothing, so comparing anyway would spend one
	// rate-limited request per package — ~300 of them — to reach a refusal that was
	// already certain here. Refusing before the first request is what makes
	// "nothing was examined" literally true.
	//
	// It is NOT an error. The operator asked a reasonable question with a provider
	// that cannot answer it, so the command prints the plan it has and exits 0.
	if _, ok := prov.(provider.PackageDirProvider); !ok {
		reportPruneAPIOnly(prov.GetName(), len(packages))
		return
	}

	// What the overlay declares about itself, and which registry entries belong to
	// each atom. Both come out of the same .autoupdate/packages.toml, and both are
	// built HERE because cmd/ is the only package importing internal/overlay and
	// internal/autoupdate at once: internal/overlay must never learn what TOML is.
	divergence, divErr := buildDivergenceMap(overlayPath)
	registryKeys, malformed, keyErr := buildPruneRegistryKeys(overlayPath)
	if err := firstPruneError(divErr, keyErr); err != nil {
		// A GATE, like the API-only one above, and reported the same way: one
		// message for one cause, because both readers open the same file and two
		// failures would read as two problems.
		//
		// Carrying on would be SAFE but not informative. An absent divergence map
		// misses on every atom, a miss reaches deriveVerdict as known == false, and
		// that yields VerdictUnknown before any per-status rule runs
		// (compare.go:185) — so every package is refused at the verdict gate with
		// its content never compared (R2.5). The outcome is therefore already
		// decided here: comparing anyway would read ~300 package directories to
		// print the same "verdict is unknown" line about each of them, and would
		// then have to close with "nothing qualified", which is false. Nothing was
		// examined; a file could not be read.
		reportPruneRegistryUnreadable(err, len(packages))
		return
	}
	for _, key := range malformed {
		logger.Warn("registry key %q is not a category/package atom; it is not listed against any package below", key)
	}

	report, err := overlay.CompareWithProvider(packages, prov, overlay.CompareOptions{
		// Every compared package reaches the plan, so the three buckets account for
		// the whole scan: a package the report does not mention would read as one
		// that does not exist. There is no progress callback because there is
		// nothing to wait for — the gate above guarantees an on-disk provider, so
		// every lookup below is a local directory read.
		IncludeSynced:      true,
		IncludeNotInRemote: true,
		Concurrency:        overlay.DefaultCompareConcurrency,
		Ctx:                ctx,
		Divergence:         divergence,
		OverlayPath:        overlayPath,
	})
	if err != nil {
		output.Error.Printf("  comparing packages: %v\n", err)
		osExit(1)
		return
	}

	opts := overlay.PruneOptions{
		OverlayPath:    overlayPath,
		IncludePatched: pruneIncludePatched,
		RegistryKeys:   registryKeys,
	}

	batch := prunePlannerFn(report.Results, prov, opts)
	displayPrunePlan(batch, pruneKeepRegistry)

	eligible := pruneEligibleCount(batch)
	if eligible == 0 {
		// R1.5's other side: packages WERE examined and none of them may be
		// removed. That is "you are done", not "run it again differently".
		reportPruneNothingQualified(len(report.Results), len(batch.Diverging), pruneIncludePatched)
		return
	}

	// ---- the removal half of the command starts here ----
	//
	// Nothing above this line can remove anything: the planner has no capability to
	// delete, and the printer only reads.
	if !pruneApply {
		output.Info.Printf("  Nothing was removed: this is a plan. Re-run with --apply to carry out %d removal(s).\n", eligible)
		return
	}

	// Consent produces a BATCH, not a permission slip: consent.authorised holds
	// only the plans a confirmation covered, so a declined or unaskable batch has
	// no representation on the path to the executor (R6.3).
	consent := gatherPruneConsent(batch)

	// The executor is not called at all when consent covered nothing. That guard is
	// not what makes the run safe — an empty batch removes nothing either way — it
	// is what keeps the run from claiming to have tried.
	var results []overlay.PruneResult
	if consent.approved > 0 {
		results = pruneExecutorFn(consent.authorised, opts)
	}
	removed, failed := reportPruneOutcome(results, consent)

	// D7: the registry edit runs ONCE, after every removal, and only for the atoms
	// whose directory actually went. A package that failed to delete keeps its
	// entries, so the file never claims a removal that did not happen.
	var registryErr error
	if atoms := prunedRegistryAtoms(results); len(atoms) > 0 {
		if pruneKeepRegistry {
			// R5.3: untouched, not rewritten identically. The overlay auto-commits, so
			// a rewrite that happens to produce the same bytes is still a file the next
			// run has to explain.
			output.Info.Printf("  --keep-registry given: .autoupdate/packages.toml was not touched, and it still holds the entries of %d removed package(s).\n", len(atoms))
		} else {
			registryErr = removePruneRegistryEntries(overlayPath, atoms)
		}
	}

	// Last line of the report, and it is last on purpose: the registry edit above
	// is part of the same diff, so advice printed before it would send the operator
	// to look at half of what this run did.
	if removed > 0 {
		output.Info.Println("  Review the diff before it is published: 'bentoo overlay diff'")
	}

	// D7's exit table. All three causes are the same answer to a caller — "what you
	// asked for did not all happen" — and each is reported above in its own words.
	if failed > 0 || consent.refused > 0 || registryErr != nil {
		osExit(1)
		return
	}
}

// pruneConsentAnswer is what one confirmation gate returned.
//
// Three values and not a bool, because "the operator said no" and "nobody could
// be asked" mean different things to the exit code (design D7) and to the
// operator: the first is the command working, the second is the command unable
// to do what it was told.
type pruneConsentAnswer int

const (
	// pruneConsentUnavailable means no consent could be obtained at all — no
	// terminal, and no flag that stands in for one here. It is the ZERO VALUE
	// deliberately, matching PruneClass and ContentResult: an answer nobody filled
	// in must not read as permission to delete, and it must not read as a quiet
	// success either.
	pruneConsentUnavailable pruneConsentAnswer = iota
	// pruneConsentDeclined means the operator was asked and said no.
	pruneConsentDeclined
	// pruneConsentGiven is the only value that authorises a removal.
	pruneConsentGiven
)

// pruneConsent is what this run may act on, and what it may not.
type pruneConsent struct {
	// authorised holds ONLY the plans a confirmation covered, in the buckets they
	// were planned in. It is the whole of R6.3's structural guarantee: the executor
	// is handed this batch, so a declined plan is not something it is trusted to
	// skip — it is not there.
	authorised overlay.PruneBatch
	// approved counts the plans in authorised.
	approved int
	// declined counts the eligible plans the operator was asked about and refused.
	// Not a failure: they were asked and they answered, and the run did exactly
	// what they said.
	declined int
	// refused counts the eligible plans nobody could be asked about (R6.2, R4.4).
	// That IS a failure — the operator asked for a removal that did not happen —
	// and it is what makes such a run exit non-zero.
	refused int
}

// record folds one gate's answer into the tally and returns the plans that may
// be handed to the executor: the slice itself when consent was given, nil
// otherwise.
//
// It returns the plans rather than a bool because the caller then has nothing to
// file except what came out of here — a bucket of authorised cannot be populated
// beside a refusal, and the tally cannot drift from the batch it is counting.
func (c *pruneConsent) record(answer pruneConsentAnswer, plans []overlay.PrunePlan) []overlay.PrunePlan {
	switch answer {
	case pruneConsentGiven:
		c.approved += len(plans)
		return plans
	case pruneConsentDeclined:
		c.declined += len(plans)
		return nil
	default:
		// pruneConsentUnavailable, and anything a later edit adds: nobody could be
		// asked. Falling through to the refused side keeps an unrecognised answer on
		// the side that authorises nothing and still tells the caller about it.
		c.refused += len(plans)
		return nil
	}
}

// gatherPruneConsent asks for each batch separately and returns what may be
// removed (R6.1, R4.3).
//
// The two batches are two decisions and are asked as two, in the order the plan
// printed them. Approving "delete 60 packages ::gentoo already ships" says
// nothing about "and also throw away the patch we carry in nano and never wrote
// down"; one prompt covering both would collect a yes for the first and spend it
// on the second.
//
// Eligible is the authorisation and the bucket is not — the same rule
// ExecutePrune follows — so every bucket is filtered through eligiblePrunePlans
// rather than trusted wholesale.
func gatherPruneConsent(batch overlay.PruneBatch) pruneConsent {
	var consent pruneConsent

	if identical := eligiblePrunePlans(batch.Identical); len(identical) > 0 {
		consent.authorised.Identical = consent.record(confirmIdenticalPrune(len(identical)), identical)
	}

	if diverging := eligiblePrunePlans(batch.Diverging); len(diverging) > 0 {
		consent.authorised.Diverging = consent.record(confirmDivergingPrune(diverging), diverging)
	}

	// The refused bucket is never asked about and never authorised: no flag on this
	// command reaches a package the verdict gate refused (R2.5). A plan filed there
	// still carrying Eligible is a planner bug, not a decision this run may act on —
	// but the plan above already printed it as "would be removed", so it is named
	// here rather than silently dropped, and counted as refused so the run exits
	// non-zero instead of reporting a clean sweep it did not perform.
	if stray := eligiblePrunePlans(batch.Refused); len(stray) > 0 {
		consent.refused += len(stray)
		for _, plan := range stray {
			output.Error.Printf("  %s/%s was planned as removable but is in the refused group; no confirmation covers that group, so it was left alone (this is a bug in the prune planner).\n",
				plan.Category, plan.Package)
		}
	}

	return consent
}

// eligiblePrunePlans keeps the plans this run may act on.
//
// It reads Eligible and never the bucket, matching pruneEligibleCount and
// ExecutePrune. A confirmation collected over a list built from the bucket would
// be consent for a different set of packages than the executor acts on.
func eligiblePrunePlans(plans []overlay.PrunePlan) []overlay.PrunePlan {
	kept := make([]overlay.PrunePlan, 0, len(plans))
	for _, plan := range plans {
		if plan.Eligible {
			kept = append(kept, plan)
		}
	}
	return kept
}

// confirmIdenticalPrune takes the ONE confirmation covering the identical batch
// (R6.1), through three gates ordered by how much they trust the caller: --yes
// proceeds unattended because the operator asked for that in so many words; a
// terminal is asked; anything else is refused and the run exits non-zero (R6.2).
//
// --yes is allowed to answer for a human HERE, and only here, because this batch
// loses nothing: every byte of every one of these packages is already in
// ::gentoo, so the worst case is a re-sync. See confirmDivergingPrune for the
// batch where that is not true.
//
// The prompt is a count rather than a list. The plan above printed every package
// with its files and its registry entries, in full and without truncation, and
// repeating it into a y/N line would push the question itself off the screen.
func confirmIdenticalPrune(n int) pruneConsentAnswer {
	if pruneYes {
		output.Warning.Printf("  --yes given: removing %d package(s) ::gentoo already provides, without a prompt.\n", n)
		return pruneConsentGiven
	}
	if !pruneInteractiveFn() {
		// R6.2, all three clauses: the plan is already on screen because it is
		// printed before this runs, nothing is removed, and the caller returns
		// non-zero. A prompt written to a terminal nobody is watching does not become
		// consent by going unanswered, so none is written.
		output.Warning.Printf("  Not an interactive terminal and --yes was not given: %d planned removal(s) were NOT carried out.\n", n)
		output.Info.Println("  Re-run with --yes to carry them out unattended.")
		return pruneConsentUnavailable
	}

	fmt.Println()
	output.Warning.Println("  These package directories are deleted from the overlay, which auto-commits and publishes within minutes.")
	if !confirmPruneFn(fmt.Sprintf("Remove %d package(s) ::gentoo already provides?", n)) {
		return pruneConsentDeclined
	}
	return pruneConsentGiven
}

// confirmDivergingPrune takes the SECOND confirmation, the one that covers
// discarding local work (R4.3). It is deliberately not the same shape as the
// first.
//
// # Why --yes cannot answer this one (R4.4)
//
// --yes exists so that a scripted run can proceed without a human. Discarding
// local work is precisely the thing a scripted run must not decide on its own:
// the flag would be standing in for a judgement about work that exists nowhere
// else and that nobody has looked at. So a session with no terminal is refused
// here outright, with or without --yes, while the identical batch — which loses
// nothing — still goes. That split is the whole of the asymmetry: a run that
// refused everything would be the right answer to a different question.
//
// # Why the prompt is a list and not a count
//
// R4.2. "Discard 8 packages?" and "discard the wayland patch in kwin?" are the
// same sentence to an operator who cannot see the list, and only one of them can
// be answered.
func confirmDivergingPrune(plans []overlay.PrunePlan) pruneConsentAnswer {
	if !pruneInteractiveFn() {
		output.Warning.Printf("  Not an interactive terminal: %d diverging package(s) were NOT removed.\n", len(plans))
		output.Info.Println("  --yes does not answer this one. It exists so a scripted run can proceed unattended, and discarding local work is not a decision a script may take on its own: somebody has to read what is being thrown away.")
		output.Info.Println("  Re-run '--apply --include-patched' from a terminal to answer for these.")
		return pruneConsentUnavailable
	}

	fmt.Println()
	output.Warning.Println("  The packages below carry work that is not in ::gentoo. Removing them destroys it — there is no copy upstream to restore it from.")
	if !confirmPruneFn(divergingPrunePrompt(plans)) {
		return pruneConsentDeclined
	}
	return pruneConsentGiven
}

// divergingPrunePrompt writes the second confirmation's text: every package, the
// reason it is in this batch, and what removing it takes (R4.2, R4.3).
//
// The reason is plan.Reason verbatim, whichever kind it is. A package whose
// registry entry DECLARES `patched` never reaches this batch in practice — the
// declaration makes deriveVerdict return keep or needs-rebase (compare.go:193),
// and PlanPrune refuses at the verdict gate before any content is compared
// (R2.5) — so what lands here is an undeclared divergence and the reason is what
// the byte comparison found ("version 1.0 differs"). Reading the one field
// covers both cases, so the prompt neither assumes a declared reason is present
// nor goes stale if the pipeline ever lets a declared package through.
//
// The "size of the difference" this can honestly state is what the plan knows:
// the reason names WHERE the difference is — a version, or a path under files/ —
// and the inventory says what goes with it. No byte count exists anywhere in the
// plan, and producing one here would mean re-reading both trees after the
// operator has already been shown the report.
func divergingPrunePrompt(plans []overlay.PrunePlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Discarding local work in %d package(s):\n", len(plans))
	for _, plan := range plans {
		fmt.Fprintf(&b, "    %s/%s — %s\n", plan.Category, plan.Package, plan.Reason)
		if len(plan.Versions) > 0 {
			fmt.Fprintf(&b, "      loses %d version(s) (%s) and %d file(s)\n",
				len(plan.Versions), strings.Join(plan.Versions, ", "), len(plan.Files))
			continue
		}
		fmt.Fprintf(&b, "      loses %d file(s)\n", len(plan.Files))
	}
	fmt.Fprintf(&b, "  Discard that work and remove these %d package(s)?", len(plans))
	return b.String()
}

// reportPruneOutcome prints what actually happened, per package, and returns how
// many removals went through and how many FAILED — the second is the number D7
// turns into an exit code.
//
// Every result is printed and the loop never stops early (R5.5). The batch was
// approved as a whole, so abandoning the report at the first failure would leave
// the operator guessing which side of it each package fell on.
func reportPruneOutcome(results []overlay.PruneResult, consent pruneConsent) (removed, failed int) {
	if len(results) == 0 {
		reportPruneNothingApplied(consent)
		return 0, 0
	}

	fmt.Println()
	output.Header.Println("Prune Result")
	fmt.Println()

	for _, res := range results {
		atom := res.Plan.Category + "/" + res.Plan.Package
		if prunePackageWentAway(res) {
			removed++
			output.Success.Printf("  %-45s removed (%d file(s))\n", atom, len(res.Plan.Files))
			continue
		}
		// The package is named here and not left to the error text: PruneResult.Err
		// says what went wrong ("permission denied") and nothing about which package
		// it happened to, and a reason with no subject cannot be acted on.
		failed++
		output.Error.Printf("  %-45s NOT removed: %v\n", atom, pruneFailureReason(res))
	}

	fmt.Println()
	if removed > 0 {
		output.Success.Printf("  Removed %d package director(ies) from the overlay.\n", removed)
	}
	if failed > 0 {
		output.Warning.Printf("  %d removal(s) failed and are listed above; those packages are still in the overlay, registry entries and all.\n", failed)
	}
	reportPruneConsentGaps(consent)
	return removed, failed
}

// prunePackageWentAway reports whether the executor says this package's
// directory is gone.
//
// BOTH fields are read, not just Removed. PruneResult documents Err as non-nil
// exactly when Removed is false, and this is the one place where believing a
// result that breaks that contract costs something: prunedRegistryAtoms deletes
// the registry entries of every atom this returns true for. A contradictory
// result therefore keeps its entries and is reported as a failure — an entry
// left behind is loud on the next --check, while an entry deleted under a
// package that is still there stops that package being updated in silence.
func prunePackageWentAway(res overlay.PruneResult) bool {
	return res.Removed && res.Err == nil
}

// pruneFailureReason is what to print beside a package that did not go. It
// covers the shape PruneResult's contract forbids — no removal and no reason —
// rather than printing "<nil>" at the operator.
func pruneFailureReason(res overlay.PruneResult) error {
	if res.Err != nil {
		return res.Err
	}
	return errors.New("the executor reported neither a removal nor a reason (this is a bug in the prune executor)")
}

// reportPruneConsentGaps says what the run did NOT do, in the two different
// senses that has. A decline is the command working: the operator was asked and
// answered. A refusal is the command unable to ask, which the exit code carries
// — the operator asked for a removal that did not happen, and a caller reading 0
// would record it as done.
func reportPruneConsentGaps(consent pruneConsent) {
	if consent.declined > 0 {
		output.Info.Printf("  %d planned removal(s) were declined at the prompt and left exactly as they were.\n", consent.declined)
	}
	if consent.refused > 0 {
		output.Warning.Printf("  %d planned removal(s) had no confirmation and were not carried out — see the reason above. This run exits non-zero because of them.\n", consent.refused)
	}
}

// reportPruneNothingApplied closes a run that reached --apply and handed the
// executor nothing, naming which of the two reasons applies. There is no result
// section to print, and a run that ended on an unanswered prompt would read as
// one that crashed.
func reportPruneNothingApplied(consent pruneConsent) {
	fmt.Println()
	switch {
	case consent.refused > 0:
		output.Warning.Printf("  Nothing was removed: no confirmation covered the %d planned removal(s) — see the reason above.\n",
			consent.refused+consent.declined)
	case consent.declined > 0:
		output.Info.Println("  Nothing was removed: the confirmation was declined. The overlay and .autoupdate/packages.toml are exactly as they were.")
	default:
		output.Info.Println("  Nothing was removed.")
	}
}

// prunedRegistryAtoms returns the atoms whose registry records may now be
// deleted: the packages whose DIRECTORY actually went (design D7), and that
// carry at least one entry.
//
// The registry-key filter is not an optimisation. The plan told the operator "no
// entry tracks this package" for the others, and handing those over anyway would
// make a run that promised to touch nothing read, re-parse and possibly refuse
// the whole file. The keys were read from the same packages.toml this edit
// rewrites, once at the top of the run, so "carries no key" and "has no record
// in that file" are the same statement.
//
// The atom is rebuilt from the plan's own Category and Package rather than from
// a registry key: RemovePackagesFromConfig matches by atom, so any spelling of a
// key would do, and the two names are the ones the operator was shown.
func prunedRegistryAtoms(results []overlay.PruneResult) []string {
	atoms := make([]string, 0, len(results))
	for _, res := range results {
		if !prunePackageWentAway(res) || len(res.Plan.RegistryKeys) == 0 {
			continue
		}
		atoms = append(atoms, res.Plan.Category+"/"+res.Plan.Package)
	}
	return atoms
}

// removePruneRegistryEntries deletes the registry records of the atoms whose
// directory went, in ONE edit after all the removals (design D7), and says what
// it did.
//
// One call, not one per package: the file is read, verified and atomically
// rewritten each time, and a per-package loop would rewrite it once per removal
// — 60 rewrites of a file that auto-commits, where one is wanted.
//
// A failure here is reported and returned rather than swallowed. The directories
// are already gone at this point, so the operator is left with a registry that
// names packages the overlay no longer holds, and that is exactly the state the
// next --check turns into an unexplained disable.
func removePruneRegistryEntries(overlayPath string, atoms []string) error {
	if err := autoupdate.RemovePackagesFromConfig(overlayPath, atoms); err != nil {
		wrapped := fmt.Errorf("removing %d atom(s) from .autoupdate/packages.toml: %w", len(atoms), err)
		output.Error.Printf("  %v\n", wrapped)
		output.Warning.Println("  The package directories are gone and the registry still lists them; delete those entries by hand. An entry whose package directory no longer exists promises an endpoint for something that is not there, and the next --check disables it without saying why.")
		return wrapped
	}

	output.Success.Printf("  Deleted the .autoupdate/packages.toml entries of %d removed package(s).\n", len(atoms))
	return nil
}

// selectPrunePackages narrows the scan to the operator's target: a bare category
// ("app-editors") or one package ("app-editors/zed"). An empty target selects
// everything.
//
// A target matching nothing is an ERROR rather than an empty selection (R1.3).
// The two are indistinguishable downstream — both produce an empty plan — and an
// empty plan says "there is nothing to remove", which is a claim about the
// overlay this run is in no position to make about a category that does not
// exist.
//
// Matching is against the SCAN, not the filesystem: the scan is what the plan
// would be built from, so "matches nothing" here means exactly "restricts the
// plan to nothing". A category directory holding no package therefore fails too,
// which is the honest answer to "prune app-editors" when there is no such
// package to prune.
func selectPrunePackages(packages []overlay.PackageInfo, target string) ([]overlay.PackageInfo, error) {
	if target == "" {
		return packages, nil
	}

	// Cut, not Split: "a/b/c" leaves name "b/c", which matches no scanned package
	// name and is refused by the emptiness test below. Nothing here is ever joined
	// into a filesystem path — the paths the plan uses come from the scan.
	category, name, named := strings.Cut(target, "/")

	selected := make([]overlay.PackageInfo, 0, len(packages))
	for _, p := range packages {
		if p.Category != category {
			continue
		}
		if named && p.Package != name {
			continue
		}
		selected = append(selected, p)
	}

	if len(selected) == 0 {
		return nil, fmt.Errorf("%q matches no package in the overlay", target)
	}
	return selected, nil
}

// buildPruneRegistryKeys groups the registry by atom: "category/package" -> every
// key that tracks it, which is what PruneOptions.RegistryKeys wants and what
// R1.4 asks the plan to print.
//
// EVERY key, never just the first. 90 of the registry's 321 atoms carry more
// than one entry — one per slot ("net-libs/webkit-gtk:4.1") or per release
// channel ("media-libs/gstreamer@stable") — and a plan showing one of them
// understates what --apply would do to a file that publishes itself minutes
// later. Keys are split with autoupdate.SplitPackageKey rather than by hand, for
// the reason buildDivergenceMap states: a second, slot-blind copy of the split is
// exactly the bug the suffixes invite.
//
// Keys are visited in sorted order so an atom's list reads the same on every
// run, and a key the split rejects is RETURNED to the caller rather than logged
// here — one bad key must not blank the whole map, and the caller owns what the
// operator is told.
//
// Nothing here is sanitised, and nothing needs to be: a key is used as a map key
// and as text to print, never to build a path. Keep it that way.
func buildPruneRegistryKeys(overlayPath string) (byAtom map[string][]string, malformed []string, err error) {
	cfg, err := autoupdate.LoadPackagesConfig(overlayPath)
	if err != nil {
		return nil, nil, err
	}

	keys := make([]string, 0, len(cfg.Packages))
	for key := range cfg.Packages {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	byAtom = make(map[string][]string, len(keys))
	for _, key := range keys {
		category, name, ok := autoupdate.SplitPackageKey(key)
		if !ok {
			malformed = append(malformed, key)
			continue
		}
		// Plain concatenation rather than path.Join, matching buildDivergenceMap:
		// this is a map key, and Join would normalise "a/../b" into something the
		// scanner never produces — a difference that could only ever hide a mismatch.
		atom := category + "/" + name
		byAtom[atom] = append(byAtom[atom], key)
	}
	return byAtom, malformed, nil
}

// firstPruneError returns the first failure worth reporting, so that one cause
// read through two functions is told to the operator once.
func firstPruneError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// pruneEligibleCount counts the plans this run may act on.
//
// It reads Eligible and never the bucket a plan was filed under. Eligible is the
// authorisation — the class only says what the evidence found — and a count that
// inferred one from the other would disagree with ExecutePrune, which tests the
// same field before every removal.
func pruneEligibleCount(batch overlay.PruneBatch) int {
	n := 0
	for _, plans := range [][]overlay.PrunePlan{batch.Identical, batch.Diverging, batch.Refused} {
		for _, plan := range plans {
			if plan.Eligible {
				n++
			}
		}
	}
	return n
}

// displayPrunePlan prints the whole batch, in full, before anything is removed
// (R1.4).
//
// The full list, never a sample: the operator approves one batch, and a
// truncated list hides exactly the line that is wrong — the sweep's own argument
// for the same decision. The refused group comes LAST because it is the largest
// by far (240 of the overlay's 314 packages are not redundant) and the least
// actionable; what the run would actually do is at the top, where the operator
// starts reading.
func displayPrunePlan(batch overlay.PruneBatch, keepRegistry bool) {
	printPrunePlanGroup("Identical to ::gentoo — our copy holds nothing of ours", batch.Identical, keepRegistry)
	printPrunePlanGroup("Diverging — something of ours is in these", batch.Diverging, keepRegistry)
	printPruneRefusals(batch.Refused)
}

// printPrunePlanGroup prints one bucket with the inventory of each package in
// it: what would go, and what this run may do about it.
func printPrunePlanGroup(title string, plans []overlay.PrunePlan, keepRegistry bool) {
	if len(plans) == 0 {
		return
	}

	fmt.Printf("  %s (%d):\n", title, len(plans))
	for _, plan := range plans {
		printPrunePlanEntry(plan, keepRegistry)
	}
	fmt.Println()
}

// printPrunePlanEntry prints one package: the verdict this run reached about it,
// then everything removing it would take.
//
// Files are printed as the plan carries them — RELATIVE to the package directory
// named on the heading line — and are never joined back into a path. They are a
// report of what a removal costs; the path a removal uses is built and proven by
// the executor.
func printPrunePlanEntry(plan overlay.PrunePlan, keepRegistry bool) {
	atom := plan.Category + "/" + plan.Package

	if plan.Eligible {
		fmt.Printf("    %s — would be removed\n", atom)
	} else {
		// Printed with its inventory anyway: the operator's next question about a
		// package the run will not touch is "what would I get if I let it", and the
		// answer is exactly this list.
		output.Info.Printf("    %s — kept on this run\n", atom)
	}

	if plan.Reason != "" {
		output.Info.Printf("      why:      %s\n", plan.Reason)
	}
	if len(plan.Versions) > 0 {
		fmt.Printf("      versions: %s\n", strings.Join(plan.Versions, ", "))
	}
	if len(plan.Files) > 0 {
		fmt.Printf("      files (%d):\n", len(plan.Files))
		for _, file := range plan.Files {
			fmt.Printf("        %s\n", file)
		}
	}

	switch {
	case len(plan.RegistryKeys) == 0:
		output.Info.Println("      registry: no entry tracks this package")
	case keepRegistry:
		// --keep-registry makes "these entries would be deleted" false, so the plan
		// says the other thing instead. A plan that describes a run other than the
		// one requested is worse than no plan.
		output.Info.Printf("      registry: %d entr(ies) kept by --keep-registry: %s\n",
			len(plan.RegistryKeys), strings.Join(plan.RegistryKeys, ", "))
	default:
		fmt.Printf("      registry entries (%d):\n", len(plan.RegistryKeys))
		for _, key := range plan.RegistryKeys {
			fmt.Printf("        %s\n", key)
		}
	}
}

// printPruneRefusals lists the packages no flag on this command can remove, each
// with the reason it is here.
//
// One line per package, with no inventory: a package refused by its verdict was
// never even listed (R2.5 stops the planner before the directory is read), and
// the reason is the whole of what the operator came for. Omitting the group
// entirely would read as "these packages do not exist".
func printPruneRefusals(plans []overlay.PrunePlan) {
	if len(plans) == 0 {
		return
	}

	fmt.Printf("  Refused — no flag on this command removes these (%d):\n", len(plans))
	for _, plan := range plans {
		fmt.Printf("    %-45s %s\n", plan.Category+"/"+plan.Package, plan.Reason)
	}
	fmt.Println()
}

// reportPruneNothingQualified says that packages WERE examined and none of them
// may be removed (R1.5).
//
// This is the "you are done" half of R1.5's distinction: the overlay is doing
// its job and there is nothing to act on. It must not be reachable from a run
// where no comparison happened — see reportPruneAPIOnly for that half, which
// sends the operator somewhere else entirely.
func reportPruneNothingQualified(examined, diverging int, includePatched bool) {
	output.Success.Printf("  Nothing qualified for removal: %d package(s) were examined and none of them may be removed.\n", examined)
	if diverging > 0 && !includePatched {
		output.Info.Printf("  %d of them diverge from ::gentoo; --include-patched would consider them, discarding that work.\n", diverging)
	}
}

// reportPruneAPIOnly says that NOTHING WAS EXAMINED, and why (R2.6, R1.5, D6).
//
// The wording is the point. "Nothing qualified" here would be a lie in the exact
// direction that costs the operator their afternoon: they would read "the
// overlay is clean" from a run that never compared a single package. This one
// says the opposite — the overlay might be full of removable packages and this
// run could not tell — and then names the way out, because "refused" without an
// instruction is a dead end to guess at.
func reportPruneAPIOnly(providerName string, inScope int) {
	output.Warning.Printf("  Nothing was examined: the %s provider exposes no local ::gentoo tree, so none of the %d package(s) in scope could be compared.\n",
		providerName, inScope)
	output.Warning.Printf("  Every package is refused: %s.\n", pruneNoLocalTreeRefusal)
	output.Info.Println("  Content is the only evidence that may authorise a removal, and an API can supply it only at one rate-limited request per package.")
	output.Info.Println("  Point bentoo at an on-disk ::gentoo in ~/.config/bentoo/config.yaml:")
	output.Info.Println("    repositories:")
	output.Info.Println("      gentoo:")
	output.Info.Println("        provider: local")
	output.Info.Println("        path: /var/db/repos/gentoo")
}

// reportPruneRegistryUnreadable says that nothing was examined because the
// overlay's own registry could not be read (R1.5).
//
// It belongs to the "nothing was EXAMINED" family and pointedly not to "nothing
// qualified", and that difference is the whole of why the function exists.
// Without the registry no package has a divergence state, and a package with no
// divergence state gets no verdict — so no comparison here ever reaches a
// conclusion about anything. "Nothing qualified" would report a fact about the
// overlay that this run is in no position to know, and the operator would read
// it as "clean" and stop looking.
//
// It exits 0, like the API-only gate: the command ran correctly and refused.
// R5.5 ties a non-zero exit to a removal that FAILED, and none was attempted.
func reportPruneRegistryUnreadable(err error, inScope int) {
	output.Warning.Printf("  Nothing was examined: the autoupdate registry could not be read, so none of the %d package(s) in scope has a known divergence state.\n",
		inScope)
	output.Warning.Printf("  Every package is refused: %v.\n", err)
	output.Info.Println("  The registry is where 'patched' is declared, and a package whose declaration cannot be read must not be removed on the assumption that it has none.")
	output.Info.Println("  Repair or restore .autoupdate/packages.toml in the overlay, then re-run.")
}

// reportPruneNothingExamined states the other way a run can examine nothing: it
// had nothing to look at. It takes the whole line because the situations that
// reach it differ in what is worth saying, and a shared sentence with a hole in
// it would say less than either.
func reportPruneNothingExamined(line string) {
	output.Info.Println(line)
}
