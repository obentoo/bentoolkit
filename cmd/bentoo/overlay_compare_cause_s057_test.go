package main

// Authored for story 057, sub-task 4.2 — R3.2, R4.1, R4.4, R4.5, R4.6, R4.7
// (R6.2 and R6.4 are pinned by TestBuildCompareReport, TestCompareComplete and
// TestCompareNotEvaluated; this file re-checks R6.4 on its own fixture).
//
// Contract names used here: ComparePkg.Cause and ComparePkg.Error (4.1), the
// "reading_failures" JSON key (4.1), CompareOptions.Redact (1.3), the overlay
// review sentinels (3.1), CompareReport's per-cause realign tally (3.2, reached
// only through compareRealignNote).
//
// The report is built by the REAL pipeline — CompareWithProvider, AnnotateReviews,
// AnnotateRealignVerdicts — over ebuilds on disk, never by hand, so the causes on
// it are the ones overlay really records.
//
// RED ON ARRIVAL: Cause, Error, Redact and the review sentinels do not exist.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/gentoo/repo"

	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

const s057Token = "ghp_TESTTOKEN0000"

// s057CmdProvider is reviewDirProvider plus lookups that fail.
type s057CmdProvider struct {
	*reviewDirProvider
	errs map[string]error
}

func (p *s057CmdProvider) GetPackageVersions(ctx context.Context, category, pkg string) ([]string, error) {
	if err, ok := p.errs[category+"/"+pkg]; ok {
		return nil, err
	}
	return p.reviewDirProvider.GetPackageVersions(ctx, category, pkg)
}

type s057CmdReviewer struct{ errs map[string]error }

func (r *s057CmdReviewer) ReviewDivergence(_ context.Context, req overlay.ReviewRequest) (overlay.ReviewNote, error) {
	if err, ok := r.errs[req.Category+"/"+req.Package]; ok {
		return overlay.ReviewNote{}, err
	}
	return overlay.ReviewNote{Origin: overlay.OriginOverlay, Summary: "adds a patch ::gentoo does not ship"}, nil
}

type s057CmdRealigner struct {
	errs  map[string]error
	notes map[string]overlay.RealignNote
}

func (r *s057CmdRealigner) ReviewRealignment(_ context.Context, req overlay.RealignRequest) (overlay.RealignNote, error) {
	atom := req.Category + "/" + req.Package
	if err, ok := r.errs[atom]; ok {
		return overlay.RealignNote{}, err
	}
	if n, ok := r.notes[atom]; ok {
		return n, nil
	}
	return overlay.RealignNote{Justified: true, Why: "still ours to carry"}, nil
}

var (
	s057Diverging = []string{"t1", "t2", "t3", "cns", "fine"}
	// Multi-line on purpose: the report must fold it without losing a word.
	errS057TimedOut = fmt.Errorf("the divergence review failed: %w: its 90s budget\nelapsed before it answered", overlay.ErrReviewTimedOut)
	errS057NoStart  = fmt.Errorf("the divergence review failed: %w: fork/exec claude: permission denied", overlay.ErrReviewCouldNotStart)
	errS057Limited  = fmt.Errorf("%w: GET https://api.github.com/x?access_token=%s\r\nresets at 1790000000", provider.ErrRateLimit, s057Token)
)

// s057Compare runs the real comparison over five undeclared divergences (and,
// when withLookupError, one package whose lookup is rate-limited), then the
// review pass with rev (nil = --no-review).
func s057Compare(t *testing.T, withLookupError bool, rev overlay.DivergenceReviewer) (*overlay.CompareReport, provider.Provider, overlay.CompareOptions) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	base := &reviewDirProvider{root: upstreamRoot, versions: map[string][]string{}}
	prov := &s057CmdProvider{reviewDirProvider: base, errs: map[string]error{}}
	opts := overlay.CompareOptions{
		IncludeSynced: true,
		OverlayPath:   overlayRoot,
		Divergence:    map[string]overlay.Divergence{},
		Redact:        []string{s057Token},
	}
	var pkgs []repo.PackageInfo
	for i, name := range s057Diverging {
		writeReviewEbuild(t, overlayRoot, "s057", name, "1.0", fmt.Sprintf("%sDESCRIPTION=\"ours %d\"\n", cmdReviewOurs, i))
		writeReviewEbuild(t, upstreamRoot, "s057", name, "1.0", fmt.Sprintf("%sDESCRIPTION=\"theirs %d\"\n", cmdReviewTheirs, i))
		base.versions["s057/"+name] = []string{"1.0"}
		opts.Divergence["s057/"+name] = overlay.Divergence{}
		pkgs = append(pkgs, repo.PackageInfo{Category: "s057", Package: name, Versions: []string{"1.0"}, LatestVersion: "1.0"})
	}
	if withLookupError {
		prov.errs["s057/limited"] = errS057Limited
		pkgs = append(pkgs, repo.PackageInfo{Category: "s057", Package: "limited", LatestVersion: "1.0"})
	}
	rep, err := overlay.CompareWithProvider(t.Context(), pkgs, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v", err)
	}
	overlay.AnnotateReviews(t.Context(), rep, rev, prov, opts)
	return rep, prov, opts
}

