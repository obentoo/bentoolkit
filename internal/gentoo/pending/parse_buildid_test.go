package pending_test

import (
	"slices"
	"testing"

	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
)

// Under binpkg-multi-instance, Portage prints a binary package's version with
// its build id appended (_emerge/resolver/output.py, _append_build_id), ahead
// of the slot and the repository. ParseWorld drops that id on [binary lines
// only, and only when what remains still ends in a version.
func TestS099ParseWorldBinaryBuildID(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		want       []string
	}{
		{"build id", "[binary   R    ] sys-libs/zlib-1.3.1-r1-1::gentoo", []string{"sys-libs/zlib"}},
		{"build id, slot and old version", "[binary     U  ] app-misc/foo-2.0-3:0::gentoo [1.9::gentoo]", []string{"app-misc/foo"}},
		{"build id, no repository", "[binary  N     ] dev-libs/bar-1.0-1", []string{"dev-libs/bar"}},
		{"revision then build id", "[binary     U  ] net-libs/libssh2-1.11.1-r2-12:0/1::gentoo", []string{"net-libs/libssh2"}},
		{"integer version then build id", "[binary  N     ] app-misc/foo-1-2::gentoo", []string{"app-misc/foo"}},
		{"name ending in a digit segment", "[binary     U  ] media-fonts/font-adobe-100dpi-1.0.4-1::gentoo", []string{"media-fonts/font-adobe-100dpi"}},
		{"no build id", "[binary     U  ] media-fonts/font-adobe-100dpi-1.0.4::gentoo", []string{"media-fonts/font-adobe-100dpi"}},
		{"no build id, revision", "[binary     U  ] app-editors/vim-9.1.0-r1::gentoo", []string{"app-editors/vim"}},
		{"no build id, integer version", "[binary  N     ] dev-libs/bar-1::gentoo", []string{"dev-libs/bar"}},
		{"no build id, integer version, no repository", "[binary  N     ] acct-user/portage-0", []string{"acct-user/portage"}},
	} {
		if got := pending.ParseWorld([]byte(tc.line + "\n")); !slices.Equal(got, tc.want) {
			t.Errorf("%s: ParseWorld(%q) = %q, want %q", tc.name, tc.line, got, tc.want)
		}
	}
}

// An [ebuild line never carries a build id: the same trailing "-<digits>" is
// not stripped there, so an atom that is only valid with it stripped is
// skipped, never read as another name.
func TestS099ParseWorldEbuildLinesKeepTheirTrailingDigits(t *testing.T) {
	for _, line := range []string{
		"[ebuild     U  ] sys-libs/zlib-1.3.1-r1-1::gentoo",
		"[ebuild  N     ] dev-libs/bar-1.0-1",
		"[ebuild  N     ] app-misc/foo-1-2::gentoo",
	} {
		if got := pending.ParseWorld([]byte(line + "\n")); len(got) != 0 {
			t.Errorf("ParseWorld(%q) = %q, want nothing", line, got)
		}
	}
	got := pending.ParseWorld([]byte("[ebuild  N     ] dev-libs/bar-1::gentoo\n[ebuild     U  ] media-fonts/font-adobe-100dpi-1.0.4::gentoo\n"))
	if want := []string{"dev-libs/bar", "media-fonts/font-adobe-100dpi"}; !slices.Equal(got, want) {
		t.Errorf("ParseWorld = %q, want %q", got, want)
	}
}
