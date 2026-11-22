package main

// Authored for story 103, sub-task 2.3 (R1.2, R3; A3).
//
// Written from the contract, not from an implementation: story.md R1.2 ("report,
// in the apply summary, a warning naming the package and saying that its header
// comment and its registry `patched` text still describe a same-version mirror
// and need rewriting by hand") and R3 ("IF no local ::gentoo tree is configured
// THEN skip R1 and R2 and say so once"); design.md "Warn about the prose half"
// (one warning per conversion, plus "KEYWORDS not rewritten" when the KEYWORDS
// was multi-line, A3).
//
// Everything drives the REAL command, `bentoo overlay autoupdate --apply`, over
// a real overlay, with ::gentoo reached the way production reaches it: through
// BENTOO_GENTOO_REPO. Only the child processes are faked (they all succeed), and
// each bump is proved beforehand by a check run on a SILENT applier of its own,
// so nothing the harness does can add a line to the output being counted.
//
// The proof matters: production publishes the tree `--check` retained, so a
// warning recorded only while Apply itself substitutes would never reach the
// summary of a real run. Here it has to.
//
// Hostile halves first:
//   - the warning is about ONE package: dev-libs/mirr-utils (a name that
//     EXTENDS dev-libs/mirr) is bumped in the same run to a version ::gentoo
//     carries, so no warning may name it — and a warning keyed on a prefix
//     match would;
//   - "KEYWORDS not rewritten" belongs to the multi-line package alone and must
//     not ride along on every warning;
//   - R3's notice is "once": two bumps in one run must not print it twice, a
//     configured BENTOO_GENTOO_REPO that points at NOTHING is a run with no
//     local tree (production's default path on a host without one), and a run
//     WITH a tree prints no notice at all.

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// s103WarnMirror is a same-version mirror of ::gentoo with ::gentoo's stable KEYWORDS.
const s103WarnMirror = `EAPI=8

# Mirrored from ::gentoo only to add python3_15.
# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

DESCRIPTION="Mirror fixture"
SLOT="0"
KEYWORDS="amd64 arm64 x86"
`

// s103WarnMultiLine is the same mirror with KEYWORDS spread over two lines (A3).
const s103WarnMultiLine = `EAPI=8

# Mirrored from ::gentoo only to add python3_15.
# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.
PYTHON_COMPAT=( python3_{11..15} )
inherit python-r1

DESCRIPTION="Mirror fixture"
SLOT="0"
KEYWORDS="amd64 arm64
	x86"
`

// s103Bump is one package the run bumps 1.0 -> 1.1.
type s103Bump struct {
	atom   string
	ebuild string
	// gentoo is the versions ::gentoo carries of this package.
	gentoo []string
}

var (
	// Past ::gentoo: ::gentoo carries 1.0 only.
	s103Converted = s103Bump{atom: "dev-libs/mirr", ebuild: s103WarnMirror, gentoo: []string{"1.0"}}
	// A name extending the one above, bumped to a version ::gentoo carries (U1).
	s103LikeNamed = s103Bump{atom: "dev-libs/mirr-utils", ebuild: s103WarnMirror, gentoo: []string{"1.0", "1.1"}}
	// Past ::gentoo with a multi-line KEYWORDS (A3).
	s103MultiLineBump = s103Bump{atom: "dev-libs/multi", ebuild: s103WarnMultiLine, gentoo: []string{"1.0"}}
	// A package ::gentoo does not carry at all (U2).
	s103OverlayOnly = s103Bump{atom: "app-misc/ours", ebuild: s103WarnMirror}
)

// s103Silent is a reporter that drops everything: the proof applier's.
type s103Silent struct{}

func (s103Silent) BatchStart(int)                            {}
func (s103Silent) TaskStart(string, string)                  {}
func (s103Silent) TaskStage(string, string)                  {}
func (s103Silent) TaskProgress(string, float64)              {}
func (s103Silent) TaskLine(string, tui.Stream, string, bool) {}
func (s103Silent) TaskDone(string, bool, string, string)     {}
func (s103Silent) Log(string, string)                        {}
func (s103Silent) BatchDone(string)                          {}

var _ tui.Reporter = s103Silent{}

