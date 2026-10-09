// Story 082 — sub-task 1.1. A staged manifest step derives the distfile names
// it expects from the PUBLISHED package dir and seeds its private distdir from
// the staged sources with them (R1.1-R1.4, R2.1, R5.2, R5.6).
//
// THE PRODUCTION SHAPE IS THE FIXTURE CONTRACT. validate.Stage carries only the
// candidate ebuild: no Manifest, no other version. Every fixture below keeps the
// staged dir that way unless a test plants something there ON PURPOSE to prove
// it is ignored; the Manifest and the older ebuild live in the published overlay
// dir. A fixture that put them in the staged dir would let the old derivation
// pass.
//
// Everything is observed at the moment pkgdev is invoked (the exec seam reads
// the --distdir it is handed), with a temp overlay, temp host distdir, temp
// cache and fixSandboxRoot overridden (s081SandboxRoot) — never the host
// DISTDIR or $HOME/.config. Reuse is asserted through a capturing slog handler
// and a recording reporter, never through a discarding logger.
//
// Uses s081SandboxRoot / s081Write / s081LogSink (applier_fix_reuse_s081_test.go),
// hashDistdirTree / stagedManifestFixture (sweep_staged_test.go) and
// recordingReporter (applier_reporter_test.go).

package autoupdate

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

const (
	s082SPkg  = "app-misc/b"
	s082SFrom = "1.0"
	s082STo   = "2.0"
)

// s082Entry is one entry of the private distdir as pkgdev found it.
type s082Entry struct {
	symlink bool
	target  string // what the link resolves to (os.Stat), for SameFile checks
	info    os.FileInfo
}

type s082PkgdevView struct {
	mu      sync.Mutex
	calls   int
	dir     string
	distdir string
	entries map[string]s082Entry
	cmd     *exec.Cmd
}

func (v *s082PkgdevView) snapshot() (int, string, map[string]s082Entry) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cmd != nil {
		v.dir = v.cmd.Dir
	}
	out := make(map[string]s082Entry, len(v.entries))
	for k, e := range v.entries {
		out[k] = e
	}
	return v.calls, v.distdir, out
}

// s082Seam records what the private distdir holds WHEN pkgdev IS INVOKED: the
// seeding has to have happened before, so a link added afterwards does not
// count.
func s082Seam(t *testing.T, v *s082PkgdevView) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "true")
		if name != "pkgdev" {
			return cmd
		}
		dd := ""
		for i, a := range arg {
			if a == "--distdir" && i+1 < len(arg) {
				dd = arg[i+1]
			}
		}
		entries := map[string]s082Entry{}
		des, err := os.ReadDir(dd)
		if err != nil {
			t.Errorf("reading the distdir pkgdev was handed (%q): %v", dd, err)
		}
		for _, de := range des {
			p := filepath.Join(dd, de.Name())
			li, err := os.Lstat(p)
			if err != nil {
				t.Errorf("lstat %s: %v", p, err)
				continue
			}
			e := s082Entry{symlink: li.Mode()&os.ModeSymlink != 0}
			if si, err := os.Stat(p); err == nil {
				e.info = si
			}
			if e.symlink {
				e.target, _ = os.Readlink(p)
			}
			entries[de.Name()] = e
		}
		v.mu.Lock()
		v.calls++
		v.distdir = dd
		v.entries = entries
		v.cmd = cmd
		v.mu.Unlock()
		return cmd
	}
}

type s082StagedEnv struct {
	sweeper   *sweeper
	overlay   string
	published string
	staged    string
	host      string
	cache     string
	sandbox   string
	rec       *recordingReporter
	sink      *s081LogSink
	view      *s082PkgdevView
}

// s082StagedFixture lays out the production shape minus the published package:
// a staged dir holding ONLY b-2.0.ebuild, a cache holding b-2.0.tar.gz, a host
// distdir holding an unrelated file, and an overlay with no app-misc/b yet.
// Callers add the published package (s082Publish) or deliberately leave it out.
func s082StagedFixture(t *testing.T) *s082StagedEnv {
	t.Helper()
	sandbox := s081SandboxRoot(t)
	tmp := t.TempDir()
	env := &s082StagedEnv{
		overlay: filepath.Join(tmp, "overlay"),
		staged:  filepath.Join(tmp, "staging", s082SPkg, s082STo, s082SPkg),
		host:    filepath.Join(tmp, "host-distdir"),
		cache:   filepath.Join(tmp, "cache"),
		sandbox: sandbox,
		rec:     &recordingReporter{},
		sink:    &s081LogSink{},
		view:    &s082PkgdevView{},
	}
	env.published = filepath.Join(env.overlay, s082SPkg)
	if err := os.MkdirAll(env.overlay, 0o755); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	s081Write(t, filepath.Join(env.staged, "b-"+s082STo+".ebuild"), "EAPI=8\n")
	s081Write(t, filepath.Join(env.cache, "b-"+s082STo+".tar.gz"), "cache b")
	s081Write(t, filepath.Join(env.host, "unrelated-9.tar.gz"), "host bytes")

	env.sweeper = newSweeper(env.overlay,
		withSweeperExec(s082Seam(t, env.view)),
		withSweeperReporter(env.rec),
		withSweeperLogger(env.sink.logger()),
		withSweeperDistdir(env.host, ""),
		withSweeperDistfilesCache(env.cache),
	)
	return env
}

