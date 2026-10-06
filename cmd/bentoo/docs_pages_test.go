package main

// The README is a front page: what bentoolkit is, how to install, build and
// test it, and a link to each reference page under docs/. The reference
// sections live in those pages, and every relative link and #anchor in the
// documentation resolves.

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// docsMaxREADMEBytes is the README size limit.
const docsMaxREADMEBytes = 16384

// docsPageMap names each reference page and the README sections it holds,
// spelled as the README headings spell them.
var docsPageMap = []struct {
	page     string
	headings []string
}{
	{"docs/configuration.md", []string{"Configuration", "Configuration Options", "Secrets"}},
	{"docs/overlay.md", []string{"Overlay Commands", "Typical Overlay Workflow"}},
	{"docs/distfiles.md", []string{"Distfile Commands", "Distfiles"}},
	{"docs/notices.md", []string{"Notice Commands"}},
	{"docs/autoupdate.md", []string{"Autoupdate System"}},
	{"docs/behaviour.md", []string{
		"Exit codes", "Live output", "Concurrency", "Timeouts",
		"Headers and environment variables", "HTTP/2", "Filesystem assumptions",
	}},
	{"docs/snapshot.md", []string{"Snapshot Management"}},
	{"docs/tray.md", []string{"Desktop Notifications (bentoo-tray)"}},
	{"docs/development.md", []string{"Security Audit", "Project Structure"}},
}

// docsRepoRoot is the repository root seen from this package's directory.
var docsRepoRoot = filepath.Join("..", "..")

type docsHeading struct {
	level int
	text  string
}

var (
	docsFenceRe   = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	docsHeadingRe = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$`)
	docsCodeRe    = regexp.MustCompile("`+[^`\n]*`+")
	docsInlineRe  = regexp.MustCompile(`!?\[[^\]\n]*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	docsRefDefRe  = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:\s+<?([^\s>]+)>?`)
	docsSlugDrop  = regexp.MustCompile(`[^\p{L}\p{N}\p{M}\p{Pc} -]`)
)

// docsProseLines returns the lines of body that are outside fenced code
// blocks. A fence closes only on the same character, at least as long.
func docsProseLines(body string) []string {
	var prose []string
	open := ""
	for _, line := range strings.Split(body, "\n") {
		if m := docsFenceRe.FindStringSubmatch(line); m != nil {
			switch {
			case open == "":
				open = m[1]
				continue
			case m[1][0] == open[0] && len(m[1]) >= len(open) &&
				strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), m[1][:1])) == "":
				open = ""
				continue
			}
		}
		if open == "" {
			prose = append(prose, line)
		}
	}
	return prose
}

// docsHeadings returns the ATX headings of body outside fenced code.
func docsHeadings(body string) []docsHeading {
	var hs []docsHeading
	for _, line := range docsProseLines(body) {
		if m := docsHeadingRe.FindStringSubmatch(line); m != nil {
			hs = append(hs, docsHeading{level: len(m[1]), text: m[2]})
		}
	}
	return hs
}

// docsSlug is GitHub's heading anchor: lowercase, punctuation other than '-'
// and '_' dropped, each space turned into '-'.
func docsSlug(text string) string {
	return strings.ReplaceAll(docsSlugDrop.ReplaceAllString(strings.ToLower(text), ""), " ", "-")
}

// docsAnchors returns every anchor GitHub gives the headings of body. A
// repeated slug gets the next free "-N" suffix, so a heading whose own text
// already ends in "-1" pushes a later duplicate on to "-2".
func docsAnchors(body string) map[string]bool {
	seen := map[string]int{}
	anchors := map[string]bool{}
	for _, h := range docsHeadings(body) {
		base := docsSlug(h.text)
		slug := base
		for anchors[slug] {
			seen[base]++
			slug = fmt.Sprintf("%s-%d", base, seen[base])
		}
		anchors[slug] = true
	}
	return anchors
}

// docsLinkTargets returns the targets of the inline links, images and link
// reference definitions in body, outside fenced code and code spans.
func docsLinkTargets(body string) []string {
	var targets []string
	for _, line := range docsProseLines(body) {
		if m := docsRefDefRe.FindStringSubmatch(line); m != nil {
			targets = append(targets, m[1])
			continue
		}
		for _, m := range docsInlineRe.FindAllStringSubmatch(docsCodeRe.ReplaceAllString(line, ""), -1) {
			targets = append(targets, m[1])
		}
	}
	return targets
}

type docsBrokenLink struct {
	file, target, why string
}

func (b docsBrokenLink) String() string {
	return fmt.Sprintf("%s: link %q: %s", b.file, b.target, b.why)
}

