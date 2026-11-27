package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/git"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// behindCheckout makes a directory that looks like the top of a git checkout
// and a deps whose overlay runner reports it behind origin/master by behind
// commits. fetches counts the fetches the guard asked for.
func behindCheckout(t *testing.T, behind int, fetchErr error) (dir string, d *deps, fetches *int) {
	t.Helper()
	dir = t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	n := 0
	mock := git.NewMockGitRunner(dir)
	mock.CountRangeFunc = func(_ context.Context, from, _ string) (int, error) {
		if from == "HEAD" {
			return behind, nil
		}
		return 0, nil
	}
	mock.FetchFunc = func(context.Context, string) error { n++; return fetchErr }
	d = defaultDeps()
	d.overlayGit = func(string) git.GitExecutor { return mock }
	return dir, d, &n
}

func TestGuardOverlayUpstreamRefusesEveryWritingMode(t *testing.T) {
	modes := map[string]func(o *autoupdateOptions){
		"--apply":              func(o *autoupdateOptions) { o.apply = "all" },
		"--revive":             func(o *autoupdateOptions) { o.revive = "dev-libs/foo" },
		"--repair":             func(o *autoupdateOptions) { o.repair = "dev-libs/foo" },
		"--clean":              func(o *autoupdateOptions) { o.clean = true },
		"--lint --fix":         func(o *autoupdateOptions) { o.lint, o.fix = true, true },
		"--mark-auto-disabled": func(o *autoupdateOptions) { o.markAutoDisabled = true },
	}
	for name, set := range modes {
		t.Run(name, func(t *testing.T) {
			dir, d, _ := behindCheckout(t, 6, nil)
			o := testAutoupdateOptions()
			set(o)
			ar := testAutoupdateRunWith(o, d)

			err := ar.guardOverlayUpstream(t.Context(), &config.Config{}, dir)

			if !errors.Is(err, overlay.ErrCheckoutBehind) {
				t.Fatalf("guard = %v, want ErrCheckoutBehind", err)
			}
			for _, want := range []string{"6 commit(s) behind origin/master", "bentoo overlay pull", "--allow-behind"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("guard error %q is missing %q", err, want)
				}
			}
		})
	}
}

func TestGuardOverlayUpstreamAllowBehindPasses(t *testing.T) {
	dir, d, _ := behindCheckout(t, 6, nil)
	o := testAutoupdateOptions()
	o.apply, o.allowBehind = "all", true
	ar := testAutoupdateRunWith(o, d)

	var err error
	stderr := captureStderr(t, func() { err = ar.guardOverlayUpstream(t.Context(), &config.Config{}, dir) })

	if err != nil || ar.behindReadOnly {
		t.Fatalf("guard = %v, behindReadOnly = %v; want a pass with writes allowed", err, ar.behindReadOnly)
	}
	if !strings.Contains(stderr, "--allow-behind given") {
		t.Errorf("stderr = %q, want the override named", stderr)
	}
}

func TestGuardOverlayUpstreamCheckWarnsAndTurnsReadOnly(t *testing.T) {
	dir, d, fetches := behindCheckout(t, 6, nil)
	o := testAutoupdateOptions()
	o.check = true
	ar := testAutoupdateRunWith(o, d)

	var err error
	stderr := captureStderr(t, func() { err = ar.guardOverlayUpstream(t.Context(), &config.Config{}, dir) })

	if err != nil || !ar.behindReadOnly {
		t.Fatalf("guard = %v, behindReadOnly = %v; want a warning and a read-only check", err, ar.behindReadOnly)
	}
	for _, want := range []string{"6 commit(s) behind origin/master", "bentoo overlay pull", "may itself be stale", "Nothing will be written"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr is missing %q:\n%s", want, stderr)
		}
	}
	if *fetches != 0 {
		t.Errorf("fetched %d time(s) without --fetch", *fetches)
	}
}

