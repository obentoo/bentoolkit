package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The CHANGELOG is split in two: CHANGELOG.md keeps [Unreleased] and every
// release from 0.30.0 on, and the archive below holds 0.1.0 through 0.29.1,
// moved byte for byte, with their link references at its own foot.
const (
	changelogFile        = "CHANGELOG.md"
	changelogArchiveFile = "docs/changelog/0.1.0-0.29.1.md"

	// changelogArchivedCount is how many releases 0.1.0 through 0.29.1 are.
	// Those releases are history: the number can never change.
	changelogArchivedCount = 78

	// changelogArchivedBodySHA256 is the SHA-256 of the archived release
	// sections exactly as they stood in CHANGELOG.md before the split: the text
	// from the `## [0.29.1]` heading up to the first link reference after it,
	// with surrounding whitespace trimmed. Moving the sections unrewritten keeps
	// this hash; editing a single byte of them does not.
	changelogArchivedBodySHA256 = "150690347031c6fa5ee9bff9bcfcdd1e49286acb46d48642be89c379f086367d"

	changelogRepoURL = "https://github.com/obentoo/bentoolkit/"
)

var (
	changelogSplitHeading = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`)
	changelogSplitLinkRef = regexp.MustCompile(`(?m)^\[([^\]]+)\]: (\S+)$`)
	changelogVersionName  = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	changelogArchiveLink  = regexp.MustCompile(`(?m)` +
		`\]\((?:\./)?docs/changelog/0\.1\.0-0\.29\.1\.md(?:#[^)\s]*)?\)` +
		`|^\[[^\]]+\]:[ \t]+(?:\./)?docs/changelog/0\.1\.0-0\.29\.1\.md(?:#\S*)?[ \t]*$`)
)

// changelogDoc is one file of the split CHANGELOG, newest file first.
type changelogDoc struct {
	name string
	text string
}

// changelogVersions returns the version headings of text, top to bottom.
func changelogVersions(text string) []string {
	var versions []string
	for _, m := range changelogSplitHeading.FindAllStringSubmatch(text, -1) {
		versions = append(versions, m[1])
	}
	return versions
}

// changelogSemver parses "X.Y.Z"; it fails the test on anything else.
func changelogSemver(t *testing.T, v string) [3]int {
	t.Helper()
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		t.Fatalf("version %q is not X.Y.Z", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("version %q: %v", v, err)
		}
		out[i] = n
	}
	return out
}

