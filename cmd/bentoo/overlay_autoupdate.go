package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/common/tui"
	"github.com/spf13/cobra"
)

var (
	// autoupdateCheck triggers version checking
	autoupdateCheck bool
	// autoupdateList triggers listing pending updates
	autoupdateList bool
	// autoupdateApply specifies package to apply update
	autoupdateApply string
	// autoupdateForce ignores cache when checking
	autoupdateForce bool
	// autoupdateCompile runs compile test after apply
	autoupdateCompile bool
	// autoupdateClean removes the old ebuild after a successful apply, keeping
	// only the newly created version
	autoupdateClean bool
	// autoupdateConcurrency bounds parallel version checks and the --apply all
	// worker pool (range [1,100])
	autoupdateConcurrency int
	// autoupdateTimeout overrides the per-request HTTP timeout in seconds
	// (0 = use config autoupdate.http_timeout, default 30)
	autoupdateTimeout int
	// autoupdateOnly restricts --check to a package type ("bin" or "source")
	autoupdateOnly string
	// autoupdateReviveList reports disabled (orphaned) entries whose upstream
	// version is newer than ::gentoo — a passive scan, no mutation
	autoupdateReviveList bool
	// autoupdateRevive performs the full revive for a single "category/pkg" or
	// "all": seed from ::gentoo, re-enable, then bump to the upstream version
	autoupdateRevive string
	// autoupdateRevivable, with --check, also reports revivable orphans (disabled
	// entries absent from the overlay whose upstream is newer than ::gentoo) in
	// the same pass — read-only, no mutation
	autoupdateRevivable bool
	// autoupdateNoTUI opts out of the live TUI during --apply, streaming plain
	// rate-limited output instead. It is one of the gate's opt-outs (alongside
	// NO_COLOR and BENTOO_NO_TUI); see tuiEnabledForApply (R2.1, R2.2).
	autoupdateNoTUI bool
)

var autoupdateCmd = &cobra.Command{
	Use:   "autoupdate [package]",
	Short: "Check and apply ebuild version updates",
	Long: `Automatically check upstream sources for new versions and apply updates.

Examples:
  bentoo overlay autoupdate --check              Check all packages for updates
  bentoo overlay autoupdate --check net-misc/foo Check specific package
  bentoo overlay autoupdate --check --force      Check ignoring cache
  bentoo overlay autoupdate --check --only source Check only source packages
  bentoo overlay autoupdate --check --only bin    Check only binary packages
  bentoo overlay autoupdate --list               List pending updates
  bentoo overlay autoupdate --apply net-misc/foo Apply update for package
  bentoo overlay autoupdate --apply all          Apply all pending updates
  bentoo overlay autoupdate --apply net-misc/foo --compile  Apply and compile test
  bentoo overlay autoupdate --apply net-misc/foo --clean    Apply and remove the old ebuild
  bentoo overlay autoupdate --revive-list         List orphaned packages with a newer upstream
  bentoo overlay autoupdate --check --revivable   Check active packages AND report revivable orphans
  bentoo overlay autoupdate --revive net-misc/foo Revive an orphan: seed from ::gentoo and bump
  bentoo overlay autoupdate --revive all          Revive every revivable orphan`,
	Run: runAutoupdate,
}

func init() {
	autoupdateCmd.Flags().BoolVar(&autoupdateCheck, "check", false, "Check for updates")
	autoupdateCmd.Flags().BoolVar(&autoupdateList, "list", false, "List pending updates")
	autoupdateCmd.Flags().StringVar(&autoupdateApply, "apply", "", "Apply update for specified package, or \"all\" for every pending update")
	autoupdateCmd.Flags().BoolVar(&autoupdateForce, "force", false, "Ignore cache when checking")
	autoupdateCmd.Flags().BoolVar(&autoupdateCompile, "compile", false, "Run compile test after apply")
	autoupdateCmd.Flags().BoolVarP(&autoupdateClean, "clean", "c", false, "Remove the old ebuild after a successful apply, keeping only the new version")
	autoupdateCmd.Flags().IntVar(&autoupdateConcurrency, "concurrency", autoupdate.DefaultConcurrency, "max parallel checks/applies (1-100)")
	autoupdateCmd.Flags().IntVar(&autoupdateTimeout, "timeout", 0, "per-request HTTP timeout in seconds for --check (0 = use config autoupdate.http_timeout, default 30)")
	autoupdateCmd.Flags().StringVar(&autoupdateOnly, "only", "", "Restrict --check to packages of this type: \"bin\" or \"source\"")
	autoupdateCmd.Flags().BoolVar(&autoupdateReviveList, "revive-list", false, "List disabled (orphaned) packages whose upstream is newer than ::gentoo")
	autoupdateCmd.Flags().StringVar(&autoupdateRevive, "revive", "", "Revive an orphaned package by seeding from ::gentoo and bumping it, or \"all\" for every revivable orphan")
	autoupdateCmd.Flags().BoolVar(&autoupdateRevivable, "revivable", false, "With --check, also report revivable orphans (disabled+absent, upstream newer than ::gentoo) in the same pass")
	autoupdateCmd.Flags().BoolVar(&autoupdateNoTUI, "no-tui", false, "Disable the live TUI; stream plain output (also honors NO_COLOR and BENTOO_NO_TUI)")

	overlayCmd.AddCommand(autoupdateCmd)
}

// tuiEnabledForApply is the apply-path gate: it defers entirely to tui.Enabled
// (the single decision point, AD7), feeding it the --no-tui flag. Enabled also
// honors NO_COLOR / BENTOO_NO_TUI and requires stdout to be a TTY, so a piped or
// opted-out run selects the plain backend (R2.1, R2.2).
func tuiEnabledForApply() bool {
	return tui.Enabled(tui.Options{NoTUI: autoupdateNoTUI})
}

