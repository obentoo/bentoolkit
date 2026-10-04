package main

// Story 069, sub-task 2.2 (R6.2, R6.3): the design-system docs describe the
// browser build tags as chromedp-only, and the changelog records the removal
// of the playwright backend with what replaces it.

import (
	"strings"
	"testing"
)

// R6.2 - both design-system documents still explain why the package is not
// behind a build tag, and name only chromedp when they do.
func TestSingleBackendDesignDocsNameOnlyChromedp(t *testing.T) {
	for _, name := range []string{
		"misc/design/design-system/doc.go",
		"misc/design/design-system/README.md",
	} {
		lower := strings.ToLower(readRepoDoc(t, name))
		if strings.Contains(lower, "playwright") {
			t.Errorf("%s still describes a playwright build tag", name)
		}
		// Converse: deleting the passage would also remove every playwright
		// mention, and would leave the docs describing no backend at all.
		if !strings.Contains(lower, "chromedp") {
			t.Errorf("%s no longer names the chromedp build tag", name)
		}
	}
}

// R6.3 - an entry newer than the last release before this story records the
// removal, the replacement flag and the run-time browser requirement, all in
// the same entry.
func TestSingleBackendChangelogRecordsRemoval(t *testing.T) {
	changelog := readRepoDoc(t, "CHANGELOG.md")
	// Everything above 0.32.0 is [Unreleased] now and whatever release ships
	// this story later, so the test survives the cut that moves the entry.
	const lastBefore = "## [0.32.0]"
	i := strings.Index(changelog, lastBefore)
	if i < 0 {
		t.Fatalf("CHANGELOG.md has no %s heading to bound the newer entries", lastBefore)
	}
	region := changelog[:i]

	var entries []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			entries = append(entries, cur.String())
			cur.Reset()
		}
	}
	for _, line := range strings.Split(region, "\n") {
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "#") {
			flush()
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	flush()

	var removal []string
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e), "playwright") {
			removal = append(removal, e)
		}
	}
	if len(removal) == 0 {
		t.Fatal("no CHANGELOG entry above [0.32.0] records the playwright backend's removal")
	}

	for _, e := range removal {
		lower := strings.ToLower(e)
		// Hostile half first: "chromedp" begins with "chrome", so an entry that
		// names only the tag must not count as naming the browser.
		browser := strings.ReplaceAll(lower, "chromedp", "")
		namesBrowser := strings.Contains(browser, "chrome") || strings.Contains(browser, "chromium")
		if namesBrowser && strings.Contains(e, "-tags chromedp") && strings.Contains(e, "PATH") {
			return
		}
	}
	t.Errorf("no removal entry names all of `-tags chromedp`, Chrome or Chromium, and PATH:\n%s",
		strings.Join(removal, "\n"))
}
