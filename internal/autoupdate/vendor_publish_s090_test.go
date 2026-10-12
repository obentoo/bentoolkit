package autoupdate

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	appconfig "github.com/obentoo/bentoolkit/internal/common/config"
)

type s090Apply struct {
	*s090Fix
	applier    *Applier
	pending    *PendingList
	stagedRoot string

	mu         sync.Mutex
	uploads    [][]string
	uploadPath string
	uploaded   []byte
}

const s090Published = "govulncheck-1.9.0.ebuild"

// s090ApplyFixture runs the whole apply of govulncheck 1.8.0 -> 1.9.0 with
// every child faked and the vendor URI answering 404 until an upload serves
// it. The sandbox root's name holds a literal "{file}": a substituted path
// must reach the upload command as it is, never re-substituted.
func s090ApplyFixture(t *testing.T, upload []string, corrupt bool) *s090Apply {
	t.Helper()
	f := s090Fixture(t, "1.9.0", "go-module", s090VendorOK)
	stagingRoot := filepath.Join(t.TempDir(), "staging")
	staged, err := validate.StagedTreePath(stagingRoot, s090Pkg, "1.9.0")
	if err != nil {
		t.Fatal(err)
	}
	f.exec.stagedRoot = staged
	sandbox := filepath.Join(t.TempDir(), "sb-{file}")
	if err := os.MkdirAll(sandbox, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := fixSandboxRoot
	fixSandboxRoot = func(context.Context) string { return sandbox }
	t.Cleanup(func() { fixSandboxRoot = orig })

	env := &s090Apply{s090Fix: f, stagedRoot: staged}
	f.tr.set(http.MethodHead, s090VendorOK, s090Resp{status: 404})
	f.tr.setFunc(http.MethodGet, s090VendorOK, func() s090Resp {
		env.mu.Lock()
		defer env.mu.Unlock()
		if env.uploaded == nil {
			return s090Resp{status: 404}
		}
		return s090Resp{status: 200, body: string(env.uploaded)}
	})
	f.exec.onUpload = func(args []string) {
		env.mu.Lock()
		defer env.mu.Unlock()
		env.uploads = append(env.uploads, args)
		for _, a := range args {
			if p, ok := strings.CutPrefix(a, "--file="); ok {
				env.uploadPath = p
				data, err := os.ReadFile(p)
				if err == nil && !f.exec.uploadFail {
					if corrupt {
						data = append(data, 'X')
					}
					env.uploaded = data
				}
			}
		}
	}

	pending, err := NewPendingList(filepath.Join(t.TempDir(), "config"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Add(PendingUpdate{Package: s090Pkg, CurrentVersion: "1.8.0", NewVersion: "1.9.0", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	env.pending = pending
	env.applier = f.applier(t, nil,
		WithApplierPendingList(pending),
		WithApplierStagingRoot(stagingRoot),
		WithApplierDepth(validate.DepthCompile),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
		WithApplierSetVersionsFunc(func(string, map[string]string) error { return nil }),
		WithApplierVendorConfig(appconfig.VendorConfig{UploadCommand: upload}),
	)
	return env
}

func (e *s090Apply) published() bool {
	_, err := os.Stat(filepath.Join(e.overlay, s090Pkg, s090Published))
	return err == nil
}

var s090UploadCmd = []string{s090Uploader, "obentoo-distfiles/{file}", "--file={path}", "--both={file}:{path}"}

// R5.1, R5.2, R5.4, R5.5: argv substitution per element, no shell, the
// served bytes compared after the upload, then promotion.
func TestApplyUploadsAndVerifiesTheGeneratedTarball(t *testing.T) {
	e := s090ApplyFixture(t, s090UploadCmd, false)
	result, err := e.applier.Apply(t.Context(), s090Pkg, false)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Apply = (%+v, %v), want a promoted bump", result, err)
	}
	if !e.published() {
		t.Error("the bump was not promoted into the overlay")
	}
	for _, shell := range []string{"sh", "bash", "/bin/sh"} {
		if n := len(e.exec.named(shell)); n != 0 {
			t.Errorf("a shell (%s) was run %d time(s) on the upload path", shell, n)
		}
	}
	if len(e.uploads) != 1 {
		t.Fatalf("the upload command ran %d time(s), want 1", len(e.uploads))
	}
	p, file := e.uploadPath, "govulncheck-1.9.0-vendor.tar.xz"
	want := []string{"obentoo-distfiles/" + file, "--file=" + p, "--both=" + file + ":" + p}
	if strings.Join(e.uploads[0], "\x00") != strings.Join(want, "\x00") {
		t.Errorf("upload argv[1:] = %q, want %q", e.uploads[0], want)
	}
	if !filepath.IsAbs(p) || filepath.Base(p) != file || !strings.Contains(p, "sb-{file}") {
		t.Errorf("{path} became %q; want the tarball's absolute path, its literal {file} left alone", p)
	}
	if string(e.uploaded) != s090TarballBytes {
		t.Errorf("the upload read %q from {path}, want the generated tarball", e.uploaded)
	}
	gets := 0
	for _, r := range e.tr.requests() {
		if r.method == http.MethodGet && r.url == s090VendorOK {
			gets++
		}
	}
	if gets == 0 {
		t.Error("the served tarball was never downloaded for the digest comparison")
	}
}

// R5.6, R5.7: a failed upload or a served digest that differs publishes
// nothing and names package, version, URL and reason.
func TestApplyDoesNotPublishWhenTheUploadIsNotProved(t *testing.T) {
	cases := []struct {
		name       string
		fail       bool
		corrupt    bool
		wantReason []string
	}{
		{"upload exits 1", true, false, nil},
		{"served digest differs", false, true, []string{"digest", "sha512"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := s090ApplyFixture(t, s090UploadCmd, tc.corrupt)
			e.exec.uploadFail = tc.fail
			result, err := e.applier.Apply(t.Context(), s090Pkg, false)
			if err == nil || (result != nil && result.Success) {
				t.Fatalf("Apply = (%+v, %v), want a failure", result, err)
			}
			if e.published() {
				t.Error("the bump was promoted although the upload was not proved")
			}
			for _, want := range []string{s090Pkg, "1.9.0", s090VendorOK} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
			if tc.wantReason != nil {
				low := strings.ToLower(err.Error())
				if !strings.Contains(low, tc.wantReason[0]) && !strings.Contains(low, tc.wantReason[1]) {
					t.Errorf("error %q does not say the digest differed", err)
				}
			}
		})
	}
}

// R6.1-R6.4: without an upload command the bump stops as "upload pending",
// keeping the tarball in the staged tree and failing the pending entry.
func TestApplyWithoutUploadCommandParksTheTarball(t *testing.T) {
	e := s090ApplyFixture(t, nil, false)
	result, err := e.applier.Apply(t.Context(), s090Pkg, false)
	if !errors.Is(err, ErrVendorUploadPending) {
		t.Fatalf("Apply error = %v, want ErrVendorUploadPending", err)
	}
	if result == nil || result.Success || e.published() {
		t.Fatalf("an upload-pending bump was published (result %+v)", result)
	}
	if len(e.uploads) != 0 {
		t.Errorf("an upload ran without upload_command: %q", e.uploads)
	}
	var kept string
	_ = filepath.WalkDir(e.stagedRoot, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "govulncheck-1.9.0-vendor.tar.xz" {
			kept = p
		}
		return nil
	})
	if kept == "" {
		t.Fatalf("no govulncheck-1.9.0-vendor.tar.xz kept inside the staged tree %s", e.stagedRoot)
	}
	if data, _ := os.ReadFile(kept); string(data) != s090TarballBytes {
		t.Errorf("kept file %s holds %q, want the generated tarball", kept, data)
	}
	if !strings.Contains(err.Error(), kept) || !strings.Contains(err.Error(), s090VendorOK) {
		t.Errorf("error %q does not name the kept path %s and the URL %s", err, kept, s090VendorOK)
	}
	entry, ok := e.pending.Get(s090Pkg)
	if !ok || entry.Status != StatusFailed || !strings.Contains(entry.Error, "vendor tarball upload pending") {
		t.Errorf("pending entry = %+v, want status failed with reason \"vendor tarball upload pending\"", entry)
	}
}
