package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/spf13/cobra"
)

// This file is `bentoo overlay validate`: the read-only gate that asks whether
// an ebuild still matches the source it points at, by reading the build options
// the upstream archive declares, reading the ones the ebuild passes, and
// subtracting. It builds nothing, needs no privilege, makes no network call and
// asks no model.
//
// # Where the operator-facing text goes
//
// In the default mode, on STDOUT through fmt and output/*, never through logger
// — the same rule overlay_prune.go states and for the same reason: logger binds
// os.Stderr once at first use, so splitting one report across two streams costs
// the reader the ordering between them the moment either is redirected. Here
// that matters more than usual, because a SKIPPED line and the reason beside it
// have to be read together.
//
// With --json that rule inverts, and it has to. STDOUT is then a single JSON
// document and nothing else may land in it, so every human-facing diagnostic
// goes to stderr instead — a warning printed into the document would break the
// `| jq` the flag exists for.

// validateRunnerFn is the seam the CLI tests drive, defaulting to the real
// runner. Same shape as overlay_prune.go's seams and for the reason that file
// states: a test has to be able to prove the runner was NOT REACHED, and that
// is only observable if reaching it goes through a replaceable name.
var validateRunnerFn = validate.Run

// newValidateCmd builds the command.
//
// It is a constructor rather than a package-level var, and its flags are read
// off the returned command rather than bound to package variables, so two
// commands never share flag state. A test drives a fresh one per case and one
// case's --json cannot survive into the next.
func newValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate [category[/package]]",
		Short: "Check that each ebuild still matches the source it points at",
		Long: `Read the build options the upstream archive declares, read the ones the
ebuild passes, and report the difference. Nothing is built, downloaded or
changed: the archive is the one already on disk, put there by the manifest step.

This catches the failure class where a version bump moves the version and the
ebuild stays put. In media-plugins/gst-plugins-qt6 upstream removed the aalib
and libcaca options at 1.29; the ebuild kept passing -Daalib= and -Dlibcaca=,
and every existing check stayed green.

An outcome names its own reach. A gate that could not run says SKIPPED and why
— a missing distfile, a build system that is not Meson, an unreadable ebuild —
so a clean report never means "we did not look".

pkgcheck findings for each package are reported beside the option findings when
pkgcheck is installed. They never affect the exit code: the overlay carries
pre-existing QA findings unrelated to any bump, and letting them decide the
status would fail the whole tree and reduce this command to noise.

--depth selects how far up the ladder to go: none, options, patches, configure
or compile, each rung including every rung before it. It defaults to options,
which is this command as it has always been — read-only, unprivileged, building
nothing. Above options the gates need a tree to build in, and that tree is a
staged copy under ~/.config/bentoo/autoupdate/staging: the published overlay is
never built in and never written to.

Exit codes:
  0  every gate outcome was PASS or SKIPPED
  1  at least one finding of severity error, from any gate but pkgcheck's,
     or an invocation that could not be honoured (a --depth that does not
     name a rung of the ladder)
  2  the selector names something the overlay does not hold

Examples:
  bentoo overlay validate                                  # the whole overlay
  bentoo overlay validate media-plugins                    # one category
  bentoo overlay validate media-plugins/gst-plugins-qt6    # every version of one package
  bentoo overlay validate --json | jq .                    # one JSON document
  bentoo overlay validate --distdir /var/cache/distfiles   # read from a named distdir
  bentoo overlay validate --depth=configure media-plugins/gst-plugins-qt6`,
		Args: cobra.MaximumNArgs(1),
		Run:  runValidate,
	}
	cmd.Flags().Bool("json", false, "Write the whole report to stdout as a single JSON document")
	cmd.Flags().String("distdir", "", "Read distfiles from this directory (never created, never written to)")
	// The default is the shipped behaviour, spelled out rather than left empty
	// (R11.3): `--depth` absent and `--depth=options` are the same run, and the
	// value is read off THIS command below, never from a package variable.
	cmd.Flags().String("depth", validate.DepthOptions.String(),
		"Validate to this rung of the ladder — none, options, patches, configure or compile, each including every rung before it. "+
			"Above \"options\" the gates need a tree to build in, and that tree is a staged copy; the published overlay is never built in")
	return cmd
}

func init() {
	overlayCmd.AddCommand(newValidateCmd())
}

