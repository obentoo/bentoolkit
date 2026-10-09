package main

// Interactive LLM registry-fix loop for `bentoo overlay autoupdate`. After a
// check run, the packages that failed with a fetch/extraction error
// (ErrFetchFailed) can be repaired one at a time by an agentic RegistryFixer
// that edits packages.toml in place. The snapshot → fix → fresh re-check →
// revert transaction is autoupdate.AttemptRegistryFix; this file keeps only the
// y/N/a/q prompt, the printing and the tally. A kept edit is left in the
// working tree only; committing it is out of scope here.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
)

// promptRegistryFixes drives the interactive per-package LLM registry-fix loop.
//
// It offers a fix only for packages whose failure wraps autoupdate.ErrFetchFailed
// and not autoupdate.ErrUpstreamUnreachable, in deterministic lexical order
// (autoupdate.RepairableFetchFailures). A transport failure — a timeout, a TLS
// EOF — is one the record did not cause, so offering to rewrite the record would
// invite a distracted "y" to break an entry that was correct.
// Prompts are y/N/a/q: `y` fixes, `a` fixes this and all remaining without
// asking, `n`/empty skips, `q` stops the loop.
//
// Each attempt is one autoupdate.AttemptRegistryFix: snapshot-guarded,
// re-checked through a FRESH Checker and decided by the re-check rather than
// the agent summary. A pass keeps the edit; a still-failing re-check prompts
// keep/revert and reverts atomically on N. A fixer error has already been
// reverted by the transaction and is NON-FATAL, which is why the function
// returns no error at all.
//
// in is the prompt source (os.Stdin in production, a strings.Reader in tests);
// newChecker constructs a fresh Checker over the same overlay on each call.
func promptRegistryFixes(ctx context.Context, overlayPath string, registryFixer fixer.RegistryFixer, failures map[string]error, in io.Reader, newChecker func() (*autoupdate.Checker, error)) {
	pkgs := autoupdate.RepairableFetchFailures(failures)
	if len(pkgs) == 0 {
		return
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

		a := autoupdate.AttemptRegistryFix(ctx, overlayPath, pkg, failures[pkg], registryFixer, newChecker)
		switch a.Status {
		case autoupdate.RegistryFixSkipped:
			fmt.Printf("  skipping %s: %s: %v\n", pkg, registryFixStageLine(a.Stage), a.Err)
			skipped++

		case autoupdate.RegistryFixReverted:
			// The transaction has already restored the snapshot; only a failed
			// restore is left to report.
			fmt.Printf("  %s: %s: %v\n", pkg, registryFixStageLine(a.Stage), a.Err)
			warnIfNotRestored(pkg, a.RestoreErr)
			reverted++

		case autoupdate.RegistryFixPassed:
			// Name the model that made the edit; FormatModelUsed says "model
			// alias ..." when the configured model was an alias, because an
			// alias resolves to a different model over time.
			fmt.Printf("✔ %s fixed using %s: %s (resolved upstream %s)\n",
				pkg, fixer.FormatModelUsed(a.Result.Model), a.Result.Summary, a.Recheck.UpstreamVersion)
			if a.Recheck.NotComparable {
				fmt.Printf("  warning: %s extracted version %q is not orderable against the current version; the parser may need more work\n", pkg, a.Recheck.UpstreamVersion)
			}
			fixed++

		case autoupdate.RegistryFixStillFailing:
			// The still-failing line carries the model record too: an edit the
			// operator may choose to KEEP is exactly the one an audit will come
			// back to. It also names the tools the agent was refused, the
			// likeliest reason its fix fell short, never their input.
			fmt.Printf("  %s still failing after fix using %s: %s%s\n  error: %v\n",
				pkg, fixer.FormatModelUsed(a.Result.Model), a.Result.Summary, llm.RefusedToolsNote(a.Result.DeniedTools), a.RecheckErr)
			fmt.Print("Keep the edit anyway? [y/N] ")
			if readAnswer(reader) == "y" {
				// User chose to keep a still-failing edit.
				fixed++
			} else {
				// Revert byte-for-byte to the pre-edit snapshot.
				warnIfNotRestored(pkg, a.Revert())
				reverted++
			}
		}
	}

	fmt.Printf("fixed %d · reverted %d · skipped %d\n", fixed, reverted, skipped)
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
// gives the user the information to recover manually.
func warnIfNotRestored(pkg string, err error) {
	if err != nil {
		fmt.Printf("  warning: could not restore packages.toml for %s: %v\n", pkg, err)
	}
}
