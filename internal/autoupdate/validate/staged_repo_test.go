package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagedRepoSection_NamesTheTreeAsStageDid(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles", "repo_name"), []byte("bentoo-staging-demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	name, section, err := StagedRepoSection(root, "app-misc/demo@stable", "2.0")
	if err != nil {
		t.Fatal(err)
	}
	if name != "bentoo-staging-demo" {
		t.Errorf("name = %q, want the tree's own repo_name", name)
	}
	for _, want := range []string{"[bentoo-staging-demo]", "location = " + root, "masters = gentoo"} {
		if !strings.Contains(section, want) {
			t.Errorf("section %q lacks %q", section, want)
		}
	}
}
