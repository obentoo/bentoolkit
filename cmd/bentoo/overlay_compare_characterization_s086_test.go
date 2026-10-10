package main

// Story 086, sub-task 5.1: characterization tests for func runCompare.
//
// These pin what runCompare does TODAY on the branches the rest of the suite
// never reached, so the gocognit refactor that follows (sub-task 5.2) can be
// checked against them: the same log lines, the same bare stderr lines, the same
// stdout and the same exit code. They are not specifications; a branch that
// looks odd is pinned as it is.
//
// Hermetic by construction: every world is built by func realignSetup (HOME,
// XDG_CONFIG_HOME and PATH under t.TempDir, both repositories `provider: local`),
// the token variables are blanked, the registry is only ever read from a cache
// file seeded under the temporary HOME, and the two cases that would otherwise
// download it run on an already-cancelled context, which the HTTP transport
// answers before dialling.

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// s086SyncBuffer is a bytes.Buffer safe for the concurrent writes a logger may
// receive under -race.
type s086SyncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *s086SyncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *s086SyncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// s086Result is everything one runCompare call made observable.
type s086Result struct {
	stdout string
	stderr string
	logs   string
	code   int
}

// s086Flags saves every package-level compare flag, restores it on cleanup, and
// sets the shipped defaults with the model switched off and a 1 s timeout.
func s086Flags(t *testing.T) {
	t.Helper()
	clone, cacheDir, noCache, timeout, token := compareClone, compareCacheDir, compareNoCache, compareTimeout, compareToken
	onlyOutdated, onlyRedundant, onlyPatched, syncFlag := compareOnlyOutdated, compareOnlyRedundant, compareOnlyPatched, compareSync
	concurrency, noReview, realign, depth, yes := compareConcurrency, compareNoReview, compareRealign, compareDepth, compareYes
	t.Cleanup(func() {
		compareClone, compareCacheDir, compareNoCache, compareTimeout, compareToken = clone, cacheDir, noCache, timeout, token
		compareOnlyOutdated, compareOnlyRedundant, compareOnlyPatched, compareSync = onlyOutdated, onlyRedundant, onlyPatched, syncFlag
		compareConcurrency, compareNoReview, compareRealign, compareDepth, compareYes = concurrency, noReview, realign, depth, yes
	})
	compareClone, compareCacheDir, compareNoCache, compareTimeout, compareToken = false, "", false, 1, ""
	compareOnlyOutdated, compareOnlyRedundant, compareOnlyPatched, compareSync = false, false, false, false
	compareConcurrency, compareNoReview, compareRealign, compareDepth, compareYes = overlay.DefaultCompareConcurrency, true, false, "", false
}

// s086World builds the realign fixture (an overlay with two packages and a
// local ::gentoo tree), blanks every token variable the run could read, and
// resets the flags.
func s086World(t *testing.T) realignFixture {
	t.Helper()
	fx := realignSetup(t, true, true)
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN", "BENTOO_REPO_GENTOO_TOKEN", "BENTOO_REPO_GURU_TOKEN"} {
		t.Setenv(k, "")
	}
	s086Flags(t)
	return fx
}

// s086Run calls runCompare with a text logger on the command's context, and
// returns stdout, stderr, the log stream and the exit code. cancelled runs it on
// a context that is already cancelled.
func s086Run(t *testing.T, args []string, cancelled bool) s086Result {
	t.Helper()
	ctx := context.Background()
	if cancelled {
		c, cancel := context.WithCancel(ctx)
		cancel()
		ctx = c
	}
	return s086RunIn(t, args, ctx)
}

// s086RunIn is s086Run on the given context.
func s086RunIn(t *testing.T, args []string, ctx context.Context) s086Result {
	t.Helper()
	buf := &s086SyncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
	compareCmd.SetContext(logging.NewContext(ctx, logger))
	t.Cleanup(func() { compareCmd.SetContext(context.Background()) })

	var res s086Result
	res.stderr = captureStderr(t, func() { //nolint:contextcheck // runCompare reads ctx from compareCmd, set above
		res.stdout = captureStdout(t, func() {
			res.code = exitCodeFor(runCompare(compareCmd, args, defaultDeps()))
		})
	})
	res.logs = buf.String()
	return res
}

