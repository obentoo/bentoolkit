package autoupdate

// Authored for story 089, sub-task 1.1 — R1, R2, R5, R7.6, R7.7.
//
// The bump reviewer must find the two release archives it compares wherever
// this run can see them: the candidate's own fetch directory, the --distdir,
// and the distfiles cache — in that order, passing over directories that are
// unset, missing, or where the choice is ambiguous. The previous version is
// matched by its version with the revision stripped (1.0-r1 -> m-1.0.tar.gz),
// and never by a neighbouring version that merely contains it (m-1.0.1).
// When no archive is found the reviewer is not asked at all; the review gate is
// SKIPPED and its reason names the side, the version and every directory read.
//
// Driven through (*Applier).reviewBump with a recording reviewer that never
// opens the archives, so plain files with the right names suffice.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

const s089aPkg = "app-misc/m"

// s089aRecorder is a BumpReviewer that records every request it is handed.
type s089aRecorder struct {
	mu    sync.Mutex
	calls []fixer.BumpReviewRequest
}

func (r *s089aRecorder) ReviewBump(_ context.Context, req fixer.BumpReviewRequest) (fixer.BumpReviewReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	return fixer.BumpReviewReport{Skipped: true, SkipReason: "recorded"}, nil
}

type s089aFixture struct {
	overlay string
	staged  string // staged package directory
	fetched string // candidate fetch directory
	distdir string // --distdir
	cache   string // distfiles cache
	rec     *s089aRecorder
	a       *Applier
}

func s089aManifest(names []string) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString("DIST " + n + " 1 BLAKE2B x SHA512 y\n")
	}
	return b.String()
}

