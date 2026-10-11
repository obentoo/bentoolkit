package autoupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// s089sSkipReviewer records every review it is asked for and answers a
// non-skipped, empty report.
type s089sSkipReviewer struct {
	mu    sync.Mutex
	calls []fixer.BumpReviewRequest
}

func (r *s089sSkipReviewer) ReviewBump(_ context.Context, req fixer.BumpReviewRequest) (fixer.BumpReviewReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	return fixer.BumpReviewReport{}, nil
}

func (r *s089sSkipReviewer) recorded() []fixer.BumpReviewRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]fixer.BumpReviewRequest(nil), r.calls...)
}

// s089sSpawns records the name of every child process the applier creates.
type s089sSpawns struct {
	mu    sync.Mutex
	names []string
}

func (s *s089sSpawns) seam(ctx context.Context, name string, _ ...string) *exec.Cmd {
	s.mu.Lock()
	s.names = append(s.names, name)
	s.mu.Unlock()
	return exec.CommandContext(ctx, "true")
}

func (s *s089sSpawns) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.names...)
}

func (s *s089sSpawns) count(name string) int {
	n := 0
	for _, got := range s.all() {
		if got == name {
			n++
		}
	}
	return n
}

// s089sSkipTarGz writes a gzip-compressed tar archive with exactly the given
// members (member path -> content) into dir.
func s089sSkipTarGz(t *testing.T, dir, name string, members map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(members))
	for n := range members {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body := members[n]
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header %s: %v", n, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body %s: %v", n, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

const (
	s089sSourceEbuild = "EAPI=8\nDESCRIPTION=\"a source package\"\ninherit meson\nLICENSE=\"MIT\"\nSLOT=\"0\"\n"
	s089sBinaryEbuild = "EAPI=8\nDESCRIPTION=\"a prebuilt package\"\nRESTRICT=\"bindist mirror strip\"\n" +
		"QA_PREBUILT=\"*\"\nLICENSE=\"all-rights-reserved\"\nSLOT=\"0\"\n"
	s089sBinaryReason = "binary package: there are no upstream build declarations to review"
)

// s089sFixture is one bump of pkg from oldV to newV: a published ebuild for
// oldV (with its Manifest), a staged candidate for newV (with its Manifest), an
// empty configured distdir and the private distdir the manifest step fetched
// into.
type s089sFixture struct {
	overlay, distdir, fetched string
	pkg, oldV, newV           string
	cand                      candidatePaths
	reviewer                  *s089sSkipReviewer
	spawns                    *s089sSpawns
	applier                   *Applier
}

func s089sNewFixture(t *testing.T, pkg, oldV, newV, ebuild, publishedManifest, stagedManifest string, wireReviewer, staged bool) *s089sFixture {
	t.Helper()
	tmp := t.TempDir()
	f := &s089sFixture{
		overlay:  filepath.Join(tmp, "overlay"),
		distdir:  filepath.Join(tmp, "distdir"),
		fetched:  filepath.Join(tmp, "fetched"),
		pkg:      pkg,
		oldV:     oldV,
		newV:     newV,
		reviewer: &s089sSkipReviewer{},
		spawns:   &s089sSpawns{},
	}
	for _, d := range []string{f.distdir, f.fetched} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	createTestEbuildFileWithContent(t, f.overlay, pkg, oldV, ebuild)
	parts := strings.SplitN(pkg, "/", 2)
	publishedDir := filepath.Join(f.overlay, parts[0], parts[1])
	if err := os.WriteFile(filepath.Join(publishedDir, "Manifest"), []byte(publishedManifest), 0o644); err != nil {
		t.Fatalf("writing the published Manifest: %v", err)
	}

	stagingRoot := filepath.Join(tmp, "staging")
	stagedPkgDir := filepath.Join(stagingRoot, parts[0], parts[1])
	if err := os.MkdirAll(stagedPkgDir, 0o755); err != nil {
		t.Fatalf("creating the staged package dir: %v", err)
	}
	stagedEbuild := filepath.Join(stagedPkgDir, parts[1]+"-"+newV+".ebuild")
	if err := os.WriteFile(stagedEbuild, []byte(ebuild), 0o644); err != nil {
		t.Fatalf("writing the staged ebuild: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagedPkgDir, "Manifest"), []byte(stagedManifest), 0o644); err != nil {
		t.Fatalf("writing the staged Manifest: %v", err)
	}
	f.cand = candidatePaths{
		staged:         staged,
		repoRoot:       stagingRoot,
		pkgDir:         stagedPkgDir,
		ebuildPath:     stagedEbuild,
		fetchedDistdir: f.fetched,
	}

	opts := []ApplierOption{
		WithExecCommand(f.spawns.seam),
		WithApplierDistdir(f.distdir, ""),
	}
	if wireReviewer {
		opts = append(opts, WithApplierBumpReviewer(f.reviewer))
	}
	a, err := NewApplier(f.overlay, filepath.Join(tmp, "config"), opts...)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	f.applier = a
	return f
}

func (f *s089sFixture) review(t *testing.T) (validate.DepthDecision, []validate.GateResult) {
	t.Helper()
	var gates []validate.GateResult
	floor := validate.DepthDecision{Depth: validate.DepthOptions, Reason: "s089 floor"}
	got := f.applier.reviewBump(t.Context(), f.cand, f.pkg, f.oldV, f.newV, floor, &gates)
	if got != floor {
		t.Errorf("reviewBump depth = %+v, want the floor %+v untouched", got, floor)
	}
	return got, gates
}

func s089sReviewGates(gates []validate.GateResult) []validate.GateResult {
	var out []validate.GateResult
	for _, g := range gates {
		if g.Gate == validate.GateReview {
			out = append(out, g)
		}
	}
	return out
}

// R3 (binary): a prebuilt package has no upstream build declarations, so the
// reviewer is not asked, nothing is fetched, and the skip says why in those
// words — not as a missing archive.
func TestS089SkipBinaryPackageIsNotReviewed(t *testing.T) {
	f := s089sNewFixture(t, "app-misc/prebuilt", "1.0", "1.1", s089sBinaryEbuild,
		"DIST prebuilt-1.0.deb 100 BLAKE2B ab SHA512 cd\n",
		"DIST prebuilt-1.1.deb 100 BLAKE2B ef SHA512 01\n",
		true, true)

	_, gates := f.review(t)

	if calls := f.reviewer.recorded(); len(calls) != 0 {
		t.Errorf("the reviewer was asked %d time(s) about a binary package; want 0: %+v", len(calls), calls)
	}
	review := s089sReviewGates(gates)
	if len(review) != 1 {
		t.Fatalf("got %d review gate(s) %+v, want exactly one SKIPPED review gate", len(review), gates)
	}
	if review[0].Outcome != validate.OutcomeSkipped {
		t.Errorf("review gate outcome = %v, want %v", review[0].Outcome, validate.OutcomeSkipped)
	}
	if !strings.Contains(review[0].Reason, s089sBinaryReason) {
		t.Errorf("review gate reason = %q, want it to contain %q", review[0].Reason, s089sBinaryReason)
	}
	if n := f.spawns.count("ebuild"); n != 0 {
		t.Errorf("an `ebuild` child was spawned %d time(s) for a binary package; nothing must be fetched", n)
	}
}

// R3 (non-Meson): the candidate archive the manifest step fetched is CMake, and
// the previous archive is absent everywhere. The skip names the detected
// system and says only Meson projects are reviewed; the reviewer is not asked
// and the previous side is not fetched.
func TestS089SkipCMakeCandidateNamesItsBuildSystem(t *testing.T) {
	f := s089sNewFixture(t, "dev-libs/cmk", "1.0", "2.0", s089sSourceEbuild,
		"DIST cmk-1.0.tar.gz 100 BLAKE2B ab SHA512 cd\n",
		"DIST cmk-2.0.tar.gz 100 BLAKE2B ef SHA512 01\n",
		true, true)
	s089sSkipTarGz(t, f.fetched, "cmk-2.0.tar.gz", map[string]string{
		"cmk-2.0/CMakeLists.txt": "project(cmk C)\n",
		"cmk-2.0/src/cmk.c":      "int cmk;\n",
	})

	_, gates := f.review(t)

	if calls := f.reviewer.recorded(); len(calls) != 0 {
		t.Errorf("the reviewer was asked %d time(s) about a CMake project; want 0: %+v", len(calls), calls)
	}
	review := s089sReviewGates(gates)
	if len(review) != 1 {
		t.Fatalf("got %d review gate(s) %+v, want exactly one SKIPPED review gate", len(review), gates)
	}
	g := review[0]
	if g.Outcome != validate.OutcomeSkipped {
		t.Errorf("review gate outcome = %v, want %v", g.Outcome, validate.OutcomeSkipped)
	}
	if !strings.Contains(g.Reason, "cmake") {
		t.Errorf("review gate reason = %q, want it to name the detected build system %q", g.Reason, "cmake")
	}
	if !strings.Contains(g.Reason, "Meson") {
		t.Errorf("review gate reason = %q, want it to say only Meson projects are reviewed", g.Reason)
	}
	if strings.Contains(g.Reason, "binary package") {
		t.Errorf("review gate reason = %q calls a source package binary", g.Reason)
	}
	if n := f.spawns.count("ebuild"); n != 0 {
		t.Errorf("an `ebuild` child was spawned %d time(s); the previous side must not be fetched for a non-Meson candidate", n)
	}
}

// Converse of the build-system skip: a Meson candidate that vendors a CMake
// tree is still a Meson project, and a source package is not a binary one.
// Neither skip may fire; the reviewer is asked.
func TestS089SkipMesonCandidateIsReviewed(t *testing.T) {
	f := s089sNewFixture(t, "media-libs/msn", "1.0", "2.0", s089sSourceEbuild,
		"DIST msn-1.0.tar.gz 100 BLAKE2B ab SHA512 cd\n",
		"DIST msn-2.0.tar.gz 100 BLAKE2B ef SHA512 01\n",
		true, true)
	mesonMembers := func(v string) map[string]string {
		root := "msn-" + v + "/"
		return map[string]string{
			root + "meson.build":                         "project('msn', 'c')\n",
			root + "meson_options.txt":                   "option('x', type: 'boolean', value: true)\n",
			root + "subprojects/vendored/CMakeLists.txt": "project(vendored C)\n",
		}
	}
	// Both sides on disk where today's lookup and the fetched distdir find them,
	// so this test is about the skip decision only, not about locating archives.
	s089sSkipTarGz(t, f.distdir, "msn-1.0.tar.gz", mesonMembers("1.0"))
	s089sSkipTarGz(t, f.distdir, "msn-2.0.tar.gz", mesonMembers("2.0"))
	s089sSkipTarGz(t, f.fetched, "msn-2.0.tar.gz", mesonMembers("2.0"))

	_, gates := f.review(t)

	if calls := f.reviewer.recorded(); len(calls) != 1 {
		t.Fatalf("the reviewer was asked %d time(s) about a Meson project; want exactly 1", len(calls))
	}
	for _, g := range s089sReviewGates(gates) {
		if strings.Contains(g.Reason, "binary package") {
			t.Errorf("review gate %+v calls a source package binary", g)
		}
		if g.Outcome == validate.OutcomeSkipped && strings.Contains(g.Reason, "Meson") {
			t.Errorf("review gate %+v skipped a Meson project as non-Meson", g)
		}
	}
}

// R7.1 guard: with no reviewer wired, review does nothing at all — no search,
// no fetch, no gate about archives. Not a Red test: holds today.
func TestS089SkipNoReviewerSearchesNothing(t *testing.T) {
	f := s089sNewFixture(t, "dev-libs/cmk", "1.0", "2.0", s089sSourceEbuild,
		"DIST cmk-1.0.tar.gz 100 BLAKE2B ab SHA512 cd\n",
		"DIST cmk-2.0.tar.gz 100 BLAKE2B ef SHA512 01\n",
		false, true)
	s089sSkipTarGz(t, f.fetched, "cmk-2.0.tar.gz", map[string]string{"cmk-2.0/CMakeLists.txt": "project(cmk C)\n"})

	_, gates := f.review(t)

	if len(gates) != 0 {
		t.Errorf("with no reviewer wired, reviewBump appended gates %+v; want none", gates)
	}
	if spawned := f.spawns.all(); len(spawned) != 0 {
		t.Errorf("with no reviewer wired, reviewBump spawned %v; want nothing searched or fetched", spawned)
	}
}

// R7.2 guard: a candidate that was not staged is never reviewed, whatever its
// archive is. Not a Red test: holds today.
func TestS089SkipUnstagedCandidateIsNotReviewed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pkg    string
		ebuild string
	}{
		{"source", "dev-libs/cmk", s089sSourceEbuild},
		{"binary", "app-misc/cmk", s089sBinaryEbuild},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := s089sNewFixture(t, tc.pkg, "1.0", "2.0", tc.ebuild,
				"DIST cmk-1.0.tar.gz 100 BLAKE2B ab SHA512 cd\n",
				"DIST cmk-2.0.tar.gz 100 BLAKE2B ef SHA512 01\n",
				true, false)
			s089sSkipTarGz(t, f.fetched, "cmk-2.0.tar.gz", map[string]string{"cmk-2.0/CMakeLists.txt": "project(cmk C)\n"})

			_, gates := f.review(t)

			if calls := f.reviewer.recorded(); len(calls) != 0 {
				t.Errorf("an unstaged candidate was reviewed %d time(s); want 0", len(calls))
			}
			if len(gates) != 0 {
				t.Errorf("an unstaged candidate got gates %+v; want none", gates)
			}
			if spawned := f.spawns.all(); len(spawned) != 0 {
				t.Errorf("an unstaged candidate spawned %v; want nothing searched or fetched", spawned)
			}
		})
	}
}
