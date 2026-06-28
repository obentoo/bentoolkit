package main

// Interactive LLM registry-fix loop for `bentoo overlay autoupdate` (story 014,
// sub-tasks 3.1 + 3.2). After a check run, the packages that failed with a
// fetch/extraction error (ErrFetchFailed) can be repaired one at a time by an
// agentic RegistryFixer that edits packages.toml in place. Each attempt is
// guarded by a byte-for-byte snapshot so a failing or erroring fix can be
// reverted atomically, and success is decided by an authoritative re-check
// through a FRESH Checker (which reloads the edited config) — never by the
// agent's own summary (R4.1/R4.2). A kept edit is left in the working tree only;
// committing it is out of scope here (R6.1).

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// readRegistrySnapshot reads the raw packages.toml bytes and its file mode so the
// loop can restore the file verbatim (including permissions) after a failed or
// erroring fix attempt. The bytes are captured before any edit; the mode is
// reapplied by restoreRegistrySnapshot. Any read or stat error is returned so the
// caller can skip the package rather than edit it without a recoverable snapshot.
func readRegistrySnapshot(configPath string) ([]byte, os.FileMode, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read packages.toml: %w", err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to stat packages.toml: %w", err)
	}
	return data, info.Mode(), nil
}

// restoreRegistrySnapshot atomically rewrites configPath with the captured
// snapshot bytes and mode, mirroring the temp-file+rename discipline of
// setPackagesEnabled (config.go). The temp file lives in the SAME directory as
// configPath so the rename is a same-filesystem (atomic) operation; on a write or
// rename failure the temp file is removed and the underlying error is returned so
// the caller surfaces a clear "could not restore" rather than leaving a stray
// `.tmp`. Only the permission bits of mode are applied (matching setPackagesEnabled).
func restoreRegistrySnapshot(configPath string, data []byte, mode os.FileMode) error {
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, mode.Perm()); err != nil {
		return fmt.Errorf("failed to write temp config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("failed to restore packages.toml: %w", err)
	}
	return nil
}

