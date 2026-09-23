package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLiteralFixture writes body to a fresh ebuild and returns its path.
func writeLiteralFixture(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// readLiteralFixture returns the ebuild's bytes as a string.
func readLiteralFixture(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(got)
}

// TestSubstituteAuxVar_InsertsLiterally pins S050-R2.1: the value substituteAuxVar
// writes reaches the ebuild byte for byte. Every case is the hostile half of the
// rule -- a value shaped like a regexp replacement reference ($1, ${1}, ${2}, $$,
// $name, \1) that a template-expanding writer would collapse into the capture
// group or drop. The benign half (a plain value such as esr-bb24) is pinned by
// TestSubstituteAuxVar and must stay green.
func TestSubstituteAuxVar_InsertsLiterally(t *testing.T) {
	const old = "esr-bb23"
	body := "EAPI=8\nMY_BUILD=\"" + old + "\"\n" +
		"SRC_URI=\"https://www.betterbird.eu/downloads/${PV}${MY_BUILD}/betterbird.tar.bz2\"\n"

	cases := []struct {
		name  string
		value string
	}{
		{"braced group one", "a${1}b"},
		{"bare group one", "a$1b"},
		{"braced group two", "${2}"},
		{"double dollar", "x$$y"},
		{"named reference", "$MY_BUILD"},
		{"backslash reference", `a\1b`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeLiteralFixture(t, "betterbird-bin-128.7.0.ebuild", body)
			if err := substituteAuxVar(path, "MY_BUILD", tc.value); err != nil {
				t.Fatalf("substituteAuxVar(%q): %v", tc.value, err)
			}
			want := strings.Replace(body, `MY_BUILD="`+old+`"`, `MY_BUILD="`+tc.value+`"`, 1)
			if got := readLiteralFixture(t, path); got != want {
				t.Errorf("value %q was not inserted literally\n got: %q\nwant: %q", tc.value, got, want)
			}
		})
	}
}

// TestSubstituteCommitHash_InsertsLiterally pins S050-R2.2: substituteCommitHash
// inserts the hash without template expansion in both the quoted and the bare
// COMMIT= forms. The value is deliberately not a real SHA: the substitute
// functions do not validate (the gate lives in Applier.Apply), so literal
// insertion is testable on its own.
func TestSubstituteCommitHash_InsertsLiterally(t *testing.T) {
	const old = "503d0d3d3858f463973f2cfce4a3aa0173567500"
	forms := []struct {
		name   string
		before string
		after  func(v string) string
	}{
		{
			name:   "quoted EGIT_COMMIT",
			before: `EGIT_COMMIT="` + old + `"`,
			after:  func(v string) string { return `EGIT_COMMIT="` + v + `"` },
		},
		{
			name:   "bare COMMIT",
			before: `COMMIT=` + old,
			after:  func(v string) string { return `COMMIT=` + v },
		},
	}
	values := []string{"ab${1}cd", "ab$1cd", "${2}", "x$$y"}

	for _, form := range forms {
		for _, value := range values {
			t.Run(form.name+"/"+value, func(t *testing.T) {
				body := "EAPI=8\n" + form.before + "\nSRC_URI=\"https://example.com/${COMMIT}.tar.gz\"\n"
				path := writeLiteralFixture(t, "demo-1.0.ebuild", body)
				if err := substituteCommitHash(path, value); err != nil {
					t.Fatalf("substituteCommitHash(%q): %v", value, err)
				}
				want := strings.Replace(body, form.before, form.after(value), 1)
				if got := readLiteralFixture(t, path); got != want {
					t.Errorf("hash %q was not inserted literally\n got: %q\nwant: %q", value, got, want)
				}
			})
		}
	}
}
