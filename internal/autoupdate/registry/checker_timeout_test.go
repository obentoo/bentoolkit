package registry

import (
	"testing"
)

// TestValidatePackageConfig_NegativeTimeout asserts a negative per-package
// timeout is rejected, while zero (the "use global" sentinel) is accepted.
func TestValidatePackageConfig_NegativeTimeout(t *testing.T) {
	bad := &PackageConfig{URL: "https://example.com", Parser: "regex", Pattern: "v(.+)", Timeout: -5}
	if err := ValidatePackageConfig(nil, "cat/pkg", bad); err == nil {
		t.Error("expected an error for a negative timeout, got nil")
	}

	ok := &PackageConfig{URL: "https://example.com", Parser: "regex", Pattern: "v(.+)", Timeout: 0}
	if err := ValidatePackageConfig(nil, "cat/pkg", ok); err != nil {
		t.Errorf("zero timeout should be valid (use global), got: %v", err)
	}
}