// s086WantLines fails for every want that is not a substring of got, and
// requires them to appear in the given order.
func s086WantLines(t *testing.T, stream, got string, wants ...string) {
	t.Helper()
	from := 0
	for _, w := range wants {
		i := strings.Index(got[from:], w)
		if i < 0 {
			t.Errorf("%s: missing (in order) %q\n%s:\n%s", stream, w, stream, got)
			return
		}
		from += i + len(w)
	}
}

// s086Absent fails for every unwanted substring present in got.
func s086Absent(t *testing.T, stream, got string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(got, u) {
			t.Errorf("%s: unexpected %q\n%s:\n%s", stream, u, stream, got)
		}
	}
}

// s086SkipIfRoot skips a case that relies on a permission error: root reads a
// mode-000 directory.
func s086SkipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-denied fixture: root bypasses directory modes")
	}
}

// TestS086CompareUsageErrorsExitBeforeAnyWork pins the two refusals runCompare
// answers before it loads the config: an out-of-range --concurrency and a
// --depth there is nothing to prove with. Each exits 1, logs one ERROR record,
// and writes nothing to stdout or stderr.
func TestS086CompareUsageErrorsExitBeforeAnyWork(t *testing.T) {
	for _, tc := range []struct {
		name        string
		concurrency int
		realign     bool
		depth       string
		wantLog     []string
	}{
		{
			name: "concurrency zero", concurrency: 0,
			wantLog: []string{`level=ERROR msg="--concurrency must be in range [1, 100]" concurrency=0`},
		},
		{
			name: "concurrency above range", concurrency: 101,
			wantLog: []string{`level=ERROR msg="--concurrency must be in range [1, 100]" concurrency=101`},
		},
		{
			name: "depth without realign", concurrency: 4, depth: "compile",
			wantLog: []string{`level=ERROR msg="refusing --depth" err="--depth=compile was given without --realign, and there is nothing to prove`},
		},
		{
			name: "unknown depth with realign", concurrency: 4, realign: true, depth: "s086-no-such-rung",
			wantLog: []string{`level=ERROR msg="refusing --depth" err="--depth: `, `s086-no-such-rung`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s086World(t)
			compareConcurrency, compareRealign, compareDepth = tc.concurrency, tc.realign, tc.depth

			res := s086Run(t, nil, false)

			if res.code != 1 {
				t.Errorf("exit code = %d, want 1", res.code)
			}
			s086WantLines(t, "logs", res.logs, tc.wantLog...)
			if n := strings.Count(res.logs, "\n"); n != 1 {
				t.Errorf("logs carry %d records, want exactly 1:\n%s", n, res.logs)
			}
			if res.stdout != "" || res.stderr != "" {
				t.Errorf("a usage error printed output; stdout=%q stderr=%q", res.stdout, res.stderr)
			}
		})
	}
}

// TestS086CompareConfigLoadFailureExitsOne pins the config branch: an overlay
// path that does not exist fails loadAppContext, which is logged and exits 1.
func TestS086CompareConfigLoadFailureExitsOne(t *testing.T) {
	s086World(t)
	missing := filepath.Join(t.TempDir(), "no-such-overlay")
	cfg := "overlay:\n  path: " + missing + "\n  remote: origin\n" +
		"git:\n  user: Test\n  email: test@test.com\n"
	cfgPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bentoo", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	res := s086Run(t, nil, false)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1", res.code)
	}
	s086WantLines(t, "logs", res.logs,
		`level=ERROR msg="loading config: failed" err="`+config.ErrOverlayPathNotFound.Error())
	s086Absent(t, "logs", res.logs, "Scanning Bentoo overlay", "Repository not found.")
	if res.stdout != "" {
		t.Errorf("stdout = %q, want empty", res.stdout)
	}
}

