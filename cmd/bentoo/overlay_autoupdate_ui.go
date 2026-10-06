package main

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
)

// autoupdateUI is --ui: which renderer this run uses.
//
// Empty means the flag was not passed, which is what lets BENTOO_UI and then
// ui.mode answer instead. The legal set, the precedence and the
// rejection message all live in internal/common/report; nothing here repeats
// them, because a second copy of that list is a list that eventually disagrees.
var autoupdateUI string

// autoupdateAll is --all: list the packages found up to date instead of
// reporting them as a count alone.
//
// It changes WHAT IS SHOWN and nothing else. That is the whole point of the
// flag being cheap: the same packages are scanned, validated and acted upon
// with it and without it.
//
// It is declared on its OWN LINE, outside overlay_autoupdate.go's grouped var
// block, and that is load-bearing rather than a style choice.
// TestAllDoesNotChangeActions parses every non-test file of this package and
// requires each mention of this name to sit on a line that also names a
// renderer Options, a Flags() registration, or a declaration. Folded into a
// grouped block the name would sit on a line matching none of those, and that
// sweep would fail on the declaration itself.
var autoupdateAll bool

// autoupdateExport is --export: a path the finished report is also written to.
// Empty means no export was asked for.
//
// The format follows the path's extension (exportFormatFor), and the
// export is a pure addition: it changes no verdict, no count and no exit status,
// and a path that cannot be written is reported while the terminal
// still receives the report.
var autoupdateExport string

// uiInputs is what THIS PROCESS knows about the mode question before the
// environment is consulted: the two flags, the configuration key, and whether
// stdout is a terminal.
//
// It exists so the environment is read in exactly one place (resolveUIMode)
// while report.ModeInputs stays pure data — that package has no os.Getenv in
// it, deliberately, and this is the edge that supplies what it will not fetch.
type uiInputs struct {
	// Flag is the --ui value, empty when the flag was not passed.
	Flag string
	// NoTUI is the --no-tui FLAG ALONE. The two environment opt-outs it has
	// always travelled with are added by resolveUIMode; adding them here too
	// would fold them in twice.
	NoTUI bool
	// Config is ui.mode, verbatim and unvalidated, empty when the key is
	// absent. Absent must stay distinguishable from an explicit "auto",
	// which is why the loader defaults it to nothing.
	Config string
	// Interactive is output.IsTerminal() and nothing else.
	//
	// NEVER tui.Enabled()'s answer. That one already has the opt-outs folded
	// in, and folding them in twice makes an opted-out run report that the
	// terminal "cannot support" the mode it was given — a false sentence,
	// because the terminal was fine and the operator opted out. The opt-outs
	// belong in NoTUI; the capability belongs here.
	Interactive bool
}

// resolveUIMode answers "which renderer" for one run: it reads the environment,
// folds the opt-outs together, and hands the decision to report.ResolveMode.
//
// It decides nothing itself. The accepted set, the precedence (--ui, then
// BENTOO_UI, then ui.mode, then auto), the downgrade sentence and the rejection
// message are all that package's; a second copy here would drift.
//
// All THREE opt-outs go into NoTUI: tui.Enabled returns false on --no-tui,
// NO_COLOR and BENTOO_NO_TUI, and report.ModeInputs has a field for only two
// of them, a precondition its doc comment cannot enforce. Drop NO_COLOR and an
// operator who set it, and configured nothing about ui.mode, silently starts
// getting inline output where they got plain. An empty value is "not set", per
// the same NO_COLOR convention tui.Enabled follows.
//
// The warning is returned, not printed: a downgrade is one sentence for stderr
// and this function has no opinion about where stderr is; warnUIDowngrade
// routes it. The empty string means nothing was downgraded.
func resolveUIMode(in uiInputs) (report.Mode, string, error) {
	mode, warning, err := report.ResolveMode(report.ModeInputs{
		Flag:        in.Flag,
		Env:         os.Getenv("BENTOO_UI"),
		Config:      in.Config,
		NoTUI:       in.NoTUI || os.Getenv("BENTOO_NO_TUI") != "" || os.Getenv("NO_COLOR") != "",
		Interactive: in.Interactive,
	})
	if err != nil {
		// The EMPTY mode, never the value that was rejected. The rule has two
		// halves — the rejection names the accepted set, AND the check does
		// not run — and returning a usable mode beside an error would leave
		// the second half to the caller's discipline. An empty mode is not a
		// renderer, so a caller that ignored the error still cannot run on it.
		//
		// Returned unwrapped on purpose: ResolveMode's message already names
		// the source the bad value came from and the set it is not in, and a
		// prefix here would say the same thing a second time.
		return "", "", err
	}
	return mode, warning, nil
}

