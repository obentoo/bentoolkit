package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// Story 047, sub-task 3.1 — S047-R1.1, S047-R1.3.
//
// What these assert is the TRANSLATION and nothing else: the envelope's two
// fixed values, the four words each of the two enums this package spells, the
// closed Diff vocabulary, which list each verdict lands in, and that no slice
// reaches the payload nil. Every one of them is a fact a JSON consumer reads
// directly, so an assertion phrased over rendered text would be pinning a
// renderer instead.
//
// The four reading words and the four diff cells are asserted LITERALLY rather
// than against a constant. They are spelled in two packages — this adapter
// produces them and internal/common/report counts them to build the redundant
// section's lead — and the constants on the other side are unexported. A typo
// in either half shows up there as a count of zero rather than as a failure,
// which is exactly the failure a literal here catches.

// compareFixtureResult is one result with the fields this adapter reads, so a
// subtest below states only what it is about.
func compareFixtureResult(category, pkg string, verdict overlay.Verdict) overlay.CompareResult {
	return overlay.CompareResult{
		Category:      category,
		Package:       pkg,
		LocalVersion:  "1.0.0",
		RemoteVersion: "1.0.0",
		Status:        overlay.StatusUpToDate,
		Verdict:       verdict,
	}
}

func TestBuildCompareReport(t *testing.T) {
	t.Run("the envelope is fixed per command and not per run", func(t *testing.T) {
		run := buildCompareReport(&overlay.CompareReport{}, "gentoo", nil)

		if run.Schema != report.SchemaVersion {
			t.Errorf("schema = %d, want %d", run.Schema, report.SchemaVersion)
		}
		if run.Kind != report.KindOverlayCompare {
			t.Errorf("kind = %q, want %q", run.Kind, report.KindOverlayCompare)
		}
		if run.Title != "Overlay comparison" {
			t.Errorf("title = %q, want %q", run.Title, "Overlay comparison")
		}
		if !run.Complete {
			t.Error("a run that was not interrupted reports Complete false")
		}
		if run.NotEvaluated != 0 {
			t.Errorf("NotEvaluated = %d, want 0", run.NotEvaluated)
		}
	})

	t.Run("Complete negates Interrupted and reads no count", func(t *testing.T) {
		// The trap `func buildManifestReport` states: a run cut short after its
		// LAST package has a gap of zero and was still cut short, so Complete
		// must negate the recorded fact rather than ask whether anything is
		// missing. Every count below is the count of a finished run.
		rep := &overlay.CompareReport{
			TotalPackages:    1,
			ComparedPackages: 1,
			Interrupted:      true,
			Results:          []overlay.CompareResult{compareFixtureResult("dev-lang", "go", overlay.VerdictKeep)},
		}

		if run := buildCompareReport(rep, "gentoo", nil); run.Complete {
			t.Error("an interrupted run reports Complete true")
		}
	})

	t.Run("a nil report is an empty run, not a crash", func(t *testing.T) {
		run := buildCompareReport(nil, "gentoo", nil)

		if !run.Complete {
			t.Error("a nil report claims to have been cut short")
		}
		payload, ok := run.Payload.(report.CompareRun)
		if !ok {
			t.Fatalf("payload is %T, want report.CompareRun", run.Payload)
		}
		if payload.Repository != "gentoo" {
			t.Errorf("repository = %q, want %q", payload.Repository, "gentoo")
		}
		assertCompareSlicesNonNil(t, payload)
	})

	t.Run("every slice reaches the payload non-nil", func(t *testing.T) {
		// S047-D8: render/json.go refuses to normalize, so a nil slice reaches
		// the wire as null and an empty one as [], and the two say different
		// things — "the producer established nothing" against "the run found
		// nothing". A run with rows in one list must still carry [] in the rest.
		rep := &overlay.CompareReport{
			Results: []overlay.CompareResult{compareFixtureResult("dev-lang", "go", overlay.VerdictKeep)},
		}

		assertCompareSlicesNonNil(t, compareComparePayload(t, buildCompareReport(rep, "gentoo", nil)))
	})

	t.Run("an empty list reaches the WIRE as [] and never as null", func(t *testing.T) {
		// The subtest above proves the payload's slices are non-nil. This one
		// proves the consequence survives the encoder, which is the claim
		// S047-D8 actually makes and the only one a consumer can observe.
		//
		// The two are not the same assertion, and the gap between them is where
		// this would break: the normalisation lives at this layer, in
		// `func buildCompareReport`, and render/json.go is documented as
		// REFUSING to do it. So a future edit that dropped the seeding would
		// leave every guard in internal/common/report green — its own goldens
		// are taken over populated fixtures, where no empty list occurs — and
		// publish `"keep": null` to every reader of --export. That is the
		// residual gap sub-task 5.2 named and could not close from inside the
		// render package.
		//
		// Taken over a report with NO results at all, because that is the run
		// where every list is empty at once and the one a wire contract is most
		// likely to be read on: an overlay with nothing to say.
		var buf strings.Builder
		if err := render.JSON(&buf, buildCompareReport(&overlay.CompareReport{}, "gentoo", nil)); err != nil {
			t.Fatalf("render.JSON returned an error: %v", err)
		}

		got := buf.String()
		for _, key := range []string{"redundant", "needs_rebase", "keep", "keep_groups", "unknown"} {
			if strings.Contains(got, `"`+key+`": null`) {
				t.Errorf("%q reached the wire as null; an empty list must be [], because null says the producer "+
					"established nothing and [] says the run found nothing (S047-D8)\n%s", key, got)
			}
			if !strings.Contains(got, `"`+key+`": []`) {
				t.Errorf("%q is not published as [] on a run with no results; the list is either absent or "+
					"some other shape\n%s", key, got)
			}
		}
	})

	t.Run("each verdict lands in its own list", func(t *testing.T) {
		rep := &overlay.CompareReport{Results: []overlay.CompareResult{
			compareFixtureResult("app-editors", "vim", overlay.VerdictRedundant),
			compareFixtureResult("dev-lang", "go", overlay.VerdictKeep),
			compareFixtureResult("dev-libs", "nettle", overlay.VerdictNeedsRebase),
			compareFixtureResult("sys-kernel", "gentoo-kernel", overlay.VerdictUnknown),
		}}

		payload := compareComparePayload(t, buildCompareReport(rep, "gentoo", nil))
		for _, tc := range []struct {
			list []report.ComparePkg
			name string
			atom string
		}{
			{payload.Redundant, "Redundant", "app-editors/vim"},
			{payload.Keep, "Keep", "dev-lang/go"},
			{payload.NeedsRebase, "NeedsRebase", "dev-libs/nettle"},
			{payload.Unknown, "Unknown", "sys-kernel/gentoo-kernel"},
		} {
			if len(tc.list) != 1 {
				t.Errorf("%s holds %d package(s), want 1", tc.name, len(tc.list))
				continue
			}
			if tc.list[0].Package != tc.atom {
				t.Errorf("%s holds %q, want %q", tc.name, tc.list[0].Package, tc.atom)
			}
		}
	})

	t.Run("the counts come from the producer's own counters", func(t *testing.T) {
		// Nothing here is subtracted from anything else. InBoth is the three
		// statuses that required a remote version to be read, summed; OnlyLocal
		// is the producer's own not-in-remote counter; the tally mirrors the
		// four verdict counters one for one.
		rep := &overlay.CompareReport{
			TotalPackages:           279,
			ComparedPackages:        279,
			OutdatedCount:           40,
			NewerCount:              130,
			UpToDateCount:           10,
			NotInRemoteCount:        99,
			VerdictKeepCount:        258,
			VerdictRedundantCount:   11,
			VerdictNeedsRebaseCount: 0,
			VerdictUnknownCount:     10,
		}

		payload := compareComparePayload(t, buildCompareReport(rep, "gentoo", nil))
		if payload.Scanned != 279 {
			t.Errorf("Scanned = %d, want 279", payload.Scanned)
		}
		if payload.InBoth != 180 {
			t.Errorf("InBoth = %d, want 180", payload.InBoth)
		}
		if payload.OnlyLocal != 99 {
			t.Errorf("OnlyLocal = %d, want 99", payload.OnlyLocal)
		}
		want := report.VerdictTally{Keep: 258, Redundant: 11, NeedsRebase: 0, Unknown: 10}
		if payload.Verdicts != want {
			t.Errorf("Verdicts = %+v, want %+v", payload.Verdicts, want)
		}
	})

	t.Run("the four reading states each cross as their own word", func(t *testing.T) {
		for _, tc := range []struct {
			reading overlay.Reading
			want    string
		}{
			{overlay.ReadingNotRequested, "not requested"},
			{overlay.ReadingNotComparable, "not comparable"},
			{overlay.ReadingFailed, "failed"},
			{overlay.ReadingDone, "read"},
		} {
			result := compareFixtureResult("dev-lang", "go", overlay.VerdictKeep)
			result.Reading = tc.reading
			rep := &overlay.CompareReport{Results: []overlay.CompareResult{result}}

			payload := compareComparePayload(t, buildCompareReport(rep, "gentoo", nil))
			if got := payload.Keep[0].Reading; got != tc.want {
				t.Errorf("Reading %v crossed as %q, want %q", tc.reading, got, tc.want)
			}
		}
	})

	t.Run("Unread counts every state except read", func(t *testing.T) {
		rep := &overlay.CompareReport{Results: []overlay.CompareResult{
			compareFixtureResultReading("a-cat", "one", overlay.ReadingDone),
			compareFixtureResultReading("a-cat", "two", overlay.ReadingFailed),
			compareFixtureResultReading("a-cat", "three", overlay.ReadingNotComparable),
			compareFixtureResultReading("a-cat", "four", overlay.ReadingNotRequested),
		}}

		if got := compareComparePayload(t, buildCompareReport(rep, "gentoo", nil)).Unread; got != 3 {
			t.Errorf("Unread = %d, want 3", got)
		}
	})

	t.Run("the diff cell is drawn from the closed vocabulary", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			verified overlay.Verification
			added    int
			removed  int
			want     string
		}{
			{"a measured difference states its magnitude", overlay.VerifiedDiffers, 24, 0, "+24/-0"},
			{"a byte-identical pair", overlay.VerifiedIdentical, 0, 0, "identical"},
			{"a check that could not run", overlay.NotVerified, 0, 0, "not compared"},
		} {
			result := compareFixtureResult("dev-lang", "go", overlay.VerdictKeep)
			result.Verified, result.DiffAdded, result.DiffRemoved = tc.verified, tc.added, tc.removed
			rep := &overlay.CompareReport{Results: []overlay.CompareResult{result}}

			if got := compareComparePayload(t, buildCompareReport(rep, "gentoo", nil)).Keep[0].Diff; got != tc.want {
				t.Errorf("%s: Diff = %q, want %q", tc.name, got, tc.want)
			}
		}
	})

	t.Run("the status crosses through the producer's own String", func(t *testing.T) {
		result := compareFixtureResult("dev-lang", "go", overlay.VerdictKeep)
		result.Status = overlay.StatusOutdated
		rep := &overlay.CompareReport{Results: []overlay.CompareResult{result}}

		if got := compareComparePayload(t, buildCompareReport(rep, "gentoo", nil)).Keep[0].Status; got != "outdated" {
			t.Errorf("Status = %q, want %q", got, "outdated")
		}
	})

	t.Run("the reason is the package's own finding, folded to one line", func(t *testing.T) {
		// FindingCompared restates Status and is skipped; the loud entry beside
		// it is what the row has to say. The fold is what keeps a raw newline
		// out of a Row.Detail, which corrupts both the plain writer and the
		// Markdown pipe table.
		rep := &overlay.CompareReport{
			Results: []overlay.CompareResult{compareFixtureResult("dev-lang", "go", overlay.VerdictRedundant)},
			Findings: []overlay.Finding{
				{Kind: overlay.FindingBaselineSkipped, Detail: "no ::gentoo tree was reached"},
				{Kind: overlay.FindingCompared, Atom: "dev-lang/go", Detail: "the overlay and ::gentoo both carry 1.0.0"},
				{Kind: overlay.FindingUndeclaredDivergence, Atom: "dev-lang/go", Detail: "undeclared divergence\n(+24/-0)"},
				{Kind: overlay.FindingDeclaredDivergence, Atom: "dev-lang/go", Detail: "patched — declared by dev-lang/go"},
			},
		}

		payload := compareComparePayload(t, buildCompareReport(rep, "gentoo", nil))
		if got, want := payload.Redundant[0].Reason, "undeclared divergence (+24/-0)"; got != want {
			t.Errorf("Reason = %q, want %q", got, want)
		}
	})

	t.Run("a package with no finding of its own carries no reason", func(t *testing.T) {
		// Load-bearing rather than incidental: GroupKeep refuses to absorb a
		// package whose Reason is non-empty, so carrying the FindingCompared
		// restatement here would leave every run with zero groups (S047-R2.2).
		rep := &overlay.CompareReport{
			Results: []overlay.CompareResult{compareFixtureResult("dev-lang", "go", overlay.VerdictKeep)},
			Findings: []overlay.Finding{
				{Kind: overlay.FindingCompared, Atom: "dev-lang/go", Detail: "the overlay and ::gentoo both carry 1.0.0"},
			},
		}

		if got := compareComparePayload(t, buildCompareReport(rep, "gentoo", nil)).Keep[0].Reason; got != "" {
			t.Errorf("Reason = %q, want empty", got)
		}
	})

	t.Run("the keep groups are filled, which is the call 2.3 left owing", func(t *testing.T) {
		// S047-D2: GroupKeep is a function and not a method, so an empty
		// KeepGroups would otherwise mean either "no version pair repeats" or
		// "nobody ran the rule". Two members share a pair and carry no finding;
		// the third is a pair of one and stays an ordinary row (S047-R2.5).
		keep := func(pkg, local string) overlay.CompareResult {
			r := compareFixtureResult("media-plugins", pkg, overlay.VerdictKeep)
			r.LocalVersion, r.RemoteVersion = local, "1.26.11"
			r.Status = overlay.StatusNewer
			return r
		}
		rep := &overlay.CompareReport{Results: []overlay.CompareResult{
			keep("gst-plugins-base", "1.29.2"),
			keep("gst-plugins-good", "1.29.2"),
			keep("gst-plugins-ugly", "1.28.6"),
		}}

		payload := compareComparePayload(t, buildCompareReport(rep, "gentoo", nil))
		if len(payload.Keep) != 3 {
			t.Fatalf("Keep holds %d package(s), want 3 — a group is a view over the list, never a replacement for it", len(payload.Keep))
		}
		if len(payload.KeepGroups) != 1 {
			t.Fatalf("KeepGroups holds %d group(s), want 1", len(payload.KeepGroups))
		}
		if got := len(payload.KeepGroups[0].Members); got != 2 {
			t.Errorf("the group holds %d member(s), want 2", got)
		}
	})
}

