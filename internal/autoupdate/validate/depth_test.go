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
// `qmerge` IS DELIBERATELY ABSENT — one rung up from where this note sat until
// story 042. The ladder stopped at compile then; what it stops at now is the
// package IMAGE under ${D}, because 042 added the install rung. qmerge touches
// the RUNNING SYSTEM and is a different activity, not a deeper rung, and
// S042-D2 says permanently. The test below keeps THAT gap deliberate on exactly
// the terms story 033 kept the install one: a future reader who assumes the
// absence is an oversight and adds the name has to change a test that says
// otherwise, which is where they will read why. 042 was that reader, and this
// note is what it read.
//
// This file pins `type Depth` with `DepthNone, DepthOptions, DepthPatches,
// DepthConfigure, DepthCompile, DepthInstall`, `Depth.String()` and
// `ParseDepth(string) (Depth, error)`.

import (
	"strings"
	"testing"
)

// theLadder is the whole ordered set, shallowest first. Every test below reads
// it, so a new depth is added in exactly one place.
var theLadder = []Depth{DepthNone, DepthOptions, DepthPatches, DepthConfigure, DepthCompile, DepthInstall}

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
	if !(DepthNone < DepthInstall) {
		t.Errorf("DepthNone (%v) is not below DepthInstall (%v)", DepthNone, DepthInstall)
	}
	if DepthNone != theLadder[0] {
		t.Errorf("the shallowest depth is %v, want DepthNone", theLadder[0])
	}
	if DepthInstall != theLadder[len(theLadder)-1] {
		t.Errorf("the deepest depth is %v, want DepthInstall", theLadder[len(theLadder)-1])
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

// TestParseDepth_InstallIsARung is R1.1 and R1.3, and it is the exact inverse
// of the guard story 033 left here. The ladder gained a rung that runs
// src_install, so the name it used to refuse is the name it must now answer.
//
// The two directions are pinned TOGETHER on purpose: parsing must yield a depth
// that sorts DEEPER than compile. A rung that parses but ties with compile
// compares equal under `max(floor, proposed)`, and an install request would
// quietly receive a compile run — a validation hole that still reports a pass.
func TestParseDepth_InstallIsARung(t *testing.T) {
	got, err := ParseDepth("install")
	if err != nil {
		t.Fatalf("ParseDepth(%q): %v — install additionally runs src_install and is the deepest rung (R1.1)", "install", err)
	}

	if name := got.String(); name != "install" {
		t.Errorf("ParseDepth(%q).String() = %q; String and ParseDepth are exact inverses, so a rung that "+
			"parses under one name and prints another is a depth a report can name but a flag cannot request", "install", name)
	}

	//nolint:staticcheck // QF1001: see the ordering tests above — the requirement is stated positively and negated, not restated as its own failure.
	if !(DepthCompile < got) {
		t.Errorf("install (%v) does not sort deeper than compile (%v); the ladder is an order, and a rung that "+
			"ties with compile makes max(floor, proposed) answer compile for an install request", got, DepthCompile)
	}
}

// TestParseDepth_UnknownNameOffersInstall is R1.3. The rejection message is
// DERIVED from depthLadder rather than retyped, so this asserts the derivation
// actually reached the new rung: a rung appended to the ladder but missing from
// the offered set is a capability the operator is never told exists.
//
// The typo is one letter SHORT of the name, never one letter long. ParseDepth
// quotes the offender back, so a probe of "installl" would find "install"
// inside the quoted typo and pass without the valid set naming the rung at all
// — the same false guard found in story 041.
func TestParseDepth_UnknownNameOffersInstall(t *testing.T) {
	got, err := ParseDepth("instal")
	if err == nil {
		t.Fatalf("ParseDepth(\"instal\") returned %v with no error; a mistyped depth must be refused", got)
	}
	if !strings.Contains(err.Error(), "install") {
		t.Errorf("the rejection %q does not offer install among the valid depths; the ladder gained a rung "+
			"the operator can only discover by reading the source", err)
	}
}

// TestParseDepth_QmergeIsNotADepth keeps the gap deliberate, one rung up from
// where story 033 put it. The ladder stops at install by decision, not by
// omission: `ebuild … install` assembles the image under ${D} inside
// PORTAGE_TMPDIR, and the phase that would touch the running system is qmerge —
// the package manager's activity, and out of this ladder permanently (S042-D2).
//
// This is the same tripwire 033 wrote, moved rather than deleted. Deleting it
// would leave the NEW ceiling unguarded, which is the one property the ladder
// has never given up: every rung states where it stops.
func TestParseDepth_QmergeIsNotADepth(t *testing.T) {
	got, err := ParseDepth("qmerge")

	if err == nil {
		t.Fatalf("ParseDepth(\"qmerge\") returned %v with no error; the ladder stops at install by decision "+
			"(S042-D2) — installing a package onto the host is the package manager's question, not a validator's", got)
	}
	if !strings.Contains(err.Error(), "qmerge") {
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

// TestGateForDepth covers the mapping the check path reads: which gate's own
// pass proves a run reached a given rung.
//
// The function has no production caller yet — the check-path verdict that would
// consume it was left out when this landed, because stories 044/045 settled the
// classification rule the other way (see the commit that exported it). A test is
// what keeps the contract its doc comment states from drifting in the meantime,
// rather than being rediscovered when a caller finally arrives.
func TestGateForDepth(t *testing.T) {
	tests := []struct {
		name  string
		depth Depth
		want  string
		ok    bool
	}{
		{"options", DepthOptions, GateOptions, true},
		{"patches", DepthPatches, GatePatches, true},
		{"configure", DepthConfigure, GateConfigure, true},
		{"compile", DepthCompile, GateCompile, true},
		{"install", DepthInstall, GateInstall, true},

		// The documented refusal: a depth-none bump ran no gate, so there is no
		// gate whose pass could stand for it. Falling back to a shallower one —
		// as the applier deliberately does for a different question — would hand
		// back proof of a rung nobody climbed.
		{"none has no gate", DepthNone, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gate, ok := GateForDepth(tc.depth)
			if ok != tc.ok {
				t.Fatalf("GateForDepth(%v) ok = %v, want %v", tc.depth, ok, tc.ok)
			}
			if gate != tc.want {
				t.Errorf("GateForDepth(%v) = %q, want %q", tc.depth, gate, tc.want)
			}
		})
	}
}

// TestGateForDepthAgreesWithGateRungs pins the two directions against each
// other: whatever gate stands for a rung must map back to that same rung. The
// mapping is one table read both ways, and this is what keeps it that way.
func TestGateForDepthAgreesWithGateRungs(t *testing.T) {
	for gate, rung := range gateRungs {
		got, ok := GateForDepth(rung)
		if !ok {
			t.Errorf("GateForDepth(%v) refused, but %q maps to it", rung, gate)
			continue
		}
		if got != gate {
			t.Errorf("GateForDepth(%v) = %q, want %q — the two directions disagree", rung, got, gate)
		}
	}
}