// runValidate drives the gate and exits with the report's code.
//
// # Why the flags are parsed here
//
// cobra has already parsed them by the time Run is called, and re-parsing the
// positional-only args it hands over is a no-op in production. It is not a
// no-op for a test, which drives this function directly with a raw argv — and a
// renderer that could only be reached through cobra's own Execute would be a
// renderer no test could hold still.
//
// # Why a malformed selector is rejected here and a merely unknown one is not
//
// "not-a-category/nor-a-package/too-many-parts" cannot name anything in an
// overlay's two-level layout, so it is answered without doing any work at all.
// A well-formed selector that simply matches nothing is a different thing: only
// the runner, which has scanned the tree, can say so — and it reports it on the
// Report rather than as an error, because the run did produce an answer.
//
// # Why a config failure does not abort
//
// A missing or unreadable overlay path reaches the runner as an empty Overlay
// and comes back as a scan error, which exits 2 naming what went wrong. That
// keeps this command inside the story's governing rule — a condition that stops
// the gate becomes a reported outcome, never an aborted run — and it keeps the
// command's tests from depending on the host having a configured overlay, which
// is one of three environment couplings this repository has had to remove from
// its suite.
func runValidate(cmd *cobra.Command, args []string) {
	ctx, stop := signalContext(cmd.Context())
	defer stop()

	_ = cmd.ParseFlags(args)
	asJSON, _ := cmd.Flags().GetBool("json")
	distdir, _ := cmd.Flags().GetString("distdir")

	// With --json, stdout belongs to the document alone.
	diag := io.Writer(os.Stdout)
	if asJSON {
		diag = os.Stderr
	}

	// The depth is settled before anything else, because a flag value that does
	// not parse is a fault in the invocation itself: it depends on neither the
	// selector nor the overlay, so answering it first costs nothing and reaches
	// no work.
	//
	// IT EXITS 1, NOT 2, AND THE DISTINCTION IS THE CONTRACT DOCUMENTED ABOVE.
	// Exit 2 means one specific thing — the selector names something the overlay
	// does not hold — and a --depth that does not parse says nothing whatever
	// about the overlay's contents, which was never consulted. A CI script that
	// branches on 2 to mean "unknown package" would otherwise mis-handle a typo
	// in a flag. ParseDepth's own error names the offender and lists every valid
	// rung, so the operator is not sent to the source for five short words.
	spelled, err := cmd.Flags().GetString("depth")
	if err != nil {
		_, _ = fmt.Fprintf(diag, "  reading --depth: %v\n", err)
		osExit(1)
		return
	}
	depth, err := validate.ParseDepth(spelled)
	if err != nil {
		_, _ = fmt.Fprintf(diag, "  --depth: %v\n", err)
		osExit(1)
		return
	}

	var selector string
	if rest := cmd.Flags().Args(); len(rest) > 0 {
		selector = rest[0]
	}
	if !validSelector(selector) {
		_, _ = fmt.Fprintf(diag, "  %q is not a category or a category/package\n", selector)
		osExit(2)
		return
	}

	var overlayPath string
	if appCtx, err := loadAppContextNoValidation(); err == nil {
		overlayPath = appCtx.OverlayPath
	}

	// Above `options` the gates need a tree to build in, and it is a STAGED COPY
	// — the published overlay is read, never built in and never written to
	// (R11.2). The root is resolved only for the depths that use one, so the
	// shipped read-only run neither names nor creates a scratch directory
	// (R11.3), and it is the same directory `overlay autoupdate --apply` stages
	// under, so a tree one command proves is a tree the other can find.
	// The three values below are set together or not at all, and the ONE branch
	// that decides them is this depth comparison. A depth-less run leaves all
	// three at their zero value, so the Options it builds are the Options this
	// command has always built — nil seams included (S037-R4.3, R11.3). nil and
	// populated are different contracts inside the runner, not a shortcut for the
	// same one: a nil DistNames parses the package's own Manifest, and a nil
	// StagedManifest means nothing travels at all.
	var stagingRoot string
	var logDir string
	var distNames func(pkgDir string) ([]string, error)
	var stagedManifest func(pkgDir string) ([]byte, error)
	if depth > validate.DepthOptions {
		stagingRoot, err = autoupdateStagingRoot()
		if err != nil {
			_, _ = fmt.Fprintf(diag, "  --depth=%s builds, and a staged tree to build in could not be placed: %v\n", depth, err)
			osExit(1)
			return
		}
		// S037-R5.1. A build gate that FAILED says so on its own, but the reason
		// upstream broke — the option `meson` refused — is only in `ebuild`'s log.
		// The same directory the apply path retains its logs in, so an operator
		// looking for "the log of the thing that just failed" has one place to
		// look regardless of which command ran the gates. A failure to place it is
		// NOT fatal: the gates still run and their reason says the log was not
		// retained, which is worth more than refusing to validate at all.
		if configDir, cerr := autoupdateConfigDir(); cerr == nil {
			logDir = filepath.Join(configDir, "logs")
		} else {
			_, _ = fmt.Fprintf(diag, "  build logs will not be retained: %v\n", cerr)
		}
		// This is the composition design D1 puts here and nowhere else: validate
		// only accepts what a caller supplies, autoupdate owns Manifest
		// GENERATION, and cmd/bentoo is the one place that already imports both.
		// This command validates each package AT ITS PUBLISHED VERSION — it bumps
		// nothing — so the published Manifest is the record that describes the
		// archive actually on disk, and both seams read it (S037-R4.1, S037-R4.2,
		// design D3).
		distNames = publishedManifestDistNames
		stagedManifest = publishedManifestBytes
	}

	report, err := validateRunnerFn(ctx, validate.Options{
		Overlay:  overlayPath,
		Distdir:  distdir,
		Selector: selector,
		// depth.String() rather than the raw flag: the two are the same string
		// for anything ParseDepth accepted, and going through the ladder means
		// the runner is handed a name it can always parse back.
		Depth:          depth.String(),
		StagingRoot:    stagingRoot,
		LogDir:         logDir,
		DistNames:      distNames,
		StagedManifest: stagedManifest,
	})
	if err != nil {
		// AN INTERRUPTION IS NOT A FAILURE TO VALIDATE, and it was being reported
		// as one. Run fills the report with interruptedResult entries precisely so
		// that a stopped sweep still says WHICH packages went unexamined — its
		// governing rule is that a package in view is never left unmentioned — and
		// discarding the report here threw all of that away. Under --json it threw
		// away the entire document: the flag emitted nothing at all, so a stopped
		// run and a run that produced no output were indistinguishable to the `|
		// jq` the flag exists for.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if asJSON {
				renderValidateJSON(report, diag)
			} else {
				renderValidateText(report)
			}
			_, _ = fmt.Fprintf(diag, "  %v\n", err)
			// 128 + SIGINT, the shell's own convention — and deliberately NOT 2.
			// Report.ExitCode documents 2 as "the selector matched nothing", and
			// answering an interrupt with it left a script no way to tell a run
			// that was stopped from a selector that was wrong, short of parsing
			// the diagnostic text.
			osExit(130)
			return
		}
		_, _ = fmt.Fprintf(diag, "  validating %s: %v\n", overlayLabel(overlayPath), err)
		osExit(2)
		return
	}

	if asJSON {
		renderValidateJSON(report, diag)
	} else {
		renderValidateText(report)
	}
	osExit(report.ExitCode())
}

