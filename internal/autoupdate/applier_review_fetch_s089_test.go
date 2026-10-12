package autoupdate

// Story 089, sub-task 3.1 — the bump reviewer fetches the PREVIOUS archive when
// no local tree holds it (R4, R7.1, R7.5, R7.7).
//
// Contract under test, stated from the requirement and never from an
// implementation:
//
//   - When the candidate archive is found and is a Meson project, and the
//     previous version's archive is in neither --distdir nor the distfiles
//     cache, reviewBump runs `ebuild <published previous ebuild> fetch` with the
//     overlay root as its working directory and a PRIVATE DISTDIR, created
//     under fixSandboxRoot and removed before reviewBump returns.
//   - A fetch that fails, or that leaves no single unambiguous archive, skips
//     the review gate with a reason naming the previous version; the depth
//     floor is returned unchanged.
//   - The fetch never writes into --distdir or the cache, never runs without a
//     reviewer, runs OUTSIDE the LLM slot, logs an INFO event, and stops when
//     ctx ends.
//
// fixSandboxRoot is a package global, so no test here runs in parallel.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

const (
	s089fPkg        = "media-libs/m"
	s089fOldVersion = "1.4.2"
	s089fNewVersion = "2.0.0"
	s089fPrivPrefix = "bentoo-review-distfiles-"
	s089fFetchedMsg = "bump review: fetched the previous archive"
)

// s089fSyncBuffer is a goroutine-safe sink for the applier's JSON log.
type s089fSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *s089fSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *s089fSyncBuffer) events(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

// s089fReviewer is a fake bump reviewer. It records every request and, while
// the call is in flight, what the OldArchive path actually held — the private
// directory is gone once reviewBump returns, so that is the only moment it can
// be observed. Like the real reviewer it skips when an archive is missing, with
// a reason that names no version, so a skip naming the version can only come
// from the applier.
type s089fReviewer struct {
	mu       sync.Mutex
	requests []fixer.BumpReviewRequest
	oldBytes [][]byte
}

func (r *s089fReviewer) ReviewBump(_ context.Context, req fixer.BumpReviewRequest) (fixer.BumpReviewReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	var held []byte
	if req.OldArchive != "" {
		held, _ = os.ReadFile(req.OldArchive)
	}
	r.oldBytes = append(r.oldBytes, held)
	if req.OldArchive == "" || req.NewArchive == "" {
		return fixer.BumpReviewReport{Skipped: true, SkipReason: "fake reviewer: an archive was not supplied"}, nil
	}
	return fixer.BumpReviewReport{}, nil
}

func (r *s089fReviewer) snapshot() ([]fixer.BumpReviewRequest, [][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]fixer.BumpReviewRequest(nil), r.requests...), append([][]byte(nil), r.oldBytes...)
}

// s089fCall is one command the exec seam was asked to build.
type s089fCall struct {
	name string
	args []string
	cmd  *exec.Cmd
}

// s089fExec is the exec seam. `ebuild … fetch` runs fetchScript under sh; any
// other command is recorded and runs `true`.
type s089fExec struct {
	mu          sync.Mutex
	calls       []s089fCall
	fetchScript string
	spawned     chan struct{}
}

func (e *s089fExec) command(ctx context.Context, name string, arg ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if name == "ebuild" && len(arg) > 0 && arg[len(arg)-1] == "fetch" {
		cmd = exec.CommandContext(ctx, "sh", "-c", e.fetchScript)
		select {
		case e.spawned <- struct{}{}:
		default:
		}
	} else {
		cmd = exec.CommandContext(ctx, "true")
	}
	e.mu.Lock()
	e.calls = append(e.calls, s089fCall{name: name, args: append([]string(nil), arg...), cmd: cmd})
	e.mu.Unlock()
	return cmd
}

func (e *s089fExec) ebuildCalls() []s089fCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []s089fCall
	for _, c := range e.calls {
		if c.name == "ebuild" {
			out = append(out, c)
		}
	}
	return out
}

