package autoupdate

// Authored for story 092 (autoupdate-apply-observable), sub-task 2.1: a failed
// manifest run and its post-fix re-check travel as two typed *ChildRunError
// values in the error chain (R3.1), each with its whole output kept in a log
// file under the autoupdate state directory (R3.3).
//
// Hermetic: temp overlay/config/staging/host distdir (WithApplierDistdir), the
// fix sandbox root overridden and restored, a fake fixer, and `pkgdev` faked
// through the exec seam — never the network, real claude or $HOME/.config.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// s092ChildRuns walks the whole chain (single and multi Unwrap) and returns
// every distinct *ChildRunError in it.
func s092ChildRuns(err error) []*ChildRunError {
	var out []*ChildRunError
	seen := map[*ChildRunError]bool{}
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if c, ok := e.(*ChildRunError); ok && !seen[c] { //nolint:errorlint // walking the chain by hand
			seen[c] = true
			out = append(out, c)
		}
		switch u := e.(type) { //nolint:errorlint // the Unwrap shape itself is inspected
		case interface{ Unwrap() []error }:
			for _, x := range u.Unwrap() {
				walk(x)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	return out
}

// s092Output renders n numbered lines tagged with tag, ending with the fetch
// diagnostic that keeps the failure repairable (it reaches the fixer).
func s092Output(tag string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s line %03d\n", tag, i)
	}
	b.WriteString("SRC_URI is unreachable: 404 Not Found")
	return b.String()
}

// s092ApplyFailingTwice applies pkg (oldV -> newV) on the staged pipeline with
// a pkgdev that fails on the first run printing first and on the re-check
// printing recheck, and a fixer that succeeds in between. It returns the
// apply error and the config (state) directory.
func s092ApplyFailingTwice(t *testing.T, configDir, pkg, oldV, newV, first, recheck string) error {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	host := filepath.Join(tmp, "host-distdir")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	createTestEbuildFile(t, overlayDir, pkg, oldV)
	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: oldV, NewVersion: newV, Status: StatusPending})

	var mu sync.Mutex
	calls := 0
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		mu.Lock()
		calls++
		out := first
		if calls > 1 {
			out = recheck
		}
		mu.Unlock()
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' "$0"; exit 1`, out)
	}
	fx := &fakeFixer{summary: "rewrote SRC_URI"}
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithApplierFixer(fx),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierDistdir(host, ""),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	_, applyErr := applier.Apply(t.Context(), pkg, false)
	if fx.called != 1 {
		t.Fatalf("the fixer ran %d time(s), want 1: the re-check is what this test is about", fx.called)
	}
	if applyErr == nil {
		t.Fatalf("Apply succeeded; want the re-check failure")
	}
	return applyErr
}

func s092SandboxRoot(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	prev := fixSandboxRoot
	fixSandboxRoot = func(context.Context) string { return root }
	t.Cleanup(func() { fixSandboxRoot = prev })
}

