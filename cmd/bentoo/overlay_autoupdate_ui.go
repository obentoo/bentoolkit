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

// configuredUIMode reads ui.mode out of a configuration that may not exist.
//
// A nil config is "nothing configured" rather than a failure — the same reading
// resolveAutoupdateDistfileDirs gives it — because a run whose config could not
// be read still has to be able to print WHY, and it cannot print anything
// without first deciding how to render it.
//
// It is one function rather than the same three lines in each caller so that
// the nil reading cannot come to differ between the two commands S044-R3.8 joins:
// "no config" and "no ui.mode" must reach ResolveMode as the same empty string
// from both, or S044-R3.7 would hold for one command and not the other.
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
// sharing it is the whole point (S044-R3.8). What genuinely must happen once is the
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
// loud and answered with plain (R3.7).
//
// The ladder below it is report.ResolveMode (the accepted set, the precedence
// and the downgrade, over pure data), then resolveUIMode (adds the environment),
// then resolveAutoupdateUIMode (adds the flags, the config key, the terminal,
// and routes the downgrade sentence). Each of those three can return an error.
// This one turns that error into a mode and a sentence, which is what every
// producer of a REPORT wants and what none of them should decide for itself.
//
// # Why an unusable value can still arrive here at all
//
// The root rejects --ui and says so eleven lines above the check it performs:
// validating the environment and the config there would make `bentoo version`
// fail on a host whose shell profile has a typo, so it deliberately looks at the
// flag alone. Measured on a binary built from HEAD, `bentoo version --ui=bogus`
// exits 1 with the sentence and `BENTOO_UI=bogus bentoo version` exits 0 in
// silence. So the error is reachable, and this is where it arrives.
//
// The SOURCE is what decides the answer, and the split is the design:
//
//   - --ui is explicit — the operator typed it for this run — so it stops the
//     run before any work, once, and the sentence comes from the ROOT rather
//     than from here: newRootCmd's PersistentPreRunE in root.go, at the
//     ResolveMode call whose comment opens "an unusable --ui stops ANY command
//     before it does work" (S046-R3.2, S046-R3.6).
//   - BENTOO_UI and ui.mode are ambient, inherited from a shell profile or a
//     config file rather than typed for this run, so failing every invocation on
//     one would break the commands that render nothing. They are refused HERE
//     instead: the render falls back to plain, the mode that always works, the
//     exit status is untouched, and the refusal is stated on stderr.
//
// What must never happen is the third option, which is what shipped: the value
// dropped and nothing said, because every producer handed this error to
// a Debug record, which sits below the default LevelInfo and reaches no one.
//
// The check producer shipped a FOURTH answer, worse than all three, and 12.3
// removed it. runAutoupdate resolved the mode before any package work and exited
// 1 on the error, citing a rule about the --ui flag; measured on a seeded
// overlay, `BENTOO_UI=bogus bentoo overlay autoupdate --check` lost not a
// sentence but the whole report, and on a run already failing for a reason of
// its own it replaced the operator's real diagnostic with one about a display
// key that could not have caused it.
//
// # The sentence carries three facts, and only two of them existed
//
// parseMode's message already names the source and the value it refused. The
// third — the mode used INSTEAD — is what the clause here adds, because listing
// plain among the accepted values is not the same as saying the report below was
// rendered in it.
//
// Warn rather than Debug, on the same stream and at the same level as
// warnUIDowngrade, and the two must stay tellable apart: both end in plain, but
// a downgrade is a device limit with nothing to fix, while this is a typo that
// costs the operator every run until they find it. The source and the value are
// what separate them, and they are exactly the two facts a downgrade can never
// carry.
//
// # It is ONE function and not four identical lines per producer
//
// The two producers that grew a report in this story carried the same block and
// the same doc comment, so the cheapest fix touched one of them and left the
// other as silent as before — which is the failure
// TestAmbientModeRefusalReachesTheSnapshotProducerToo exists to catch. R3.7 is a
// rule about reports, not about one command's file, so a producer inherits it by
// calling this rather than by being reviewed for it.
//
// EVERY producer of a report calls it. That is the rule, and it is stated as a
// rule rather than as a count on purpose: this comment read "All THREE producers
// call it now" and named them, while presentValidateReport
// (overlay_validate_report.go) built the same report.Run through the same
// envelope and resolved no mode at all. Measured on one fixture, same overlay,
// same BENTOO_UI=bogus, all exit 0 — `overlay manifest --dry-run` stated the
// refusal once, `overlay validate` stated it zero times, with and without
// --json. A number in a doc comment goes stale the moment a fourth call site
// lands and says nothing when it does, which is the same silence R3.7 forbids,
// one level up.
//
// `overlay validate` calls it for the SENTENCE alone: its human half is a
// printer of its own until story 047, so the mode it gets back has no consumer
// there and is dropped at the call. That is the rule holding rather than an
// exception to it — R3.7 is about what the operator is told, and a command with
// one renderer is already rendering in the mode it would have fallen back to.
//
// The check producer is why the rule was worth spending a function on. Its own
// file called this shape "already correct" while the run died over the same key
// on the way in; nothing about that file said otherwise, and only asking here
// rather than deciding there could have caught it.
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