// configuredUIMode reads ui.mode out of a configuration that may not exist.
//
// A nil config is "nothing configured" rather than a failure — the same reading
// resolveAutoupdateDistfileDirs gives it — because a run whose config could not
// be read still has to be able to print WHY, and it cannot print anything
// without first deciding how to render it.
//
// It is one function rather than the same three lines in each caller so that
// the nil reading cannot come to differ between the commands that share the
// mode: "no config" and "no ui.mode" must reach ResolveMode as the same empty
// string from both, or the ambient-mode rule would hold for one command and
// not the other.
func configuredUIMode(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.UI.Mode
}

// resolveAutoupdateUIMode is the one path this command resolves its renderer
// through: the flags, the configuration key and the terminal in, one mode out,
// and the downgrade sentence routed on the way.
//
// # It is not cached, on purpose
//
// It is a pure function of the flags, the config and the terminal, so two
// callers asking get the same answer, and asking costs three os.Getenv calls
// and a switch. A package variable holding the first answer would be a cache
// keyed on nothing: it would hand the second caller the FIRST caller's config,
// which is a defect that only shows up once two commands share this path — and
// sharing it is the whole point. What genuinely must happen once is the
// downgrade sentence, and warnUIDowngrade is what holds that line.
//
// A nil config is read as "nothing configured" rather than as a failure; see
// configuredUIMode.
func resolveAutoupdateUIMode(log *slog.Logger, cfg *config.Config, noTUI bool, isTerminal func() bool) (report.Mode, error) {
	mode, warning, err := resolveUIMode(uiInputs{
		Flag:        autoupdateUI,
		NoTUI:       noTUI,
		Config:      configuredUIMode(cfg),
		Interactive: isTerminal(),
	})
	if err != nil {
		return "", err
	}

	warnUIDowngrade(log, warning)
	return mode, nil
}

// reportModeOrPlain is the rung of the resolution ladder that CANNOT fail: the
// renderer a report producer draws in, with an unusable ambient mode refused out
// loud and answered with plain.
//
// The ladder below it — report.ResolveMode, resolveUIMode (adds the
// environment), resolveAutoupdateUIMode (adds the flags, the config key and the
// terminal) — can each return an error. This one turns that error into a mode
// and a sentence, which every producer of a REPORT wants and none should decide.
//
// The SOURCE decides the answer. --ui is explicit, so the ROOT stops the run
// before any work (newRootCmd's PersistentPreRunE, at the comment opening "an
// unusable --ui stops ANY command before it does work"). BENTOO_UI and ui.mode
// are ambient — inherited from a shell profile or a config file — so failing
// every invocation on one would break commands that render nothing; they are
// refused HERE: plain, exit status untouched, refusal stated on stderr with the
// source, the value and the mode used instead. Warn, not Debug: a Debug record
// reaches no one, and this is a typo that costs the operator every run. EVERY
// producer of a report calls it (`overlay validate` for the sentence alone), so
// a producer inherits the rule by calling this, not by being reviewed for it.
func reportModeOrPlain(log *slog.Logger, cfg *config.Config, noTUI bool, isTerminal func() bool) report.Mode {
	mode, err := resolveAutoupdateUIMode(log, cfg, noTUI, isTerminal)
	if err != nil {
		log.Warn("the UI mode is unusable — this report is rendered in plain instead", "err", err)
		return report.ModePlain
	}
	return mode
}

// modeUsesLiveRegion answers the only question the two live-region call sites
// ask of a mode: does this run redraw in place, or does it print lines?
//
// Fullscreen leaves the live region ON. `autoupdate --apply` and `overlay
// manifest` are Reporter CONSUMERS — they stream a worker's progress — not
// report producers, so handing one of them an alternate screen is separate
// work. Of the two wrong readings this is the less wrong: turning the live
// region OFF takes something away by omission, while taking over the terminal
// mid-compile would take the sudo prompt away with it.
//
// The set is enumerated and the default is plain: a mode added later needs a
// decision made HERE, not inherited from a `!= ModePlain` that quietly says yes
// to anything new. Plain assumes nothing about the terminal, the same reasoning
// renderExport applies. ModeAuto cannot reach this function: ResolveMode never
// returns it.
func modeUsesLiveRegion(mode report.Mode) bool {
	switch mode {
	case report.ModeInline, report.ModeFullscreen:
		return true
	default:
		return false
	}
}

