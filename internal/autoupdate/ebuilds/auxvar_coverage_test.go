package ebuilds

import (
	"testing"
)

// TestExtractPackageAtom covers the atom-parsing helper across its branches:
// leading operators, a version boundary, slot, USE flags, the no-slash case, and
// a bare atom.
func TestExtractPackageAtom(t *testing.T) {
	cases := map[string]string{
		">=dev-util/foo-1.2.3":   "dev-util/foo",
		"=app-editors/vim-9.0":   "app-editors/vim",
		"~net-misc/curl-8.1":     "net-misc/curl",
		"dev-libs/openssl:0/3":   "dev-libs/openssl",
		"x11-libs/gtk+[wayland]": "x11-libs/gtk+",
		"sys-apps/portage":       "sys-apps/portage",
		"noslash":                "",
		"":                       "",
	}
	for atom, want := range cases {
		if got := extractPackageAtom(atom); got != want {
			t.Errorf("extractPackageAtom(%q) = %q, want %q", atom, got, want)
		}
	}
}
