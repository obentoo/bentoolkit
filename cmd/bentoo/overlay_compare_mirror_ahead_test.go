package main

// Authored for story 103, sub-task 3.1 (R2, R2.1; A1).
//
// Written from the contract, not from an implementation: story.md R2 ("an
// overlay ebuild whose version ::gentoo does not carry, for a package ::gentoo
// does carry, while its KEYWORDS holds anything other than ~amd64 or its text
// carries a "Mirrored from ::gentoo" header" gets a finding on that package's
// row) and R2.1 (the finding names the version and which condition it met, in
// the terminal report AND in the JSON payload, and changes neither the verdict,
// nor the schema version, nor the exit status); A1 (versions compared without
// the -rN suffix).
//
// It drives the REAL command, `bentoo overlay compare --no-review --export`,
// over a real overlay and a `provider: local` ::gentoo tree. The JSON export is
// the payload; the terminal is stdout. A finding is located in the payload by
// what it says (it names the version and the word "mirror"), never by a kind
// name the design has not fixed yet.
//
// Hostile halves first. "A version ::gentoo carries" is an identity rule:
//   - wrongly collapse: ::gentoo carries 1.10 (not 1.1); ::gentoo carries a
//     like-named package (dev-libs/near-utils, not dev-libs/near); KEYWORDS
//     "~amd64 ~arm64" is not "~amd64";
//   - wrongly split: our 1.0-r1 against ::gentoo's 1.0, and our 1.0 against
//     ::gentoo's 1.0-r2, are the same version — neither is ahead;
//   - a third element: a package whose AHEAD ebuild is not its newest (1.1
//     between ::gentoo's 1.0 and 1.2) is flagged at 1.1, and its 1.2 is not.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	s103Stable = `KEYWORDS="amd64 arm64 x86"`
	s103Header = "# Mirrored from ::gentoo only to add python3_15."
)

// s103Ebuild builds an ebuild with the given KEYWORDS line and, when header is
// set, the mirror header.
func s103Ebuild(keywords string, header bool) string {
	var b strings.Builder
	b.WriteString("# Copyright 1999-2026 Gentoo Authors\n# Distributed under the terms of the GNU General Public License v2\n\n")
	if header {
		b.WriteString(s103Header + "\n")
	}
	b.WriteString("EAPI=8\n\nDESCRIPTION=\"compare fixture\"\nHOMEPAGE=\"https://example.com\"\nLICENSE=\"MIT\"\nSLOT=\"0\"\n")
	b.WriteString(keywords + "\n")
	return b.String()
}

// s103Pkg is one package: our ebuilds by version, and ::gentoo's.
type s103Pkg struct {
	atom   string
	ours   map[string]string // version -> ebuild text
	gentoo []string          // versions ::gentoo carries (same text as ours when ours has it)
}

func s103ComparePackages() []s103Pkg {
	plain := s103Ebuild(`KEYWORDS="~amd64"`, false)
	mirror := s103Ebuild(s103Stable, true)
	return []s103Pkg{
		// Flagged.
		{atom: "dev-libs/kwahead", ours: map[string]string{"1.1": s103Ebuild(`KEYWORDS="amd64 ~arm64"`, false)}, gentoo: []string{"1.0"}},
		{atom: "dev-libs/hdrahead", ours: map[string]string{"1.1": s103Ebuild(`KEYWORDS="~amd64"`, true)}, gentoo: []string{"1.0"}},
		{atom: "dev-libs/tildes", ours: map[string]string{"1.1": s103Ebuild(`KEYWORDS="~amd64 ~arm64"`, false)}, gentoo: []string{"1.0"}},
		{atom: "dev-libs/prefix", ours: map[string]string{"1.1": mirror}, gentoo: []string{"1.0", "1.10"}},
		{atom: "dev-libs/between", ours: map[string]string{"1.1": mirror, "1.2": mirror}, gentoo: []string{"1.0", "1.2"}},
		// Not flagged.
		{atom: "dev-libs/converted", ours: map[string]string{"1.1": plain}, gentoo: []string{"1.0"}},
		{atom: "dev-libs/revision", ours: map[string]string{"1.0-r1": mirror}, gentoo: []string{"1.0"}},
		{atom: "dev-libs/gentoorev", ours: map[string]string{"1.0": mirror}, gentoo: []string{"1.0-r2"}},
		{atom: "dev-libs/samever", ours: map[string]string{"1.0": mirror}, gentoo: []string{"1.0"}},
		{atom: "dev-libs/near", ours: map[string]string{"1.1": mirror}, gentoo: nil},
		{atom: "app-misc/ours", ours: map[string]string{"1.1": mirror}, gentoo: nil},
	}
}