// autoupdateUsesTUI is the `--apply` path's live-region gate, read as a boolean
// from the mode this run resolved to.
//
// What it obeys is the rule that the mode is resolved ONCE per run and every
// consumer reads that one answer: it shares resolveAutoupdateUIMode with
// everything else this command renders, so the report and the apply progress
// cannot disagree about which renderer this run is using.
func (ar *autoupdateRun) autoupdateUsesTUI() bool {
	mode, err := resolveAutoupdateUIMode(ar.log(), ar.uiConfig, ar.opts.noTUI, ar.deps.uiIsTerminal)
	if err != nil {
		// Reachable, and only from the two AMBIENT sources: the root stops an
		// unusable --ui for every command, so what arrives here is a BENTOO_UI
		// or a ui.mode that does not name a mode, answered with plain rather
		// than with a failure.
		//
		// Debug rather than Warn, and that is a decision about WHO SPEAKS. The
		// refusal rule is about REPORTS; this is a live-region boolean on the
		// apply path, which streams a worker's progress instead of drawing one.
		// Where the same command produces a report, presentCheckReport states
		// the refusal once through reportModeOrPlain, and a second sentence from
		// here would answer one typo in two voices.
		//
		// False is the fallback for the same reason plain is: it is the answer
		// that assumes nothing about the terminal.
		ar.log().Debug("apply: the UI mode did not resolve, rendering in plain", "err", err)
		return false
	}
	return modeUsesLiveRegion(mode)
}

// manifestUsesTUI is `overlay manifest`'s live-region gate: the command stops
// deciding on its own and reads the answer the same resolution hands
// autoupdate, so ONE setting governs both rather than an operator having to
// learn a different switch per command. It lives here, beside
// autoupdateUsesTUI, so that "both callers resolve through the same path" is
// visible rather than taken on trust.
//
// Flag is passed: --ui is a ROOT persistent flag, so autoupdateUI holds what
// THIS run was given. Leaving it at zero resolved the mode TWICE from different
// inputs — the report obeyed --ui=plain and the live region did not, so an
// operator who asked for "no escape sequence at all" got them anyway.
//
// NoTUI stays at zero: --no-tui is declared on `overlay autoupdate` alone (see
// the comment beginning "--no-tui deliberately stays on autoupdate" in
// newRootCmd), so the option holds whatever THAT command was given, and reading
// it here would be a cross-command leak. The two ENVIRONMENT opt-outs are folded
// in by resolveUIMode for every caller, which keeps this identical to the
// tui.Enabled(tui.Options{}) it replaces. A nil config reads as "nothing
// configured" here too; see configuredUIMode.
func manifestUsesTUI(log *slog.Logger, cfg *config.Config, isTerminal func() bool) bool {
	mode, warning, err := resolveUIMode(uiInputs{
		Flag:        autoupdateUI,
		Config:      configuredUIMode(cfg),
		Interactive: isTerminal(),
	})
	if err != nil {
		// What reaches here is an AMBIENT mode alone: the root rejects an
		// unusable --ui for every command, so only a BENTOO_UI or a ui.mode that
		// does not name a mode can arrive (one a higher-precedence source
		// overrules comes back as a warning instead). It records rather than
		// fails: regenerating a Manifest is not a rendering question, and
		// refusing to do it over a display key would be worse than plain.
		//
		// Debug rather than Warn, for the reason autoupdateUsesTUI states: this
		// is a live-region boolean, and presentManifestReport already states
		// the refusal through reportModeOrPlain. A second sentence from here
		// would answer one typo in two voices.
		//
		// A `--dry-run` hides that second voice: chooseManifestReporter returns
		// tui.Noop() before this gate on a dry run, which is why
		// TestManifestSingleVoice asserts on a REAL run.
		log.Debug("manifest: the UI mode did not resolve, rendering in plain", "err", err)
		return false
	}

	warnUIDowngrade(log, warning)
	return modeUsesLiveRegion(mode)
}

