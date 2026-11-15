package buildenv

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeTransport records every command and the stdin it was fed, and fails the
// argv0 named in fail.
type fakeTransport struct {
	t      *testing.T
	fail   string
	mu     sync.Mutex
	starts int
	stops  int
	calls  []fakeCall
}

type fakeCall struct {
	argv, env []string
	dir       string
	stdinFile string
}

func (f *fakeTransport) Name() string { return "fake" }
func (f *fakeTransport) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return nil
}
func (f *fakeTransport) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return nil
}
func (f *fakeTransport) Command(ctx context.Context, argv, env []string, dir string) *exec.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	file := filepath.Join(f.t.TempDir(), "stdin-"+strconv.Itoa(len(f.calls)))
	f.calls = append(f.calls, fakeCall{argv: argv, env: env, dir: dir, stdinFile: file})
	code := "0"
	if len(argv) > 0 && argv[0] == f.fail {
		code = "1"
	}
	return exec.CommandContext(ctx, "sh", "-c", `cat > "$1"; exit "$2"`, "sh", file, code)
}

func (f *fakeTransport) call(argv0 string) (fakeCall, bool) {
	for _, c := range f.calls {
		if len(c.argv) > 0 && c.argv[0] == argv0 {
			return c, true
		}
	}
	return fakeCall{}, false
}