// s103CompareWorld writes the overlay and the ::gentoo tree, and names the tree
// as `provider: local` in the harness config.
func s103CompareWorld(t *testing.T) *testCLI {
	t.Helper()
	c := newTestCLI(t)
	gentoo := filepath.Join(c.Home(), "gentoo")
	for _, p := range s103ComparePackages() {
		cat, pn, _ := strings.Cut(p.atom, "/")
		for v, text := range p.ours {
			realignWriteEbuild(t, c.Overlay(), cat, pn, v, text)
		}
		for _, v := range p.gentoo {
			text, ok := p.ours[v]
			if !ok {
				text = s103Ebuild(s103Stable, false) // ::gentoo's own ebuild
			}
			realignWriteEbuild(t, gentoo, cat, pn, v, text)
		}
	}
	// A like-named package ::gentoo carries; dev-libs/near is not it.
	realignWriteEbuild(t, gentoo, "dev-libs", "near-utils", "1.1", s103Ebuild(s103Stable, false))

	profiles := filepath.Join(gentoo, "profiles")
	if err := os.MkdirAll(profiles, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profiles, "repo_name"), []byte("gentoo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	appendHarnessConfig(t, c, "repositories:\n  gentoo:\n    provider: local\n    path: "+gentoo+"\n")
	t.Setenv("PATH", t.TempDir()) // no `claude`: the run stays offline and deterministic
	return c
}

// s103Row is what the JSON payload says about one package.
type s103Row struct {
	lists    map[string]bool // the payload lists the row appears in (its verdict)
	findings []string        // reason + further_findings
}

// s103Rows decodes the export and collects, per package, the lists it sits in
// and every sentence on its row.
func s103Rows(t *testing.T, data []byte) (schema float64, rows map[string]*s103Row) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the JSON export did not parse: %v\n%s", err, data)
	}
	schema, _ = doc["schema"].(float64)
	rows = map[string]*s103Row{}
	var walk func(key string, v any)
	walk = func(key string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if pkg, ok := x["package"].(string); ok {
				r := rows[pkg]
				if r == nil {
					r = &s103Row{lists: map[string]bool{}}
					rows[pkg] = r
				}
				r.lists[key] = true
				if s, ok := x["reason"].(string); ok && s != "" {
					r.findings = append(r.findings, s)
				}
				if ff, ok := x["further_findings"].([]any); ok {
					for _, f := range ff {
						if s, ok := f.(string); ok {
							r.findings = append(r.findings, s)
						}
					}
				}
			}
			for k, child := range x {
				walk(k, child)
			}
		case []any:
			for _, child := range x {
				walk(key, child)
			}
		}
	}
	walk("", doc["payload"])
	return schema, rows
}

// s103NamesVersion: does s name version v as a whole version (1.1, not 1.10)?
func s103NamesVersion(s, v string) bool {
	return regexp.MustCompile(`(?:^|[^\w.])` + regexp.QuoteMeta(v) + `(?:$|[^\w.]|\.$|\.\s)`).MatchString(s)
}

// s103MirrorFindings is every sentence on the row that is about being shaped as
// a mirror.
func s103MirrorFindings(r *s103Row) []string {
	if r == nil {
		return nil
	}
	var out []string
	for _, f := range r.findings {
		if strings.Contains(strings.ToLower(f), "mirror") {
			out = append(out, f)
		}
	}
	return out
}

// s103LineNamesAtom: does line name atom as a whole package, not as the prefix
// of a longer name? A version may follow it ("dev-libs/near-1.1").
func s103LineNamesAtom(line, atom string) bool {
	return regexp.MustCompile(`(?:^|[^\w/+.-])` + regexp.QuoteMeta(atom) + `(?:-\d|[^\w+-]|$)`).MatchString(line)
}

