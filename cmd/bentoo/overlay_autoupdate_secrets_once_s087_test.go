package main

// Story 087, sub-task 4.5 (R6.3, R10.7): one `overlay autoupdate` run warns
// about an unreadable secrets file exactly once, whatever the number of update
// Checkers it builds and whether it also resolves the ::gentoo provider.
//
// What counts: a WARN record on the run's stderr (the invocation's slog text
// handler, one record per line) whose text carries secrets.ErrUnreadable's
// message. Every lookup that fails on the unreadable user secrets file wraps
// that sentinel, so the count is independent of which code path logged it and
// of its message: the Checker's own GitHub-token warning and the run's shared
// secrets warning are both counted.
//
// The modes and the Checkers each builds today:
//   - --check --revivable: one Checker, then the ::gentoo provider;
//   - --revive-list: one Checker, then the ::gentoo provider;
//   - --revive all over two orphans: the ::gentoo provider, one Checker to list
//     the targets, then one fresh Checker per target (N+1 Checkers).
//
// Each run must log exactly one such warning, naming the file and carrying
// err=. Two runs in one process must each log one (once per run, not once per
// process).
//
// Hermetic: s058Env puts HOME and every XDG directory under t.TempDir and
// blanks GITHUB_TOKEN/GH_TOKEN; the per-repository token variable is blanked
// too; the user secrets file is a directory, so reading it fails with EISDIR.
// ::gentoo is a `provider: local` tree under the temporary HOME (no registry,
// no clone), BENTOO_GENTOO_REPO points at the same tree, every upstream is an
// httptest server, and --revive runs with a temporary --distdir and no
// distfiles cache. No target reaches the Applier's bump: each per-target check
// finds upstream equal to the seeded ::gentoo version and is skipped.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// s087d45Pkgs are the two disabled, absent registry entries ::gentoo carries
// at s087d45GentooVersion: the revivable orphans of every run below.
var s087d45Pkgs = []string{"dev-test/s087alpha", "dev-test/s087beta"}

const (
	s087d45GentooVersion = "1.0.0"
	s087d45NewerVersion  = "2.0.0"
)

// s087d45World is one temporary home with an overlay, a local ::gentoo tree, a
// registry of two orphans, an unreadable secrets file and a local upstream.
type s087d45World struct {
	c           *testCLI
	secretsPath string
	distdir     string
}

// s087d45Upstream answers every orphan with s087d45NewerVersion. With
// flipAfterFirst, only the first request for each package does; every later
// one answers s087d45GentooVersion, so --revive's per-target check (which
// follows the orphan scan's one request) finds nothing to bump and skips.
func s087d45Upstream(t *testing.T, flipAfterFirst bool) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		n := hits[r.URL.Path]
		mu.Unlock()
		v := s087d45NewerVersion
		if flipAfterFirst && n > 1 {
			v = s087d45GentooVersion
		}
		_, _ = fmt.Fprintf(w, "version %s\n", v)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func s087d45Setup(t *testing.T, flipAfterFirst bool) s087d45World {
	t.Helper()
	c := s058Env(t)
	t.Setenv(repoTokenName("gentoo"), "")

	gentoo := filepath.Join(c.Home(), "gentoo")
	if err := os.MkdirAll(filepath.Join(gentoo, "profiles"), 0o750); err != nil {
		t.Fatalf("mkdir gentoo profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gentoo, "profiles", "repo_name"), []byte("gentoo\n"), 0o600); err != nil {
		t.Fatalf("write repo_name: %v", err)
	}
	t.Setenv("BENTOO_GENTOO_REPO", gentoo)

	srv := s087d45Upstream(t, flipAfterFirst)
	var reg strings.Builder
	for _, pkg := range s087d45Pkgs {
		_, name, _ := strings.Cut(pkg, "/")
		writeReviveSrcEbuild(t, filepath.Join(gentoo, pkg), name, s087d45GentooVersion)
		reg.WriteString("[\"" + pkg + "\"]\n" +
			"enabled = false\n" +
			"url = \"" + srv.URL + "/" + name + "\"\n" +
			"parser = \"regex\"\n" +
			"pattern = 'version ([0-9.]+)'\n\n")
	}
	s058WriteRegistry(t, c.Overlay(), reg.String())

	appendHarnessConfig(t, c, "repositories:\n  gentoo:\n    provider: local\n    path: "+gentoo+"\n")

	// Present but unreadable: a directory where the user secrets file belongs.
	secretsPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bentoo", "secrets")
	if err := os.MkdirAll(secretsPath, 0o750); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	return s087d45World{c: c, secretsPath: secretsPath, distdir: t.TempDir()}
}

// s087d45SecretsWarnings returns the WARN records of stderr that cite the
// unreadable secrets file.
func s087d45SecretsWarnings(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "level=WARN ") && strings.Contains(line, secrets.ErrUnreadable.Error()) {
			out = append(out, line)
		}
	}
	return out
}