func stagedFixture(t *testing.T) (root, distfile string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app-misc", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app-misc", "demo", "demo-2.0.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	distfile = filepath.Join(t.TempDir(), "demo-2.0.tar.gz")
	if err := os.WriteFile(distfile, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, distfile
}

func TestSession_PrepareShipsRegistersAndInstallsDependencies(t *testing.T) {
	ft := &fakeTransport{t: t}
	s := NewSession(ft)
	root, distfile := stagedFixture(t)
	req := PrepareRequest{StagedRoot: root, Distfiles: []string{distfile}, RepoName: "bentoo-staging-demo",
		RepoSection: "[bentoo-staging-demo]\nlocation = " + root + "\n", Atom: "=app-misc/demo-2.0::bentoo-staging-demo"}

	if err := s.Prepare(t.Context(), req); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := s.Prepare(t.Context(), req); err != nil {
		t.Fatalf("second Prepare: %v", err)
	}
	if ft.starts != 1 {
		t.Errorf("started %d environments for one session, want 1", ft.starts)
	}

	ship, ok := ft.call("tar")
	if !ok || !slices.Equal(ship.argv, []string{"tar", "-C", "/", "-xf", "-"}) {
		t.Fatalf("no tar extraction at / was run: %+v", ft.calls)
	}
	entries := tarEntries(t, ship.stdinFile)
	for _, want := range []string{
		strings.TrimPrefix(filepath.Join(root, "app-misc", "demo", "demo-2.0.ebuild"), "/"),
		strings.TrimPrefix(distfile, "/"),
	} {
		if !slices.Contains(entries, want) {
			t.Errorf("the shipped archive lacks %s at its own path; it holds %v", want, entries)
		}
	}

	repo, ok := ft.call("sh")
	if !ok || !slices.Contains(repo.argv, "/etc/portage/repos.conf/bentoo-staging-demo.conf") {
		t.Fatalf("the staged repository was not registered: %+v", ft.calls)
	}
	if body, _ := os.ReadFile(repo.stdinFile); string(body) != req.RepoSection {
		t.Errorf("repos.conf got %q, want the staged section", body)
	}

	deps, ok := ft.call("emerge")
	if !ok {
		t.Fatalf("no dependency install: %+v", ft.calls)
	}
	for _, want := range []string{"--onlydeps", "--with-bdeps=y", "--getbinpkg", req.Atom} {
		if !slices.Contains(deps.argv, want) {
			t.Errorf("the dependency install %v lacks %q", deps.argv, want)
		}
	}
}

func TestSession_ADependencyFailureIsTheEnvironments(t *testing.T) {
	ft := &fakeTransport{t: t, fail: "emerge"}
	root, _ := stagedFixture(t)
	err := NewSession(ft).Prepare(t.Context(), PrepareRequest{StagedRoot: root, RepoName: "r", Atom: "=a/b-1::r"})
	if !errors.Is(err, ErrEnvironment) {
		t.Fatalf("err = %v, want ErrEnvironment", err)
	}
}

func TestSession_AdaptMovesTheBuildChildIn(t *testing.T) {
	ft := &fakeTransport{t: t}
	s := NewSession(ft)
	cmd := exec.Command("ebuild", "/staging/a/b/b-1.ebuild", "clean", "configure")
	cmd.Dir = "/staging"
	cmd.Env = []string{"PATH=/usr/bin", "HOME=/home/x", "DISTDIR=/tmp/d", "FEATURES=-test"}
	cmd.Stdin = strings.NewReader("")

	adapted := s.Adapt(t.Context(), cmd)
	got := ft.calls[len(ft.calls)-1]

	if !slices.Equal(got.argv, cmd.Args) || got.dir != "/staging" {
		t.Errorf("argv %v dir %q, want the gate's own %v in %q", got.argv, got.dir, cmd.Args, cmd.Dir)
	}
	if !slices.Equal(got.env, []string{"DISTDIR=/tmp/d", "FEATURES=-test"}) {
		t.Errorf("env %v, want the gate's variables without the host's PATH and HOME", got.env)
	}
	if adapted.Stdin != cmd.Stdin {
		t.Error("the gate's stdin was not carried over")
	}
	if !s.LastPassFresh() {
		t.Error("the first build child in a new environment is not reported fresh")
	}
	s.Adapt(t.Context(), exec.Command("ebuild"))
	if s.LastPassFresh() {
		t.Error("a second build child in the same environment is reported fresh")
	}
}

func TestSession_FreshReplacesOnlyAUsedEnvironment(t *testing.T) {
	ft := &fakeTransport{t: t}
	s := NewSession(ft)
	root, _ := stagedFixture(t)
	if err := s.Prepare(t.Context(), PrepareRequest{StagedRoot: root, RepoName: "r", Atom: "=a/b-1::r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Fresh(t.Context()); err != nil || ft.starts != 1 || ft.stops != 0 {
		t.Fatalf("Fresh replaced an environment that had built nothing: starts=%d stops=%d err=%v", ft.starts, ft.stops, err)
	}
	s.Adapt(t.Context(), exec.Command("ebuild"))
	if err := s.Fresh(t.Context()); err != nil || ft.starts != 2 || ft.stops != 1 {
		t.Fatalf("Fresh did not replace a used environment: starts=%d stops=%d err=%v", ft.starts, ft.stops, err)
	}
	if err := s.Close(t.Context()); err != nil || ft.stops != 2 {
		t.Fatalf("Close: stops=%d err=%v", ft.stops, err)
	}
	if err := s.Close(t.Context()); err != nil || ft.stops != 2 {
		t.Fatalf("a second Close stopped again: stops=%d err=%v", ft.stops, err)
	}
}

func tarEntries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var names []string
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatalf("reading the shipped archive: %v", err)
		}
		names = append(names, strings.TrimSuffix(hdr.Name, "/"))
	}
}

// TestSession_IsolationIsMeasuredInside: the namespace probe runs in the
// environment, and an environment that is not running is not isolated.
func TestSession_IsolationIsMeasuredInside(t *testing.T) {
	ft := &fakeTransport{t: t}
	s := NewSession(ft)
	if ok, why := s.Isolated(t.Context()); ok || !strings.Contains(why, "not running") {
		t.Errorf("before Start: isolated=%v %q, want no, naming the stopped environment", ok, why)
	}
	if err := s.startLocked(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ok, why := s.Isolated(t.Context()); !ok {
		t.Errorf("isolated = no (%s), want yes", why)
	}
	if c, ok := ft.call("unshare"); !ok || !slices.Contains(c.argv, "--net") {
		t.Errorf("the probe did not run unshare --net in the environment: %+v", ft.calls)
	}
	ft.fail = "unshare"
	if ok, why := s.Isolated(t.Context()); ok || !strings.Contains(why, "fake environment") {
		t.Errorf("failing unshare: isolated=%v %q, want no, naming the environment", ok, why)
	}
}
