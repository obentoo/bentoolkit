package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/spf13/cobra"
)

// This file is `bentoo overlay validate`: the gate that asks whether an ebuild
// still matches the source it points at, by reading the build options the
// upstream archive declares, reading the ones the ebuild passes, and
// subtracting. At the DEFAULT depth it builds nothing, needs no privilege,
// makes no network call and asks no model.
//
// THAT SENTENCE USED TO HAVE NO QUALIFIER, and it was true until this story
// wired the build seams: every build gate reached from here SKIPPED, so the
// command really was read-only whatever `--depth` said. It now stages each
// candidate outside the overlay and runs upstream's own build phases against
// it, which compiles code and fetches any distfile this host does not already
// hold. The published overlay is still never written to — but "read-only" has
// stopped being true of the HOST, and a command that says otherwise in its own
// help is how an operator runs a whole-overlay compile by accident.
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

// newValidateCmd builds the command.
//
// It is a constructor rather than a package-level var, and its flags are read
// off the returned command rather than bound to package variables, so two
// commands never share flag state. A test drives a fresh one per case and one
// case's --json cannot survive into the next.
func newValidateCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "validate [category[/package]]",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Check that each ebuild still matches the source it points at",
		Long: `Read the build options the upstream archive declares, read the ones the
ebuild passes, and report the difference. At the default depth nothing is built,
downloaded or changed: the archive is the one already on disk, put there by the
manifest step.

--depth above "options" is different. Each candidate is staged outside the
overlay and upstream's own build phases are run against it, so that depth
compiles code and downloads any distfile this host does not already hold — over
every package the selector matches. The published overlay is still never written
to.

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

--depth selects how far up the ladder to go: none, options, patches,
configure, compile or install, each rung including every rung before it. It
defaults to options, which is this command as it has always been — read-only,
unprivileged, building nothing. Above options the gates need a tree to build
in, and that tree is a staged copy under ~/.config/bentoo/autoupdate/staging:
the published overlay is never built in and never written to.

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
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(cmd, args, d)
		},
	}
	// NO BACK-QUOTE IN THE SENTENCE BELOW, and that is load-bearing rather than
	// stylistic: pflag reads the first back-quoted substring of a usage string as
	// the flag's VALUE PLACEHOLDER and prints it after the flag name
	// (UnquoteUsage, flag.go:594; FlagUsagesWrapped, flag.go:725). Quoting
	// jq '.kind' here made --help advertise "--json jq '.kind'" on a boolean that
	// rejects every argument — and stripped the quotes out of the sentence anyway,
	// so it bought nothing and cost the flag its own signature. The damage exists
	// only in rendered help and never in this line, which is why the guard is a
	// test rather than a reading of this file: flag_usage_test.go.
	cmd.Flags().Bool("json", false, "Write the whole report to stdout as a single JSON document. It is the same document --export=<path>.json writes to a file, at stdout instead: schema and kind at the root, and this command's own model one level down under payload. A consumer that already reads an exported report reads this one, and jq '.kind' says which command wrote it")
	cmd.Flags().String("distdir", "", "Read distfiles from this directory (never created, never written to)")
	// The default is the shipped behaviour, spelled out rather than left empty
	// (R11.3): `--depth` absent and `--depth=options` are the same run, and the
	// value is read off THIS command below, never from a package variable.
	cmd.Flags().String("depth", validate.DepthOptions.String(),
		"Validate to this rung of the ladder — none, options, patches, configure, compile or install, each including every rung before it. "+
			"Above \"options\" the gates need a tree to build in, and that tree is a staged copy; the published overlay is never built in")
	return cmd
}

// runValidate drives the gate and returns the report's code as its exit
// status (func exitWith: nil when the code is 0).
//
// # Why the flags are parsed here
//
// cobra has already parsed them by the time RunE is called, and re-parsing the
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
func runValidate(cmd *cobra.Command, args []string, d *deps) error {
	// The process-wide context (func commandContext): overlay validate is
	// cancellable, so the first SIGINT, SIGTERM or SIGHUP cancels the run.
	ctx := commandContext(cmd)

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
		return exitWith(1)
	}
	depth, err := validate.ParseDepth(spelled)
	if err != nil {
		_, _ = fmt.Fprintf(diag, "  --depth: %v\n", err)
		return exitWith(1)
	}

	var selector string
	if rest := cmd.Flags().Args(); len(rest) > 0 {
		selector = rest[0]
	}
	if !validSelector(selector) {
		_, _ = fmt.Fprintf(diag, "  %q is not a category or a category/package\n", selector)
		return exitWith(2)
	}

	var overlayPath string
	// requireIsolation is read from the SAME key `overlay autoupdate` reads
	// (autoupdate.validate.require_isolation), because the gates it governs are
	// the same gates. A config that could not be loaded leaves it false, which is
	// the shipped behaviour of both commands with the key unset — the run is not
	// refused over a missing config file, it simply carries no policy to apply.
	var requireIsolation bool
	if appCtx, err := loadAppContextNoValidation(); err == nil {
		overlayPath = appCtx.OverlayPath
		requireIsolation = appCtx.Config.Autoupdate.Validate.GetRequireIsolation()
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
			return exitWith(1)
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

	report, err := d.validateRunner(ctx, validate.Options{
		Overlay:  overlayPath,
		Distdir:  distdir,
		Selector: selector,
		// depth.String() rather than the raw flag: the two are the same string
		// for anything ParseDepth accepted, and going through the ladder means
		// the runner is handed a name it can always parse back.
		Depth:            depth.String(),
		StagingRoot:      stagingRoot,
		LogDir:           logDir,
		RequireIsolation: requireIsolation,
		DistNames:        distNames,
		StagedManifest:   stagedManifest,
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
			// complete=false, and this is the one call site that passes it. The
			// envelope's Complete means "the run reached the end of its plan",
			// and this branch is reached precisely because it did not — so the
			// exported document says so in the key every kind of run answers,
			// beside the diagnostic below that only a human reads.
			presentValidateReport(d, report, false, asJSON, diag)
			_, _ = fmt.Fprintf(diag, "  %v\n", err)
			// 128 + SIGINT, the shell's own convention — and deliberately NOT 2.
			// Report.ExitCode documents 2 as "the selector matched nothing", and
			// answering an interrupt with it left a script no way to tell a run
			// that was stopped from a selector that was wrong, short of parsing
			// the diagnostic text.
			return exitWith(130)
		}
		_, _ = fmt.Fprintf(diag, "  validating %s: %v\n", overlayLabel(overlayPath), err)
		return exitWith(2)
	}

	presentValidateReport(d, report, true, asJSON, diag)
	// The status is the RUN's, computed from what the gates said. It is read
	// after the export deliberately and is unaffected by it: exportReport
	// returns nothing, so a path that could not be written has no value to
	// travel back through (R3.5).
	return exitWith(report.ExitCode())
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
// # A MISSING Manifest is an answer; a Manifest that cannot be READ is a fault
//
// The two used to be one branch, and collapsing them kept an entire class of
// package out of every build gate. Under thin-manifests a Manifest holds DIST
// lines and NOTHING else, so a package with no distfile has no Manifest file at
// all — not an empty one. That is the normal, expected state of a very common
// shape: every acct-group/*, every acct-user/* and every virtual/*, and in
// ::bentoo today net-dns/bind-tools, app-eselect/eselect-nodejs and
// sys-kernel/linux-firmware. Reporting their normal state as a fault made the
// prepared build read "a Manifest was expected and its production failed", which
// is a reported skip — so those packages were never measured, and nothing inside
// validate could reach them, because the answer was decided here.
//
// So an absent file returns NO CONTENT AND NO ERROR: the seam's third answer,
// which validate answers by staging an empty Manifest and letting Portage
// discriminate (prepareStagedManifest, validate/run.go). Measured rather than
// assumed — with an empty Manifest present Portage runs the phases of an ebuild
// with no SRC_URI, and refuses one WITH a SRC_URI at digest verification before
// attempting any fetch (.draft/d4-portage-evidence.md).
//
// A Manifest that EXISTS and could not be opened keeps travelling as an error,
// with the sentence it has always carried. A non-nil seam is a caller taking
// responsibility for the answer, so no content is AUTHORITATIVE — "I looked, and
// this package publishes nothing" (S037-D2) — and a package whose Manifest was
// unreadable has said no such thing. Handing that back as the no-distfile class
// would stage an empty Manifest over a package that HAS digests, and the gate
// would then report on a candidate nobody could describe. That is D6's
// conflation exactly: "I could not look" and "there is nothing there" are two
// answers, and a report that merges them denies what it also proves.
//
// The distinction is read off the error VALUE, with errors.Is. A stat before the
// read would be a race — the file can vanish between the two calls — and matching
// on the message text would break the moment the operating system reworded it.
//
// validate renders the error case as a build gate reported SKIPPED carrying
// these words verbatim (S037-R4.4), which is why the sentence names what was
// attempted and the directory needed to reproduce it, rather than only what the
// operating system said.
func publishedManifestBytes(pkgDir string) ([]byte, error) {
	body, err := os.ReadFile(publishedManifestPath(pkgDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
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
// The parser this calls is the ERROR-RETURNING one, and that is the whole
// difference: ParseManifestDistFilenames answers a missing or unreadable Manifest
// with an empty slice, and through a non-nil seam an empty slice is an ANSWER
// (S037-D2). This command used to recover the distinction by reading the file
// once and discarding the data, only to learn it was readable, then parsing it
// again — as did internal/autoupdate's publishedDistNames, in its own copy of the
// same work-around. ReadManifestDistFilenames reports the read failure at the
// source instead, from a single read (S039-R5.1), so what keeps "I could not
// look" from being reported as "this package publishes no archive" is now the
// parser's answer rather than a probe above it. The sentence below is unchanged
// to the byte (S039-R5.3): the mechanism moved, the report did not.
//
// # A MISSING Manifest names no archive; one that cannot be READ is still a fault
//
// This is publishedManifestBytes' distinction, above, applied to the same file
// for the same package (S040-R3.1). Under thin-manifests a Manifest holds DIST
// lines and NOTHING else, so a package with no distfile has no Manifest file at
// all — not an empty one — and that is the normal state of a very common shape:
// every acct-group/*, every acct-user/* and every virtual/*, and in ::bentoo
// today net-dns/bind-tools, app-eselect/eselect-nodejs and
// sys-kernel/linux-firmware. Reporting it through the error return made the
// option gate say "the distfile names could not be produced: … no such file or
// directory", which blames the CHECKOUT for a package whose truth is that it
// declares nothing, and sends an operator to repair a file that was never meant
// to exist.
//
// NOTHING NEW IS WRITTEN for the sentence that replaces it. With no names and no
// error the gate reaches selectDistfile, and its existing named refusal — "<the
// source> names no distfile, so there was no archive to look for in <distdir>",
// story 031's words — is already the right report for a package that declares no
// archive. The outcome also stays a reported SKIPPED (S040-R3.3): nothing was
// compared against this package, so it has not been validated, and widening what
// counts as "declares no archive" must not widen into a pass.
//
// The two seams read the SAME FILE for the same package. Story 039 taught the
// distinction to publishedManifestBytes and left this one reporting the file's
// absence as a fault; its task-4 commit (907a33e) recorded this half as "task
// 5's", and task 5 could not take it without breaking its own R5.3, which held
// the two wrappers byte-identical on inputs where they already AGREED. They
// agreed that a missing file is a fault, and they should not have — leaving them
// disagreeing about what one file's absence means is the divergence R5.3 existed
// to prevent, arrived at from the other side. This discharges that deferral.
//
// The distinction is read off the error VALUE, with errors.Is, for the reasons
// publishedManifestBytes' comment gives at length: a stat before the read is a
// race, because the file can vanish between the two calls, and matching on the
// message text breaks the moment the operating system rewords it.
//
// # The applier-side sibling is NOT this, and stays as it is (S040-R3.4)
//
// internal/autoupdate's publishedDistNames answers for ONE bump on the APPLY
// path, where the candidate's Manifest has just been produced by the manifest
// step — so an absent Manifest there is that step not having done what it
// reported, which is a fault and remains one. This one answers for a package at
// its PUBLISHED version, where a thin tree legitimately carries no Manifest at
// all. Same file name, two different propositions, and an unrequired change on
// the apply path is what story 039's R1.6 was protecting against.
func publishedManifestDistNames(pkgDir string) ([]string, error) {
	names, err := distfiles.ReadManifestDistFilenames(publishedManifestPath(pkgDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the published Manifest of %s, to name the archives the option gate looks for: %w", pkgDir, err)
	}
	return names, nil
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

// renderValidateJSON moved to overlay_validate_report.go with story 046's
// sub-task 8.1, and its encoder went with it (R4.3, design D8). It used to build
// a json.Encoder here and write validate.Report.Normalized() at the document
// ROOT — the project's second JSON schema, which `--export` could not produce
// and no consumer of `--export` could read. It now writes the same report.Run
// every other command exports, through the same renderExport, with the model one
// level down under "payload".
//
// The note is left rather than deleted because this is where the JSON write path
// has been since the flag was added, and it is where a reader looking for it will
// come. What is still HERE is the human renderer below: this command's report
// content migrates onto the shared model in story 047, and until it does, the
// text a person reads is this file's own.

// infoCountLabel labels the line that stands in for the option-gate info
// findings this renderer collapses into a count.
//
// It is a constant because it is used twice — once as a value the severity
// column is measured over, once as the value laid into that column — and two
// spellings of one label is how a column comes to be sized for a word it does
// not print, or to print a word it was not sized for.
const infoCountLabel = "info:"

// renderValidateText prints the human report.
//
// Every SKIPPED line carries its reason on the line below it. That is the whole
// story in one formatting rule: a skip the operator cannot read is a pass, and
// a report that renders "SKIPPED" the way it renders "PASS" has told them
// nothing they can act on.
func renderValidateText(report validate.Report) {
	if report.UnmatchedSelector != "" {
		output.Error.Fprintf(os.Stderr, "  nothing in the overlay matches %q\n", report.UnmatchedSelector)
		return
	}

	output.Header.Printf("Validating %s\n\n", overlayLabel(report.Overlay))

	// validateLine is one ebuild's block, established before any of it is
	// printed.
	//
	// # Why the render is now two passes and not one
	//
	// It was one loop that printed each ebuild as it reached it, and that is
	// exactly why its two columns carried typed widths — %-14s for the outcome
	// word, %-8s for a finding's severity. A loop that prints its first row
	// before it has seen its last cannot know how wide either column has to be,
	// so both were guessed, and a guess is wrong in both directions at once: 14
	// cells for a word that is never longer than SKIPPED's seven, and an
	// overflow the day a sixth outcome is spelled longer than the guess (R6.2).
	//
	// Collecting first is the whole of the fix. The values are the same values
	// in the same order and the printing below is the same printing; what
	// changed is that the widths are now measured from what THIS run produced.
	//
	// The type is local because it describes a layout and not a run: what a run
	// found is validate.Report's business, and a shape that exists so a printer
	// can measure its own columns has no business outliving the function that
	// prints them.
	type validateLine struct {
		result validate.EbuildResult
		// worst is the headline. One column, five gates: the headline is the
		// WORST of them, so a configure failure can never hide behind an
		// option-gate pass. The per-gate outcomes follow on the same line,
		// because R4.4 asks for each gate's own answer and not just the summary
		// of them.
		worst validate.Outcome
		// findings are the ones this block PRINTS, in gate order and then in
		// the order each gate reported them — the order the single loop
		// printed them in, kept because a report whose lines reorder between
		// runs cannot be diffed.
		findings []validate.Finding
		// infos is how many option-gate info findings were collapsed into the
		// count line instead of printed.
		infos int
	}

	var failed, passed, skipped, qaFindings int
	lines := make([]validateLine, 0, len(report.Results))
	// The values each column will hold, gathered as the lines are. Nothing is
	// deduplicated: a column is as wide as its widest value, and a repeated
	// value cannot change which one that is.
	outcomes := make([]string, 0, len(report.Results))
	var severities []string

	for _, res := range report.Results {
		line := validateLine{result: res, worst: res.WorstOutcome()}
		switch line.worst {
		case validate.OutcomeFailed:
			failed++
		case validate.OutcomePass:
			passed++
		default:
			// SKIPPED, and anything nobody set. Counting the leftovers here is
			// what keeps the three tallies summing to the number of ebuilds.
			skipped++
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
		for _, gate := range res.Gates {
			for _, f := range gate.Findings {
				if f.Gate == validate.GateQA {
					qaFindings++
				}
				// Only the OPTION gate's infos are collapsed into the count,
				// since that is what the count line describes. pkgcheck findings
				// are also carried at info — its records have no level at all —
				// and folding them in here would make the number claim
				// something it is not.
				if f.Gate == validate.GateOptions && f.Severity == validate.SeverityInfo {
					line.infos++
					continue
				}
				line.findings = append(line.findings, f)
				severities = append(severities, string(f.Severity))
			}
		}
		// The count line's label sits in the severity column too, so it is
		// measured with the rest of it. It used to be kept in line by hand —
		// "info:" followed by three typed spaces, which came to 8 because %-8s
		// did — and an agreement between two hands about a number neither
		// states is one edit from being a misalignment nobody notices.
		if line.infos > 0 {
			severities = append(severities, infoCountLabel)
		}

		outcomes = append(outcomes, string(line.worst))
		lines = append(lines, line)
	}

	// Both columns, measured in display cells and never in bytes (R6.1): a cell
	// is the unit the terminal aligns on, and it is the one unit that survives a
	// value carrying a rune wider or narrower than its byte count suggests.
	//
	// The severity column is measured over what this run will PRINT rather than
	// over the three words the vocabulary holds: a run whose findings are all
	// errors gets a five-cell column, and one that printed no finding at all
	// gets none, because the loop that would have used it never runs.
	outcomeWidth := render.ColumnWidth(outcomes)
	severityWidth := render.ColumnWidth(severities)

	for _, line := range lines {
		// The single space after each padded word is the gap between two
		// columns — air that belongs to neither, which nothing in a run's data
		// can make wider, so it is written down where a width is measured
		// (R6.3). %-14s folded the two together, which is how seven cells of
		// separator ended up inside a number nobody could account for.
		outcomeColor(line.worst).Printf("  %s ", padColumn(string(line.worst), outcomeWidth))
		output.Package.Printf("%s-%s", line.result.Package, line.result.Version)
		if summary := gateSummary(line.result.Gates); summary != "" {
			output.Dim.Printf("   %s", summary)
		}
		fmt.Println()

		// Every gate names its OWN reason, prefixed by the gate it belongs to
		// (R4.4, R5.3). One shared reason line is what this replaces, and it was
		// wrong in the ordinary case: an option gate skipping for a missing
		// distfile and a QA gate skipping for a missing pkgcheck are two facts,
		// and the operator has to act on a different one of them each time.
		for _, gate := range line.result.Gates {
			if gate.Reason != "" {
				output.Dim.Printf("      %s: %s\n", gate.Gate, gate.Reason)
			}
		}

		for _, f := range line.findings {
			severityColor(f.Severity).Printf("      %s ", padColumn(string(f.Severity), severityWidth))
			fmt.Println(f.Detail)
		}
		if line.infos > 0 {
			output.Dim.Printf("      %s %d option(s) upstream declares and this ebuild does not pass — see --json\n",
				padColumn(infoCountLabel, severityWidth), line.infos)
		}
		// The evidence, printed even on a PASS. A pass whose sources are not
		// shown cannot be told apart from a pass that found no source to read,
		// which is the complaint this whole command answers.
		if len(line.result.Sources) > 0 {
			output.Dim.Printf("      read: %s\n", strings.Join(line.result.Sources, ", "))
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
