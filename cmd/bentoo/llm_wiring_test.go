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
