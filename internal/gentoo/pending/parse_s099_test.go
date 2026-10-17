package pending_test

// Story 099, sub-task 3.2: ParseWorld reads `emerge --pretend` output into
// category/name entries (R2.2); ParseGLSA reads `glsa-check --list affected`
// standard output into the applicable GLSAs (R3.2, R3.6).
//
// Hostile halves first. ParseWorld: a name ending in a digit segment
// (font-adobe-100dpi) must not be cut at its first "-<digit>"; two names
// sharing a prefix, or one name in two categories, must stay apart; one name
// listed in two slots, or as an ebuild and a binary, must come together.
// ParseGLSA: a title carrying parentheses must not lend its "(" to the atoms;
// glsa-check's "..." (more than three packages) is not an atom; two GLSAs
// with one title stay two.
//
// glsa-check writes its [A]/[U]/[N] legend to standard error, so standard
// output carries GLSA lines only.

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
)

func s099WorldNames(t *testing.T, out string, want []string) {
	t.Helper()
	got := pending.ParseWorld([]byte(out))
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !slices.Equal(got, want) {
		t.Errorf("ParseWorld(%q)\n got %q\nwant %q", out, got, want)
	}
}

// TestS099ParseWorldKeepsNamesEndingInADigitSegment is the hostile half of the
// version strip: the version is the PMS grammar anchored at the end, never
// the first "-<digit>".
func TestS099ParseWorldKeepsNamesEndingInADigitSegment(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"[ebuild     U  ] media-fonts/font-adobe-100dpi-1.0.3-r2::gentoo [1.0.3-r1::gentoo]", "media-fonts/font-adobe-100dpi"},
		{"[ebuild     U  ] media-fonts/font-adobe-75dpi-1.0.3-r2", "media-fonts/font-adobe-75dpi"},
		{"[ebuild     U  ] media-fonts/font-misc-misc-1.1.3::gentoo", "media-fonts/font-misc-misc"},
		{"[ebuild     U  ] net-libs/libssh2-1.11.1-r1::gentoo", "net-libs/libssh2"},
		{"[ebuild     U  ] media-libs/x264-0.0.20250910::gentoo", "media-libs/x264"},
		{"[ebuild     U  ] dev-lang/rust-bin-1.91.0:1.91.0::gentoo [1.90.0:1.90.0::gentoo]", "dev-lang/rust-bin"},
		{"[ebuild     U  ] sys-kernel/gentoo-kernel-bin-6.17.3_p1:6.17.3::gentoo", "sys-kernel/gentoo-kernel-bin"},
		{"[ebuild     U  ] dev-perl/Locale-gettext-1.70.0-r1::gentoo", "dev-perl/Locale-gettext"},
	} {
		s099WorldNames(t, tc.line+"\n", []string{tc.want})
	}
}

// TestS099ParseWorldStripsEveryVersionForm covers the PMS version suffixes,
// slots, sub-slots and repositories.
func TestS099ParseWorldStripsEveryVersionForm(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"[ebuild     U  ] dev-qt/qtbase-6.10.1:6::gentoo [6.10.0:6::gentoo]", "dev-qt/qtbase"},
		{"[ebuild     U  ] app-misc/foo-1.0a::gentoo", "app-misc/foo"},
		{"[ebuild     U  ] dev-util/bar-20261016_p20261016", "dev-util/bar"},
		{"[ebuild     U  ] dev-lang/baz-2.0_rc2-r1:2", "dev-lang/baz"},
		{"[ebuild     U  ] sys-libs/qux-1.2.3_pre1_p2-r10::gentoo", "sys-libs/qux"},
		{"[ebuild   R   *] app-misc/live-9999::bentoo", "app-misc/live"},
		{"[ebuild     U  ] x11-libs/gtk+-3.24.43:3/3::gentoo [3.24.42:3/3::gentoo]", "x11-libs/gtk+"},
		{"[ebuild  N     ] acct-user/portage-0-r3::gentoo", "acct-user/portage"},
		{"[ebuild     U ~] dev-lang/go-1.27.2:0/1.27.2::gentoo [1.27.1:0/1.27.1::gentoo]", "dev-lang/go"},
		{"[ebuild     U  ] app-misc/foo2-1.0", "app-misc/foo2"},
		{"[ebuild  NS    ] dev-lang/python-3.14.0:3.14::gentoo", "dev-lang/python"},
	} {
		s099WorldNames(t, tc.line+"\n", []string{tc.want})
	}
}

