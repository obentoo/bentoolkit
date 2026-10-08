// Story 081 — sub-task 2.1. The LLM manifest repair of a STAGED apply receives
// the first attempt's completed downloads, as real files, before the fixer runs
// (R1.1, R1.3, R1.4, R1.5, R3.1, R3.2). The cache copy (old R2) was moved to a
// later story; its tests are parked in .draft/parked-for-cache-copy-story/.
//
// Every assertion is made on what req.DistDir holds WHEN THE FIXER IS CALLED
// (os.Lstat: regular files, no symlink), never via gateDistdirFrom, which skips
// without sudo/doas.
//
// FIXTURE CONTRACT (story 043 style): a temp host distdir
// (WithApplierDistdir), a temp distfiles cache (WithApplierDistfilesCache), and
// fixSandboxRoot overridden with a t.Cleanup restore — never the host DISTDIR or
// $HOME/.config.
//
// WHY TWO ENTRY POINTS. The carry-over test goes through Apply with a real
// staging root: that is the production shape. The carry-failure test calls
// runManifestWithFix directly with a staged candidate, because it needs a
// pkgdev double that sabotages its own distdir and nothing else of Apply's
// pipeline; the staged branch it exercises is the same one.

package autoupdate

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
)

const (
	s081Pkg  = "app-misc/b"
	s081From = "1.4"
	s081To   = "2.1" // no 3 in it, so the carried count is an unambiguous token
)

// --- an all-levels capturing slog handler ------------------------------------

type s081Record struct {
	level slog.Level
	msg   string
	attrs map[string]slog.Value
}

type s081LogSink struct {
	mu      sync.Mutex
	records []s081Record
}

type s081Handler struct {
	sink  *s081LogSink
	attrs []slog.Attr
}

func (h s081Handler) Enabled(context.Context, slog.Level) bool { return true }

func (h s081Handler) Handle(_ context.Context, r slog.Record) error {
	rec := s081Record{level: r.Level, msg: r.Message, attrs: map[string]slog.Value{}}
	for _, a := range h.attrs {
		rec.attrs[a.Key] = a.Value.Resolve()
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.Resolve()
		return true
	})
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	h.sink.records = append(h.sink.records, rec)
	return nil
}

func (h s081Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return s081Handler{sink: h.sink, attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}

func (h s081Handler) WithGroup(string) slog.Handler { return h }

func (s *s081LogSink) logger() *slog.Logger { return slog.New(s081Handler{sink: s}) }

func (s *s081LogSink) snapshot() []s081Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]s081Record{}, s.records...)
}

func s081AttrString(v slog.Value) string {
	if v.Kind() == slog.KindAny {
		if err, ok := v.Any().(error); ok && err != nil {
			return err.Error()
		}
	}
	return v.String()
}

func s081AttrInt(v slog.Value) (int64, bool) {
	switch v.Kind() {
	case slog.KindInt64:
		return v.Int64(), true
	case slog.KindUint64:
		return int64(v.Uint64()), true //nolint:gosec // G115: test counts are tiny
	default:
		return 0, false
	}
}

func s081ForPackage(r s081Record) bool {
	return s081AttrString(r.attrs["package"]) == s081Pkg && s081AttrString(r.attrs["version"]) == s081To
}

// s081AssertPreparedEvent is R3.1: exactly one INFO event for the package and
// version carrying the carried-over count. The attribute key is matched loosely
// ("carr…") so the test pins the content, not a spelling.
func s081AssertPreparedEvent(t *testing.T, sink *s081LogSink, wantCarried int64) {
	t.Helper()
	var matches []s081Record
	for _, r := range sink.snapshot() {
		if r.level != slog.LevelInfo || !s081ForPackage(r) {
			continue
		}
		for k, v := range r.attrs {
			n, ok := s081AttrInt(v)
			if !ok || !strings.Contains(k, "carr") {
				continue
			}
			if n != wantCarried {
				t.Errorf("the prepared-distdir INFO event says %s=%d, want %d: %q %v", k, n, wantCarried, r.msg, r.attrs)
			}
			matches = append(matches, r)
			break
		}
	}
	if len(matches) != 1 {
		t.Errorf("got %d INFO event(s) carrying the package, version and the carried count, want exactly 1 (R3.1)", len(matches))
	}
}

