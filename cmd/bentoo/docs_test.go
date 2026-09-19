package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// The README is the only place the gated-distfile flow is written down for
// somebody who is not reading the source: what the command is called, where it
// writes, and the one schema rule that is easy to get wrong (the serial keys go
// in pairs). An ebuild's pkg_nofetch points here, so a rename that leaves this
// section behind sends the user to instructions for a command that moved.
func TestREADME_DocumentsGatedDistfileFetch(t *testing.T) {
	readme := readRepoDoc(t, "README.md")
	requireContains(t, "README.md", readme,
		"#### Fetch a gated distfile",
		"bentoo distfile fetch",
		"--version",
		"--distdir",
		"portageq distdir",
		"`fetch_serial_env`",
		"`fetch_serial_field`",
		"one half alone is an error",
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

// depthEnumeration returns just the list of rung names out of a surface that
// enumerates the ladder: the run of text between the phrase that INTRODUCES the
// list and the phrase that CLOSES it, both supplied by the caller.
//
// Matching against this slice rather than against the whole text is the entire
// point. A document-wide search for "install" passes on any unrelated
// occurrence — "src_install", "installed", a sentence about --depth=install
// elsewhere in the same help — and story 041 found exactly that false guard.
//
// Both delimiters are per-surface and MANDATORY. They differ — a flag usage
// introduces its list with an em dash, the validate command's Long text
// introduces it with "ladder to go: " and closes it with a different phrase —
// and a delimiter that silently failed to match would hand the whole document
// back and restore the very false guard this function exists to prevent. A miss
// is a t.Fatalf, never a fallback.
func depthEnumeration(t *testing.T, where, text, intro, tail string) string {
	t.Helper()

	end := strings.Index(text, tail)
	if end < 0 {
		t.Fatalf("%s: the text does not carry %q, so its enumeration cannot be isolated and every "+
			"assertion below would be searching the whole document:\n%s", where, tail, text)
	}
	head := text[:end]

	start := strings.LastIndex(head, intro)
	if start < 0 {
		t.Fatalf("%s: the enumeration is not introduced by %q, so it cannot be isolated and every "+
			"assertion below would be searching the whole document:\n%s", where, intro, text)
	}
	return head[start+len(intro):]
}

// TestDepthFlags_EveryEnumerationNamesTheInstallRung is S042-R5.1 and R5.2.
//
// The enumerations are deliberately NOT unified. Each names a different subset
// for a reason: compare's path builds only what a realignment proposes and
// reports nothing at all below patches, so collapsing the sentences would widen
// compare's documented surface as a side effect.
//
// # A flag usage is not the only surface an operator reads
//
// The fourth case is `overlay validate`'s Long text, and it is here because it
// was MISSED. Story 042 updated the three flag usages, and this test guarded
// exactly those three — so a fourth enumeration inside the SAME --help output
// went on telling the operator the ladder stopped at compile, five lines above
// the flag that said otherwise. A contradiction, not an omission, and the guard
// could not see it because it read Flags().Lookup("depth").Usage and nothing
// else.
//
// Any surface that spells the rungs belongs in this table. A guard scoped to
// one KIND of surface is a guard the next rung walks around.
func TestDepthFlags_EveryEnumerationNamesTheInstallRung(t *testing.T) {
	const (
		dash     = "— "
		flagTail = ", each including every rung before it"
	)

	for _, tt := range []struct {
		where  string
		text   string
		intro  string
		tail   string
		absent []string
	}{
		{
			where: "overlay autoupdate --depth",
			text:  autoupdateCmd.Flags().Lookup("depth").Usage,
			intro: dash, tail: flagTail,
		},
		{
			where: "overlay validate --depth",
			text:  newValidateCmd().Flags().Lookup("depth").Usage,
			intro: dash, tail: flagTail,
		},
		{
			where: "overlay compare --depth",
			text:  compareCmd.Flags().Lookup("depth").Usage,
			intro: dash, tail: flagTail,
			// compare reports or builds nothing below patches, so these two must
			// stay out of ITS list even though the other two carry them.
			absent: []string{"none", "options"},
		},
		{
			where: "overlay validate --help (Long)",
			text:  newValidateCmd().Long,
			// The Long carries an em dash of its own, paragraphs earlier, so it
			// names the phrase that actually introduces ITS list — and its closing
			// phrase differs from the flags' by one word.
			intro: "ladder to go: ",
			tail:  ", each rung including every rung before it",
		},
	} {
		t.Run(tt.where, func(t *testing.T) {
			enumeration := depthEnumeration(t, tt.where, tt.text, tt.intro, tt.tail)

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

// changelogVersionHeading matches `## [0.29.0] - 2026-09-06`, and deliberately
// not `## [Unreleased]`: the two are checked against each other below, so
// conflating them here would make the check circular.
var (
	changelogVersionHeading = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`)
	changelogLinkRef        = regexp.MustCompile(`(?m)^\[([^\]]+)\]: (\S+)$`)
)

// TestCHANGELOG_LinkRefsFollowTheVersions is the invariant a release cut
// maintains BY HAND, and that nothing checked.
//
// Cutting a version edits two places that must agree and sit 4800 lines apart:
// a heading goes in at the top, and at the foot the [Unreleased] link must be
// re-pointed at the new tag while the new version gets a compare range of its
// own. Nothing failed if you forgot. The existing CHANGELOG test reads the
// `## [0.2.0]` section and no other, which story 047's own register recorded as
// a limit: its gate confirms an edit broke nothing rather than that the entry is
// right.
//
// # What is checked, and what deliberately is not
//
// The CONTENT of an entry is editorial and no test should have an opinion about
// it. What is mechanical is the correspondence: every version heading has a
// reference, every reference has a heading, and each range spans from the
// version below it to itself. Those are the parts a human retypes and a diff
// makes hard to see.
//
// The oldest version is the one exception and is asserted as one: it has no
// predecessor to compare against, so it points at the release tag instead.
func TestCHANGELOG_LinkRefsFollowTheVersions(t *testing.T) {
	changelog := readRepoDoc(t, "CHANGELOG.md")

	var versions []string
	for _, m := range changelogVersionHeading.FindAllStringSubmatch(changelog, -1) {
		versions = append(versions, m[1])
	}
	if len(versions) < 2 {
		t.Fatalf("found %d version headings in CHANGELOG.md; the parse found the wrong thing", len(versions))
	}

	refs := map[string]string{}
	for _, m := range changelogLinkRef.FindAllStringSubmatch(changelog, -1) {
		refs[m[1]] = m[2]
	}

	// The newest heading is the one [Unreleased] must compare from. Re-pointing
	// it is the step a cut is most likely to leave behind, because the section
	// it belongs to is empty and looks finished either way.
	newest := versions[0]
	wantUnreleased := fmt.Sprintf("https://github.com/obentoo/bentoolkit/compare/v%s...HEAD", newest)
	if got := refs["Unreleased"]; got != wantUnreleased {
		t.Errorf("[Unreleased] compares from the wrong point.\n got: %s\nwant: %s\nThe newest version in the file is %s, "+
			"so anything else makes the Unreleased link show changes that are already released.", got, wantUnreleased, newest)
	}

	for i, version := range versions {
		got, ok := refs[version]
		if !ok {
			t.Errorf("## [%s] has no link reference at the foot of the file, so the heading renders as literal brackets", version)
			continue
		}

		// The oldest version FIRST, because there is no versions[i+1] to read
		// for it — computing the compare range before testing for it is an index
		// past the end, which is how this guard failed the first time it ran.
		var want string
		if i == len(versions)-1 {
			want = fmt.Sprintf("https://github.com/obentoo/bentoolkit/releases/tag/v%s", version)
		} else {
			want = fmt.Sprintf("https://github.com/obentoo/bentoolkit/compare/v%s...v%s", versions[i+1], version)
		}
		if got != want {
			t.Errorf("[%s] does not span from the version below it.\n got: %s\nwant: %s", version, got, want)
		}
	}

	// The other direction: a reference nothing points at is a version that was
	// renamed or removed and left half-deleted.
	for name := range refs {
		if name == "Unreleased" {
			continue
		}
		if !slicesContains(versions, name) {
			t.Errorf("[%s] is referenced at the foot of the file but has no `## [%s]` heading", name, name)
		}
	}
}

// slicesContains is spelled here rather than imported so this file keeps the
// import set it had.
func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
