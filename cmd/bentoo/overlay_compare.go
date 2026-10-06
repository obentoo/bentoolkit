package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/gentoo/repo"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

var (
	compareClone        bool
	compareCacheDir     string
	compareNoCache      bool
	compareTimeout      int
	compareToken        string
	compareOnlyOutdated bool
	// compareOnlyRedundant and compareOnlyPatched narrow the REPORT, not the
	// comparison: they are applied to the finished results by
	// filterCompareResults, unlike compareOnlyOutdated which selects on
	// Status inside CompareOptions.
	compareOnlyRedundant bool
	compareOnlyPatched   bool
	compareSync          bool
	// compareConcurrency bounds parallel upstream comparisons (range [1,100])
	compareConcurrency int
	// compareNoReview turns the model off. It is the ONLY flag here that
	// suppresses work rather than narrowing a view, and it suppresses the only
	// work that leaves this machine to something other than a package registry.
	compareNoReview bool
	// compareRealign turns the description into a JUDGEMENT: every package is
	// measured against the ::gentoo ebuild it should be compared with, and what
	// the two carry differently is reported by axis, by declaration and by class.
	//
	// It DEFAULTS TO FALSE and everything it adds is gated on it, because
	// `overlay compare` is shipped and in daily use: without this flag the
	// rendered output and the exit code are the ones the command produced
	// yesterday. The gate is mechanical rather than careful — the renderer
	// never learns which flags were passed, so every field the review writes
	// renders nothing at its zero value and the passes that fill them are called
	// only from behind this flag (overlay_compare_realign.go).
	compareRealign bool
	// compareDepth is the rung of the build ladder each proposed realignment is
	// PROVED at, and it is the switch that turns proving on at all. Empty — the
	// default, and every invocation that does not name it — is report-only:
	// `--realign` alone stays report-only, with no staging, no plan, no prompt
	// and no build.
	compareDepth string
	// compareYes proves the plan unattended, and it exists here for the reason it
	// exists on the sweep: the refusal on a non-interactive terminal has to be able
	// to NAME the way forward, and "pass --yes" is not a thing it can say about a
	// flag the command does not have. It buys past the PROMPT and never past the
	// PLAN — the plan is printed either way, because the point of the plan is
	// that the operator sees the cost.
	compareYes bool
)