// uiDowngradeReported makes the downgrade sentence's "once" explicit.
//
// The mode is resolved once per run, so the sentence would reach stderr
// once even without this guard. It is here because "once" is the requirement
// and a second resolution is one refactor away — and because a swap is cheaper
// than the alternative of trusting that nobody adds one. It is an atomic rather
// than a sync.Once so that two goroutines resolving at the same time still
// produce one line, and so that a test can put it back.
var uiDowngradeReported atomic.Bool

// warnUIDowngrade puts the downgrade sentence on stderr, at most once.
//
// On stderr because the report itself goes to stdout, and a run whose output is
// being piped into a file or a diff must not find a UI notice in the middle of
// it. Through the logger because that is where this command's other "your
// configuration says something this run cannot honour" notices already go (see
// sanitizeConfiguredDir), so one run does not answer in two voices.
//
// The empty string is ResolveMode's success signal and prints nothing: a
// sentence on every run would train the reader to skip the one run it matters
// on.
func warnUIDowngrade(log *slog.Logger, warning string) {
	if warning == "" || uiDowngradeReported.Swap(true) {
		return
	}
	log.Warn("the UI mode was downgraded", "reason", warning)
}

// exportFormat is the syntax an export is written in. It is a string
// type for the reason report.Mode and report.Outcome are: a value that names
// itself is readable in a message without a second table mapping it back.
type exportFormat string

const (
	// exportPlain is the same text a plain terminal render produces, and the
	// format any extension that is not one of the two below selects.
	exportPlain exportFormat = "plain"
	// exportMarkdown is the syntax a pull request comment, an issue and a file
	// in a repository all read.
	exportMarkdown exportFormat = "markdown"
	// exportJSON is the view model serialized, for a reader that is a program.
	exportJSON exportFormat = "json"
)

// exportFormatFor picks the export syntax from the path's extension:
// .md is Markdown, .json is JSON, and anything else is plain text.
//
// # The comparison is case-insensitive, unlike the mode names
//
// report.parseMode matches its four words exactly because the same word is read
// from three places and a normalization applied in one of them would make the
// config accept what the flag rejects. An extension has no such second reader:
// REPORT.MD is the file a Windows editor or a shell completion produces, and
// writing plain text into it would be answering a typo nobody made.
//
// # The extension is the LAST element's, which is why report.json/x is plain
//
// filepath.Ext reads the final dot of the final path element, so a directory
// called report.json holding a file called x gives no extension at all. That is
// the right answer: the format has to follow the file being written, not a
// directory that happens to be named after one.
func exportFormatFor(path string) exportFormat {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md":
		return exportMarkdown
	case ".json":
		return exportJSON
	default:
		return exportPlain
	}
}