// publishedManifestBytes is Options.StagedManifest for this command: the bytes
// the staged copy of pkgDir must carry before Portage will build in it
// (S037-R4.2, design D3).
//
// # Why the PUBLISHED Manifest
//
// This command bumps nothing. Every ebuild it validates is the one already in
// the overlay, so the digests the published Manifest records are the digests of
// the archive on disk — the same-version half of D3's two callers. (The other
// half, a bump, must feed the GENERATED Manifest instead: the published digests
// there belong to the release being replaced.)
//
// # DIST lines only — and what that does and does not buy
//
// Each DIST line travels UNTOUCHED — never re-encoded, re-ordered or
// re-hashed — because Portage verifies those digests against the archive it
// finds, and a digest that survived a round-trip through some intermediate form
// is a digest the build gates fail on. The records around them are dropped:
// EBUILD, AUX and MISC name files in the package directory, and the staged tree
// holds one ebuild, no metadata.xml and none of the siblings, so those records
// describe a repository that is not there.
//
// AN EARLIER VERSION OF THIS COMMENT CLAIMED THIS FIXED THE NON-THIN OVERLAY
// CASE. IT DOES NOT, and the claim was made without checking Portage's source.
// On a non-thin repository digestcheck goes past its early return and requires
// every .ebuild in the directory to carry an EBUILD record, failing under
// `strict` — so a DIST-only Manifest trips that check exactly as an unfiltered
// one trips FileNotFound. Filtering cannot fix it from this side at all. What
// fixes it is Stage imposing `thin-manifests = true` on the staged tree; see
// stagedThinManifests in validate/stage.go.
//
// So this is hygiene rather than the fix: it keeps records describing absent
// files out of a synthetic tree, and it is what the retired manifestDistLines
// did before the seam replaced it.
//
// # A Manifest that cannot be read is an ERROR, never empty bytes
//
// A non-nil seam is a caller taking responsibility for the answer, so an empty
// result is AUTHORITATIVE — "I looked, and this package publishes nothing"
// (S037-D2). A package whose Manifest could not be opened has said no such
// thing. The error travels instead, and validate renders it as a build gate
// reported SKIPPED carrying these words verbatim (S037-R4.4) — which is why the
// sentence names what was attempted and the directory needed to reproduce it,
// rather than only what the operating system said.
func publishedManifestBytes(pkgDir string) ([]byte, error) {
	body, err := os.ReadFile(publishedManifestPath(pkgDir)) //nolint:gosec // the path is the package directory the runner is walking, joined with a fixed filename
	if err != nil {
		return nil, fmt.Errorf("reading the published Manifest of %s, to give its staged copy the digests Portage verifies: %w", pkgDir, err)
	}
	return distfiles.ManifestDistLines(body), nil
}

