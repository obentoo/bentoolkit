package autoupdate

// Authored for story 103, sub-task 2.2 (R1, R1.1, R1.3, U1, U2, U3; A1, A3).
//
// Written from the contract, not from an implementation: story.md R1/R1.1/R1.3
// and the unchanged behaviours U1-U3, design.md "Convert the mechanical half in
// applySubstitutions" (single-line KEYWORDS rewritten to ~amd64; the KEYWORDS tag
// inserted directly above the first `# BENTOO-DIVERGENCE:` line, or above
// PYTHON_COMPAT=/inherit when there is none; nothing happens unless ::gentoo
// carries the OLD version and not the NEW one, both compared without -rN).
//
// Every case drives a real Apply on each of the three ways a bump reaches the
// overlay: in place (no staging root, prepareInOverlay); staged (a staging
// root, prepareInStagingTree); and checked-then-applied (Validate stages and
// proves the candidate, as `--check` does, and Apply then promotes that retained
// tree, as `--apply` does in production). The observable is the published
// ebuild.
//
// Hostile halves come first. The rule under test is an IDENTITY rule — "a
// version ::gentoo carries" — so the table opens with the inputs that would make
// it fire wrongly in each direction:
//   - wrongly collapse: ::gentoo carries 1.10 (not 1.1), or a like-named
//     package carries 1.1 — neither is "::gentoo carries cat/p-1.1";
//   - wrongly split: ::gentoo carries 1.1-r1, or the old version only as 1.0-r2,
//     or our old ebuild is 1.0-r1 — each IS the same version under A1.
// The derived values the fix writes (the KEYWORDS line and the KEYWORDS tag) get
// their own third elements: a commented-out KEYWORDS line, a variable whose name
// ends in KEYWORDS, and a PYTHON_COMPAT tag whose prose mentions KEYWORDS.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

const (
	s103Pkg = "dev-libs/mirr"
	s103Tag = "# BENTOO-DIVERGENCE: KEYWORDS - ~amd64 only: a bump past ::gentoo is no longer a mirror, so it follows the overlay baseline (amd64, x86-64-v3)."
)

// s103Mirror is a same-version mirror of ::gentoo as the overlay carries one:
// the mirror header, one existing divergence tag, ::gentoo's stable KEYWORDS.
const s103Mirror = `# Copyright 1999-2026 Gentoo Authors
# Distributed under the terms of the GNU General Public License v2

# Mirrored from ::gentoo only to add python3_15.
# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15, which ::gentoo does not list yet.

EAPI=8

PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

DESCRIPTION="Mirror fixture"
HOMEPAGE="https://example.com"
SRC_URI="https://example.com/${P}.tar.gz"

LICENSE="MIT"
SLOT="0"
KEYWORDS="amd64 arm arm64 ppc ppc64 x86"
`

// s103MirrorConverted is s103Mirror bumped past ::gentoo, written out by hand:
// the tag directly above the first existing tag, KEYWORDS="~amd64", every other
// byte — the header included (R1.2 leaves prose to a human) — unchanged.
const s103MirrorConverted = `# Copyright 1999-2026 Gentoo Authors
# Distributed under the terms of the GNU General Public License v2

# Mirrored from ::gentoo only to add python3_15.
# BENTOO-DIVERGENCE: KEYWORDS - ~amd64 only: a bump past ::gentoo is no longer a mirror, so it follows the overlay baseline (amd64, x86-64-v3).
# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15, which ::gentoo does not list yet.

EAPI=8

PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

DESCRIPTION="Mirror fixture"
HOMEPAGE="https://example.com"
SRC_URI="https://example.com/${P}.tar.gz"

LICENSE="MIT"
SLOT="0"
KEYWORDS="~amd64"
`

// s103Untagged carries no divergence tag at all.
const s103Untagged = `# Copyright 1999-2026 Gentoo Authors
# Distributed under the terms of the GNU General Public License v2

EAPI=8

PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

DESCRIPTION="Mirror fixture"
SLOT="0"
KEYWORDS="amd64 ~arm64"
`

// s103UntaggedConverted: with no tag to stand beside, the tag goes directly
// above PYTHON_COMPAT=.
const s103UntaggedConverted = `# Copyright 1999-2026 Gentoo Authors
# Distributed under the terms of the GNU General Public License v2

EAPI=8

# BENTOO-DIVERGENCE: KEYWORDS - ~amd64 only: a bump past ::gentoo is no longer a mirror, so it follows the overlay baseline (amd64, x86-64-v3).
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

DESCRIPTION="Mirror fixture"
SLOT="0"
KEYWORDS="~amd64"
`