// newCompareCmd builds `overlay compare`.
func newCompareCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "compare [repository]",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Compare overlay packages with upstream repository",
		Long: `Compare package versions in your local Bentoo overlay against
an upstream repository.

Any repository from the Gentoo ecosystem (~428 repos) can be used by name.
The repository list is fetched from the official Gentoo repositories.xml
registry and cached locally. Use --sync to force a refresh.

Custom repositories can also be defined in ~/.config/bentoo/config.yaml
and take priority over registry entries.

The provider (GitHub API, GitLab API, or Git) is automatically detected
based on the repository's source URL. Use --clone to force git clone.

By default, all packages are shown (outdated, up-to-date, and newer).
Use --only-outdated to filter and show only packages that need updates.
Use --only-redundant to see just the removal candidates, or --only-patched
to see just the packages an .autoupdate/packages.toml entry declares a
divergence for. The three filters combine by intersection.

Where a package differs from upstream and nothing declares why, a local model
(the claude CLI, on your own subscription) reads both ebuilds and says what the
difference does and which side it came from. It is commentary: the verdicts and
the removal recommendations are the same with or without it, nothing it says is
written to a file, and --no-review contacts no model at all.

Use --realign to measure every package against the ::gentoo ebuild it should be
compared with: which baseline was used and how far it is, which structural axes
differ, what the ebuild declares about them, and what a model makes of what is
left. It needs ::gentoo's tree on this machine, so it is refused against any
other repository and against an API-only provider. Without it the command behaves
exactly as it always has.

Examples:
  bentoo overlay compare                    # Compare with gentoo (API)
  bentoo overlay compare guru               # Compare with GURU (API)
  bentoo overlay compare some-overlay       # Compare with any registered repo
  bentoo overlay compare --clone            # Compare with gentoo (git clone)
  bentoo overlay compare --sync             # Refresh repo list before comparing
  bentoo overlay compare --only-outdated    # Show only outdated packages
  bentoo overlay compare --only-redundant   # Show only removal candidates
  bentoo overlay compare --only-patched     # Show only declared divergences
  bentoo overlay compare --no-review        # Contact no model
  bentoo overlay compare --realign          # Review against the ::gentoo baseline`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCompare(cmd, args, d)
		},
	}
	cmd.Flags().BoolVar(&compareClone, "clone", false, "Use git clone instead of API")
	cmd.Flags().StringVar(&compareCacheDir, "cache-dir", "", "Directory to cache data")
	cmd.Flags().BoolVar(&compareNoCache, "no-cache", false, "Disable caching")
	cmd.Flags().IntVar(&compareTimeout, "timeout", 30, "HTTP request timeout in seconds")
	cmd.Flags().StringVar(&compareToken, "token", "", "Auth token for API provider")
	cmd.Flags().BoolVar(&compareOnlyOutdated, "only-outdated", false, "Show only outdated packages (Bentoo < Gentoo)")
	cmd.Flags().BoolVar(&compareOnlyRedundant, "only-redundant", false, "Show only redundant packages (removal candidates)")
	cmd.Flags().BoolVar(&compareOnlyPatched, "only-patched", false, "Show only packages a registry entry declares a divergence for")
	cmd.Flags().BoolVar(&compareSync, "sync", false, "Force refresh of repository list")
	cmd.Flags().IntVar(&compareConcurrency, "concurrency", overlay.DefaultCompareConcurrency, "max parallel checks (1-100)")
	cmd.Flags().BoolVar(&compareNoReview, "no-review", false, "Contact no model; print the report without commentary")
	cmd.Flags().BoolVar(&compareRealign, "realign", false, "Review each package against its ::gentoo baseline (needs a local gentoo tree)")
	// The default is the EMPTY string and not a rung's name, unlike `overlay
	// validate --depth`, because the two defaults mean opposite things: there the
	// shallowest useful rung IS the shipped behaviour, here any rung at all is a
	// build nobody asked for. Absent means report-only, which is what `--realign`
	// shipped as.
	cmd.Flags().StringVar(&compareDepth, "depth", "",
		"Prove each proposed realignment by building it to this rung of the ladder — patches, configure, compile or install, each including every rung before it. "+
			"Needs --realign, builds in a staged tree outside the overlay, and asks once before the first build. Absent means report only, and nothing is built")
	cmd.Flags().BoolVar(&compareYes, "yes", false, "Prove the whole plan without the prompt. Only --depth builds anything; publishing stays a separate per-package question, asked only in an interactive terminal and never answered by this flag")
	return cmd
}

