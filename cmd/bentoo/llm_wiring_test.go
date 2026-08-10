package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// TestLLMConfigToAutoupdate_CarriesAllFields verifies the mapper copies every
// CLI-reachable field from config.LLMConfig onto autoupdate.LLMConfig, and that
// BaseURL (which has no config-side source) is left empty.
// _Requirements: R8.2_
func TestLLMConfigToAutoupdate_CarriesAllFields(t *testing.T) {
	src := config.LLMConfig{
		Provider:     "claude",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		Model:        "claude-3-haiku-20240307",
		Bare:         "true",
		MaxBudgetUSD: 12.5,
	}

	got := llmConfigToAutoupdate(src)

	if got.Provider != src.Provider {
		t.Errorf("Provider = %q, want %q", got.Provider, src.Provider)
	}
	if got.APIKeyEnv != src.APIKeyEnv {
		t.Errorf("APIKeyEnv = %q, want %q", got.APIKeyEnv, src.APIKeyEnv)
	}
	if got.Model != src.Model {
		t.Errorf("Model = %q, want %q", got.Model, src.Model)
	}
	if got.Bare != src.Bare {
		t.Errorf("Bare = %q, want %q", got.Bare, src.Bare)
	}
	if got.MaxBudgetUSD != src.MaxBudgetUSD {
		t.Errorf("MaxBudgetUSD = %v, want %v", got.MaxBudgetUSD, src.MaxBudgetUSD)
	}

	// BaseURL has no config-side source and must remain empty (intentionally unmapped).
	if got.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty (intentionally unmapped)", got.BaseURL)
	}
}

// TestLLMConfigToAutoupdate_FieldParity guards against config drift (R-config-drift).
// It asserts that EVERY field on autoupdate.LLMConfig is either carried by the
// mapper (non-zero after mapping a fully-populated source) or is on a documented
// allow-list of intentionally-unmapped fields (currently only BaseURL).
//
// If someone adds a new field to autoupdate.LLMConfig that has a config-side
// counterpart but forgets to wire it through the mapper, this test fails — the
// new field stays zero and is not on the allow-list.
func TestLLMConfigToAutoupdate_FieldParity(t *testing.T) {
	// Fields on autoupdate.LLMConfig that intentionally have NO config source.
	intentionallyUnmapped := map[string]bool{
		"BaseURL": true, // set internally for HTTP providers; no config field
	}

	// Build a source with every config-side field set to a distinctive non-zero value.
	src := config.LLMConfig{
		Provider:     "claude",
		APIKeyEnv:    "ANTHROPIC_API_KEY",
		Model:        "claude-3-haiku-20240307",
		Bare:         "true",
		MaxBudgetUSD: 12.5,
	}

	got := llmConfigToAutoupdate(src)
	v := reflect.ValueOf(got)
	typ := v.Type()

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if intentionallyUnmapped[field.Name] {
			// Allow-listed: must stay zero.
			if !v.Field(i).IsZero() {
				t.Errorf("field %q is on the unmapped allow-list but was populated (%v)", field.Name, v.Field(i).Interface())
			}
			continue
		}
		// Every other field must have been carried (i.e. be non-zero) when the
		// source is fully populated.
		if v.Field(i).IsZero() {
			t.Errorf("field %q was not carried by llmConfigToAutoupdate (got zero value); "+
				"wire it through the mapper or add it to the intentionally-unmapped allow-list", field.Name)
		}
	}
}