// compareFixtureResultReading is one kept package in a stated reading state.
func compareFixtureResultReading(category, pkg string, reading overlay.Reading) overlay.CompareResult {
	result := compareFixtureResult(category, pkg, overlay.VerdictKeep)
	result.Reading = reading
	return result
}

// compareComparePayload takes the payload out of the envelope, failing the test
// rather than panicking when it is not the one this adapter builds.
func compareComparePayload(t *testing.T, run report.Run) report.CompareRun {
	t.Helper()

	payload, ok := run.Payload.(report.CompareRun)
	if !ok {
		t.Fatalf("payload is %T, want report.CompareRun", run.Payload)
	}
	return payload
}

// assertCompareSlicesNonNil states S047-D8 over every slice the payload
// carries: null and [] reach the reader exactly as written and say different
// things, and the adapter is the producer's side of that boundary.
func assertCompareSlicesNonNil(t *testing.T, payload report.CompareRun) {
	t.Helper()

	if payload.Redundant == nil {
		t.Error("Redundant is nil, and would reach the JSON export as null")
	}
	if payload.NeedsRebase == nil {
		t.Error("NeedsRebase is nil, and would reach the JSON export as null")
	}
	if payload.Keep == nil {
		t.Error("Keep is nil, and would reach the JSON export as null")
	}
	if payload.Unknown == nil {
		t.Error("Unknown is nil, and would reach the JSON export as null")
	}
	if payload.KeepGroups == nil {
		t.Error("KeepGroups is nil, and would reach the JSON export as null")
	}
}

