package overlay

// Authored for story 057, sub-task 3.2 — R3.1 (R6.9 is pinned by
// TestRealignVerdictDecidesNothing and TestRealignVerdictUnreachableModelSaysSo).
//
// Contract names used here: CompareReport.RealignNoVerdictBy and the review
// sentinels ErrReviewTimedOut (3.1). The per-cause tally is read by reflection —
// a map keyed by the cause, or a list of {Cause, Count} — and every cause through
// fmt.Sprint, so the test does not pin a container shape story.md never names.
//
// Hostile first: a timed-out realignment and an unanswered one that merely
// returned an error must not be merged into one bucket, an unreadable pair must
// not be counted as a failed call, and a divergence that WAS judged must
// contribute nothing.
//
// RED ON ARRIVAL: RealignNoVerdictBy and ErrReviewTimedOut do not exist.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/obentoo/bentoolkit/internal/gentoo/repo"
)

// s057RealignByAtom answers per atom; an atom absent from both maps is judged.
type s057RealignByAtom struct {
	errs  map[string]error
	notes map[string]RealignNote
}

func (r *s057RealignByAtom) ReviewRealignment(_ context.Context, req RealignRequest) (RealignNote, error) {
	atom := req.Category + "/" + req.Package
	if err, ok := r.errs[atom]; ok {
		return RealignNote{}, err
	}
	if n, ok := r.notes[atom]; ok {
		return n, nil
	}
	return RealignNote{Justified: true, Why: "still ours to carry"}, nil
}

// s057CountsByCause flattens the tally into word -> count.
func s057CountsByCause(t *testing.T, tally any) map[string]int {
	t.Helper()
	out := map[string]int{}
	v := reflect.ValueOf(tally)
	switch v.Kind() {
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			out[fmt.Sprint(it.Key().Interface())] += int(it.Value().Int())
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			e := reflect.Indirect(v.Index(i))
			c, n := e.FieldByName("Cause"), e.FieldByName("Count")
			if !c.IsValid() || !n.IsValid() {
				t.Fatalf("RealignNoVerdictBy is a list of %s with no Cause/Count fields", e.Type())
			}
			out[fmt.Sprint(c.Interface())] += int(n.Int())
		}
	default:
		t.Fatalf("RealignNoVerdictBy is a %s; want a map keyed by cause or a list of {Cause, Count}", v.Type())
	}
	for k, n := range out {
		if n == 0 {
			delete(out, k)
		}
	}
	return out
}

func TestRealignNoVerdictIsCountedPerCause(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	overlayRoot, gentooRoot := t.TempDir(), t.TempDir()
	atoms := []string{"s057/timeout-a", "s057/timeout-b", "s057/failed", "s057/silent", "s057/unreadable", "s057/judged"}
	versions := map[string][]string{}
	pkgs := make([]repo.PackageInfo, 0, len(atoms))
	for i, atom := range atoms {
		cat, pkg := "s057", atom[len("s057/"):]
		// Every pair distinct, so each is its own question and its own cache key.
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

	// The baseline is resolved first so one pair can be made unreadable AFTER it
	// was found, which is the only way realignRequestFor meets a missing file.
	AnnotateBaseline(report, prov, opts)
	for _, r := range report.Results {
		if !r.Baseline.Found {
			t.Fatalf("the fixture is wrong: %s/%s has no baseline, so it would never be put to the model", r.Category, r.Package)
		}
		if r.Package == "unreadable" {
			if err := os.Remove(r.Baseline.Path); err != nil {
				t.Fatalf("the fixture is wrong: removing %s: %v", r.Baseline.Path, err)
			}
		}
	}

	rev := &s057RealignByAtom{
		errs: map[string]error{
			"s057/timeout-a": fmt.Errorf("the realignment review failed: %w", ErrReviewTimedOut),
			"s057/timeout-b": fmt.Errorf("the realignment review failed: %w", ErrReviewTimedOut),
			// Says "ran out of time" but carries no sentinel: it must NOT join the
			// timed-out bucket.
			"s057/failed": errors.New("the realignment review failed: claude CLI ran out of time"),
		},
		notes: map[string]RealignNote{"s057/silent": {Justified: true, Why: "   "}},
	}
	AnnotateRealignVerdicts(t.Context(), report, rev, prov, opts)

	if report.RealignAsked != 6 || report.RealignNoVerdict != 5 {
		t.Fatalf("the fixture is wrong: RealignAsked %d, RealignNoVerdict %d; want 6 and 5 (five unanswered, one judged)",
			report.RealignAsked, report.RealignNoVerdict)
	}

	got := s057CountsByCause(t, report.RealignNoVerdictBy)
	want := map[string]int{
		"timed out":               2,
		"other":                   1,
		"empty or unusable reply": 1,
		"ebuild unreadable":       1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RealignNoVerdictBy = %v, want %v (R3.1: unreadable pair, classified error, silent note)", got, want)
	}

	sum := 0
	for _, n := range got {
		sum += n
	}
	if sum != report.RealignNoVerdict {
		t.Errorf("the per-cause counts sum to %d, but RealignNoVerdict is %d: the breakdown must cover exactly the unanswered population", sum, report.RealignNoVerdict)
	}
}
