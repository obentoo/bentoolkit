package main

import (
	"context"
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
var (
	prunePlannerFn  = overlay.PlanPrune
	pruneExecutorFn = overlay.ExecutePrune
	confirmPruneFn  = confirmAction
)

var (
	// pruneApply is --apply: without it the command plans and prints, and the
	// executor is unreachable (R1.1).
	pruneApply bool
	// pruneIncludePatched is --include-patched: the operator saying out loud that
	// they accept discarding local work (R4.1). It never moves a package between
	// buckets — it only decides whether the diverging bucket may be acted on.
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

Packages that carry something of ours — a declared 'patched' entry, or an
undeclared difference the byte comparison found — are printed under their own
heading and left alone unless --include-patched says otherwise. Packages no run
may remove are printed last, each with its reason.

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
	pruneCmd.Flags().BoolVar(&pruneIncludePatched, "include-patched", false, "Also remove packages that diverge from ::gentoo, discarding that work")
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
	// delete, and the printer only reads. The confirmations, pruneExecutorFn and
	// the packages.toml edit are wired in below, behind --apply.
	if !pruneApply {
		output.Info.Printf("  Nothing was removed: this is a plan. Re-run with --apply to carry out %d removal(s).\n", eligible)
		return
	}

	output.Warning.Printf("  --apply is not wired up in this build: %d planned removal(s) were NOT carried out.\n", eligible)
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