// s081AssertWarn is R1.5: one WARN event for the package and version naming
// the file and the error.
func s081AssertWarn(t *testing.T, sink *s081LogSink, file string) {
	t.Helper()
	n := 0
	for _, r := range sink.snapshot() {
		if r.level != slog.LevelWarn || !s081ForPackage(r) {
			continue
		}
		namesFile, hasErr := false, false
		for k, v := range r.attrs {
			s := s081AttrString(v)
			if s == file {
				namesFile = true
			}
			if strings.Contains(k, "err") && s != "" {
				hasErr = true
			}
		}
		if namesFile && hasErr {
			n++
		}
	}
	if n != 1 {
		t.Errorf("got %d WARN event(s) for %s naming file %q and an error, want exactly 1; records: %+v",
			n, s081Pkg, file, sink.snapshot())
	}
}

// s081AssertReporterLine is R3.2: one reporter line, sent before the fixer ran,
// naming the package and stating the carried-over number.
func s081AssertReporterLine(t *testing.T, before []string, carried string) {
	t.Helper()
	re := regexp.MustCompile(`\b` + carried + `\b`)
	n := 0
	for _, ev := range before {
		if strings.HasPrefix(ev, "Log:") && strings.Contains(ev, s081Pkg) && re.MatchString(ev) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("got %d reporter line(s) before the fixer naming %s with %s, want exactly 1 (R3.2); events: %v",
			n, s081Pkg, carried, before)
	}
}

// --- what the fixer saw -------------------------------------------------------

type s081Seen struct {
	distDir      string
	entries      map[string]os.FileMode
	contents     map[string]string
	reporterSeen []string
}

// s081Observe makes the fake fixer record req.DistDir as it stands when called.
func s081Observe(t *testing.T, fx *fakeFixer, rec *recordingReporter, also func(req fixer.ManifestFixRequest)) *s081Seen {
	t.Helper()
	seen := &s081Seen{entries: map[string]os.FileMode{}, contents: map[string]string{}}
	fx.onCall = func(req fixer.ManifestFixRequest) {
		seen.distDir = req.DistDir
		seen.reporterSeen = rec.snapshot()
		entries, err := os.ReadDir(req.DistDir)
		if err != nil {
			t.Errorf("reading the fix distdir: %v", err)
			return
		}
		for _, e := range entries {
			p := filepath.Join(req.DistDir, e.Name())
			info, err := os.Lstat(p)
			if err != nil {
				t.Errorf("lstat %s: %v", p, err)
				continue
			}
			seen.entries[e.Name()] = info.Mode()
			if info.Mode().IsRegular() {
				b, err := os.ReadFile(p)
				if err != nil {
					t.Errorf("read %s: %v", p, err)
				}
				seen.contents[e.Name()] = string(b)
			}
		}
		if also != nil {
			also(req)
		}
	}
	return seen
}

// s081AssertHolds asserts the fixer saw exactly want (name -> bytes), every
// entry a regular file.
func s081AssertHolds(t *testing.T, seen *s081Seen, want map[string]string) {
	t.Helper()
	for name, mode := range seen.entries {
		if mode&os.ModeSymlink != 0 {
			t.Errorf("the fix distdir held a symlink %s when the fixer ran (R1.3): the agent's Write would go through it", name)
		} else if !mode.IsRegular() {
			t.Errorf("the fix distdir held a non-regular entry %s (%v) when the fixer ran", name, mode)
		}
		if _, ok := want[name]; !ok {
			t.Errorf("the fix distdir held an unexpected %s when the fixer ran", name)
		}
	}
	for name, body := range want {
		got, ok := seen.contents[name]
		if !ok {
			t.Errorf("the fix distdir did not hold %s when the fixer ran; it held %v", name, seen.entries)
			continue
		}
		if got != body {
			t.Errorf("%s in the fix distdir holds %q, want %q", name, got, body)
		}
	}
}

func s081SandboxRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	prev := fixSandboxRoot
	fixSandboxRoot = func(context.Context) string { return root }
	t.Cleanup(func() { fixSandboxRoot = prev })
	return root
}