// docsBrokenLinks checks every relative link in the scope files of fsys.
// External (http, https, mailto) links are not followed. An #anchor is
// checked against the headings of the file it points into, for .md targets.
func docsBrokenLinks(fsys fs.FS, scope []string) []docsBrokenLink {
	var broken []docsBrokenLink
	cache := map[string]map[string]bool{}
	for _, src := range scope {
		body, err := fs.ReadFile(fsys, src)
		if err != nil {
			broken = append(broken, docsBrokenLink{src, "", err.Error()})
			continue
		}
		for _, target := range docsLinkTargets(string(body)) {
			lower := strings.ToLower(target)
			if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "mailto:") {
				continue
			}
			file, frag, _ := strings.Cut(target, "#")
			dest := src
			if file != "" {
				dest = path.Clean(path.Join(path.Dir(src), file))
				if dest == ".." || strings.HasPrefix(dest, "../") {
					broken = append(broken, docsBrokenLink{src, target, "points outside the repository"})
					continue
				}
				if _, err := fs.Stat(fsys, dest); err != nil {
					broken = append(broken, docsBrokenLink{src, target, dest + " does not exist"})
					continue
				}
			}
			if frag == "" || !strings.HasSuffix(dest, ".md") {
				continue
			}
			anchors, ok := cache[dest]
			if !ok {
				data, err := fs.ReadFile(fsys, dest)
				if err != nil {
					broken = append(broken, docsBrokenLink{src, target, err.Error()})
					continue
				}
				anchors = docsAnchors(string(data))
				cache[dest] = anchors
			}
			if !anchors[frag] {
				broken = append(broken, docsBrokenLink{src, target, "no heading in " + dest + " has the anchor #" + frag})
			}
		}
	}
	return broken
}

// docsMarkdownPages lists docs/**/*.md under root as slash paths.
func docsMarkdownPages(t *testing.T, root string) []string {
	t.Helper()
	var pages []string
	err := fs.WalkDir(os.DirFS(root), "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			pages = append(pages, p)
		}
		return nil
	})
	if err != nil {
		t.Errorf("listing docs/**/*.md: %v", err)
	}
	sort.Strings(pages)
	return pages
}

func docsHasHeading(hs []docsHeading, text string) bool {
	return slices.ContainsFunc(hs, func(h docsHeading) bool { return h.text == text })
}

