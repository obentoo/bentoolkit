package main

// Authored for story 057, sub-task 6.1 — R4.1 (refined v2).
//
// A row whose upstream lookup failed says WHY in its Reason, even when the row
// carries another finding — here the registry's declaration. The declaration
// is kept, one line down, among the row's further findings.
//
// The report is built by the REAL pipeline (CompareWithProvider over ebuilds on
// disk), never by hand, so the declaration finding on the failed row is the one
// overlay really emits for a declared package.
//
// RED ON ARRIVAL: buildCompareReport fills the lookup sentence only when the
// row has no Reason yet, so the declaration wins and the cause is lost.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

const (
	s057PatchedEntry  = "s057-registry/declared"
	s057PatchedReason = "keeps our USE=lto default"
)

// s057CompareDeclared runs the real comparison over two DECLARED packages: one
// whose lookup is rate-limited (s057/declfail) and one whose lookup succeeds
// (s057/declok). No review pass: the declaration is the only other finding.
func s057CompareDeclared(t *testing.T) *overlay.CompareReport {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	base := &reviewDirProvider{root: upstreamRoot, versions: map[string][]string{}}
	prov := &s057CmdProvider{reviewDirProvider: base, errs: map[string]error{"s057/declfail": errS057Limited}}
	declared := overlay.Divergence{Patched: true, Entry: s057PatchedEntry, Reason: s057PatchedReason}
	opts := overlay.CompareOptions{
		IncludeSynced: true,
		OverlayPath:   overlayRoot,
		Divergence:    map[string]overlay.Divergence{"s057/declfail": declared, "s057/declok": declared},
		Redact:        []string{s057Token},
	}
	var pkgs []overlay.PackageInfo
	for _, name := range []string{"declfail", "declok"} {
		// The two sides DIFFER, so the declaration is live, not stale.
		writeReviewEbuild(t, overlayRoot, "s057", name, "1.0", fmt.Sprintf("%sDESCRIPTION=\"ours %s\"\n", cmdReviewOurs, name))
		writeReviewEbuild(t, upstreamRoot, "s057", name, "1.0", fmt.Sprintf("%sDESCRIPTION=\"theirs %s\"\n", cmdReviewTheirs, name))
		base.versions["s057/"+name] = []string{"1.0"}
		pkgs = append(pkgs, overlay.PackageInfo{Category: "s057", Package: name, Versions: []string{"1.0"}, LatestVersion: "1.0"})
	}
	rep, err := overlay.CompareWithProvider(pkgs, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v", err)
	}
	return rep
}

func s057CountOf(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if l == want {
			n++
		}
	}
	return n
}

func TestCompareReasonNamesTheLookupCauseOnADeclaredPackage(t *testing.T) {
	rows := s057Rows(t, buildCompareReport(s057CompareDeclared(t), "gentoo", nil))
	const lookupPrefix = "the upstream lookup failed (rate-limited): "
	declLine := "patched — declared by " + s057PatchedEntry + ": " + s057PatchedReason

	fail := s057Row(t, rows, "s057/declfail")
	ok := s057Row(t, rows, "s057/declok")

	// Fixture guards: the failed row really failed, the other really did not.
	if fail.Status != "error" || fail.Cause != "rate-limited" {
		t.Fatalf("the fixture is wrong: s057/declfail status %q cause %q, want \"error\" and \"rate-limited\" (R4.4)", fail.Status, fail.Cause)
	}
	if ok.Status == "error" || ok.Cause != "" {
		t.Fatalf("the fixture is wrong: s057/declok status %q cause %q, want a successful lookup", ok.Status, ok.Cause)
	}

	// Hostile first: the declaration must not swallow the lookup's cause.
	t.Run("the failed lookup's sentence is the Reason, not the declaration", func(t *testing.T) {
		if !strings.HasPrefix(fail.Reason, lookupPrefix) {
			t.Errorf("s057/declfail: reason %q, want it to start with %q (R4.1: the lookup sentence wins over a declaration)", fail.Reason, lookupPrefix)
		}
	})

	t.Run("the declaration is kept among the further findings, once", func(t *testing.T) {
		if n := s057CountOf(fail.FurtherFindings, declLine); n != 1 {
			t.Errorf("s057/declfail: the declaration %q appears %d times in further findings %q, want exactly 1 (R4.1: kept, not dropped)", declLine, n, fail.FurtherFindings)
		}
		for _, l := range fail.FurtherFindings {
			if strings.HasPrefix(l, "the upstream lookup failed") {
				t.Errorf("s057/declfail: the lookup sentence is repeated in further findings %q; it belongs to the Reason alone", fail.FurtherFindings)
			}
		}
	})

	// Converse: a declared package whose lookup SUCCEEDED moves nowhere.
	t.Run("a declared package whose lookup succeeded keeps the declaration as its Reason", func(t *testing.T) {
		if ok.Reason != declLine {
			t.Errorf("s057/declok: reason %q, want %q (nothing but a failed lookup may displace the declaration)", ok.Reason, declLine)
		}
		if s057CountOf(ok.FurtherFindings, declLine) != 0 {
			t.Errorf("s057/declok: the declaration is duplicated into further findings %q", ok.FurtherFindings)
		}
	})

	t.Run("the token reaches neither the Reason nor a further finding", func(t *testing.T) {
		for _, r := range []report.ComparePkg{fail, ok} {
			if strings.Contains(r.Reason, s057Token) {
				t.Errorf("%s: the token reached the Reason (R4.7)", r.Package)
			}
			for _, l := range r.FurtherFindings {
				if strings.Contains(l, s057Token) {
					t.Errorf("%s: the token reached a further finding (R4.7)", r.Package)
				}
			}
		}
	})
}
