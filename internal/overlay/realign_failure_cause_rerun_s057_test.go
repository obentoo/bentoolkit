package overlay

// Added at run time for story 057 after the group-3 tech review: the per-cause
// tally must describe the pass that just ran (a second pass must not inherit
// the first one's map), and a realignment cancelled mid-call counts as
// `cancelled` (R3.1) while the loop's own stop adds nothing (R6.9).

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/gentoo/repo"
)

// s057CancellingRealign cancels the run during its first call and answers every
// later call.
type s057CancellingRealign struct {
	cancel context.CancelFunc
	calls  int
}

func (r *s057CancellingRealign) ReviewRealignment(ctx context.Context, _ RealignRequest) (RealignNote, error) {
	r.calls++
	if r.calls == 1 {
		r.cancel()
		return RealignNote{}, fmt.Errorf("the realignment review failed: %w", ctx.Err())
	}
	return RealignNote{Justified: true, Why: "still ours to carry"}, nil
}

func s057RealignFixture(t *testing.T, atoms ...string) (*CompareReport, *localRootedFakeProvider, CompareOptions) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	overlayRoot, gentooRoot := t.TempDir(), t.TempDir()
	versions := map[string][]string{}
	pkgs := make([]repo.PackageInfo, 0, len(atoms))
	for i, atom := range atoms {
		cat, pkg := "s057", atom[len("s057/"):]
		writeVerifyEbuild(t, overlayRoot, cat, pkg, "1.0", fmt.Sprintf("%sDESCRIPTION=\"ours %d\"\n", realignOurs, i))
		writeVerifyEbuild(t, gentooRoot, cat, pkg, "1.0", fmt.Sprintf("%sDESCRIPTION=\"theirs %d\"\n", realignBaseline, i))
		versions[atom] = []string{"1.0"}
		pkgs = append(pkgs, repo.PackageInfo{Category: cat, Package: pkg, Versions: []string{"1.0"}, LatestVersion: "1.0"})
	}
	prov := &localRootedFakeProvider{root: gentooRoot, versions: versions}
	opts := CompareOptions{IncludeSynced: true, IncludeNotInRemote: true, OverlayPath: overlayRoot}
	report, err := CompareWithProvider(t.Context(), pkgs, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v", err)
	}
	AnnotateBaseline(report, prov, opts)
	return report, prov, opts
}

func TestRealignNoVerdictByDescribesTheLastPass(t *testing.T) {
	report, prov, opts := s057RealignFixture(t, "s057/a", "s057/b")

	AnnotateRealignVerdicts(t.Context(), report, &s057RealignByAtom{errs: map[string]error{
		"s057/a": fmt.Errorf("the realignment review failed: %w", ErrReviewTimedOut),
	}}, prov, opts)
	if got := s057CountsByCause(t, report.RealignNoVerdictBy); got["timed out"] != 1 || report.RealignNoVerdict != 1 {
		t.Fatalf("the fixture is wrong: first pass gave NoVerdict %d, by %v", report.RealignNoVerdict, got)
	}

	AnnotateRealignVerdicts(t.Context(), report, &s057RealignByAtom{}, prov, opts)
	if report.RealignNoVerdict != 0 {
		t.Fatalf("the fixture is wrong: second pass left NoVerdict %d", report.RealignNoVerdict)
	}
	if got := s057CountsByCause(t, report.RealignNoVerdictBy); len(got) != 0 {
		t.Errorf("RealignNoVerdictBy = %v after a pass with nothing unanswered; it must sum to RealignNoVerdict (0)", got)
	}
}

func TestRealignCancelledMidCallCountsAsCancelled(t *testing.T) {
	report, prov, opts := s057RealignFixture(t, "s057/a", "s057/b", "s057/c")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rev := &s057CancellingRealign{cancel: cancel}
	AnnotateRealignVerdicts(ctx, report, rev, prov, opts)

	if rev.calls != 1 {
		t.Fatalf("the loop asked %d times after the run was cancelled; want 1", rev.calls)
	}
	if report.RealignNoVerdict != 1 {
		t.Errorf("RealignNoVerdict %d, want 1: only the in-flight call is unanswered, the stop itself counts nothing (R6.9)", report.RealignNoVerdict)
	}
	if got := s057CountsByCause(t, report.RealignNoVerdictBy); len(got) != 1 || got["cancelled"] != 1 {
		t.Errorf("RealignNoVerdictBy = %v, want map[cancelled:1] (R3.1)", got)
	}
}