// changelogLess reports whether version a sorts before version b.
func changelogLess(t *testing.T, a, b string) bool {
	t.Helper()
	x, y := changelogSemver(t, a), changelogSemver(t, b)
	for i := range x {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

// changelogLinkRefProblems checks every version heading against the link
// references of the file that holds it. The files read as one chain, newest
// first: each release compares from the release just below it — in the same
// file or, for a file's oldest release, the newest release of the next file —
// and the oldest release of all points at its tag. [Unreleased] belongs to the
// first file only. A reference that names a version, or points at a compare or
// tag URL of the repository, must have a heading in its own file.
func changelogLinkRefProblems(t *testing.T, docs []changelogDoc) []string {
	t.Helper()
	var problems []string
	type held struct {
		version string
		doc     int
	}
	var chain []held
	holder := map[string]string{}
	refs := make([]map[string]string, len(docs))
	for d, doc := range docs {
		refs[d] = map[string]string{}
		for _, m := range changelogSplitLinkRef.FindAllStringSubmatch(doc.text, -1) {
			refs[d][m[1]] = m[2]
		}
		for _, v := range changelogVersions(doc.text) {
			if prev, ok := holder[v]; ok {
				problems = append(problems, fmt.Sprintf("## [%s] is in both %s and %s; a release lives in exactly one file", v, prev, doc.name))
				continue
			}
			holder[v] = doc.name
			chain = append(chain, held{v, d})
		}
	}
	if len(chain) == 0 {
		return append(problems, "no version headings found in any file; the parse found the wrong thing")
	}

	for i := 1; i < len(chain); i++ {
		if !changelogLess(t, chain[i].version, chain[i-1].version) {
			problems = append(problems, fmt.Sprintf("## [%s] (%s) sits below ## [%s] (%s) but is not older; the files must read newest first",
				chain[i].version, docs[chain[i].doc].name, chain[i-1].version, docs[chain[i-1].doc].name))
		}
	}

	wantUnreleased := fmt.Sprintf("%scompare/v%s...HEAD", changelogRepoURL, chain[0].version)
	if got := refs[0]["Unreleased"]; got != wantUnreleased {
		problems = append(problems, fmt.Sprintf("%s: [Unreleased] compares from the wrong point.\n got: %s\nwant: %s", docs[0].name, got, wantUnreleased))
	}
	for d := 1; d < len(docs); d++ {
		if _, ok := refs[d]["Unreleased"]; ok {
			problems = append(problems, fmt.Sprintf("%s carries an [Unreleased] reference; only %s may", docs[d].name, docs[0].name))
		}
		if strings.Contains(docs[d].text, "## [Unreleased]") {
			problems = append(problems, fmt.Sprintf("%s carries an ## [Unreleased] heading; only %s may", docs[d].name, docs[0].name))
		}
	}

	for i, h := range chain {
		doc := docs[h.doc]
		got, ok := refs[h.doc][h.version]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: ## [%s] has no link reference at the foot of the file that holds it", doc.name, h.version))
			continue
		}
		var want string
		if i == len(chain)-1 {
			want = fmt.Sprintf("%sreleases/tag/v%s", changelogRepoURL, h.version)
		} else {
			want = fmt.Sprintf("%scompare/v%s...v%s", changelogRepoURL, chain[i+1].version, h.version)
		}
		if got != want {
			problems = append(problems, fmt.Sprintf("%s: [%s] does not span from the release below it.\n got: %s\nwant: %s", doc.name, h.version, got, want))
		}
	}

	for d, doc := range docs {
		own := map[string]bool{}
		for _, v := range changelogVersions(doc.text) {
			own[v] = true
		}
		for name, url := range refs[d] {
			if name == "Unreleased" {
				continue
			}
			versionish := changelogVersionName.MatchString(name) ||
				strings.HasPrefix(url, changelogRepoURL+"compare/") ||
				strings.HasPrefix(url, changelogRepoURL+"releases/tag/")
			if versionish && !own[name] {
				problems = append(problems, fmt.Sprintf("%s: [%s] is referenced at its foot but the file has no `## [%s]` heading", doc.name, name, name))
			}
		}
	}
	return problems
}

func readChangelogDocs(t *testing.T) []changelogDoc {
	t.Helper()
	return []changelogDoc{
		{changelogFile, readRepoDoc(t, changelogFile)},
		{changelogArchiveFile, readRepoDoc(t, changelogArchiveFile)},
	}
}

// TestCHANGELOG_LinkRefsFollowTheVersions checks every version heading of
// CHANGELOG.md and of the archive against the link references of the file
// that holds it. The synthetic cases come first: each is a mistake the split
// invites, and the checker must catch it before its verdict on the real files
// means anything.
func TestCHANGELOG_LinkRefsFollowTheVersions(t *testing.T) {
	const u = changelogRepoURL
	head := "# Changelog\n\n## [Unreleased]\n\n## [0.3.0] - x\n\n"
	headRefs := "[Unreleased]: " + u + "compare/v0.3.0...HEAD\n[0.3.0]: " + u + "compare/v0.2.0...v0.3.0\n"
	arch := "# Archive\n\n## [0.2.0] - x\n\n## [0.1.0] - x\n\n"
	archRefs := "[0.2.0]: " + u + "compare/v0.1.0...v0.2.0\n[0.1.0]: " + u + "releases/tag/v0.1.0\n"

	bad := []struct {
		name string
		docs []changelogDoc
	}{
		{"a moved release's reference left behind in the first file", []changelogDoc{
			{"new", head + headRefs + "[0.2.0]: " + u + "compare/v0.1.0...v0.2.0\n"},
			{"old", arch + "[0.1.0]: " + u + "releases/tag/v0.1.0\n"},
		}},
		{"a release kept in both files", []changelogDoc{
			{"new", head + "## [0.2.0] - x\n\n" + headRefs + "[0.2.0]: " + u + "compare/v0.1.0...v0.2.0\n"},
			{"old", arch + archRefs},
		}},
		{"the first file's oldest release treated as the oldest of all", []changelogDoc{
			{"new", head + "[Unreleased]: " + u + "compare/v0.3.0...HEAD\n[0.3.0]: " + u + "releases/tag/v0.3.0\n"},
			{"old", arch + archRefs},
		}},
		{"a version-named reference pointing at the archive with no heading of its own", []changelogDoc{
			{"new", head + headRefs + "[0.2.0]: docs/changelog/0.1.0-0.29.1.md\n"},
			{"old", arch + archRefs},
		}},
		{"an [Unreleased] reference in the archive", []changelogDoc{
			{"new", head + headRefs},
			{"old", arch + archRefs + "[Unreleased]: " + u + "compare/v0.2.0...HEAD\n"},
		}},
		{"a newer release in the archive than in the first file", []changelogDoc{
			{"new", "## [Unreleased]\n\n## [0.2.0] - x\n\n[Unreleased]: " + u + "compare/v0.2.0...HEAD\n[0.2.0]: " + u + "compare/v0.3.0...v0.2.0\n"},
			{"old", "## [0.3.0] - x\n\n## [0.1.0] - x\n\n[0.3.0]: " + u + "compare/v0.1.0...v0.3.0\n[0.1.0]: " + u + "releases/tag/v0.1.0\n"},
		}},
	}
	for _, c := range bad {
		t.Run("rejects "+c.name, func(t *testing.T) {
			if p := changelogLinkRefProblems(t, c.docs); len(p) == 0 {
				t.Errorf("the checker accepted %s", c.name)
			}
		})
	}

	t.Run("accepts a compare range that crosses from one file to the next", func(t *testing.T) {
		docs := []changelogDoc{{"new", head + headRefs + "[archive]: docs/changelog/0.1.0-0.29.1.md\n"}, {"old", arch + archRefs}}
		if p := changelogLinkRefProblems(t, docs); len(p) != 0 {
			t.Errorf("the checker rejected a correct split:\n%s", strings.Join(p, "\n"))
		}
	})

	t.Run("the repository's CHANGELOG and its archive", func(t *testing.T) {
		for _, p := range changelogLinkRefProblems(t, readChangelogDocs(t)) {
			t.Error(p)
		}
	})
}

// TestCHANGELOG_HasV020 reads the release 0.2.0 section from the archive,
// where it lives after the split, and nowhere else.
func TestCHANGELOG_HasV020(t *testing.T) {
	archive := readRepoDoc(t, changelogArchiveFile)
	requireContains(t, changelogArchiveFile, archive,
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
	if strings.Contains(readRepoDoc(t, changelogFile), "## [0.2.0]") {
		t.Errorf("%s still carries ## [0.2.0]; it belongs to %s only", changelogFile, changelogArchiveFile)
	}
}

// TestCHANGELOG_KeepsUnreleasedAndReleasesFrom030On checks what stays in
// CHANGELOG.md: the [Unreleased] section and every release from 0.30.0 on, and
// no release older than that.
func TestCHANGELOG_KeepsUnreleasedAndReleasesFrom030On(t *testing.T) {
	changelog := readRepoDoc(t, changelogFile)
	versions := changelogVersions(changelog)
	for _, v := range versions {
		if changelogLess(t, v, "0.30.0") {
			t.Errorf("%s carries ## [%s]; releases before 0.30.0 belong to %s", changelogFile, v, changelogArchiveFile)
		}
	}
	if !regexp.MustCompile(`(?m)^## \[Unreleased\]`).MatchString(changelog) {
		t.Errorf("%s lost its ## [Unreleased] section", changelogFile)
	}
	for _, kept := range []string{"0.30.0", "0.30.1", "0.30.2", "0.30.3", "0.31.0", "0.31.1", "0.32.0"} {
		if !slicesContainsString(versions, kept) {
			t.Errorf("%s lost ## [%s]; every release from 0.30.0 on stays in it", changelogFile, kept)
		}
	}
}

// TestCHANGELOG_ArchiveHoldsOldReleasesUnrewritten checks the archive holds
// exactly the releases 0.1.0 through 0.29.1, byte for byte as they were.
func TestCHANGELOG_ArchiveHoldsOldReleasesUnrewritten(t *testing.T) {
	archive := readRepoDoc(t, changelogArchiveFile)
	versions := changelogVersions(archive)
	for _, v := range versions {
		if changelogLess(t, "0.29.1", v) {
			t.Errorf("%s carries ## [%s]; only releases up to 0.29.1 belong in it", changelogArchiveFile, v)
		}
	}
	if len(versions) != changelogArchivedCount {
		t.Errorf("%s holds %d releases, want %d (0.1.0 through 0.29.1)", changelogArchiveFile, len(versions), changelogArchivedCount)
	}
	if len(versions) == 0 || versions[0] != "0.29.1" || versions[len(versions)-1] != "0.1.0" {
		t.Fatalf("%s must run from ## [0.29.1] down to ## [0.1.0]; got headings %v", changelogArchiveFile, versions)
	}

	start := strings.Index(archive, "## [0.29.1]")
	foot := changelogSplitLinkRef.FindStringIndex(archive[start:])
	if foot == nil {
		t.Fatalf("%s has no link references after ## [0.29.1]; they belong at its foot", changelogArchiveFile)
	}
	body := strings.TrimSpace(archive[start : start+foot[0]])
	sum := sha256.Sum256([]byte(body))
	if got := hex.EncodeToString(sum[:]); got != changelogArchivedBodySHA256 {
		t.Errorf("the release sections of %s differ from the ones CHANGELOG.md held (%d bytes, sha256 %s, want %s). "+
			"They move unrewritten; any note of the archive's own goes above ## [0.29.1].",
			changelogArchiveFile, len(body), got, changelogArchivedBodySHA256)
	}
}

// changelogLinksArchive reports whether text carries a Markdown link to the
// archive, inline or by reference. A bare mention of the path is not a link.
func changelogLinksArchive(text string) bool {
	return changelogArchiveLink.MatchString(text)
}

// TestCHANGELOG_LinksTheArchive checks CHANGELOG.md links the archive file.
func TestCHANGELOG_LinksTheArchive(t *testing.T) {
	t.Run("a bare path is not a link", func(t *testing.T) {
		if changelogLinksArchive("Older releases are in docs/changelog/0.1.0-0.29.1.md.\n") {
			t.Error("a plain-text path was taken for a link")
		}
		if changelogLinksArchive("See [the archive](docs/changelog/0.1.0-0.29.10.md).\n") {
			t.Error("a link to a different file was taken for the archive link")
		}
	})
	t.Run("inline and reference links count", func(t *testing.T) {
		for _, text := range []string{
			"See [the archive](docs/changelog/0.1.0-0.29.1.md).\n",
			"See [the archive][old].\n\n[old]: ./docs/changelog/0.1.0-0.29.1.md\n",
		} {
			if !changelogLinksArchive(text) {
				t.Errorf("not recognised as a link to the archive: %q", text)
			}
		}
	})
	t.Run("the repository's CHANGELOG", func(t *testing.T) {
		readRepoDoc(t, changelogArchiveFile) // the link must resolve
		if !changelogLinksArchive(readRepoDoc(t, changelogFile)) {
			t.Errorf("%s has no Markdown link to %s", changelogFile, changelogArchiveFile)
		}
	})
}

// slicesContainsString reports whether haystack holds needle.
func slicesContainsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
