package vendor

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// s090RealRunner runs the real tools. testEnv points go at a local file
// proxy; the step's own env is appended last so it wins for its keys.
type s090RealRunner struct{ testEnv []string }

func (r s090RealRunner) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), r.testEnv...), env...)
	return cmd.CombinedOutput()
}

func s090WriteFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// s090Proxy publishes example.com/dep at v1.0.0 and v1.1.0 as a GOPROXY=file:// tree.
func s090Proxy(t *testing.T, root string) {
	t.Helper()
	at := filepath.Join(root, "example.com", "dep", "@v")
	s090WriteFile(t, filepath.Join(at, "list"), "v1.0.0\nv1.1.0\n")
	for _, v := range []string{"v1.0.0", "v1.1.0"} {
		mod := "module example.com/dep\n\ngo 1.21\n"
		s090WriteFile(t, filepath.Join(at, v+".info"), `{"Version":"`+v+`","Time":"2024-01-01T00:00:00Z"}`)
		s090WriteFile(t, filepath.Join(at, v+".mod"), mod)
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, body := range map[string]string{
			"go.mod": mod,
			"dep.go": "package dep\n\n// Version is " + v + ".\nconst Version = \"" + v + "\"\n",
		} {
			w, err := zw.Create("example.com/dep@" + v + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		s090WriteFile(t, filepath.Join(at, v+".zip"), buf.String())
	}
}

// s090AppArchive builds app-1.0.tar.gz requiring dep at depVersion, with a
// go.sum computed offline by `go mod tidy` against the file proxy.
func s090AppArchive(t *testing.T, r s090RealRunner, root, depVersion string) string {
	t.Helper()
	top := filepath.Join(root, "app-1.0")
	s090WriteFile(t, filepath.Join(top, "go.mod"), "module example.com/app\n\ngo 1.21\n\nrequire example.com/dep "+depVersion+"\n")
	s090WriteFile(t, filepath.Join(top, "main.go"), "package main\n\nimport \"example.com/dep\"\n\nfunc main() { println(dep.Version) }\n")
	if out, err := r.Run(t.Context(), top, []string{"GOFLAGS=-modcacherw"}, "go", "mod", "tidy"); err != nil {
		t.Fatalf("go mod tidy in the fixture: %v\n%s", err, out)
	}
	archive := filepath.Join(root, "app-1.0.tar.gz")
	if out, err := exec.Command("tar", "-C", root, "-czf", archive, "app-1.0").CombinedOutput(); err != nil {
		t.Fatalf("packing the fixture source: %v\n%s", err, out)
	}
	return archive
}

func s090Generate(t *testing.T, r s090RealRunner, source string) []byte {
	t.Helper()
	tmp := t.TempDir()
	work, out := filepath.Join(tmp, "work"), filepath.Join(tmp, "out")
	for _, d := range []string{work, out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path, err := Generate(t.Context(), r, GenerateRequest{
		SourcePath: source, VendorFile: "app-1.0-vendor.tar.xz", OutDir: out, WorkRoot: work, Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// R2.3 (Q6): the same source vendored twice gives identical bytes, though the
// two runs differ in work dir, in wall-clock time (file mtimes) and in the
// operator's XZ_OPT; a different dependency gives different bytes.
func TestVendorTarballIsReproducible(t *testing.T) {
	for _, bin := range []string{"go", "tar", "xz"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not on PATH; this test builds a real tarball with the host toolchain", bin)
		}
	}
	tmp := t.TempDir()
	proxy, cache := filepath.Join(tmp, "proxy"), filepath.Join(tmp, "modcache")
	s090Proxy(t, proxy)
	r := s090RealRunner{testEnv: []string{
		"GOPROXY=file://" + proxy, "GOSUMDB=off", "GONOSUMDB=*", "GOMODCACHE=" + cache,
		"GOCACHE=" + filepath.Join(tmp, "gocache"), "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local",
	}}
	t.Cleanup(func() { s090MakeWritable(cache) })

	sourceA := s090AppArchive(t, r, filepath.Join(tmp, "a"), "v1.0.0")
	sourceB := s090AppArchive(t, r, filepath.Join(tmp, "b"), "v1.1.0")

	first := s090Generate(t, r, sourceA)
	time.Sleep(1100 * time.Millisecond)
	t.Setenv("XZ_OPT", "-0")
	second := s090Generate(t, r, sourceA)
	if !bytes.Equal(first, second) {
		t.Fatalf("two runs over the same source differ: %d vs %d bytes", len(first), len(second))
	}

	other := s090Generate(t, r, sourceB)
	if bytes.Equal(first, other) {
		t.Fatal("a source requiring dep v1.1.0 produced the same bytes as one requiring v1.0.0")
	}
}