// buildApplyReporter selects the apply backend per the gate (tuiEnabledForApply)
// and returns a tui.Reporter, the extra ApplierOptions that wire it into the
// Applier, and a finish func to run once the applies complete.
//
// Plain branch (non-TTY / opt-out): a rate-limited plainReporter streams the tail
// to stderr with NO ANSI (R2.2/R2.3); finish closes the batch.
//
// TUI branch: a Bubble Tea Program is started and bound to ctx so Ctrl-C invokes
// cancel — cancelling the apply context, which kills the in-flight child
// (WithApplierContext) and triggers the existing orphan rollback (R5.1/R5.2). The
// extra options also route the in-UI y/n confirm (R4.2) and release the terminal
// for the compile step's sudo/doas prompt while teeing the child's output to a
// capture buffer the failure path still logs (R4.1). finish closes the batch,
// stops the program, and waits for it to exit so the terminal is restored before
// the summary is printed.
func buildApplyReporter(ctx context.Context, cancel context.CancelFunc, total int) (tui.Reporter, []autoupdate.ApplierOption, func()) {
	if !tuiEnabledForApply() {
		r := tui.NewPlainReporter(os.Stderr, time.Second)
		r.BatchStart(total)
		return r, []autoupdate.ApplierOption{autoupdate.WithApplierReporter(r)}, func() { r.BatchDone("") }
	}

	prog, r := tui.New(ctx, cancel, os.Stdout, os.Stdin)
	prog.Start()
	r.BatchStart(total)
	extra := []autoupdate.ApplierOption{
		autoupdate.WithApplierReporter(r),
		// In-UI y/n confirmation rendered by the model instead of reading stdin
		// behind the program (R4.2).
		autoupdate.WithConfirmFunc(prog.Confirm),
		// Hand the real terminal to the compile child so a sudo/doas password
		// prompt is visible (R4.1), while teeing its stdout+stderr to a capture
		// buffer the failure path still saves (the applier's Output: %s contract).
		autoupdate.WithApplierRunAttached(func(cmd *exec.Cmd) ([]byte, error) {
			var buf bytes.Buffer
			cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
			cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
			cmd.Stdin = os.Stdin
			err := tui.RunAttached(prog, cmd)
			return buf.Bytes(), err
		}),
	}
	finish := func() {
		r.BatchDone("")
		prog.Stop()
		// Wait for the program goroutine to exit so the terminal is restored
		// before the summary prints. A context-cancel (Ctrl-C) outcome surfaces as
		// tea.ErrProgramKilled here — the EXPECTED cancellation result, not a
		// failure — so it is intentionally not escalated (R2.4); the manual TTY
		// gate covers a program that never started cleanly.
		if err := prog.Wait(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
			logger.Debug("apply: live TUI program exited with error: %v", err)
		}
	}
	return r, extra, finish
}

func runAutoupdate(cmd *cobra.Command, args []string) {
	const (
		minConcurrency = 1
		maxConcurrency = 100
	)

	// Validate --concurrency BEFORE any package work so a bad value fails fast
	// with a clear message and a non-zero exit (R4.2). The accepted range
	// mirrors autoupdate.WithConcurrency's [1, 100] bound.
	if autoupdateConcurrency < minConcurrency || autoupdateConcurrency > maxConcurrency {
		logger.Error("--concurrency must be in range [%d, %d], got %d", minConcurrency, maxConcurrency, autoupdateConcurrency)
		osExit(1)
		return
	}

	// Validate --timeout up front: a negative value is a typo, and 0 is the
	// sentinel for "use the configured/default value".
	if autoupdateTimeout < 0 {
		logger.Error("--timeout must be >= 0 seconds, got %d", autoupdateTimeout)
		osExit(1)
		return
	}

	// Validate --only up front so a typo fails fast rather than silently
	// checking everything. Only "bin"/"source" (or unset) are accepted.
	switch autoupdateOnly {
	case "", "bin", "source":
		// valid
	default:
		logger.Error("--only must be \"bin\" or \"source\", got %q", autoupdateOnly)
		osExit(1)
		return
	}

	appCtx, err := loadAppContextNoValidation()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
		return
	}

	overlayPath := appCtx.OverlayPath

	// Determine config directory for autoupdate
	home, err := os.UserHomeDir()
	if err != nil {
		logger.Error("failed to get home directory: %v", err)
		osExit(1)
		return
	}
	configDir := filepath.Join(home, ".config", "bentoo", "autoupdate")

	// Wire SIGINT/SIGTERM into a context so an in-flight check cancels cleanly.
	// The Checker threads this context through every outbound HTTP/LLM call, so
	// the run aborts within ~2 s of a signal (R3.1). See signalContext for the
	// OQ-1 note on why cmd.Context() alone is not signal-aware.
	runCtx, stop := signalContext(cmd.Context())
	defer stop()

	// Compute the autoupdate cache TTL from config (R2.1, R2.2). GetCacheTTL
	// returns the user-configured value when positive, otherwise the
	// 3600-second default — so the duration here is always positive and safe
	// to pass to WithCacheTTL inside runCheck.
	cacheTTL := time.Duration(appCtx.Config.Autoupdate.GetCacheTTL()) * time.Second

	// Handle different modes
	switch {
	case autoupdateCheck:
		runCheck(runCtx, overlayPath, configDir, args, cacheTTL, appCtx.Config, appCtx.Config.Autoupdate.LLM)
	case autoupdateList:
		runList(configDir)
	case autoupdateApply == "all":
		runApplyAll(runCtx, overlayPath, configDir, appCtx.Config.Autoupdate.LLM)
	case autoupdateApply != "":
		runApply(runCtx, overlayPath, configDir, autoupdateApply, appCtx.Config.Autoupdate.LLM)
	case autoupdateReviveList:
		runReviveList(runCtx, overlayPath, configDir, cacheTTL, appCtx.Config, appCtx.Config.Autoupdate.LLM)
	case autoupdateRevive != "":
		runRevive(runCtx, overlayPath, configDir, autoupdateRevive, cacheTTL, appCtx.Config, appCtx.Config.Autoupdate.LLM)
	default:
		// No flag specified, show help
		cmd.Help() //nolint:errcheck // help output failure is not actionable
	}
}

// resolveHTTPTimeout resolves the per-request HTTP timeout for --check and the
// revive flows: the --timeout flag when positive, otherwise
// autoupdate.http_timeout from config (which itself falls back to a 30s default).
// The result is always a positive duration, safe to pass to WithHTTPRequestTimeout.
func resolveHTTPTimeout(cfg *config.Config) time.Duration {
	secs := autoupdateTimeout
	if secs <= 0 {
		secs = cfg.Autoupdate.GetHTTPTimeout()
	}
	return time.Duration(secs) * time.Second
}