// TestS099ParseWorldReadsVerboseLines: the verbose form carries USE flags and
// sizes after the atom; only the atom counts.
func TestS099ParseWorldReadsVerboseLines(t *testing.T) {
	out := strings.Join([]string{
		"These are the packages that would be merged, in order:",
		"",
		"Calculating dependencies  ... done!",
		`[ebuild     U  ] dev-qt/qtbase-6.10.1:6/6.10.1::gentoo [6.10.0:6/6.10.0::gentoo] USE="X -test%" 0 KiB`,
		`[ebuild  N     ] app-misc/foo-1.0::bentoo  USE="-doc" 12 KiB`,
		"",
		"Total: 2 packages (1 upgrade, 1 new), Size of downloads: 12 KiB",
	}, "\n") + "\n"
	s099WorldNames(t, out, []string{"dev-qt/qtbase", "app-misc/foo"})
}

// TestS099ParseWorldCountsEbuildAndBinaryOnly: [uninstall, [blocks and
// [nomerge lines are not merges.
func TestS099ParseWorldCountsEbuildAndBinaryOnly(t *testing.T) {
	out := strings.Join([]string{
		"[ebuild     U  ] dev-qt/qtbase-6.10.1:6::gentoo [6.10.0:6::gentoo]",
		"[uninstall     ] sys-apps/old-1.0::gentoo",
		`[blocks B      ] <sys-apps/foo-2 ("<sys-apps/foo-2" is soft blocking sys-apps/bar-1.0)`,
		"[nomerge       ] x11-libs/gtk+-3.24.43:3::gentoo",
		"[binary     U  ] app-editors/vim-9.1.0-r1:0/0::gentoo [9.1.0::gentoo]",
	}, "\n") + "\n"
	s099WorldNames(t, out, []string{"dev-qt/qtbase", "app-editors/vim"})
}

// TestS099ParseWorldKeepsDistinctNamesApart is the wrongly-collapse half of
// the de-duplication: a shared prefix or a shared name in another category is
// another package.
func TestS099ParseWorldKeepsDistinctNamesApart(t *testing.T) {
	out := strings.Join([]string{
		"[ebuild     U  ] dev-python/pyyaml-6.0.3::gentoo",
		"[ebuild  N     ] dev-python/pyyaml-env-tag-1.1::gentoo",
		"[ebuild     U  ] sys-libs/glibc-2.42-r1:2.2::gentoo",
		"[ebuild  N     ] cross-x86_64-pc-linux-gnu/glibc-2.42::crossdev",
	}, "\n") + "\n"
	s099WorldNames(t, out, []string{
		"dev-python/pyyaml", "dev-python/pyyaml-env-tag",
		"sys-libs/glibc", "cross-x86_64-pc-linux-gnu/glibc",
	})
}

// TestS099ParseWorldDeduplicatesInOrder is the wrongly-split half: one name in
// two slots, or as an ebuild and a binary, is one entry at its first place.
func TestS099ParseWorldDeduplicatesInOrder(t *testing.T) {
	out := strings.Join([]string{
		"[ebuild     U  ] dev-lang/python-3.13.9:3.13::gentoo [3.13.8:3.13::gentoo]",
		"[ebuild     U  ] dev-libs/openssl-3.5.4:0/3::gentoo [3.5.3:0/3::gentoo]",
		"[ebuild  NS    ] dev-lang/python-3.14.0:3.14::gentoo",
		"[binary     U  ] dev-libs/openssl-3.5.4:0/3::gentoo [3.5.3:0/3::gentoo]",
		"[ebuild     U  ] app-misc/zzz-2.0::gentoo",
	}, "\n") + "\n"
	s099WorldNames(t, out, []string{"dev-lang/python", "dev-libs/openssl", "app-misc/zzz"})
}

// TestS099ParseWorldIgnoresGarbage: text that is not a valid merge line yields
// nothing, and never a name outside category/name.
func TestS099ParseWorldIgnoresGarbage(t *testing.T) {
	for _, out := range []string{
		"",
		"\n\n",
		"!!! The following installed packages are masked:\n",
		"[ebuild     U  ]\n",
		"[ebuild     U  ] /etc/passwd\n",
		"[ebuild     U  ] ../../x-1.0\n",
		"[ebuild     U  ] noslash-1.0\n",
		"[ebuild     U  ] a/b/c-1.0\n",
		"\x00\xff\xfe[ebuild\n",
		"dev-qt/qtbase-6.10.1:6::gentoo\n",
	} {
		s099WorldNames(t, out, nil)
	}
}

