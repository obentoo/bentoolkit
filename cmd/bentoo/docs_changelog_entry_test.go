package main

import (
	"regexp"
	"strings"
	"testing"
)

// changelogSections splits a CHANGELOG into its "## [" sections.
func changelogSections(text string) []string {
	idx := regexp.MustCompile(`(?m)^## \[`).FindAllStringIndex(text, -1)
	var out []string
	for i, at := range idx {
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out = append(out, text[at[0]:end])
	}
	return out
}

// changelogHygieneEntryProblems returns what the section announcing the
// comment guard fails to name: messages without tracker IDs, the docs/
// reference pages, and the CHANGELOG archive. The section is whichever one
// mentions `audit-comments` — [Unreleased] before the release cut, the release
// after it — so the check survives the cut.
func changelogHygieneEntryProblems(changelog string) []string {
	for _, s := range changelogSections(changelog) {
		if !strings.Contains(s, "audit-comments") {
			continue
		}
		var problems []string
		if !regexp.MustCompile(`(?i)tracker`).MatchString(s) {
			problems = append(problems, "it does not say messages no longer carry tracker IDs")
		}
		if strings.Count(s, "docs/") <= strings.Count(s, "docs/changelog/") {
			problems = append(problems, "it does not name the docs/ reference pages (the archive path alone does not count)")
		}
		if !strings.Contains(s, changelogArchiveFile) {
			problems = append(problems, "it does not name the CHANGELOG archive "+changelogArchiveFile)
		}
		return problems
	}
	return []string{"no section mentions `audit-comments`"}
}

// TestCHANGELOG_EntryNamesTheCommentGuardDocsAndArchive checks the entry that
// ships this change names what operators will notice.
func TestCHANGELOG_EntryNamesTheCommentGuardDocsAndArchive(t *testing.T) {
	t.Run("the archive path does not stand in for the docs/ pages", func(t *testing.T) {
		entry := "## [Unreleased]\n\n- `make audit-comments` keeps tracker IDs out; old releases moved to " + changelogArchiveFile + ".\n"
		if len(changelogHygieneEntryProblems(entry)) == 0 {
			t.Error("an entry naming only the archive was accepted as naming the docs/ pages")
		}
	})
	t.Run("a mention in another section does not count", func(t *testing.T) {
		text := "## [Unreleased]\n\n- `make audit-comments` keeps tracker IDs out.\n\n## [0.32.0] - x\n\n- README moved to docs/; old releases in " + changelogArchiveFile + ".\n"
		if len(changelogHygieneEntryProblems(text)) == 0 {
			t.Error("an entry split across two sections was accepted")
		}
	})
	t.Run("the repository's CHANGELOG", func(t *testing.T) {
		for _, p := range changelogHygieneEntryProblems(readRepoDoc(t, changelogFile)) {
			t.Errorf("%s: the entry for the comment guard: %s", changelogFile, p)
		}
	})
}