// s089fFixture is one bump of media-libs/m from 1.4.2 (published) to 2.0.0
// (staged), with every directory the contract talks about kept apart.
type s089fFixture struct {
	overlay     string // published overlay root
	stage       string // staged repository root
	distdir     string // --distdir
	cache       string // distfiles cache
	fetched     string // cand.fetchedDistdir
	sandboxRoot string // what fixSandboxRoot answers
	mirror      string // where the fake fetch copies the previous archive from
	recDir      string // where the fake fetch records what it saw
	oldArchive  []byte // the previous archive's bytes
	reviewer    *s089fReviewer
	exec        *s089fExec
	logs        *s089fSyncBuffer
	applier     *Applier
	cand        candidatePaths
}

type s089fOptions struct {
	noReviewer        bool
	candidateNotMeson bool
	previousInDistdir bool
	extraOldDist      string // a second Manifest entry for the previous version
}

// s089fTarGz writes a gzipped tarball holding files and returns its bytes.
func s089fTarGz(t *testing.T, path string, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, raw.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return raw.Bytes()
}

func s089fMesonTree(top string) map[string]string {
	return map[string]string{
		top + "/meson.build":       "project('m', 'c', version : '" + strings.TrimPrefix(top, "m-") + "')\n",
		top + "/meson_options.txt": "option('docs', type : 'boolean', value : false)\n",
		top + "/src/m.c":           "int m(void) { return 0; }\n",
	}
}

func s089fWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func s089fLook(t *testing.T, bin string) string {
	t.Helper()
	p, err := exec.LookPath(bin)
	if err != nil {
		t.Skipf("%s not on PATH: %v", bin, err)
	}
	return p
}

// s089fCopyScript is a fake `ebuild … fetch` that records its working
// directory and DISTDIR, then copies the previous archive into DISTDIR.
// Binaries are absolute because the applier may hand the child a reduced env.
func (f *s089fFixture) s089fCopyScript(t *testing.T, names ...string) string {
	t.Helper()
	cp := s089fLook(t, "cp")
	script := "printf '%s' \"$(pwd)\" > '" + filepath.Join(f.recDir, "dir") + "' && " +
		"printf '%s' \"$DISTDIR\" > '" + filepath.Join(f.recDir, "distdir") + "'"
	for _, name := range names {
		script += " && '" + cp + "' '" + filepath.Join(f.mirror, "m-"+s089fOldVersion+".tar.gz") + "' \"$DISTDIR/" + name + "\""
	}
	return script
}