func s057FailingReviewer() *s057CmdReviewer {
	return &s057CmdReviewer{errs: map[string]error{
		"s057/t1": errS057TimedOut, "s057/t2": errS057TimedOut, "s057/t3": errS057TimedOut, "s057/cns": errS057NoStart,
	}}
}

// s057Rows collects every row the payload lists, keyed by atom.
func s057Rows(t *testing.T, run report.Run) map[string]report.ComparePkg {
	t.Helper()
	p := compareComparePayload(t, run)
	rows := map[string]report.ComparePkg{}
	for _, list := range [][]report.ComparePkg{p.Redundant, p.NeedsRebase, p.Keep, p.Unknown} {
		for _, r := range list {
			rows[r.Package] = r
		}
	}
	return rows
}

func s057Row(t *testing.T, rows map[string]report.ComparePkg, atom string) report.ComparePkg {
	t.Helper()
	r, ok := rows[atom]
	if !ok {
		t.Fatalf("the fixture is wrong: %s is on no list of the payload (%d rows)", atom, len(rows))
	}
	return r
}

// s057AssertFolded: one line, no word lost, token scrubbed.
func s057AssertFolded(t *testing.T, atom, got, original string) {
	t.Helper()
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("%s: error %q holds a line break (R4.6)", atom, got)
	}
	want := strings.Fields(strings.ReplaceAll(original, s057Token, "***"))
	if g := strings.Fields(got); !reflect.DeepEqual(g, want) {
		t.Errorf("%s: error words\n got: %q\nwant: %q (R4.6 folds, never drops; R4.7 scrubs)", atom, g, want)
	}
}

func TestBuildCompareReportCarriesCauseAndError(t *testing.T) {
	rep, _, _ := s057Compare(t, true, s057FailingReviewer())
	rows := s057Rows(t, buildCompareReport(rep, "gentoo", nil))

	// Hostile: the lookup cause is not a reading failure, and a read row carries nothing.
	lim := s057Row(t, rows, "s057/limited")
	if lim.Status != "error" || lim.Cause != "rate-limited" {
		t.Errorf("s057/limited: status %q cause %q, want \"error\" and \"rate-limited\" (R4.4)", lim.Status, lim.Cause)
	}
	if lim.Reading == "failed" {
		t.Errorf("s057/limited: a failed LOOKUP reads as a failed review")
	}
	if !strings.HasPrefix(lim.Reason, "the upstream lookup failed (rate-limited): ") {
		t.Errorf("s057/limited: reason %q, want the lookup sentence of R4.1", lim.Reason)
	}
	s057AssertFolded(t, "s057/limited", lim.Error, errS057Limited.Error())

	fine := s057Row(t, rows, "s057/fine")
	if fine.Reading != "read" || fine.Cause != "" || fine.Error != "" {
		t.Errorf("s057/fine: reading %q cause %q error %q, want \"read\" and both empty (R4.4)", fine.Reading, fine.Cause, fine.Error)
	}

	// Benign.
	for atom, want := range map[string]struct {
		cause string
		err   error
	}{"s057/t1": {"timed out", errS057TimedOut}, "s057/cns": {"could not start", errS057NoStart}} {
		r := s057Row(t, rows, atom)
		if r.Reading != "failed" || r.Cause != want.cause {
			t.Errorf("%s: reading %q cause %q, want \"failed\" and %q (R4.4, R6.2)", atom, r.Reading, r.Cause, want.cause)
		}
		s057AssertFolded(t, atom, r.Error, want.err.Error())
	}
}