// s082Publish writes the published package as promote.go leaves it before this
// candidate lands: the current ebuild and a Manifest naming its distfiles.
func s082Publish(t *testing.T, env *s082StagedEnv, dists ...string) {
	t.Helper()
	s081Write(t, filepath.Join(env.published, "b-"+s082SFrom+".ebuild"), "EAPI=8\n")
	var m strings.Builder
	for _, d := range dists {
		m.WriteString("DIST " + d + " 100 BLAKE2B ab SHA512 cd\n")
	}
	s081Write(t, filepath.Join(env.published, "Manifest"), m.String())
}

func (env *s082StagedEnv) run(t *testing.T, pkg string) (string, error) {
	t.Helper()
	distdir, err := env.sweeper.runStagedManifestIn(t.Context(), "", env.staged, pkg, s082STo)
	if distdir != "" {
		t.Cleanup(func() { removeStagedDistdir(nil, distdir) })
	}
	return distdir, err
}

// s082AssertSeeded asserts the private distdir held exactly want when pkgdev
// ran, every entry a symlink resolving to the named source file.
func s082AssertSeeded(t *testing.T, env *s082StagedEnv, want map[string]string) {
	t.Helper()
	calls, _, entries := env.view.snapshot()
	if calls != 1 {
		t.Fatalf("pkgdev ran %d time(s), want 1", calls)
	}
	var got []string
	for n := range entries {
		got = append(got, n)
	}
	sort.Strings(got)
	for name, e := range entries {
		src, ok := want[name]
		if !ok {
			t.Errorf("the private distdir held %s when pkgdev ran; only %v were expected (held %v)", name, s082KeysOf(want), got)
			continue
		}
		if !e.symlink {
			t.Errorf("%s in the private distdir is not a link into the staged source", name)
			continue
		}
		si, err := os.Stat(src)
		if err != nil || e.info == nil || !os.SameFile(e.info, si) {
			t.Errorf("%s in the private distdir points at %q, want the source %q", name, e.target, src)
		}
	}
	for name := range want {
		if _, ok := entries[name]; !ok {
			t.Errorf("the private distdir did not hold %s when pkgdev ran (held %v): it will be downloaded again", name, got)
		}
	}
}

func s082KeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// s082ReuseLines counts the reporter lines naming the package and dir.
func s082ReuseLines(env *s082StagedEnv, dir string) int {
	n := 0
	for _, ev := range env.rec.snapshot() {
		if strings.HasPrefix(ev, "Log:") && strings.Contains(ev, s082SPkg) && strings.Contains(ev, dir) {
			n++
		}
	}
	return n
}

// s082ReuseEvents counts INFO events naming the package and the source dir.
func s082ReuseEvents(env *s082StagedEnv, dir string) int {
	n := 0
	for _, r := range env.sink.snapshot() {
		if r.level != slog.LevelInfo {
			continue
		}
		pkg, src := false, false
		for _, v := range r.attrs {
			s := s081AttrString(v)
			pkg = pkg || s == s082SPkg
			src = src || s == dir
		}
		if pkg && src {
			n++
		}
	}
	return n
}