// TestS086CompareRegistryInitFailureExitsOne pins the registry constructor's
// failure: ~/.cache is a regular file, so the cache directory cannot be made.
func TestS086CompareRegistryInitFailureExitsOne(t *testing.T) {
	s086World(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".cache"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write ~/.cache: %v", err)
	}

	res := s086Run(t, nil, false)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1", res.code)
	}
	s086WantLines(t, "logs", res.logs,
		`level=ERROR msg="Failed to initialize repository registry" err="failed to create cache directory: `)
	s086Absent(t, "logs", res.logs, "Scanning Bentoo overlay")
	if res.stdout != "" || res.stderr != "" {
		t.Errorf("stdout=%q stderr=%q, want both empty", res.stdout, res.stderr)
	}
}

// TestS086CompareSyncInterruptedExitsOne pins --sync on a cancelled context: the
// registry download returns the cancellation, which runCompare reports as the
// interrupted-registry line and exits 1, without the generic sync failure line.
func TestS086CompareSyncInterruptedExitsOne(t *testing.T) {
	s086World(t)
	compareSync = true

	res := s086Run(t, nil, true)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1", res.code)
	}
	s086WantLines(t, "logs", res.logs, `level=ERROR msg="`+registryInterruptedMsg+`"`)
	s086Absent(t, "logs", res.logs, "Failed to sync repository list", "Repository not found.", "Scanning Bentoo overlay")
	if res.stdout != "" || res.stderr != "" {
		t.Errorf("stdout=%q stderr=%q, want both empty", res.stdout, res.stderr)
	}
}

// TestS086CompareSyncFailureExitsOne pins a --sync whose download fails for a
// reason other than cancellation. The context's deadline has already passed, so
// the transport answers before dialling with context.DeadlineExceeded, which is
// not context.Canceled: the generic sync failure line is logged, exit 1.
func TestS086CompareSyncFailureExitsOne(t *testing.T) {
	s086World(t)
	compareSync = true
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	t.Cleanup(cancel)

	res := s086RunIn(t, nil, ctx)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1", res.code)
	}
	s086WantLines(t, "logs", res.logs,
		`level=ERROR msg="Failed to sync repository list" err="fetching registry `,
		`context deadline exceeded`)
	s086Absent(t, "logs", res.logs, registryInterruptedMsg, "Repository not found.", "Scanning Bentoo overlay")
	if res.stdout != "" || res.stderr != "" {
		t.Errorf("stdout=%q stderr=%q, want both empty", res.stdout, res.stderr)
	}
}

// TestS086CompareInvalidProviderExitsOne pins a configured repository whose
// provider names no known kind: the provider factory refuses it, exit 1.
func TestS086CompareInvalidProviderExitsOne(t *testing.T) {
	fx := s086World(t)
	cfg := "overlay:\n  path: " + fx.overlayPath + "\n  remote: origin\n" +
		"git:\n  user: Test\n  email: test@test.com\n" +
		"repositories:\n  gentoo:\n    provider: s086-bogus\n    path: " + fx.gentooPath + "\n"
	cfgPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bentoo", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	res := s086Run(t, nil, false)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1\nlogs:\n%s", res.code, res.logs)
	}
	s086WantLines(t, "logs", res.logs,
		`level=ERROR msg="Failed to create provider" err="invalid provider type: s086-bogus"`)
	s086Absent(t, "logs", res.logs, "Scanning Bentoo overlay")
	if res.stdout != "" || res.stderr != "" {
		t.Errorf("stdout=%q stderr=%q, want both empty", res.stdout, res.stderr)
	}
}

