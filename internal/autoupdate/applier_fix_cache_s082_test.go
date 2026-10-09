// Story 082 — sub-task 3.1. The LLM manifest repair of a STAGED apply receives
// the distfiles cache's (and the host DISTDIR's) expected files as independent
// regular files, after story 081's carry-over, and reports both counts (R3.1,
// R3.2, R3.5, R3.7, R4.1, R4.2, R1.2, R5.4, R5.6).
//
// THE PRODUCTION SHAPE IS THE FIXTURE CONTRACT. The Manifest and the older
// ebuild live in the PUBLISHED overlay dir; the staged dir holds only the
// candidate ebuild. Story 081's parked version of these tests put both in the
// staged dir — exactly the defect this story fixes — so they could pass against
// a derivation production never reaches.
//
// Every assertion is made on what req.DistDir holds WHEN THE FIXER IS CALLED
// (os.Lstat: regular files, no symlink, no hard link into a source), with a temp
// overlay, a temp host distdir (WithApplierDistdir), a temp cache
// (WithApplierDistfilesCache) and fixSandboxRoot overridden (s081SandboxRoot) —
// never the host DISTDIR or $HOME/.config. Logs go through a capturing handler.
//
// Uses s081SandboxRoot / s081Write / s081LogSink / s081Observe / s081Seen /
// s081AttrInt / s081AttrString (applier_fix_reuse_s081_test.go), hashDistdirTree
// (sweep_staged_test.go), recordingReporter, fakeFixer, createTestEbuildFile.

package autoupdate

import (
	"context"
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
	s082Pkg  = "app-misc/b"
	s082From = "1.4"
	s082To   = "2.1" // no 0, 1 or 3 in it beyond the version, see the count tokens below
)

var s082Dists = []string{"b-%s.tar.gz", "b-data-%s.tar.xz", "b-extra-%s.zip"}

func s082Dist(i int, v string) string { return strings.Replace(s082Dists[i], "%s", v, 1) }

func s082ForPackage(r s081Record) bool {
	return s081AttrString(r.attrs["package"]) == s082Pkg && s081AttrString(r.attrs["version"]) == s082To
}

// s082AssertPreparedEvent is R4.1: exactly one INFO event for the package and
// version carrying both counts, zero included. Keys are matched loosely ("carr…",
// "cop…") so the test pins the content, not a spelling.
func s082AssertPreparedEvent(t *testing.T, sink *s081LogSink, wantCarried, wantCopied int64) {
	t.Helper()
	n := 0
	for _, r := range sink.snapshot() {
		if r.level != slog.LevelInfo || !s082ForPackage(r) {
			continue
		}
		carried, copied := int64(-1), int64(-1)
		for k, v := range r.attrs {
			x, ok := s081AttrInt(v)
			switch {
			case !ok:
			case strings.Contains(k, "carr"):
				carried = x
			case strings.Contains(k, "cop"):
				copied = x
			}
		}
		if carried < 0 || copied < 0 {
			continue
		}
		n++
		if carried != wantCarried || copied != wantCopied {
			t.Errorf("the prepared-distdir INFO event says carried=%d copied=%d, want %d and %d: %q %v",
				carried, copied, wantCarried, wantCopied, r.msg, r.attrs)
		}
	}
	if n != 1 {
		t.Errorf("got %d INFO event(s) carrying %s %s and BOTH counts, want exactly 1 (R4.1); records: %+v",
			n, s082Pkg, s082To, sink.snapshot())
	}
}

// s082ReporterLines counts reporter lines sent before the fixer ran that name
// the package and state both numbers (R4.2).
func s082ReporterLines(before []string, carried, copied string) int {
	reC := regexp.MustCompile(`\b` + carried + `\b`)
	reP := regexp.MustCompile(`\b` + copied + `\b`)
	n := 0
	for _, ev := range before {
		if strings.HasPrefix(ev, "Log:") && strings.Contains(ev, s082Pkg) &&
			reC.MatchString(strings.ReplaceAll(ev, s082To, "")) && reP.MatchString(strings.ReplaceAll(ev, s082To, "")) {
			n++
		}
	}
	return n
}