func TestDocsREADME_FitsIn16KiB(t *testing.T) {
	info, err := os.Stat(filepath.Join(docsRepoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > docsMaxREADMEBytes {
		t.Errorf("README.md is %d bytes, over the %d-byte limit", info.Size(), docsMaxREADMEBytes)
	}
}

// The README keeps the description, the module list, the install steps, the
// build and test commands, a link to every reference page and the license.
func TestDocsREADME_KeepsTheFrontPage(t *testing.T) {
	readme := readRepoDoc(t, "README.md")
	hs := docsHeadings(readme)
	if len(hs) == 0 || hs[0].level != 1 {
		t.Fatalf("README.md does not open with an H1 title")
	}

	// The description: prose between the title and the next heading.
	prose := docsProseLines(readme)
	var description strings.Builder
	for _, line := range prose[slices.IndexFunc(prose, func(l string) bool { return docsHeadingRe.MatchString(l) })+1:] {
		if docsHeadingRe.MatchString(line) {
			break
		}
		description.WriteString(strings.TrimSpace(line))
	}
	if description.Len() == 0 {
		t.Errorf("README.md has no description between its title and the first section")
	}

	for _, want := range []string{"Modules", "Installation", "License"} {
		if !docsHasHeading(hs, want) {
			t.Errorf("README.md has no %q section", want)
		}
	}

	makefile := readRepoDoc(t, "Makefile")
	for _, target := range []string{"build", "test"} {
		if !strings.Contains(readme, "make "+target) {
			t.Errorf("README.md does not name `make %s`", target)
		}
		if !regexp.MustCompile(`(?m)^` + target + `:`).MatchString(makefile) {
			t.Errorf("the Makefile has no %q target, which the README names", target)
		}
	}

	// A page counts as linked only through a real link: a path in a code
	// block, a code span or plain text is not one.
	linked := map[string]bool{}
	for _, target := range docsLinkTargets(readme) {
		file, _, _ := strings.Cut(target, "#")
		linked[path.Clean(file)] = true
	}
	for _, entry := range docsPageMap {
		if !linked[entry.page] {
			t.Errorf("README.md has no link to %s", entry.page)
		}
	}
}

// Each reference page exists, has one title, and holds the README sections
// the page map gives it, which have left the README.
func TestDocsPages_FollowThePageMap(t *testing.T) {
	t.Run("headings in fenced code do not count", func(t *testing.T) {
		body := "# Title\n```bash\n# a shell comment\n```\n````md\n```\n# inside a four-backtick fence\n```\n````\n~~~\n# tilde fence\n~~~\n## Real\n"
		hs := docsHeadings(body)
		want := []docsHeading{{1, "Title"}, {2, "Real"}}
		if !slices.Equal(hs, want) {
			t.Errorf("docsHeadings = %v, want %v", hs, want)
		}
	})

	readmeHeadings := docsHeadings(readRepoDoc(t, "README.md"))
	for _, entry := range docsPageMap {
		data, err := os.ReadFile(filepath.Join(docsRepoRoot, filepath.FromSlash(entry.page)))
		if err != nil {
			t.Errorf("%s: %v", entry.page, err)
			continue
		}
		hs := docsHeadings(string(data))
		h1 := 0
		for _, h := range hs {
			if h.level == 1 {
				h1++
			}
		}
		if h1 != 1 {
			t.Errorf("%s has %d H1 headings, want exactly one", entry.page, h1)
		}
		for _, section := range entry.headings {
			// Exact text: "Configuration" is not satisfied by
			// "Configuration Options" or "Configuration (`snapshot.toml`)".
			if !docsHasHeading(hs, section) {
				t.Errorf("%s has no %q heading", entry.page, section)
			}
			if docsHasHeading(readmeHeadings, section) {
				t.Errorf("README.md still holds the %q section, which moves to %s", section, entry.page)
			}
		}
	}
}

// Every relative link and #anchor in README.md, CHANGELOG.md and
// docs/**/*.md resolves; the reference pages must exist for the check to
// cover them.
func TestDocsLinks_Resolve(t *testing.T) {
	type checkerCase struct {
		name       string
		files      map[string]string
		scope      []string
		wantBroken []string // targets that must be reported, in order
	}
	cases := []checkerCase{
		{
			name: "an anchor resolves only in the file it points into",
			files: map[string]string{
				"README.md": "# T\n[s](docs/a.md#secrets)\n",
				"docs/a.md": "# A\n",
				"docs/b.md": "# B\n## Secrets\n",
			},
			scope:      []string{"README.md"},
			wantBroken: []string{"docs/a.md#secrets"},
		},
		{
			name: "a heading inside fenced code is not an anchor",
			files: map[string]string{
				"README.md": "# T\n```bash\n# Whole overlay\n```\n[w](#whole-overlay)\n",
			},
			scope:      []string{"README.md"},
			wantBroken: []string{"#whole-overlay"},
		},
		{
			name: "a missing file, a near-name file, a reference definition and an escape are reported",
			files: map[string]string{
				"README.md": "# T\n[m](docs/missing.md) [n](docs/a.md.bak) [up](../outside.md)\n\n[ref]: docs/gone.md\n",
				"docs/a.md": "# A\n",
			},
			scope:      []string{"README.md"},
			wantBroken: []string{"docs/missing.md", "docs/a.md.bak", "../outside.md", "docs/gone.md"},
		},
		{
			name: "a third heading takes the next free suffix",
			files: map[string]string{
				"docs/a.md": "# A\n## Commands\n## Commands 1\n## Commands\n" +
					"[1](#commands) [2](#commands-1) [3](#commands-2) [4](#commands-3)\n",
			},
			scope:      []string{"docs/a.md"},
			wantBroken: []string{"#commands-3"},
		},
		{
			name: "punctuation, relative paths and closing hashes resolve",
			files: map[string]string{
				"README.md": "# Readme top\n",
				"docs/a.md": "# A\n" +
					"## Several release lines of one package (`series`)\n" +
					"## Cloud backup & restore\n## HTTP/2\n## Desktop Notifications (bentoo-tray)\n## Closed ##\n" +
					"[1](#several-release-lines-of-one-package-series) [2](#cloud-backup--restore) [3](#http2)\n" +
					"[4](#desktop-notifications-bentoo-tray) [5](#closed) [6](../README.md#readme-top)\n" +
					"[7](./b.md#secrets) [8](b.md) ![img](b.md \"title\")\n",
				"docs/b.md": "# B\n### Secrets\n",
			},
			scope: []string{"docs/a.md"},
		},
		{
			name: "external links and links in code are not followed",
			files: map[string]string{
				"README.md": "# T\n[h](https://example.com/x.md#nope) [m](mailto:a@b.c) `[c](missing.md)`\n" +
					"```\n[f](missing-too.md)\n```\n[same](#t)\n",
			},
			scope: []string{"README.md"},
		},
	}
	for _, c := range cases {
		t.Run("checker/"+c.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for name, body := range c.files {
				fsys[name] = &fstest.MapFile{Data: []byte(body)}
			}
			var got []string
			for _, b := range docsBrokenLinks(fsys, c.scope) {
				got = append(got, b.target)
			}
			if !slices.Equal(got, c.wantBroken) {
				t.Errorf("broken links = %q, want %q", got, c.wantBroken)
			}
		})
	}

	scope := append([]string{"README.md", "CHANGELOG.md"}, docsMarkdownPages(t, docsRepoRoot)...)
	for _, entry := range docsPageMap {
		if !slices.Contains(scope, entry.page) {
			t.Errorf("%s does not exist, so its links are not checked", entry.page)
		}
	}
	for _, b := range docsBrokenLinks(os.DirFS(docsRepoRoot), scope) {
		t.Error(b)
	}
}
