package main

// Authored for story 058, sub-task 8.1 — R2.10.
//
// The README's "Exit codes" section documents every command's contract, not
// only overlay autoupdate's: success, failure, usage error, total and partial
// batch failure, and interruption.
//
// Red on arrival: the section documents overlay autoupdate only.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// s058MarkdownSection returns the body under the first heading whose text
// contains title, up to the next heading of the same or a higher level.
func s058MarkdownSection(doc, title string) string {
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		level := len(line) - len(strings.TrimLeft(line, "#"))
		if level == 0 || !strings.Contains(line, title) {
			continue
		}
		var body []string
		for _, next := range lines[i+1:] {
			l := len(next) - len(strings.TrimLeft(next, "#"))
			if l > 0 && l <= level && strings.HasPrefix(strings.TrimLeft(next, "#"), " ") {
				break
			}
			body = append(body, next)
		}
		return strings.Join(body, "\n")
	}
	return ""
}

func TestS058ReadmeDocumentsTheExitCodeContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	section := s058MarkdownSection(string(data), "Exit codes")
	if strings.TrimSpace(section) == "" {
		t.Fatal(`README.md has no "Exit codes" section`)
	}
	lower := strings.ToLower(section)
	for _, want := range []string{
		"overlay validate", "130", "interrupt",
		"overlay analyze --all", "overlay autoupdate --check", "partial", "total",
		"usage",
	} {
		if !strings.Contains(lower, strings.ToLower(want)) {
			t.Errorf("the Exit codes section does not mention %q", want)
		}
	}
}