func s089aPut(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// s089aNew builds a published app-misc/m-1.0-r1 whose Manifest lists published,
// and a staged m-2.0 whose Manifest lists staged. distdirMissing points
// --distdir at a directory that does not exist.
func s089aNew(t *testing.T, published, staged []string, distdirMissing bool) *s089aFixture {
	t.Helper()
	root := t.TempDir()
	f := &s089aFixture{
		overlay: filepath.Join(root, "overlay"),
		staged:  filepath.Join(root, "staged", "app-misc", "m"),
		fetched: filepath.Join(root, "fetched"),
		distdir: filepath.Join(root, "distdir"),
		cache:   filepath.Join(root, "cache"),
		rec:     &s089aRecorder{},
	}
	pub := filepath.Join(f.overlay, "app-misc", "m")
	s089aPut(t, pub, "m-1.0-r1.ebuild", "EAPI=8\n")
	s089aPut(t, pub, "Manifest", s089aManifest(published))
	s089aPut(t, f.staged, "m-2.0.ebuild", "EAPI=8\n")
	s089aPut(t, f.staged, "Manifest", s089aManifest(staged))
	for _, d := range []string{f.fetched, f.cache} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if distdirMissing {
		f.distdir = filepath.Join(root, "no-such-distdir")
	} else if err := os.MkdirAll(f.distdir, 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := NewApplier(f.overlay, filepath.Join(root, "config"),
		WithApplierBumpReviewer(f.rec),
		WithApplierDistdir(f.distdir, ""),
		WithApplierDistfilesCache(f.cache),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	f.a = a
	return f
}

func (f *s089aFixture) review(t *testing.T, fetchedDistdir string) []validate.GateResult {
	t.Helper()
	var gates []validate.GateResult
	cand := candidatePaths{
		staged:         true,
		repoRoot:       filepath.Dir(filepath.Dir(f.staged)),
		pkgDir:         f.staged,
		ebuildPath:     filepath.Join(f.staged, "m-2.0.ebuild"),
		fetchedDistdir: fetchedDistdir,
	}
	f.a.reviewBump(context.Background(), cand, s089aPkg, "1.0-r1", "2.0", validate.DepthDecision{}, &gates)
	return gates
}

func (f *s089aFixture) onlyCall(t *testing.T) fixer.BumpReviewRequest {
	t.Helper()
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()
	if len(f.rec.calls) != 1 {
		t.Fatalf("reviewer called %d times, want 1", len(f.rec.calls))
	}
	return f.rec.calls[0]
}

// R1.1: the candidate archive is only where this run fetched it.
func TestS089ArchiveCandidateFromFetchedDistdir(t *testing.T) {
	f := s089aNew(t, []string{"m-1.0.tar.gz"}, []string{"m-2.0.tar.gz"}, false)
	s089aPut(t, f.distdir, "m-1.0.tar.gz", "old")
	want := s089aPut(t, f.fetched, "m-2.0.tar.gz", "new")
	f.review(t, f.fetched)
	if got := f.onlyCall(t).NewArchive; got != want {
		t.Errorf("NewArchive = %q, want %q (the candidate's fetch directory)", got, want)
	}
}

// R7.6 (converse: wrongly split): the current version carries a revision the
// distfile name never does.
func TestS089ArchivePreviousRevisionedVersion(t *testing.T) {
	f := s089aNew(t, []string{"m-1.0.tar.gz"}, []string{"m-2.0.tar.gz"}, false)
	want := s089aPut(t, f.distdir, "m-1.0.tar.gz", "old")
	s089aPut(t, f.distdir, "m-2.0.tar.gz", "new")
	f.review(t, f.fetched)
	if got := f.onlyCall(t).OldArchive; got != want {
		t.Errorf("OldArchive = %q, want %q (1.0-r1 ships m-1.0.tar.gz)", got, want)
	}
}

// R1.2: the previous archive survives only in the distfiles cache.
func TestS089ArchivePreviousFromCache(t *testing.T) {
	f := s089aNew(t, []string{"m-1.0.tar.gz"}, []string{"m-2.0.tar.gz"}, false)
	want := s089aPut(t, f.cache, "m-1.0.tar.gz", "old")
	s089aPut(t, f.fetched, "m-2.0.tar.gz", "new")
	f.review(t, f.fetched)
	if got := f.onlyCall(t).OldArchive; got != want {
		t.Errorf("OldArchive = %q, want %q (the distfiles cache)", got, want)
	}
}

// R2.1: the fetch directory is read before --distdir.
func TestS089ArchiveFetchedDistdirWinsOverDistdir(t *testing.T) {
	f := s089aNew(t, []string{"m-1.0.tar.gz"}, []string{"m-2.0.tar.gz"}, false)
	s089aPut(t, f.distdir, "m-1.0.tar.gz", "old")
	s089aPut(t, f.distdir, "m-2.0.tar.gz", "stale bytes in distdir")
	want := s089aPut(t, f.fetched, "m-2.0.tar.gz", "fresh bytes just fetched")
	f.review(t, f.fetched)
	if got := f.onlyCall(t).NewArchive; got != want {
		t.Errorf("NewArchive = %q, want %q (fetch directory before --distdir)", got, want)
	}
}

// R7.7 (hostile half, wrongly collapse): m-1.0.1 contains "1.0" but is not 1.0.
func TestS089ArchiveNeverPicksDecoyVersion(t *testing.T) {
	published := []string{"m-1.0.tar.gz", "m-1.0.1.tar.gz"}

	t.Run("decoy beside the real archive", func(t *testing.T) {
		f := s089aNew(t, published, []string{"m-2.0.tar.gz"}, false)
		realPath := s089aPut(t, f.distdir, "m-1.0.tar.gz", "old")
		decoy := s089aPut(t, f.distdir, "m-1.0.1.tar.gz", "decoy")
		cached := s089aPut(t, f.cache, "m-1.0.tar.gz", "old")
		s089aPut(t, f.fetched, "m-2.0.tar.gz", "new")
		f.review(t, f.fetched)
		got := f.onlyCall(t).OldArchive
		if got == decoy {
			t.Fatalf("OldArchive = decoy %q", got)
		}
		if got != realPath && got != cached {
			t.Errorf("OldArchive = %q, want %q or %q", got, realPath, cached)
		}
	})

	// A third element: the decoy is the ONLY Manifest name --distdir holds.
	t.Run("decoy alone in the directory", func(t *testing.T) {
		f := s089aNew(t, published, []string{"m-2.0.tar.gz"}, false)
		decoy := s089aPut(t, f.distdir, "m-1.0.1.tar.gz", "decoy")
		cached := s089aPut(t, f.cache, "m-1.0.tar.gz", "old")
		s089aPut(t, f.fetched, "m-2.0.tar.gz", "new")
		f.review(t, f.fetched)
		got := f.onlyCall(t).OldArchive
		if got == decoy {
			t.Fatalf("OldArchive = decoy %q", got)
		}
		if got != cached {
			t.Errorf("OldArchive = %q, want %q (the cache, past a directory holding only a neighbour version)", got, cached)
		}
	})
}

// R2.2: a directory where the choice is ambiguous is passed over.
func TestS089ArchiveAmbiguousDirPassedOver(t *testing.T) {
	f := s089aNew(t, []string{"m-1.0.tar.gz", "m-1.0-docs.tar.gz"}, []string{"m-2.0.tar.gz"}, false)
	s089aPut(t, f.distdir, "m-1.0.tar.gz", "old")
	s089aPut(t, f.distdir, "m-1.0-docs.tar.gz", "docs")
	want := s089aPut(t, f.cache, "m-1.0.tar.gz", "old")
	s089aPut(t, f.fetched, "m-2.0.tar.gz", "new")
	f.review(t, f.fetched)
	if got := f.onlyCall(t).OldArchive; got != want {
		t.Errorf("OldArchive = %q, want %q (ambiguous --distdir passed over for the cache)", got, want)
	}
}

// R1.3: an unset fetch directory and a missing --distdir are passed over.
func TestS089ArchiveUnsetAndMissingDirsPassedOver(t *testing.T) {
	f := s089aNew(t, []string{"m-1.0.tar.gz"}, []string{"m-2.0.tar.gz"}, true)
	wantOld := s089aPut(t, f.cache, "m-1.0.tar.gz", "old")
	wantNew := s089aPut(t, f.cache, "m-2.0.tar.gz", "new")
	f.review(t, "")
	req := f.onlyCall(t)
	if req.OldArchive != wantOld {
		t.Errorf("OldArchive = %q, want %q", req.OldArchive, wantOld)
	}
	if req.NewArchive != wantNew {
		t.Errorf("NewArchive = %q, want %q", req.NewArchive, wantNew)
	}
}

// R5.1/R5.2: nothing found -> no reviewer call, a SKIPPED review gate whose
// reason names the side, its version and every directory searched.
func TestS089ArchiveNeitherFoundSkipsWithSearchedDirs(t *testing.T) {
	f := s089aNew(t, []string{"m-1.0.tar.gz"}, []string{"m-2.0.tar.gz"}, false)
	gates := f.review(t, f.fetched)

	f.rec.mu.Lock()
	calls := len(f.rec.calls)
	f.rec.mu.Unlock()
	if calls != 0 {
		t.Errorf("reviewer called %d times with no archive on disk, want 0", calls)
	}

	var review []validate.GateResult
	for _, g := range gates {
		if g.Gate == validate.GateReview {
			review = append(review, g)
		}
	}
	if len(review) != 1 {
		t.Fatalf("review gates = %+v, want exactly one", review)
	}
	g := review[0]
	if g.Outcome != validate.OutcomeSkipped {
		t.Errorf("review gate outcome = %q, want %q", g.Outcome, validate.OutcomeSkipped)
	}
	reason := g.Reason
	dirs := []string{f.distdir, f.cache}
	switch {
	case strings.Contains(reason, "candidate"):
		if !strings.Contains(reason, "2.0") {
			t.Errorf("reason %q names the candidate but not its version 2.0", reason)
		}
		dirs = append(dirs, f.fetched)
	case strings.Contains(reason, "previous"):
		if !strings.Contains(reason, "1.0") {
			t.Errorf("reason %q names the previous version but not 1.0", reason)
		}
	default:
		t.Errorf("reason %q names neither the candidate nor the previous archive", reason)
	}
	for _, d := range dirs {
		if !strings.Contains(reason, d) {
			t.Errorf("reason %q does not name searched directory %q", reason, d)
		}
	}
}
