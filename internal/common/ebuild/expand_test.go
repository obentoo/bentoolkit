package ebuild

import "testing"

func TestExpandVersionRefs(t *testing.T) {
	cases := []struct {
		name, value, body, pvr string
		want                   string
		ok                     bool
	}{
		{"literal", "0", "", "1.0", "0", true},
		{"PV drops the revision", "${PV}", "", "1.2-r3", "1.2", true},
		{"PVR keeps it", "${PVR}", "", "1.2-r3", "1.2-r3", true},
		{"ver_cut", "3/$(ver_cut 1-2)", "", "5.0.1", "3/5.0", true},
		{"python indirection", "${PYVER}", "EAPI=8\nPYVER=$(ver_cut 1-2)\nSLOT=\"${PYVER}\"\n", "3.15.2_p1-r1", "3.15", true},
		{"bare $NAME", "$MY_SLOT/0", "MY_SLOT='2'\n", "2.4", "2/0", true},
		{"indented assignment is not global", "${V}", "src_prepare() {\n\tV=1\n}\n", "1.0", "", false},
		{"two levels stay unresolved", "${A}", "B=1\nA=${B}\n", "1.0", "", false},
		{"eclass variable", "${LLVM_MAJOR}", "inherit llvm\n", "23.1", "", false},
		{"other command substitution", "$(get_version_component_range 1)", "", "1.0", "", false},
		{"empty", "", "", "1.0", "", false},
		// A ver_cut range the version lacks is unknown, not "": pasting the
		// empty cut in would report the wrong slot "3/".
		{"impossible ver_cut", "3/$(ver_cut 9)", "", "5.0.1", "", false},
		{"impossible ver_cut through a variable", "${V}", "V=$(ver_cut 4)\n", "1.2", "", false},
		// bash leaves the later global assignment in effect.
		{"last global assignment wins", "${V}", "V=1\nV=2\n", "1.0", "2", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ExpandVersionRefs(c.value, c.body, c.pvr)
			if got != c.want || ok != c.ok {
				t.Errorf("ExpandVersionRefs(%q) = (%q, %v), want (%q, %v)", c.value, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestVerCut(t *testing.T) {
	cases := map[string]string{"1": "5", "1-2": "5.0", "2-": "0.1_p2", "1-9": "5.0.1_p2", "9": "", "0": "", "3-2": ""}
	for spec, want := range cases {
		if got := VerCut("5.0.1_p2", spec); got != want {
			t.Errorf("VerCut(5.0.1_p2, %q) = %q, want %q", spec, got, want)
		}
	}
}
