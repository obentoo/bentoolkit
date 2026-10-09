package autoupdate

// Story 084, sub-task 2.1. promote writes the published Manifest's own records
// plus the validated ones, instead of the staged Manifest whole (R1.1, R1.2,
// R1.6, R1.7, R2.1), and every rollback, mode and refusal guard stays as it is
// (R3.1-R3.7).
//
// The staged package directory holds the candidate ebuild alone, so the
// Manifest `pkgdev manifest` writes there names the candidate's archive alone.
// Publishing it whole deletes the DIST record of every other version still in
// the package directory — an ebuild with no record for its archive does not
// install. Observed on dev-util/flutter (3.47.6-r1), 2026-10-09.
//
// Most tests call promote directly on a temp overlay and a temp staged tree,
// the shape TestPromoteRefusesEbuildCreatedAfterCheck uses. Two run the whole
// Apply on promoteFixture (applier_promote_test.go) with a stand-in
// `pkgdev manifest` that, like the real one, writes one DIST record per ebuild
// in its working directory: the staged tree on the validation path, the
// published package directory on the --clean regeneration path.
//
// Log assertions read the applier's logger through s062Recorder
// (logger_injection_test.go). Every test asserting a WARN is ABSENT first finds
// the merge's INFO event in the same recorder, so a logger that was never wired
// cannot make it pass.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// s084Pkg is promoteFixture's package, so writePublishedManifest addresses it.
const s084Pkg = "media-plugins/gst-plugins-qt6"

// s084Case describes one direct promotion.
type s084Case struct {
	versions  []string // published ebuilds already in the package directory
	published *string  // the published Manifest; nil: the package has none
	mode      fs.FileMode
	staged    *string // the staged Manifest; nil: the staged tree has none
	candidate string
}

type s084Promoted struct {
	pkgDir, manifest, candidate string
	rec                         *s062Recorder
	undo                        publishedUndo
	err                         error
}

func s084Str(s string) *string { return &s }