func TestGuardOverlayUpstreamLintWithoutFixOnlyWarns(t *testing.T) {
	dir, d, _ := behindCheckout(t, 2, nil)
	o := testAutoupdateOptions()
	o.lint = true
	ar := testAutoupdateRunWith(o, d)

	var err error
	stderr := captureStderr(t, func() { err = ar.guardOverlayUpstream(t.Context(), &config.Config{}, dir) })

	if err != nil || ar.behindReadOnly {
		t.Fatalf("guard = %v, behindReadOnly = %v; want a warning only", err, ar.behindReadOnly)
	}
	if !strings.Contains(stderr, "2 commit(s) behind") {
		t.Errorf("stderr = %q, want the warning", stderr)
	}
}

func TestGuardOverlayUpstreamUpToDateSaysNothing(t *testing.T) {
	dir, d, _ := behindCheckout(t, 0, nil)
	o := testAutoupdateOptions()
	o.apply = "all"
	ar := testAutoupdateRunWith(o, d)

	var err error
	stderr := captureStderr(t, func() { err = ar.guardOverlayUpstream(t.Context(), &config.Config{}, dir) })

	if err != nil || ar.behindReadOnly || stderr != "" {
		t.Fatalf("guard = %v, behindReadOnly = %v, stderr = %q; want silence", err, ar.behindReadOnly, stderr)
	}
}

func TestGuardOverlayUpstreamSkipsWhatItCannotMeasure(t *testing.T) {
	t.Run("not a git checkout", func(t *testing.T) {
		d := defaultDeps()
		d.overlayGit = func(string) git.GitExecutor {
			t.Fatal("the runner was built for a directory with no .git")
			return nil
		}
		o := testAutoupdateOptions()
		o.apply = "all"
		if err := testAutoupdateRunWith(o, d).guardOverlayUpstream(t.Context(), &config.Config{}, t.TempDir()); err != nil {
			t.Fatalf("guard = %v, want a silent skip", err)
		}
	})
	t.Run("no upstream", func(t *testing.T) {
		dir, d, _ := behindCheckout(t, 9, nil)
		mock := git.NewMockGitRunner(dir)
		mock.UpstreamFunc = func(context.Context) (string, error) { return "", git.ErrNoUpstream }
		mock.CountRangeFunc = func(context.Context, string, string) (int, error) { return 9, nil }
		d.overlayGit = func(string) git.GitExecutor { return mock }
		o := testAutoupdateOptions()
		o.apply = "all"
		if err := testAutoupdateRunWith(o, d).guardOverlayUpstream(t.Context(), &config.Config{}, dir); err != nil {
			t.Fatalf("guard = %v, want a silent skip", err)
		}
	})
	t.Run("--list reads no overlay", func(t *testing.T) {
		dir, d, _ := behindCheckout(t, 9, nil)
		o := testAutoupdateOptions()
		o.list = true
		if err := testAutoupdateRunWith(o, d).guardOverlayUpstream(t.Context(), &config.Config{}, dir); err != nil {
			t.Fatalf("guard = %v, want no check for --list", err)
		}
	})
}

func TestGuardOverlayUpstreamFetchFailureContinuesOnTheLocalRef(t *testing.T) {
	dir, d, fetches := behindCheckout(t, 3, errors.New("network is unreachable"))
	o := testAutoupdateOptions()
	o.check, o.fetchUpstream = true, true
	ar := testAutoupdateRunWith(o, d)

	var err error
	captureStderr(t, func() {
		err = ar.guardOverlayUpstream(t.Context(), &config.Config{Overlay: config.OverlayConfig{Remote: "origin"}}, dir)
	})

	if err != nil || !ar.behindReadOnly || *fetches != 1 {
		t.Fatalf("guard = %v, behindReadOnly = %v, fetches = %d; want one failed fetch and a read-only check", err, ar.behindReadOnly, *fetches)
	}

	// A writing mode is still refused on the local ref.
	o2 := testAutoupdateOptions()
	o2.apply, o2.fetchUpstream = "all", true
	if err := testAutoupdateRunWith(o2, d).guardOverlayUpstream(t.Context(), &config.Config{}, dir); !errors.Is(err, overlay.ErrCheckoutBehind) {
		t.Fatalf("apply after a failed fetch: guard = %v, want ErrCheckoutBehind", err)
	}
}