// s099GLSAOut is standard output of `glsa-check --list affected --nocolor`:
// " (" then " <pkg> " per package (at most three, then "... ") then ")".
const s099GLSAOut = "202508-06 [N] Composer: Multiple Vulnerabilities ( dev-php/composer )\n" +
	"202401-12 [N] OpenSSH (sshd): Remote Code Execution (CVE) ( net-misc/openssh )\n" +
	"202507-01 [N] GStreamer, GStreamer Plugins: Multiple Vulnerabilities ( media-libs/gstreamer  media-libs/gst-plugins-base  media-libs/gst-plugins-good ... )\n" +
	"202501-03 [A] Foo: Applied Advisory ( app-misc/foo )\n" +
	"202502-04 [U] Bar: Unaffected Advisory ( app-misc/bar )\n" +
	"202508-07 [N] Composer: Multiple Vulnerabilities ( dev-php/composer )\n"

func s099GLSAs(t *testing.T, out string) []pending.GLSA {
	t.Helper()
	got, ok := pending.ParseGLSA([]byte(out))
	if !ok {
		t.Fatalf("ParseGLSA(%q) ok = false, want true", out)
	}
	return got
}

func s099SameGLSA(t *testing.T, got, want []pending.GLSA) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ParseGLSA returned %d GLSAs %+v, want %d %+v", len(got), got, len(want), want)
	}
	for n := range want {
		if got[n].ID != want[n].ID || got[n].Title != want[n].Title || !slices.Equal(got[n].Atoms, want[n].Atoms) {
			t.Errorf("GLSA %d = %+v, want %+v", n, got[n], want[n])
		}
	}
}

// TestS099ParseGLSATitleParenthesesStayInTheTitle is the hostile half of the
// atom split: the atoms open at the LAST " ( ", so a title's own parentheses
// stay in the title.
func TestS099ParseGLSATitleParenthesesStayInTheTitle(t *testing.T) {
	got := s099GLSAs(t, "202401-12 [N] OpenSSH (sshd): Remote Code Execution (CVE) ( net-misc/openssh )\n")
	s099SameGLSA(t, got, []pending.GLSA{
		{ID: "202401-12", Title: "OpenSSH (sshd): Remote Code Execution (CVE)", Atoms: []string{"net-misc/openssh"}},
	})
}

// TestS099ParseGLSAReadsAffectedLinesOnly is R3.2: every [N] line, in order,
// with id, title and atoms; [A] and [U] are not applicable; "..." is not an
// atom; two GLSAs sharing a title stay two.
func TestS099ParseGLSAReadsAffectedLinesOnly(t *testing.T) {
	s099SameGLSA(t, s099GLSAs(t, s099GLSAOut), []pending.GLSA{
		{ID: "202508-06", Title: "Composer: Multiple Vulnerabilities", Atoms: []string{"dev-php/composer"}},
		{ID: "202401-12", Title: "OpenSSH (sshd): Remote Code Execution (CVE)", Atoms: []string{"net-misc/openssh"}},
		{ID: "202507-01", Title: "GStreamer, GStreamer Plugins: Multiple Vulnerabilities",
			Atoms: []string{"media-libs/gstreamer", "media-libs/gst-plugins-base", "media-libs/gst-plugins-good"}},
		{ID: "202508-07", Title: "Composer: Multiple Vulnerabilities", Atoms: []string{"dev-php/composer"}},
	})
}

// TestS099ParseGLSAIgnoresTheLegend: should the legend reach standard output
// it is not a GLSA.
func TestS099ParseGLSAIgnoresTheLegend(t *testing.T) {
	out := "[A] means this GLSA was marked as applied (injected),\n" +
		"[U] means the system is not affected and\n" +
		"[N] indicates that the system might be affected.\n\n" +
		"202508-06 [N] Composer: Multiple Vulnerabilities ( dev-php/composer )\n"
	s099SameGLSA(t, s099GLSAs(t, out), []pending.GLSA{
		{ID: "202508-06", Title: "Composer: Multiple Vulnerabilities", Atoms: []string{"dev-php/composer"}},
	})
}

// TestS099ParseGLSANotAffectedLinesAreRecognisable: a listing of [A] and [U]
// lines only is a valid answer with no applicable GLSA, not a failure.
func TestS099ParseGLSANotAffectedLinesAreRecognisable(t *testing.T) {
	got, ok := pending.ParseGLSA([]byte("202501-03 [A] Foo: Applied Advisory ( app-misc/foo )\n202502-04 [U] Bar: Unaffected Advisory ( app-misc/bar )\n"))
	if !ok {
		t.Error("[A]/[U]-only listing: ok = false, want true (recognisable lines, none applicable)")
	}
	if len(got) != 0 {
		t.Errorf("[A]/[U]-only listing: %+v, want no applicable GLSA", got)
	}
}