// R1.1 + R2.1 + R5.6 — the reproduction of the defect. The published Manifest
// names b-1.0.tar.gz and b-1.0-vendor.tar.xz beside b-1.0.ebuild, so the names
// 2.0 expects are b-2.0.tar.gz (in the cache) and b-2.0-vendor.tar.xz (only in
// the host DISTDIR). Both must be linked before pkgdev runs, from the source
// that holds them, and the reuse reported per source.
//
// HOSTILE HALF FIRST — near-identical names that are NOT expected must stay out:
// the old version's own archive, a partial download of the expected name, and a
// longer version that merely starts with "2.0". A seeding keyed on a prefix or
// on "any b-*" would link them.
func TestS082StagedSeedsFromThePublishedManifest(t *testing.T) {
	env := s082StagedFixture(t)
	s082Publish(t, env, "b-"+s082SFrom+".tar.gz", "b-"+s082SFrom+"-vendor.tar.xz")
	for _, decoy := range []string{"b-" + s082SFrom + ".tar.gz", "b-" + s082STo + ".tar.gz.__download__", "b-" + s082STo + "0.tar.gz"} {
		s081Write(t, filepath.Join(env.cache, decoy), "decoy "+decoy)
	}
	s081Write(t, filepath.Join(env.host, "b-"+s082STo+"-vendor.tar.xz"), "host vendor")
	publishedBefore := hashDistdirTree(t, env.published)
	cacheBefore, hostBefore := hashDistdirTree(t, env.cache), hashDistdirTree(t, env.host)

	if _, err := env.run(t, s082SPkg); err != nil {
		t.Fatalf("runStagedManifestIn: %v", err)
	}

	s082AssertSeeded(t, env, map[string]string{
		"b-" + s082STo + ".tar.gz":        filepath.Join(env.cache, "b-"+s082STo+".tar.gz"),
		"b-" + s082STo + "-vendor.tar.xz": filepath.Join(env.host, "b-"+s082STo+"-vendor.tar.xz"),
	})
	if n := s082ReuseLines(env, env.cache); n != 1 {
		t.Errorf("got %d reporter line(s) naming %s and the cache %s, want 1 (R2.1); events: %v", n, s082SPkg, env.cache, env.rec.snapshot())
	}
	if n := s082ReuseLines(env, env.host); n != 1 {
		t.Errorf("got %d reporter line(s) naming %s and the host distdir %s, want 1 (R2.1); events: %v", n, s082SPkg, env.host, env.rec.snapshot())
	}
	if n := s082ReuseEvents(env, env.cache); n != 1 {
		t.Errorf("got %d INFO event(s) naming %s and the cache, want 1 (R2.1)", n, s082SPkg)
	}
	if after := hashDistdirTree(t, env.published); after != publishedBefore {
		t.Errorf("the published package dir changed during a staged manifest step (R5.6): %s -> %s", publishedBefore, after)
	}
	if after := hashDistdirTree(t, env.cache); after != cacheBefore {
		t.Errorf("the distfiles cache changed: %s -> %s", cacheBefore, after)
	}
	if after := hashDistdirTree(t, env.host); after != hostBefore {
		t.Errorf("the host distdir changed: %s -> %s", hostBefore, after)
	}
}

// R1.2 — the staged dir is never read for the derivation, in both directions:
//   - wrongly kept: a Manifest and an older ebuild PLANTED in the staged dir name
//     decoy-1.0.tar.gz; the cache holds decoy-2.0.tar.gz, which must not be
//     linked — while the published name still is;
//   - wrongly substituted: with NO published Manifest, the planted staged one
//     names b-1.0.tar.gz and the cache holds b-2.0.tar.gz; nothing may be linked,
//     because there is no fallback to the staged dir.
func TestS082StagedIgnoresAManifestPlantedInTheStagedDir(t *testing.T) {
	t.Run("published Manifest present", func(t *testing.T) {
		env := s082StagedFixture(t)
		s082Publish(t, env, "b-"+s082SFrom+".tar.gz")
		s081Write(t, filepath.Join(env.staged, "b-"+s082SFrom+".ebuild"), "EAPI=8\n")
		s081Write(t, filepath.Join(env.staged, "Manifest"), "DIST decoy-"+s082SFrom+".tar.gz 100 BLAKE2B ab SHA512 cd\n")
		s081Write(t, filepath.Join(env.cache, "decoy-"+s082STo+".tar.gz"), "decoy")

		if _, err := env.run(t, s082SPkg); err != nil {
			t.Fatalf("runStagedManifestIn: %v", err)
		}
		s082AssertSeeded(t, env, map[string]string{
			"b-" + s082STo + ".tar.gz": filepath.Join(env.cache, "b-"+s082STo+".tar.gz"),
		})
	})
	t.Run("no published Manifest", func(t *testing.T) {
		env := s082StagedFixture(t)
		s081Write(t, filepath.Join(env.published, "b-"+s082SFrom+".ebuild"), "EAPI=8\n")
		s081Write(t, filepath.Join(env.staged, "b-"+s082SFrom+".ebuild"), "EAPI=8\n")
		s081Write(t, filepath.Join(env.staged, "Manifest"), "DIST b-"+s082SFrom+".tar.gz 100 BLAKE2B ab SHA512 cd\n")

		if _, err := env.run(t, s082SPkg); err != nil {
			t.Fatalf("runStagedManifestIn: %v", err)
		}
		s082AssertSeeded(t, env, map[string]string{})
	})
}

