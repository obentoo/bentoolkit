package main

import (
	"errors"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// TestDetectedThisRun_PlansOnlyWhatTheScanConfirmed reproduces the 2026-11-13
// run: the plan listed 46 bumps against 45 found, the extra one a `failed`
// entry for kde-plasma/kwin, disabled and without an ebuild since 2026-10-28.
func TestDetectedThisRun_PlansOnlyWhatTheScanConfirmed(t *testing.T) {
	updates := []autoupdate.PendingUpdate{
		{Package: "app-misc/jq", NewVersion: "1.8.0", Status: autoupdate.StatusPending},
		{Package: "kde-plasma/kwin", NewVersion: "6.7.5", Status: autoupdate.StatusFailed},
		{Package: "dev-util/claude-code", NewVersion: "2.1.323", Status: autoupdate.StatusPending},
		{Package: "dev-vcs/broken", NewVersion: "2.0", Status: autoupdate.StatusPending},
		{Package: "app-office/libreoffice-l10n@stable", NewVersion: "26.8.3.1", Status: autoupdate.StatusPending},
	}
	checked := []autoupdate.CheckResult{
		{Package: "app-misc/jq", CurrentVersion: "1.7.1", UpstreamVersion: "1.8.0", HasUpdate: true},
		{Package: "dev-util/claude-code", HasUpdate: true, HeldBy: "held by min_age_days"},
		{Package: "dev-vcs/broken", HasUpdate: true, NotComparable: true},
		{Package: "app-office/libreoffice-l10n@stable", HasUpdate: true},
	}

	got := detectedThisRun(updates, checked)

	want := map[string]bool{"app-misc/jq": true, "app-office/libreoffice-l10n@stable": true}
	if len(got) != len(want) {
		t.Fatalf("planned %d entries %v, want exactly %v", len(got), got, want)
	}
	for _, update := range got {
		if !want[update.Package] {
			t.Errorf("planned %s, which this run's scan did not confirm as an update", update.Package)
		}
	}
}

// TestPendingValidation_AStaleEntryPlansNothing drives runPendingValidation
// itself, so the filter is proven wired rather than merely written. The only
// entry on disk is the stale `failed` kwin record and the scan found nothing:
// no plan may be priced, shown or confirmed.
func TestPendingValidation_AStaleEntryPlansNothing(t *testing.T) {
	configDir := t.TempDir()
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Add(autoupdate.PendingUpdate{
		Package: "kde-plasma/kwin", CurrentVersion: "6.7.4", NewVersion: "6.7.5", Status: autoupdate.StatusFailed,
	}); err != nil {
		t.Fatal(err)
	}

	auOpts := testAutoupdateOptions()
	auOpts.llm = true
	var printed bool
	stdout := captureStdout(t, func() {
		_, printed = testAutoupdateRun(auOpts).runPendingValidation(t.Context(), t.TempDir(), configDir,
			[]autoupdate.CheckResult{{Package: "app-misc/jq", CurrentVersion: "1.8.0", UpstreamVersion: "1.8.0"}},
			config.LLMConfig{})
	})

	if printed {
		t.Errorf("a plan was printed for an entry the scan never confirmed; stdout:\n%s", stdout)
	}
}

// TestCheckReport_CountsTheFailedPackages reproduces 599 × 595: the progress
// counter counts every checkable package, the report counted only Items. The
// lead line must state the same total, and the failure must be in it.
func TestCheckReport_CountsTheFailedPackages(t *testing.T) {
	result := autoupdate.BatchResult[autoupdate.CheckResult]{
		Items: []autoupdate.CheckResult{
			{Package: "app-misc/jq", CurrentVersion: "1.7.1", UpstreamVersion: "1.8.0", HasUpdate: true, Type: "source"},
			{Package: "app-misc/zz", CurrentVersion: "1.0", UpstreamVersion: "1.0", Type: "source"},
		},
		Failures: map[string]error{"dev-vcs/broken": errors.New("upstream answered 502")},
	}

	facts := checkPayload(discardLog(), checkReport(discardLog(), scannedWithFailures(result), nothingValidated()))

	if got, want := len(facts.Scanned), 3; got != want {
		t.Fatalf("report scanned %d package(s), want %d — the progress counter counted the failure too", got, want)
	}
	if facts.Scanned[2].Package != "dev-vcs/broken" || facts.Scanned[2].Error == "" {
		t.Errorf("the failed package is not a sorted row carrying its error: %+v", facts.Scanned)
	}
}
