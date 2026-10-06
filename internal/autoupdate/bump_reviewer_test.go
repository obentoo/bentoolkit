package autoupdate

// Authored for story 033, sub-task 10.1 — R7, R7.3, R7.4, R7.6, R7.7.
//
// A READING THAT CANNOT TOUCH ANYTHING. The reviewer's whole value is that it
// may raise scrutiny; its whole safety is that it may do nothing else. R7.4 says
// it gets no capability that can modify a file, and that is asserted against the
// PACKAGE-LEVEL tool list rather than a constructed reviewer — the same reason
// 11.1 does it for the fixer: NewClaudeCode* refuses to build without the
// `claude` CLI, so a constructor-based check would skip on every CI runner, and
// a security property that evaporates on the machine enforcing it is not
// enforced.
//
// R7.6 IS STRUCTURAL, NOT REMEMBERED. `Report.ExitCode` counts findings of
// severity `error` and nothing else. So if the reviewer can only ever emit
// `info` and `warning`, then "a reviewer's opinion never decides whether a bump
// passed" is a property of the type rather than a rule somebody has to keep in
// mind while editing the prompt. This is story 025's R4.5 — a finding never
// changes a verdict — restated for a model. The case below drives the reviewer's
// worst-case output through the report and asserts the severities that come out.
//
// THE REVIEWER READS TWO ARCHIVES, NOT THE NETWORK (design D8). The protocol
// listed G1.5 as needing network; it does not. Both versions' build-declaration
// files come out of the two distfiles with the selective `tar -xO` extraction
// story 031 already implements, and the diff is computed locally. Its INPUT is a
// prepared diff, not a capability to go and find one — which is what keeps the
// no-network property true for the reviewer as well.
//
// R7.7 SAYS "CANNOT RUN", NOT "HAS NO ARCHIVE". The requirement was widened in
// review for a concrete reason: handling only the missing-archive case leaves a
// provider failure with no stated outcome at all — the run would carry on and
// the report would be silent about the fact that nothing was reviewed — and it
// leaves `validate.timeout` with no consumer anywhere in the story. Each cause
// is named in the skip, because "the reviewer did not run" and "the reviewer
// timed out after an hour" ask different things of the operator.
//
// This file pins `bumpReviewAllowedTools`, the `BumpReviewer` interface,
// `BumpReviewRequest{Package, OldVersion, NewVersion, BuildFileDiff, OldArchive,
// NewArchive}`, `BumpReviewReport{Risks, ProposedDepth, Reason, Skipped,
// SkipReason}` and `WithBumpReviewerTimeout`, which is where
// `validate.timeout` (sub-task 9.1) is consumed.
//
// ONE UNCERTAIN HELPER: `stubLookPathFound` already exists in this package's
// tests and is used here to make `claudeAvailable()` answer true without the CLI
// on PATH. If its signature differs from `stubLookPathFound(t)`, adjust that one
// call — the assertions around it do not change.