// s103World is a CLI whose every applier fakes its children, and an overlay
// holding bumps. gentooRoot is what BENTOO_GENTOO_REPO names; gentooPresent
// says whether it is created and filled.
func s103World(t *testing.T, gentooPresent bool, bumps ...s103Bump) (c *testCLI, gentooRoot string) {
	t.Helper()
	c = newTestCLI(t, withDeps(func(d *deps) {
		d.newApplier = func(overlayPath, configDir string, opts ...autoupdate.ApplierOption) (*autoupdate.Applier, error) {
			opts = append(opts,
				autoupdate.WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					return exec.CommandContext(ctx, "true")
				}),
				autoupdate.WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
				autoupdate.WithApplierSetVersionsFunc(func(string, map[string]string) error { return nil }),
			)
			opts = append(opts, cleanBuildOptions()...)

			// The proof, taken on an applier of its own that says nothing.
			proofOpts := append(append([]autoupdate.ApplierOption{}, opts...),
				autoupdate.WithApplierReporter(s103Silent{}),
				autoupdate.WithApplierLogger(slog.New(slog.DiscardHandler)),
			)
			proof, err := autoupdate.NewApplier(overlayPath, configDir, proofOpts...)
			if err != nil {
				return nil, err
			}
			for _, u := range proof.Pending().List() {
				proof.Validate(context.Background(), u.Package, validate.DepthNone)
			}
			return autoupdate.NewApplier(overlayPath, configDir, opts...)
		}
	}))

	// Deliberately named without "gentoo": a path printed anywhere must not be
	// mistaken for a sentence about the tree.
	gentooRoot = filepath.Join(c.Home(), "upstream-tree")
	t.Setenv("BENTOO_GENTOO_REPO", gentooRoot)
	if gentooPresent {
		s103WriteEbuild(t, filepath.Join(gentooRoot, "profiles"), "repo_name", "gentoo\n")
	}

	pending := make([]autoupdate.PendingUpdate, 0, len(bumps))
	for _, b := range bumps {
		cat, pn, _ := strings.Cut(b.atom, "/")
		s103WriteEbuild(t, filepath.Join(c.Overlay(), cat, pn), pn+"-1.0.ebuild", b.ebuild)
		if gentooPresent {
			for _, v := range b.gentoo {
				// ::gentoo's copy is the same bytes: ours is its mirror.
				s103WriteEbuild(t, filepath.Join(gentooRoot, cat, pn), pn+"-"+v+".ebuild", b.ebuild)
			}
		}
		pending = append(pending, autoupdate.PendingUpdate{
			Package: b.atom, CurrentVersion: "1.0", NewVersion: "1.1", Status: autoupdate.StatusPending,
		})
	}
	seedCooldownPending(t, c, pending...)
	return c, gentooRoot
}