// Story 047, sub-task 3.2 — S047-R4.3, S047-R5.1, S047-R5.2, S047-R5.3.
//
// The two below are one rule asserted from its two sides: Complete is whether
// the run established what it set out to, and NotEvaluated is how much it did
// not. They are separate tests because they fail for different reasons — a
// Complete that ignores a killed review is a wrong ANSWER, while a NotEvaluated
// that counts one failed package twice is a wrong NUMBER over the right answer,
// and a machine reader acts on both.
//
// Every fixture states TotalPackages and ComparedPackages explicitly, including
// where both are zero. The gap between them is one of the two populations the
// count sums, so a fixture that leaves them at the zero value would be asserting
// the other population while silently depending on this one.

// compareFixtureResultStatus is one package in a stated status, for the arms
// where the LOOKUP is what failed rather than the reading.
func compareFixtureResultStatus(category, pkg string, status overlay.CompareStatus) overlay.CompareResult {
	result := compareFixtureResult(category, pkg, overlay.VerdictUnknown)
	result.Status = status
	return result
}

func TestCompareComplete(t *testing.T) {
	t.Run("a run that reached everything and read it is complete", func(t *testing.T) {
		rep := &overlay.CompareReport{
			TotalPackages:    2,
			ComparedPackages: 2,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingDone),
				compareFixtureResultReading("dev-lang", "rust", overlay.ReadingDone),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if !run.Complete {
			t.Error("a run that reached every package and read every difference reports itself cut short (S047-R5.1)")
		}
		if run.NotEvaluated != 0 {
			t.Errorf("NotEvaluated = %d over a run with nothing unestablished, want 0 (S047-R5.2)", run.NotEvaluated)
		}
	})

	t.Run("an interrupted run is incomplete even with no gap and nothing unread", func(t *testing.T) {
		// The fact, not the count. A run cut short after its LAST package has a
		// gap of zero and every row read, and was still cut short — so
		// Interrupted is OR-ed in rather than inferred (S047-R5.1).
		rep := &overlay.CompareReport{
			TotalPackages:    1,
			ComparedPackages: 1,
			Interrupted:      true,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingDone),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if run.Complete {
			t.Error("an interrupted run whose last package happened to finish reports Complete true (S047-R5.1)")
		}
		if run.NotEvaluated != 0 {
			t.Errorf("NotEvaluated = %d, want 0: this run left no fact unestablished, it was stopped (S047-R5.2)", run.NotEvaluated)
		}
	})

	t.Run("a killed review makes a finished run incomplete", func(t *testing.T) {
		// The run reached every package and was never interrupted. What it did
		// not establish is the DIFFERENCE on one of them (S047-R4.3).
		rep := &overlay.CompareReport{
			TotalPackages:    2,
			ComparedPackages: 2,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingDone),
				compareFixtureResultReading("dev-lang", "rust", overlay.ReadingFailed),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if run.Complete {
			t.Error("a run whose review was killed reports Complete true, so a machine reader trusts a recommendation nobody backed (S047-R5.1)")
		}
		if run.NotEvaluated != 1 {
			t.Errorf("NotEvaluated = %d over one killed review, want 1 (S047-R4.3)", run.NotEvaluated)
		}
	})

	t.Run("a version pair the content check refused makes a run incomplete", func(t *testing.T) {
		rep := &overlay.CompareReport{
			TotalPackages:    1,
			ComparedPackages: 1,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingNotComparable),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if run.Complete {
			t.Error("a run whose content check refused the pair reports Complete true (S047-R5.1)")
		}
		if run.NotEvaluated != 1 {
			t.Errorf("NotEvaluated = %d over one refused pair, want 1 (S047-R5.2)", run.NotEvaluated)
		}
	})

	t.Run("a lookup that errored makes a run incomplete", func(t *testing.T) {
		rep := &overlay.CompareReport{
			TotalPackages:    2,
			ComparedPackages: 2,
			ErrorCount:       1,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingDone),
				compareFixtureResultStatus("dev-lang", "rust", overlay.StatusError),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if run.Complete {
			t.Error("a run whose API lookup errored reports Complete true (S047-R5.1)")
		}
		if run.NotEvaluated != 1 {
			t.Errorf("NotEvaluated = %d over one errored lookup, want 1 (S047-R5.2)", run.NotEvaluated)
		}
	})

	t.Run("--no-review leaves the run complete", func(t *testing.T) {
		// The trap. Every row sits at ReadingNotRequested, which is the zero
		// value, and the operator put it there by asking for no review. A
		// NotEvaluated built from the set CompareRun.Unread counts would report
		// this deliberate run as holed (S047-R5.3).
		rep := &overlay.CompareReport{
			TotalPackages:    3,
			ComparedPackages: 3,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingNotRequested),
				compareFixtureResultReading("dev-lang", "rust", overlay.ReadingNotRequested),
				compareFixtureResultReading("dev-lang", "zig", overlay.ReadingNotRequested),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if !run.Complete {
			t.Error("a --no-review run reports itself incomplete, so a choice the operator made reads as a shortfall (S047-R5.3)")
		}
		if run.NotEvaluated != 0 {
			t.Errorf("NotEvaluated = %d over a run nobody asked to review, want 0 (S047-R5.3)", run.NotEvaluated)
		}
	})

	t.Run("a narrowed view leaves the run complete", func(t *testing.T) {
		// What --only-redundant hands this function: the producer's counters
		// untouched, and a Results holding the one row the operator asked to
		// see. A count taken from len(Results) against the tally would answer 4
		// here and report a filtered run as cut short (S047-R5.3).
		rep := &overlay.CompareReport{
			TotalPackages:    5,
			ComparedPackages: 5,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingDone),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if !run.Complete {
			t.Error("a run narrowed by a filter flag reports itself cut short, so the operator's own choice reads as a shortfall (S047-R5.3)")
		}
		if run.NotEvaluated != 0 {
			t.Errorf("NotEvaluated = %d over a filtered run that evaluated all 5, want 0 (S047-R5.3)", run.NotEvaluated)
		}
	})

	t.Run("a nil report is complete, not cut short", func(t *testing.T) {
		run := buildCompareReport(nil, "gentoo", nil)

		if !run.Complete {
			t.Error("a nil report claims to have been cut short (S047-R5.1)")
		}
		if run.NotEvaluated != 0 {
			t.Errorf("NotEvaluated = %d over a run that planned nothing, want 0 (S047-R5.2)", run.NotEvaluated)
		}
	})
}