// runCheck handles the --check flag. cacheTTL must be a positive duration —
// the caller resolves it from AutoupdateConfig.GetCacheTTL, which guarantees a
// positive value (R2.1, R2.2). A non-positive cacheTTL is treated as "use the
// Checker default" and the WithCacheTTL option is skipped, since WithCacheTTL
// rejects non-positive values at construction time.
func runCheck(ctx context.Context, overlayPath, configDir string, args []string, cacheTTL time.Duration, cfg *config.Config, llmCfg config.LLMConfig) {
	opts := []autoupdate.CheckerOption{
		autoupdate.WithConfigDir(configDir),
		autoupdate.WithContext(ctx),
		autoupdate.WithConcurrency(autoupdateConcurrency),
		// Per-request HTTP timeout (flag > config > 30s default). The Checker
		// derives the larger per-operation budget so the retry attempts fit.
		autoupdate.WithHTTPRequestTimeout(resolveHTTPTimeout(cfg)),
		// Restrict the batch to a package type when --only is set; empty is a
		// no-op (checks every package). Ignored on the single-package path.
		autoupdate.WithTypeFilter(autoupdateOnly),
		// NewChecker authenticates api.github.com itself: it resolves the token
		// from GITHUB_TOKEN/GH_TOKEN via the secrets chain (github.ResolveToken).
		// Tune per-host HTTP rate limits: GitHub ~10/s and GitLab ~3/s (the two
		// hosts that dominate packages.toml), every other host at the conservative
		// 6s default. Without this the uniform 1-req/6s-per-host limiter serialises
		// the ~220 GitHub/GitLab packages, making a large --concurrency pointless.
		autoupdate.WithRateLimiter(autoupdate.NewRateLimiter(autoupdate.WithTunedHostPolicies())),
	}
	if cacheTTL > 0 {
		opts = append(opts, autoupdate.WithCacheTTL(cacheTTL))
	}

	// Wire an LLM provider into the check path (R5.2). newConfiguredLLMProvider
	// returns (nil, nil) when no provider is configured, (provider, nil) on
	// success, and (typed-nil, err) on a construction failure. The error must be
	// the PRIMARY guard: a failed constructor boxes a nil concrete pointer into a
	// NON-nil interface, so we wire WithLLMClient only on err==nil AND p!=nil —
	// never a typed-nil (which would make fetchUpstreamVersion dereference a nil
	// receiver). On failure we Warn and continue; --check still runs, skipping LLM
	// extraction. WithLLMProviderConfigured records that a provider WAS requested
	// (provider != "") so the Checker suppresses its "unused llm_prompt" Warn
	// (R5.3) and we avoid a double-warn with the failure line just below.
	if p, err := newConfiguredLLMProvider(llmCfg); err != nil {
		logger.Warn("LLM provider %q unavailable; --check will skip LLM version extraction: %v", llmCfg.Provider, err)
	} else if p != nil {
		opts = append(opts, autoupdate.WithLLMClient(p))
	}
	opts = append(opts, autoupdate.WithLLMProviderConfigured(llmCfg.Provider != ""))

	// Progress feedback: CheckAll fans out concurrently and otherwise prints
	// nothing until the final table, so show a live [pct%] done/total counter on
	// a single self-rewriting line (mirrors `overlay compare`). The callback is
	// driven by CheckAll's atomic counter, so the count is monotonic even though
	// it fires from many goroutines. Suppressed under --quiet; harmless on the
	// single-package path (CheckPackage never fires it).
	if !quiet {
		opts = append(opts, autoupdate.WithProgressCallback(func(done, total uint64) {
			percent := uint64(0)
			if total > 0 {
				percent = (done * 100) / total
			}
			fmt.Printf("\r  Checking: [%3d%%] %d/%d", percent, done, total)
		}))
	}

	// Capture the fully-assembled option set so the story-014 registry-fix
	// re-check can build a FRESH Checker that reloads packages.toml after an agent
	// edit (AD4: CheckPackage reads the config cached at NewChecker time, so there
	// is no in-place reload — a new Checker is required).
	newChecker := func() (*autoupdate.Checker, error) {
		return autoupdate.NewChecker(overlayPath, opts...)
	}

	checker, err := newChecker()
	if err != nil {
		logger.Error("failed to initialize checker: %v", err)
		osExit(1)
		return
	}

	if len(args) > 0 {
		// Check specific package
		pkg := args[0]
		// ctx is threaded into the Checker via WithContext above, so every
		// outbound request observes it; CheckPackage takes no ctx parameter.
		result, err := checker.CheckPackage(pkg, autoupdateForce) //nolint:contextcheck // ctx is injected via autoupdate.WithContext
		if err != nil {
			// A removed ebuild is not a hard error: auto-disable the orphaned
			// entry and report it as info so repeated runs stay quiet.
			if errors.Is(err, autoupdate.ErrNoEbuildFound) {
				if derr := checker.DisableOrphans([]string{pkg}); derr != nil {
					logger.Warn("failed to disable orphaned package %s: %v", pkg, derr)
				}
				logger.Info("%s has no ebuild in the overlay — disabled in packages.toml", pkg)
				return
			}
			logger.Error("failed to check package %s: %v", pkg, err)
			osExit(1)
			return
		}
		displayCheckResults([]autoupdate.CheckResult{*result})
		return
	}

	// Check all packages. CheckAll never returns a fatal error: every
	// per-package failure is captured in the BatchResult. ctx is threaded
	// into the Checker via WithContext above; CheckAll takes no ctx parameter.
	result := checker.CheckAll(autoupdateForce) //nolint:contextcheck // ctx is injected via autoupdate.WithContext

	// Clear the progress line before rendering results so the counter does not
	// bleed into the table. Mirrors `overlay compare`'s clear step.
	if !quiet {
		fmt.Print("\r                                        \r")
	}

	// Display the successfully checked packages.
	displayCheckResults(result.Items)

	// Emit one stderr line per per-package failure. FormatFailures is called
	// only after CheckAll has fully completed, so the output is deterministic.
	if result.HasFailures() {
		result.FormatFailures(os.Stderr)
	}

	// Offer an interactive LLM registry repair for the packages that failed
	// upstream-version extraction (story 014). Gated to a usable claude-code fixer
	// AND an interactive stdin. newConfiguredRegistryFixer returns a TRUE nil
	// interface for a non-claude provider; a construction error Warns and is
	// treated as absent — never box a nil pointer (AD9). When the gate is false
	// (non-claude provider, no claude CLI, or non-TTY stdin) the output and exit
	// code below are exactly as before this story (R7.x / R10.1).
	fixer, ferr := newConfiguredRegistryFixer(llmCfg)
	if ferr != nil {
		logger.Warn("LLM registry fixer unavailable; --check will not offer registry repair: %v", ferr)
		fixer = nil
	}
	if fixer != nil && stdinIsTerminal() {
		if perr := promptRegistryFixes(ctx, overlayPath, fixer, result.Failures, os.Stdin, newChecker); perr != nil {
			logger.Warn("registry-fix prompt ended with an error: %v", perr)
		}
	}

	// --revivable: in the same pass, also scan the disabled+absent (orphaned)
	// entries and report those an autoupdate could revive (upstream newer than
	// ::gentoo), reusing the checker --check already built. Read-only and
	// best-effort — it never changes the check's exit code.
	if autoupdateRevivable {
		//nolint:contextcheck // ctx is already injected into checker via autoupdate.WithContext above
		reportRevivableOrphans(checker, cfg)
	}

	// Exit with the contract-defined code: 0 all-ok, 1 partial, 2 total fail.
	osExit(result.ExitCode())
}

// stdinIsTerminal reports whether standard input is an interactive terminal (a
// character device) rather than a pipe, regular file, or /dev/null. The
// story-014 registry-fix prompt is shown only when this is true, so a piped or
// CI run of --check keeps its existing non-interactive output and exit code
// (R7.3, AD8). output.IsTerminal probes stdout; this probes stdin specifically.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// reportRevivableOrphans is the --check --revivable add-on: it resolves the
// ::gentoo provider and appends the revivable-orphan report to a --check run. It
// is read-only and best-effort — a provider-resolution failure warns and returns
// without affecting the check's exit code. checker is the one --check already
// built, so its loaded packages.toml and token wiring are reused.
func reportRevivableOrphans(checker *autoupdate.Checker, cfg *config.Config) {
	prov, err := resolveGentooProviderFn(cfg)
	if err != nil {
		logger.Warn("revivable-orphan scan skipped: %v", err)
		return
	}
	defer prov.Close() //nolint:errcheck

	candidates, ferr := checker.FindRevivableOrphans(prov) //nolint:contextcheck // ctx is injected via autoupdate.WithContext
	if ferr != nil {
		logger.Warn("revivable-orphan scan completed with soft errors: %v", ferr)
	}
	displayReviveCandidates(candidates)
}

