package validate

// Authored for story 033, sub-task 1.2 — R2, R2.1.
//
// THE LADDER IS AN ORDER, AND THE ORDER IS THE CONTRACT. Every later decision in
// this story is arithmetic on it: policy selects a floor, a per-package override
// may raise or lower it, `--depth` replaces it, and the reviewer's proposal is
// applied as `max(floor, proposed)`. `max` is meaningless unless `<` means
// "shallower than", so the ordering is pinned here rather than inferred from the
// order somebody happened to declare the constants in.
//
// "Each depth includes every depth before it" is the other half: `configure`
// does not mean "run configure", it means "run everything up to and including
// configure". A reader who takes the constants as a menu rather than a ladder
// writes a runner that skips the patch gate on a configure request.
//
// `install` IS DELIBERATELY ABSENT (story Out of Scope). The ladder stops at
// compile, and R6.4 makes a compile pass SAY that src_install went uncovered.
// The test below keeps the gap deliberate: a future reader who assumes the
// absence is an oversight and adds the name has to change a test that says
// otherwise, which is where they will read why.
//
// This file pins `type Depth` with `DepthNone, DepthOptions, DepthPatches,
// DepthConfigure, DepthCompile`, `Depth.String()` and
// `ParseDepth(string) (Depth, error)`.

import (
	"strings"
	"testing"
)

// theLadder is the whole ordered set, shallowest first. Every test below reads
// it, so a new depth is added in exactly one place.
var theLadder = []Depth{DepthNone, DepthOptions, DepthPatches, DepthConfigure, DepthCompile}

// TestDepth_OrderingHoldsUnderLessThan is R2.1. Without it, `max(floor,
// proposed)` in the policy silently picks whichever constant happens to hold the
// larger integer.
func TestDepth_OrderingHoldsUnderLessThan(t *testing.T) {
	for i := 1; i < len(theLadder); i++ {
		prev, cur := theLadder[i-1], theLadder[i]
		//nolint:staticcheck // QF1001: the negation is deliberate. `prev < cur` IS the contract, and reading it under a `!` says "the thing we require did not hold". De Morgan's `prev >= cur` states the failure instead of the requirement, which is the wrong way round in a test that exists to pin the requirement.
		if !(prev < cur) {
			t.Errorf("%v is not less than %v; the ladder is an order and `max(floor, proposed)` depends on it", prev, cur)
		}
	}

	// And transitively, so no pair is left to chance: the shallowest is below
	// the deepest by more than one step.
	//nolint:staticcheck // QF1001: see the note above — the requirement is stated positively and negated, not restated as its own failure.
	if !(DepthNone < DepthCompile) {
		t.Errorf("DepthNone (%v) is not below DepthCompile (%v)", DepthNone, DepthCompile)
	}
	if DepthNone != theLadder[0] {
		t.Errorf("the shallowest depth is %v, want DepthNone", theLadder[0])
	}
	if DepthCompile != theLadder[len(theLadder)-1] {
		t.Errorf("the deepest depth is %v, want DepthCompile", theLadder[len(theLadder)-1])
	}
}

// TestDepth_RoundTripsThroughItsOwnName pins String() and ParseDepth as
// inverses. They are read from two different surfaces — a report prints the
// name, a flag and a config key parse it — and a mismatch between them is a
// depth that can be reported but never requested.
func TestDepth_RoundTripsThroughItsOwnName(t *testing.T) {
	for _, d := range theLadder {
		name := d.String()
		if name == "" {
			t.Errorf("%v renders as the empty string; a report cannot name it", d)
			continue
		}
		if strings.TrimSpace(name) != name || strings.ToLower(name) != name {
			t.Errorf("Depth.String() = %q; the flag and the config key are lower-case and unpadded", name)
		}

		got, err := ParseDepth(name)
		if err != nil {
			t.Errorf("ParseDepth(%q): %v — a depth the report prints must be a depth the flag accepts", name, err)
			continue
		}
		if got != d {
			t.Errorf("ParseDepth(%q) = %v, want %v", name, got, d)
		}
	}
}

// TestDepth_EveryNameIsDistinct catches the copy-paste that makes two rungs
// render identically — at which point the round-trip above still passes for one
// of them and the report has quietly lost a rung.
func TestDepth_EveryNameIsDistinct(t *testing.T) {
	seen := map[string]Depth{}
	for _, d := range theLadder {
		name := d.String()
		if other, dup := seen[name]; dup {
			t.Errorf("%v and %v both render as %q", other, d, name)
		}
		seen[name] = d
	}
	if len(seen) != len(theLadder) {
		t.Errorf("%d distinct names for %d depths", len(seen), len(theLadder))
	}
}

// TestParseDepth_TheExpectedNames pins the five spellings the CLI documents, so
// a rename is a deliberate break rather than a surprise in somebody's script.
func TestParseDepth_TheExpectedNames(t *testing.T) {
	want := map[string]Depth{
		"none":      DepthNone,
		"options":   DepthOptions,
		"patches":   DepthPatches,
		"configure": DepthConfigure,
		"compile":   DepthCompile,
	}

	for name, expected := range want {
		got, err := ParseDepth(name)
		if err != nil {
			t.Errorf("ParseDepth(%q): %v", name, err)
			continue
		}
		if got != expected {
			t.Errorf("ParseDepth(%q) = %v, want %v", name, got, expected)
		}
	}
}

// TestParseDepth_UnknownNameListsTheValidSet is the usability half of R2.1. An
// error that says only "invalid depth" makes the operator go and read the
// source; the valid set is five short words and there is no reason to withhold
// them.
func TestParseDepth_UnknownNameListsTheValidSet(t *testing.T) {
	_, err := ParseDepth("shallow")

	if err == nil {
		t.Fatal(`ParseDepth("shallow") returned no error; an unknown depth must be refused, not defaulted`)
	}
	msg := err.Error()
	if !strings.Contains(msg, "shallow") {
		t.Errorf("the error %q does not quote the name that was rejected", msg)
	}
	for _, name := range []string{"none", "options", "patches", "configure", "compile"} {
		if !strings.Contains(msg, name) {
			t.Errorf("the error %q does not list the valid depth %q; the operator has to read the source to recover", msg, name)
		}
	}
}

// TestParseDepth_InstallIsNotADepth keeps the gap deliberate. The ladder stops
// at compile by decision, not by omission: R6.4 requires a compile pass to state
// that src_install went uncovered, which names the gap without building a gate
// for it.
func TestParseDepth_InstallIsNotADepth(t *testing.T) {
	got, err := ParseDepth("install")

	if err == nil {
		t.Fatalf("ParseDepth(\"install\") returned %v with no error; the ladder stops at compile by decision "+
			"(story Out of Scope) — adding the rung is a story of its own, not a parser change", got)
	}
	if !strings.Contains(err.Error(), "install") {
		t.Errorf("the error %q does not quote the rejected name", err)
	}
}

// TestParseDepth_EmptyStringIsRefusedRatherThanDefaulted pins the degenerate
// input. Silently answering DepthNone for "" would turn a mistyped config key
// into "validation switched off", which is the loudest possible failure
// rendered as the quietest.
func TestParseDepth_EmptyStringIsRefusedRatherThanDefaulted(t *testing.T) {
	got, err := ParseDepth("")

	if err == nil {
		t.Fatalf(`ParseDepth("") = %v with no error; an empty depth must be refused so a mistyped key cannot switch validation off`, got)
	}
}
