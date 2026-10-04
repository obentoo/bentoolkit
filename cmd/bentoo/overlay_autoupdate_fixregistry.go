package main

// Interactive LLM registry-fix loop for `bentoo overlay autoupdate` (story 014,
// sub-tasks 3.1 + 3.2). After a check run, the packages that failed with a
// fetch/extraction error (ErrFetchFailed) can be repaired one at a time by an
// agentic RegistryFixer that edits packages.toml in place. The snapshot → fix →
// fresh re-check → revert transaction is autoupdate.AttemptRegistryFix (story
// 060, R3.7); this file keeps only the y/N/a/q prompt, the printing and the
// tally. A kept edit is left in the working tree only; committing it is out of
// scope here (R6.1).

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// promptRegistryFixes drives the interactive per-package LLM registry-fix loop.
//
// It offers a fix only for packages whose failure wraps autoupdate.ErrFetchFailed
// (R3.5), in deterministic lexical order (R3.4) — autoupdate.RepairableFetchFailures.
// For each such package it prompts y/N/a/q (R3.1-R3.3): `y` attempts a fix, `a`
// attempts this and all remaining without further per-package prompts,
// `n`/empty skips, `q` stops the loop.
//
// Each attempt is one autoupdate.AttemptRegistryFix: snapshot-guarded (R5.1),
// re-checked through a FRESH Checker (R4.1), decided by the re-check rather than
// the agent summary (R4.2). A pass keeps the edit (R5.2); a still-failing
// re-check prompts keep/revert (R5.3) and reverts atomically on N (R5.4). A fixer
// error has already been reverted by the transaction and is NON-FATAL (R5.5):
// the function returns nil even though an individual FixRegistry call errored.
//
// in is the prompt source (os.Stdin in production, a strings.Reader in tests);
// newChecker constructs a fresh Checker over the same overlay on each call.
func promptRegistryFixes(ctx context.Context, overlayPath string, fixer autoupdate.RegistryFixer, failures map[string]error, in io.Reader, newChecker func() (*autoupdate.Checker, error)) error {
	pkgs := autoupdate.RepairableFetchFailures(failures)
	if len(pkgs) == 0 {
		return nil
	}

	// One reader for the whole loop: re-creating a bufio.Reader per package would
	// drop bytes already buffered from `in` (a single test reader feeds several
	// prompts), so it is constructed once and reused.
	reader := bufio.NewReader(in)
	applyAll := false

	var fixed, reverted, skipped int

loop:
	for _, pkg := range pkgs {
		if !applyAll {
			fmt.Printf("Fix registry for %s with LLM? [y/N/a/q] ", pkg)
			answer := readAnswer(reader)
			switch answer {
			case "q":
				// Stop processing the remaining packages entirely.
				break loop
			case "a":
				applyAll = true
			case "y":
				// proceed
			default:
				// n, empty, or anything unrecognized: skip this package.
				skipped++
				continue
			}
		}

		a := autoupdate.AttemptRegistryFix(ctx, overlayPath, pkg, failures[pkg], fixer, newChecker)
		switch a.Status {
		case autoupdate.RegistryFixSkipped:
			fmt.Printf("  skipping %s: %s: %v\n", pkg, registryFixStageLine(a.Stage), a.Err)
			skipped++

		case autoupdate.RegistryFixReverted:
			// The transaction has already restored the snapshot; only a failed
			// restore is left to report (R3.6).
			fmt.Printf("  %s: %s: %v\n", pkg, registryFixStageLine(a.Stage), a.Err)
			warnIfNotRestored(pkg, a.RestoreErr)
			reverted++

		case autoupdate.RegistryFixPassed:
			// Name the model that made the edit; FormatModelUsed says "model
			// alias ..." when the configured model was an alias, because an
			// alias resolves to a different model over time (S030-R4.1/R4.2).
			fmt.Printf("✔ %s fixed using %s: %s (resolved upstream %s)\n",
				pkg, autoupdate.FormatModelUsed(a.Result.Model), a.Result.Summary, a.Recheck.UpstreamVersion)
			if a.Recheck.NotComparable {
				fmt.Printf("  warning: %s extracted version %q is not orderable against the current version; the parser may need more work\n", pkg, a.Recheck.UpstreamVersion)
			}
			fixed++

		case autoupdate.RegistryFixStillFailing:
			// The still-failing line carries the model record too: an edit the
			// operator may choose to KEEP is exactly the one an audit will come
			// back to (S030-R4.1). It also names the tools the agent was refused,
			// the likeliest reason its fix fell short, never their input (S051-R5.2).
			fmt.Printf("  %s still failing after fix using %s: %s%s\n  error: %v\n",
				pkg, autoupdate.FormatModelUsed(a.Result.Model), a.Result.Summary, autoupdate.RefusedToolsNote(a.Result.DeniedTools), a.RecheckErr)
			fmt.Print("Keep the edit anyway? [y/N] ")
			if readAnswer(reader) == "y" {
				// User chose to keep a still-failing edit (R5.3).
				fixed++
			} else {
				// Revert byte-for-byte to the pre-edit snapshot (R5.4).
				warnIfNotRestored(pkg, a.Revert())
				reverted++
			}
		}
	}

	fmt.Printf("fixed %d · reverted %d · skipped %d\n", fixed, reverted, skipped)
	return nil
}

// registryFixStageLine is the wording each stopped stage has always printed
// between the package name and the cause.
func registryFixStageLine(stage autoupdate.RegistryFixStage) string {
	switch stage {
	case autoupdate.RegistryFixStageSnapshot:
		return "could not snapshot packages.toml"
	case autoupdate.RegistryFixStageLoad:
		return "could not load packages.toml"
	case autoupdate.RegistryFixStageFixer:
		return "registry fix failed"
	case autoupdate.RegistryFixStageChecker:
		return "could not build checker for re-check"
	default:
		return fmt.Sprintf("registry fix stage %d", int(stage))
	}
}

// readAnswer reads one line from reader and normalizes it to a lowercase,
// whitespace-trimmed token. A read error or EOF is treated as "q" so an
// exhausted/closed input stops the loop rather than spinning.
func readAnswer(reader *bufio.Reader) string {
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "q"
	}
	return strings.TrimSpace(strings.ToLower(line))
}

// warnIfNotRestored prints a warning when restoring packages.toml failed (rare,
// e.g. the directory became unwritable). The error is not propagated: a
// per-package revert failure must not abort the whole loop, and the warning
// gives the user the information to recover manually (R3.6).
func warnIfNotRestored(pkg string, err error) {
	if err != nil {
		fmt.Printf("  warning: could not restore packages.toml for %s: %v\n", pkg, err)
	}
}