// publishedManifestDistNames is Options.DistNames for this command: the upstream
// archives the option gate may look for when it reads a STAGED tree, which
// carries no Manifest of its own (S037-R4.1).
//
// It answers BASENAMES and never Manifest lines — the gate looks each name up
// with os.Stat in the distdir — so the shared parser in internal/common/distfiles
// answers, and this command holds no second copy of the DIST-line grammar.
//
// The read below is not redundant with that parser: ParseManifestDistFilenames
// answers a missing or unreadable Manifest with an empty slice, and through a
// non-nil seam an empty slice is an ANSWER (S037-D2). Reading first is what keeps
// "I could not look" from being reported as "this package publishes no archive".
func publishedManifestDistNames(pkgDir string) ([]string, error) {
	path := publishedManifestPath(pkgDir)
	if _, err := os.ReadFile(path); err != nil { //nolint:gosec // the path is the package directory the runner is walking, joined with a fixed filename
		return nil, fmt.Errorf("reading the published Manifest of %s, to name the archives the option gate looks for: %w", pkgDir, err)
	}
	return distfiles.ParseManifestDistFilenames(path), nil
}

// publishedManifestPath is the one place this command spells out where a package
// directory keeps its Manifest, so the two producers above cannot drift into
// disagreeing about which file they are talking about.
func publishedManifestPath(pkgDir string) string {
	return filepath.Join(pkgDir, "Manifest")
}

// validSelector reports whether a selector can name anything at all. An overlay
// is two levels deep, so anything with a second slash is a usage error rather
// than a miss.
func validSelector(selector string) bool {
	return strings.Count(selector, "/") <= 1 && !strings.HasPrefix(selector, "/")
}

// overlayLabel names the overlay in a diagnostic, including when there is none.
func overlayLabel(path string) string {
	if path == "" {
		return "the overlay (no path could be resolved from the config)"
	}
	return path
}

// renderValidateJSON writes the whole report as ONE document (R5.8).
//
// One document and not a stream: a caller piping this into jq must not have to
// reassemble it. Normalized turns nil slices into empty ones first, so
// `.results[].findings[]` works on every entry.
func renderValidateJSON(report validate.Report, diag io.Writer) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report.Normalized()); err != nil {
		_, _ = fmt.Fprintf(diag, "  writing the JSON report: %v\n", err)
	}
}