func runCompare(cmd *cobra.Command, args []string, d *deps) error {
	log := logging.FromContext(commandContext(cmd))

	// Validate --concurrency BEFORE any package work so a bad value fails fast
	// with a clear message and a non-zero exit.
	if compareConcurrency < 1 || compareConcurrency > 100 {
		log.Error("--concurrency must be in range [1, 100]", "concurrency", compareConcurrency)
		return exitWith(1)
	}

	// Refuse a --depth this invocation has nothing to prove with, in the same
	// position and for the same reason as the check above: it depends on neither
	// the config, the repository nor the overlay, so answering it first costs
	// nothing and reaches no work.
	//
	// It exits 1 like every other usage error here. That is NOT the review's own
	// non-zero condition — that one is kept for "no baseline tree at all" — and the
	// two cannot be confused, because this one returns before a single package has
	// been looked at and prints no report to attach a verdict to.
	if err := compareDepthPreflight(compareDepth, compareRealign); err != nil {
		log.Error("refusing --depth", "err", err)
		return exitWith(1)
	}

	// The process-wide context (func commandContext): overlay compare is
	// annotated cancellable, so the first SIGINT, SIGTERM or SIGHUP cancels it.
	// CompareWithProvider threads it through every upstream lookup, and the
	// review passes and the realignment builds hand it to the children they
	// start.
	ctx := commandContext(cmd)

	appCtx, err := loadAppContext(cmd)
	if err != nil {
		log.Error("loading config: failed", "err", err)
		return exitWith(1)
	}

	overlayPath := appCtx.OverlayPath
	cfg := appCtx.Config

	// Determine repository name (default: gentoo)
	repoName := "gentoo"
	if len(args) > 0 {
		repoName = args[0]
	}

	// Convert config repos to provider.RepositoryInfo map
	configRepos := convertConfigRepos(log, cfg)

	// Create repository registry
	registry, err := provider.NewRepositoryRegistry()
	if err != nil {
		log.Error("Failed to initialize repository registry", "err", err)
		return exitWith(1)
	}

	if compareSync {
		if err := registry.Sync(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				log.Error(registryInterruptedMsg)
				return exitWith(1)
			}
			log.Error("Failed to sync repository list", "err", err)
			return exitWith(1)
		}
	}

	// Resolve repository info
	repoInfo, err := provider.ResolveRepository(ctx, repoName, configRepos, registry)
	if err != nil {
		// An interruption is not a missing repository, and returning here keeps
		// the hint below from downloading the registry a second time.
		if errors.Is(err, context.Canceled) {
			log.Error(registryInterruptedMsg)
			return exitWith(1)
		}
		log.Error("Repository not found.", "repository", repoName)
		configNames := provider.ListAvailableRepositories(ctx, configRepos, nil)
		registryNames := provider.ListAvailableRepositories(ctx, nil, registry)
		if len(configNames) > 0 {
			log.Info("Config repositories", "repositories", strings.Join(configNames, ", "))
		}
		if len(registryNames) > 0 {
			log.Info("Registry repositories: use `eselect repository list` to see all available")
		} else {
			log.Info("Registry unavailable. Use --sync to refresh or run `eselect repository list`")
		}
		return exitWith(1)
	}

	// Token precedence lives in resolveRepoToken. An unreadable secrets file
	// warns and degrades to anonymous access rather than aborting the comparison.
	resolvedToken, err := resolveRepoToken(compareToken, repoInfo.Token)
	if err != nil {
		log.Warn("resolving GitHub token: failed; continuing with unauthenticated GitHub API access", "err", err)
	}
	repoInfo.Token = resolvedToken

	// Create provider
	prov, err := provider.NewProvider(repoInfo, compareClone)
	if err != nil {
		log.Error("Failed to create provider", "err", err)
		return exitWith(1)
	}
	defer prov.Close() //nolint:errcheck // every provider Close is a no-op that returns nil; there is nothing to act on

	// Refuse what THIS INVOCATION cannot do, before it costs anything.
	//
	// It sits here, immediately after the provider exists and before the rate
	// limit is consulted, because that is the first moment both halves of the
	// question can be answered and the last one before the run starts spending:
	// the check below issues an API call, and the scan beneath it walks the whole
	// overlay. A refusal that arrived after either would have spent the run before
	// saying the request was impossible.
	//
	// The reason comes back as a VALUE and is logged here, through the
	// invocation's logger, so the check itself stays free of output and a test
	// can read the refusal as a returned error.
	if compareRealign {
		if err := realignPreflight(repoName, prov); err != nil {
			log.Error("refusing --realign", "err", err)
			return exitWith(1)
		}
	}

	// Set timeout for API providers
	if ghProv, ok := prov.(*provider.GitHubProvider); ok {
		ghProv.HTTPClient.Timeout = time.Duration(compareTimeout) * time.Second
		if compareNoCache {
			ghProv.CacheDir = ""
		}
	}

	// Check rate limit for GitHub provider - block if exhausted
	if ghProv, ok := prov.(*provider.GitHubProvider); ok {
		remaining, resetTime, err := ghProv.GetRateLimitInfo(ctx)
		if err == nil {
			switch {
			case remaining == 0:
				// The failure is a diagnostic; the ways out below it are the
				// command's guidance and print as bare lines (ui_notes.go), so
				// the indented YAML snippet can be pasted as it reads.
				resetAt := resetTime.Format("15:04:05")
				log.Error("GitHub API rate limit exceeded", "resets_at", resetAt)
				uiInfo("")
				uiInfo("Options:")
				uiInfo("  1. Use --clone to download the repository:")
				uiInfo(fmt.Sprintf("     bentoo overlay compare %s --clone", repoName))
				uiInfo("")
				uiInfo("  2. Configure a local repository path in ~/.config/bentoo/config.yaml:")
				uiInfo("     repositories:")
				uiInfo("       gentoo:")
				uiInfo("         provider: local")
				uiInfo("         path: /var/db/repos/gentoo")
				uiInfo("")
				uiInfo(fmt.Sprintf("  3. Wait until %s for rate limit reset", resetAt))
				return exitWith(1)
			case remaining < 10:
				log.Warn("GitHub API rate limit low",
					"remaining", remaining, "resets_at", resetTime.Format("15:04:05"))
				if !compareClone {
					uiInfo("Tip: Use --clone flag to avoid rate limits")
				}
			case verbose:
				log.Debug("GitHub API rate limit", "remaining", remaining)
			}
		}
	}

	// Scan local overlay
	log.Info("Scanning Bentoo overlay", "path", overlayPath)
	scanResult, err := repo.ScanOverlay(overlayPath)
	if err != nil {
		log.Error("scanning overlay: failed", "err", err)
		return exitWith(1)
	}

	if len(scanResult.Packages) == 0 {
		log.Warn("No packages found in overlay")
		return nil
	}

	uiInfo(fmt.Sprintf("Found %s packages in Bentoo overlay",
		output.Sprint(output.Info, fmt.Sprintf("%d", len(scanResult.Packages)))))

	// Report scan errors if any
	if len(scanResult.Errors) > 0 {
		log.Warn("Encountered errors during scan", "errors", len(scanResult.Errors))
		for _, e := range scanResult.Errors {
			log.Debug("scan error", "path", e.Path, "message", e.Message)
		}
	}

	// What the overlay declares about itself, resolved once before the comparison
	// starts so the per-package goroutines only ever read it. An unreadable
	// registry warns exactly once here and leaves divergence nil, which the
	// comparator reads as "nothing is known about any package": compare
	// has never depended on packages.toml and must not start now.
	divergence, err := buildDivergenceMap(log, overlayPath)
	if err != nil {
		log.Warn("reading the autoupdate registry: failed; every package's divergence state will be reported as unknown", "err", err)
	}

	// Compare with upstream
	log.Info("Comparing with upstream", "repository", repoInfo.Name, "provider", prov.GetName())

	opts := overlay.CompareOptions{
		// The invocation's logger, so the review, realignment and baseline
		// passes below warn where every other diagnostic of this run goes.
		Logger:        log,
		OnlyOutdated:  compareOnlyOutdated,
		IncludeSynced: !compareOnlyOutdated, // Include synced unless only-outdated is set
		// A REVIEW RUN AND ONLY A REVIEW RUN sees the packages ::gentoo does not
		// carry. They are the review's own subject — 84 of the overlay's 321
		// packages have no ::gentoo counterpart, which is a fact about the overlay
		// that only the baseline review can report.
		//
		// Switched on unconditionally it would be a regression rather than a
		// feature: those packages would gain rows they do not have today, and
		// the summary's "N of M have no row above" — Scanned minus what the
		// report lists, in `func compareSummarySection` in
		// internal/common/report — would move for every operator who never
		// asked for a review.
		IncludeNotInRemote: compareRealign,
		Concurrency:        compareConcurrency,
		Divergence:         divergence,
		OverlayPath:        overlayPath,
		ProgressCallback: func(done, total uint64) {
			percent := uint64(0)
			if total > 0 {
				percent = (done * 100) / total
			}
			fmt.Printf("\r  Checking: [%3d%%] %d/%d", percent, done, total)
		},
	}
	// A failure's text is recorded on the result and exported, and a transport
	// error can echo the request, so the token this run resolved is scrubbed
	// from it before anything reads it.
	if resolvedToken != "" {
		opts.Redact = []string{resolvedToken}
	}

	report, err := overlay.CompareWithProvider(ctx, scanResult.Packages, prov, opts)
	if err != nil {
		// Check if it's a rate limit error and suggest --clone
		if strings.Contains(err.Error(), "rate limit") && !compareClone {
			log.Error("GitHub API rate limit exceeded.")
			uiInfo("Try using --clone flag to download the repository instead:")
			uiInfo(fmt.Sprintf("  bentoo overlay compare %s --clone", repoName))
			return exitWith(1)
		}
		log.Error("comparing packages: failed", "err", err)
		return exitWith(1)
	}

	// Clear progress line
	fmt.Printf("\r%s\r", "                                                                  ")

	// What the overlay's own content proves about WHO wrote each difference. Two
	// symmetric ebuilds cannot say it, so this reads the files/ tree beside them
	// and annotates the finished report: an ebuild referencing a file ::gentoo
	// does not ship carries something upstream never had.
	//
	// It runs HERE, between the comparison and the filter, for two reasons. After
	// the comparison because it must not join the 10-way concurrency inside it
	// (nothing here is concurrent, and the report it annotates is already sorted),
	// and before the filter because annotation is part of producing the report,
	// not of presenting it — a --only-redundant run must not reach a different
	// conclusion about a package than a full one.
	//
	// It cannot fail: every way of not knowing is recorded as "unproved", which is
	// also what an API-only provider leaves on every package, so this costs
	// nothing and says nothing when the compared repository is not on disk.
	overlay.AnnotateAuthorship(report, prov, opts)

	// What our ebuild was MEASURED AGAINST, and what that measurement found: the
	// ::gentoo baseline and how far it is, the structural axes that differ, what
	// the ebuild declares about them, how much of the diff the three-way
	// reduction could attribute, and — for a package ::gentoo carries no version
	// of — which other repository does.
	//
	// IT RUNS ONLY FOR A REVIEW RUN, and that single condition is the whole of
	// the promise that a run without --realign is byte-identical to before. Every
	// field it writes renders nothing at its zero value, so a run that never
	// reaches this line prints exactly what `overlay compare` printed yesterday —
	// the tables and every line of the summary block untouched, the sentence
	// counting what the report left out among them.
	//
	// Locating the tree comes FIRST because it is the one condition the command
	// exits non-zero for: a review with no ::gentoo repository to read
	// examined nothing, which is a different sentence from having looked and found
	// nothing, and the report has to say so instead of printing a coverage line
	// over a comparison that never happened.
	realignRan := compareRealign &&
		realignBaselineIsLocatable(log, report, realignBaselineTreeCandidate(repoInfo, prov))
	if realignRan {
		overlay.AnnotateBaseline(report, prov, opts)
		annotateOtherRepositories(report, realignLocalRepos(cfg))
	}

	// The budget every review invocation on this run is bounded by, read ONCE
	// from the operator's configuration and handed to both passes below.
	//
	// ONCE, because one read is what keeps them the same number. Two calls could
	// not disagree today, but two call sites are two places for a later edit to
	// move one and not the other, and a run whose divergence review and
	// realignment review died at different deadlines would give two reasons for
	// one configuration.
	//
	// Through the GETTER, never the raw key: unset, zero and negative all become
	// DefaultReviewTimeout there, and newClaudeAsker states what forwarding a raw
	// zero past it would cost.
	reviewBudget := cfg.Autoupdate.Review.GetTimeout()

	// What a MODEL makes of the differences the report cannot settle: where each
	// one came from, what it does, and — where it is ours — the `patched` text
	// that would declare it. It is commentary and nothing else: the grouping, the
	// Verdicts and the removal recommendations are the same whether it ran or not.
	//
	// It runs HERE, beside AnnotateAuthorship and on the SAME opts value: the same
	// opts lets it re-read the two files the comparison read (through the same
	// resolvePackagePaths), and running before the filter keeps annotation part of
	// PRODUCING the report, so a --only-redundant run cannot reach a different
	// conclusion about a package than a full one. It runs after authorship because
	// a proof from the overlay's own content outranks a guess, and the report
	// prints them in that order.
	//
	// A nil reviewer makes the whole pass a no-op, which is how `--no-review` and a
	// machine with no `claude` installed reach ONE path instead of two conditions
	// that could disagree. Nothing here can fail the run: every way of not getting
	// a reading costs one warning and the report is printed unchanged.
	overlay.AnnotateReviews(ctx, report, compareDivergenceReviewer(log, compareNoReview, reviewBudget, d), prov, opts)

	// A model's JUDGEMENT of what the baseline review found: is each undeclared
	// divergence still justified, and what would replace it if not.
	//
	// It runs after the two passes above and on the same opts for their reasons,
	// and it can no more fail the run than they can: a nil reviewer — `--no-review`,
	// or a machine with no `claude` on PATH — makes it a no-op, and
	// every other way of not getting an answer leaves an EMPTY verdict and is
	// counted, never invented. It returns nothing precisely so there is nothing to
	// exit on: an unreachable model is exit 0, because the deterministic half of
	// the report above is complete and useful without one.
	//
	// realignJudged records whether a model was reachable at all, which is the one
	// thing the report itself cannot say: with no reviewer nothing is asked, both
	// of its counters stay zero, and the silence would read as "every divergence
	// was judged and none objected".
	realignJudged := false
	if realignRan {
		reviewer := compareRealignReviewer(log, compareNoReview, reviewBudget, d)
		realignJudged = reviewer != nil
		overlay.AnnotateRealignVerdicts(ctx, report, reviewer, prov, opts)
	}

	// Whether each proposed realignment still BUILDS, proved the way a bump is:
	// staged outside the published overlay and taken up the build ladder to the
	// depth the operator named, behind one plan and one confirmation for the run.
	//
	// IT RUNS ONLY WHEN --depth WAS GIVEN, a second gate on top of --realign
	// rather than a redundant one: `--realign` alone is report-only by definition.
	//
	// It sits after the three annotation passes, because the candidate rule reads
	// the baseline they filled in and a proposal is only as good as the review
	// behind it; and before the view is narrowed, because the plan is about the
	// OVERLAY and not the rows the operator asked to see: a --only-redundant run
	// and a full one must not prove different sets, and the plan names every
	// atom, so nothing is proved that was not first shown.
	//
	// It cannot change the exit code. Declining is not a failure, a gate that says
	// no is an answer, and the one non-zero condition is decided below by
	// exitOnSkippedBaseline over a field nothing here writes.
	if realignRan && compareDepth != "" {
		proveRealignments(ctx, report, overlayPath, d)
	}

	// The report's FINDINGS, re-established now that every annotation pass has
	// written back onto it.
	//
	// The comparison establishes them once, at the end of CompareWithProvider,
	// from what was known then — the versions, the registry declaration and the
	// content check. The four passes above each write facts a finding built
	// before them could not have known: whether the overlay's own content PROVED
	// the difference is ours, what a model read it as doing, what it proposed be
	// declared. Un-refreshed, the list would report an undeclared divergence as
	// unproved after the files had proved it — the one reading that authorises
	// deleting work of our own.
	//
	// One call rather than a refresh inside each pass, because four refresh
	// points are four things to keep in step. The findings are a pure function of
	// report.Results, so this simply asks the question again. It runs BEFORE the
	// narrowing below for the reason the passes do: the findings describe the
	// OVERLAY, not the rows the operator asked to look at, just as no counter on
	// the report is narrowed.
	overlay.EstablishFindings(report)

	// Narrow the VIEW, never the computation. The comparison above already
	// produced the whole picture; only report.Results — the rows the table
	// prints — is narrowed here, and every counter on report keeps the value the
	// unfiltered run produced. That is what makes a filtered run and a full run
	// unable to disagree about a package, and it is why the summary below still
	// answers "what is in the overlay" rather than "what did you ask to see".
	//
	// --only-outdated is deliberately NOT a parameter: it selects on Status
	// inside CompareOptions above and has already removed its rows, so the
	// filters compose by intersection — each stage only ever removes.
	//
	// The WHOLE run's rows are KEPT before the narrowing replaces them. Every
	// counter survives the filter because it is a field; the two answers taken
	// by walking the rows do not, so `func buildCompareReport` in
	// overlay_compare_report.go takes them over this slice. Without it a
	// --only-patched run that removed a row whose reading failed would report a
	// smaller gap than the same run unfiltered.
	unfiltered := report.Results
	report.Results = filterCompareResults(report.Results, compareOnlyRedundant, compareOnlyPatched)

	// Present the comparison as the report every other command presents: the
	// terminal first, then the export. One tail serves the full run and the
	// empty one: the report states its own scope in its first section and its
	// counts in its last, so an empty run says it in the same words a full one
	// uses. It runs AFTER the progress region is down — the cleared line above —
	// because both write to stdout, and a report drawn into a live region is a
	// report the UI redraws over.
	//
	// The candidate declarations a review proposes are PRINTED, deliberately not
	// carried as notes: a candidate is a multi-line block a maintainer COPIES back
	// into an ebuild or a registry, and the renderer wraps a note to the device,
	// which would re-flow it into something that no longer pastes back as one
	// well-formed entry. It is called DIRECTLY rather than through `func
	// realignAddendum`, whose other half — the "no verdict was produced" notice —
	// crosses as a run note; the wrapper would print that fact twice. It follows
	// the VIEW: a proposal answers what is on screen, so a package
	// `--only-redundant` removed gets no paste block, unlike the counters, which
	// answer what the run did.
	if realignRan {
		fmt.Print(realignCandidateSection(report.Results))
	}

	// What no row can carry crosses in the PAYLOAD, not beside it. The run-level
	// sentences — no ::gentoo tree was reached, a review produced no verdict at
	// all, the classification share, the prune advice — are built here because
	// they are derived in part from flags the adapter never sees, and handed to
	// `func buildCompareReport` as CompareRun.Notes. Every finding beyond the
	// first a package has, which a one-line reason cell has no room for, travels
	// on that package's own entry. Both reach the terminal and every
	// export through one code path, which is what they did not do while they were
	// appended to the sections after the fact.
	presentCompareReport(log, d, cfg, buildCompareReport(report, repoInfo.Name, unfiltered,
		compareRunNotes(report, realignRan, realignJudged, compareNoReview)...))

	// The ONE non-zero condition: the review could not locate a
	// ::gentoo tree, so nothing was examined. It is LAST — after the render and
	// after the export — because the report is still worth printing and worth
	// exporting, `compare` did its job, and the status is returned only now.
	return exitOnSkippedBaseline(report)
}