func s081Write(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// =============================================================================
// R1 through Apply, on a real staged tree
// =============================================================================

// TestS081RepairInheritsTheFirstAttemptsDownloads is the defect itself: the
// first `pkgdev manifest` downloads into its private distdir and fails; the
// fixer must then find those completed files in ITS distdir — and none of the
// partial, the cache symlink or the subdirectory the first attempt also held —
// while the first distdir is already gone.
func TestS081RepairInheritsTheFirstAttemptsDownloads(t *testing.T) {
	s081SandboxRoot(t)
	tmp := t.TempDir()
	overlayDir, configDir := filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config")
	host, cache := filepath.Join(tmp, "host-distdir"), filepath.Join(tmp, "cache")
	s081Write(t, filepath.Join(host, "unrelated-9.tar.gz"), "host bytes")
	cached := s081Write(t, filepath.Join(cache, "b-"+s081From+".tar.gz"), "cache bytes")
	createTestEbuildFile(t, overlayDir, s081Pkg, s081From)

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	pending.Add(PendingUpdate{Package: s081Pkg, CurrentVersion: s081From, NewVersion: s081To, Status: StatusPending})

	var mu sync.Mutex
	var firstDistdir string
	calls := 0
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		dd := ""
		for i, a := range arg {
			if a == "--distdir" && i+1 < len(arg) {
				dd = arg[i+1]
			}
		}
		mu.Lock()
		calls++
		first := calls == 1
		if first {
			firstDistdir = dd
		}
		mu.Unlock()
		if !first {
			return exec.CommandContext(ctx, "true")
		}
		// The first attempt: three completed downloads, one partial, a cache
		// symlink (what the ordinary path's seeding plants) and a subdirectory —
		// then the failure. Paths travel as arguments, never interpolated.
		script := `set -e; dd=$1; v=$2; cached=$3
printf 'tarball' > "$dd/b-$v.tar.gz"
printf 'patches' > "$dd/b-$v-patches.tar.xz"
printf 'docs' > "$dd/b-$v-docs.tar.bz2"
printf 'half' > "$dd/b-$v-extra.tar.gz.__download__"
ln -s "$cached" "$dd/$(basename "$cached")"
mkdir "$dd/sub"; printf 'nested' > "$dd/sub/nested-$v.tar.gz"
printf '%s\n' 'SRC_URI is unreachable: 404 Not Found'; exit 1`
		return exec.CommandContext(ctx, "sh", "-c", script, "sh", dd, s081To, cached)
	}

	fx := &fakeFixer{summary: "rewrote SRC_URI"}
	rec := &recordingReporter{}
	sink := &s081LogSink{}
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithApplierFixer(fx),
		WithApplierReporter(rec),
		WithApplierLogger(sink.logger()),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierDistdir(host, ""),
		WithApplierDistfilesCache(cache),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	firstGone := false
	seen := s081Observe(t, fx, rec, func(fixer.ManifestFixRequest) {
		mu.Lock()
		dd := firstDistdir
		mu.Unlock()
		_, statErr := os.Lstat(dd)
		firstGone = dd != "" && errors.Is(statErr, os.ErrNotExist)
	})
	hostBefore, cacheBefore := hashDistdirTree(t, host), hashDistdirTree(t, cache)

	_, _ = applier.Apply(t.Context(), s081Pkg, false)

	if fx.called != 1 {
		t.Fatalf("the fixer ran %d time(s), want 1; this test is about the distdir it was given", fx.called)
	}
	if firstDistdir == "" || firstDistdir == seen.distDir {
		t.Fatalf("the first attempt's distdir %q is not distinct from the fix distdir %q", firstDistdir, seen.distDir)
	}
	s081AssertHolds(t, seen, map[string]string{
		"b-" + s081To + ".tar.gz":         "tarball",
		"b-" + s081To + "-patches.tar.xz": "patches",
		"b-" + s081To + "-docs.tar.bz2":   "docs",
	})
	if !firstGone {
		t.Errorf("the first attempt's distdir %s still existed when the fixer ran (R1.4)", firstDistdir)
	}
	if after := hashDistdirTree(t, cache); after != cacheBefore {
		t.Errorf("the distfiles cache changed: %s -> %s", cacheBefore, after)
	}
	if after := hashDistdirTree(t, host); after != hostBefore {
		t.Errorf("the host distdir changed: %s -> %s", hostBefore, after)
	}
	s081AssertPreparedEvent(t, sink, 3)
	s081AssertReporterLine(t, seen.reporterSeen, "3")
}