// displayCheckResults formats and displays check results
func displayCheckResults(results []autoupdate.CheckResult) {
	if len(results) == 0 {
		logger.Info("No packages configured for autoupdate")
		return
	}

	var updatesFound int
	var errorsFound int
	var warningsFound int
	var disabledFound int
	var srcCount int
	var binCount int

	fmt.Println()
	output.Header.Println("Version Check Results")
	fmt.Println()

	for _, r := range results {
		tag := typeTag(r.Type)
		switch r.Type {
		case "bin":
			binCount++
		case "source":
			srcCount++
		}

		if r.Orphaned {
			disabledFound++
			output.Warning.Printf("  %s%s: no ebuild in overlay — disabled in packages.toml\n", tag, r.Package)
			continue
		}

		if r.Error != nil {
			errorsFound++
			output.Error.Printf("  %s%s: %v\n", tag, r.Package, r.Error)
			continue
		}

		if r.NotComparable {
			warningsFound++
			output.Warning.Printf("  %s%s: %q not comparable to current %s (check parser config)\n",
				tag, r.Package, r.UpstreamVersion, r.CurrentVersion)
			continue
		}

		if r.HasUpdate {
			updatesFound++
			cacheIndicator := ""
			if r.FromCache {
				cacheIndicator = output.Sprintf(output.Dim, " (cached)")
			}
			output.Success.Printf("  %s%s: %s → %s%s\n",
				tag, r.Package, r.CurrentVersion, r.UpstreamVersion, cacheIndicator)
		} else {
			output.Dim.Printf("  %s%s: %s (up to date)\n", tag, r.Package, r.CurrentVersion)
		}
	}

	fmt.Println()
	if updatesFound > 0 {
		output.Info.Printf("Found %d update(s) available\n", updatesFound)
		output.Info.Println("Use 'bentoo overlay autoupdate --list' to see pending updates")
	} else if warningsFound == 0 && errorsFound == 0 && disabledFound == 0 {
		output.Success.Println("All packages are up to date")
	}

	if disabledFound > 0 {
		output.Warning.Printf("%d package(s) had no ebuild and were disabled (enabled = false)\n", disabledFound)
	}

	if warningsFound > 0 {
		output.Warning.Printf("%d package(s) had non-comparable upstream versions\n", warningsFound)
	}

	if errorsFound > 0 {
		output.Warning.Printf("%d package(s) had errors\n", errorsFound)
	}

	output.Dim.Printf("Checked %d source, %d bin\n", srcCount, binCount)
}

// typeTag renders a short, dim prefix marking a package's resolved type for the
// check report ("[bin] " / "[src] "). An unknown/empty type yields no tag so
// the line layout is unchanged when classification was unavailable.
func typeTag(t string) string {
	switch t {
	case "bin":
		return output.Sprintf(output.Dim, "[bin] ")
	case "source":
		return output.Sprintf(output.Dim, "[src] ")
	default:
		return ""
	}
}

// runList handles the --list flag
func runList(configDir string) {
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		logger.Error("failed to load pending list: %v", err)
		osExit(1)
	}

	updates := pending.List()
	displayPendingUpdates(updates)
}

// displayPendingUpdates formats and displays pending updates
func displayPendingUpdates(updates []autoupdate.PendingUpdate) {
	if len(updates) == 0 {
		logger.Info("No pending updates")
		return
	}

	fmt.Println()
	output.Header.Println("Pending Updates")
	fmt.Println()

	for _, u := range updates {
		statusColor := getStatusColor(u.Status)
		statusStr := output.Sprintf(statusColor, "[%s]", u.Status)

		output.Package.Printf("  %s\n", u.Package)
		fmt.Printf("    Version: %s → %s\n", u.CurrentVersion, u.NewVersion)
		fmt.Printf("    Status:  %s\n", statusStr)
		if u.Error != "" {
			output.Error.Printf("    Error:   %s\n", u.Error)
		}
		fmt.Printf("    Detected: %s\n", u.DetectedAt.Format("2006-01-02 15:04:05"))
		fmt.Println()
	}

	output.Info.Printf("Total: %d pending update(s)\n", len(updates))
	output.Info.Println("Use 'bentoo overlay autoupdate --apply <package>' to apply an update")
	output.Info.Println("Or 'bentoo overlay autoupdate --apply all' to apply every pending update")
}

// getStatusColor returns the appropriate color for an update status
func getStatusColor(status autoupdate.UpdateStatus) *color.Color {
	switch status {
	case autoupdate.StatusPending:
		return output.Warning
	case autoupdate.StatusValidated:
		return output.Success
	case autoupdate.StatusFailed:
		return output.Error
	case autoupdate.StatusApplied:
		return output.Info
	default:
		return output.Dim
	}
}

// loadPackagesConfigForApply loads the overlay's packages.toml so the applier
// can honour any [meta] authenticated-fetch instructions. It is best-effort: a
// missing or unparseable config is not fatal to --apply (only serial-gated
// packages need it), so it logs a debug note and returns nil, leaving the
// normal pkgdev-from-SRC_URI path intact for every package.
func loadPackagesConfigForApply(overlayPath string) *autoupdate.PackagesConfig {
	cfg, err := autoupdate.LoadPackagesConfig(overlayPath)
	if err != nil {
		logger.Debug("apply: no usable packages.toml (%v); authenticated fetch disabled", err)
		return nil
	}
	return cfg
}

// applierFixerOption builds the optional LLM manifest-fixer option for --apply.
// The fixer is wired automatically whenever the configured provider supports
// agentic file editing (claude-code); for every other provider it is a no-op
// (WithApplierFixer(nil) is ignored). A configured-but-unconstructable fixer
// (e.g. the `claude` CLI is absent) is logged as a Warn and --apply proceeds with
// its original fail-fast manifest behaviour.
//
// The fixer needs no context of its own here: the Applier threads its own
// signal-aware context (WithApplierContext) into FixManifest, so a SIGINT/SIGTERM
// already cancels an in-flight agent process.
func applierFixerOption(llmCfg config.LLMConfig) autoupdate.ApplierOption {
	fixer, err := newConfiguredManifestFixer(llmCfg)
	if err != nil {
		logger.Warn("LLM manifest fixer unavailable; --apply will not auto-fix failed manifests: %v", err)
		return autoupdate.WithApplierFixer(nil)
	}
	return autoupdate.WithApplierFixer(fixer)
}

