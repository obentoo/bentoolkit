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

// depthEnumeration returns just the list of rung names out of a --depth usage
// string: the run of text that ENDS at ", each including every rung before it"
// and BEGINS after the em dash introducing it.
//
// Matching against this slice rather than against the whole usage string is the
// entire point. A document-wide search for "install" passes on any unrelated
// occurrence — "src_install", "installed", a sentence about --depth=install
// elsewhere in the same help — and story 041 found exactly that false guard.
func depthEnumeration(t *testing.T, where, usage string) string {
	t.Helper()

	const tail = ", each including every rung before it"
	end := strings.Index(usage, tail)
	if end < 0 {
		t.Fatalf("%s: the --depth usage does not carry %q, so its enumeration cannot be isolated and every "+
			"assertion below would be searching the whole sentence:\n%s", where, tail, usage)
	}
	head := usage[:end]

	const dash = "— "
	if start := strings.LastIndex(head, dash); start >= 0 {
		head = head[start+len(dash):]
	}
	return head
}

// TestDepthFlags_EveryEnumerationNamesTheInstallRung is S042-R5.1 and R5.2.
//
// The three enumerations are deliberately NOT unified. Each names a different
// subset for a reason: compare's path builds only what a realignment proposes
// and reports nothing at all below patches, so collapsing the three sentences
// would widen compare's documented surface as a side effect.
func TestDepthFlags_EveryEnumerationNamesTheInstallRung(t *testing.T) {
	for _, tt := range []struct {
		where  string
		usage  string
		absent []string
	}{
		{where: "overlay autoupdate --depth", usage: autoupdateCmd.Flags().Lookup("depth").Usage},
		{where: "overlay validate --depth", usage: newValidateCmd().Flags().Lookup("depth").Usage},
		{
			where: "overlay compare --depth",
			usage: compareCmd.Flags().Lookup("depth").Usage,
			// compare reports or builds nothing below patches, so these two must
			// stay out of ITS list even though the other two carry them.
			absent: []string{"none", "options"},
		},
	} {
		t.Run(tt.where, func(t *testing.T) {
			enumeration := depthEnumeration(t, tt.where, tt.usage)

			if !strings.Contains(enumeration, "install") {
				t.Errorf("%s enumerates %q and does not offer install; a capability an operator paid for is not "+
					"one they should have to read the source to find (R5.2)", tt.where, enumeration)
			}
			// Last, because that is where the ladder puts it.
			if !strings.HasSuffix(strings.TrimSpace(enumeration), "install") {
				t.Errorf("%s enumerates %q; install is the DEEPEST rung and the lists are ordered shallowest "+
					"first, so it belongs last", tt.where, enumeration)
			}
			for _, name := range tt.absent {
				if strings.Contains(enumeration, name) {
					t.Errorf("%s enumerates %q, which now offers %q; the three sentences name different subsets "+
						"on purpose and this one widened", tt.where, enumeration, name)
				}
			}
		})
	}
}

// TestCompileFlag_NamesTheDeeperPathItDoesNotTake is S042-R5.3 and R6.2.
//
// The privileged --compile gate keeps its ceiling at src_compile by design D7:
// teaching it a new phase would mean a second prompt, a second sudo invocation
// and a second copy of the repair loop, for a path the protocol wants to
// converge into --depth rather than extend. The consequence is DOCUMENTED here
// rather than left to be discovered by an operator who assumed --compile was
// the deepest thing on offer.
func TestCompileFlag_NamesTheDeeperPathItDoesNotTake(t *testing.T) {
	usage := autoupdateCmd.Flags().Lookup("compile").Usage

	if !strings.Contains(usage, "src_compile") {
		t.Errorf("the --compile usage %q does not say where the privileged gate STOPS; its ceiling is the "+
			"difference between the two paths", usage)
	}
	if !strings.Contains(usage, "--depth=install") {
		t.Errorf("the --compile usage %q does not name --depth=install as the path that goes further; an "+
			"operator who wants src_install validated has no way to learn which flag does it", usage)
	}
}
