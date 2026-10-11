package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// TestRepairSkipsRevalidationAfterTermOrHangup: a SIGTERM or SIGHUP sent to
// bentoo alone during the session leaves the session running to its end, then
// skips the re-validation: exit 1 naming the signal, bundle byte-identical,
// nothing published.
func TestRepairSkipsRevalidationAfterTermOrHangup(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			sink := signalSink(t, sig)
			auOpts := testAutoupdateOptions()
			f := repairFixture(t, auOpts, 0)
			release := filepath.Join(t.TempDir(), "release")
			writeStub(t, f.toolDir, "claude", fmt.Sprintf(
				"echo $$ > %q.pid\nwhile [ ! -e %q ]; do sleep 0.02; done\necho done > %q.done\nexit 0\n", f.record, release, f.record))
			before := readFileOrEmpty(f.bundlePath)

			go func() {
				deadline := time.Now().Add(20 * time.Second)
				for readFileOrEmpty(f.record+".pid") == "" && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				_ = syscall.Kill(os.Getpid(), sig)
				select {
				case <-sink:
				case <-time.After(2 * time.Second):
				}
				_ = os.WriteFile(release, nil, 0o600)
			}()
			code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")

			if strings.TrimSpace(readFileOrEmpty(f.record+".done")) != "done" {
				t.Fatalf("the session did not run to its end:\n%s", out)
			}
			if code != 1 || !strings.Contains(out, "re-validation was skipped because bentoo received "+sig.String()) {
				t.Fatalf("exit %d, want 1 naming %v:\n%s", code, sig, out)
			}
			if readFileOrEmpty(f.bundlePath) != before {
				t.Fatalf("the bundle changed although the re-validation was skipped")
			}
			if _, err := os.Stat(filepath.Join(f.overlay, "dev-go", "repairdemo", "repairdemo-"+repairCand+".ebuild")); err == nil {
				t.Fatalf("the candidate was published although the re-validation was skipped")
			}
		})
	}
}

// TestRepairSessionDoesNotInheritSecrets: the session sees the operator's
// ordinary variables but none bentoo reads a secret from.
func TestRepairSessionDoesNotInheritSecrets(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 0)
	writeStub(t, f.toolDir, "claude", fmt.Sprintf("env > %q.env\nexit 0\n", f.record))
	t.Setenv("BENTOO_FETCH_REPAIR_SERIAL", "serial-value")
	t.Setenv("GITHUB_TOKEN", "github-value")
	t.Setenv("REPAIR_ORDINARY", "ordinary-value")
	runRepair(t, auOpts, ttyDeps(true, true), "y\n")
	env := readFileOrEmpty(f.record + ".env")
	if !strings.Contains(env, "REPAIR_ORDINARY=ordinary-value") {
		t.Fatalf("the session lost an ordinary variable:\n%s", env)
	}
	for _, leak := range []string{"serial-value", "github-value"} {
		if strings.Contains(env, leak) {
			t.Fatalf("the session inherited a secret (%s):\n%s", leak, env)
		}
	}
}

// TestRepairSecretEnvNames: the config's key variables, the fixed tokens and
// every BENTOO_* name in the environment are secret; nothing else is.
func TestRepairSecretEnvNames(t *testing.T) {
	cfg := config.AutoupdateConfig{LLM: config.LLMConfig{APIKeyEnv: "MY_LLM_KEY"}, Search: config.SearchConfig{APIKeyEnv: "MY_SEARCH_KEY"}}
	got := repairSecretEnvNames([]string{"PATH=/bin", "BENTOO_REPO_X_TOKEN=a", "BENTOO_FETCH_S=b", "HOME=/h"}, cfg)
	for _, want := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "MY_LLM_KEY", "MY_SEARCH_KEY", "BENTOO_REPO_X_TOKEN", "BENTOO_FETCH_S"} {
		if !slices.Contains(got, want) {
			t.Errorf("secret names lack %s: %q", want, got)
		}
	}
	for _, keep := range []string{"PATH", "HOME"} {
		if slices.Contains(got, keep) {
			t.Errorf("secret names include %s: %q", keep, got)
		}
	}
}

// TestRepairCancelledRunOffersNoSession: a run whose context is already
// cancelled stops before the summary, with the bundle untouched.
func TestRepairCancelledRunOffersNoSession(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 0)
	before := readFileOrEmpty(f.bundlePath)
	d := ttyDeps(true, true)
	asked := false
	d.confirmRepair = func(string) bool { asked = true; return true }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := testAutoupdateRunWith(auOpts, d).runRepair(ctx, f.overlay, f.configDir, config.AutoupdateConfig{})
	st, bare := err.(*exitStatus) //nolint:errorlint // the same identity test as func execute
	if !bare || st.cause == nil || exitCodeFor(err) != 1 || !strings.Contains(st.cause.Error(), "interrupted") {
		t.Fatalf("err = %#v (exit %d), want exit 1 saying it was interrupted", err, exitCodeFor(err))
	}
	if asked {
		t.Fatalf("a cancelled run asked the session question")
	}
	assertNoSession(t, f)
	if readFileOrEmpty(f.bundlePath) != before {
		t.Fatalf("a cancelled run changed the bundle")
	}
}
