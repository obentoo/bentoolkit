package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// R6.3: an upload-pending package is reported as such, with the kept tarball
// path and the URL it must be served from, not as a generic failure.
func TestDisplayApplyResultUploadPending(t *testing.T) {
	const (
		kept = "/var/tmp/bentoo-staging/dev-go/govulncheck/1.9.0/.bentoo-vendor/govulncheck-1.9.0-vendor.tar.xz"
		url  = "https://distfiles.obentoo.org/govulncheck-1.9.0-vendor.tar.xz"
	)
	result := &autoupdate.ApplyResult{
		Package: "dev-go/govulncheck", OldVersion: "1.8.0", NewVersion: "1.9.0",
		Error:      fmt.Errorf("%w: upload %s to %s", autoupdate.ErrVendorUploadPending, kept, url),
		StagedPath: "/var/tmp/bentoo-staging/dev-go/govulncheck/1.9.0",
	}
	if !errors.Is(result.Error, autoupdate.ErrVendorUploadPending) {
		t.Fatal("fixture error does not wrap ErrVendorUploadPending")
	}
	out := captureStdout(t, func() { (&autoupdateRun{}).displayApplyResult(result) })

	if !strings.Contains(strings.ToLower(out), "upload pending") {
		t.Errorf("output does not say upload pending:\n%s", out)
	}
	for _, want := range []string{kept, url} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not name %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Status:  Failed") {
		t.Errorf("an upload-pending package is rendered as a generic failure:\n%s", out)
	}
}