func s089fNewFixture(t *testing.T, opts s089fOptions) *s089fFixture {
	t.Helper()
	f := &s089fFixture{
		overlay:     t.TempDir(),
		stage:       t.TempDir(),
		distdir:     t.TempDir(),
		cache:       t.TempDir(),
		fetched:     t.TempDir(),
		sandboxRoot: t.TempDir(),
		mirror:      t.TempDir(),
		recDir:      t.TempDir(),
		reviewer:    &s089fReviewer{},
		logs:        &s089fSyncBuffer{},
	}

	oldName := "m-" + s089fOldVersion + ".tar.gz"
	newName := "m-" + s089fNewVersion + ".tar.gz"

	// Published 1.4.2: ebuild + Manifest naming its archive.
	pubDir := filepath.Join(f.overlay, s089fPkg)
	s089fWrite(t, filepath.Join(pubDir, "m-"+s089fOldVersion+".ebuild"), "EAPI=8\ninherit meson\nSRC_URI=\"https://example.invalid/${P}.tar.gz\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n")
	oldManifest := "DIST " + oldName + " 100 BLAKE2B 00 SHA512 00\n"
	if opts.extraOldDist != "" {
		oldManifest += "DIST " + opts.extraOldDist + " 100 BLAKE2B 00 SHA512 00\n"
	}
	s089fWrite(t, filepath.Join(pubDir, "Manifest"), oldManifest)

	// Staged 2.0.0: ebuild + Manifest naming its archive.
	stagedDir := filepath.Join(f.stage, s089fPkg)
	stagedEbuild := filepath.Join(stagedDir, "m-"+s089fNewVersion+".ebuild")
	s089fWrite(t, stagedEbuild, "EAPI=8\ninherit meson\nSRC_URI=\"https://example.invalid/${P}.tar.gz\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n")
	s089fWrite(t, filepath.Join(stagedDir, "Manifest"), "DIST "+newName+" 100 BLAKE2B 00 SHA512 00\n")

	// The candidate archive, in the run's fetched distdir and in --distdir.
	newFiles := s089fMesonTree("m-" + s089fNewVersion)
	if opts.candidateNotMeson {
		newFiles = map[string]string{"m-" + s089fNewVersion + "/CMakeLists.txt": "project(m C)\n"}
	}
	s089fTarGz(t, filepath.Join(f.fetched, newName), newFiles)
	s089fTarGz(t, filepath.Join(f.distdir, newName), newFiles)

	// The previous archive lives only on the "mirror" unless asked otherwise.
	f.oldArchive = s089fTarGz(t, filepath.Join(f.mirror, oldName), s089fMesonTree("m-"+s089fOldVersion))
	if opts.previousInDistdir {
		s089fTarGz(t, filepath.Join(f.distdir, oldName), s089fMesonTree("m-"+s089fOldVersion))
	}

	// Unrelated content in both shared trees, so a hash over them means something.
	s089fWrite(t, filepath.Join(f.distdir, "unrelated-9.tar.gz"), "unrelated distdir content\n")
	s089fWrite(t, filepath.Join(f.cache, "other-1.0.tar.gz"), "unrelated cache content\n")

	f.exec = &s089fExec{spawned: make(chan struct{}, 4)}
	f.exec.fetchScript = f.s089fCopyScript(t, oldName)

	prevRoot := fixSandboxRoot
	fixSandboxRoot = func(context.Context) string { return f.sandboxRoot }
	t.Cleanup(func() { fixSandboxRoot = prevRoot })

	configDir := t.TempDir()
	options := []ApplierOption{
		WithExecCommand(f.exec.command),
		WithApplierDistdir(f.distdir, ""),
		WithApplierDistfilesCache(f.cache),
		WithApplierLLMConcurrency(1),
		WithApplierLogger(slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	}
	if !opts.noReviewer {
		options = append(options, WithApplierBumpReviewer(f.reviewer))
	}
	a, err := NewApplier(f.overlay, configDir, options...)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	f.applier = a
	f.cand = candidatePaths{
		staged:         true,
		repoRoot:       f.stage,
		pkgDir:         stagedDir,
		ebuildPath:     stagedEbuild,
		fetchedDistdir: f.fetched,
	}
	return f
}

var s089fFloor = validate.DepthDecision{Reason: "s089 floor"}

func (f *s089fFixture) review(ctx context.Context) (validate.DepthDecision, []validate.GateResult) {
	var gates []validate.GateResult
	got := f.applier.reviewBump(ctx, f.cand, s089fPkg, s089fOldVersion, s089fNewVersion, s089fFloor, &gates)
	return got, gates
}

// s089fLeftovers lists private distdirs still present under the sandbox root.
func (f *s089fFixture) s089fLeftovers(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.sandboxRoot)
	if err != nil {
		t.Fatalf("read sandbox root: %v", err)
	}
	var left []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), s089fPrivPrefix) {
			left = append(left, e.Name())
		}
	}
	return left
}

func s089fReviewGates(gates []validate.GateResult) []validate.GateResult {
	var out []validate.GateResult
	for _, g := range gates {
		if g.Gate == validate.GateReview {
			out = append(out, g)
		}
	}
	return out
}

// s089fRequireFetchSpawned fails the test unless exactly one `ebuild … fetch`
// was built — every scenario below is vacuous without it.
func (f *s089fFixture) s089fRequireFetchSpawned(t *testing.T) s089fCall {
	t.Helper()
	calls := f.exec.ebuildCalls()
	if len(calls) != 1 {
		t.Fatalf("ebuild spawns = %d, want exactly 1 `ebuild <previous ebuild> fetch` (previous archive is in neither --distdir nor the cache)", len(calls))
	}
	return calls[0]
}

// s089fRequireSkipNaming asserts one SKIPPED review gate whose reason names the
// previous version, and that the floor came back unchanged.
func s089fRequireSkipNaming(t *testing.T, got validate.DepthDecision, gates []validate.GateResult, extra ...string) {
	t.Helper()
	if got != s089fFloor {
		t.Errorf("reviewBump returned %+v, want the floor %+v unchanged", got, s089fFloor)
	}
	review := s089fReviewGates(gates)
	if len(review) != 1 {
		t.Fatalf("review gates = %+v, want exactly one", review)
	}
	if review[0].Outcome != validate.OutcomeSkipped {
		t.Fatalf("review gate outcome = %v (reason %q), want SKIPPED", review[0].Outcome, review[0].Reason)
	}
	if !strings.Contains(review[0].Reason, s089fOldVersion) {
		t.Errorf("skip reason %q does not name the previous version %s", review[0].Reason, s089fOldVersion)
	}
	if len(extra) > 0 {
		for _, want := range extra {
			if strings.Contains(review[0].Reason, want) {
				return
			}
		}
		t.Errorf("skip reason %q names none of %q (the fetch error)", review[0].Reason, extra)
	}
}