// =============================================================================
// R1.5 through runManifestWithFix, on a staged candidate
// =============================================================================

type s081Staged struct {
	applier *Applier
	fixer   *fakeFixer
	rec     *recordingReporter
	sink    *s081LogSink
	cand    candidatePaths
	sandbox string
}

// s081StagedFixture lays out a staged package directory holding the candidate
// ebuild, a temp host distdir and a temp cache (one unrelated file each, so
// both resolve as sources and stay out of the way), and the sandbox root.
func s081StagedFixture(t *testing.T, seam func(ctx context.Context, name string, arg ...string) *exec.Cmd) *s081Staged {
	t.Helper()
	sandbox := s081SandboxRoot(t)
	tmp := t.TempDir()
	overlayDir, configDir := filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config")
	createTestEbuildFile(t, overlayDir, s081Pkg, s081From)

	repo := filepath.Join(tmp, "staging", s081Pkg, s081To)
	pkgDir := filepath.Join(repo, s081Pkg)
	ebuild := s081Write(t, filepath.Join(pkgDir, "b-"+s081To+".ebuild"), "EAPI=8\n")

	host, cache := filepath.Join(tmp, "host-distdir"), filepath.Join(tmp, "cache")
	s081Write(t, filepath.Join(host, "unrelated-9.tar.gz"), "host bytes")
	s081Write(t, filepath.Join(cache, "unrelated-8.tar.gz"), "cache bytes")

	fx := &fakeFixer{summary: "rewrote SRC_URI"}
	rec := &recordingReporter{}
	sink := &s081LogSink{}
	applier, err := NewApplier(overlayDir, configDir,
		WithExecCommand(seam),
		WithApplierFixer(fx),
		WithApplierReporter(rec),
		WithApplierLogger(sink.logger()),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierDistdir(host, ""),
		WithApplierDistfilesCache(cache),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return &s081Staged{
		applier: applier, fixer: fx, rec: rec, sink: sink,
		cand:    candidatePaths{staged: true, repoRoot: repo, pkgDir: pkgDir, ebuildPath: ebuild},
		sandbox: sandbox,
	}
}

func (e *s081Staged) run(t *testing.T) {
	t.Helper()
	distdir, _ := e.applier.runManifestWithFix(t.Context(), e.cand, s081Pkg, s081To, &ApplyResult{})
	t.Cleanup(func() { removeStagedDistdir(nil, distdir) })
	if e.fixer.called != 1 {
		t.Fatalf("the fixer ran %d time(s), want 1", e.fixer.called)
	}
}

// R1.5 — a carried file whose move fails is skipped with one WARN naming
// package, version, file and error, and the repair goes on.
//
// The first pkgdev call downloads a file and then makes its own distdir
// read-only, so the file cannot be moved out of it.
func TestS081RepairWarnsAndContinuesWhenACarryFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the move cannot be made to fail this way")
	}
	var mu sync.Mutex
	calls := 0
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if !first {
			return exec.CommandContext(ctx, "true")
		}
		dd := ""
		for i, a := range arg {
			if a == "--distdir" && i+1 < len(arg) {
				dd = arg[i+1]
			}
		}
		return exec.CommandContext(ctx, "sh", "-c",
			`set -e; printf 'unmovable' > "$1/b-$2-unlisted.tar.gz"; chmod 0500 "$1"
printf '%s\n' 'SRC_URI is unreachable: 404 Not Found'; exit 1`, "sh", dd, s081To)
	}
	env := s081StagedFixture(t, seam)
	// Registered after the sandbox TempDir, so it runs first and lets the
	// removal of the sandbox succeed.
	t.Cleanup(func() {
		_ = filepath.WalkDir(env.sandbox, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	s081Observe(t, env.fixer, env.rec, nil)

	env.run(t)

	s081AssertWarn(t, env.sink, "b-"+s081To+"-unlisted.tar.gz")
	s081AssertPreparedEvent(t, env.sink, 0)
}