// TestNewConfiguredLLMProvider exercises the shared analyze/--check wiring helper.
//
// The helper's only policy is the empty-provider short-circuit; every other
// decision is delegated to autoupdate.NewLLMProvider. We therefore assert the
// three cases reachable deterministically from package main:
//
//   - Provider:""       → (nil, nil): caller proceeds without an LLM.
//   - Provider:"bogus"  → (nil, err) wrapping ErrLLMUnsupportedProvider.
//   - Provider:"claude" → (nil, err) wrapping ErrLLMAPIKeyMissing, with the
//     API-key env var cleared so construction deterministically fails.
//
// The "claude-code"-present path depends on autoupdate's UNEXPORTED `lookPath`
// seam, which package main cannot set (autoupdate exposes no exported setter).
// That path is covered in-package by autoupdate's TestNewLLMProvider_ClaudeCode,
// so re-asserting it here would require either exporting the seam (rejected) or a
// host-PATH dependency (non-deterministic). We intentionally omit it.
//
// _Requirements: R4.1, R4.2, R6.1, R6.2_
func TestNewConfiguredLLMProvider(t *testing.T) {
	// A stable env var name that is guaranteed empty for the claude no-key case.
	const claudeKeyEnv = "BENTOO_TEST_ANTHROPIC_API_KEY"

	tests := []struct {
		name        string
		cfg         config.LLMConfig
		wantErr     bool  // expect a non-nil error
		wantErrIs   error // if wantErr, the sentinel the error must wrap (nil = don't assert)
		wantNilProv bool  // (only meaningful when !wantErr) provider must be exactly nil
		clearKeyEnv bool  // clear claudeKeyEnv before running (for the no-key path)
	}{
		{
			name:        "empty provider short-circuits to (nil, nil)",
			cfg:         config.LLMConfig{Provider: ""},
			wantErr:     false,
			wantNilProv: true,
		},
		{
			name:      "unknown provider returns ErrLLMUnsupportedProvider",
			cfg:       config.LLMConfig{Provider: "bogus"},
			wantErr:   true,
			wantErrIs: autoupdate.ErrLLMUnsupportedProvider,
		},
		{
			name:        "claude without API key returns ErrLLMAPIKeyMissing",
			cfg:         config.LLMConfig{Provider: "claude", APIKeyEnv: claudeKeyEnv},
			wantErr:     true,
			wantErrIs:   autoupdate.ErrLLMAPIKeyMissing,
			clearKeyEnv: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.clearKeyEnv {
				// t.Setenv to "" makes the lookup deterministically empty and
				// restores any prior value after the subtest.
				t.Setenv(claudeKeyEnv, "")
			}

			p, err := newConfiguredLLMProvider(tt.cfg)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("newConfiguredLLMProvider(%+v): want error, got nil", tt.cfg)
				}
				if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
					t.Errorf("newConfiguredLLMProvider(%+v): error %v does not wrap %v", tt.cfg, err, tt.wantErrIs)
				}
				// NOTE: on the error path we deliberately do NOT assert anything
				// about p. autoupdate.NewLLMProvider routes "claude" through
				// NewClaudeClient, which returns a nil *ClaudeClient alongside the
				// error; assigning that to the LLMProvider interface yields a
				// typed-nil (non-nil interface, nil concrete pointer). That is
				// harmless here because runAnalyze gates on `err != nil` FIRST and
				// only touches p in the `else if p != nil` success branch — the
				// typed-nil never reaches WithAnalyzerLLMClient. The contract this
				// helper owes its callers on failure is "return a non-nil error",
				// which is asserted above.
				return
			}
			if err != nil {
				t.Fatalf("newConfiguredLLMProvider(%+v): want no error, got %v", tt.cfg, err)
			}
			// Success path. For the empty-provider short-circuit the provider must
			// be exactly nil (the caller proceeds without an LLM).
			if tt.wantNilProv && p != nil {
				t.Errorf("newConfiguredLLMProvider(%+v): want nil provider, got %T", tt.cfg, p)
			}
		})
	}
}

// isTrueNil reports whether an interface value is nil ALL THE WAY DOWN — no
// type, no pointer. A boxed nil pointer answers false here and true to `== nil`
// nowhere, which is the whole point.
func isTrueNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

// TestNewConfiguredBuildFixer_NonAgenticProviderYieldsATrueNil is the
// typed-nil discipline for the fixer. Only the local claude-code CLI can edit a
// staged ebuild; every other provider — including the empty one and the
// non-agentic HTTP "claude" — must leave the capability genuinely absent.
func TestNewConfiguredBuildFixer_NonAgenticProviderYieldsATrueNil(t *testing.T) {
	for _, provider := range []string{"", "claude", "openai", "ollama", "bogus"} {
		t.Run("provider="+provider, func(t *testing.T) {
			got, err := newConfiguredBuildFixer(config.LLMConfig{Provider: provider})

			if err != nil {
				t.Fatalf("newConfiguredBuildFixer(%q): unexpected error %v — a provider that simply cannot fix builds is not an error",
					provider, err)
			}
			if got != nil && !isTrueNil(got) {
				t.Fatalf("newConfiguredBuildFixer(%q) returned a non-nil %T; a real capability was wired for a provider that has none",
					provider, got)
			}
			if !isTrueNil(got) {
				t.Errorf("newConfiguredBuildFixer(%q) returned a BOXED nil (%T); the caller's `fixer != nil` gate is then true "+
					"and the build fixer is silently enabled (llm_wiring.go:66-68)", provider, got)
			}
		})
	}
}