func s103Flatten(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestCompareFlagsCopiesAheadOfGentooStillShapedAsMirrors(t *testing.T) {
	c := s103CompareWorld(t)
	export := filepath.Join(t.TempDir(), "compare.json")

	stdout, stderr, code := c.Run("overlay", "compare", "--no-review", "--export", export)
	if compareReportBody(stdout) == "" {
		t.Fatalf("precondition: the run produced no report\nexit %d\nstderr:\n%s\nstdout:\n%s", code, stderr, stdout)
	}
	data, err := os.ReadFile(export)
	if err != nil {
		t.Fatalf("precondition: no JSON export at %s: %v\nstderr:\n%s", export, err, stderr)
	}
	schema, rows := s103Rows(t, data)
	terminal := s103Flatten(stdout)

	// R2.1: neither the exit status nor the schema version changes.
	if code != 0 {
		t.Errorf("`overlay compare` exited %d; a mirror-ahead finding must not change the exit status (R2.1)\nstderr:\n%s", code, stderr)
	}
	if schema != 2 {
		t.Errorf("the export's schema is %v; R2.1 keeps it at 2", schema)
	}

	// --- Hostile: nothing that is not ahead of ::gentoo may be flagged.
	for _, atom := range []string{
		"dev-libs/converted", // ahead, but already converted
		"dev-libs/revision",  // our 1.0-r1 is ::gentoo's 1.0 (A1)
		"dev-libs/gentoorev", // our 1.0 is ::gentoo's 1.0-r2 (A1)
		"dev-libs/samever",   // a mirror at ::gentoo's own version
		"dev-libs/near",      // ::gentoo carries dev-libs/near-utils, not this package
		"app-misc/ours",      // ::gentoo does not carry the package
	} {
		if got := s103MirrorFindings(rows[atom]); len(got) > 0 {
			t.Errorf("%s is flagged as ahead of ::gentoo but shaped as a mirror, and it is not: %q", atom, got)
		}
		// The terminal too: a package with no row in the payload (::gentoo
		// does not carry it) can still be named by a run-level note.
		for _, line := range strings.Split(stdout, "\n") {
			if s103LineNamesAtom(line, atom) && strings.Contains(strings.ToLower(line), "mirror") {
				t.Errorf("the terminal flags %s as shaped as a mirror, and it is not ahead of ::gentoo: %q", atom, line)
			}
		}
	}
	// The third element: dev-libs/between's 1.2 is ::gentoo's 1.2.
	for _, f := range s103MirrorFindings(rows["dev-libs/between"]) {
		if s103NamesVersion(f, "1.2") && !s103NamesVersion(f, "1.1") {
			t.Errorf("dev-libs/between is flagged at 1.2, which ::gentoo carries: %q", f)
		}
	}

	// --- Flagged, each naming the version and the condition it met (R2, R2.1).
	type want struct {
		atom, version string
		keywords      bool // the KEYWORDS condition is met
		header        bool // the "Mirrored from ::gentoo" condition is met
	}
	for _, w := range []want{
		{"dev-libs/kwahead", "1.1", true, false},
		{"dev-libs/hdrahead", "1.1", false, true},
		{"dev-libs/tildes", "1.1", true, false}, // "~amd64 ~arm64" is not "~amd64"
		{"dev-libs/prefix", "1.1", true, true},  // ::gentoo's 1.10 is not 1.1
		{"dev-libs/between", "1.1", true, true}, // the ahead ebuild is not the newest
	} {
		var hit string
		for _, f := range s103MirrorFindings(rows[w.atom]) {
			if s103NamesVersion(f, w.version) {
				hit = f
				break
			}
		}
		if hit == "" {
			t.Errorf("%s-%s: no finding in the JSON payload saying it is ahead of ::gentoo but still shaped as a mirror (R2); row findings: %q", w.atom, w.version, s103RowFindings(rows[w.atom]))
			continue
		}
		if w.keywords && !strings.Contains(hit, "KEYWORDS") {
			t.Errorf("%s: the finding does not name the KEYWORDS condition it met (R2.1): %q", w.atom, hit)
		}
		if !w.keywords && strings.Contains(hit, "KEYWORDS") {
			t.Errorf("%s: the finding names the KEYWORDS condition, but its KEYWORDS is ~amd64 (R2.1): %q", w.atom, hit)
		}
		if w.header && !strings.Contains(hit, "Mirrored from ::gentoo") {
			t.Errorf("%s: the finding does not name the \"Mirrored from ::gentoo\" condition it met (R2.1): %q", w.atom, hit)
		}
		if !w.header && strings.Contains(hit, "Mirrored from ::gentoo") {
			t.Errorf("%s: the finding names the header condition, but the ebuild carries no such header (R2.1): %q", w.atom, hit)
		}
		if !strings.Contains(terminal, s103Flatten(hit)) {
			t.Errorf("%s: the finding is in the JSON payload but not in the terminal report (R2.1): %q\nstdout:\n%s", w.atom, hit, stdout)
		}
	}

	// R2.1: the verdict does not move. Each flagged package sits in the same
	// verdict list as dev-libs/converted, its unflagged twin (overlay 1.1 against
	// ::gentoo 1.0, nothing declared).
	twin := rows["dev-libs/converted"]
	if twin == nil {
		t.Fatalf("precondition: dev-libs/converted has no row in the export\n%s", data)
	}
	for _, atom := range []string{"dev-libs/kwahead", "dev-libs/hdrahead", "dev-libs/tildes"} {
		r := rows[atom]
		if r == nil {
			t.Errorf("precondition: %s has no row in the export", atom)
			continue
		}
		if !s103SameLists(r.lists, twin.lists) {
			t.Errorf("%s's verdict lists are %v, its unflagged twin's %v — the finding moved the verdict (R2.1)", atom, s103Keys(r.lists), s103Keys(twin.lists))
		}
	}
}

func s103RowFindings(r *s103Row) []string {
	if r == nil {
		return nil
	}
	return r.findings
}

func s103SameLists(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func s103Keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