// R3.1/R3.3: two runs, two typed errors, each with its OWN output and its OWN
// log file. Hostile halves: the two runs never collapse into one (shared
// output, shared or overwritten log file), and one run never splits into two
// (exactly two values in the chain). The error text no longer embeds the
// output, and ErrManifestFailed still classifies the failure.
func TestChildRunErrorFirstRunAndRecheck(t *testing.T) {
	isolateSecretsPaths(t)
	s092SandboxRoot(t)
	configDir := filepath.Join(t.TempDir(), "config")
	first, recheck := s092Output("FIRST-RUN", 60), s092Output("RECHECK-RUN", 60)
	applyErr := s092ApplyFailingTwice(t, configDir, "dev-go/govulncheck", "1.1.3", "1.1.4", first, recheck)

	if !errors.Is(applyErr, ErrManifestFailed) {
		t.Errorf("the failure no longer matches ErrManifestFailed: %v", applyErr)
	}
	runs := s092ChildRuns(applyErr)
	if len(runs) != 2 {
		t.Fatalf("want exactly 2 *ChildRunError in the chain, got %d: %v", len(runs), applyErr)
	}
	byLabel := map[string]*ChildRunError{}
	for _, r := range runs {
		byLabel[r.Label] = r
	}
	f, rc := byLabel["first manifest run"], byLabel["re-check after LLM fix"]
	if f == nil || rc == nil {
		t.Fatalf("labels = %q, %q; want %q and %q", runs[0].Label, runs[1].Label, "first manifest run", "re-check after LLM fix")
	}
	if fo := string(f.Output); !strings.Contains(fo, "FIRST-RUN line 060") || strings.Contains(fo, "RECHECK-RUN") {
		t.Errorf("first run's Output is not its own output alone:\n%s", fo)
	}
	if ro := string(rc.Output); !strings.Contains(ro, "RECHECK-RUN line 060") || strings.Contains(ro, "FIRST-RUN") {
		t.Errorf("re-check's Output is not its own output alone:\n%s", ro)
	}
	if f.LogPath == "" || rc.LogPath == "" || f.LogPath == rc.LogPath {
		t.Fatalf("log paths must be set and distinct: first=%q recheck=%q", f.LogPath, rc.LogPath)
	}
	for _, r := range []*ChildRunError{f, rc} {
		rel, err := filepath.Rel(configDir, r.LogPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("%s log %q is not under the state dir %q", r.Label, r.LogPath, configDir)
		}
		body, err := os.ReadFile(r.LogPath)
		if err != nil {
			t.Errorf("%s log: %v", r.Label, err)
			continue
		}
		if !strings.Contains(string(body), strings.TrimSpace(string(r.Output))) {
			t.Errorf("%s log does not hold the whole output", r.Label)
		}
		if fi, err := os.Stat(r.LogPath); err == nil && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s log mode = %v, want 0600", r.Label, fi.Mode().Perm())
		}
	}
	msg := applyErr.Error()
	for _, leak := range []string{"FIRST-RUN line 002", "FIRST-RUN line 059", "RECHECK-RUN line 002", "RECHECK-RUN line 059"} {
		if strings.Contains(msg, leak) {
			t.Errorf("the error text still embeds child output (%q)", leak)
		}
	}
}

// R3.3 derived value: the log path. Hostile half: the same package NAME in two
// categories at the same version must not share a log file — the second apply
// would silently overwrite the first one's only full copy.
func TestChildRunErrorLogPathsDistinctAcrossCategories(t *testing.T) {
	isolateSecretsPaths(t)
	s092SandboxRoot(t)
	configDir := filepath.Join(t.TempDir(), "config")
	errA := s092ApplyFailingTwice(t, configDir, "dev-go/vuln", "1.0", "1.1", s092Output("CAT-A-FIRST", 3), s092Output("CAT-A-RECHECK", 3))
	errB := s092ApplyFailingTwice(t, configDir, "dev-python/vuln", "1.0", "1.1", s092Output("CAT-B-FIRST", 3), s092Output("CAT-B-RECHECK", 3))
	paths := map[string]string{}
	for _, e := range []error{errA, errB} {
		for _, r := range s092ChildRuns(e) {
			if prev, dup := paths[r.LogPath]; dup {
				t.Errorf("two runs share log %q (%s and %s)", r.LogPath, prev, r.Label)
			}
			paths[r.LogPath] = r.Label
			body, err := os.ReadFile(r.LogPath)
			if err != nil {
				t.Errorf("read %s: %v", r.LogPath, err)
				continue
			}
			if !strings.Contains(string(body), strings.TrimSpace(string(r.Output))) {
				t.Errorf("log %s was overwritten by another run", r.LogPath)
			}
		}
	}
	if len(paths) != 4 {
		t.Errorf("want 4 distinct log files for 2 packages x 2 runs, got %d", len(paths))
	}
}
