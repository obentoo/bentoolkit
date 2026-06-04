package autoupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// writePackagesTOML writes content to overlay/.autoupdate/packages.toml under a
// temp dir and returns the overlay path and the config file path.
func writePackagesTOML(t *testing.T, content string) (overlayPath, configPath string) {
	t.Helper()
	overlayPath = t.TempDir()
	dir := filepath.Join(overlayPath, ".autoupdate")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath = filepath.Join(dir, "packages.toml")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return overlayPath, configPath
}

func TestTomlTableName(t *testing.T) {
	cases := []struct {
		line     string
		wantName string
		wantOK   bool
	}{
		{`["dev-util/claude-code"]`, "dev-util/claude-code", true},
		{`  [ "net-misc/foo" ]  # comment`, "net-misc/foo", true},
		{`['app/bar']`, "app/bar", true},
		{`[plain]`, "plain", true},
		{`[[array.table]]`, "", false},
		{`# ["commented/out"]`, "", false},
		{`["-", "."],`, "", false}, // array continuation line, not a header
		{`url = "https://x/[y]"`, "", false},
		{`transform = [["-", "."]]`, "", false},
		{``, "", false},
	}
	for _, tc := range cases {
		gotName, gotOK := tomlTableName(tc.line)
		if gotName != tc.wantName || gotOK != tc.wantOK {
			t.Errorf("tomlTableName(%q) = (%q, %v), want (%q, %v)",
				tc.line, gotName, gotOK, tc.wantName, tc.wantOK)
		}
	}
}

func TestDisablePackagesInConfigInserts(t *testing.T) {
	content := `# header comment
["dev-util/claude-code"]
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
# trailing comment for claude-code

["sys-apps/pnpm"]
url = "https://registry.npmjs.org/pnpm"
parser = "json"
`
	overlay, configPath := writePackagesTOML(t, content)

	if err := DisablePackagesInConfig(overlay, []string{"dev-util/claude-code"}); err != nil {
		t.Fatalf("DisablePackagesInConfig: %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := `# header comment
["dev-util/claude-code"]
enabled = false
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
# trailing comment for claude-code

["sys-apps/pnpm"]
url = "https://registry.npmjs.org/pnpm"
parser = "json"
`
	if string(got) != want {
		t.Errorf("unexpected output:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// The result must still parse and report the package as disabled.
	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if pc := cfg.Packages["dev-util/claude-code"]; pc.IsEnabled() {
		t.Error("expected dev-util/claude-code to be disabled after edit")
	}
	if pc := cfg.Packages["sys-apps/pnpm"]; !pc.IsEnabled() {
		t.Error("expected sys-apps/pnpm to remain enabled")
	}
}

func TestDisablePackagesInConfigRewritesExisting(t *testing.T) {
	content := `["a/b"]
enabled = true
url = "https://x/y"
parser = "json"
path = "v"
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := DisablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("DisablePackagesInConfig: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	want := `["a/b"]
enabled = false
url = "https://x/y"
parser = "json"
path = "v"
`
	if string(got) != want {
		t.Errorf("unexpected output:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestDisablePackagesInConfigPreservesMultilineArray(t *testing.T) {
	// A multi-line array value must not be mistaken for a section header.
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
transform = [
  ["-", "."],
  ["_", "."],
]
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := DisablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("DisablePackagesInConfig: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	want := `["a/b"]
enabled = false
url = "https://x/y"
parser = "json"
path = "v"
transform = [
  ["-", "."],
  ["_", "."],
]
`
	if string(got) != want {
		t.Errorf("multiline array corrupted:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestDisablePackagesInConfigNoOps(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
`
	overlay, configPath := writePackagesTOML(t, content)
	before, _ := os.ReadFile(configPath)

	// Empty list: untouched.
	if err := DisablePackagesInConfig(overlay, nil); err != nil {
		t.Fatalf("nil list: %v", err)
	}
	// Absent package: untouched.
	if err := DisablePackagesInConfig(overlay, []string{"c/d"}); err != nil {
		t.Fatalf("absent pkg: %v", err)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Errorf("file changed unexpectedly:\n%s", after)
	}
}

func TestDisablePackagesInConfigBatch(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"

["c/d"]
url = "https://x/z"
parser = "json"
path = "v"

["e/f"]
url = "https://x/w"
parser = "json"
path = "v"
`
	overlay, _ := writePackagesTOML(t, content)
	if err := DisablePackagesInConfig(overlay, []string{"a/b", "e/f"}); err != nil {
		t.Fatalf("batch: %v", err)
	}
	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	enabled := func(pkg string) bool {
		pc := cfg.Packages[pkg]
		return pc.IsEnabled()
	}
	if enabled("a/b") {
		t.Error("a/b should be disabled")
	}
	if !enabled("c/d") {
		t.Error("c/d should remain enabled")
	}
	if enabled("e/f") {
		t.Error("e/f should be disabled")
	}
}