// s087d45ExactlyOne fails unless stderr holds exactly one warning about the
// unreadable secrets file, naming the file and carrying the error.
func s087d45ExactlyOne(t *testing.T, label, stderr, secretsPath string) {
	t.Helper()
	recs := s087d45SecretsWarnings(stderr)
	if len(recs) != 1 {
		t.Errorf("%s: %d warnings about the unreadable secrets file, want exactly 1 (R6.3)\nwarnings:\n%s\nstderr:\n%s",
			label, len(recs), strings.Join(recs, "\n"), stderr)
		return
	}
	if !strings.Contains(recs[0], secretsPath) || !strings.Contains(recs[0], " err=") {
		t.Errorf("%s: the warning does not name the file %q and carry the error as err: %q", label, secretsPath, recs[0])
	}
}

// s087d45WantOrphanTable fails unless stdout lists both orphans: proof that
// the run built its Checker and resolved ::gentoo, so the count above is not
// read off a run that stopped early.
func s087d45WantOrphanTable(t *testing.T, label, stdout, stderr string) {
	t.Helper()
	if !strings.Contains(stdout, "Revivable Orphans") {
		t.Errorf("%s: no revivable-orphan report on stdout\nstdout:\n%s\nstderr:\n%s", label, stdout, stderr)
	}
	for _, pkg := range s087d45Pkgs {
		if !strings.Contains(stdout, pkg) {
			t.Errorf("%s: the orphan report does not list %s\nstdout:\n%s", label, pkg, stdout)
		}
	}
}

// TestS087_4_5_CheckRevivableWarnsOnce: --check --revivable builds one Checker
// and resolves ::gentoo; the two token lookups share one warning.
func TestS087_4_5_CheckRevivableWarnsOnce(t *testing.T) {
	w := s087d45Setup(t, false)

	stdout, stderr, code := w.c.Run("overlay", "autoupdate", "--check", "--revivable", "--force")

	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr:\n%s", code, stderr)
	}
	s087d45WantOrphanTable(t, "--check --revivable", stdout, stderr)
	s087d45ExactlyOne(t, "--check --revivable", stderr, w.secretsPath)
}

// TestS087_4_5_ReviveListWarnsOnce: --revive-list builds one Checker and
// resolves ::gentoo; the two token lookups share one warning.
func TestS087_4_5_ReviveListWarnsOnce(t *testing.T) {
	w := s087d45Setup(t, false)

	stdout, stderr, code := w.c.Run("overlay", "autoupdate", "--revive-list")

	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr:\n%s", code, stderr)
	}
	s087d45WantOrphanTable(t, "--revive-list", stdout, stderr)
	s087d45ExactlyOne(t, "--revive-list", stderr, w.secretsPath)
}

// TestS087_4_5_ReviveOverTwoTargetsWarnsOnce is the hostile half against a
// split per Checker: --revive all over two orphans resolves ::gentoo, builds
// one Checker to list the targets and one fresh Checker per target, and still
// warns exactly once.
func TestS087_4_5_ReviveOverTwoTargetsWarnsOnce(t *testing.T) {
	w := s087d45Setup(t, true)

	stdout, stderr, code := w.c.Run("overlay", "autoupdate", "--revive", "all",
		"--distdir", w.distdir, "--distfiles-cache", "")

	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	// Both targets went through Reviver.Revive up to their own Checker: each is
	// reported as already current, which only a per-target check can conclude.
	for _, pkg := range s087d45Pkgs {
		if !strings.Contains(stdout, "Reviving "+pkg+"...") {
			t.Errorf("--revive all did not process %s\nstdout:\n%s\nstderr:\n%s", pkg, stdout, stderr)
		}
		want := fmt.Sprintf("  - %s: gentoo %s already current with upstream %s", pkg, s087d45GentooVersion, s087d45GentooVersion)
		if !strings.Contains(stdout, want) {
			t.Errorf("--revive all: no per-target check outcome %q\nstdout:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}
	s087d45ExactlyOne(t, "--revive all (2 targets)", stderr, w.secretsPath)
}

// TestS087_4_5_EachRunWarnsAgain is the hostile half against a collapse: once
// per run is not once per process. Two runs in one process each warn exactly
// once.
func TestS087_4_5_EachRunWarnsAgain(t *testing.T) {
	w := s087d45Setup(t, false)

	for i := range 2 {
		label := fmt.Sprintf("run %d of --check --revivable", i+1)
		stdout, stderr, code := w.c.Run("overlay", "autoupdate", "--check", "--revivable", "--force")
		if code != 0 {
			t.Errorf("%s: exit code = %d, want 0\nstderr:\n%s", label, code, stderr)
		}
		s087d45WantOrphanTable(t, label, stdout, stderr)
		s087d45ExactlyOne(t, label, stderr, w.secretsPath)
	}
}
