package vendor

import "testing"

// A vendor file name is substituted into the upload argv on its own, so one
// that starts like an option is refused; the accepted shape still passes.
func TestVendorNameRefusesALeadingDash(t *testing.T) {
	for name, want := range map[string]bool{
		"-o-vendor.tar.xz":        false,
		"--force-vendor.tar.xz":   false,
		".x-1.0-vendor.tar.xz":    false,
		"x-1.0-vendor.tar.xz":     true,
		"9base-1.0-vendor.tar.xz": true,
	} {
		if got := vendorName.MatchString(name); got != want {
			t.Errorf("vendorName.MatchString(%q) = %v, want %v", name, got, want)
		}
	}
}
