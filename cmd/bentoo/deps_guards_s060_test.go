package main

// Test-after guards for story 060, sub-task 4.1, written once withDeps let a
// test substitute a seam on the tree the CLI builds.
//
// Not here, on purpose: R5.3/R5.6 (--distfiles-cache "" versus an unpassed
// flag) and R5.4 (a standalone --clean sweeps with concurrency 1). Both are
// only observable through autoupdate.SweepOption / applier option closures,
// which configure an unexported struct of internal/autoupdate, so a stub
// executor receives them but cannot read them. The resolution itself is
// pinned at the resolver by TestAutoupdateDistfilesCacheDefaultMatchesManifestCommand.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// s060CountingFixer records how often the registry fix was offered and taken.
type s060CountingFixer struct{ calls atomic.Int32 }

func (f *s060CountingFixer) FixRegistry(context.Context, autoupdate.RegistryFixRequest) (autoupdate.RegistryFixResult, error) {
	f.calls.Add(1)
	return autoupdate.RegistryFixResult{}, errors.New("s060 counting fixer edits nothing")
}

// TestS060GuardDepsRegistryFixOfferedOnlyOnATerminal is R3.1: with a usable
// fixer and a package whose extraction fails, `--check` offers the registry
// fix only when the tree's checkInteractive reports a terminal.
func TestS060GuardDepsRegistryFixOfferedOnlyOnATerminal(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "stdin is not a terminal", true: "stdin is a terminal"}[interactive], func(t *testing.T) {
			// 200 OK without the "version" field: extraction fails for real.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{"other": "1.0.0"})
			}))
			t.Cleanup(server.Close)

			fixer := &s060CountingFixer{}
			var asked atomic.Int32
			c := newTestCLI(t, withDeps(func(d *deps) {
				d.checkRegistryFixer = func(*slog.Logger, config.LLMConfig) (autoupdate.RegistryFixer, error) { return fixer, nil }
				d.checkInteractive = func() bool { asked.Add(1); return interactive }
			}))
			const pkg = "app-misc/probe"
			writeExitTestPackagesConfig(t, c.Overlay(), server.URL, []string{pkg})
			writeExitTestEbuild(t, c.Overlay(), pkg, "0.9.0")

			// Answer "y" should the prompt be offered.
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.WriteString("y\n"); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			oldStdin := os.Stdin
			os.Stdin = r
			t.Cleanup(func() { os.Stdin = oldStdin; _ = r.Close() })

			_, stderr, _ := c.Run("overlay", "autoupdate", "--check", "--force")

			if asked.Load() == 0 {
				t.Fatalf("checkInteractive was never consulted: the fix gate did not run through the tree's deps\nstderr: %s", stderr)
			}
			got := fixer.calls.Load()
			if interactive && got == 0 {
				t.Errorf("a terminal and a usable fixer, yet the registry fix was never offered (R3.1)\nstderr: %s", stderr)
			}
			if !interactive && got != 0 {
				t.Errorf("no terminal, yet the registry fixer ran %d time(s) (R3.1)", got)
			}
		})
	}
}

// TestS060GuardDepsRestoreFailurePrintsItsWarningLine is R3.6: a failed
// restore of packages.toml is reported on one line naming the package and the
// error, and the loop is not aborted.
func TestS060GuardDepsRestoreFailurePrintsItsWarningLine(t *testing.T) {
	out := captureStdout(t, func() {
		warnIfNotRestored("app-misc/probe", errors.New("permission denied"))
		warnIfNotRestored("app-misc/clean", nil)
	})
	const want = "  warning: could not restore packages.toml for app-misc/probe: permission denied\n"
	if out != want {
		t.Errorf("restore-failure output = %q, want exactly %q (R3.6)", out, want)
	}
	if strings.Contains(out, "app-misc/clean") {
		t.Errorf("a successful restore printed a warning: %q", out)
	}
}

// TestS060GuardDepsDistfilesCacheFlagVersusConfig pins R5.3 and R5.6 at the
// point that decides them. The resolved directory reaches the applier and the
// sweep only inside private option closures, so the decision is asserted where
// it is made: an explicit --distfiles-cache "" disables the lookup even when
// the config names a cache, and an unpassed flag takes the config key.
func TestS060GuardDepsDistfilesCacheFlagVersusConfig(t *testing.T) {
	cfg := &config.Config{Autoupdate: config.AutoupdateConfig{DistfilesCache: "/srv/configured-cache"}}

	empty := testAutoupdateOptions()
	empty.distfilesCache = ""
	if got := empty.resolveAutoupdateDistfileDirs(discardLog(), cfg, true).Cache; got != "" {
		t.Errorf(`--distfiles-cache "" resolved the cache to %q, want "" (lookup off, R5.3)`, got)
	}

	unpassed := testAutoupdateOptions()
	if got := unpassed.resolveAutoupdateDistfileDirs(discardLog(), cfg, false).Cache; got != "/srv/configured-cache" {
		t.Errorf("an unpassed --distfiles-cache resolved the cache to %q, want the autoupdate.distfiles_cache value (R5.6)", got)
	}
}

// TestS060GuardDepsStandaloneSweepIsSerialByDefault pins R5.4: a standalone
// --clean sweep without an explicit --concurrency runs one directory at a
// time, whatever the flag's default, and an explicit value is honoured.
func TestS060GuardDepsStandaloneSweepIsSerialByDefault(t *testing.T) {
	o := testAutoupdateOptions()
	o.concurrency = 7
	ar := testAutoupdateRun(o)
	if got := ar.sweepConcurrency(false); got != 1 {
		t.Errorf("sweepConcurrency without --concurrency = %d, want 1 (R5.4)", got)
	}
	if got := ar.sweepConcurrency(true); got != 7 {
		t.Errorf("sweepConcurrency with --concurrency 7 = %d, want 7", got)
	}
}
