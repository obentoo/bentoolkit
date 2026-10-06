package realign

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// realignPackageDoc returns this package's doc comment as go doc assembles it:
// the package comments of every non-test file, joined, lower-cased and with
// whitespace collapsed so a reflowed paragraph reads the same.
func realignPackageDoc(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	fset := token.NewFileSet()
	var parts []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		if f.Doc != nil {
			parts = append(parts, f.Doc.Text())
		}
	}
	if len(parts) == 0 {
		t.Fatal("internal/realign has no package doc comment at all")
	}
	return strings.Join(strings.Fields(strings.ToLower(strings.Join(parts, "\n"))), " ")
}

// realignDocSentences splits a normalised doc into sentences. A period followed
// by a space ends one; a period inside a path or a file name does not.
func realignDocSentences(doc string) []string {
	var out []string
	for s := range strings.SplitSeq(doc, ". ") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// realignContainsAll reports whether s contains every word in all, and at least one of
// anyOf (anyOf may be empty).
func realignContainsAll(s string, all, anyOf []string) bool {
	for _, w := range all {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return len(anyOf) == 0 || slices.ContainsFunc(anyOf, func(w string) bool { return strings.Contains(s, w) })
}

// TestPackageDocSaysValidateOverlayCycleIsGone pins R3.3 and R3.4 (story 061,
// design D8). Story 061 moved ScanOverlay out of internal/overlay, so
// internal/autoupdate/validate no longer imports it and the validate -> overlay
// cycle this package's doc used to cite is gone. The doc must stop claiming it
// (R3.3), and must keep the reason that still holds: internal/overlay must not
// import internal/autoupdate (R3.4).
//
// The wording checks are deliberately loose: they look for the facts, in any
// phrasing, never for one sentence. A later change that rewrites the paragraph
// again edits this test in the same change only if it drops one of the facts.
func TestPackageDocSaysValidateOverlayCycleIsGone(t *testing.T) {
	doc := realignPackageDoc(t)
	sentences := realignDocSentences(doc)

	// The stale claim, in the two forms the pre-061 paragraph used.
	for _, stale := range []string{"the edge back is a cycle", "validate imports internal/overlay itself"} {
		if strings.Contains(doc, stale) {
			t.Errorf("the package doc still claims %q; since story 061 validate no longer imports internal/overlay, "+
				"so there is no such cycle (R3.3)", stale)
		}
	}

	gone := []string{"no longer", "gone", "not a cycle", "no cycle", "longer exists", "does not exist", "no such cycle"}
	statesGone := slices.ContainsFunc(sentences, func(s string) bool {
		return realignContainsAll(s, []string{"cycle"}, gone) &&
			(strings.Contains(s, "validate") || strings.Contains(s, "overlay"))
	})
	if !statesGone {
		t.Errorf("the package doc does not state that the validate -> internal/overlay cycle no longer exists (R3.3); "+
			"want one sentence that names the cycle, validate or internal/overlay, and says it is gone. Doc:\n%s", doc)
	}

	negations := []string{"must not import", "must never import", "may not import", "imports nothing from",
		"never imports", "does not import", "free of an import"}
	keepsReason := slices.ContainsFunc(sentences, func(s string) bool {
		return realignContainsAll(s, []string{"internal/overlay", "internal/autoupdate"}, negations)
	})
	if !keepsReason {
		t.Errorf("the package doc no longer states that internal/overlay must not import internal/autoupdate, "+
			"which is why this package remains (R3.4). Doc:\n%s", doc)
	}
}