func TestCompareNotEvaluated(t *testing.T) {
	t.Run("the never-reached gap is TotalPackages minus ComparedPackages", func(t *testing.T) {
		// An interrupted scan that dispatched four of ten workers. The six that
		// never ran left no row behind, so the gap is the only evidence they
		// were ever planned (S047-R5.2).
		rep := &overlay.CompareReport{
			TotalPackages:    10,
			ComparedPackages: 4,
			Interrupted:      true,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingDone),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if run.NotEvaluated != 6 {
			t.Errorf("NotEvaluated = %d over ten planned and four reached, want 6 (S047-R5.2)", run.NotEvaluated)
		}
	})

	t.Run("the unreached gap and the reached shortfalls add", func(t *testing.T) {
		// Two populations, no overlap: the two packages the run never reached
		// have no row, and the one it reached and could not read has one.
		rep := &overlay.CompareReport{
			TotalPackages:    5,
			ComparedPackages: 3,
			Interrupted:      true,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingDone),
				compareFixtureResultReading("dev-lang", "rust", overlay.ReadingDone),
				compareFixtureResultReading("dev-lang", "zig", overlay.ReadingFailed),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if run.NotEvaluated != 3 {
			t.Errorf("NotEvaluated = %d over a gap of 2 plus 1 killed review, want 3 (S047-R5.2)", run.NotEvaluated)
		}
	})

	t.Run("one package that failed several ways counts once", func(t *testing.T) {
		// The pairing the review pass in internal/overlay/review.go actually
		// produces: it writes ReadingNotComparable onto every row whose Verified
		// is NotVerified, and a package whose lookup errored was never verified.
		// ErrorCount plus a tally of the reading states would answer 2 here,
		// over one package (S047-R5.2).
		errored := compareFixtureResultStatus("dev-lang", "rust", overlay.StatusError)
		errored.Reading = overlay.ReadingNotComparable

		rep := &overlay.CompareReport{
			TotalPackages:    2,
			ComparedPackages: 2,
			ErrorCount:       1,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingDone),
				errored,
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if run.NotEvaluated != 1 {
			t.Errorf("NotEvaluated = %d over ONE package that errored and was therefore also unreadable, want 1 (S047-R5.2)", run.NotEvaluated)
		}
	})

	t.Run("every reached shortfall counts, once each", func(t *testing.T) {
		rep := &overlay.CompareReport{
			TotalPackages:    4,
			ComparedPackages: 4,
			ErrorCount:       1,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingDone),
				compareFixtureResultReading("dev-lang", "rust", overlay.ReadingFailed),
				compareFixtureResultReading("dev-lang", "zig", overlay.ReadingNotComparable),
				compareFixtureResultStatus("dev-lang", "nim", overlay.StatusError),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if run.NotEvaluated != 3 {
			t.Errorf("NotEvaluated = %d over a killed review, a refused pair and an errored lookup, want 3 (S047-R4.3, S047-R5.2)", run.NotEvaluated)
		}
		if run.Complete {
			t.Error("a run with three unestablished facts reports Complete true (S047-R5.1)")
		}
	})

	t.Run("a tally smaller than the rows it carries does not go negative", func(t *testing.T) {
		// A CompareReport assembled by hand carries results without carrying the
		// tally. An unclamped subtraction would net the reached shortfall away
		// and report a holed run as whole.
		rep := &overlay.CompareReport{
			TotalPackages:    0,
			ComparedPackages: 3,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingFailed),
			},
		}

		run := buildCompareReport(rep, "gentoo", nil)

		if run.NotEvaluated != 1 {
			t.Errorf("NotEvaluated = %d over a negative gap and one killed review, want 1 (S047-R5.2)", run.NotEvaluated)
		}
		if run.Complete {
			t.Error("a killed review was cancelled out by a negative gap and the run reports Complete true (S047-R5.1)")
		}
	})

	t.Run("a read difference is not a shortfall", func(t *testing.T) {
		rep := &overlay.CompareReport{
			TotalPackages:    2,
			ComparedPackages: 2,
			Results: []overlay.CompareResult{
				compareFixtureResultReading("dev-lang", "go", overlay.ReadingDone),
				compareFixtureResultReading("dev-lang", "rust", overlay.ReadingDone),
			},
		}

		if run := buildCompareReport(rep, "gentoo", nil); run.NotEvaluated != 0 {
			t.Errorf("NotEvaluated = %d over a run that read every difference, want 0 (S047-R5.2)", run.NotEvaluated)
		}
	})
}