// promptRegistryFixes drives the interactive per-package LLM registry-fix loop.
//
// It offers a fix only for packages whose failure wraps autoupdate.ErrFetchFailed
// (R3.5), in deterministic lexical order (R3.4). For each such package it prompts
// y/N/a/q (R3.1-R3.3): `y` attempts a fix, `a` attempts this and all remaining
// without further per-package prompts, `n`/empty skips, `q` stops the loop.
//
// Every attempt is snapshot-guarded (R5.1): the raw packages.toml bytes+mode are
// captured before the fixer runs. After the agent edits the file, a FRESH Checker
// re-checks the package (R4.1) — a fresh checker is required so the edited config
// is reloaded from disk (config staleness). Success is decided by the re-check,
// not the agent summary (R4.2): a pass keeps the edit (R5.2); a still-failing
// re-check prompts keep/revert (R5.3) and reverts atomically on N (R5.4). A fixer
// error restores the snapshot and continues — it is NON-FATAL (R5.5): the function
// returns nil even though an individual FixRegistry call errored.
//
// in is the prompt source (os.Stdin in production, a strings.Reader in tests);
// newChecker constructs a fresh Checker over the same overlay on each call.
func promptRegistryFixes(ctx context.Context, overlayPath string, fixer autoupdate.RegistryFixer, failures map[string]error, in io.Reader, newChecker func() (*autoupdate.Checker, error)) error {
	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")
	configDir := filepath.Join(overlayPath, ".autoupdate")

	// Only fetch/extraction failures are repairable by the registry fixer; every
	// other failure class (e.g. manifest verification) is filtered out here (R3.5).
	pkgs := make([]string, 0, len(failures))
	for pkg, ferr := range failures {
		if errors.Is(ferr, autoupdate.ErrFetchFailed) {
			pkgs = append(pkgs, pkg)
		}
	}
	if len(pkgs) == 0 {
		return nil
	}
	sort.Strings(pkgs) // deterministic, lexical prompt order (R3.4)

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

		// Snapshot BEFORE any edit so a failing/erroring fix can be reverted
		// byte-for-byte (R5.1). Without a snapshot we must not edit the file.
		snapshot, mode, err := readRegistrySnapshot(configPath)
		if err != nil {
			fmt.Printf("  skipping %s: could not snapshot packages.toml: %v\n", pkg, err)
			skipped++
			continue
		}

		// Load the current (broken) config to seed the fix request. No edit has
		// happened yet, so a load failure just skips the package — nothing to revert.
		pc, err := autoupdate.LoadPackagesConfig(overlayPath)
		if err != nil {
			fmt.Printf("  skipping %s: could not load packages.toml: %v\n", pkg, err)
			skipped++
			continue
		}
		cfg := pc.Packages[pkg]
		req := autoupdate.RegistryFixRequest{
			Package:    pkg,
			Config:     &cfg,
			FetchError: failures[pkg].Error(),
			ConfigDir:  configDir,
		}

		res, err := fixer.FixRegistry(ctx, req)
		if err != nil {
			// A per-package fixer error is non-fatal (R5.5): undo any partial edit
			// and move on. The function itself still returns nil.
			fmt.Printf("  %s: registry fix failed: %v\n", pkg, err)
			restoreOrWarn(configPath, snapshot, mode, pkg)
			reverted++
			continue
		}

		// Authoritative re-check through a FRESH checker so the edited config is
		// reloaded from disk (R4.1). Success is decided here, not by res.Summary.
		c, cerr := newChecker()
		if cerr != nil {
			fmt.Printf("  %s: could not build checker for re-check: %v\n", pkg, cerr)
			restoreOrWarn(configPath, snapshot, mode, pkg)
			reverted++
			continue
		}
		checkRes, checkErr := c.CheckPackage(pkg, true) //nolint:contextcheck // ctx is injected via autoupdate.WithContext in newChecker's opts

		// Pass = no error AND a version was extracted. A benign cache/pending
		// warning leaves checkErr nil with UpstreamVersion set (checker.go success
		// path returns result, nil), so the gate is checkErr==nil && version!="",
		// NOT checkRes.Error (which may hold a non-fatal cache warning).
		if checkErr == nil && checkRes != nil && checkRes.UpstreamVersion != "" {
			fmt.Printf("✔ %s fixed: %s (resolved upstream %s)\n", pkg, res.Summary, checkRes.UpstreamVersion)
			if checkRes.NotComparable {
				fmt.Printf("  warning: %s extracted version %q is not orderable against the current version; the parser may need more work\n", pkg, checkRes.UpstreamVersion)
			}
			fixed++
			continue
		}

		// Still failing: report the new error and let the user keep or revert.
		newErr := checkErr
		if checkRes != nil && checkRes.Error != nil {
			newErr = checkRes.Error
		}
		fmt.Printf("  %s still failing after fix: %s\n  error: %v\n", pkg, res.Summary, newErr)
		fmt.Print("Keep the edit anyway? [y/N] ")
		if readAnswer(reader) == "y" {
			// User chose to keep a still-failing edit (R5.3).
			fixed++
		} else {
			// Revert byte-for-byte to the pre-edit snapshot (R5.4).
			restoreOrWarn(configPath, snapshot, mode, pkg)
			reverted++
		}
	}

	fmt.Printf("fixed %d · reverted %d · skipped %d\n", fixed, reverted, skipped)
	return nil
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

// restoreOrWarn restores the snapshot and prints a warning if the restore itself
// fails (rare, e.g. the directory became unwritable). The restore error is not
// propagated: a per-package revert failure must not abort the whole loop, and the
// warning gives the user the information to recover manually.
func restoreOrWarn(configPath string, snapshot []byte, mode os.FileMode, pkg string) {
	if err := restoreRegistrySnapshot(configPath, snapshot, mode); err != nil {
		fmt.Printf("  warning: could not restore packages.toml for %s: %v\n", pkg, err)
	}
}
