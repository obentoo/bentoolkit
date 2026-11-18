package po

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const header = `package x

import tr "github.com/obentoo/bentoolkit/internal/common/i18n"
`

func TestExtract(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cmd", "a.go"), header+`
var _ = tr.N("Usage:")
var _ = tr.T("help for %s")
var _ = tr.N("multi " + "part\n" + `+"`raw\"line`"+`)
func f(s string) string { return tr.T(s) } // a variable: skipped, not an error
`)
	writeFile(t, filepath.Join(root, "cmd", "b.go"), header+`var _ = tr.N("Usage:")`)
	writeFile(t, filepath.Join(root, "cmd", "b_test.go"), header+`var _ = tr.N("only in a test")`)
	writeFile(t, filepath.Join(root, "cmd", "vendor", "v.go"), header+`var _ = tr.N("vendored")`)
	writeFile(t, filepath.Join(root, "cmd", "plain.go"), "package x\nfunc N(s string) string { return s }\nvar _ = N(\"not i18n\")\n")

	msgs, err := Extract(root, "cmd")
	if err != nil {
		t.Fatal(err)
	}
	want := []Message{
		{ID: "Usage:", Refs: []string{"cmd/a.go", "cmd/b.go"}},
		{ID: "help for %s", Refs: []string{"cmd/a.go"}},
		{ID: "multi part\nraw\"line", Refs: []string{"cmd/a.go"}},
	}
	if len(msgs) != len(want) {
		t.Fatalf("got %d messages %+v, want %+v", len(msgs), msgs, want)
	}
	for i := range want {
		if msgs[i].ID != want[i].ID || !slices.Equal(msgs[i].Refs, want[i].Refs) {
			t.Errorf("message %d = %+v, want %+v", i, msgs[i], want[i])
		}
	}
}

func TestExtractRejectsComputedN(t *testing.T) {
	for name, call := range map[string]string{
		"variable": `tr.N(s)`,
		"empty":    `tr.N("")`,
		"two args": `tr.N("a", "b")`,
		"sprintf":  `tr.N("a" + s)`,
		"carriage": `tr.T("\rprogress")`,
	} {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "cmd", "a.go"), header+"var s string\nvar _ = "+call+"\n")
		if _, err := Extract(root, "cmd"); err == nil {
			t.Errorf("%s: Extract accepted %s", name, call)
		}
	}
}

func TestWritePOTRoundTrip(t *testing.T) {
	msgs := []Message{
		{ID: "Usage:", Refs: []string{"cmd/a.go"}},
		{ID: "help for %s", Refs: []string{"cmd/a.go", "cmd/b.go"}},
		{ID: "line one\nline \"two\"\twith tab\n", Refs: []string{"cmd/c.go"}},
		{ID: "no trailing\nnewline", Refs: []string{"cmd/c.go"}},
		{ID: `back\slash`, Refs: []string{"cmd/c.go"}},
	}
	var buf bytes.Buffer
	if err := WritePOT(&buf, msgs); err != nil {
		t.Fatal(err)
	}
	// No c-format flag: msgfmt checks C's syntax and would reject Go's
	// explicit argument indexes (%[2]s); TestCatalogsInSync checks verbs.
	if strings.Contains(buf.String(), "c-format") {
		t.Errorf("the template flags c-format:\n%s", buf.String())
	}
	entries, err := Parse(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(msgs) {
		t.Fatalf("parsed %d entries, want %d", len(entries), len(msgs))
	}
	for i, e := range entries {
		if e.ID != msgs[i].ID || e.Str != "" {
			t.Errorf("entry %d = %+v, want ID %q", i, e, msgs[i].ID)
		}
	}
}

func TestParseFuzzyAndObsolete(t *testing.T) {
	in := `msgid ""
msgstr "Language: pt_BR\n"

#: a.go
#, fuzzy, c-format
msgid "Old %s"
msgstr "Velho %s"

#: a.go
msgid "Kept"
msgstr ""
"Mantido"

#~ msgid "Gone"
#~ msgstr "Sumiu"
`
	entries, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{ID: "Old %s", Str: "Velho %s", Fuzzy: true}, {ID: "Kept", Str: "Mantido"}}
	if !slices.Equal(entries, want) {
		t.Errorf("Parse = %+v, want %+v", entries, want)
	}
}

func TestParseRejectsUnsupportedSyntax(t *testing.T) {
	for _, in := range []string{
		"msgctxt \"menu\"\nmsgid \"Open\"\nmsgstr \"\"\n",
		"msgid \"one\"\nmsgid_plural \"many\"\nmsgstr[0] \"\"\n",
		"\"orphan string\"\n",
		"msgid \"unterminated\n",
		"msgstr \"no msgid\"\n",
		"garbage\n",
	} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("Parse accepted %q", in)
		}
	}
}

