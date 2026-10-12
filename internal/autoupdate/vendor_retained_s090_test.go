package autoupdate

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

const s090VendorFile = "govulncheck-1.9.0-vendor.tar.xz"

// s090VendorRuns counts how often the vendor step ran: one `go mod vendor`
// and one HEAD probe of the vendor URI per run.
func (e *s090Apply) s090VendorRuns() (goRuns, heads int) {
	for _, r := range e.tr.requests() {
		if r.method == http.MethodHead && r.url == s090VendorOK {
			heads++
		}
	}
	return len(e.exec.named("go")), heads
}

func (e *s090Apply) s090Record(t *testing.T) validate.StageRecord {
	t.Helper()
	rec, err := validate.ReadStageRecord(e.stagedRoot)
	if err != nil {
		t.Fatalf("reading the stage record beside %s: %v", e.stagedRoot, err)
	}
	return rec
}

// s090ReapplyRevalidates runs a second --apply over the retained tree and
// asserts it took the gating path and ran the vendor step again.
func (e *s090Apply) s090ReapplyRevalidates(t *testing.T) (*ApplyResult, error) {
	t.Helper()
	goBefore, headBefore := e.s090VendorRuns()
	result, err := e.applier.Apply(t.Context(), s090Pkg, false)
	if result == nil {
		t.Fatalf("second Apply returned no result (err %v)", err)
	}
	if result.ValidationSource == ValidationSourceStaged {
		t.Errorf("the retained tree was promoted as it stands (reason %q)", result.DepthReason)
	}
	if !strings.Contains(result.DepthReason, "vendor tarball generated but not published; re-validating") {
		t.Errorf("DepthReason %q does not say why the retained tree was not reused", result.DepthReason)
	}
	goAfter, headAfter := e.s090VendorRuns()
	if goAfter <= goBefore || headAfter <= headBefore {
		t.Errorf("the vendor step did not run again: go %d -> %d, HEAD %d -> %d", goBefore, goAfter, headBefore, headAfter)
	}
	return result, err
}

// R6.6: a retained tree whose record names a vendor tarball that was generated
// but not published is never promoted as it stands; the next --apply runs the
// vendor step again. A record without the field still reuses.
func TestRetainedTreeWithUnpublishedVendorIsRevalidated(t *testing.T) {
	t.Run("check then apply", func(t *testing.T) {
		e := s090ApplyFixture(t, s090UploadCmd, false)
		e.applier.Validate(t.Context(), s090Pkg, validate.DepthCompile)
		if got := e.s090Record(t).VendorUnpublished; got != s090VendorFile {
			t.Fatalf("the --check record names %q as unpublished, want %q", got, s090VendorFile)
		}

		result, err := e.s090ReapplyRevalidates(t)
		if err != nil || !result.Success || !e.published() {
			t.Fatalf("Apply = (%+v, %v), want the bump promoted after its tarball was uploaded", result, err)
		}
		if len(e.uploads) != 1 {
			t.Errorf("the upload command ran %d time(s), want 1", len(e.uploads))
		}
		if got := e.s090Record(t).VendorUnpublished; got != "" {
			t.Errorf("a published tarball is still recorded as unpublished: %q", got)
		}
	})

	t.Run("upload pending then apply", func(t *testing.T) {
		e := s090ApplyFixture(t, nil, false)
		if _, err := e.applier.Apply(t.Context(), s090Pkg, false); !errors.Is(err, ErrVendorUploadPending) {
			t.Fatalf("first Apply error = %v, want ErrVendorUploadPending", err)
		}
		if got := e.s090Record(t).VendorUnpublished; got != s090VendorFile {
			t.Fatalf("the upload-pending record names %q as unpublished, want %q", got, s090VendorFile)
		}

		_, err := e.s090ReapplyRevalidates(t)
		if !errors.Is(err, ErrVendorUploadPending) {
			t.Errorf("second Apply error = %v, want ErrVendorUploadPending again", err)
		}
		if e.published() {
			t.Error("a bump whose tarball was never uploaded was published")
		}
	})

	t.Run("upload fails then apply", func(t *testing.T) {
		e := s090ApplyFixture(t, s090UploadCmd, false)
		e.exec.uploadFail = true
		if _, err := e.applier.Apply(t.Context(), s090Pkg, false); !errors.Is(err, ErrVendorUpload) {
			t.Fatalf("first Apply error = %v, want ErrVendorUpload", err)
		}
		if got := e.s090Record(t).VendorUnpublished; got != s090VendorFile {
			t.Fatalf("the failed-upload record names %q as unpublished, want %q", got, s090VendorFile)
		}

		e.exec.uploadFail = false
		result, err := e.s090ReapplyRevalidates(t)
		if err != nil || !result.Success || !e.published() {
			t.Fatalf("second Apply = (%+v, %v), want the bump promoted after a successful upload", result, err)
		}
	})

	t.Run("record without the field still reuses", func(t *testing.T) {
		e := s090ApplyFixture(t, s090UploadCmd, false)
		e.applier.Validate(t.Context(), s090Pkg, validate.DepthCompile)
		rec := e.s090Record(t)
		rec.VendorUnpublished = ""
		if err := validate.WriteStageRecord(e.stagedRoot, rec); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(validate.StageRecordPath(e.stagedRoot))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "vendor_unpublished") {
			t.Fatalf("an unset field was written to the record: %s", raw)
		}

		goBefore, _ := e.s090VendorRuns()
		result, err := e.applier.Apply(t.Context(), s090Pkg, false)
		if err != nil || result == nil || !result.Success {
			t.Fatalf("Apply = (%+v, %v), want the retained tree promoted", result, err)
		}
		if result.ValidationSource != ValidationSourceStaged {
			t.Errorf("ValidationSource = %q, want %q (reason %q)", result.ValidationSource, ValidationSourceStaged, result.DepthReason)
		}
		if goAfter, _ := e.s090VendorRuns(); goAfter != goBefore {
			t.Errorf("the vendor step ran again for a record that names no unpublished tarball: go %d -> %d", goBefore, goAfter)
		}
	})
}
