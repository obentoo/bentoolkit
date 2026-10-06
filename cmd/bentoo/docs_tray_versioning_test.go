package main

// docs/tray.md says how bentoo-tray is versioned: on its own line,
// independent of bentoolkit, from internal/tray/version/VERSION, bumped in
// the change that alters the tray following semantic versioning, and shipped
// inside the bentoolkit release tarball.

import (
	"strings"
	"testing"
)

// markdownSection returns the lines from the heading line equal to heading up
// to the next heading of the same or a higher level. Lines inside ``` or ~~~
// fences are never headings, so a shell comment in an example does not end the
// section. It returns "" when the heading is absent.
func markdownSection(doc, heading string) string {
	level := len(heading) - len(strings.TrimLeft(heading, "#"))
	var out []string
	in, fence := false, ""
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence == "" && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			fence = trimmed[:3]
		} else if fence != "" && strings.HasPrefix(trimmed, fence) {
			fence = ""
		} else if fence == "" && strings.HasPrefix(line, "#") {
			hashes := len(line) - len(strings.TrimLeft(line, "#"))
			isHeading := strings.HasPrefix(line[hashes:], " ")
			if in && isHeading && hashes <= level {
				break
			}
			if !in && line == heading {
				in = true
			}
		}
		if in {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func TestDocsTray_VersioningSectionStatesTheRule(t *testing.T) {
	section := markdownSection(readRepoDoc(t, "docs/tray.md"), "### Versioning")
	if section == "" {
		t.Fatal("docs/tray.md has no `### Versioning` section")
	}
	requireContains(t, "docs/tray.md ### Versioning", section,
		"internal/tray/version/VERSION",
		"bentoo-tray version",
		"bentoolkit",
	)
	lower := strings.ToLower(section)
	for _, phrase := range []string{"independent", "semantic versioning", "same change", "tarball"} {
		if !strings.Contains(lower, phrase) {
			t.Errorf("docs/tray.md ### Versioning does not say %q:\n%s", phrase, section)
		}
	}
}

// The section helper itself: a fenced shell comment does not end a section,
// and the next heading of the same level does.
func TestDocsTray_MarkdownSectionSkipsFencedComments(t *testing.T) {
	doc := "## Page\n### Install\nx\n### Versioning\nkept\n```bash\n# a comment\n```\nstill kept\n#### Sub\nsub kept\n### Next\ndropped\n"
	got := markdownSection(doc, "### Versioning")
	for _, want := range []string{"kept", "# a comment", "still kept", "sub kept"} {
		if !strings.Contains(got, want) {
			t.Errorf("section lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "dropped") || strings.Contains(got, "### Install") {
		t.Errorf("section runs past its bounds:\n%s", got)
	}
	if markdownSection(doc, "### Absent") != "" {
		t.Error("an absent heading yields a section")
	}
}
