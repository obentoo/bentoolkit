package main

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Story 052, sub-task 4.1 — S052-R8.1, R8.2: the README states each variable's
// host binding, the refusal, the removal of the two LLM keys and the redirect
// rule, and no example pairs ${GITHUB_TOKEN} with a non-GitHub host; the
// CHANGELOG records the breaking change and its migration.
func TestREADME_DocumentsCredentialHostBinding(t *testing.T) {
	readme := readRepoDoc(t, "README.md")

	section := sectionFrom(readme, "### Headers and environment variables")
	if section == "" {
		t.Fatal(`README has no "### Headers and environment variables" section`)
	}
	requireContains(t, "README.md#headers", section,
		"api.github.com", "codeload.github.com", "objects.githubusercontent.com",
		"raw.githubusercontent.com", "gitlab.com", "base_url", "https", "redirect",
	)
	if !regexp.MustCompile(`(?i)refus`).MatchString(section) {
		t.Error("the headers section does not describe the refusal of a mismatched credential")
	}
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		removed := false
		for _, line := range strings.Split(section, "\n") {
			if strings.Contains(line, key) && regexp.MustCompile(`(?i)no longer|removed|not expand`).MatchString(line) {
				removed = true
			}
		}
		if !removed {
			t.Errorf("the headers section does not state that %s is no longer expandable", key)
		}
	}

	// Hostile: an example that teaches the leak. Any fenced block expanding
	// ${GITHUB_TOKEN} may only name GitHub hosts in its url fields.
	github := map[string]bool{"api.github.com": true, "github.com": true, "codeload.github.com": true,
		"objects.githubusercontent.com": true, "raw.githubusercontent.com": true}
	urlField := regexp.MustCompile(`(?m)^\s*(?:url|base_url|fallback_url)\s*=\s*"([^"]+)"`)
	for i, block := range fencedBlocks(readme) {
		if !strings.Contains(block, "${GITHUB_TOKEN}") {
			continue
		}
		for _, m := range urlField.FindAllStringSubmatch(block, -1) {
			u, err := url.Parse(m[1])
			if err != nil || !github[strings.ToLower(u.Hostname())] {
				t.Errorf("README code block #%d pairs ${GITHUB_TOKEN} with %q, a non-GitHub host", i, m[1])
			}
		}
	}

	t.Run("CHANGELOG records the breaking change", func(t *testing.T) {
		changelog := readRepoDoc(t, "CHANGELOG.md")
		unreleased := sectionFrom(changelog, "## [Unreleased]")
		security := sectionFrom(unreleased, "### Security")
		if security == "" {
			t.Fatal("CHANGELOG [Unreleased] has no ### Security entry")
		}
		requireContains(t, "CHANGELOG.md#unreleased-security", security,
			"BREAKING", "BENTOO_", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GITHUB_TOKEN")
	})
}

// sectionFrom returns the text from heading up to the next heading of the same
// or a higher level, or "" when heading is absent.
func sectionFrom(doc, heading string) string {
	i := strings.Index(doc, heading)
	if i < 0 {
		return ""
	}
	level := strings.Count(strings.SplitN(heading, " ", 2)[0], "#")
	rest := doc[i+len(heading):]
	for off := 0; ; {
		j := strings.Index(rest[off:], "\n#")
		if j < 0 {
			return heading + rest
		}
		line := rest[off+j+1:]
		hashes := len(line) - len(strings.TrimLeft(line, "#"))
		if hashes <= level && strings.HasPrefix(line[hashes:], " ") {
			return heading + rest[:off+j]
		}
		off += j + 2
	}
}

// fencedBlocks returns the bodies of the ``` fenced code blocks in doc.
func fencedBlocks(doc string) []string {
	var blocks []string
	parts := strings.Split(doc, "```")
	for i := 1; i < len(parts); i += 2 {
		blocks = append(blocks, parts[i])
	}
	return blocks
}