// runApply handles the --apply flag. ctx is threaded into the Applier via
// WithApplierContext so a SIGINT/SIGTERM cancels the in-flight `pkgdev manifest`
// or compile child process within ~2 s (R1.1, R1.2). The existing orphan
// rollback path then removes the half-applied .ebuild (R1.3).
func runApply(ctx context.Context, overlayPath, configDir, pkg string, llmCfg config.LLMConfig) {
	// Derive a cancelable apply context from the signal-aware ctx so the TUI's
	// Ctrl-C (which invokes cancel) cancels the in-flight child via
	// WithApplierContext and triggers the existing orphan rollback (R5.1/R5.2).
	applyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// buildApplyReporter wires the reporter into extra (WithApplierReporter), so
	// the reporter value itself is not needed at this call site.
	_, extra, finish := buildApplyReporter(applyCtx, cancel, 1)

	opts := []autoupdate.ApplierOption{
		autoupdate.WithApplierContext(applyCtx),
		autoupdate.WithApplierClean(autoupdateClean),
		autoupdate.WithApplierPackagesConfig(loadPackagesConfigForApply(overlayPath)),
		applierFixerOption(llmCfg),
	}
	opts = append(opts, extra...)

	applier, err := autoupdate.NewApplier(overlayPath, configDir, opts...)
	if err != nil {
		finish()
		logger.Error("failed to initialize applier: %v", err)
		osExit(1)
		return
	}

	// The applier's TaskStart now surfaces "applying <pkg>" through the reporter
	// (the plain backend prints a START line; the TUI shows the task), so the
	// previous output.Info Printf is intentionally gone.

	//nolint:contextcheck // applyCtx is propagated into Apply's spawned processes
	// via WithApplierContext (a.ctx) — the deliberate single-source wiring derived
	// from signal.NotifyContext. Apply takes no ctx param by design.
	result, err := applier.Apply(pkg, autoupdateCompile)

	// Stop the TUI and restore the terminal BEFORE the summary so the inline run
	// history stays in scrollback and displayApplyResult prints to a clean line.
	finish()

	if err != nil {
		displayApplyResult(result)
		osExit(1)
		return
	}

	displayApplyResult(result)
}

// runApplyAll handles `--apply all`: it applies every pending update, reusing a
// single Applier so the pending list and logs directory are loaded once. ctx is
// threaded into the Applier via WithApplierContext so a SIGINT/SIGTERM cancels
// the in-flight `pkgdev manifest` or compile child process (R1.1, R1.2).
//
// The package list is snapshotted up front: Apply mutates the underlying
// pending list (a successful apply deletes its entry), so iterating over the
// live map would be unsafe. Each Apply is independent — a failure on one
// package never aborts the others — and the process exits non-zero when any
// package failed, matching the single-package --apply contract.
//
// Without --compile the applies run concurrently across a worker pool bounded by
// --concurrency, so the slow, network-bound `pkgdev manifest` step of each
// package overlaps instead of running one at a time. With --compile they stay
// serial so the elevated compile step's confirmation prompt and sudo invocation
// are not interleaved. Both paths live in applyAllPackages.
func runApplyAll(ctx context.Context, overlayPath, configDir string, llmCfg config.LLMConfig) {
	// Read the pending list up front so the reporter's batch denominator (and the
	// "nothing to do" short-circuit) are known before the TUI program starts. The
	// applier built below loads the same pending.json, and Apply mutates it as it
	// goes, so this snapshot is the count we iterate over (mirrors the existing
	// snapshot rationale).
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		logger.Error("failed to load pending list: %v", err)
		osExit(1)
		return
	}
	updates := pending.List()
	if len(updates) == 0 {
		logger.Info("No pending updates to apply")
		return
	}

	// Derive a cancelable apply context from the signal-aware ctx so the TUI's
	// Ctrl-C (which invokes cancel) cancels the in-flight child via
	// WithApplierContext and triggers the existing orphan rollback (R5.1/R5.2).
	applyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	_, extra, finish := buildApplyReporter(applyCtx, cancel, len(updates))

	opts := []autoupdate.ApplierOption{
		autoupdate.WithApplierContext(applyCtx),
		autoupdate.WithApplierClean(autoupdateClean),
		autoupdate.WithApplierPackagesConfig(loadPackagesConfigForApply(overlayPath)),
		// Reuse the pending list already loaded so the applier and this snapshot
		// share one in-memory source of truth.
		autoupdate.WithApplierPendingList(pending),
		applierFixerOption(llmCfg),
	}
	opts = append(opts, extra...)

	applier, err := autoupdate.NewApplier(overlayPath, configDir, opts...)
	if err != nil {
		finish()
		logger.Error("failed to initialize applier: %v", err)
		osExit(1)
		return
	}

	// The applier's TaskStart surfaces each package through the reporter, so the
	// previous output.Info Printf per package is intentionally gone.
	//nolint:contextcheck // applyCtx reaches each Apply's spawned processes via
	// WithApplierContext (a.ctx) — Apply takes no ctx param by design, so the
	// manifest chain is cancelled through a.ctx rather than parameter propagation.
	results, failures := applyAllPackages(applier, updates, autoupdateCompile, autoupdateConcurrency)

	// Stop the TUI and restore the terminal BEFORE the summary so the inline run
	// history stays in scrollback and displayApplyAllResults prints cleanly.
	finish()

	displayApplyAllResults(results, failures)

	if failures > 0 {
		osExit(1)
	}
}

// applyAllPackages applies every pending update through the shared Applier and
// returns the per-package results in input order plus the number of hard
// failures (an Apply returning a non-nil error). It is the concurrency seam of
// runApplyAll.
//
// With compile == true the applies run serially: the compile step prompts for
// confirmation and runs under sudo, and interleaving those across goroutines
// would scramble the prompts. Otherwise the applies are dispatched across a
// bounded worker pool (mirroring overlay.RegenerateManifests) so each Apply's
// slow, network-bound `pkgdev manifest` step overlaps. concurrency caps the live
// workers and is clamped to [1, len(updates)].
//
// Concurrency safety: the Applier's pending list and reporter are mutex-guarded,
// each Apply's file work is scoped to its own package directory, and workers
// write results to distinct slice indices — so beyond the atomic failure tally
// no additional locking is needed.
func applyAllPackages(applier *autoupdate.Applier, updates []autoupdate.PendingUpdate, compile bool, concurrency int) ([]*autoupdate.ApplyResult, int) {
	results := make([]*autoupdate.ApplyResult, len(updates))

	// --compile path: serial, so the confirmation prompt and sudo invocation of
	// each compile step are not interleaved across goroutines.
	if compile {
		failures := 0
		for i, u := range updates {
			result, err := applier.Apply(u.Package, true)
			if err != nil {
				failures++
			}
			results[i] = result
		}
		return results, failures
	}

	// Non-compile path: a bounded worker pool over an index queue. Workers write
	// results[i] at distinct indices (no lock) and tally failures atomically.
	jobs := concurrency
	if jobs < 1 {
		jobs = 1
	}
	if jobs > len(updates) {
		jobs = len(updates)
	}

	var failures int64
	queue := make(chan int, len(updates))
	for i := range updates {
		queue <- i
	}
	close(queue)

	var wg sync.WaitGroup
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				result, err := applier.Apply(updates[i].Package, false)
				results[i] = result
				if err != nil {
					atomic.AddInt64(&failures, 1)
				}
			}
		}()
	}
	wg.Wait()

	return results, int(failures)
}

