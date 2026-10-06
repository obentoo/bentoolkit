package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
)

// TestREADMEExampleIsAConfigThisParserAccepts reads the documented record out of
// the README and puts it through the real loader and the real parser.
//
// A documented example nobody executes is a plausible-looking string: the
// previous draft of this one used backslash line continuations, which TOML does
// not have, and it read perfectly well. An operator who copies a broken example
// discovers it as a failed sweep.
func TestREADMEExampleIsAConfigThisParserAccepts(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}

	const marker = `meta = { fetch_url = "https://vendor.example`
	var line string
	for _, l := range strings.Split(string(readme), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), marker) {
			line = strings.TrimSpace(l)
			break
		}
	}
	if line == "" {
		t.Fatalf("the README no longer carries the documented two-step meta example (looked for %q);\n"+
			"if it was renamed or moved, move this guard with it rather than deleting it", marker)
	}

	cfg, err := decodePackagesConfig([]byte("[\"app-misc/example\"]\n" +
		"url = \"https://upstream.test/releases.json\"\nparser = \"json\"\npath = \"tag_name\"\n" +
		line + "\ncomments = \"\"\"the README's own example\"\"\"\n"))
	if err != nil {
		t.Fatalf("the README example does not load: %v", err)
	}
	pkg := cfg.Packages["app-misc/example"]
	if err := ValidatePackageConfig(nil, "app-misc/example", &pkg); err != nil {
		t.Fatalf("the README example does not pass --lint's validation: %v", err)
	}

	spec, ok, err := fetch.ParseAuthFetchSpec(pkg.Meta)
	if err != nil || !ok {
		t.Fatalf("the README example does not parse: ok=%v err=%v", ok, err)
	}
	if !spec.UsesIDLookup() || spec.Body != fetch.FetchBodyJSON || spec.Response != fetch.FetchResponseURL {
		t.Errorf("the example no longer demonstrates what its prose claims: %+v", spec)
	}
	if spec.MinBytes == 0 || spec.ContentType == "" {
		t.Errorf("the example no longer demonstrates the final-response guards: %+v", spec)
	}
}