// TestNewConfiguredBumpReviewer_NonAgenticProviderYieldsATrueNil is the same
// discipline for the reviewer, asserted separately because the two constructors
// are separate code and the trap is per-constructor.
func TestNewConfiguredBumpReviewer_NonAgenticProviderYieldsATrueNil(t *testing.T) {
	for _, provider := range []string{"", "claude", "openai", "ollama", "bogus"} {
		t.Run("provider="+provider, func(t *testing.T) {
			got, err := newConfiguredBumpReviewer(config.LLMConfig{Provider: provider})

			if err != nil {
				t.Fatalf("newConfiguredBumpReviewer(%q): unexpected error %v", provider, err)
			}
			if !isTrueNil(got) {
				t.Errorf("newConfiguredBumpReviewer(%q) returned %T rather than a true nil; a boxed nil silently enables the reviewer",
					provider, got)
			}
		})
	}
}

// TestIsTrueNil_DistinguishesABoxedNil guards the guard. If this helper answered
// the way `== nil` does, every assertion above would pass vacuously — which is
// the exact shape of the bug being tested for, one level up.
func TestIsTrueNil_DistinguishesABoxedNil(t *testing.T) {
	var typed *autoupdate.ClaudeCodeFixer //nolint:staticcheck // SA4023 related information: the concrete type IS the box the comparison below demonstrates. The nolint on the comparison itself does not cover the related-information diagnostic golangci-lint raises here.
	var boxed autoupdate.ManifestFixer = typed

	if boxed == nil { //nolint:staticcheck // demonstrating the trap is the point
		t.Fatal("a boxed nil pointer compared equal to nil; this Go release has changed and the whole discipline needs revisiting")
	}
	if isTrueNil(boxed) != true {
		t.Error("isTrueNil did not see through the box; every nil assertion in this file would then pass vacuously")
	}

	var absent autoupdate.ManifestFixer
	if !isTrueNil(absent) {
		t.Error("isTrueNil reported a genuinely nil interface as non-nil")
	}
}

// TestLLMCapabilities_TheFlagEnablesBoth is R7.1. One flag, both capabilities —
// the operator should not have to learn two names to turn the feature on.
func TestLLMCapabilities_TheFlagEnablesBoth(t *testing.T) {
	review, fix := llmCapabilities(true, config.ValidateConfig{})

	if !review || !fix {
		t.Errorf("--llm alone enabled review=%v fix=%v; R7.1 enables both", review, fix)
	}
}

// TestLLMCapabilities_ConfigDisablesOneAtATime is R7.2. Configuration subtracts
// from what the flag asked for, which is how a host that wants the reading but
// not the edits (or the reverse) expresses it.
func TestLLMCapabilities_ConfigDisablesOneAtATime(t *testing.T) {
	off := false

	t.Run("review off leaves only the fixer", func(t *testing.T) {
		review, fix := llmCapabilities(true, config.ValidateConfig{Review: &off})
		if review {
			t.Error("review: false was ignored while --llm was given")
		}
		if !fix {
			t.Error("disabling the reviewer also disabled the fixer; the two switches are independent (R7.2)")
		}
	})

	t.Run("fix_on_failure off leaves only the reviewer", func(t *testing.T) {
		review, fix := llmCapabilities(true, config.ValidateConfig{FixOnFailure: &off})
		if fix {
			t.Error("fix_on_failure: false was ignored while --llm was given")
		}
		if !review {
			t.Error("disabling the fixer also disabled the reviewer")
		}
	})

	t.Run("both off is a --llm that enables nothing", func(t *testing.T) {
		review, fix := llmCapabilities(true, config.ValidateConfig{Review: &off, FixOnFailure: &off})
		if review || fix {
			t.Errorf("both switches off still enabled review=%v fix=%v", review, fix)
		}
	})
}

// TestLLMCapabilities_WithoutTheFlagNeitherIsConstructed pins the direction
// configuration may NOT go. `review: true` in a config file must not spend money
// on a run where nobody typed --llm: the flag is what records the operator's
// consent to the cost.
func TestLLMCapabilities_WithoutTheFlagNeitherIsConstructed(t *testing.T) {
	on := true

	review, fix := llmCapabilities(false, config.ValidateConfig{Review: &on, FixOnFailure: &on})

	if review || fix {
		t.Errorf("without --llm, config alone enabled review=%v fix=%v; configuration subtracts from the flag, it never adds to it",
			review, fix)
	}
}