// displayApplyAllResults renders the per-package outcomes of `--apply all`
// followed by an aggregate summary line.
func displayApplyAllResults(results []*autoupdate.ApplyResult, failures int) {
	for _, result := range results {
		displayApplyResult(result)
	}

	applied, obsolete := 0, 0
	for _, r := range results {
		switch {
		case r == nil:
		case r.Obsolete:
			obsolete++
		case r.Success:
			applied++
		}
	}

	fmt.Println()
	output.Header.Println("Apply All Summary")
	output.Success.Printf("  Applied:  %d\n", applied)
	if obsolete > 0 {
		output.Warning.Printf("  Obsolete: %d (pruned from pending)\n", obsolete)
	}
	if failures > 0 {
		output.Error.Printf("  Failed:   %d\n", failures)
	}
	if applied > 0 {
		output.Info.Println("Don't forget to commit the changes with 'bentoo overlay commit'")
	}
}

// displayApplyResult formats and displays a single apply outcome.
// It is a no-op when result is nil. Otherwise it prints the package and
// version transition, then reports status (obsolete, success, or failure)
// plus any available details such as obsolete reason, LLM fix/QA summary,
// cleaned old-version info/warnings, and failure log path.
func displayApplyResult(result *autoupdate.ApplyResult) {
	if result == nil {
		return
	}

	fmt.Println()
	output.Header.Println("Apply Result")
	fmt.Println()

	output.Package.Printf("  %s\n", result.Package)
	fmt.Printf("    Version: %s → %s\n", result.OldVersion, result.NewVersion)

	if result.Obsolete {
		output.Warning.Println("    Status:  Obsolete (pruned from pending)")
		if result.ObsoleteReason != "" {
			output.Info.Printf("    Reason:  %s\n", result.ObsoleteReason)
		}
		return
	}

	if result.Success {
		output.Success.Println("    Status:  Success")
		if result.Fixed {
			output.Warning.Printf("    Fixed:   manifest repaired by LLM — %s\n", result.FixSummary)
		}
		if result.QASummary != "" {
			output.Warning.Printf("    QA:      pkgcheck findings after the fix — review before committing:\n%s\n", result.QASummary)
		}
		if result.CleanedOldVersion != "" {
			fmt.Printf("    Removed: %s-%s.ebuild (old version)\n", filepath.Base(result.Package), result.CleanedOldVersion)
		}
		if result.CleanWarning != "" {
			output.Warning.Printf("    Clean:   %s\n", result.CleanWarning)
		}
		output.Success.Println("\n✓ Update applied successfully")
		output.Info.Println("Don't forget to commit the changes with 'bentoo overlay commit'")
	} else {
		output.Error.Println("    Status:  Failed")
		if result.Error != nil {
			output.Error.Printf("    Error:   %v\n", result.Error)
		}
		if result.LogPath != "" {
			output.Info.Printf("    Log:     %s\n", result.LogPath)
		}
	}
}

// reviveCheckerOptions builds the Checker option set shared by the revive modes.
// It mirrors runCheck's option set exactly — config dir, context, concurrency,
// type filter, tuned rate limiter, cache TTL, and the same LLM wiring (with the
// err-first nil guard) — so a revived package's upstream check behaves
// identically to a normal --check. The GitHub token is not an option: NewChecker
// resolves it itself from GITHUB_TOKEN/GH_TOKEN via the secrets chain. The
// progress callback is omitted: the revive paths drive single-package
// CheckPackage calls, which never fire it.
func reviveCheckerOptions(ctx context.Context, configDir string, cacheTTL, httpTimeout time.Duration, llmCfg config.LLMConfig) []autoupdate.CheckerOption {
	opts := []autoupdate.CheckerOption{
		autoupdate.WithConfigDir(configDir),
		autoupdate.WithContext(ctx),
		autoupdate.WithConcurrency(autoupdateConcurrency),
		autoupdate.WithTypeFilter(autoupdateOnly),
		autoupdate.WithHTTPRequestTimeout(httpTimeout),
		autoupdate.WithRateLimiter(autoupdate.NewRateLimiter(autoupdate.WithTunedHostPolicies())),
	}
	if cacheTTL > 0 {
		opts = append(opts, autoupdate.WithCacheTTL(cacheTTL))
	}

	// Same err-first nil guard as runCheck: a failed constructor boxes a nil
	// concrete pointer into a NON-nil interface, so wire WithLLMClient only on
	// err==nil AND p!=nil. On failure Warn and continue (revive still runs,
	// skipping LLM extraction). WithLLMProviderConfigured suppresses the Checker's
	// "unused llm_prompt" Warn when a provider was requested.
	if p, err := newConfiguredLLMProvider(llmCfg); err != nil {
		logger.Warn("LLM provider %q unavailable; revive will skip LLM version extraction: %v", llmCfg.Provider, err)
	} else if p != nil {
		opts = append(opts, autoupdate.WithLLMClient(p))
	}
	opts = append(opts, autoupdate.WithLLMProviderConfigured(llmCfg.Provider != ""))

	return opts
}

// resolveGentooProviderFn is the seam the revive flows use to obtain the
// ::gentoo provider. It points at resolveGentooProvider in production and is
// overridable in tests so the flows can be driven with an on-disk fake (a
// provider.PackageDirProvider) instead of resolving the real gentoo repo.
var resolveGentooProviderFn = resolveGentooProvider

// resolveGentooProvider resolves the ::gentoo provider the revive flow seeds
// from, mirroring `overlay compare`'s provider-resolution idiom: config repos >
// registry, with the GitHub token resolved from GITHUB_TOKEN/GH_TOKEN via the
// secrets chain (github.ResolveToken). forceClone is false so a user-configured
// local/clone repo is honoured; an API-only gentoo simply will not implement
// provider.PackageDirProvider, which runRevive detects and reports. The caller
// owns prov.Close() and decides whether a resolution error is fatal
// (runRevive/runReviveList exit non-zero; the --revivable add-on to --check only
// warns and skips the report).
func resolveGentooProvider(cfg *config.Config) (provider.Provider, error) {
	configRepos := convertConfigRepos(cfg)

	registry, err := provider.NewRepositoryRegistry()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize repository registry: %w", err)
	}

	repoInfo, err := provider.ResolveRepository("gentoo", configRepos, registry)
	if err != nil {
		return nil, fmt.Errorf("repository 'gentoo' not found: %w", err)
	}

	// Resolve the GitHub token from GITHUB_TOKEN/GH_TOKEN via the secrets chain
	// (github.ResolveToken); a resolution error warns and continues with
	// unauthenticated access. Only fill an empty repo token so a config-specific
	// one still wins.
	token, err := github.ResolveToken()
	if err != nil {
		logger.Warn("resolving GitHub token: %v; continuing with unauthenticated GitHub API access", err)
	}
	if token != "" && repoInfo.Token == "" {
		repoInfo.Token = token
	}

	// forceClone=false: honour the resolved provider type so a configured local
	// git repo (the path that yields PackageDirProvider) is used as-is.
	prov, err := provider.NewProvider(repoInfo, false)
	if err != nil {
		return nil, fmt.Errorf("failed to create gentoo provider: %w", err)
	}
	return prov, nil
}

