package overlay

import (
	"strings"
	"testing"
)

// The declaration line is what closes the gap between "the struct knows a
// package is patched" and "the operator does". Tasks 1-6 satisfied R2.2 in the
// struct — PatchedBy is set and asserted by compare_divergence_test.go — but the
// only path from that field to the terminal was the stale-declaration finding,
// which needs a local repository copy to fire. With an API-only provider
// Verified is always NotVerified, so a patched package rendered exactly like an
// unpatched one: same Verdict "keep", no column, no line. That is the story's
// opening ambiguity one level down, which is why R3.8 exists.
//
// Every case below asserts on FormatReport's OUTPUT rather than on a helper, so
// a line that is rendered but never reaches the report would fail here.

// declMarker is the phrase unique to the declaration line. The stale-declaration
// finding also names the entry, so asserting on the entry alone cannot tell the
// two lines apart — which is exactly what the "no second line" case below has to
// distinguish.
const declMarker = "declared by"

func declReport(results ...CompareResult) string {
	return FormatReport(&CompareReport{Results: results})
}

// declLine returns the rendered line carrying the declaration marker for pkg, or
// "" when there is none.
func declLine(out, pkg string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, declMarker) && strings.Contains(line, pkg) {
			return line
		}
	}
	return ""
}

// _Requirements: R2.2, R3.8_
func TestRendersPatchedDeclaration(t *testing.T) {
	const (
		entry  = "app-editors/zed@stable"
		reason = "wayland IME patch we carry until upstream lands it"
	)

	patched := CompareResult{
		Category: "app-editors", Package: "zed",
		LocalVersion: "1.0", RemoteVersion: "1.0",
		Status: StatusUpToDate, Verdict: VerdictKeep,
		Patched: true, PatchedBy: entry, PatchedReason: reason,
		Verified: NotVerified,
	}

	t.Run("a patched package names its entry and reason with no verification", func(t *testing.T) {
		out := declReport(patched)

		line := declLine(out, "zed")
		if line == "" {
			t.Fatalf("no declaration line for a patched package; the report is silent about the divergence it was told about.\n--- report ---\n%s", out)
		}
		if !strings.Contains(line, entry) {
			t.Errorf("declaration line %q does not name the declaring entry %q — that key is what the operator greps the registry for", strings.TrimSpace(line), entry)
		}
		if !strings.Contains(line, reason) {
			t.Errorf("declaration line %q does not carry the declared reason %q", strings.TrimSpace(line), reason)
		}
	})

	t.Run("a stale declaration keeps its warning and gains no declaration line", func(t *testing.T) {
		stale := patched
		stale.Verified = VerifiedIdentical

		out := declReport(stale)

		if !strings.Contains(strings.ToLower(out), "stale") {
			t.Errorf("the stale-declaration finding (R4.2) disappeared.\n--- report ---\n%s", out)
		}
		if line := declLine(out, "zed"); line != "" {
			t.Errorf("a stale declaration got a second, contradicting line %q: one says the divergence is described, the other says it no longer exists", strings.TrimSpace(line))
		}
		if strings.Contains(out, reason) {
			t.Errorf("the report reprints the reason %q of a declaration it has just called stale — that text is precisely the part that is no longer true.\n--- report ---\n%s", reason, out)
		}
	})

	t.Run("an unpatched package gains no line", func(t *testing.T) {
		clean := CompareResult{
			Category: "www-client", Package: "firefox",
			LocalVersion: "1.0", RemoteVersion: "1.0",
			Status: StatusUpToDate, Verdict: VerdictRedundant,
		}

		out := declReport(clean)

		if line := declLine(out, "firefox"); line != "" {
			t.Errorf("an unpatched package got a declaration line %q", strings.TrimSpace(line))
		}
	})

	t.Run("an over-long reason is capped while the entry stays whole", func(t *testing.T) {
		const longEntry = "app-office/libreoffice-l10n-meta@stable"
		long := patched
		long.PatchedBy = longEntry
		long.PatchedReason = strings.Repeat("x", patchedReasonCap*3)

		out := declReport(long)

		line := declLine(out, "zed")
		if line == "" {
			t.Fatalf("no declaration line.\n--- report ---\n%s", out)
		}
		if !strings.Contains(line, longEntry) {
			t.Errorf("the declaring entry was truncated to %q; a shortened key looks usable and is not, which is worse than printing none", strings.TrimSpace(line))
		}
		if strings.Contains(line, long.PatchedReason) {
			t.Errorf("an unbounded reason was printed in full; one entry's essay must not decide the width of the whole report")
		}
		if !strings.Contains(line, "…") {
			t.Errorf("the reason was shortened without saying so; line %q must mark the cut", strings.TrimSpace(line))
		}
	})

	t.Run("a patched entry with no reason still names the entry", func(t *testing.T) {
		// Reachable in production: LoadPackagesConfig never calls
		// ValidatePackageConfig, so R1.3 does not protect this path and an entry
		// whose reason is whitespace can arrive here trimmed to "".
		silent := patched
		silent.PatchedReason = ""

		out := declReport(silent)

		line := declLine(out, "zed")
		if line == "" {
			t.Fatalf("a reasonless declaration produced no line at all; the divergence is still declared.\n--- report ---\n%s", out)
		}
		if !strings.Contains(line, entry) {
			t.Errorf("line %q does not name the entry", strings.TrimSpace(line))
		}
		if strings.HasSuffix(strings.TrimRight(line, " "), ":") {
			t.Errorf("line %q ends in a dangling colon introducing a reason that is not there", strings.TrimSpace(line))
		}
	})
}