// s103KeywordsTagged already carries a KEYWORDS tag, worded differently from
// the one the applier writes. It is a KEYWORDS tag all the same (R1.1 "unless a
// KEYWORDS tag is already present").
const s103KeywordsTagged = `EAPI=8

# BENTOO-DIVERGENCE: KEYWORDS - overlay baseline only.
# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

SLOT="0"
KEYWORDS="amd64 x86"
`

const s103KeywordsTaggedConverted = `EAPI=8

# BENTOO-DIVERGENCE: KEYWORDS - overlay baseline only.
# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

SLOT="0"
KEYWORDS="~amd64"
`

// s103MentionsKeywords has a PYTHON_COMPAT tag whose PROSE mentions KEYWORDS.
// It is not a KEYWORDS tag, so the KEYWORDS tag must still be added.
const s103MentionsKeywords = `EAPI=8

# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15; KEYWORDS follow ::gentoo.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

SLOT="0"
KEYWORDS="amd64 x86"
`

const s103MentionsKeywordsConverted = `EAPI=8

# BENTOO-DIVERGENCE: KEYWORDS - ~amd64 only: a bump past ::gentoo is no longer a mirror, so it follows the overlay baseline (amd64, x86-64-v3).
# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15; KEYWORDS follow ::gentoo.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

SLOT="0"
KEYWORDS="~amd64"
`

// s103LookAlikes carries two lines that look like KEYWORDS and are not the
// assignment: a commented-out one and a variable whose name ends in KEYWORDS.
const s103LookAlikes = `EAPI=8

# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

SLOT="0"
MY_KEYWORDS="amd64 x86"
#KEYWORDS="amd64 arm64 x86"
KEYWORDS="amd64 arm64 x86"
`

const s103LookAlikesConverted = `EAPI=8

# BENTOO-DIVERGENCE: KEYWORDS - ~amd64 only: a bump past ::gentoo is no longer a mirror, so it follows the overlay baseline (amd64, x86-64-v3).
# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

SLOT="0"
MY_KEYWORDS="amd64 x86"
#KEYWORDS="amd64 arm64 x86"
KEYWORDS="~amd64"
`

// s103Commit is a commit-tracked mirror: U3's commit variable must still be
// rewritten beside the conversion.
const s103Commit = `EAPI=8

# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

COMMIT="1111111111111111111111111111111111111111"
SRC_URI="https://example.com/${COMMIT}.tar.gz"
SLOT="0"
KEYWORDS="amd64 x86"
`

const s103CommitHash = "2222222222222222222222222222222222222222"

const s103CommitConverted = `EAPI=8

# BENTOO-DIVERGENCE: KEYWORDS - ~amd64 only: a bump past ::gentoo is no longer a mirror, so it follows the overlay baseline (amd64, x86-64-v3).
# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

COMMIT="2222222222222222222222222222222222222222"
SRC_URI="https://example.com/${COMMIT}.tar.gz"
SLOT="0"
KEYWORDS="~amd64"
`

// s103AlreadyBaseline already carries the overlay baseline (R1.3).
const s103AlreadyBaseline = `EAPI=8

# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

SLOT="0"
KEYWORDS="~amd64"
`

// s103NoKeywords has no KEYWORDS assignment at all (R1.3).
const s103NoKeywords = `EAPI=8

# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

SLOT="0"
`

// s103MultiLine spreads KEYWORDS over two lines (A3: reported, never edited).
const s103MultiLine = `EAPI=8

# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

SLOT="0"
KEYWORDS="amd64 arm64
	ppc64 x86"
`

// s103Case is one bump: what ::gentoo holds, the ebuild the overlay holds at
// oldVersion, and what the published oldVersion -> newVersion ebuild must read.
type s103Case struct {
	name       string
	oldVersion string
	newVersion string
	// gentoo lists every ebuild ::gentoo holds, as "<category>/<pn>/<pn>-<pvr>".
	gentoo     []string
	ebuild     string
	commitHash string
	// want is the exact published ebuild.
	want string
	// mayAddTag: the ebuild must equal the source byte for byte once any line
	// equal to the KEYWORDS tag is removed — R1.3 fixes KEYWORDS, not the tag.
	mayAddTag bool
}

// s103Gentoo turns a version list of dev-libs/mirr into ::gentoo paths.
func s103Gentoo(versions ...string) []string {
	out := make([]string, 0, len(versions))
	for _, v := range versions {
		out = append(out, "dev-libs/mirr/mirr-"+v)
	}
	return out
}