func s089fTreeHash(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		h.Write([]byte(rel + "\x00"))
		if !d.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h.Write(body)
			h.Write([]byte{0})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("hash %s: %v", root, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func s089fCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Scenario 1 (R4.1, R4.2): the fetched archive reaches the reviewer from a
// private DISTDIR that is gone once reviewBump returns.
func TestS089FetchHandsReviewerThePreviousArchiveFromAPrivateDistdir(t *testing.T) {
	// A host DISTDIR must not leak into the child next to the private one.
	t.Setenv("DISTDIR", "/s089-host-distdir-must-not-leak")
	f := s089fNewFixture(t, s089fOptions{})

	got, gates := f.review(s089fCtx(t))

	call := f.s089fRequireFetchSpawned(t)
	wantEbuild := f.applier.EbuildPath(s089fPkg, s089fOldVersion)
	if !strings.HasPrefix(wantEbuild, f.overlay) {
		t.Fatalf("EbuildPath(%s, %s) = %q is not in the published overlay %q", s089fPkg, s089fOldVersion, wantEbuild, f.overlay)
	}
	if len(call.args) != 2 || call.args[0] != wantEbuild || call.args[1] != "fetch" {
		t.Errorf("spawned `ebuild %s`, want `ebuild %s fetch`", strings.Join(call.args, " "), wantEbuild)
	}
	if filepath.Clean(call.cmd.Dir) != filepath.Clean(f.overlay) {
		t.Errorf("fetch Dir = %q, want the overlay root %q", call.cmd.Dir, f.overlay)
	}

	reqs, held := f.reviewer.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("reviewer called %d times, want 1", len(reqs))
	}
	old := reqs[0].OldArchive
	if old == "" {
		t.Fatalf("reviewer got no OldArchive; want the archive the fetch wrote")
	}
	priv := filepath.Dir(old)
	if filepath.Dir(priv) != f.sandboxRoot || !strings.HasPrefix(filepath.Base(priv), s089fPrivPrefix) {
		t.Errorf("OldArchive %q is not inside a %s* dir under the sandbox root %q", old, s089fPrivPrefix, f.sandboxRoot)
	}
	for _, shared := range []string{f.distdir, f.cache, f.fetched} {
		if strings.HasPrefix(old, shared+string(filepath.Separator)) {
			t.Errorf("OldArchive %q lies in a shared tree %q, want the private DISTDIR", old, shared)
		}
	}
	if !bytes.Equal(held[0], f.oldArchive) {
		t.Errorf("OldArchive held %d bytes during the review, want the fetched archive's %d", len(held[0]), len(f.oldArchive))
	}
	if reqs[0].NewArchive == "" {
		t.Errorf("reviewer got no NewArchive although the candidate archive is present")
	}

	var distdirs []string
	for _, kv := range call.cmd.Env {
		if strings.HasPrefix(kv, "DISTDIR=") {
			distdirs = append(distdirs, strings.TrimPrefix(kv, "DISTDIR="))
		}
	}
	if len(distdirs) != 1 || distdirs[0] != priv {
		t.Errorf("fetch Env DISTDIR entries = %q, want exactly one = %q", distdirs, priv)
	}
	if seen, err := os.ReadFile(filepath.Join(f.recDir, "distdir")); err != nil || string(seen) != priv {
		t.Errorf("the fetch child saw DISTDIR=%q (err %v), want %q", seen, err, priv)
	}

	if _, err := os.Stat(priv); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("private DISTDIR %q still exists after reviewBump returned (stat err %v)", priv, err)
	}
	if left := f.s089fLeftovers(t); len(left) != 0 {
		t.Errorf("private distdirs left under the sandbox root: %q", left)
	}
	if review := s089fReviewGates(gates); len(review) != 1 || review[0].Outcome != validate.OutcomePass {
		t.Errorf("review gates = %+v, want one PASS", review)
	}
	if got != s089fFloor {
		t.Errorf("reviewBump returned %+v, want the floor (the fake proposes nothing)", got)
	}
}