// renderValidateText prints the human report.
//
// Every SKIPPED line carries its reason on the line below it. That is the whole
// story in one formatting rule: a skip the operator cannot read is a pass, and
// a report that renders "SKIPPED" the way it renders "PASS" has told them
// nothing they can act on.
func renderValidateText(report validate.Report) {
	if report.UnmatchedSelector != "" {
		output.Error.Printf("  nothing in the overlay matches %q\n", report.UnmatchedSelector)
		return
	}

	output.Header.Printf("Validating %s\n\n", overlayLabel(report.Overlay))

	var failed, passed, skipped, qaFindings int
	for _, res := range report.Results {
		// One column, five gates: the headline is the WORST of them, so a
		// configure failure can never hide behind an option-gate pass. The
		// per-gate outcomes follow on the same line, because R4.4 asks for each
		// gate's own answer and not just the summary of them.
		worst := res.WorstOutcome()
		switch worst {
		case validate.OutcomeFailed:
			failed++
		case validate.OutcomePass:
			passed++
		default:
			// SKIPPED, and anything nobody set. Counting the leftovers here is
			// what keeps the three tallies summing to the number of ebuilds.
			skipped++
		}

		outcomeColor(worst).Printf("  %-14s", string(worst))
		output.Package.Printf("%s-%s", res.Package, res.Version)
		if summary := gateSummary(res.Gates); summary != "" {
			output.Dim.Printf("   %s", summary)
		}
		fmt.Println()

		// Every gate names its OWN reason, prefixed by the gate it belongs to
		// (R4.4, R5.3). One shared reason line is what this replaces, and it was
		// wrong in the ordinary case: an option gate skipping for a missing
		// distfile and a QA gate skipping for a missing pkgcheck are two facts,
		// and the operator has to act on a different one of them each time.
		for _, gate := range res.Gates {
			if gate.Reason != "" {
				output.Dim.Printf("      %s: %s\n", gate.Gate, gate.Reason)
			}
		}

		// info findings are counted here and printed in full only by --json.
		//
		// Measured on the live overlay: media-libs/mesa alone declares 101
		// options its ebuild does not pass, every one a legitimate info finding
		// (R3.2). Printed in full, one package fills a screen and a
		// whole-overlay run buries every error and warning inside thousands of
		// lines nobody scrolls — which is this story's own stated failure mode,
		// a gate too noisy to be read being a gate that gets switched off.
		//
		// Nothing is lost: the finding is emitted, carried on the Report, and
		// written in full by --json. This is a rendering choice about the human
		// surface, not a filter on what the gate reports.
		var infos int
		for _, gate := range res.Gates {
			for _, f := range gate.Findings {
				if f.Gate == validate.GateQA {
					qaFindings++
				}
				// Only the OPTION gate's infos are collapsed into the count,
				// since that is what the line below describes. pkgcheck findings
				// are also carried at info — its records have no level at all —
				// and folding them in here would make the number claim
				// something it is not.
				if f.Gate == validate.GateOptions && f.Severity == validate.SeverityInfo {
					infos++
					continue
				}
				severityColor(f.Severity).Printf("      %-8s", string(f.Severity))
				fmt.Println(f.Detail)
			}
		}
		if infos > 0 {
			output.Dim.Printf("      info:   %d option(s) upstream declares and this ebuild does not pass — see --json\n", infos)
		}
		// The evidence, printed even on a PASS. A pass whose sources are not
		// shown cannot be told apart from a pass that found no source to read,
		// which is the complaint this whole command answers.
		if len(res.Sources) > 0 {
			output.Dim.Printf("      read: %s\n", strings.Join(res.Sources, ", "))
		}
	}

	fmt.Printf("\n%d ebuilds: %d failed, %d passed, %d skipped\n",
		len(report.Results), failed, passed, skipped)

	if qaFindings > 0 {
		output.Dim.Println("pkgcheck findings are all reported at info: its JsonStream records carry no level,\n" +
			"and inferring one from the message text would be a guess. They never affect the exit code.")
	}
}

// gateSummary renders every gate's own outcome on one line, as
// `options=PASS qa=SKIPPED`.
//
// It lists ALL of them, including the one the headline already shows. The
// repetition is the point: R4.4 asks for each gate's outcome separately, and a
// summary that dropped the worst gate would leave the reader deducing which of
// the five the headline came from.
func gateSummary(gates []validate.GateResult) string {
	parts := make([]string, 0, len(gates))
	for _, gate := range gates {
		parts = append(parts, gate.Gate+"="+string(gate.Outcome))
	}
	return strings.Join(parts, " ")
}

// outcomeColor keeps the three outcomes visually distinct, so SKIPPED is never
// mistaken for PASS at a glance — the same distinction the reason line makes in
// words.
func outcomeColor(o validate.Outcome) *color.Color {
	switch o {
	case validate.OutcomeFailed:
		return output.Error
	case validate.OutcomePass:
		return output.Success
	default:
		return output.Warning
	}
}

func severityColor(s validate.Severity) *color.Color {
	switch s {
	case validate.SeverityError:
		return output.Error
	case validate.SeverityWarning:
		return output.Warning
	default:
		return output.Info
	}
}
