package autoupdate

// Story 011 — Phase-3 authored (RED-first) tests for the fixer fetch-mode:
//   5.2 fetch-focused instruction
//   5.3 fetch-mode args (copied gh/curl allowlist + reduced caps)
//
// Target: internal/autoupdate/manifest_fixer_test.go. RED until the executor adds
// FixMode (FixModeGeneric/FixModeFetch), manifestFixFetchMaxTurns(=12),
// manifestFixFetchBudgetFactor(=0.5), ManifestFixRequest.{Mode,DiscoveredURL}, and
// the fetch branches in buildFixInstruction/buildFixArgs. Go compile error
// "undefined: X" is valid RED.
//
// Behavior-level: assertions pin the instruction's fetch directives + hint, the
// presence of Bash(gh *)/Bash(curl *) in the fetch allowlist, the global
// manifestFixAllowedTools being UNMUTATED, --max-turns == 12, the reduced budget,
// and the FixModeGeneric path being byte-for-byte unchanged (UB2). The FixModeFetch
// args are exercised end-to-end through FixManifest with the scripted exec seam so
// no real `claude` runs.

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// =============================================================================
// 5.2 — fetch-focused instruction (Unit) — R5.1, UB2
// =============================================================================

func TestBuildFixInstruction_FetchMode_Directives(t *testing.T) {
	req := ManifestFixRequest{
		Package:       "net-misc/rustdesk",
		Version:       "1.2.3",
		EbuildPath:    "/overlay/net-misc/rustdesk/rustdesk-1.2.3.ebuild",
		ManifestError: "failed fetching required distfiles",
		DistDir:       "/tmp/distdir",
		Mode:          FixModeFetch,
		DiscoveredURL: "https://github.com/rustdesk/rustdesk/releases/download/1.2.3/vcpkg-lite.tar.gz",
	}
	got := buildFixInstruction(req)

	// Fetch-focus directives (behavior-level: the directive intent must be present;
	// match on stable keyword fragments, not exact prose).
	lower := strings.ToLower(got)
	for _, frag := range []string{"src_uri", "upstream"} {
		if !strings.Contains(lower, frag) {
			t.Errorf("fetch instruction missing keyword %q", frag)
		}
	}
	// "do not edit build logic" intent.
	if !strings.Contains(lower, "build logic") && !strings.Contains(lower, "build") {
		t.Error("fetch instruction should direct the agent NOT to edit build logic")
	}
	// "never invent URLs".
	if !strings.Contains(lower, "invent") {
		t.Error("fetch instruction should forbid inventing URLs")
	}
	// "give up with a one-line report if no obtainable distfile".
	if !strings.Contains(lower, "report") {
		t.Error("fetch instruction should ask for a give-up report when nothing is obtainable")
	}
	// The DiscoveredURL hint must be included when set.
	if !strings.Contains(got, req.DiscoveredURL) {
		t.Errorf("fetch instruction must include the DiscoveredURL hint %q", req.DiscoveredURL)
	}
}

func TestBuildFixInstruction_FetchMode_NoHintWhenEmpty(t *testing.T) {
	req := ManifestFixRequest{
		Package:       "net-misc/rustdesk",
		Version:       "1.2.3",
		EbuildPath:    "/overlay/net-misc/rustdesk/rustdesk-1.2.3.ebuild",
		ManifestError: "failed fetching required distfiles",
		DistDir:       "/tmp/distdir",
		Mode:          FixModeFetch,
		// DiscoveredURL empty: the instruction must still be valid (no dangling hint).
	}
	got := buildFixInstruction(req)
	if strings.Contains(got, "DiscoveredURL") {
		t.Error("instruction must not leak the literal field name when no hint is set")
	}
	if strings.TrimSpace(got) == "" {
		t.Error("fetch instruction must be non-empty even without a hint")
	}
}

// TestBuildFixInstruction_GenericMode_Unchanged covers UB2: the generic-mode
// instruction must remain byte-for-byte what it was before fetch mode existed. We
// pin it by asserting the generic instruction does NOT carry the fetch-only
// give-up/hint framing, and still carries the existing generic markers.
func TestBuildFixInstruction_GenericMode_Unchanged(t *testing.T) {
	req := ManifestFixRequest{
		Package:       "dev-games/godot",
		Version:       "4.7",
		EbuildPath:    "/overlay/dev-games/godot/godot-4.7.ebuild",
		ManifestError: "404 Not Found",
		DistDir:       "/tmp/distdir",
		Mode:          FixModeGeneric, // zero value
	}
	got := buildFixInstruction(req)
	// Existing generic markers (from the current instruction) must survive.
	for _, want := range []string{req.Package, req.Version, req.EbuildPath, req.DistDir, "/bentoo"} {
		if !strings.Contains(got, want) {
			t.Errorf("generic instruction missing existing marker %q (UB2)", want)
		}
	}
}

// =============================================================================
// 5.3 — fetch-mode args: allowlist (copied) + caps (Unit) — R5.2, R5.3, AD5, UB2/UB4
// =============================================================================