// Scenario 2 (R4.4, R7.5): a failing fetch skips the review gate, names the
// previous version and the error, and leaves the floor alone.
func TestS089FetchFailureSkipsTheReviewNamingVersionAndError(t *testing.T) {
	f := s089fNewFixture(t, s089fOptions{})
	f.exec.fetchScript = "echo 's089 no mirror answered' >&2; exit 1"

	got, gates := f.review(s089fCtx(t))

	f.s089fRequireFetchSpawned(t)
	s089fRequireSkipNaming(t, got, gates, "exit status 1", "s089 no mirror answered")
	if reqs, _ := f.reviewer.snapshot(); len(reqs) != 0 {
		t.Errorf("reviewer called %d times after a failed fetch, want 0", len(reqs))
	}
	if left := f.s089fLeftovers(t); len(left) != 0 {
		t.Errorf("private distdirs left after a failed fetch: %q", left)
	}
}

// Scenario 3 (R4.4): a fetch that succeeds but leaves no single usable archive
// skips the review — the applier never guesses.
func TestS089FetchWithNoUsableArchiveSkipsTheReview(t *testing.T) {
	oldName := "m-" + s089fOldVersion + ".tar.gz"
	docsName := "m-" + s089fOldVersion + "-docs.tar.gz"
	cases := []struct {
		name  string
		opts  s089fOptions
		files []string // what the fake fetch writes into DISTDIR
	}{
		{name: "fetch writes nothing", opts: s089fOptions{}, files: nil},
		{name: "ambiguous pair for the previous version", opts: s089fOptions{extraOldDist: docsName}, files: []string{oldName, docsName}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := s089fNewFixture(t, tc.opts)
			f.exec.fetchScript = f.s089fCopyScript(t, tc.files...)

			got, gates := f.review(s089fCtx(t))

			f.s089fRequireFetchSpawned(t)
			s089fRequireSkipNaming(t, got, gates)
			reqs, _ := f.reviewer.snapshot()
			for _, r := range reqs {
				if r.OldArchive != "" {
					t.Errorf("reviewer got OldArchive %q, want none when the fetch left no single archive", r.OldArchive)
				}
			}
			if left := f.s089fLeftovers(t); len(left) != 0 {
				t.Errorf("private distdirs left: %q", left)
			}
		})
	}
}

// Scenario 4 (R4.3): the fetch writes into neither --distdir nor the cache.
func TestS089FetchLeavesDistdirAndCacheUntouched(t *testing.T) {
	f := s089fNewFixture(t, s089fOptions{})
	beforeDist, beforeCache := s089fTreeHash(t, f.distdir), s089fTreeHash(t, f.cache)

	f.review(s089fCtx(t))

	// Without a fetch the hash comparison below proves nothing.
	f.s089fRequireFetchSpawned(t)
	if after := s089fTreeHash(t, f.distdir); after != beforeDist {
		t.Errorf("--distdir changed across the review fetch")
	}
	if after := s089fTreeHash(t, f.cache); after != beforeCache {
		t.Errorf("distfiles cache changed across the review fetch")
	}
}

// Scenario 5 (R7.1 and the fetch's preconditions) — REGRESSION GUARD, passes
// before 3.1: no `ebuild` is spawned when there is no reviewer, when the
// previous archive is already in --distdir, or when the candidate is not Meson.
func TestS089FetchNotSpawnedWhenItIsNotNeeded(t *testing.T) {
	cases := []struct {
		name string
		opts s089fOptions
	}{
		{name: "no reviewer wired", opts: s089fOptions{noReviewer: true}},
		{name: "previous archive already in distdir", opts: s089fOptions{previousInDistdir: true}},
		{name: "candidate is not a Meson project", opts: s089fOptions{candidateNotMeson: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := s089fNewFixture(t, tc.opts)
			f.review(s089fCtx(t))
			if calls := f.exec.ebuildCalls(); len(calls) != 0 {
				t.Errorf("ebuild spawned %d times (%q), want none", len(calls), calls[0].args)
			}
			if left := f.s089fLeftovers(t); len(left) != 0 {
				t.Errorf("private distdirs created although no fetch was needed: %q", left)
			}
			if tc.opts.previousInDistdir {
				reqs, _ := f.reviewer.snapshot()
				want := filepath.Join(f.distdir, "m-"+s089fOldVersion+".tar.gz")
				if len(reqs) != 1 || reqs[0].OldArchive != want {
					t.Errorf("reviewer requests = %+v, want one with OldArchive %q", reqs, want)
				}
			}
		})
	}
}

