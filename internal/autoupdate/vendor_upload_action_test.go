package autoupdate

import (
	"errors"
	"fmt"
	"testing"
)

// The action is read back from the pending error, and from nothing else.
func TestVendorUploadAction(t *testing.T) {
	pending := fmt.Errorf("%w: dev-go/x-1.0: upload /k/x-1.0-vendor.tar.xz to https://h/x-1.0-vendor.tar.xz", ErrVendorUploadPending)
	if got, ok := VendorUploadAction(pending); !ok || got != "upload /k/x-1.0-vendor.tar.xz to https://h/x-1.0-vendor.tar.xz" {
		t.Fatalf("VendorUploadAction(pending) = %q, %v", got, ok)
	}
	wrapped := fmt.Errorf("apply: %w", pending)
	if _, ok := VendorUploadAction(wrapped); !ok {
		t.Fatal("a wrapped pending error lost its action")
	}
	for _, err := range []error{nil, errors.New("upload failed: upload x to y"), ErrVendorUpload} {
		if got, ok := VendorUploadAction(err); ok {
			t.Fatalf("VendorUploadAction(%v) = %q, true; want false", err, got)
		}
	}
}
