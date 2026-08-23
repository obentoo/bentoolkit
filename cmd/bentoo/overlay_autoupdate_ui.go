package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
)

// autoupdateUI is --ui: which renderer this run uses (S044-R3).
//
// Empty means the flag was not passed, which is what lets BENTOO_UI and then
// ui.mode answer instead (R3.2). The legal set, the precedence and the
// rejection message all live in internal/common/report; nothing here repeats
// them, because a second copy of that list is a list that eventually disagrees.
var autoupdateUI string

// autoupdateAll is --all: list the packages found up to date instead of
// reporting them as a count alone (R8.1, R8.2).
//
// It changes WHAT IS SHOWN and nothing else. R8.4 is the whole point of the
// flag being cheap: the same packages are scanned, validated and acted upon
// with it and without it.
//
// It is declared on its OWN LINE, outside overlay_autoupdate.go's grouped var
// block, and that is load-bearing rather than a style choice.
// TestAllDoesNotChangeActions parses every non-test file of this package and
// requires each mention of this name to sit on a line that also names a
// renderer Options, a Flags() registration, or a declaration. Folded into a
// grouped block the name would sit on a line matching none of those, and the
// sweep that enforces R8.4 would fail on the declaration itself.
var autoupdateAll bool

// autoupdateExport is --export: a path the finished report is also written to
// (R9.1). Empty means no export was asked for.
//
// The format follows the path's extension (R9.2, exportFormatFor), and the
// export is a pure addition: it changes no verdict, no count and no exit status
// (R9.6), and a path that cannot be written is reported while the terminal
// still receives the report (R9.5).
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
	// absent. Absent must stay distinguishable from an explicit "auto"
	// (R3.7), which is why the loader defaults it to nothing.
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
// message are all that package's, and duplicating any of them here would create
// a second authority that drifts.
//
// # All THREE opt-outs go into NoTUI
//
// tui.Enabled returns false on three signals — the --no-tui flag, NO_COLOR and
// BENTOO_NO_TUI — and report.ModeInputs deliberately has a field for only two
// of them. Its doc comment records the precondition and cannot enforce it, so
// it is honoured here: NO_COLOR is folded in with the rest. Drop it and an
// operator who set NO_COLOR, and configured nothing about ui.mode, silently
// starts getting inline output where they get plain today — precisely the
// change R3.7 forbids. An empty value is "not set", per the same NO_COLOR
// convention tui.Enabled follows.
//
// # The warning is returned, not printed
//
// A downgrade is one sentence for stderr (R3.6) and this function has no
// opinion about where stderr is; warnUIDowngrade routes it. The empty string
// means nothing was downgraded.
func resolveUIMode(in uiInputs) (report.Mode, string, error) {
	mode, warning, err := report.ResolveMode(report.ModeInputs{
		Flag:        in.Flag,
		Env:         os.Getenv("BENTOO_UI"),
		Config:      in.Config,
		NoTUI:       in.NoTUI || os.Getenv("BENTOO_NO_TUI") != "" || os.Getenv("NO_COLOR") != "",
		Interactive: in.Interactive,
	})
	if err != nil {
		// The EMPTY mode, never the value that was rejected. R3.9 has two
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

// uiIsTerminal is the package seam for the stdout-TTY stat: the ONE point where
// this package asks "is stdout a terminal?", so a test can answer it.
//
// It mirrors internal/common/tui's own isTerminal seam deliberately, and for
// the same reason. R3.7 is a statement about behaviour ON a terminal and OFF
// one; a `go test` binary writes to a pipe, so it is only ever off one. Without
// a seam the on-a-terminal half of the requirement cannot be checked at all —
// and that is the half that decides whether an operator who configured nothing
// still gets the live region they have always had.
var uiIsTerminal = output.IsTerminal

// configuredUIMode reads ui.mode out of a configuration that may not exist.
//
// A nil config is "nothing configured" rather than a failure — the same reading
// resolveAutoupdateDistfileDirs gives it — because a run whose config could not
// be read still has to be able to print WHY, and it cannot print anything
// without first deciding how to render it.
//
// It is one function rather than the same three lines in each caller so that
// the nil reading cannot come to differ between the two commands R3.8 joins:
// "no config" and "no ui.mode" must reach ResolveMode as the same empty string
// from both, or R3.7 would hold for one command and not the other.
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
// sharing it is the whole point (R3.8). What genuinely must happen once is the
// downgrade sentence, and warnUIDowngrade is what holds that line.
//
// A nil config is read as "nothing configured" rather than as a failure; see
// configuredUIMode.
func resolveAutoupdateUIMode(cfg *config.Config) (report.Mode, error) {
	mode, warning, err := resolveUIMode(uiInputs{
		Flag:        autoupdateUI,
		NoTUI:       autoupdateNoTUI,
		Config:      configuredUIMode(cfg),
		Interactive: uiIsTerminal(),
	})
	if err != nil {
		return "", err
	}

	warnUIDowngrade(warning)
	return mode, nil
}

// modeUsesLiveRegion answers the only question the two live-region call sites
// ask of a mode: does this run redraw in place, or does it print lines?
//
// # Fullscreen leaves the live region ON, and that is the Out-of-Scope boundary
//
// `autoupdate --apply` and `overlay manifest` are Reporter CONSUMERS — they
// stream a worker's progress — not report producers, so handing one of them an
// alternate screen is a different piece of work than this story does. Both
// readings of fullscreen are wrong here, and this is the less wrong one:
// turning the live region OFF because the operator asked for fullscreen
// somewhere else takes something away by omission, while taking over the
// terminal mid-compile would take the sudo prompt away with it.
//
// # The set is enumerated and the default is plain
//
// A fifth mode added later needs a decision made HERE, not inherited from a
// `!= ModePlain` that quietly says yes to anything new. Plain is the mode that
// assumes nothing about the terminal, so it is the safe default — the same
// reasoning renderExport applies to its own switch. ModeAuto cannot reach this
// function: ResolveMode never returns it.
func modeUsesLiveRegion(mode report.Mode) bool {
	switch mode {
	case report.ModeInline, report.ModeFullscreen:
		return true
	default:
		return false
	}
}

// autoupdateUIConfig is the configuration the apply path resolves its renderer
// against, captured once by runAutoupdate.
//
// It exists because the gate is reached from buildApplyReporter, which runApply
// and runApplyAll call, and none of those three carries a *config.Config — they
// take the one sub-struct they needed. Threading the whole config down for a
// display question would change three signatures on a path this story is
// otherwise not touching, so the value is parked here instead, beside the other
// decisions runAutoupdate resolves once and hands down the same way
// (autoupdateDirs, autoupdateValidate, autoupdateValidateCfg).
//
// nil is a legal value and reads as "nothing configured". That is what makes a
// binary that never ran runAutoupdate — every test binary, for one — behave
// exactly as it did before this key existed (R3.7).
var autoupdateUIConfig *config.Config

// autoupdateUsesTUI is the `--apply` path's live-region gate, read as a boolean
// from the mode this run resolved to (R3.8).
//
// It shares resolveAutoupdateUIMode with everything else this command renders,
// which is the point: one resolution, so the report and the apply progress
// cannot disagree about which renderer this run is using.
func autoupdateUsesTUI(cfg *config.Config) bool {
	mode, err := resolveAutoupdateUIMode(cfg)
	if err != nil {
		// Unreachable in a real run: R3.9 stops an unusable --ui, BENTOO_UI or
		// ui.mode in runAutoupdate, before any package work. Debug rather than
		// Warn because the operator has already been told, as a fatal error,
		// and one run answering the same thing twice in two voices is worse
		// than a line in a log. Plain is the fallback because plain is the mode
		// that always works.
		logger.Debug("apply: the UI mode did not resolve, rendering in plain: %v", err)
		return false
	}
	return modeUsesLiveRegion(mode)
}

// manifestUsesTUI is `overlay manifest`'s live-region gate, and R3.8 itself:
// the command stops deciding on its own and reads the answer the same
// resolution hands autoupdate, so ONE setting governs both rather than an
// operator having to learn a different switch per command.
//
// It lives here, beside autoupdateUsesTUI, rather than next to its caller in
// overlay_manifest.go, so that "both callers resolve through the same path" is
// something a reader can see instead of having to take on trust.
//
// # Neither flag is passed, on purpose
//
// Flag and NoTUI stay at their zero values because `overlay manifest` registers
// neither --ui nor --no-tui. Reading autoupdateUI or autoupdateNoTUI here would
// be a cross-command leak: those hold whatever the autoupdate command was given
// and a manifest run never parses them at all. The two ENVIRONMENT opt-outs are
// a different matter — resolveUIMode folds NO_COLOR and BENTOO_NO_TUI in for
// every caller, which is exactly what keeps this identical to the
// tui.Enabled(tui.Options{}) it replaces (R3.7).
//
// A nil config reads as "nothing configured" here too; see configuredUIMode.
func manifestUsesTUI(cfg *config.Config) bool {
	mode, warning, err := resolveUIMode(uiInputs{
		Config:      configuredUIMode(cfg),
		Interactive: uiIsTerminal(),
	})
	if err != nil {
		// Unlike the apply path there is no earlier gate to have reported this
		// — `overlay manifest` has no --ui flag to reject — so this is the one
		// place an operator can learn their ui.mode is not a mode. It warns
		// rather than fails: regenerating a Manifest is not a rendering
		// question, and refusing to do it over a display key would be a worse
		// answer than doing it in plain.
		logger.Warn("%v — this run renders in plain", err)
		return false
	}

	warnUIDowngrade(warning)
	return modeUsesLiveRegion(mode)
}

// uiDowngradeReported is R3.6's "once", made explicit.
//
// The mode is resolved once per run (D4), so the sentence would reach stderr
// once even without this guard. It is here because "once" is the requirement
// and a second resolution is one refactor away — and because a swap is cheaper
// than the alternative of trusting that nobody adds one. It is an atomic rather
// than a sync.Once so that two goroutines resolving at the same time still
// produce one line, and so that a test can put it back.
var uiDowngradeReported atomic.Bool

// warnUIDowngrade puts the downgrade sentence on stderr, at most once (R3.6).
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
func warnUIDowngrade(warning string) {
	if warning == "" || uiDowngradeReported.Swap(true) {
		return
	}
	logger.Warn("%s", warning)
}

// exportFormat is the syntax an export is written in (R9.2). It is a string
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
	// exportJSON is the view model serialized, for a reader that is a program
	// (R9.4).
	exportJSON exportFormat = "json"
)

// exportFormatFor picks the export syntax from the path's extension (R9.2):
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
// selects (R9.1, R9.2, R9.3).
//
// # Every error names the path
//
// An operator cannot fix what the message does not name, and the whole class of
// failure here — a directory that does not exist, a read-only mount, a full
// disk — is fixed by acting on that one string. The failure is RETURNED rather
// than printed because R9.5 makes the export subordinate to the terminal: the
// caller reports it and renders the report anyway, and an export that decided
// on its own how loudly to fail would be deciding that for it.
//
// # The close is checked
//
// A buffered write that only fails at close is the ordinary shape of a full
// disk, and a function that ignored it would report success for a file that is
// truncated. The deferred close is the fallback for the error paths above it;
// closing twice returns os.ErrClosed, which is exactly the nothing it should be.
func writeExport(path string, r report.Report) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating the export %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	if err := renderExport(file, r, exportFormatFor(path)); err != nil {
		return fmt.Errorf("writing the export %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("completing the export %s: %w", path, err)
	}
	return nil
}

// renderExport writes r to w in one export syntax.
//
// exportPlain is the DEFAULT rather than a case of its own, in both senses of
// the word: it is the format R9.2 gives every extension that is not one of the
// two named above, and it is what a format this switch has not been taught
// about still produces — a whole report in the least demanding syntax there is,
// rather than an empty file.
func renderExport(w io.Writer, r report.Report, format exportFormat) error {
	switch format {
	case exportMarkdown:
		return render.Markdown(w, r)
	case exportJSON:
		return render.JSON(w, r)
	default:
		return render.Plain(w, r, render.Options{Width: unshortenedWidth})
	}
}

// renderCheckReportIn writes the report to the terminal through the renderer
// the resolved mode names (R2, R2.4).
//
// # It is a named function rather than a switch inside the caller
//
// R2.4 says the three modes differ in presentation and not in content. That is
// a property OF THE COMMAND, not of any one renderer: each renderer can be
// perfectly self-consistent while the command hands one of them a different
// report, or different Options, than the others. Naming the mapping is what
// lets a test drive every mode through the same call with the same report and
// compare what came out — measured at the command, which is where the defect
// this story removes actually lived.
//
// # Every mode receives the SAME report and the SAME Options
//
// Both are parameters and neither is touched here. A mode that filtered its own
// report, or quietly widened its own budget, would be the R2.4 failure arriving
// as a helpful special case; there is no branch here that could hold one.
//
// # Plain is the default, not a case
//
// A fifth mode added later renders in plain — whole, escape-free and readable
// anywhere — instead of falling through a `switch` that returns nil and prints
// nothing. It is the same reasoning renderExport applies to its own default,
// and the same one modeUsesLiveRegion states: plain is the mode that assumes
// nothing about the terminal. ModeAuto cannot arrive here, because ResolveMode
// never returns it.
//
// # The destination is os.Stdout, read at call time
//
// Inline and Fullscreen own a region of the terminal and name their own
// destination; Plain takes a writer, and it is given the same one so that all
// three modes write to a single place. Reading os.Stdout here rather than
// capturing it in a variable is what lets a test swap the descriptor and see
// what an operator would have seen.
func renderCheckReportIn(mode report.Mode, r report.Report, opts render.Options) error {
	var err error

	switch mode {
	case report.ModeInline:
		err = render.Inline(r, opts)
	case report.ModeFullscreen:
		err = render.Fullscreen(r, opts)
	default:
		err = render.Plain(os.Stdout, r, opts)
	}

	if err != nil {
		return fmt.Errorf("rendering the report in %s mode: %w", mode, err)
	}
	return nil
}

// unshortenedWidth is the line budget a PLAIN export is rendered at: a number
// no report can reach, so nothing is ever cut (R9.3).
//
// It is not a terminal width and must never be mistaken for one. render.Options
// reads a Width of 0 as "ask the device", and the device here is whichever
// terminal the operator happened to be standing at — which would make the FILE
// inherit the screen's truncation. A record missing exactly what the screen
// dropped answers no question later, and a report is kept precisely because the
// screen is gone.
//
// Markdown and JSON cannot make this mistake at all: they take no Options, and
// that absence is R9.3 stated as a signature. Plain is the one export format
// that shares a renderer with the terminal, so it is the one place the width
// has to be said out loud.
const unshortenedWidth = math.MaxInt
