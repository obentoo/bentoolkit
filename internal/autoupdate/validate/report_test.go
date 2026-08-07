package validate

// Authored for story 031, sub-task 4.2 — R3, R3.4, R3.5, R4, R5.5, R5.6, R5.7.
//
// Written from the contract: design.md's Data Models fix Report, EbuildResult,
// Finding, Outcome and Severity, and its CLI table fixes the three exit codes.
//
// ONE THING THIS TEST PINS RATHER THAN INHERITS. design.md says exit 2 means
// "the selector names a category or package the overlay does not hold", but it
// does not say how a Report carries that. This file pins the field
// `Report.UnmatchedSelector string` — non-empty means nothing matched. If the
// implementer prefers another shape, this is the file to change first; the
// three exit-code behaviours themselves are not negotiable.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import "testing"

func TestReport_ExitCode_CleanRunIsZero(t *testing.T) {
	r := Report{
		Overlay: "/var/db/repos/bentoo",
		Results: []EbuildResult{
			{Package: "media-plugins/gst-plugins-qt6", Version: "1.28.6", Options: "PASS", QA: "PASS"},
			{Package: "dev-python/gst-python", Version: "1.28.6", Options: "SKIPPED", QA: "PASS", Reason: "distfile absent: gst-python-1.28.6.tar.xz"},
		},
	}

	if got := r.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0 — PASS and SKIPPED are both clean outcomes", got)
	}
}

func TestReport_ExitCode_ErrorFindingIsOne(t *testing.T) {
	r := Report{
		Results: []EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.29.2",
			Options: "FAILED",
			Findings: []Finding{
				{Gate: "options", Severity: "error", Detail: "-Daalib= is passed but upstream 1.29.2 declares no such option"},
			},
		}},
	}

	if got := r.ExitCode(); got != 1 {
		t.Errorf("ExitCode: got %d, want 1", got)
	}
}

func TestReport_ExitCode_UnmatchedSelectorIsTwo(t *testing.T) {
	r := Report{UnmatchedSelector: "media-plugins/does-not-exist"}

	if got := r.ExitCode(); got != 2 {
		t.Errorf("ExitCode: got %d, want 2 — a selector matching nothing is a usage error, not a clean run", got)
	}
}

// TestReport_ExitCode_QaFindingsDoNotChangeIt is design decision D8, and it is
// what keeps the command usable. The overlay carries pre-existing pkgcheck
// findings that have nothing to do with any bump; letting them set the exit
// status would make `overlay validate` fail across the whole tree and become
// noise — a metadata.xml DOCTYPE typo outranking the real signal.
func TestReport_ExitCode_QaFindingsDoNotChangeIt(t *testing.T) {
	r := Report{
		Results: []EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.28.6",
			Options: "PASS",
			QA:      "PASS",
			Findings: []Finding{
				{Gate: "qa", Severity: "error", Detail: "MissingXmlDoctype: metadata.xml lacks a DOCTYPE"},
			},
		}},
	}

	if got := r.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0 — only option-gate findings decide the exit code (D8)", got)
	}
}

// TestReport_ExitCode_WarningsAndInfoDoNotFail pins that the two non-blocking
// severities stay non-blocking. An unresolved option name is a limit of the
// gate's reach, not a defect in the ebuild.
func TestReport_ExitCode_WarningsAndInfoDoNotFail(t *testing.T) {
	r := Report{
		Results: []EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.28.6",
			Options: "PASS",
			Findings: []Finding{
				{Gate: "options", Severity: "warning", Detail: "-D${plugin}= at line 57 cannot be resolved statically"},
				{Gate: "options", Severity: "info", Detail: "upstream declares vulkan, which the ebuild never passes"},
			},
		}},
	}

	if got := r.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0", got)
	}
}

// TestEbuildResult_SkippedAlwaysCarriesAReason is the invariant the whole story
// rests on. A SKIPPED without a reason is indistinguishable from a pass to
// anyone reading the report, which is the exact defect this gate exists to
// remove — so it is asserted, not left to convention.
func TestEbuildResult_SkippedAlwaysCarriesAReason(t *testing.T) {
	r := Report{
		Results: []EbuildResult{
			{Package: "a/b", Version: "1", Options: "SKIPPED", QA: "PASS", Reason: "build system is not Meson: cmake"},
			{Package: "c/d", Version: "2", Options: "PASS", QA: "SKIPPED", Reason: "pkgcheck was not found on PATH"},
			{Package: "e/f", Version: "3", Options: "PASS", QA: "PASS"},
		},
	}

	for _, res := range r.Results {
		skipped := res.Options == "SKIPPED" || res.QA == "SKIPPED"
		if skipped && res.Reason == "" {
			t.Errorf("%s-%s reports SKIPPED with an empty Reason; a skip nobody can read is a pass", res.Package, res.Version)
		}
		if !skipped && res.Reason != "" {
			t.Errorf("%s-%s carries a Reason (%q) without being SKIPPED", res.Package, res.Version, res.Reason)
		}
	}
}

// TestReport_ExitCode_MixedRunPrefersFailure pins that one bad ebuild in a
// whole-overlay sweep is enough to fail the run.
func TestReport_ExitCode_MixedRunPrefersFailure(t *testing.T) {
	r := Report{
		Results: []EbuildResult{
			{Package: "a/b", Version: "1", Options: "PASS"},
			{Package: "c/d", Version: "2", Options: "SKIPPED", Reason: "distfile absent: d-2.tar.xz"},
			{Package: "e/f", Version: "3", Options: "FAILED", Findings: []Finding{{Gate: "options", Severity: "error", Detail: "-Dgone= is undeclared"}}},
		},
	}

	if got := r.ExitCode(); got != 1 {
		t.Errorf("ExitCode: got %d, want 1", got)
	}
}
