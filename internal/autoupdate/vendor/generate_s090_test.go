package vendor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// s090Call is one child the code under test asked the Runner for.
type s090Call struct {
	dir  string
	env  []string
	name string
	args []string
}

// s090Runner runs the real tar (local files only) and fakes `go mod vendor`
// by writing a vendor tree into the directory it is run in.
type s090Runner struct {
	mu         sync.Mutex
	calls      []s090Call
	modulesTxt string
	goFail     bool
	goOut      []byte
	goBlock    bool
	readOnly   bool
}

func (r *s090Runner) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, s090Call{dir: dir, env: slices.Clone(env), name: name, args: slices.Clone(args)})
	r.mu.Unlock()
	switch name {
	case "tar":
		cmd := exec.CommandContext(ctx, "tar", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		return cmd.CombinedOutput()
	case "go":
		if r.goBlock {
			select {
			case <-ctx.Done():
				return []byte("killed"), ctx.Err()
			case <-time.After(5 * time.Second):
				return nil, errors.New("fake go was never cancelled")
			}
		}
		if r.readOnly {
			ro := filepath.Join(dir, ".modcache", "pkg")
			if err := os.MkdirAll(ro, 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(ro, "f.go"), []byte("package f\n"), 0o444); err != nil {
				return nil, err
			}
			_ = os.Chmod(ro, 0o555)
			_ = os.Chmod(filepath.Dir(ro), 0o555)
		}
		if r.goFail {
			return r.goOut, errors.New("exit status 1")
		}
		// Created out of name order so a missing --sort=name shows.
		txt := r.modulesTxt
		if txt == "" {
			txt = "# example.com/adep v1.0.0\n## explicit\nexample.com/adep\n"
		}
		files := map[string]string{
			"vendor/modules.txt":           txt,
			"vendor/example.com/zdep/z.go": "package zdep\n",
			"vendor/example.com/mdep/m.go": "package mdep\n",
			"vendor/example.com/adep/a.go": "package adep\n",
		}
		for _, rel := range []string{"vendor/modules.txt", "vendor/example.com/zdep/z.go", "vendor/example.com/mdep/m.go", "vendor/example.com/adep/a.go"} {
			p := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(p, []byte(files[rel]), 0o644); err != nil {
				return nil, err
			}
		}
		return []byte("go: vendored\n"), nil
	}
	return nil, errors.New("unexpected child " + name)
}

func (r *s090Runner) callsNamed(name string) []s090Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []s090Call
	for _, c := range r.calls {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func s090NeedTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"tar", "xz"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not on PATH; the vendor tarball is written by the host tar/xz", bin)
		}
	}
}

// s090Source writes a gzip tar holding the given files (dirs end in "/").
func s090Source(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		hdr := &tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), ModTime: time.Unix(1700000000, 0), Typeflag: tar.TypeReg}
		if strings.HasSuffix(n, "/") {
			hdr = &tar.Header{Name: n, Mode: 0o755, ModTime: time.Unix(1700000000, 0), Typeflag: tar.TypeDir}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(files[n])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

var s090AppSource = map[string]string{
	"app-1.0/":        "",
	"app-1.0/go.mod":  "module example.com/app\n\ngo 1.21\n",
	"app-1.0/main.go": "package main\n\nfunc main() {}\n",
	"app-1.0/README":  "not vendored\n",
}

type s090Env struct {
	runner  *s090Runner
	req     GenerateRequest
	workDir string
}

// s090Setup lays out separate source, output and work-root directories, and
// makes everything under the work root writable again before t.TempDir's
// own removal runs, so a leak is reported by the test, not by the cleanup.
func s090Setup(t *testing.T, files map[string]string) *s090Env {
	t.Helper()
	tmp := t.TempDir()
	for _, d := range []string{"src", "out", "work"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	work := filepath.Join(tmp, "work")
	t.Cleanup(func() { s090MakeWritable(work) })
	return &s090Env{
		runner:  &s090Runner{},
		workDir: work,
		req: GenerateRequest{
			SourcePath: s090Source(t, filepath.Join(tmp, "src"), "app-1.0.tar.gz", files),
			VendorFile: "app-1.0-vendor.tar.xz",
			OutDir:     filepath.Join(tmp, "out"),
			WorkRoot:   work,
			Timeout:    time.Minute,
		},
	}
}

func s090MakeWritable(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
}

// s090Effective is the value a child sees for key: the last assignment wins,
// on top of the inherited environment.
func s090Effective(env []string, key string) string {
	val := os.Getenv(key)
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			val = v
		}
	}
	return val
}