// TestS086CompareUnknownRepositoryHints pins the not-found branch and its three
// hint shapes. The registry is read from a cache file seeded under HOME, fresh
// enough that nothing is downloaded. A registry that lists repositories gives
// the eselect hint; one that lists none, or cannot be parsed, gives the
// "unavailable" hint instead. The config's names are listed in every case.
func TestS086CompareUnknownRepositoryHints(t *testing.T) {
	const listed = `<?xml version="1.0" encoding="UTF-8"?>
<repositories version="1.0">
  <repo quality="experimental" status="unofficial">
    <name>s086-registered</name>
    <source type="git">https://example.invalid/s086-registered.git</source>
  </repo>
</repositories>
`
	const (
		availableHint   = `level=INFO msg="Registry repositories: use ` + "`eselect repository list`" + ` to see all available"`
		unavailableHint = `level=INFO msg="Registry unavailable. Use --sync to refresh or run ` + "`eselect repository list`" + `"`
	)
	for _, tc := range []struct {
		name       string
		registry   string
		wantHint   string
		absentHint string
	}{
		{name: "registry lists repositories", registry: listed, wantHint: availableHint, absentHint: unavailableHint},
		{name: "registry lists none", registry: `<?xml version="1.0"?><repositories version="1.0"></repositories>`, wantHint: unavailableHint, absentHint: availableHint},
		{name: "registry unparsable", registry: "<repositories><repo>", wantHint: unavailableHint, absentHint: availableHint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s086World(t)
			cacheDir := filepath.Join(os.Getenv("HOME"), ".cache", "bentoo")
			if err := os.MkdirAll(cacheDir, 0o750); err != nil {
				t.Fatalf("mkdir cache: %v", err)
			}
			if err := os.WriteFile(filepath.Join(cacheDir, "repositories.xml"), []byte(tc.registry), 0o600); err != nil {
				t.Fatalf("seed registry: %v", err)
			}

			res := s086Run(t, []string{"s086-unknown"}, false)

			if res.code != 1 {
				t.Errorf("exit code = %d, want 1", res.code)
			}
			s086WantLines(t, "logs", res.logs,
				`level=ERROR msg="Repository not found." repository=s086-unknown`,
				`level=INFO msg="Config repositories" repositories="gentoo, guru"`,
				tc.wantHint)
			s086Absent(t, "logs", res.logs, tc.absentHint, registryInterruptedMsg, "Scanning Bentoo overlay")
			if res.stdout != "" || res.stderr != "" {
				t.Errorf("stdout=%q stderr=%q, want both empty", res.stdout, res.stderr)
			}
		})
	}
}