// TestS099ParseGLSAEmptyIsOKWithNone: glsa-check prints its legend on stderr,
// so a clean system leaves stdout empty (or blank). That must read as "no
// applicable GLSA", or the last GLSA condition could never clear.
func TestS099ParseGLSAEmptyIsOKWithNone(t *testing.T) {
	for _, out := range []string{"", "\n", "  \n\t\n"} {
		got, ok := pending.ParseGLSA([]byte(out))
		if !ok || len(got) != 0 {
			t.Errorf("ParseGLSA(%q) = %+v, ok=%v; want none, ok=true", out, got, ok)
		}
	}
}

// TestS099ParseGLSAGarbageIsNotOK is R3.6's input: output with no
// recognisable line reports ok=false and no GLSA, so the caller keeps its
// conditions. Empty output is the other test's: it means no GLSA.
func TestS099ParseGLSAGarbageIsNotOK(t *testing.T) {
	for _, out := range []string{
		"glsa-check: error: unrecognized arguments\n",
		"\x00\xff\xfe\n",
		"[A] means this GLSA was marked as applied (injected),\n[U] means the system is not affected and\n[N] indicates that the system might be affected.\n",
		"202508-06 Composer: Multiple Vulnerabilities ( dev-php/composer )\n",
		"202508-06 [N] Composer: Multiple Vulnerabilities\n",
		"202508-06 [N] Composer: Multiple Vulnerabilities ( dev-php/composer\n",
		"2025-06 [N] Bad id ( app-misc/x )\n",
		"../../x [N] Bad id ( app-misc/x )\n",
	} {
		got, ok := pending.ParseGLSA([]byte(out))
		if ok || len(got) != 0 {
			t.Errorf("ParseGLSA(%q) = %+v, ok=%v; want none, ok=false", out, got, ok)
		}
	}
}

var (
	s099NameRE = regexp.MustCompile(`^[A-Za-z0-9+_][A-Za-z0-9+_.-]*/[A-Za-z0-9+_][A-Za-z0-9+_-]*$`)
	s099IDRE   = regexp.MustCompile(`^\d{6}-\d{2}$`)
)

// FuzzParseWorld: never panics; every entry is category/name, unique, and
// parses back to itself as the atom of a fresh merge line.
func FuzzParseWorld(f *testing.F) {
	for _, s := range []string{
		"[ebuild     U  ] dev-qt/qtbase-6.10.1:6::gentoo [6.10.0:6::gentoo]\n",
		"[ebuild     U  ] media-fonts/font-adobe-100dpi-1.0.3-r2::gentoo\n",
		"[binary     U  ] app-editors/vim-9.1.0-r1:0/0::gentoo\n[uninstall     ] a/b-1\n",
		"[ebuild     U  ] dev-util/bar-20261016_p20261016\n[ebuild  N ] x/y-1.0a_rc2-r1\n",
		"\x00\xff[ebuild ] /x\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, out []byte) {
		got := pending.ParseWorld(out)
		seen := map[string]bool{}
		for _, n := range got {
			if !s099NameRE.MatchString(n) {
				t.Fatalf("ParseWorld(%q) returned %q, not category/name", out, n)
			}
			if seen[n] {
				t.Fatalf("ParseWorld(%q) returned %q twice", out, n)
			}
			seen[n] = true
			again := pending.ParseWorld([]byte("[ebuild  N     ] " + n + "-1.0::gentoo\n"))
			if len(again) != 1 || again[0] != n {
				t.Fatalf("name %q from %q does not parse back from %q-1.0: got %q", n, out, n, again)
			}
		}
	})
}

// FuzzParseGLSA: never panics; not ok means no GLSA; every GLSA has a GLSA id,
// a one-line title and atoms with no blank, none of them "...".
func FuzzParseGLSA(f *testing.F) {
	f.Add([]byte(s099GLSAOut))
	f.Add([]byte("202401-12 [N] OpenSSH (sshd): x ( net-misc/openssh )\n"))
	f.Add([]byte("[A] means this GLSA\n"))
	f.Add([]byte("202508-06 [N] ( ( ( ) )\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, out []byte) {
		got, ok := pending.ParseGLSA(out)
		if !ok && len(got) != 0 {
			t.Fatalf("ParseGLSA(%q) ok=false with %d GLSAs", out, len(got))
		}
		for _, g := range got {
			if !s099IDRE.MatchString(g.ID) {
				t.Fatalf("ParseGLSA(%q): id %q is not a GLSA id", out, g.ID)
			}
			if strings.Contains(g.Title, "\n") {
				t.Fatalf("ParseGLSA(%q): title %q spans lines", out, g.Title)
			}
			for _, a := range g.Atoms {
				if a == "" || a == "..." || strings.ContainsAny(a, " \t\n\r") {
					t.Fatalf("ParseGLSA(%q): atom %q in %+v", out, a, g)
				}
			}
		}
	})
}