// TestAutoupdateReconcileBehindWritesNothing: a --check from a stale checkout
// still reports the divergences but never reaches the registry writer.
func TestAutoupdateReconcileBehindWritesNothing(t *testing.T) {
	td := defaultDeps()
	auOpts := testAutoupdateOptions()
	f := allClassesFixture(t)
	setReconcileYes(t, auOpts, true)
	setReconcileWriter(td, func(string, map[string]string) error {
		t.Error("the registry writer was reached from a checkout behind its upstream")
		return nil
	})
	ar := testAutoupdateRunWith(auOpts, td)
	ar.behindReadOnly = true

	out := captureStdout(t, func() { ar.reconcileRegistryAfterCheck(f.overlayDir) })

	if !strings.Contains(out, "Registry Reconciliation") || !strings.Contains(out, "behind its upstream") {
		t.Errorf("want the divergences reported and the skip explained, got:\n%s", out)
	}
}

// TestAutoupdateBehindRefusesBeforeTheLock runs the whole command: --lint --fix
// from a stale checkout exits 1 naming the fix and leaves packages.toml alone.
func TestAutoupdateBehindRefusesBeforeTheLock(t *testing.T) {
	auOpts := testAutoupdateOptions()
	overlayDir, _ := s056AutoupdateEnv(t, auOpts)
	auOpts.check, auOpts.lint, auOpts.fix, auOpts.yes = false, true, true, true
	if err := os.Mkdir(filepath.Join(overlayDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(overlayDir, ".autoupdate", "packages.toml")
	if err := os.WriteFile(registryPath, []byte("[\"dev-libs/foo\"]\nenabled = true\nurl = \"https://example.invalid\"\nparser = \"json\"\npath = \"version\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(registryPath)
	_, d, _ := behindCheckout(t, 6, nil)

	err := runAutoupdate(testAutoupdateCmd(), nil, auOpts, d)

	if code := exitCodeFor(err); code != 1 {
		t.Fatalf("exit %d (%v), want 1", code, err)
	}
	var st *exitStatus
	if !errors.As(err, &st) || !errors.Is(st.cause, overlay.ErrCheckoutBehind) {
		t.Fatalf("err = %v, want a failure caused by ErrCheckoutBehind", err)
	}
	if _, statErr := os.Stat(filepath.Join(overlayDir, ".autoupdate.bentoo-lock")); statErr == nil {
		t.Error("the overlay lock was taken before the refusal")
	}
	after, _ := os.ReadFile(registryPath)
	if string(after) != string(before) {
		t.Errorf("packages.toml changed:\n%s", after)
	}
}

func TestGuardCompareUpstream(t *testing.T) {
	restoreFlagGlobals(t, &compareRealign, &compareDepth)
	cases := []struct {
		name        string
		realign     bool
		depth       string
		allowBehind bool
		wantErr     bool
	}{
		{"plain compare warns", false, "", false, false},
		{"--realign without --depth warns", true, "", false, false},
		{"--realign --depth is refused", true, "compile", false, true},
		{"--realign --depth --allow-behind passes", true, "compile", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, d, _ := behindCheckout(t, 6, nil)
			// Built first: registering the flags resets the variables behind them.
			cmd := subCommand(subCommand(newRootCmd(), "overlay"), "compare")
			compareRealign, compareDepth = c.realign, c.depth
			if c.allowBehind {
				if err := cmd.Flags().Set("allow-behind", "true"); err != nil {
					t.Fatal(err)
				}
			}

			var err error
			stderr := captureStderr(t, func() { err = guardCompareUpstream(t.Context(), nil, cmd, d, &config.Config{}, dir) })

			if c.wantErr {
				if !errors.Is(err, overlay.ErrCheckoutBehind) || !strings.Contains(err.Error(), "--allow-behind") {
					t.Fatalf("guard = %v, want a refusal naming --allow-behind", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("guard = %v, want a pass", err)
			}
			if !c.allowBehind && !strings.Contains(stderr, "6 commit(s) behind origin/master") {
				t.Errorf("stderr = %q, want the behind warning", stderr)
			}
		})
	}
}