// TestS086CompareUnreadableSecretsWarnsAndCompletes pins the token fallback: a
// user secrets file that cannot be read (here a directory) warns once per
// configured repository and once for the GitHub token, and the run still
// completes with exit 0.
func TestS086CompareUnreadableSecretsWarnsAndCompletes(t *testing.T) {
	s086World(t)
	if err := os.MkdirAll(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bentoo", "secrets"), 0o750); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}

	res := s086Run(t, nil, false)

	if res.code != 0 {
		t.Errorf("exit code = %d, want 0\nlogs:\n%s", res.code, res.logs)
	}
	if n := strings.Count(res.logs, `msg="resolving token for repository: failed; treating it as unset"`); n != 2 {
		t.Errorf("per-repository token warnings = %d, want 2 (gentoo, guru)\nlogs:\n%s", n, res.logs)
	}
	s086WantLines(t, "logs", res.logs,
		`level=WARN msg="resolving GitHub token: failed; continuing with unauthenticated GitHub API access" err="secrets: file present but unreadable: `,
		`level=INFO msg="Scanning Bentoo overlay"`,
		`level=INFO msg="Comparing with upstream" repository=gentoo`)
	s086WantLines(t, "stderr", res.stderr, "Found 2 packages in Bentoo overlay")
	s086WantLines(t, "stdout", res.stdout, "media-libs/gst-plugins-qt6")
}

// TestS086CompareTokenFlagCompletesWithoutTokenWarning pins --token on a local
// provider: the flag short-circuits the secrets chain (no token warning even
// with an unreadable secrets file in place), the run completes with exit 0,
// and the token is printed nowhere.
func TestS086CompareTokenFlagCompletesWithoutTokenWarning(t *testing.T) {
	s086World(t)
	if err := os.MkdirAll(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bentoo", "secrets"), 0o750); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	const token = "s086-flag-token-value"
	compareToken = token

	res := s086Run(t, nil, false)

	if res.code != 0 {
		t.Errorf("exit code = %d, want 0\nlogs:\n%s", res.code, res.logs)
	}
	s086Absent(t, "logs", res.logs, "resolving GitHub token", token)
	s086Absent(t, "stdout", res.stdout, token)
	s086Absent(t, "stderr", res.stderr, token)
	s086WantLines(t, "stderr", res.stderr, "Found 2 packages in Bentoo overlay")
}

// TestS086CompareScanFailureExitsOne pins an overlay that passes validation but
// cannot be listed (write+search, no read): the scan fails, is logged, exits 1.
func TestS086CompareScanFailureExitsOne(t *testing.T) {
	s086SkipIfRoot(t)
	fx := s086World(t)
	if err := os.Chmod(fx.overlayPath, 0o300); err != nil {
		t.Fatalf("chmod overlay: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(fx.overlayPath, 0o750) })

	res := s086Run(t, nil, false)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1\nlogs:\n%s", res.code, res.logs)
	}
	s086WantLines(t, "logs", res.logs,
		`level=INFO msg="Scanning Bentoo overlay" path=`+fx.overlayPath,
		`level=ERROR msg="scanning overlay: failed" err="open `+fx.overlayPath+`: permission denied"`)
	s086Absent(t, "logs", res.logs, "Comparing with upstream")
	if res.stdout != "" || res.stderr != "" {
		t.Errorf("stdout=%q stderr=%q, want both empty", res.stdout, res.stderr)
	}
}

// TestS086CompareScanErrorsWarnAndComplete pins a partial scan: an unreadable
// category is counted in one WARN record, each error is a DEBUG record, and the
// comparison proceeds over the packages that were read, exiting 0.
func TestS086CompareScanErrorsWarnAndComplete(t *testing.T) {
	s086SkipIfRoot(t)
	fx := s086World(t)
	locked := filepath.Join(fx.overlayPath, "dev-util")
	if err := os.MkdirAll(locked, 0o750); err != nil {
		t.Fatalf("mkdir category: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod category: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o750) })

	res := s086Run(t, nil, false)

	if res.code != 0 {
		t.Errorf("exit code = %d, want 0\nlogs:\n%s", res.code, res.logs)
	}
	s086WantLines(t, "logs", res.logs,
		`level=WARN msg="Encountered errors during scan" errors=1`,
		`level=DEBUG msg="scan error" path=`+locked+` message="open `+locked+`: permission denied"`,
		`level=INFO msg="Comparing with upstream"`)
	s086WantLines(t, "stderr", res.stderr, "Found 2 packages in Bentoo overlay")
}

// TestS086CompareInterruptedComparisonExitsOne pins a comparison whose context
// is already cancelled: config and scan succeed, CompareWithProvider returns the
// cancellation, which is NOT a rate limit, so the generic failure line is
// logged and the run exits 1 with nothing on stdout (no progress, no report).
func TestS086CompareInterruptedComparisonExitsOne(t *testing.T) {
	s086World(t)

	res := s086Run(t, nil, true)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1\nlogs:\n%s", res.code, res.logs)
	}
	s086WantLines(t, "logs", res.logs,
		`level=INFO msg="Scanning Bentoo overlay"`,
		`level=INFO msg="Comparing with upstream" repository=gentoo`,
		`level=ERROR msg="comparing packages: failed" err="context canceled"`)
	s086Absent(t, "logs", res.logs, "GitHub API rate limit exceeded.")
	s086WantLines(t, "stderr", res.stderr, "Found 2 packages in Bentoo overlay")
	s086Absent(t, "stderr", res.stderr, "Try using --clone flag")
	if res.stdout != "" {
		t.Errorf("stdout = %q, want empty", res.stdout)
	}
}