func s103Cases() []s103Case {
	return []s103Case{
		// --- Hostile: the identity rule wrongly COLLAPSING two versions or packages.
		{
			name:       "hostile collapse: ::gentoo carries 1.10, which is not 1.1",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0", "1.10"),
			ebuild: s103Mirror, want: s103MirrorConverted,
		},
		{
			name:       "hostile collapse: a like-named package and another category carry 1.1, cat/p does not",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: append(s103Gentoo("1.0"),
				"dev-libs/mirr-utils/mirr-utils-1.1",
				"dev-python/mirr/mirr-1.1"),
			ebuild: s103Mirror, want: s103MirrorConverted,
		},
		{
			name:       "hostile collapse: a PYTHON_COMPAT tag that mentions KEYWORDS is not a KEYWORDS tag",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0"),
			ebuild: s103MentionsKeywords, want: s103MentionsKeywordsConverted,
		},
		{
			name:       "hostile collapse: a commented KEYWORDS line and MY_KEYWORDS are not the assignment",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0"),
			ebuild: s103LookAlikes, want: s103LookAlikesConverted,
		},
		{
			name:       "hostile collapse: ::gentoo carries the package but not the old version (never a mirror)",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("0.9"),
			ebuild: s103Mirror, want: s103Mirror,
		},
		// --- Hostile: the identity rule wrongly SPLITTING one version in two (A1).
		{
			name:       "hostile split: ::gentoo carries the new version as 1.1-r1 (U1)",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0", "1.1-r1"),
			ebuild: s103Mirror, want: s103Mirror,
		},
		{
			name:       "hostile split: ::gentoo carries the old version only as 1.0-r2",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0-r2"),
			ebuild: s103Mirror, want: s103MirrorConverted,
		},
		{
			name:       "hostile split: our old ebuild is 1.0-r1, ::gentoo carries 1.0",
			oldVersion: "1.0-r1", newVersion: "1.1",
			gentoo: s103Gentoo("1.0"),
			ebuild: s103Mirror, want: s103MirrorConverted,
		},
		// --- Benign.
		{
			name:       "R1: past ::gentoo, tag directly above the first existing tag",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0"),
			ebuild: s103Mirror, want: s103MirrorConverted,
		},
		{
			name:       "R1.1: no tag yet, the tag goes above PYTHON_COMPAT=",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0"),
			ebuild: s103Untagged, want: s103UntaggedConverted,
		},
		{
			name:       "R1.1: a KEYWORDS tag is already present, so none is added",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0"),
			ebuild: s103KeywordsTagged, want: s103KeywordsTaggedConverted,
		},
		{
			name:       "U3: the commit variable is still rewritten beside the conversion",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo:     s103Gentoo("1.0"),
			ebuild:     s103Commit,
			commitHash: s103CommitHash,
			want:       s103CommitConverted,
		},
		{
			name:       "U1: ::gentoo carries the new version too",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0", "1.1"),
			ebuild: s103Mirror, want: s103Mirror,
		},
		{
			name:       "U2: ::gentoo does not carry the package at all",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: []string{"dev-libs/other/other-1.0"},
			ebuild: s103Mirror, want: s103Mirror,
		},
		{
			name:       "R1.3: KEYWORDS is already exactly ~amd64",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0"),
			ebuild: s103AlreadyBaseline, want: s103AlreadyBaseline, mayAddTag: true,
		},
		{
			name:       "R1.3: the ebuild has no KEYWORDS assignment",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0"),
			ebuild: s103NoKeywords, want: s103NoKeywords, mayAddTag: true,
		},
		{
			name:       "A3: a multi-line KEYWORDS is not edited, and no tag claims it was",
			oldVersion: "1.0", newVersion: "1.1",
			gentoo: s103Gentoo("1.0"),
			ebuild: s103MultiLine, want: s103MultiLine,
		},
	}
}

// s103Mode is the way a bump reaches the overlay.
type s103Mode string

const (
	s103InPlace        s103Mode = "in-place"
	s103Staged         s103Mode = "staged"
	s103CheckThenApply s103Mode = "check-then-apply"
)