// runReviveList handles --revive-list: a passive report of disabled (orphaned)
// packages.toml entries whose upstream release is strictly newer than the highest
// version ::gentoo still carries. It mutates nothing — it only builds a Checker
// (the same option set as --check) and the ::gentoo provider, then prints the
// candidates FindRevivableOrphans returns as a PACKAGE | GENTOO | UPSTREAM table.
func runReviveList(ctx context.Context, overlayPath, configDir string, cacheTTL time.Duration, cfg *config.Config, llmCfg config.LLMConfig) {
	checker, err := autoupdate.NewChecker(overlayPath, reviveCheckerOptions(ctx, configDir, cacheTTL, resolveHTTPTimeout(cfg), llmCfg)...)
	if err != nil {
		logger.Error("failed to initialize checker: %v", err)
		osExit(1)
		return
	}

	prov, err := resolveGentooProviderFn(cfg)
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
		return
	}
	defer prov.Close() //nolint:errcheck

	// FindRevivableOrphans threads ctx into every upstream/gentoo lookup via the
	// Checker (WithContext) and the provider. Soft per-package errors are returned
	// alongside the candidates, so a partial scan still reports what it found.
	candidates, err := checker.FindRevivableOrphans(prov) //nolint:contextcheck // ctx is injected via autoupdate.WithContext
	if err != nil {
		logger.Warn("revive scan completed with soft errors: %v", err)
	}

	displayReviveCandidates(candidates)
}

// displayReviveCandidates renders the revivable-orphan report as a fixed-width
// PACKAGE | GENTOO | UPSTREAM table, reusing truncatePkgName for column
// alignment (as `overlay compare` does). An empty set prints a "nothing to
// revive" note instead of an empty table.
func displayReviveCandidates(candidates []autoupdate.ReviveCandidate) {
	if len(candidates) == 0 {
		output.Success.Println("Nothing to revive — no orphaned package has an upstream newer than ::gentoo")
		return
	}

	fmt.Println()
	output.Header.Println("Revivable Orphans")
	fmt.Println()

	output.Dim.Printf("  %s %s %s\n",
		truncatePkgName("PACKAGE", 40), truncatePkgName("GENTOO", 16), "UPSTREAM")
	for _, c := range candidates {
		output.Package.Printf("  %s ", truncatePkgName(c.Package, 40))
		fmt.Printf("%s %s\n", truncatePkgName(c.GentooVersion, 16), c.UpstreamVersion)
	}

	fmt.Println()
	output.Info.Printf("Found %d revivable orphan(s)\n", len(candidates))
	output.Info.Println("Use 'bentoo overlay autoupdate --revive <package>' to revive one, or '--revive all' for every candidate")
}

// reviveOutcome records the result of reviving a single package so runRevive can
// print an aggregate summary without aborting on the first failure.
type reviveOutcome struct {
	pkg    string
	status string // "revived", "skipped", or "failed"
	detail string // human-facing note (e.g. the apply error or skip reason)
}

// runRevive handles --revive <pkg|all>: it resurrects each target orphan by
// seeding the current ::gentoo ebuild into the overlay, re-enabling the entry,
// and bumping it to the upstream version via the normal CheckPackage+Apply flow.
//
// The ::gentoo provider must expose an on-disk package directory
// (provider.PackageDirProvider); an API-only gentoo cannot seed a base ebuild, so
// that case aborts ONCE up front with a clear, actionable error. Each package is
// independent: a failure on one never aborts the others; outcomes are accumulated
// and the process exits non-zero when any package failed.
func runRevive(ctx context.Context, overlayPath, configDir, target string, cacheTTL time.Duration, cfg *config.Config, llmCfg config.LLMConfig) {
	prov, err := resolveGentooProviderFn(cfg)
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
		return
	}
	defer prov.Close() //nolint:errcheck

	// The revive seed copies the ::gentoo package dir off disk; an API-only
	// provider cannot do that. Detect it ONCE before the loop and bail with an
	// actionable hint (mirrors `overlay compare`'s local-repo guidance).
	pdp, ok := prov.(provider.PackageDirProvider)
	if !ok {
		logger.Error("the resolved gentoo provider has no local package directory; revive needs an on-disk ::gentoo tree.")
		logger.Info("Configure a local gentoo repository in ~/.config/bentoo/config.yaml:")
		logger.Info("  repositories:")
		logger.Info("    gentoo:")
		logger.Info("      provider: local")
		logger.Info("      path: /var/db/repos/gentoo")
		logger.Info("(or force a clone-backed provider so the package tree is available on disk)")
		osExit(1)
		return
	}

	// Build the initial Checker (shared option set) to resolve the target list.
	checker, err := autoupdate.NewChecker(overlayPath, reviveCheckerOptions(ctx, configDir, cacheTTL, resolveHTTPTimeout(cfg), llmCfg)...)
	if err != nil {
		logger.Error("failed to initialize checker: %v", err)
		osExit(1)
		return
	}

	// Resolve the target package list: an explicit "category/pkg", or "all"
	// (every candidate FindRevivableOrphans reports).
	var targets []string
	if target == "all" {
		candidates, ferr := checker.FindRevivableOrphans(prov) //nolint:contextcheck // ctx is injected via autoupdate.WithContext
		if ferr != nil {
			logger.Warn("revive scan completed with soft errors: %v", ferr)
		}
		if len(candidates) == 0 {
			output.Success.Println("Nothing to revive — no orphaned package has an upstream newer than ::gentoo")
			return
		}
		for _, c := range candidates {
			targets = append(targets, c.Package)
		}
	} else {
		targets = []string{target}
	}

	// One shared pending list for the whole revive run. CheckPackage (which
	// writes the pending entry) and applier.Apply (which reads it) run in the
	// SAME process here — unlike the separate `--check` / `--apply` invocations
	// that each reload pending.json from disk. PendingList.Get reads its in-memory
	// map, so without a shared instance the applier (loaded before the check)
	// would never see the freshly-written entry and Apply would fail with
	// ErrPackageNotInPending. Injecting one instance into both makes the in-memory
	// state the single source of truth.
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		logger.Error("failed to initialize pending list: %v", err)
		osExit(1)
		return
	}

	applier, err := autoupdate.NewApplier(overlayPath, configDir,
		autoupdate.WithApplierContext(ctx),
		autoupdate.WithApplierClean(autoupdateClean),
		autoupdate.WithApplierPackagesConfig(loadPackagesConfigForApply(overlayPath)),
		autoupdate.WithApplierPendingList(pending),
	)
	if err != nil {
		logger.Error("failed to initialize applier: %v", err)
		osExit(1)
		return
	}

	httpTimeout := resolveHTTPTimeout(cfg)
	outcomes := make([]reviveOutcome, 0, len(targets))
	for _, pkg := range targets {
		outcomes = append(outcomes, reviveOne(ctx, pkg, overlayPath, configDir, cacheTTL, httpTimeout, llmCfg, prov, pdp, applier, pending))
	}

	failures := displayReviveSummary(outcomes)
	if failures > 0 {
		osExit(1)
	}
}