// writeExport writes the complete report to path in the syntax its extension
// selects.
//
// # Every error names the path
//
// An operator cannot fix what the message does not name, and the whole class of
// failure here — a directory that does not exist, a read-only mount, a full
// disk — is fixed by acting on that one string. The failure is RETURNED rather
// than printed because the export is subordinate to the terminal: the
// caller reports it and renders the report anyway, and an export that decided
// on its own how loudly to fail would be deciding that for it.
//
// # The close is checked
//
// A buffered write that only fails at close is the ordinary shape of a full
// disk, and a function that ignored it would report success for a file that is
// truncated. The deferred close is the fallback for the error paths above it;
// closing twice returns os.ErrClosed, which is exactly the nothing it should be.
func writeExport(path string, run report.Run) error {
	file, err := os.Create(path) //nolint:gosec // G304: path is the user's own --export flag; writing where the user asked is the feature
	if err != nil {
		return fmt.Errorf("creating the export %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	if err := renderExport(file, run, exportFormatFor(path)); err != nil {
		return fmt.Errorf("writing the export %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("completing the export %s: %w", path, err)
	}
	return nil
}

// renderExport writes run to w in one export syntax.
//
// exportPlain is the DEFAULT rather than a case of its own: it is the format
// every extension other than the two named above gets, and it is what a format
// this switch has not been taught about still produces — a whole report in the
// least demanding syntax there is, rather than an empty file.
//
// The sections are built ONCE, above the switch, as presentCheckReport does for
// the screen modes: "the export formats differ in syntax and not in content"
// becomes a fact about this call rather than a promise about three renderers.
// A branch that built its own could quietly ask for less, and an export whose
// completeness depends on the extension is a rule nobody wrote down.
//
// JSON does not consume them, and building them on that path costs a slice of
// rows this function then drops — the price of the invariant. render.JSON
// serializes the whole run, so it arrives at the same completeness.
func renderExport(w io.Writer, run report.Run, format exportFormat) error {
	blocks := run.Sections(exportContent())

	switch format {
	case exportMarkdown:
		return render.Markdown(w, blocks)
	case exportJSON:
		return render.JSON(w, run)
	default:
		return render.Plain(w, blocks, render.Options{Width: unshortenedWidth})
	}
}

// The two values an export asks for when it builds its sections. They are
// constants rather than fields read back from a caller: an export never asks
// what the terminal was told, so there is nothing here for a screen setting to
// arrive through.
//
// They are named rather than written as bare literals because a positional
// report.SectionOptions literal says only that something was on and something
// else was off. exportContent lives in overlay_autoupdate_check.go, beside the
// screen's own options, because building a report.SectionOptions means writing
// down the field that omits the plan — and a source-text guard over this file
// forbids that name here, precisely so an export can never acquire one.
//
// Both formats ask for everyScannedPackage: the file carries the complete
// report whatever the terminal was told, so its completeness never depends on
// the extension.
const (
	// everyScannedPackage lists every package the run looked at, instead of
	// counting the ones found up to date. It is what EVERY export asks for: a
	// record is kept precisely because the terminal is gone, and one that named
	// only the interesting packages could not answer "was this one checked at
	// all".
	everyScannedPackage = true

	// keepThePlan states the validation-plan section, whatever the screen was
	// told to do with it.
	//
	// A report is kept precisely BECAUSE the terminal is gone, so a record
	// missing the plan the terminal had already shown answers no question later
	// — the plan is where a package's reason is stated at all, so
	// dropping it from a file would not shorten the record, it would empty it.
	// A named false says that at the call site; a bare false would only say
	// something was off.
	keepThePlan = false
)

// renderCheckReportIn writes the report to the terminal through the renderer
// the resolved mode names.
//
// The three modes differ in presentation and not in content — a property OF
// THE COMMAND, not of any one renderer: each renderer can be self-consistent
// while the command hands one of them a different report or different Options.
// Naming the mapping lets a test drive every mode through the same call and
// compare what came out. Every mode receives the SAME finished sections and the
// SAME Options, and neither is touched here, so there is no branch that could
// filter its own report or widen its own budget.
//
// Plain is the default, not a case: a mode added later renders whole and
// escape-free instead of falling through a `switch` that prints nothing — the
// same reasoning as renderExport and modeUsesLiveRegion. ModeAuto cannot
// arrive here, because ResolveMode never returns it.
//
// The destination is os.Stdout, read at call time, so all three modes write to
// one place and a test can swap the descriptor to see what an operator saw.
func renderCheckReportIn(mode report.Mode, blocks []report.Section, opts render.Options) error {
	var err error

	// All three renderers take sections rather than a report, and the caller
	// built them once. Nothing below knows what a package is, so a mode cannot
	// decide to say something the others do not.
	switch mode {
	case report.ModeInline:
		err = render.Inline(blocks, opts)
	case report.ModeFullscreen:
		err = render.Fullscreen(blocks, opts)
	default:
		err = render.Plain(os.Stdout, blocks, opts)
	}

	if err != nil {
		return fmt.Errorf("rendering the report in %s mode: %w", mode, err)
	}
	return nil
}

// unshortenedWidth is the line budget a PLAIN export is rendered at: a number
// no report can reach, so nothing is ever cut.
//
// It is not a terminal width and must never be mistaken for one. render.Options
// reads a Width of 0 as "ask the device", and the device here is whichever
// terminal the operator happened to be standing at — which would make the FILE
// inherit the screen's truncation. A record missing exactly what the screen
// dropped answers no question later, and a report is kept precisely because the
// screen is gone.
//
// Markdown and JSON cannot make this mistake at all: they take no Options, so
// their signature already says nothing is cut. Plain is the one export format
// that shares a renderer with the terminal, so it is the one place the width
// has to be said out loud.
const unshortenedWidth = math.MaxInt