// filterCompareResults narrows a report to the rows the operator asked for.
// Both flags false returns the input unchanged, order preserved; both true
// intersects. It is a pure function over the finished results rather than a
// condition inside CompareOptions, so the comparison always computes the whole
// picture and only the presentation narrows — a filtered run and a full run can
// therefore never disagree about a package.
//
// The two flags select on two different fields. --only-redundant reads the
// Verdict, which is derived: "should this package leave the overlay?".
// --only-patched reads Patched, which is declared: "did we say we changed
// something here?". A package can be patched under any verdict, so neither is
// a rename of the other. --only-outdated is absent by construction: it selects
// on Status inside CompareWithProvider, so this runs on what it left behind.
//
// A matching slice is BUILT rather than compacted in place: filtering with
// results[:0] would rewrite the caller's backing array. A filter that matches
// nothing is an answer, not a failure, and the caller says so in words.
func filterCompareResults(results []overlay.CompareResult, onlyRedundant, onlyPatched bool) []overlay.CompareResult {
	if !onlyRedundant && !onlyPatched {
		return results
	}

	filtered := make([]overlay.CompareResult, 0, len(results))
	for _, r := range results {
		if onlyRedundant && r.Verdict != overlay.VerdictRedundant {
			continue
		}
		if onlyPatched && !r.Patched {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}

// buildDivergenceMap turns the registry into the per-atom view compare needs:
// one entry per bare "category/package" atom, saying whether any registry entry
// for that atom declares a divergence from ::gentoo and which entry said so. It
// lives in cmd/ because this is the only package that imports both halves —
// internal/overlay must never learn what TOML is, and internal/autoupdate must
// never learn what a comparison is.
//
// An unreadable registry yields (nil, err); the caller warns and compares with
// every package unknown, because packages.toml must not become a hard
// dependency of compare. Absence from the map IS the unknown state.
//
// Keys are split with ebuilds.SplitPackageKey, never by hand, so ":slot" and
// "@label" keys land on their bare atom; a key it rejects is skipped with a
// warning, so one bad key cannot blank the map. Keys are visited sorted, so the
// entry recorded for an atom is stable across runs (":slot" beats "@label"
// because ':' sorts before '@'). Any patched entry marks the atom — failing
// toward "patched" can only suppress a removal recommendation, never produce
// one. The key is only a map key, never a filesystem path (verification uses
// the scanned directory names), and nothing here sanitises it: keep it so.
func buildDivergenceMap(log *slog.Logger, overlayPath string) (map[string]overlay.Divergence, error) {
	cfg, err := registry.LoadPackagesConfig(overlayPath)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(cfg.Packages))
	for key := range cfg.Packages {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	divs := make(map[string]overlay.Divergence, len(keys))
	for _, key := range keys {
		category, name, ok := ebuilds.SplitPackageKey(key)
		if !ok {
			log.Warn("registry key is not a category/package atom; skipping it", "key", key)
			continue
		}
		// Plain concatenation, not path.Join: this is a map key, not a path, and
		// Join would quietly normalise a key like "a/../b" into something the
		// scanner never produces — a difference that would only ever hide a
		// mismatch.
		atom := category + "/" + name

		// The zero value is "known to the registry, declares nothing", which is
		// what a silent entry must record — that is not the same as absent.
		div := divs[atom]
		// Trimmed because only a trimmed value distinguishes a stated reason
		// from a whitespace-only one — ValidatePackageConfig rejects the latter,
		// but LoadPackagesConfig never calls it, so this path is not protected
		// by it. The report caps this text when it prints it; what is printed
		// verbatim is the entry key, not the reason.
		if reason := strings.TrimSpace(cfg.Packages[key].Patched); reason != "" && !div.Patched {
			div.Patched = true
			div.Reason = reason
			div.Entry = key
		}
		divs[atom] = div
	}

	return divs, nil
}

func truncatePkgName(name string, maxLen int) string {
	if len(name) <= maxLen {
		return name + strings.Repeat(" ", maxLen-len(name))
	}
	return name[:maxLen-3] + "..."
}

// repoTokenName maps a repository name to the environment variable / secrets key
// that supplies its auth token: BENTOO_REPO_<NAME>_TOKEN, where <NAME> is the
// name upper-cased with every rune outside [A-Z0-9] replaced by '_'.
//
// The normalization is lossy, so distinct names can collide: "my-repo" and
// "my.repo" both map to BENTOO_REPO_MY_REPO_TOKEN. This is intentional and
// documented — an actual key clash is resolved by the secrets file's
// first-occurrence-wins rule, so the first matching entry supplies the token
// for every colliding name.
func repoTokenName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return "BENTOO_REPO_" + b.String() + "_TOKEN"
}

// resolveRepoToken applies the token precedence for a single repository:
//
//	--token flag (flagToken)
//	  > per-repo token (repoToken, resolved from BENTOO_REPO_<NAME>_TOKEN into
//	    RepositoryInfo.Token by convertConfigRepos)
//	  > global token (GITHUB_TOKEN/GH_TOKEN via env or the secrets file).
//
// config.yaml is no longer a token source. Previously the per-repo token beat
// everything, including an explicit --token: defensible while that token lived
// in the config file the user was editing, indefensible once it lives in a
// secrets file the flag cannot override.
//
// github.ResolveToken is consulted ONLY when both arguments are empty, so the
// two short-circuit paths do no file I/O. An absent token everywhere yields
// ("", nil) — anonymous access, not a failure. A present-but-unreadable secrets
// file yields ("", err) so the caller can warn; this function never logs, which
// keeps it pure with respect to its inputs and leaves the warning to the caller.
func resolveRepoToken(flagToken, repoToken string) (string, error) {
	if flagToken != "" {
		return flagToken, nil
	}
	if repoToken != "" {
		return repoToken, nil
	}
	return github.ResolveToken()
}

// registryInterruptedMsg is the one line an interrupted repository registry
// fetch reports: printed by runCompare, and the wrap text of
// resolveGentooProvider's error for prune and the revive flows.
const registryInterruptedMsg = "interrupted while fetching the repository registry"

// convertConfigRepos converts a config.RepoConfig map to a
// provider.RepositoryInfo map, resolving each repository's auth token from
// BENTOO_REPO_<NAME>_TOKEN via the secrets chain (env → user file → system file).
// config.yaml is no longer a token source. An unreadable secrets file warns and
// the token is treated as unset rather than aborting the whole conversion.
func convertConfigRepos(log *slog.Logger, cfg *config.Config) map[string]*provider.RepositoryInfo {
	if cfg.Repositories == nil {
		return nil
	}

	result := make(map[string]*provider.RepositoryInfo)
	for name, repo := range cfg.Repositories {
		tok, _, err := secrets.Lookup(repoTokenName(name))
		if err != nil {
			log.Warn("resolving token for repository: failed; treating it as unset", "repository", name, "err", err)
			tok = ""
		}
		result[name] = &provider.RepositoryInfo{
			Name:     name,
			Provider: repo.Provider,
			URL:      repo.URL,
			Path:     repo.Path,
			Token:    tok,
			Branch:   repo.Branch,
		}
	}
	return result
}
