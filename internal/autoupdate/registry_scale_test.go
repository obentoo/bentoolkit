package autoupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidatePackageConfigAcceptsAPinlessRegistryAtScale is S021-R1.2's
// durable guard: a registry where NO record declares `version` must load and
// validate in full. It exists because the real overlay's registry is exactly
// that — 410 pinless records at the time this landed — and a validation rule
// that is even slightly too strict would reject the whole file at load, which
// presents to the operator as "autoupdate stopped working", not as a config
// error on one entry.
//
// The fixture is generated rather than committed: what matters is the SHAPES a
// real record takes (a bare atom, a @label with a series, a :slot, a multi-line
// comments block, a revision suffix), repeated at a scale where a per-record
// rule that rejects one in a hundred cannot hide.
func TestValidatePackageConfigAcceptsAPinlessRegistryAtScale(t *testing.T) {
	const records = 400

	var b strings.Builder
	for i := 0; i < records; i++ {
		key := fmt.Sprintf("cat-%d/pkg-%d", i%20, i)
		switch i % 4 {
		case 1:
			key += "@stable"
		case 2:
			key += ":4.1"
		case 3:
			key += "@dev"
		}
		fmt.Fprintf(&b, "[%q]\n", key)
		fmt.Fprintf(&b, "url = \"https://example.invalid/%d/releases\"\n", i)
		b.WriteString("parser = \"regex\"\n")
		b.WriteString("pattern = '\"name\":\"([0-9]+\\.[0-9]+\\.[0-9]+)\"'\n")
		b.WriteString("select = \"max\"\n")
		if i%4 == 1 {
			b.WriteString("series = '^[0-9]+\\.[0-9]*[02468]\\.'\n")
		}
		if i%4 == 3 {
			b.WriteString("series = '^[0-9]+\\.[0-9]*[13579]\\.'\n")
		}
		if i%7 == 0 {
			b.WriteString("revision = 1\n")
		}
		// A comments body may legally contain lines that look like a table
		// header or a comment — the record model says so, and the raw-text
		// scanner has to survive them.
		b.WriteString("comments = \"\"\"\n")
		b.WriteString("# this line opens with a hash\n")
		b.WriteString("[this line opens with a bracket]\n")
		b.WriteString("version = \"not-a-real-assignment\"\n")
		b.WriteString("\"\"\"\n")
		b.WriteString("# END\n\n")
	}

	overlay := t.TempDir()
	if err := os.MkdirAll(filepath.Join(overlay, ".autoupdate"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(overlay, ".autoupdate", "packages.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("a fully pinless registry must load: %v", err)
	}
	if got := len(cfg.Packages); got != records {
		t.Fatalf("expected %d records, got %d", records, got)
	}
	for key := range cfg.Packages {
		pc := cfg.Packages[key]
		if pc.Version != "" {
			t.Fatalf("fixture is meant to be pinless, but %s carries %q", key, pc.Version)
		}
		if err := ValidatePackageConfig(key, &pc); err != nil {
			t.Errorf("pinless record %s must validate: %v", key, err)
		}
	}

	// The same registry must also survive a no-op write byte-for-byte: the file
	// is published, so a batch that changes nothing must produce no diff.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := SetPackageVersions(overlay, nil); err != nil {
		t.Fatalf("SetPackageVersions(nil): %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Error("an empty batch rewrote a pinless registry")
	}
}
