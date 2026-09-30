//go:build unix

package main

// Authored for story 058, sub-task 4.3 — R1.1, R1.2, R1.3, R2.5, R4.4.
//
// overlay prune and overlay manifest are RunE commands and keep their codes,
// and a hang-up cancels a manifest run exactly as SIGINT does. The SIGHUP
// proof runs in a re-exec'd child that ends through exitProcess(runMain(root))
// with a slow pkgdev stub on PATH, so the signal lands mid-work.
//
// Red on arrival: both commands are still Run commands, and runManifest builds
// its own signal context without SIGHUP, so a hang-up kills the process.

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestS058PruneAndManifestReturnThroughRunE(t *testing.T) {
	if bad := s058NotOnRunE(t, "overlay prune", "overlay manifest"); len(bad) > 0 {
		t.Errorf("these commands still end the process themselves instead of returning their outcome (R1.1):\n  %s", strings.Join(bad, "\n  "))
	}
}

func s058PruneManifestRows() []s058Row {
	return []s058Row{
		// Would wrongly become 1: an empty overlay is a successful prune.
		{name: "prune on an overlay with no packages", args: []string{"overlay", "prune"}, want: 0},
		{name: "prune with a restriction that matches nothing", args: []string{"overlay", "prune", "nosuch-cat"}, want: 1, once: `"nosuch-cat" matches no package in the overlay`},
		{name: "prune with two restrictions", args: []string{"overlay", "prune", "a", "b"}, want: 1, usage: true},
		{name: "manifest with nothing to update", args: []string{"overlay", "manifest"}, want: 1, once: "no packages found to update"},
		{name: "manifest with two scopes", args: []string{"overlay", "manifest", "a", "b"}, want: 1, usage: true},
	}
}

func TestS058PruneAndManifestKeepTheirExitCodes(t *testing.T) {
	s058RunRows(t, s058PruneManifestRows())
}

const (
	s058ManifestRoleEnv = "BENTOO_TEST_S058_MANIFEST_ROLE"
	s058ManifestArgsEnv = "BENTOO_TEST_S058_MANIFEST_ARGS"
)

// TestS058HelperManifestChild is not a test: in the child role it runs the
// production tree with the arguments its parent gave and ends through the
// production exit path.
func TestS058HelperManifestChild(t *testing.T) {
	if os.Getenv(s058ManifestRoleEnv) == "" {
		t.Skip("child role only")
	}
	root := newRootCmd()
	root.SetArgs(strings.Split(os.Getenv(s058ManifestArgsEnv), "\x1f"))
	exitProcess(runMain(root))
	os.Exit(s058ChildReturned)
}

// TestS058ManifestIsCancelledBySIGHUP (R4.4): a hang-up that lands while pkgdev
// runs cancels the manifest run the way SIGINT does — the process is not killed
// by the signal, and it exits 1, the code of an interrupted manifest run.
func TestS058ManifestIsCancelledBySIGHUP(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			c := s058Env(t)
			for _, pkg := range []string{"app-misc/jq", "dev-lang/go"} {
				writeExitTestEbuild(t, c.Overlay(), pkg, "1.0.0")
			}
			marker := stubSlowPkgdev(t)
			ch := s058StartChild(t, "TestS058HelperManifestChild", s058ManifestRoleEnv, "run",
				s058ManifestArgsEnv+"="+strings.Join([]string{"overlay", "manifest", "--ui=plain"}, "\x1f"),
				"HOME="+c.Home(),
				"XDG_CONFIG_HOME="+os.Getenv("XDG_CONFIG_HOME"),
				"XDG_STATE_HOME="+os.Getenv("XDG_STATE_HOME"),
				"XDG_CACHE_HOME="+os.Getenv("XDG_CACHE_HOME"),
				"XDG_DATA_HOME="+os.Getenv("XDG_DATA_HOME"),
			)

			deadline := time.After(15 * time.Second)
		started:
			for {
				if _, err := os.Stat(marker); err == nil {
					break started
				}
				select {
				case <-ch.done:
					t.Fatalf("the manifest run ended before pkgdev started:\n%s", ch.out.String())
				case <-deadline:
					t.Fatalf("pkgdev never started; the run cannot be interrupted mid-work:\n%s", ch.out.String())
				case <-time.After(25 * time.Millisecond):
				}
			}

			if err := ch.cmd.Process.Signal(sig); err != nil {
				t.Fatalf("signalling the child: %v", err)
			}
			ch.awaitExit(t, 15*time.Second, "the signal did not cancel the manifest run")
			code, got, signaled := ch.status()
			if signaled {
				t.Fatalf("%v killed the process by its default action (%v) instead of cancelling the manifest run; its pkgdev child is left orphaned:\n%s", sig, got, ch.out.String())
			}
			if code == s058ChildReturned {
				t.Fatalf("exitProcess(runMain(root)) returned instead of ending the process:\n%s", ch.out.String())
			}
			if code != 1 {
				t.Errorf("the interrupted manifest run exited %d, want 1:\n%s", code, ch.out.String())
			}
		})
	}
}