// s103Apply builds the overlay and ::gentoo trees for one case, runs Apply on
// the chosen path, and returns the published new ebuild.
func s103Apply(t *testing.T, tc s103Case, mode s103Mode) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	overlay := filepath.Join(tmp, "overlay")
	gentoo := filepath.Join(tmp, "gentoo")
	config := filepath.Join(tmp, "config")
	if err := os.MkdirAll(gentoo, 0o755); err != nil {
		t.Fatalf("mkdir gentoo: %v", err)
	}
	for _, rel := range tc.gentoo {
		path := filepath.Join(gentoo, rel+".ebuild")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		// ::gentoo's copy is the same bytes: the overlay ebuild is its mirror.
		if err := os.WriteFile(path, []byte(tc.ebuild), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	createTestEbuildFileWithContent(t, overlay, s103Pkg, tc.oldVersion, tc.ebuild)

	pending, err := NewPendingList(config)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	if err := pending.Add(PendingUpdate{
		Package:        s103Pkg,
		CurrentVersion: tc.oldVersion,
		NewVersion:     tc.newVersion,
		CommitHash:     tc.commitHash,
		Status:         StatusPending,
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}

	opts := []ApplierOption{
		WithApplierPendingList(pending),
		WithApplierGentooPath(gentoo),
		WithExecCommand((&execRecorder{}).command),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	}
	switch mode {
	case s103InPlace:
	case s103Staged:
		opts = append(opts, WithApplierStagingRoot(filepath.Join(tmp, "staging")), WithApplierDepth(validate.DepthOptions))
	case s103CheckThenApply:
		// A check whose compile gate passes, so its retained tree is a proof
		// the following apply promotes as it stands.
		opts = append(opts,
			WithApplierStagingRoot(filepath.Join(tmp, "staging")),
			WithApplierDistdir(t.TempDir(), ""),
			WithApplierRunCaptured(capturedFrom(func(*exec.Cmd) ([]byte, error) { return []byte(cleanCompile), nil })),
		)
	}
	applier, err := NewApplier(overlay, config, opts...)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	if mode == s103CheckThenApply {
		applier.Validate(t.Context(), s103Pkg, validate.DepthCompile)
	}

	result, err := applier.Apply(t.Context(), s103Pkg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Success {
		t.Fatalf("Apply did not succeed: Error=%v Waiting=%q Held=%v", result.Error, result.Waiting, result.Held)
	}
	if mode == s103CheckThenApply && result.ValidationSource != ValidationSourceStaged {
		t.Fatalf("precondition: Apply did not promote the tree Validate retained (ValidationSource=%q: %s), so this mode is not exercising --check then --apply", result.ValidationSource, result.DepthReason)
	}

	got, err := os.ReadFile(applier.EbuildPath(s103Pkg, tc.newVersion))
	if err != nil {
		t.Fatalf("reading the published %s-%s ebuild: %v", s103Pkg, tc.newVersion, err)
	}
	old, err := os.ReadFile(applier.EbuildPath(s103Pkg, tc.oldVersion))
	if err != nil {
		t.Fatalf("reading the old %s-%s ebuild: %v", s103Pkg, tc.oldVersion, err)
	}
	if string(old) != tc.ebuild {
		t.Errorf("the bump edited the OLD ebuild %s-%s; only the new one may change\n--- got ---\n%s", s103Pkg, tc.oldVersion, old)
	}
	return string(got)
}

// withoutTag drops every line equal to the KEYWORDS tag.
func withoutTag(s string) string {
	lines := strings.SplitAfter(s, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if strings.TrimRight(l, "\n") == s103Tag {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "")
}

func TestApplyConvertsMirrorBumpedPastGentoo(t *testing.T) {
	for _, mode := range []s103Mode{s103InPlace, s103Staged, s103CheckThenApply} {
		for _, tc := range s103Cases() {
			t.Run(fmt.Sprintf("%s/%s", mode, tc.name), func(t *testing.T) {
				got := s103Apply(t, tc, mode)

				if tc.mayAddTag {
					if n := strings.Count(got, s103Tag); n > 1 {
						t.Errorf("the KEYWORDS tag appears %d times; at most once", n)
					}
					if withoutTag(got) != tc.want {
						t.Errorf("KEYWORDS (or another line) changed where R1.3 leaves it untouched\n--- got ---\n%s\n--- want (modulo the tag) ---\n%s", got, tc.want)
					}
					return
				}

				if got == tc.want {
					return
				}
				switch {
				case tc.want == tc.ebuild && strings.Contains(got, `KEYWORDS="~amd64"`) && !strings.Contains(tc.ebuild, `KEYWORDS="~amd64"`):
					t.Errorf("KEYWORDS was rewritten to ~amd64 on a bump that does not leave ::gentoo behind")
				case tc.want != tc.ebuild && !strings.Contains(got, `KEYWORDS="~amd64"`):
					t.Errorf("a bump past ::gentoo kept the mirror's KEYWORDS (R1)")
				case tc.want != tc.ebuild && !strings.Contains(got, s103Tag):
					t.Errorf("KEYWORDS was rewritten without the KEYWORDS divergence tag (R1.1)")
				}
				t.Errorf("published ebuild differs from the expected one\n--- got ---\n%s\n--- want ---\n%s", got, tc.want)
			})
		}
	}
}
