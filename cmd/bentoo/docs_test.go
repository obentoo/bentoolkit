package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readRepoDoc reads a documentation file from the repository root. The
// cmd/bentoo package directory is two levels below the root, so the doc files
// (README.md, CHANGELOG.md) are reached via "../../".
func readRepoDoc(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", name)
	data, err := os.ReadFile(path) //nolint:gosec // fixed, test-local doc path
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// requireContains fails the test when haystack does not contain every needle.
func requireContains(t *testing.T, doc, haystack string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			t.Errorf("%s: expected to contain %q, but it does not", doc, needle)
		}
	}
}

func TestREADME_DocumentsExitCodes(t *testing.T) {
	readme := readRepoDoc(t, "README.md")
	requireContains(t, "README.md", readme,
		"### Exit codes",
		"`0`",
		"`1`",
		"`2`",
		"autoupdate",
	)
}

func TestREADME_DocumentsConcurrency(t *testing.T) {
	readme := readRepoDoc(t, "README.md")
	requireContains(t, "README.md", readme,
		"### Concurrency",
		"--concurrency",
		"100",
		"`10`",
	)
}

func TestREADME_DocumentsHeaderAllowlist(t *testing.T) {
	readme := readRepoDoc(t, "README.md")
	requireContains(t, "README.md", readme,
		"### Headers and environment variables",
		"BENTOO_",
		"Authorization",
		"X-Api-Key",
		"X-Auth-Token",
		"Private-Token",
		"allow-list",
		"${BENTOO_MY_TOKEN}",
	)
}

func TestREADME_DocumentsHTTP2(t *testing.T) {
	readme := readRepoDoc(t, "README.md")
	requireContains(t, "README.md", readme,
		"### HTTP/2",
		"BENTOO_DISABLE_HTTP2",
		"HTTP/2 by default",
	)
}

func TestREADME_DocumentsFilesystem(t *testing.T) {
	readme := readRepoDoc(t, "README.md")
	requireContains(t, "README.md", readme,
		"### Filesystem assumptions",
		"0600",
		"FAT32",
		"exFAT",
		"Warn",
	)
}

func TestCHANGELOG_HasV020(t *testing.T) {
	changelog := readRepoDoc(t, "CHANGELOG.md")
	requireContains(t, "CHANGELOG.md", changelog,
		"## [0.2.0]",
		"### Added",
		"### Changed",
		"### Security",
		"### Fixed",
		"go test -race ./...",
		"golangci-lint run",
		"govulncheck ./...",
		"make audit-ctx",
	)
}