// autoupdateUsesTUI is the `--apply` path's live-region gate, read as a boolean
// from the mode this run resolved to (S046-R3.3).
//
// The citation is deliberately NOT S044-R3.8: that requirement is about
// `overlay manifest` resolving its presentation from the shared mode, and this
// gate is neither `overlay manifest` nor a report. What it obeys is the rule
// that the mode is resolved ONCE per run and every consumer reads that one
// answer.
//
// It shares resolveAutoupdateUIMode with everything else this command renders,
// which is the point: one resolution, so the report and the apply progress
// cannot disagree about which renderer this run is using.
func (ar *autoupdateRun) autoupdateUsesTUI() bool {
	mode, err := resolveAutoupdateUIMode(ar.log(), ar.uiConfig, ar.opts.noTUI, ar.deps.uiIsTerminal)
	if err != nil {
		// Reachable, and only from the two AMBIENT sources. S044-R3.9 stops an
		// unusable --ui and says nothing about the other two; the root has
		// enforced that rule for all 30 commands since Task 4, and the gate in
		// runAutoupdate has stopped exiting on what it never governed. So what
		// arrives here is a BENTOO_UI or a ui.mode that does not name a mode,
		// which S046-R3.7 answers with plain rather than with a failure.
		//
		// Debug rather than Warn, and that is a decision about WHO SPEAKS. R3.7
		// is a rule about REPORTS; this is a live-region boolean on the apply
		// path, which streams a worker's progress instead of drawing one (see
		// modeUsesLiveRegion). Where the same command produces a report,
		// presentCheckReport states the refusal once through reportModeOrPlain,
		// naming the source, the value and the mode used instead — and a second
		// sentence from here would answer one typo in two voices, which is the
		// duplication R3.6 forbids. On a path that draws no report the refusal
		// is therefore recorded rather than announced.
		//
		// False is the fallback for the same reason plain is: it is the answer
		// that assumes nothing about the terminal.
		ar.log().Debug("apply: the UI mode did not resolve, rendering in plain", "err", err)
		return false
	}
	return modeUsesLiveRegion(mode)
}