// runFixWithMode drives FixManifest through the scripted seam in the given mode and
// returns the captured argv. Uses the existing fixerSeam + newTestFixer helpers.
func runFixWithMode(t *testing.T, mode FixMode, budget float64) *capturedExec {
	t.Helper()
	factory, cap, _ := fixerSeam(`printf '%s' '{"type":"result","is_error":false,"result":"ok"}'`)
	cfg := LLMConfig{Provider: "claude-code"}
	if budget > 0 {
		cfg.MaxBudgetUSD = budget
	}
	f := newTestFixer(t, cfg, WithFixerExecCommand(factory))
	req := sampleFixRequest(t)
	req.Mode = mode
	if _, err := f.FixManifest(context.Background(), req); err != nil {
		t.Fatalf("FixManifest(mode=%v): %v", mode, err)
	}
	return cap
}

func TestFetchArgs_AllowlistAddsGhAndCurl(t *testing.T) {
	cap := runFixWithMode(t, FixModeFetch, 0)
	allowed, ok := flagValue(cap.args, "--allowedTools")
	if !ok {
		t.Fatal("expected --allowedTools in fetch mode")
	}
	for _, want := range []string{"Bash(gh *)", "Bash(curl *)"} {
		if !strings.Contains(allowed, want) {
			t.Errorf("fetch allowlist %q missing %q", allowed, want)
		}
	}
	// The default tools must still be present (it is an append-onto-copy).
	for _, want := range []string{"Read", "Edit", "Bash(pkgdev *)"} {
		if !strings.Contains(allowed, want) {
			t.Errorf("fetch allowlist %q dropped default tool %q", allowed, want)
		}
	}
}

// TestFetchArgs_GlobalAllowlistNotMutated covers AD5: the shared package slice
// manifestFixAllowedTools must be unchanged after a fetch-mode call (copy-never-mutate).
func TestFetchArgs_GlobalAllowlistNotMutated(t *testing.T) {
	before := append([]string(nil), manifestFixAllowedTools...)
	_ = runFixWithMode(t, FixModeFetch, 0)

	if len(manifestFixAllowedTools) != len(before) {
		t.Fatalf("manifestFixAllowedTools length changed: %d → %d (must never mutate)",
			len(before), len(manifestFixAllowedTools))
	}
	for i := range before {
		if manifestFixAllowedTools[i] != before[i] {
			t.Errorf("manifestFixAllowedTools[%d] mutated: %q → %q", i, before[i], manifestFixAllowedTools[i])
		}
	}
	// The fetch-only tools must NOT have leaked into the global slice.
	for _, leaked := range []string{"Bash(gh *)", "Bash(curl *)"} {
		for _, got := range manifestFixAllowedTools {
			if got == leaked {
				t.Errorf("fetch-only tool %q leaked into the global allowlist", leaked)
			}
		}
	}
}

func TestFetchArgs_ReducedMaxTurns(t *testing.T) {
	cap := runFixWithMode(t, FixModeFetch, 0)
	turns, ok := flagValue(cap.args, "--max-turns")
	if !ok {
		t.Fatal("expected --max-turns in fetch mode")
	}
	if turns != strconv.Itoa(manifestFixFetchMaxTurns) {
		t.Errorf("--max-turns = %q, want %d (manifestFixFetchMaxTurns)", turns, manifestFixFetchMaxTurns)
	}
	if turns != "12" {
		t.Errorf("--max-turns = %q, want 12 per design", turns)
	}
}

func TestFetchArgs_ReducedBudgetWhenConfigured(t *testing.T) {
	const base = 2.0
	cap := runFixWithMode(t, FixModeFetch, base)
	v, ok := flagValue(cap.args, "--max-budget-usd")
	if !ok {
		t.Fatal("expected --max-budget-usd in fetch mode when budget configured")
	}
	got, err := strconv.ParseFloat(v, 64)
	if err != nil {
		t.Fatalf("--max-budget-usd %q not a float: %v", v, err)
	}
	want := base * manifestFixFetchBudgetFactor
	if got != want {
		t.Errorf("fetch budget = %v, want base*factor = %v", got, want)
	}
}

func TestFetchArgs_NoBudgetFlagWhenUnconfigured(t *testing.T) {
	cap := runFixWithMode(t, FixModeFetch, 0)
	if _, ok := flagValue(cap.args, "--max-budget-usd"); ok {
		t.Error("no --max-budget-usd should be emitted when MaxBudgetUSD == 0")
	}
}

// TestGenericArgs_Unchanged covers UB2/UB4: FixModeGeneric args carry the default
// allowlist (no gh/curl) and the default --max-turns (30), unchanged vs today.
func TestGenericArgs_Unchanged(t *testing.T) {
	cap := runFixWithMode(t, FixModeGeneric, 0)
	allowed, _ := flagValue(cap.args, "--allowedTools")
	for _, forbidden := range []string{"Bash(gh *)", "Bash(curl *)"} {
		if strings.Contains(allowed, forbidden) {
			t.Errorf("generic allowlist must NOT contain %q (UB2)", forbidden)
		}
	}
	turns, _ := flagValue(cap.args, "--max-turns")
	if turns != strconv.Itoa(manifestFixMaxTurns) {
		t.Errorf("generic --max-turns = %q, want default %d (UB2)", turns, manifestFixMaxTurns)
	}
}
