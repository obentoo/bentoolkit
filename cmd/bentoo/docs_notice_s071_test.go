package main

// Story 071, sub-task 7.2 (R6.3, Q11): the README documents `bentoo notice`
// and names only commands that exist; config.example.yaml documents
// notice.site_path; the changelog section that ships 071 (0.32.0) records the
// commands.

import (
	"regexp"
	"testing"
)

func TestDocsNotice_READMEDocumentsBothCommands(t *testing.T) {
	readme := readDocSet(t)
	requireContains(t, "README.md", readme,
		"bentoo notice new",
		"bentoo notice revise",
		"site_path",
		"--affects",
		"EDITOR",
	)
}

// Every `bentoo notice <word>` the README names resolves to a real command.
func TestDocsNotice_READMENamesOnlyRealCommands(t *testing.T) {
	readme := readDocSet(t)
	subs := regexp.MustCompile(`bentoo notice ([a-z][a-z-]*)`).FindAllStringSubmatch(readme, -1)
	if len(subs) == 0 {
		t.Fatal("the README names no `bentoo notice` command")
	}
	root := newRootCmd()
	for _, m := range subs {
		cmd, _, err := root.Find([]string{"notice", m[1]})
		if err != nil || cmd == nil || cmd.Name() != m[1] {
			t.Errorf("the README names `bentoo notice %s`, which is not a command", m[1])
		}
	}
}

func TestDocsNotice_ConfigExampleDocumentsSitePath(t *testing.T) {
	example := readRepoDoc(t, "config.example.yaml")
	if !regexp.MustCompile(`(?m)^#?\s*notice:\s*$`).MatchString(example) {
		t.Error("config.example.yaml has no notice: section")
	}
	requireContains(t, "config.example.yaml", example, "site_path")
}

func TestDocsNotice_ChangelogUnreleasedRecordsTheCommands(t *testing.T) {
	changelog := readRepoDoc(t, "CHANGELOG.md")
	section := shippingSection(changelog, "0.32.0")
	if section == "" {
		t.Fatal("CHANGELOG.md has neither a [0.32.0] nor an Unreleased section")
	}
	requireContains(t, "CHANGELOG.md [0.32.0]", section, "bentoo notice")
}