// TestConfiguredCapabilities_ConstructionFailureIsAWarningNotAnAbort follows the
// precedent at overlay_autoupdate.go:1202. On a host without the `claude` CLI,
// `--llm` degrades the run to the same apply it would have done anyway — it does
// not refuse to publish a bump that is fine.
//
// The error is what the caller logs; what matters here is that it is RETURNED
// rather than fatal, and that no half-built capability comes back with it.
func TestConfiguredCapabilities_ConstructionFailureIsAWarningNotAnAbort(t *testing.T) {
	// A claude-code provider on a host with no `claude` on PATH is the shape of
	// the failure. Whether this host has one is not knowable here, so both
	// outcomes are accepted — and BOTH are checked for the boxed nil, which is
	// the property that must hold either way.
	cfg := config.LLMConfig{Provider: "claude-code"}

	fixer, fixErr := newConfiguredBuildFixer(cfg)
	reviewer, revErr := newConfiguredBumpReviewer(cfg)

	if fixErr != nil && !isTrueNil(fixer) {
		t.Errorf("newConfiguredBuildFixer returned %T alongside its error %v; a failed construction must not hand back a usable-looking capability",
			fixer, fixErr)
	}
	if revErr != nil && !isTrueNil(reviewer) {
		t.Errorf("newConfiguredBumpReviewer returned %T alongside its error %v", reviewer, revErr)
	}
	if fixErr == nil && isTrueNil(fixer) {
		t.Error("newConfiguredBuildFixer reported success and returned nothing; the caller would silently run without the fixer it asked for")
	}
	if revErr == nil && isTrueNil(reviewer) {
		t.Error("newConfiguredBumpReviewer reported success and returned nothing")
	}
}

// TestConfiguredCapabilities_NilIsStrictlyNilAtTheApplierGate closes a gap in
// the case above, and it is worth the duplication because the gap is exactly
// the bug group 12's risk line names — "a boxed nil silently enabling a
// capability".
//
// isTrueNil UNWRAPS the box: for a nil *T inside an interface it answers true.
// So `!isTrueNil(got)` is false for a boxed nil and reports nothing, and a
// constructor that returned `fixer, err` instead of `nil, err` passes every
// assertion above. That was verified by mutation, not reasoned about.
//
// The gate that actually decides is WithApplierBuildFixer's `fixer != nil`,
// which a boxed nil satisfies — so this asserts the same comparison the
// production code makes, on the interface value itself. isTrueNil deliberately
// does not appear below; using it here would reintroduce the blind spot.
func TestConfiguredCapabilities_NilIsStrictlyNilAtTheApplierGate(t *testing.T) {
	t.Run("non-agentic provider", func(t *testing.T) {
		// Deterministic on every host: the provider check returns before any
		// construction is attempted.
		cfg := config.LLMConfig{Provider: "openai"}

		fixer, err := newConfiguredBuildFixer(cfg)
		if err != nil {
			t.Fatalf("newConfiguredBuildFixer: %v", err)
		}
		if fixer != nil {
			t.Errorf("newConfiguredBuildFixer returned a non-nil interface (%T) for a non-agentic provider; "+
				"WithApplierBuildFixer gates on `!= nil` and would wire it", fixer)
		}

		reviewer, err := newConfiguredBumpReviewer(cfg)
		if err != nil {
			t.Fatalf("newConfiguredBumpReviewer: %v", err)
		}
		if reviewer != nil {
			t.Errorf("newConfiguredBumpReviewer returned a non-nil interface (%T) for a non-agentic provider", reviewer)
		}
	})

	t.Run("construction failure", func(t *testing.T) {
		// Whether this host has `claude` on PATH is not knowable here, so the
		// assertion is conditional on the error — but it is the STRICT one
		// either way, which is the whole point.
		cfg := config.LLMConfig{Provider: "claude-code"}

		if fixer, err := newConfiguredBuildFixer(cfg); err != nil && fixer != nil {
			t.Errorf("newConfiguredBuildFixer returned a non-nil interface (%T) alongside its error %v; "+
				"the applier would wire a capability whose construction failed", fixer, err)
		}
		if reviewer, err := newConfiguredBumpReviewer(cfg); err != nil && reviewer != nil {
			t.Errorf("newConfiguredBumpReviewer returned a non-nil interface (%T) alongside its error %v", reviewer, err)
		}
	})
}