// Story 047, sub-task 3.3 — S047-R1.2, S047-R1.3, S047-R6.3, S047-R7.1.
//
// What is assertable about a presenter WITHOUT a terminal is narrower than what
// it does, and these four subtests are exactly that narrow set: which mode it
// resolved and where it resolved it, what it asked the payload to SAY, and that
// the two deliveries — screen and file — are independent in both directions.
//
// # The warning sentence itself is deliberately NOT asserted here
//
// `func Default` in internal/common/logger/logger.go fixes the logger's writer
// to os.Stderr inside a sync.Once, so an in-process test cannot redirect it: by
// the time this file runs, the singleton already holds the real stream. The
// sentence is therefore pinned where it CAN be observed — the subprocess route
// `func manifestUnderAmbientMode` in report_mode_fallback_test.go uses — and
// what is pinned below is the half that outlives the sentence: a failed render
// does not withhold the export, and a failed export does not withhold the
// render.

// comparePresentPayload records what presentCompareReport asked it to say, so
// "ShowAll is answered once, here" is observable as a fact rather than inferred
// from rendered text. It implements report.Payload and nothing else, because
// report.Payload is the whole contract between a producer and every renderer —
// a stub that needed more would be evidence the seam had grown.
type comparePresentPayload struct {
	asked []report.SectionOptions
}