func TestArgVerbs(t *testing.T) {
	same := [][2]string{
		{"%s from %d", "%[2]d em %[1]s"},
		{"%s %s %q %s", "%[3]q (%[4]s) %[1]s %[2]s"},
		{"%d of %d", "%[2]d ... %[1]d"},
		{"%s then %d", "%[1]s then %d"}, // %[1] resets; the next implicit verb is 2
		{"100%% of %s", "%s: 100%%"},
	}
	for _, c := range same {
		if a, b := ArgVerbs(c[0]), ArgVerbs(c[1]); !slices.Equal(a, b) {
			t.Errorf("%q vs %q: %q != %q", c[0], c[1], a, b)
		}
	}
	differ := [][2]string{
		{"%s from %d", "%d from %s"}, // reordered without indexes
		{"%d of %d", "%d of %d of %d"},
		{"%s", "%q"},
		{"%s and %d", "%[1]s and %[1]d"},
	}
	for _, c := range differ {
		if slices.Equal(ArgVerbs(c[0]), ArgVerbs(c[1])) {
			t.Errorf("%q and %q should differ", c[0], c[1])
		}
	}
}

func TestVerbs(t *testing.T) {
	cases := map[string][]string{
		"help for %s":          {"%s"},
		"100%% done":           nil,
		"%d of %-5s at %.2f%%": {"%d", "%-5s", "%.2f"},
		"%[2]s then %[1]s":     {"%[2]s", "%[1]s"},
		"no verbs here":        nil,
		"%v and %q":            {"%v", "%q"},
	}
	for in, want := range cases {
		if got := Verbs(in); !slices.Equal(got, want) {
			t.Errorf("Verbs(%q) = %q, want %q", in, got, want)
		}
	}
}

// repoRoot is the module root, four levels above this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	return root
}

func parseFile(t *testing.T, path string) []Entry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, err := Parse(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return entries
}

func ids(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	slices.Sort(out)
	return out
}

// templateActionRE matches a text/template action such as {{.CommandPath}}.
var templateActionRE = regexp.MustCompile(`\{\{[^}]*\}\}`)

// TestCatalogsInSync keeps po/ honest: the template holds exactly the
// messages the source marks, every language holds exactly the template's
// messages, and no translation drops or reorders a fmt verb or a template
// action — either would print garbage at run time. A failure here is fixed
// with "make i18n-update"; new messages may stay untranslated (they print
// in English) until a translator fills them in.
func TestCatalogsInSync(t *testing.T) {
	root := repoRoot(t)
	msgs, err := Extract(root, "cmd", "internal")
	if err != nil {
		t.Fatal(err)
	}
	source := make([]string, 0, len(msgs))
	for _, m := range msgs {
		source = append(source, m.ID)
	}
	slices.Sort(source)

	pot := ids(parseFile(t, filepath.Join(root, "po", "bentoolkit.pot")))
	if !slices.Equal(pot, source) {
		t.Fatalf("po/bentoolkit.pot is stale: run make i18n-update (source has %d messages, template %d)", len(source), len(pot))
	}

	linguas, err := os.ReadFile(filepath.Join(root, "po", "LINGUAS"))
	if err != nil {
		t.Fatal(err)
	}
	langs := strings.Fields(string(linguas))
	files, err := filepath.Glob(filepath.Join(root, "po", "*.po"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(langs) {
		t.Errorf("po/LINGUAS lists %d languages but po/ holds %d .po files", len(langs), len(files))
	}
	for _, lang := range langs {
		t.Run(lang, func(t *testing.T) {
			entries := parseFile(t, filepath.Join(root, "po", lang+".po"))
			if got := ids(entries); !slices.Equal(got, source) {
				t.Fatalf("po/%s.po is out of sync with the template: run make i18n-update", lang)
			}
			for _, e := range entries {
				if e.Str == "" || e.Fuzzy {
					continue
				}
				if a, b := ArgVerbs(e.ID), ArgVerbs(e.Str); !slices.Equal(a, b) {
					t.Errorf("%q: verbs %q in the msgid, %q in the translation", e.ID, a, b)
				}
				if a, b := templateActionRE.FindAllString(e.ID, -1), templateActionRE.FindAllString(e.Str, -1); !slices.Equal(a, b) {
					t.Errorf("%q: template actions %q in the msgid, %q in the translation", e.ID, a, b)
				}
				if strings.HasSuffix(e.ID, "\n") != strings.HasSuffix(e.Str, "\n") {
					t.Errorf("%q: the translation's trailing newline differs from the msgid's", e.ID)
				}
			}
		})
	}
}

// TestCatalogsCompile runs msgfmt --check over every .po when msgfmt is
// installed: the same check "make locale" applies before a release.
func TestCatalogsCompile(t *testing.T) {
	msgfmt, err := exec.LookPath("msgfmt")
	if err != nil {
		t.Skip("msgfmt not installed")
	}
	root := repoRoot(t)
	files, err := filepath.Glob(filepath.Join(root, "po", "*.po"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		out, err := exec.CommandContext(t.Context(), msgfmt, "--check", "-o", os.DevNull, f).CombinedOutput()
		if err != nil {
			t.Errorf("msgfmt --check %s: %v\n%s", filepath.Base(f), err, out)
		}
	}
}