// reviveOne performs the full revive for a single package and returns its
// outcome. It never calls osExit: every failure is captured so the caller can
// continue with the remaining targets and exit non-zero at the end.
//
// Steps (in order): locate the ::gentoo package dir, pick the highest ::gentoo
// version, seed it into the overlay, re-enable the entry in packages.toml BEFORE
// checking (so the checker won't skip it), CheckPackage(force=true) to populate
// pending with the upstream version, then Apply (honouring --compile / --clean).
func reviveOne(ctx context.Context, pkg, overlayPath, configDir string, cacheTTL, httpTimeout time.Duration, llmCfg config.LLMConfig, prov provider.Provider, pdp provider.PackageDirProvider, applier *autoupdate.Applier, pending *autoupdate.PendingList) reviveOutcome {
	output.Info.Printf("Reviving %s...\n", pkg)

	category, pkgName, ok := splitPackage(pkg)
	if !ok {
		return reviveOutcome{pkg: pkg, status: "failed", detail: fmt.Sprintf("invalid package name %q (want category/package)", pkg)}
	}

	// On-disk ::gentoo package dir to seed from.
	srcDir, err := pdp.LocalPackagePath(category, pkgName)
	if err != nil {
		return reviveOutcome{pkg: pkg, status: "failed", detail: fmt.Sprintf("gentoo package dir lookup failed: %v", err)}
	}

	// Highest ::gentoo version is the base ebuild we copy in.
	versions, err := prov.GetPackageVersions(category, pkgName)
	if err != nil {
		return reviveOutcome{pkg: pkg, status: "failed", detail: fmt.Sprintf("gentoo version lookup failed: %v", err)}
	}
	gentooVersion := highestVersion(versions)
	if gentooVersion == "" {
		return reviveOutcome{pkg: pkg, status: "failed", detail: "no comparable gentoo version found"}
	}

	// Seed the ::gentoo ebuild (+ metadata.xml / files/) into the overlay.
	// SeedFromGentoo takes the full "category/package" (it splits internally).
	if err := applier.SeedFromGentoo(pkg, srcDir, gentooVersion); err != nil {
		return reviveOutcome{pkg: pkg, status: "failed", detail: fmt.Sprintf("seed from gentoo failed: %v", err)}
	}

	// Re-enable the entry BEFORE checking: the checker skips disabled entries, so
	// a still-disabled package would never produce a pending update.
	if err := autoupdate.EnablePackagesInConfig(overlayPath, []string{pkg}); err != nil {
		return reviveOutcome{pkg: pkg, status: "failed", detail: fmt.Sprintf("re-enable in packages.toml failed: %v", err)}
	}

	// Build a FRESH Checker so it loads the now re-enabled packages.toml, then
	// check the package (force=true to bypass cache) to populate the pending list
	// with the upstream version (+ aux_var/commit values via the existing paths).
	// It shares the applier's pending list so the entry CheckPackage writes is
	// visible to Apply below (same in-memory map, same process).
	checker, err := autoupdate.NewChecker(overlayPath,
		append(reviveCheckerOptions(ctx, configDir, cacheTTL, httpTimeout, llmCfg), autoupdate.WithPendingList(pending))...)
	if err != nil {
		return reviveOutcome{pkg: pkg, status: "failed", detail: fmt.Sprintf("checker init failed: %v", err)}
	}
	result, err := checker.CheckPackage(pkg, true) //nolint:contextcheck // ctx is injected via autoupdate.WithContext
	if err != nil {
		return reviveOutcome{pkg: pkg, status: "failed", detail: fmt.Sprintf("check failed: %v", err)}
	}
	if !result.HasUpdate {
		// Seeded base already equals upstream: nothing to bump. The base ebuild is
		// in place and the entry re-enabled, so normal --check will track it going
		// forward.
		return reviveOutcome{pkg: pkg, status: "skipped", detail: fmt.Sprintf("gentoo %s already current with upstream %s", gentooVersion, result.UpstreamVersion)}
	}

	// Bump to the upstream version using the existing apply flow (honours
	// --compile and --clean exactly as runApply does).
	//nolint:contextcheck // ctx is propagated into Apply's spawned processes via WithApplierContext.
	applyResult, err := applier.Apply(pkg, autoupdateCompile)
	if err != nil {
		detail := err.Error()
		if applyResult != nil && applyResult.LogPath != "" {
			detail = fmt.Sprintf("%v (log: %s)", err, applyResult.LogPath)
		}
		return reviveOutcome{pkg: pkg, status: "failed", detail: detail}
	}
	if applyResult != nil && applyResult.Obsolete {
		return reviveOutcome{pkg: pkg, status: "skipped", detail: applyResult.ObsoleteReason}
	}

	return reviveOutcome{pkg: pkg, status: "revived", detail: fmt.Sprintf("%s → %s", gentooVersion, result.UpstreamVersion)}
}

// displayReviveSummary prints per-package revive outcomes followed by an
// aggregate (revived / skipped / failed) and returns the failure count so the
// caller can set the exit code.
func displayReviveSummary(outcomes []reviveOutcome) int {
	fmt.Println()
	output.Header.Println("Revive Summary")
	fmt.Println()

	var revived, skipped, failed int
	for _, o := range outcomes {
		switch o.status {
		case "revived":
			revived++
			output.Success.Printf("  ✓ %s: %s\n", o.pkg, o.detail)
		case "skipped":
			skipped++
			output.Warning.Printf("  - %s: %s\n", o.pkg, o.detail)
		default:
			failed++
			output.Error.Printf("  ✗ %s: %s\n", o.pkg, o.detail)
		}
	}

	fmt.Println()
	output.Success.Printf("  Revived: %d\n", revived)
	if skipped > 0 {
		output.Warning.Printf("  Skipped: %d\n", skipped)
	}
	if failed > 0 {
		output.Error.Printf("  Failed:  %d\n", failed)
	}
	if revived > 0 {
		output.Info.Println("Don't forget to commit the changes with 'bentoo overlay commit'")
	}

	return failed
}

// splitPackage splits a "category/package" string, returning ok=false for any
// value that is not exactly two non-empty segments. It mirrors the split+length
// check the checker/applier helpers use.
func splitPackage(pkg string) (category, name string, ok bool) {
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// highestVersion returns the highest valid version from versions using the same
// Gentoo-aware ordering the checker uses to pick the highest ebuild. Unparseable
// entries are skipped; "" means no comparable version was found.
func highestVersion(versions []string) string {
	var best string
	for _, v := range versions {
		v = strings.TrimSpace(v)
		if !ebuild.IsValidVersion(v) {
			continue
		}
		if best == "" || ebuild.CompareVersions(v, best) > 0 {
			best = v
		}
	}
	return best
}
