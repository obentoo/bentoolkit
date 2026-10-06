package main

// The documentation set is README.md plus every Markdown page under docs/.
// Tests that pin a documented phrase read the whole set, so a section that
// moves from the README into a docs/ page keeps every phrase its tests pin.
//
// The helpers under test live next to readRepoDoc:
//
//	readDocSetAt(t *testing.T, root string) string
//	readDocSet(t *testing.T) string // readDocSetAt(t, filepath.Join("..", ".."))
//
// readDocSetAt returns root/README.md followed by every root/docs/**/*.md in
// lexical order, each file starting on a line of its own. It fails the test,
// naming the path, when a file cannot be read; a missing docs/ directory is
// not an error.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDocTree creates files (slash paths relative to the returned root).
func writeDocTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A README that does not end in a newline must not glue its last line to
// the first line of the next page: a phrase spanning two files is in neither,
// and a page's H1 must still open a line so section extraction stops there.
func TestDocsSet_KeepsEachFileOnItsOwnLines(t *testing.T) {
	root := writeDocTree(t, map[string]string{
		"README.md":    "# Tool\n\nlast line of the readme",
		"docs/a.md":    "# Page A\n\nend of page a",
		"docs/b.md":    "# Page B\n\nbody of b\n",
		"docs/ignored": "",
	})
	got := readDocSetAt(t, root)

	for _, glued := range []string{
		"last line of the readme# Page A",
		"end of page a# Page B",
	} {
		if strings.Contains(got, glued) {
			t.Errorf("the doc set joins two files on one line: found %q", glued)
		}
	}
	for _, opener := range []string{"\n# Page A\n", "\n# Page B\n"} {
		if !strings.Contains(got, opener) {
			t.Errorf("a page's H1 does not open a line of its own: %q missing", opener)
		}
	}
}

// Only README.md and Markdown pages under docs/ belong to the set: other
// Markdown in the repository and non-Markdown files under docs/ do not.
func TestDocsSet_LeavesOutFilesOutsideTheSet(t *testing.T) {
	root := writeDocTree(t, map[string]string{
		"README.md":              "# Tool\nREADME-PHRASE\n",
		"docs/a.md":              "# A\nPAGE-PHRASE\n",
		"docs/notes.txt":         "TXT-ONLY-PHRASE\n",
		"docs/a.md.orig":         "ORIG-ONLY-PHRASE\n",
		"CHANGELOG.md":           "CHANGELOG-ONLY-PHRASE\n",
		"misc/design/README.md":  "MISC-ONLY-PHRASE\n",
		"documentation/other.md": "SIBLING-DIR-PHRASE\n",
	})
	got := readDocSetAt(t, root)

	for _, outside := range []string{
		"TXT-ONLY-PHRASE", "ORIG-ONLY-PHRASE", "CHANGELOG-ONLY-PHRASE",
		"MISC-ONLY-PHRASE", "SIBLING-DIR-PHRASE",
	} {
		if strings.Contains(got, outside) {
			t.Errorf("the doc set holds %q, from a file outside README.md and docs/**/*.md", outside)
		}
	}
	for _, inside := range []string{"README-PHRASE", "PAGE-PHRASE"} {
		if !strings.Contains(got, inside) {
			t.Errorf("the doc set lost %q", inside)
		}
	}
}

// A page nested below docs/ is part of the set: a phrase moved there is
// still found.
func TestDocsSet_IncludesNestedPages(t *testing.T) {
	root := writeDocTree(t, map[string]string{
		"README.md":                      "# Tool\n",
		"docs/changelog/0.1.0-0.29.1.md": "# Archive\nNESTED-PHRASE\n",
		"docs/deep/er/page.md":           "# Deep\nDEEPER-PHRASE\n",
	})
	got := readDocSetAt(t, root)

	for _, want := range []string{"NESTED-PHRASE", "DEEPER-PHRASE"} {
		if !strings.Contains(got, want) {
			t.Errorf("the doc set misses %q from a page nested under docs/", want)
		}
	}
}

// Before any page exists the set is the README alone, and reading it is not
// an error.
func TestDocsSet_WithoutADocsDirectoryIsTheREADME(t *testing.T) {
	const readme = "# Tool\n\nWhat the tool is.\n"
	root := writeDocTree(t, map[string]string{"README.md": readme})
	got := readDocSetAt(t, root)

	if strings.TrimRight(got, "\n") != strings.TrimRight(readme, "\n") {
		t.Errorf("with no docs/ directory the doc set is %q, want the README %q", got, readme)
	}
}

func TestDocsSet_PutsTheREADMEFirstThenPagesByName(t *testing.T) {
	root := writeDocTree(t, map[string]string{
		"README.md": "README-MARK\n",
		"docs/b.md": "B-MARK\n",
		"docs/a.md": "A-MARK\n",
	})
	got := readDocSetAt(t, root)

	r, a, b := strings.Index(got, "README-MARK"), strings.Index(got, "A-MARK"), strings.Index(got, "B-MARK")
	if r < 0 || a < 0 || b < 0 {
		t.Fatalf("the doc set misses a file: README at %d, a.md at %d, b.md at %d", r, a, b)
	}
	if r >= a || a >= b {
		t.Errorf("order: README at %d, docs/a.md at %d, docs/b.md at %d; want README, then pages by name", r, a, b)
	}
}

// readDocSet reads the repository's own documentation, README first.
func TestDocsSet_ReadsTheRepositoryDocs(t *testing.T) {
	readme := strings.TrimRight(readRepoDoc(t, "README.md"), "\n")
	got := readDocSet(t)
	if !strings.HasPrefix(got, readme) {
		t.Errorf("readDocSet does not start with the repository README (%d bytes read, README is %d)", len(got), len(readme))
	}
}