// R1.3 — a published package that cannot name anything seeds nothing, and the
// step still runs pkgdev and succeeds. The cache holds both the old and the new
// archive, so a derivation that guessed anything would show up as a link.
// (Regression guard: green before and after the fix.)
func TestS082StagedWithoutAPublishedManifestProceedsUnseeded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		publish func(t *testing.T, env *s082StagedEnv)
	}{
		{"published dir holds no Manifest", func(t *testing.T, env *s082StagedEnv) {
			s081Write(t, filepath.Join(env.published, "b-"+s082SFrom+".ebuild"), "EAPI=8\n")
		}},
		{"published dir holds no ebuild", func(t *testing.T, env *s082StagedEnv) {
			s081Write(t, filepath.Join(env.published, "Manifest"), "DIST b-"+s082SFrom+".tar.gz 100 BLAKE2B ab SHA512 cd\n")
		}},
		{"published dir does not exist", func(*testing.T, *s082StagedEnv) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := s082StagedFixture(t)
			s081Write(t, filepath.Join(env.cache, "b-"+s082SFrom+".tar.gz"), "old")
			tc.publish(t, env)

			distdir, err := env.run(t, s082SPkg)
			if err != nil {
				t.Fatalf("runStagedManifestIn: %v", err)
			}
			s082AssertSeeded(t, env, map[string]string{})
			if _, dd, _ := env.view.snapshot(); dd != distdir {
				t.Errorf("pkgdev was handed %q, %q was returned", dd, distdir)
			}
			if n := s082ReuseLines(env, env.cache); n != 0 {
				t.Errorf("a reuse was reported although nothing was seeded: %v", env.rec.snapshot())
			}
		})
	}
}

// R1.4 — an atom that cannot be split derives nothing and the step behaves as
// it does today: ErrManifestFailed, no pkgdev, no private distdir created.
// (Regression guard: green before and after the fix.)
func TestS082StagedUnsplittableAtomProceedsAsToday(t *testing.T) {
	env := s082StagedFixture(t)
	s082Publish(t, env, "b-"+s082SFrom+".tar.gz")

	_, err := env.run(t, "b")
	if !errors.Is(err, ErrManifestFailed) {
		t.Errorf("err = %v, want it to wrap ErrManifestFailed as today", err)
	}
	if calls, _, _ := env.view.snapshot(); calls != 0 {
		t.Errorf("pkgdev ran %d time(s) for an unsplittable atom, want 0", calls)
	}
	if entries, rerr := os.ReadDir(env.sandbox); rerr != nil || len(entries) != 0 {
		t.Errorf("the sandbox root holds %v (err %v); no private distdir should have been created", entries, rerr)
	}
}

// R5.2 non-vacuity — the 043 guard (TestStagedManifestDoesNotSeedASuppliedDistdir)
// can only fail if its fixture yields a non-empty expected list through the
// production path. With the re-pointed stagedManifestFixture (Manifest and
// gst-plugins-qt6-1.28.6.ebuild in the PUBLISHED dir, only the candidate staged),
// the ordinary path must link gst-plugins-good-1.29.2.tar.xz from the cache. If
// this fails, the guard is passing for the wrong reason.
func TestS082StagedTheSuppliedDistdirGuardFixtureExpectsAName(t *testing.T) {
	env := stagedManifestFixture(t, false)
	published := filepath.Join(env.sweeper.overlayPath, "media-plugins", "gst-plugins-qt6")
	publishedBefore := hashDistdirTree(t, published)

	distdir, err := env.sweeper.runStagedManifest(t.Context(), env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2")
	if distdir != "" {
		t.Cleanup(func() { removeStagedDistdir(nil, distdir) })
	}
	if err != nil {
		t.Fatalf("runStagedManifest: %v", err)
	}

	link := filepath.Join(distdir, "gst-plugins-good-1.29.2.tar.xz")
	li, lerr := os.Lstat(link)
	if lerr != nil || li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the private distdir holds no link gst-plugins-good-1.29.2.tar.xz (lstat err %v): the fixture's expected "+
			"list is empty through the production path, so the 043 guard cannot catch a seeding of a supplied distdir", lerr)
	}
	si, serr := os.Stat(link)
	ci, cerr := os.Stat(filepath.Join(env.cacheDir, "gst-plugins-good-1.29.2.tar.xz"))
	if serr != nil || cerr != nil || !os.SameFile(si, ci) {
		t.Errorf("the link does not resolve to the cache entry (stat errs %v, %v)", serr, cerr)
	}
	if after := hashDistdirTree(t, published); after != publishedBefore {
		t.Errorf("the published package dir changed (R5.6): %s -> %s", publishedBefore, after)
	}
}