// s082AssertWarn is R3.5: one WARN event for the package and version naming the
// file, the source directory and an error.
func s082AssertWarn(t *testing.T, sink *s081LogSink, file, source string) {
	t.Helper()
	n := 0
	for _, r := range sink.snapshot() {
		if r.level != slog.LevelWarn || !s082ForPackage(r) {
			continue
		}
		namesFile, namesSource, hasErr := false, false, false
		for k, v := range r.attrs {
			s := s081AttrString(v)
			namesFile = namesFile || s == file
			namesSource = namesSource || s == source
			hasErr = hasErr || (strings.Contains(k, "err") && s != "")
		}
		if namesFile && namesSource && hasErr {
			n++
		}
	}
	if n != 1 {
		t.Errorf("got %d WARN event(s) for %s naming file %q, source %q and an error, want exactly 1 (R3.5); records: %+v",
			n, s082Pkg, file, source, sink.snapshot())
	}
}

// s082AssertHolds asserts the fixer saw exactly want (name -> bytes), every
// entry a regular file (R3.2), and none sharing an inode with the source file
// named in sources (a hard link is a link too).
func s082AssertHolds(t *testing.T, seen *s081Seen, inodes map[string]os.FileInfo, want, sources map[string]string) {
	t.Helper()
	for name, mode := range seen.entries {
		if mode&os.ModeSymlink != 0 {
			t.Errorf("the fix distdir held a symlink %s when the fixer ran (R3.2): the agent's Write would go through it", name)
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
		if src, ok := sources[name]; ok {
			if si, err := os.Stat(src); err == nil && inodes[name] != nil && os.SameFile(inodes[name], si) {
				t.Errorf("%s in the fix distdir is a hard link to %s, not a copy", name, src)
			}
		}
	}
}

// s082Inodes captures the Lstat of every entry of the fix distdir when the
// fixer runs, for the hard-link check.
func s082Inodes(t *testing.T, into map[string]os.FileInfo, mu *sync.Mutex) func(fixer.ManifestFixRequest) {
	return func(req fixer.ManifestFixRequest) {
		entries, err := os.ReadDir(req.DistDir)
		if err != nil {
			t.Errorf("reading the fix distdir: %v", err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, e := range entries {
			if info, err := os.Lstat(filepath.Join(req.DistDir, e.Name())); err == nil {
				into[e.Name()] = info
			}
		}
	}
}

// s082FirstAttempt is a pkgdev seam whose FIRST invocation runs script (with
// $1 = its --distdir, $2 = the version) and fails; later invocations succeed.
func s082FirstAttempt(script string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	var mu sync.Mutex
	calls := 0
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
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
			"set -e; "+script+"\nprintf '%s\\n' 'SRC_URI is unreachable: 404 Not Found'; exit 1", "sh", dd, s082To)
	}
}

// s082PublishedPackage writes the published package as promote.go leaves it
// before the candidate lands: b-1.4.ebuild and a Manifest naming three archives.
func s082PublishedPackage(t *testing.T, overlayDir string) string {
	t.Helper()
	createTestEbuildFile(t, overlayDir, s082Pkg, s082From)
	published := filepath.Join(overlayDir, s082Pkg)
	var m strings.Builder
	for i := range s082Dists {
		m.WriteString("DIST " + s082Dist(i, s082From) + " 100 BLAKE2B ab SHA512 cd\n")
	}
	s081Write(t, filepath.Join(published, "Manifest"), m.String())
	return published
}

// s082Sources fills the staged sources: the cache holds b-2.1.tar.gz (with
// cacheB's bytes unless cacheEntry replaces it); the host DISTDIR holds all
// three new names, the first with different bytes so precedence is visible.
func s082Sources(t *testing.T, host, cache string, cacheEntry func(cache string)) {
	t.Helper()
	s081Write(t, filepath.Join(host, s082Dist(0, s082To)), "host b")
	s081Write(t, filepath.Join(host, s082Dist(1, s082To)), "host data")
	s081Write(t, filepath.Join(host, s082Dist(2, s082To)), "host extra")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if cacheEntry != nil {
		cacheEntry(cache)
		return
	}
	s081Write(t, filepath.Join(cache, s082Dist(0, s082To)), "cache b")
}

// =============================================================================
// Through Apply, on a real staged tree built by validate.Stage
// =============================================================================

// R3.1 + R3.2 + R4.1 + R4.2 + R5.4 — the success metric. The first attempt
// downloads one file nobody expected, then fails; the fixer must find that
// carried file AND the three expected names copied from the sources (the cache
// before the host DISTDIR), every one an independent regular file, with the
// sources byte-identical.
//
// HOSTILE: the first attempt's private distdir is SEEDED with symlinks to these
// very names (sub-task 1.1). A carry-over that moved symlinks, or a copy that
// linked, hands the fixer links into the cache — caught here with the real
// seeding, not a planted link. Near-names in the cache (a partial download, a
// longer version) must not be copied.
func TestS082RepairCopiesExpectedDistfilesThroughApply(t *testing.T) {
	s081SandboxRoot(t)
	tmp := t.TempDir()
	overlayDir, configDir := filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config")
	host, cache := filepath.Join(tmp, "host-distdir"), filepath.Join(tmp, "cache")
	s082PublishedPackage(t, overlayDir)
	s082Sources(t, host, cache, nil)
	for _, decoy := range []string{s082Dist(0, s082To) + ".__download__", s082Dist(0, s082To+"0"), s082Dist(0, s082From)} {
		s081Write(t, filepath.Join(cache, decoy), "decoy")
	}

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	pending.Add(PendingUpdate{Package: s082Pkg, CurrentVersion: s082From, NewVersion: s082To, Status: StatusPending})

	fx := &fakeFixer{summary: "rewrote SRC_URI"}
	rec := &recordingReporter{}
	sink := &s081LogSink{}
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(s082FirstAttempt(`printf 'patches' > "$1/b-$2-patches.tar.xz"`)),
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
	var mu sync.Mutex
	inodes := map[string]os.FileInfo{}
	seen := s081Observe(t, fx, rec, s082Inodes(t, inodes, &mu))
	hostBefore, cacheBefore := hashDistdirTree(t, host), hashDistdirTree(t, cache)

	_, _ = applier.Apply(t.Context(), s082Pkg, false)

	if fx.called != 1 {
		t.Fatalf("the fixer ran %d time(s), want 1; this test is about the distdir it was given", fx.called)
	}
	mu.Lock()
	defer mu.Unlock()
	s082AssertHolds(t, seen, inodes, map[string]string{
		"b-" + s082To + "-patches.tar.xz": "patches",
		s082Dist(0, s082To):               "cache b",
		s082Dist(1, s082To):               "host data",
		s082Dist(2, s082To):               "host extra",
	}, map[string]string{
		s082Dist(0, s082To): filepath.Join(cache, s082Dist(0, s082To)),
		s082Dist(1, s082To): filepath.Join(host, s082Dist(1, s082To)),
		s082Dist(2, s082To): filepath.Join(host, s082Dist(2, s082To)),
	})
	if after := hashDistdirTree(t, cache); after != cacheBefore {
		t.Errorf("the distfiles cache changed: %s -> %s (R3.4)", cacheBefore, after)
	}
	if after := hashDistdirTree(t, host); after != hostBefore {
		t.Errorf("the host distdir changed: %s -> %s (R3.4)", hostBefore, after)
	}
	s082AssertPreparedEvent(t, sink, 1, 3)
	if n := s082ReporterLines(seen.reporterSeen, "1", "3"); n != 1 {
		t.Errorf("got %d reporter line(s) before the fixer naming %s with 1 carried and 3 copied, want exactly 1 (R4.2); events: %v",
			n, s082Pkg, seen.reporterSeen)
	}
}

// =============================================================================
// Through runManifestWithFix, on a staged candidate in the production shape
// =============================================================================

type s082RepairEnv struct {
	applier   *Applier
	fixer     *fakeFixer
	rec       *recordingReporter
	sink      *s081LogSink
	cand      candidatePaths
	published string
	host      string
	cache     string
}

func s082RepairFixture(t *testing.T, seam func(ctx context.Context, name string, arg ...string) *exec.Cmd, publish bool, cacheEntry func(string)) *s082RepairEnv {
	t.Helper()
	s081SandboxRoot(t)
	tmp := t.TempDir()
	overlayDir, configDir := filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config")
	published := filepath.Join(overlayDir, s082Pkg)
	if publish {
		s082PublishedPackage(t, overlayDir)
	} else {
		createTestEbuildFile(t, overlayDir, s082Pkg, s082From)
	}
	repo := filepath.Join(tmp, "staging", s082Pkg, s082To)
	pkgDir := filepath.Join(repo, s082Pkg)
	ebuild := s081Write(t, filepath.Join(pkgDir, "b-"+s082To+".ebuild"), "EAPI=8\n")

	host, cache := filepath.Join(tmp, "host-distdir"), filepath.Join(tmp, "cache")
	s082Sources(t, host, cache, cacheEntry)

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
	return &s082RepairEnv{
		applier: applier, fixer: fx, rec: rec, sink: sink,
		cand:      candidatePaths{staged: true, repoRoot: repo, pkgDir: pkgDir, ebuildPath: ebuild},
		published: published, host: host, cache: cache,
	}
}

func (e *s082RepairEnv) run(t *testing.T) {
	t.Helper()
	distdir, _ := e.applier.runManifestWithFix(t.Context(), e.cand, s082Pkg, s082To, &ApplyResult{})
	t.Cleanup(func() { removeStagedDistdir(nil, distdir) })
	if e.fixer.called != 1 {
		t.Fatalf("the fixer ran %d time(s), want 1", e.fixer.called)
	}
}

// R1.2 + R3.7 + R5.6, hostile halves first:
//   - wrongly kept: a Manifest and an older ebuild PLANTED in the staged dir name
//     decoy-1.4.tar.gz, and the cache holds decoy-2.1.tar.gz — it must not reach
//     the fixer;
//   - wrongly replaced: the first attempt itself completed b-data-2.1.tar.xz (an
//     expected name, carried over); the host's copy of that name must not
//     replace it.
//
// Then the benign half: the other two expected names arrive by copy. The
// published package dir is byte-identical afterwards.
func TestS082RepairIgnoresTheStagedManifestAndNeverReplaces(t *testing.T) {
	// The seeding linked b-data-2.1.tar.xz from the host into the first distdir:
	// the script removes that link before writing, so it never writes through it.
	env := s082RepairFixture(t, s082FirstAttempt(`rm -f "$1/b-data-$2.tar.xz"; printf 'downloaded data' > "$1/b-data-$2.tar.xz"`), true,
		func(cache string) {
			s081Write(t, filepath.Join(cache, s082Dist(0, s082To)), "cache b")
			s081Write(t, filepath.Join(cache, "decoy-"+s082To+".tar.gz"), "decoy")
		})
	s081Write(t, filepath.Join(env.cand.pkgDir, "b-"+s082From+".ebuild"), "EAPI=8\n")
	s081Write(t, filepath.Join(env.cand.pkgDir, "Manifest"), "DIST decoy-"+s082From+".tar.gz 100 BLAKE2B ab SHA512 cd\n")
	var mu sync.Mutex
	inodes := map[string]os.FileInfo{}
	seen := s081Observe(t, env.fixer, env.rec, s082Inodes(t, inodes, &mu))
	publishedBefore, hostBefore := hashDistdirTree(t, env.published), hashDistdirTree(t, env.host)

	env.run(t)

	mu.Lock()
	defer mu.Unlock()
	s082AssertHolds(t, seen, inodes, map[string]string{
		s082Dist(0, s082To): "cache b",
		s082Dist(1, s082To): "downloaded data",
		s082Dist(2, s082To): "host extra",
	}, map[string]string{
		s082Dist(0, s082To): filepath.Join(env.cache, s082Dist(0, s082To)),
		s082Dist(2, s082To): filepath.Join(env.host, s082Dist(2, s082To)),
	})
	if after := hashDistdirTree(t, env.published); after != publishedBefore {
		t.Errorf("the published package dir changed (R5.6): %s -> %s", publishedBefore, after)
	}
	if after := hashDistdirTree(t, env.host); after != hostBefore {
		t.Errorf("the host distdir changed: %s -> %s", hostBefore, after)
	}
	s082AssertPreparedEvent(t, env.sink, 1, 2)
	if n := s082ReporterLines(seen.reporterSeen, "1", "2"); n != 1 {
		t.Errorf("got %d reporter line(s) naming %s with 1 carried and 2 copied, want exactly 1 (R4.2); events: %v",
			n, s082Pkg, seen.reporterSeen)
	}
}

// R3.5 — a copy that fails mid-way is skipped: nothing under its final name and
// no temporary file, one WARN naming package, version, file, source directory
// and error, the other names still copied and the fixer still run.
//
// The cache entry is a symlink to /proc/self/mem: it stats as a regular file and
// fails with EIO on the first read. The host DISTDIR is not given that name, so
// the assertion does not depend on whether a failed source falls through.
func TestS082RepairWarnsAndContinuesWhenACopyFails(t *testing.T) {
	if _, err := os.Stat("/proc/self/mem"); err != nil {
		t.Skipf("no /proc/self/mem to fail a read with: %v", err)
	}
	env := s082RepairFixture(t, pkgdevFailsUntilFixed(), true, func(cache string) {
		if err := os.Symlink("/proc/self/mem", filepath.Join(cache, s082Dist(0, s082To))); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	})
	if err := os.Remove(filepath.Join(env.host, s082Dist(0, s082To))); err != nil {
		t.Fatalf("removing the host copy: %v", err)
	}
	var mu sync.Mutex
	inodes := map[string]os.FileInfo{}
	seen := s081Observe(t, env.fixer, env.rec, s082Inodes(t, inodes, &mu))

	env.run(t)

	mu.Lock()
	defer mu.Unlock()
	s082AssertHolds(t, seen, inodes, map[string]string{
		s082Dist(1, s082To): "host data",
		s082Dist(2, s082To): "host extra",
	}, nil)
	s082AssertWarn(t, env.sink, s082Dist(0, s082To), env.cache)
	s082AssertPreparedEvent(t, env.sink, 0, 2)
}

// R4.1 + R4.2, the zero case — a published package with no Manifest names
// nothing, the first attempt downloads nothing: the INFO event still carries
// both counts as 0, and no reporter line about reuse is sent.
func TestS082RepairReportsZeroCountsWithoutAReporterLine(t *testing.T) {
	env := s082RepairFixture(t, pkgdevFailsUntilFixed(), false, nil)
	seen := s081Observe(t, env.fixer, env.rec, nil)

	env.run(t)

	if len(seen.entries) != 0 {
		t.Errorf("the fix distdir held %v; nothing was expected and nothing downloaded", seen.entries)
	}
	s082AssertPreparedEvent(t, env.sink, 0, 0)
	for _, ev := range seen.reporterSeen {
		if strings.HasPrefix(ev, "Log:") && strings.Contains(ev, s082Pkg) &&
			(strings.Contains(ev, "reus") || strings.Contains(ev, "cop") || strings.Contains(ev, "carr")) {
			t.Errorf("a reuse reporter line was sent although nothing was carried or copied (R4.2): %q", ev)
		}
	}
}