func s084Lines(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// s084Promote lays out the overlay and the staged tree for c and promotes.
func s084Promote(t *testing.T, c s084Case) s084Promoted {
	t.Helper()
	root := t.TempDir()
	overlay := filepath.Join(root, "overlay")
	for _, v := range c.versions {
		createTestEbuildFile(t, overlay, s084Pkg, v)
	}
	pkgDir := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6")
	manifest := filepath.Join(pkgDir, "Manifest")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if c.published != nil {
		writePublishedManifest(t, overlay, *c.published)
		if c.mode != 0 {
			if err := os.Chmod(manifest, c.mode); err != nil {
				t.Fatal(err)
			}
		}
	}

	rec := &s062Recorder{}
	a, err := NewApplier(overlay, filepath.Join(root, "config"), WithApplierLogger(rec.logger()))
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	cand, err := stagedCandidate(filepath.Join(root, "staging"), s084Pkg, c.candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cand.pkgDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cand.ebuildPath, []byte("EAPI=8\n# validated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c.staged != nil {
		if err := os.WriteFile(filepath.Join(cand.pkgDir, "Manifest"), []byte(*c.staged), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	undo, err := a.promote(t.Context(), cand, s084Pkg, c.candidate)
	return s084Promoted{
		pkgDir:    pkgDir,
		manifest:  manifest,
		candidate: filepath.Join(pkgDir, "gst-plugins-qt6-"+c.candidate+".ebuild"),
		rec:       rec,
		undo:      undo,
		err:       err,
	}
}

func s084MustPromote(t *testing.T, c s084Case) s084Promoted {
	t.Helper()
	p := s084Promote(t, c)
	if p.err != nil {
		t.Fatalf("promote %s-%s: %v", s084Pkg, c.candidate, p.err)
	}
	return p
}

func s084AssertManifest(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the published Manifest after promotion: %v", err)
	}
	if string(got) != want {
		t.Errorf("published Manifest after promotion:\n  got  %q\n  want %q\n"+
			"every DIST record the published Manifest held for another version must survive the promotion — an ebuild "+
			"with no record for its archive does not install — and the candidate's record must be the staged one", got, want)
	}
}

func s084AssertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("published Manifest mode = %04o, want %04o", got, want)
	}
}

// s084Count finds the attribute whose key names word (kept, written, replaced)
// and returns its integer value.
func s084Count(rec map[string]any, word string) (int, bool) {
	for k, v := range rec {
		if !strings.Contains(strings.ToLower(k), word) {
			continue
		}
		if n, ok := v.(float64); ok {
			return int(n), true
		}
	}
	return 0, false
}

// s084MergeInfos returns the INFO events naming pkg and version that carry the
// three counts R2.1 lists.
func s084MergeInfos(t *testing.T, rec *s062Recorder, pkg, version string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range rec.records(t) {
		if r["level"] != "INFO" || r["package"] != pkg || r["version"] != version {
			continue
		}
		_, k := s084Count(r, "kept")
		_, w := s084Count(r, "written")
		_, p := s084Count(r, "replaced")
		if k && w && p {
			out = append(out, r)
		}
	}
	return out
}

// s084AssertMergeInfo requires exactly one merge INFO event with these counts.
func s084AssertMergeInfo(t *testing.T, rec *s062Recorder, version string, kept, written, replaced int) {
	t.Helper()
	infos := s084MergeInfos(t, rec, s084Pkg, version)
	if len(infos) != 1 {
		t.Fatalf("want exactly one INFO event carrying package=%s, version=%s and the kept/written/replaced counts "+
			"for the written Manifest, got %d; all records: %v", s084Pkg, version, len(infos), rec.records(t))
	}
	for word, want := range map[string]int{"kept": kept, "written": written, "replaced": replaced} {
		if got, _ := s084Count(infos[0], word); got != want {
			t.Errorf("merge INFO event: %s = %d, want %d (event %v)", word, got, want, infos[0])
		}
	}
}

// s084WarnsNaming returns the WARN events carrying path as an attribute value.
func s084WarnsNaming(t *testing.T, rec *s062Recorder, path string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range rec.records(t) {
		if r["level"] != "WARN" {
			continue
		}
		for k, v := range r {
			if k != "msg" && v == path {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// TestS084PromoteKeepsTheOtherVersionsRecords is the story's reproduction.
func TestS084PromoteKeepsTheOtherVersionsRecords(t *testing.T) {
	p := s084MustPromote(t, s084Case{
		versions:  []string{"1.28.6"},
		published: s084Str("DIST gst-plugins-qt6-1.28.6.tar.xz 100 BLAKE2B aa SHA512 bb\n"),
		staged:    s084Str("DIST gst-plugins-qt6-1.29.2.tar.xz 200 BLAKE2B cc SHA512 dd\n"),
		candidate: "1.29.2",
	})
	s084AssertManifest(t, p.manifest, "DIST gst-plugins-qt6-1.28.6.tar.xz 100 BLAKE2B aa SHA512 bb\n"+
		"DIST gst-plugins-qt6-1.29.2.tar.xz 200 BLAKE2B cc SHA512 dd\n")
	if _, err := os.Stat(filepath.Join(p.pkgDir, "gst-plugins-qt6-1.28.6.ebuild")); err != nil {
		t.Errorf("the published 1.28.6 ebuild is gone after promotion: %v", err)
	}
}

// TestS084PromoteKeepsEveryOlderSlotsRecords: a package that keeps two release
// lines on purpose loses neither when a third version is promoted.
func TestS084PromoteKeepsEveryOlderSlotsRecords(t *testing.T) {
	p := s084MustPromote(t, s084Case{
		versions: []string{"1.0", "3.0"},
		published: s084Str(s084Lines(
			"DIST gst-plugins-qt6-1.0.tar.xz 10 BLAKE2B a1 SHA512 a2",
			"DIST gst-plugins-qt6-3.0.tar.xz 30 BLAKE2B c1 SHA512 c2",
		)),
		staged:    s084Str("DIST gst-plugins-qt6-3.1.tar.xz 31 BLAKE2B d1 SHA512 d2\n"),
		candidate: "3.1",
	})
	s084AssertManifest(t, p.manifest, s084Lines(
		"DIST gst-plugins-qt6-1.0.tar.xz 10 BLAKE2B a1 SHA512 a2",
		"DIST gst-plugins-qt6-3.0.tar.xz 30 BLAKE2B c1 SHA512 c2",
		"DIST gst-plugins-qt6-3.1.tar.xz 31 BLAKE2B d1 SHA512 d2",
	))
}

// TestS084PromoteNearNamesSurviveAndASharedFilenameTakesTheStagedRecord: a
// revision bump shares its archive with the published version. Near-names of
// that archive are other files and must survive (wrong collapse); the shared
// one, published with other whitespace, is the same file and must come out as
// the staged record alone (wrong split).
func TestS084PromoteNearNamesSurviveAndASharedFilenameTakesTheStagedRecord(t *testing.T) {
	p := s084MustPromote(t, s084Case{
		versions: []string{"1.28.6"},
		published: s084Str(s084Lines(
			"DIST gst-plugins-qt6-1.28.6.tar.xz.asc 5 BLAKE2B s1 SHA512 s2",
			"\tDIST  gst-plugins-qt6-1.28.6.tar.xz 100 BLAKE2B old SHA512 old",
			"DIST gst-plugins-qt6-1.28.60.tar.xz 160 BLAKE2B n1 SHA512 n2",
		)),
		staged:    s084Str("DIST gst-plugins-qt6-1.28.6.tar.xz 100 BLAKE2B new SHA512 new\n"),
		candidate: "1.28.6-r1",
	})
	s084AssertManifest(t, p.manifest, s084Lines(
		"DIST gst-plugins-qt6-1.28.6.tar.xz 100 BLAKE2B new SHA512 new",
		"DIST gst-plugins-qt6-1.28.6.tar.xz.asc 5 BLAKE2B s1 SHA512 s2",
		"DIST gst-plugins-qt6-1.28.60.tar.xz 160 BLAKE2B n1 SHA512 n2",
	))
}

// TestS084PromoteWithNoPublishedManifestWritesTheStagedBytesUnchanged is R1.7
// and the new-file half of R3.3. The staged body is deliberately one a merge
// would rewrite (unsorted, a non-DIST record, blank lines), so a promotion that
// merges when there is nothing to merge with is caught. Green on today's code.
func TestS084PromoteWithNoPublishedManifestWritesTheStagedBytesUnchanged(t *testing.T) {
	staged := "EBUILD gst-plugins-qt6-1.29.2.ebuild 9 BLAKE2B e SHA512 e\n" +
		"DIST gst-plugins-qt6-1.29.2.tar.xz 200 BLAKE2B cc SHA512 dd\n\n" +
		"DIST gst-plugins-good-1.29.2.tar.xz 300 BLAKE2B ee SHA512 ff\n\n"
	p := s084MustPromote(t, s084Case{
		versions:  []string{"1.28.6"},
		staged:    s084Str(staged),
		candidate: "1.29.2",
	})
	s084AssertManifest(t, p.manifest, staged)
	s084AssertMode(t, p.manifest, publishedFileMode)
}

// TestS084PromoteWithNoStagedManifestLeavesThePublishedOneAlone is R3.1.
// Green on today's code.
func TestS084PromoteWithNoStagedManifestLeavesThePublishedOneAlone(t *testing.T) {
	published := "  DIST gst-plugins-qt6-1.28.6.tar.xz 100 BLAKE2B aa SHA512 bb\nMISC metadata.xml 1\n"
	p := s084MustPromote(t, s084Case{
		versions:  []string{"1.28.6"},
		published: s084Str(published),
		mode:      0o600,
		candidate: "1.29.2",
	})
	s084AssertManifest(t, p.manifest, published)
	s084AssertMode(t, p.manifest, 0o600)
}

// TestS084PromoteKeepsTheModeOfTheExistingManifest is R3.3 for a merged write.
// Green on today's code.
func TestS084PromoteKeepsTheModeOfTheExistingManifest(t *testing.T) {
	p := s084MustPromote(t, s084Case{
		versions:  []string{"1.28.6"},
		published: s084Str("DIST gst-plugins-qt6-1.28.6.tar.xz 100 BLAKE2B aa SHA512 bb\n"),
		mode:      0o640,
		staged:    s084Str("DIST gst-plugins-qt6-1.29.2.tar.xz 200 BLAKE2B cc SHA512 dd\n"),
		candidate: "1.29.2",
	})
	s084AssertMode(t, p.manifest, 0o640)
}

// TestS084UndoAfterAMergedPromotionRestoresThePublishedManifest is R3.2 on a
// merged write: a later step fails, and the Manifest comes back byte for byte
// (odd spacing and a non-DIST record included) with its mode, the candidate
// ebuild goes, the other version stays. Green on today's code.
func TestS084UndoAfterAMergedPromotionRestoresThePublishedManifest(t *testing.T) {
	published := "DIST  gst-plugins-qt6-1.28.6.tar.xz\t100 BLAKE2B aa SHA512 bb\nMISC metadata.xml 1 BLAKE2B m SHA512 m\n"
	p := s084MustPromote(t, s084Case{
		versions:  []string{"1.28.6"},
		published: s084Str(published),
		mode:      0o640,
		staged:    s084Str("DIST gst-plugins-qt6-1.29.2.tar.xz 200 BLAKE2B cc SHA512 dd\n"),
		candidate: "1.29.2",
	})
	p.undo(errors.New("a step after promotion failed"))

	s084AssertManifest(t, p.manifest, published)
	s084AssertMode(t, p.manifest, 0o640)
	if _, err := os.Stat(p.candidate); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the candidate %s survived the rollback (stat err %v); an ebuild no Manifest record covers is one --clean deletes", p.candidate, err)
	}
	if _, err := os.Stat(filepath.Join(p.pkgDir, "gst-plugins-qt6-1.28.6.ebuild")); err != nil {
		t.Errorf("the rollback removed the published 1.28.6 ebuild: %v", err)
	}
}

// TestS084PromoteLogsTheMergeCounts is R2.1 with three different counts, so a
// count logged under the wrong name is caught.
func TestS084PromoteLogsTheMergeCounts(t *testing.T) {
	p := s084MustPromote(t, s084Case{
		versions: []string{"1.28.6"},
		published: s084Str(s084Lines(
			"DIST a-1.tar.xz 1 BLAKE2B a SHA512 a",
			"DIST c-1.tar.xz 3 BLAKE2B c SHA512 c",
			"DIST x-1.tar.xz 9 BLAKE2B old SHA512 old",
		)),
		staged: s084Str(s084Lines(
			"DIST x-1.tar.xz 9 BLAKE2B new SHA512 new",
			"DIST b-1.tar.xz 2 BLAKE2B b SHA512 b",
			"DIST d-1.tar.xz 4 BLAKE2B d SHA512 d",
		)),
		candidate: "1.29.2",
	})
	s084AssertMergeInfo(t, p.rec, "1.29.2", 2, 3, 1)
}

// TestS084PromoteLogsZeroCounts is the "zeros included" half of R2.1.
func TestS084PromoteLogsZeroCounts(t *testing.T) {
	t.Run("no published Manifest", func(t *testing.T) {
		p := s084MustPromote(t, s084Case{
			versions: []string{"1.28.6"},
			staged: s084Str(s084Lines(
				"DIST gst-plugins-qt6-1.29.2.tar.xz 200 BLAKE2B cc SHA512 dd",
				"DIST gst-plugins-good-1.29.2.tar.xz 300 BLAKE2B ee SHA512 ff",
			)),
			candidate: "1.29.2",
		})
		s084AssertMergeInfo(t, p.rec, "1.29.2", 0, 2, 0)
	})
	t.Run("nothing replaced", func(t *testing.T) {
		p := s084MustPromote(t, s084Case{
			versions:  []string{"1.28.6"},
			published: s084Str("DIST gst-plugins-qt6-1.28.6.tar.xz 100 BLAKE2B aa SHA512 bb\n"),
			staged:    s084Str("DIST gst-plugins-qt6-1.29.2.tar.xz 200 BLAKE2B cc SHA512 dd\n"),
			candidate: "1.29.2",
		})
		s084AssertMergeInfo(t, p.rec, "1.29.2", 1, 1, 0)
	})
}

// TestS084PromoteDropsAMalformedPublishedRecordWithOneWarn is R1.6. Wrong-fire
// first: ordinary dotted names are kept and raise no WARN (the logger is proven
// wired by the merge INFO event). Then each refused shape, alone: not written,
// and exactly one WARN naming the package, the version and the Manifest path.
func TestS084PromoteDropsAMalformedPublishedRecordWithOneWarn(t *testing.T) {
	stagedLine := "DIST gst-plugins-qt6-1.29.2.tar.xz 200 BLAKE2B cc SHA512 dd"
	valid := "DIST gst-plugins-qt6-1.28.6.tar.xz 100 BLAKE2B aa SHA512 bb"

	t.Run("ordinary dotted names are kept without a warning", func(t *testing.T) {
		p := s084MustPromote(t, s084Case{
			versions: []string{"1.28.6"},
			published: s084Str(s084Lines(
				"DIST ... 1 BLAKE2B a SHA512 a",
				"DIST .foo 2 BLAKE2B b SHA512 b",
				"DIST ..foo 3 BLAKE2B c SHA512 c",
				"DIST foo..tar.gz 4 BLAKE2B d SHA512 d",
			)),
			staged:    s084Str(stagedLine + "\n"),
			candidate: "1.29.2",
		})
		s084AssertManifest(t, p.manifest, s084Lines(
			"DIST ... 1 BLAKE2B a SHA512 a",
			"DIST ..foo 3 BLAKE2B c SHA512 c",
			"DIST .foo 2 BLAKE2B b SHA512 b",
			"DIST foo..tar.gz 4 BLAKE2B d SHA512 d",
			stagedLine,
		))
		if len(s084MergeInfos(t, p.rec, s084Pkg, "1.29.2")) == 0 {
			t.Fatalf("no merge INFO event was recorded, so the logger is not wired and an absent WARN proves nothing; records: %v", p.rec.records(t))
		}
		if w := s084WarnsNaming(t, p.rec, p.manifest); len(w) != 0 {
			t.Errorf("ordinary file names raised %d WARN events naming the Manifest: %v", len(w), w)
		}
	})

	for _, bad := range []string{
		"DIST",
		"DIST . 1 BLAKE2B x SHA512 x",
		"DIST .. 1 BLAKE2B x SHA512 x",
		"DIST ../../etc/passwd 1 BLAKE2B x SHA512 x",
		"DIST sub/x.tar.xz 1 BLAKE2B x SHA512 x",
		"DIST sub\\x.tar.xz 1 BLAKE2B x SHA512 x",
	} {
		t.Run(fmt.Sprintf("refused %q", bad), func(t *testing.T) {
			p := s084MustPromote(t, s084Case{
				versions:  []string{"1.28.6"},
				published: s084Str(s084Lines(bad, valid)),
				staged:    s084Str(stagedLine + "\n"),
				candidate: "1.29.2",
			})
			s084AssertManifest(t, p.manifest, s084Lines(valid, stagedLine))
			warns := s084WarnsNaming(t, p.rec, p.manifest)
			if len(warns) != 1 {
				t.Fatalf("want exactly one WARN naming the Manifest %s for the dropped record, got %d; records: %v",
					p.manifest, len(warns), p.rec.records(t))
			}
			if warns[0]["package"] != s084Pkg || warns[0]["version"] != "1.29.2" {
				t.Errorf("the WARN must carry package=%s and version=1.29.2 (the version being promoted), got %v", s084Pkg, warns[0])
			}
		})
	}
}

// s084FakePkgdev answers `pkgdev manifest` as the real one does for a thin
// Manifest — one DIST record per ebuild in its working directory — and records
// that directory in calls. Every other command succeeds.
func s084FakePkgdev(calls string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	const script = `set -eu
pwd >> "$1"
{ for e in *.ebuild; do [ -e "$e" ] || continue; printf 'DIST %s.tar.xz 1 BLAKE2B 00 SHA512 00\n' "${e%.ebuild}"; done; } > Manifest
`
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" && len(arg) > 0 && arg[0] == "manifest" {
			return exec.CommandContext(ctx, "sh", "-c", script, "sh", calls)
		}
		return exec.CommandContext(ctx, "true")
	}
}

// TestS084ApplyKeepsTheCurrentVersionsRecords runs the whole staged apply: the
// staged `pkgdev manifest` writes the candidate's record alone, and the
// published Manifest must still hold the current version's.
func TestS084ApplyKeepsTheCurrentVersionsRecords(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "pkgdev-calls")
	rec := &s062Recorder{}
	a, overlayDir, pkg, _, _ := promoteFixture(t,
		WithExecCommand(s084FakePkgdev(calls)),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierLogger(rec.logger()),
	)

	result, err := a.Apply(t.Context(), pkg, false)
	if err != nil || !result.Success {
		t.Fatalf("apply failed: err=%v result.Error=%v", err, result.Error)
	}
	manifest := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "Manifest")
	s084AssertManifest(t, manifest, publishedManifestBody+
		"DIST gst-plugins-qt6-1.29.2.tar.xz 1 BLAKE2B 00 SHA512 00\n")
	s084AssertMergeInfo(t, rec, "1.29.2", 1, 1, 0)
}