func s103WriteEbuild(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// s103Run runs the command and fails the test outright unless every bump was
// published: a warning test over a run that published nothing asserts nothing.
func s103Run(t *testing.T, c *testCLI, target string, bumps ...s103Bump) (stdout, stderr string) {
	t.Helper()
	stdout, stderr, code := c.Run("overlay", "autoupdate", "--apply", target)
	if code != 0 {
		t.Fatalf("precondition: --apply %s exited %d\nstdout:\n%s\nstderr:\n%s", target, code, stdout, stderr)
	}
	for _, b := range bumps {
		cat, pn, _ := strings.Cut(b.atom, "/")
		if _, err := os.Stat(filepath.Join(c.Overlay(), cat, pn, pn+"-1.1.ebuild")); err != nil {
			t.Fatalf("precondition: %s-1.1 was not published\nstdout:\n%s\nstderr:\n%s", b.atom, stdout, stderr)
		}
	}
	return stdout, stderr
}

// s103NamesAtom: does line name atom as a whole package — not as the prefix of
// a longer name (dev-libs/mirr inside dev-libs/mirr-utils)? A version may follow
// it ("dev-libs/mirr-1.1").
func s103NamesAtom(line, atom string) bool {
	re := regexp.MustCompile(`(?:^|[^\w/+.-])` + regexp.QuoteMeta(atom) + `(?:-\d|[^\w+-]|$)`)
	return re.MatchString(line)
}

// s103Block is the part of the summary under the "Apply Result" heading whose
// package line is exactly atom, or "" when there is none.
func s103Block(stdout, atom string) string {
	for _, block := range strings.Split(stdout, "Apply Result")[1:] {
		for _, line := range strings.Split(block, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if strings.TrimSpace(line) == atom {
				if i := strings.Index(block, "Apply All Summary"); i >= 0 {
					block = block[:i]
				}
				return block
			}
			break
		}
	}
	return ""
}

// s103IsProseWarning: does text tell the operator the header comment and the
// registry `patched` text still need rewriting?
func s103IsProseWarning(text string) bool {
	l := strings.ToLower(text)
	return strings.Contains(l, "header") && strings.Contains(l, "patched")
}

// s103Warned: did the run warn about atom (R1.2) — either in the package's own
// block of the summary, or on a line that names it?
func s103Warned(stdout, atom string) bool {
	if s103IsProseWarning(s103Block(stdout, atom)) {
		return true
	}
	for _, line := range strings.Split(stdout, "\n") {
		if s103NamesAtom(line, atom) && s103IsProseWarning(line) {
			return true
		}
	}
	return false
}

// s103SaidNotRewritten: did the run say atom's KEYWORDS was not rewritten?
func s103SaidNotRewritten(stdout, atom string) bool {
	if strings.Contains(strings.ToLower(s103Block(stdout, atom)), "keywords not rewritten") {
		return true
	}
	for _, line := range strings.Split(stdout, "\n") {
		if s103NamesAtom(line, atom) && strings.Contains(strings.ToLower(line), "keywords not rewritten") {
			return true
		}
	}
	return false
}

// s103NoticeRe matches a sentence about the ::gentoo tree itself.
var s103NoticeRe = regexp.MustCompile(`(?i)::gentoo|gentoo (tree|repository)`)

// s103Notices counts the lines of stdout and stderr that talk about the tree.
func s103Notices(stdout, stderr string) []string {
	var hits []string
	for _, line := range strings.Split(stdout+"\n"+stderr, "\n") {
		if s103NoticeRe.MatchString(line) {
			hits = append(hits, line)
		}
	}
	return hits
}

func TestApplyMirrorWarningNamesOnlyTheConvertedPackage(t *testing.T) {
	bumps := []s103Bump{s103LikeNamed, s103Converted, s103MultiLineBump}
	c, _ := s103World(t, true, bumps...)

	stdout, stderr := s103Run(t, c, "all", bumps...)

	// Hostile: the like-named package was bumped to a version ::gentoo carries.
	if s103Warned(stdout, s103LikeNamed.atom) {
		t.Errorf("the summary warns about %s, which was bumped to a version ::gentoo carries (U1) — the warning is attributed by a prefix of the name or to every package\nstdout:\n%s", s103LikeNamed.atom, stdout)
	}
	// Hostile: the multi-line note belongs to the multi-line package only.
	if s103SaidNotRewritten(stdout, s103Converted.atom) {
		t.Errorf("the summary says KEYWORDS was not rewritten for %s, whose single-line KEYWORDS was\nstdout:\n%s", s103Converted.atom, stdout)
	}

	if !s103Warned(stdout, s103Converted.atom) {
		t.Errorf("no warning in the apply summary that %s's header comment and registry `patched` text still describe a same-version mirror (R1.2)\nstdout:\n%s\nstderr:\n%s", s103Converted.atom, stdout, stderr)
	}
	if !s103Warned(stdout, s103MultiLineBump.atom) {
		t.Errorf("no warning in the apply summary for %s, bumped past ::gentoo with a multi-line KEYWORDS (R1.2, A3)\nstdout:\n%s", s103MultiLineBump.atom, stdout)
	}
	if !s103SaidNotRewritten(stdout, s103MultiLineBump.atom) {
		t.Errorf("the summary does not say %s's multi-line KEYWORDS was not rewritten (A3)\nstdout:\n%s", s103MultiLineBump.atom, stdout)
	}
}

func TestApplyMirrorWarningOnASinglePackageApply(t *testing.T) {
	c, _ := s103World(t, true, s103Converted)

	stdout, stderr := s103Run(t, c, s103Converted.atom, s103Converted)

	if !s103Warned(stdout, s103Converted.atom) {
		t.Errorf("`--apply %s` printed no warning that its header comment and registry `patched` text still describe a same-version mirror (R1.2)\nstdout:\n%s\nstderr:\n%s", s103Converted.atom, stdout, stderr)
	}
}

func TestApplyMirrorNoTreeSaysSoOnce(t *testing.T) {
	cases := []struct {
		name   string
		target string
		bumps  []s103Bump
	}{
		// Hostile: two bumps in one run must not each say it.
		{"apply all, two bumps", "all", []s103Bump{s103Converted, s103MultiLineBump}},
		{"apply one package", s103Converted.atom, []s103Bump{s103Converted}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// BENTOO_GENTOO_REPO names a directory that does not exist.
			c, root := s103World(t, false, tc.bumps...)
			if _, err := os.Stat(root); err == nil {
				t.Fatalf("precondition: %s exists", root)
			}

			stdout, stderr := s103Run(t, c, tc.target, tc.bumps...)

			if got := s103Notices(stdout, stderr); len(got) != 1 {
				t.Errorf("with no local ::gentoo tree the run must say so exactly once (R3); %d line(s) say it: %q\nstdout:\n%s\nstderr:\n%s", len(got), got, stdout, stderr)
			}
			for _, b := range tc.bumps {
				if s103Warned(stdout, b.atom) {
					t.Errorf("a conversion warning for %s was printed although R3 skips R1 without a tree\nstdout:\n%s", b.atom, stdout)
				}
				cat, pn, _ := strings.Cut(b.atom, "/")
				got, err := os.ReadFile(filepath.Join(c.Overlay(), cat, pn, pn+"-1.1.ebuild"))
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != b.ebuild {
					t.Errorf("%s-1.1 was edited although R3 skips R1 without a tree (no guessing which side of ::gentoo it is on)\n--- got ---\n%s", b.atom, got)
				}
			}
		})
	}

	// The control bumps a package ::gentoo does not carry, so nothing else in
	// the run has a reason to mention the tree.
	t.Run("control: a run with a tree says nothing about it", func(t *testing.T) {
		c, _ := s103World(t, true, s103OverlayOnly)

		stdout, stderr := s103Run(t, c, "all", s103OverlayOnly)

		if got := s103Notices(stdout, stderr); len(got) != 0 {
			t.Errorf("a run whose ::gentoo tree is present printed %d line(s) about the tree: %q", len(got), got)
		}
	})
}