// manifestUsesTUI is `overlay manifest`'s live-region gate, and S044-R3.8 itself:
// the command stops deciding on its own and reads the answer the same
// resolution hands autoupdate, so ONE setting governs both rather than an
// operator having to learn a different switch per command.
//
// It lives here, beside autoupdateUsesTUI, rather than next to its caller in
// overlay_manifest.go, so that "both callers resolve through the same path" is
// something a reader can see instead of having to take on trust.
//
// # One of the two flags is passed now; the other still is not
//
// This comment used to say both stayed at their zero values because `overlay
// manifest` registers neither --ui nor --no-tui. Half of that premise expired
// two sub-tasks after it was written: sub-task 4.3 moved --ui onto the ROOT's
// persistent flags, so a manifest run DOES parse it, and autoupdateUI holds
// what THIS run was given rather than another command's answer.
//
// Leaving Flag at zero meant one run resolving its mode TWICE from different
// inputs. The report obeyed --ui=plain and the live region did not, so on a
// terminal an operator who asked for the one mode whose help text promises "no
// escape sequence at all" got them anyway (S046-R3.3). Off a terminal the
// Interactive input degrades the mode regardless and the two answers agreed by
// accident, which is why nothing here failed for two sub-tasks.
//
// NoTUI stays at zero, for the half that did NOT expire. --no-tui is declared
// on `overlay autoupdate` alone, by the decision recorded in `func newRootCmd`
// in root.go — the comment beginning "--no-tui deliberately stays on
// autoupdate", beside the persistent-flag registration — so the --no-tui option
// holds whatever THAT command was given. Reading it here
// would still be exactly the cross-command leak this comment has always
// described.
//
// The two ENVIRONMENT opt-outs are a different matter again — resolveUIMode
// folds NO_COLOR and BENTOO_NO_TUI in for every caller, which is what keeps
// this identical to the tui.Enabled(tui.Options{}) it replaces (S046-R3.7).
//
// A nil config reads as "nothing configured" here too; see configuredUIMode.
func manifestUsesTUI(log *slog.Logger, cfg *config.Config, isTerminal func() bool) bool {
	mode, warning, err := resolveUIMode(uiInputs{
		Flag:        autoupdateUI,
		Config:      configuredUIMode(cfg),
		Interactive: isTerminal(),
	})
	if err != nil {
		// What reaches here is an AMBIENT mode alone, and only one that
		// nothing outranks. The root has rejected an unusable --ui for every
		// command since Task 4, so the flag wired in above cannot arrive
		// unusable; a BENTOO_UI or a ui.mode that does not name a mode can, and
		// since sub-task 15.1 one that a higher-precedence source overrules
		// comes back as a warning instead. It records rather than fails:
		// regenerating a Manifest is not a rendering question, and refusing to
		// do it over a display key would be a worse answer than doing it in
		// plain (S046-R3.7).
		//
		// Debug rather than Warn, for the reason autoupdateUsesTUI states above
		// it: this is a live-region boolean, not a report, and `overlay
		// manifest` DOES produce a report — presentManifestReport resolves
		// through reportModeOrPlain, which states the refusal naming the source,
		// the value and the mode used instead. A second sentence from here
		// answers one typo in two voices, which is the duplication R3.6 forbids.
		//
		// This comment used to claim to be "the one place an operator learns
		// it". That was true when it was written and stopped being true at
		// sub-task 12.1, which routed `overlay manifest` through
		// reportModeOrPlain — and a comment that is confidently false is worse
		// than none in a package this comment-dense, because a reader has more
		// reason to believe it.
		//
		// WHY FIVE AUDITS READ THIS AS SINGLE-VOICED: every one of them measured
		// with `--dry-run`, and chooseManifestReporter returns tui.Noop() before
		// this gate is reached on a dry run. The one flag used to make the
		// measurement cheap is the one flag that hides the second voice, so the
		// evidence was real and the conclusion was not. TestManifestSingleVoice
		// asserts on a REAL run for exactly that reason.
		log.Debug("manifest: the UI mode did not resolve, rendering in plain", "err", err)
		return false
	}

	warnUIDowngrade(log, warning)
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
func warnUIDowngrade(log *slog.Logger, warning string) {
	if warning == "" || uiDowngradeReported.Swap(true) {
		return
	}
	log.Warn("the UI mode was downgraded", "reason", warning)
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
// exportPlain is the DEFAULT rather than a case of its own, in both senses of
// the word: it is the format R9.2 gives every extension that is not one of the
// two named above, and it is what a format this switch has not been taught
// about still produces — a whole report in the least demanding syntax there is,
// rather than an empty file.
//
// # The sections are built ONCE, above the switch
//
// It is the same move presentCheckReport makes for the three screen modes, for
// the same reason: it turns "the export formats differ in syntax and not in
// content" into a fact about this call rather than a promise about three
// renderers. A branch that built its own could quietly ask for less, which is
// exactly what the plain export did until story 046's sub-task 4.5 — it counted
// the packages found up to date where Markdown listed them, and an export whose
// completeness depends on the extension is a rule the operator has to know and
// nobody wrote down (S046-R3.4).
//
// JSON does not consume them, and building them on that path costs a slice of
// rows this function then drops. That is the price of the invariant above, and
// it is worth it: render.JSON serializes the whole run, so the completeness it
// arrives at is the same one from the other direction, and there is nothing in
// its signature to shorten either.
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
// arrive through (R9.3, R2.4, S046-R3.4).
//
// They are named rather than written as bare literals because
// `r.Sections(report.SectionOptions{true, false})` says only that something was
// on and something else was off. exportContent lives in
// overlay_autoupdate_check.go, beside the screen's own options, because building
// a report.SectionOptions means writing down the field that omits the plan — and
// a source-text guard over this file forbids that name here, precisely so an
// export can never acquire one. These two constants are already what
// report.Payload.Sections is asked, so sub-task 3.1's rename leaves them
// untouched.
//
// There were THREE until story 046's sub-task 4.5. The third, countTheUpToDate,
// was everyScannedPackage's opposite and was what the PLAIN export asked for —
// a disagreement inherited verbatim from the renderer that path replaced, and
// deliberately left open at the time. S046-R3.4 answers it: the file carries the
// complete report whatever the terminal was told, so both formats now ask for
// everyScannedPackage and the constant that shortened one of them is gone.
const (
	// everyScannedPackage lists every package the run looked at, instead of
	// counting the ones found up to date. It is what EVERY export asks for: a
	// record is kept precisely because the terminal is gone, and one that named
	// only the interesting packages could not answer "was this one checked at
	// all".
	everyScannedPackage = true

	// keepThePlan states the validation-plan section, whatever the screen was
	// told to do with it (R2.4).
	//
	// A report is kept precisely BECAUSE the terminal is gone, so a record
	// missing the plan the terminal had already shown answers no question later
	// — the plan is where a package's reason is stated at all (R7.2), so
	// dropping it from a file would not shorten the record, it would empty it.
	// A named false says that at the call site; a bare false would only say
	// something was off.
	keepThePlan = false
)

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
// # Every mode receives the SAME sections and the SAME Options
//
// Both are parameters and neither is touched here — and the first is now a
// finished []report.Section rather than a report each branch converts for
// itself, so "the modes differ in presentation and not in content" is settled
// before this function is entered. A mode that filtered its own report, or
// quietly widened its own budget, would be the R2.4 failure arriving as a
// helpful special case; there is no branch here that could hold one.
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
func renderCheckReportIn(mode report.Mode, blocks []report.Section, opts render.Options) error {
	var err error

	// All three renderers take sections rather than a report (story 046, Task
	// 2), and the caller built them once. Nothing below knows what a package is,
	// so a mode cannot decide to say something the others do not.
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