func s090AssertWorkRootEmpty(t *testing.T, root string) {
	t.Helper()
	left, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading the work root: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("the work root still holds %d entr(ies) after the step: %v", len(left), left)
	}
}

// R2.1: `go mod vendor` runs in the single top directory with the recipe's
// environment, even when the operator's environment says otherwise.
func TestGenerateRunsGoModVendorInTheSingleTopDir(t *testing.T) {
	s090NeedTools(t)
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOTOOLCHAIN", "go1.99.0")
	env := s090Setup(t, s090AppSource)

	if _, err := Generate(t.Context(), env.runner, env.req); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	gos := env.runner.callsNamed("go")
	if len(gos) != 1 {
		t.Fatalf("go was run %d time(s), want 1: %+v", len(gos), env.runner.calls)
	}
	call := gos[0]
	if !slices.Equal(call.args, []string{"mod", "vendor"}) {
		t.Errorf("go args = %q, want [mod vendor]", call.args)
	}
	if filepath.Base(call.dir) != "app-1.0" {
		t.Errorf("go ran in %q, want the source's top directory app-1.0", call.dir)
	}
	if rel, err := filepath.Rel(env.workDir, call.dir); err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("go ran in %q, outside the work root %q", call.dir, env.workDir)
	}
	if got := s090Effective(call.env, "GOFLAGS"); got != "-modcacherw" {
		t.Errorf("GOFLAGS seen by go = %q, want -modcacherw", got)
	}
	if got := s090Effective(call.env, "GOTOOLCHAIN"); got != "local" {
		t.Errorf("GOTOOLCHAIN seen by go = %q, want local", got)
	}
}

func s090ReadTarXz(t *testing.T, path string) []*tar.Header {
	t.Helper()
	raw, err := exec.Command("xz", "-dc", path).Output()
	if err != nil {
		t.Fatalf("xz -dc %s: %v (is it an xz file?)", path, err)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	var hdrs []*tar.Header
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		hdrs = append(hdrs, h)
	}
	return hdrs
}

// R2.2, R2.4: only <top>/vendor, sorted by name, owner/group 0, numeric
// ids, mtime 0, written into OutDir under the vendor file name.
func TestGenerateWritesTheRecipeTarball(t *testing.T) {
	s090NeedTools(t)
	t.Setenv("XZ_OPT", "-0")
	env := s090Setup(t, s090AppSource)

	got, err := Generate(t.Context(), env.runner, env.req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if want := filepath.Join(env.req.OutDir, env.req.VendorFile); got != want {
		t.Errorf("Generate returned %q, want %q", got, want)
	}
	hdrs := s090ReadTarXz(t, got)
	var names []string
	for _, h := range hdrs {
		names = append(names, h.Name)
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: owner %d(%q):%d(%q), want 0:0 numeric", h.Name, h.Uid, h.Uname, h.Gid, h.Gname)
		}
		if !h.ModTime.Equal(time.Unix(0, 0)) {
			t.Errorf("%s: mtime %v, want the epoch", h.Name, h.ModTime)
		}
	}
	want := []string{
		"app-1.0/vendor/",
		"app-1.0/vendor/example.com/",
		"app-1.0/vendor/example.com/adep/",
		"app-1.0/vendor/example.com/adep/a.go",
		"app-1.0/vendor/example.com/mdep/",
		"app-1.0/vendor/example.com/mdep/m.go",
		"app-1.0/vendor/example.com/zdep/",
		"app-1.0/vendor/example.com/zdep/z.go",
		"app-1.0/vendor/modules.txt",
	}
	if !slices.Equal(names, want) {
		t.Errorf("tarball entries\n got %q\nwant %q", names, want)
	}
	var creates []s090Call
	for _, c := range env.runner.callsNamed("tar") {
		if slices.ContainsFunc(c.args, func(a string) bool { return strings.Contains(a, env.req.VendorFile) }) {
			creates = append(creates, c)
		}
	}
	if len(creates) == 0 {
		t.Fatalf("no tar call named %s: %+v", env.req.VendorFile, env.runner.calls)
	}
	if got := s090Effective(creates[len(creates)-1].env, "XZ_OPT"); got != "-9T0" {
		t.Errorf("XZ_OPT seen by the packing tar = %q, want -9T0 whatever the operator exported", got)
	}
}