// TestS084ApplyCleanPrunesTheRemovedVersionsRecords is R3.5: after the merge,
// --clean removes 1.28.6 and regenerates the Manifest in the published package
// directory, so the removed version's record is gone. Green on today's code.
func TestS084ApplyCleanPrunesTheRemovedVersionsRecords(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "pkgdev-calls")
	a, overlayDir, pkg, _, _ := promoteFixture(t,
		WithExecCommand(s084FakePkgdev(calls)),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierClean(true),
		WithApplierPackagesConfig(&registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
			s084Pkg: regEntry("", ""),
		}}),
	)

	result, err := a.Apply(t.Context(), pkg, false)
	if err != nil || !result.Success {
		t.Fatalf("apply failed: err=%v result.Error=%v", err, result.Error)
	}
	if result.CleanWarning != "" {
		t.Fatalf("--clean reported a warning, so the sweep did not run as this test needs: %s", result.CleanWarning)
	}
	pkgDir := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6")
	if _, err := os.Stat(filepath.Join(pkgDir, "gst-plugins-qt6-1.28.6.ebuild")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("--clean did not remove 1.28.6 (stat err %v)", err)
	}
	s084AssertManifest(t, filepath.Join(pkgDir, "Manifest"),
		"DIST gst-plugins-qt6-1.29.2.tar.xz 1 BLAKE2B 00 SHA512 00\n")

	ran, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("reading the pkgdev call log: %v", err)
	}
	inPublished := false
	for _, dir := range strings.Split(strings.TrimSpace(string(ran)), "\n") {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			dir = r
		}
		if p, err := filepath.EvalSymlinks(pkgDir); err == nil && dir == p {
			inPublished = true
		}
	}
	if !inPublished {
		t.Errorf("`pkgdev manifest` never ran in the published package directory %s; --clean must regenerate it there (ran in: %q)", pkgDir, ran)
	}
}