func TestBuildCompareReportTalliesReadingFailures(t *testing.T) {
	rep, _, _ := s057Compare(t, false, s057FailingReviewer())
	run := buildCompareReport(rep, "gentoo", nil)
	p := compareComparePayload(t, run)

	var doc struct {
		ReadingFailures []struct {
			Cause string `json:"cause"`
			Count int    `json:"count"`
		} `json:"reading_failures"`
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("reading_failures is not a list of {cause,count}: %v", err)
	}
	got, sum := map[string]int{}, 0
	for _, e := range doc.ReadingFailures {
		got[e.Cause] += e.Count
		sum += e.Count
	}
	if want := map[string]int{"timed out": 3, "could not start": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("reading_failures = %v, want %v (R4.5)", got, want)
	}
	if p.Unread != 4 || sum != p.Unread {
		t.Errorf("reading_failures sums to %d and unread is %d; with every unread row a failed review both must be 4 (R4.5: same population)", sum, p.Unread)
	}

	// The baseline is the same run with CAUSE-LESS failures (every error "other"):
	// the counts may depend on whether a review failed, never on why (R6.4).
	t.Run("no verdict, count, Complete or NotEvaluated moves because a cause is recorded", func(t *testing.T) {
		boom := errors.New("boom")
		quiet, _, _ := s057Compare(t, false, &s057CmdReviewer{errs: map[string]error{
			"s057/t1": boom, "s057/t2": boom, "s057/t3": boom, "s057/cns": boom}})
		base := buildCompareReport(quiet, "gentoo", nil)
		bp := compareComparePayload(t, base)
		if run.Complete != base.Complete || run.NotEvaluated != base.NotEvaluated {
			t.Errorf("Complete/NotEvaluated %v/%d vs %v/%d with cause-less failures (R6.4)", run.Complete, run.NotEvaluated, base.Complete, base.NotEvaluated)
		}
		if !reflect.DeepEqual(p.Verdicts, bp.Verdicts) || p.Unread != bp.Unread || p.Scanned != bp.Scanned {
			t.Errorf("verdicts %+v unread %d vs %+v unread %d with cause-less failures", p.Verdicts, p.Unread, bp.Verdicts, bp.Unread)
		}
	})
}

func TestCompareRealignNoteCountsPerCause(t *testing.T) {
	rep, prov, opts := s057Compare(t, false, nil)
	overlay.AnnotateBaseline(rep, prov, opts)
	rev := &s057CmdRealigner{
		errs: map[string]error{
			"s057/t1": fmt.Errorf("the realignment review failed: %w", overlay.ErrReviewTimedOut),
			"s057/t2": fmt.Errorf("the realignment review failed: %w", overlay.ErrReviewTimedOut),
		},
		notes: map[string]overlay.RealignNote{"s057/t3": {Justified: true, Why: " "}},
	}
	overlay.AnnotateRealignVerdicts(t.Context(), rep, rev, prov, opts)
	if rep.RealignAsked != 5 || rep.RealignNoVerdict != 3 {
		t.Fatalf("the fixture is wrong: RealignAsked %d, RealignNoVerdict %d; want 5 and 3", rep.RealignAsked, rep.RealignNoVerdict)
	}

	note := compareRealignNote(rep, true, true, false)
	if !strings.Contains(note, "3 of the 5 divergences") {
		t.Errorf("the existing sentence is gone: %q", note)
	}
	if !strings.Contains(note, "(2 timed out, 1 empty or unusable reply)") {
		t.Errorf("the realign note does not break the 3 down by cause (R3.2):\n%s", note)
	}
}

func TestCompareRedactsTheToken(t *testing.T) {
	rep, _, _ := s057Compare(t, true, s057FailingReviewer())
	var buf bytes.Buffer
	if err := render.JSON(&buf, buildCompareReport(rep, "gentoo", nil)); err != nil {
		t.Fatalf("render.JSON: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, s057Token) {
		t.Errorf("the token reached the JSON (R4.7)")
	}
	if !strings.Contains(out, "access_token=***") {
		t.Errorf("the scrubbed error text is not in the JSON; want it with the token as *** (R4.7)")
	}
}
