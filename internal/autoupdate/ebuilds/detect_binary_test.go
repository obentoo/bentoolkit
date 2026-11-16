package ebuilds

import "testing"

// TestDetectBinaryPackage_DependencyAtomIsNotEvidence: the -bin evidence must
// come from SRC_URI. app-emulation/qemu ships a .tar.xz and depends on
// sys-firmware/edk2-bin-${EDK2_OVMF_VERSION}; reading "-bin-" anywhere in the
// ebuild classified it as a prebuilt blob, so a check validated it at depth
// none and "proved" it by a download instead of a build.
func TestDetectBinaryPackage_DependencyAtomIsNotEvidence(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"source tarball with a -bin dependency", `SRC_URI="https://download.qemu.org/${P}.tar.xz"
RDEPEND="=sys-firmware/edk2-bin-${EDK2_OVMF_VERSION}*"
`, false},
		{"-bin archive in SRC_URI", `SRC_URI="https://example.com/${PN}-bin-${PV}.tar.gz"
`, true},
		{"${PN}-bin in SRC_URI", `SRC_URI="https://example.com/v${PV}/${PN}-bin.tar.xz"
`, true},
		{"bindist", `SRC_URI="https://example.com/x.deb"
RESTRICT="bindist mirror strip"
`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectBinaryPackage([]byte(tt.content)); got != tt.want {
				t.Errorf("DetectBinaryPackage = %v, want %v", got, tt.want)
			}
		})
	}
}