func (p *comparePresentPayload) Sections(opts report.SectionOptions) []report.Section {
	p.asked = append(p.asked, opts)
	return []report.Section{{
		Title: "Overlay comparison",
		Lead:  []string{"dev-lang/rust 1.0.0 vs 1.1.0"},
	}}
}

// comparePresentGlobals points the three root flags this presenter reads at
// known values for one test and puts them back after it. They are package
// variables, so a subtest that set one and did not restore it would be handing
// its neighbour a flag nobody typed.
func comparePresentGlobals(t *testing.T, ui string, all bool, export string) {
	t.Helper()
	origUI, origAll, origExport := autoupdateUI, autoupdateAll, autoupdateExport
	autoupdateUI, autoupdateAll, autoupdateExport = ui, all, export
	t.Cleanup(func() {
		autoupdateUI, autoupdateAll, autoupdateExport = origUI, origAll, origExport
	})
}

// comparePresentRun is a one-section run carrying the recording payload.
func comparePresentRun(payload *comparePresentPayload) report.Run {
	return report.Run{
		Schema:   report.SchemaVersion,
		Kind:     report.KindOverlayCompare,
		Title:    "Overlay comparison",
		Complete: true,
		Payload:  payload,
	}
}

func TestPresentCompareReport(t *testing.T) {
	t.Run("the mode is resolved here, and an unusable one still renders", func(t *testing.T) {
		cfg := &config.Config{}

		comparePresentGlobals(t, "plain", false, "")
		stdout := captureStdout(t, func() {
			presentCompareReport(cfg, comparePresentRun(&comparePresentPayload{}))
		})
		if !strings.Contains(stdout, "Overlay comparison") {
			t.Errorf("the resolved plain mode printed no report; stdout = %q", stdout)
		}
		// Plain never takes the screen. Asserting the absence of the alternate
		// screen is what separates "it rendered" from "it rendered in the mode
		// that was resolved" — fullscreen would have printed this section too.
		if strings.Contains(stdout, "\x1b[?1049h") {
			t.Error("a plain report opened the alternate screen (S047-R7.1)")
		}

		// The half that matters: an unusable ambient mode is a typo in a shell
		// profile, and it must not become "your comparison did not run".
		comparePresentGlobals(t, "sideways", false, "")
		if got := reportModeOrPlain(cfg, false, uiIsTerminal); got != report.ModePlain {
			// Non-vacuity: if this value were ACCEPTED, the render below would
			// prove nothing about the fallback.
			t.Fatalf("reportModeOrPlain(%q) = %q, want the plain fallback", "sideways", got)
		}
		stdout = captureStdout(t, func() {
			presentCompareReport(cfg, comparePresentRun(&comparePresentPayload{}))
		})
		if !strings.Contains(stdout, "Overlay comparison") {
			t.Errorf("an unusable mode cost the operator the report; stdout = %q", stdout)
		}
	})

	t.Run("Sections is asked once, with ShowAll taken from --all", func(t *testing.T) {
		for _, all := range []bool{false, true} {
			payload := &comparePresentPayload{}
			comparePresentGlobals(t, "plain", all, "")
			captureStdout(t, func() {
				presentCompareReport(&config.Config{}, comparePresentRun(payload))
			})

			if len(payload.asked) != 1 {
				t.Fatalf("--all=%v: Sections was asked %d time(s), want exactly 1", all, len(payload.asked))
			}
			if payload.asked[0].ShowAll != all {
				t.Errorf("--all=%v: SectionOptions.ShowAll = %v, want %v", all, payload.asked[0].ShowAll, all)
			}
			// Nothing here prints a plan before a prompt, so nothing here may
			// suppress one: SkipPlan belongs to the check's confirm path alone.
			if payload.asked[0].SkipPlan {
				t.Errorf("--all=%v: SectionOptions.SkipPlan = true, want false", all)
			}
		}
	})

	t.Run("a render that fails does not withhold the export", func(t *testing.T) {
		exportPath := filepath.Join(t.TempDir(), "compare.json")
		comparePresentGlobals(t, "plain", false, exportPath)

		// A closed stdout is the terminal that went away mid-write. render.Plain
		// writes to os.Stdout, read at call time, so this is the one arrangement
		// that produces a real render error without touching the renderer.
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("creating the stdout pipe: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("closing the read end: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("closing the write end: %v", err)
		}
		orig := os.Stdout
		os.Stdout = w
		t.Cleanup(func() { os.Stdout = orig })

		payload := &comparePresentPayload{}
		run := comparePresentRun(payload)
		// Non-vacuity: prove the arrangement really does fail a render, so a
		// passing export below is evidence of independence rather than of a
		// render that quietly succeeded.
		if err := renderCheckReportIn(report.ModePlain, run.Sections(report.SectionOptions{}), render.Options{}); err == nil {
			t.Fatal("a closed stdout rendered without error; this subtest would assert nothing")
		}

		presentCompareReport(&config.Config{}, run)

		data, err := os.ReadFile(exportPath)
		if err != nil {
			t.Fatalf("a failed render withheld the export: %v", err)
		}
		if !strings.Contains(string(data), string(report.KindOverlayCompare)) {
			t.Errorf("the export does not carry this run's kind; file = %q", string(data))
		}
	})

	t.Run("an unwritable export does not withhold the report", func(t *testing.T) {
		// The other direction, and the ordering that makes it true: the terminal
		// is written FIRST, so a directory that does not exist cannot cost the
		// operator the comparison they waited on (S047-R1.3).
		exportPath := filepath.Join(t.TempDir(), "no-such-dir", "compare.json")
		comparePresentGlobals(t, "plain", false, exportPath)

		stdout := captureStdout(t, func() {
			presentCompareReport(&config.Config{}, comparePresentRun(&comparePresentPayload{}))
		})
		if !strings.Contains(stdout, "dev-lang/rust 1.0.0 vs 1.1.0") {
			t.Errorf("an unwritable export cost the operator the report; stdout = %q", stdout)
		}
		if _, err := os.Stat(exportPath); err == nil {
			t.Errorf("the export at %q was written into a directory that does not exist", exportPath)
		}
	})
}