// R2.5: a source that is not one top directory holding go.mod fails, naming
// the distfile, before go runs.
func TestGenerateRefusesASourceWithoutOneGoModTop(t *testing.T) {
	s090NeedTools(t)
	cases := map[string]map[string]string{
		"two top directories": {"app-1.0/": "", "app-1.0/go.mod": "module a\n", "other/": "", "other/x": "x"},
		"top without go.mod":  {"app-1.0/": "", "app-1.0/main.go": "package main\n"},
		"top dir and a file":  {"app-1.0/": "", "app-1.0/go.mod": "module a\n", "NOTICE": "x"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			env := s090Setup(t, files)
			_, err := Generate(t.Context(), env.runner, env.req)
			if err == nil {
				t.Fatal("Generate accepted the source")
			}
			if !strings.Contains(err.Error(), "app-1.0.tar.gz") {
				t.Errorf("error %q does not name the distfile app-1.0.tar.gz", err)
			}
			if n := len(env.runner.callsNamed("go")); n != 0 {
				t.Errorf("go ran %d time(s) on a refused source", n)
			}
			if _, statErr := os.Stat(filepath.Join(env.req.OutDir, env.req.VendorFile)); statErr == nil {
				t.Error("a tarball was written for a refused source")
			}
		})
	}
}

// R2.6: the error carries the TAIL of go's output, not its head.
func TestGenerateReportsTheTailOfAFailedGoModVendor(t *testing.T) {
	s090NeedTools(t)
	env := s090Setup(t, s090AppSource)
	last := "go: go.mod requires go >= 1.99 (running go 1.27.2; GOTOOLCHAIN=local)"
	out := "HEAD-MARKER\n" + strings.Repeat("downloading example.com/x v1.0.0\n", 300) +
		"WITHIN-TAIL\n" + strings.Repeat("y", 2900) + "\n" + last + "\n"
	env.runner.goFail, env.runner.goOut = true, []byte(out)

	_, err := Generate(t.Context(), env.runner, env.req)
	if err == nil {
		t.Fatal("Generate succeeded although go mod vendor failed")
	}
	msg := err.Error()
	if !strings.Contains(msg, last) || !strings.Contains(msg, "WITHIN-TAIL") {
		t.Errorf("error lacks the last 4 KiB of output: %.300q", msg)
	}
	if strings.Contains(msg, "HEAD-MARKER") {
		t.Error("error carries the head of a >8 KiB output; want only its last 4 KiB")
	}
	if _, statErr := os.Stat(filepath.Join(env.req.OutDir, env.req.VendorFile)); statErr == nil {
		t.Error("a tarball was written after go mod vendor failed")
	}
}

// R2.6: the vendor timeout stops the step.
func TestGenerateStopsAtTheTimeout(t *testing.T) {
	s090NeedTools(t)
	env := s090Setup(t, s090AppSource)
	env.runner.goBlock = true
	env.req.Timeout = 100 * time.Millisecond

	start := time.Now()
	_, err := Generate(t.Context(), env.runner, env.req)
	if err == nil {
		t.Fatal("Generate succeeded past its timeout")
	}
	if waited := time.Since(start); waited > 4*time.Second {
		t.Errorf("Generate returned after %v; the 100ms timeout did not reach the child", waited)
	}
}

// R2.7: the private work dir goes, read-only files included, on success and
// on failure.
func TestGenerateRemovesItsWorkDir(t *testing.T) {
	s090NeedTools(t)
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			env := s090Setup(t, s090AppSource)
			env.runner.readOnly, env.runner.goFail = true, fail
			_, err := Generate(t.Context(), env.runner, env.req)
			if (err != nil) != fail {
				t.Fatalf("Generate error = %v, want failure=%v", err, fail)
			}
			s090AssertWorkRootEmpty(t, env.workDir)
		})
	}
}