// Scenario 6 (R7.7): the fetch is not an LLM agent, so it runs while every LLM
// slot is held by someone else.
func TestS089FetchRunsWhileTheLLMSlotIsHeld(t *testing.T) {
	f := s089fNewFixture(t, s089fOptions{})
	if f.applier.slots.llm == nil {
		t.Fatalf("applier has no LLM slot pool")
	}
	f.applier.slots.llm <- struct{}{} // the only slot, held by "another agent"
	held := true
	release := func() {
		if held {
			<-f.applier.slots.llm
			held = false
		}
	}
	t.Cleanup(release)

	done := make(chan struct{})
	go func() {
		defer close(done)
		f.review(s089fCtx(t))
	}()

	select {
	case <-f.exec.spawned:
	case <-time.After(5 * time.Second):
		release()
		<-done
		t.Fatalf("no `ebuild … fetch` was spawned within 5s while the LLM slot was held")
	}
	// The fetch must also COMPLETE with the slot still held.
	marker := filepath.Join(f.recDir, "distdir")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			release()
			<-done
			t.Fatalf("the fetch did not finish within 5s while the LLM slot was held")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("reviewBump did not return after the LLM slot was released")
	}
	if reqs, _ := f.reviewer.snapshot(); len(reqs) != 1 || reqs[0].OldArchive == "" {
		t.Errorf("reviewer requests = %+v, want one carrying the fetched OldArchive", reqs)
	}
}

// Scenario 7 (R4.5): one INFO event per successful fetch, carrying package,
// version, bytes and duration.
func TestS089FetchLogsAnInfoEvent(t *testing.T) {
	f := s089fNewFixture(t, s089fOptions{})

	f.review(s089fCtx(t))

	var found []map[string]any
	for _, ev := range f.logs.events(t) {
		if ev["msg"] == s089fFetchedMsg {
			found = append(found, ev)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%q events = %d, want 1", s089fFetchedMsg, len(found))
	}
	ev := found[0]
	if ev["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", ev["level"])
	}
	if ev["package"] != s089fPkg {
		t.Errorf("package = %v, want %s", ev["package"], s089fPkg)
	}
	if ev["version"] != s089fOldVersion {
		t.Errorf("version = %v, want the previous version %s", ev["version"], s089fOldVersion)
	}
	if b, ok := ev["bytes"].(float64); !ok || int(b) != len(f.oldArchive) {
		t.Errorf("bytes = %v, want %d", ev["bytes"], len(f.oldArchive))
	}
	if _, ok := ev["duration"]; !ok {
		t.Errorf("event has no duration: %v", ev)
	}
}

// Scenario 8 (R4.6): a done ctx stops a running fetch and the review is
// skipped promptly.
func TestS089FetchStopsWhenTheContextEnds(t *testing.T) {
	f := s089fNewFixture(t, s089fOptions{})
	f.exec.fetchScript = "exec '" + s089fLook(t, "sleep") + "' 30"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type result struct {
		got   validate.DepthDecision
		gates []validate.GateResult
	}
	out := make(chan result, 1)
	go func() {
		got, gates := f.review(ctx)
		out <- result{got, gates}
	}()

	select {
	case <-f.exec.spawned:
	case <-time.After(5 * time.Second):
		cancel()
		<-out
		t.Fatalf("no `ebuild … fetch` was spawned within 5s")
	}
	time.Sleep(100 * time.Millisecond) // let the child start sleeping
	cancel()

	select {
	case r := <-out:
		if r.got != s089fFloor {
			t.Errorf("reviewBump returned %+v, want the floor", r.got)
		}
		review := s089fReviewGates(r.gates)
		if len(review) != 1 || review[0].Outcome != validate.OutcomeSkipped {
			t.Errorf("review gates = %+v, want one SKIPPED", review)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("reviewBump did not return within 5s of ctx ending (the fetch was not stopped)")
	}
	if left := f.s089fLeftovers(t); len(left) != 0 {
		t.Errorf("private distdirs left after cancellation: %q", left)
	}
}
