package autoupdate

import (
	"os"
	"testing"
)

func TestEnablePackagesInConfigRewritesExisting(t *testing.T) {
	content := `["a/b"]
enabled = false
url = "https://x/y"
parser = "json"
path = "v"
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := EnablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("EnablePackagesInConfig: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	want := `["a/b"]
enabled = true
url = "https://x/y"
parser = "json"
path = "v"
`
	if string(got) != want {
		t.Errorf("unexpected output:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// The result must still parse and report the package as enabled.
	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if pc := cfg.Packages["a/b"]; !pc.IsEnabled() {
		t.Error("expected a/b to be enabled after edit")
	}
}

// Enable must NOT insert an `enabled` key when one is absent: a section with no
// enabled field is already enabled by default (IsEnabled returns true for nil),
// so adding the line would only churn the hand-maintained file.
func TestEnablePackagesInConfigAbsentKeyStaysAbsent(t *testing.T) {
	content := `# header comment
["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
`
	overlay, configPath := writePackagesTOML(t, content)
	before, _ := os.ReadFile(configPath)

	if err := EnablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("EnablePackagesInConfig: %v", err)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Errorf("file changed unexpectedly (enable must not insert):\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	// Still enabled (it always was, by default).
	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if pc := cfg.Packages["a/b"]; !pc.IsEnabled() {
		t.Error("expected a/b to remain enabled")
	}
}

// Comments, ordering, and a multi-line array value must survive the edit; only
// the targeted enabled assignment changes.
func TestEnablePackagesInConfigPreservesCommentsAndOrder(t *testing.T) {
	content := `# top comment
["dev-util/claude-code"]
enabled = false
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
# trailing comment for claude-code
transform = [
  ["-", "."],
  ["_", "."],
]

["sys-apps/pnpm"]
url = "https://registry.npmjs.org/pnpm"
parser = "json"
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := EnablePackagesInConfig(overlay, []string{"dev-util/claude-code"}); err != nil {
		t.Fatalf("EnablePackagesInConfig: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	want := `# top comment
["dev-util/claude-code"]
enabled = true
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
# trailing comment for claude-code
transform = [
  ["-", "."],
  ["_", "."],
]

["sys-apps/pnpm"]
url = "https://registry.npmjs.org/pnpm"
parser = "json"
`
	if string(got) != want {
		t.Errorf("unexpected output:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Multiple packages in one call: each existing key is rewritten; an absent key
// is left untouched.
func TestEnablePackagesInConfigBatch(t *testing.T) {
	content := `["a/b"]
enabled = false
url = "https://x/y"
parser = "json"
path = "v"

["c/d"]
enabled = false
url = "https://x/z"
parser = "json"
path = "v"

["e/f"]
url = "https://x/w"
parser = "json"
path = "v"
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := EnablePackagesInConfig(overlay, []string{"a/b", "e/f"}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	want := `["a/b"]
enabled = true
url = "https://x/y"
parser = "json"
path = "v"

["c/d"]
enabled = false
url = "https://x/z"
parser = "json"
path = "v"

["e/f"]
url = "https://x/w"
parser = "json"
path = "v"
`
	if string(got) != want {
		t.Errorf("unexpected output:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	enabled := func(pkg string) bool {
		pc := cfg.Packages[pkg]
		return pc.IsEnabled()
	}
	if !enabled("a/b") {
		t.Error("a/b should be enabled")
	}
	if enabled("c/d") {
		t.Error("c/d should remain disabled")
	}
	if !enabled("e/f") {
		t.Error("e/f should remain enabled")
	}
}

// An empty list, or a package whose section is absent, leaves the file
// untouched.
func TestEnablePackagesInConfigNoOps(t *testing.T) {
	content := `["a/b"]
enabled = false
url = "https://x/y"
parser = "json"
path = "v"
`
	overlay, configPath := writePackagesTOML(t, content)
	before, _ := os.ReadFile(configPath)

	// Empty list: untouched.
	if err := EnablePackagesInConfig(overlay, nil); err != nil {
		t.Fatalf("nil list: %v", err)
	}
	// Absent package: untouched.
	if err := EnablePackagesInConfig(overlay, []string{"c/d"}); err != nil {
		t.Fatalf("absent pkg: %v", err)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Errorf("file changed unexpectedly:\n%s", after)
	}
}

// A file with no trailing newline must be reproduced byte-for-byte aside from
// the rewritten assignment.
func TestEnablePackagesInConfigNoTrailingNewline(t *testing.T) {
	content := `["a/b"]
enabled = false
url = "https://x/y"
parser = "json"
path = "v"`
	overlay, configPath := writePackagesTOML(t, content)
	if err := EnablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("EnablePackagesInConfig: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	want := `["a/b"]
enabled = true
url = "https://x/y"
parser = "json"
path = "v"`
	if string(got) != want {
		t.Errorf("trailing newline not preserved:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}
