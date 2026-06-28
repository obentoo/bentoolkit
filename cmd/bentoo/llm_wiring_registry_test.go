package main

// Authored (Red-phase) test for story 014 — newConfiguredRegistryFixer wiring.
//
// Independent contract spec for sub-task 2.1. References newConfiguredRegistryFixer,
// which does not exist yet, so the cmd/bentoo package fails to COMPILE until Task 2
// lands — that compile failure is the expected Red signal.
//
// The headline invariant (AD9 / R7.1, R-Untyped-nil): for a non-claude-code provider
// the helper must return a TRUE nil interface, not a boxed (*ClaudeCodeRegistryFixer)(nil).
// A boxed nil pointer passes `!= nil` and would make runCheck believe a fixer exists
// and show the prompt. The assertion below uses both `f != nil` and reflect to catch
// the typed-nil trap.

import (
	"reflect"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// TestNewConfiguredRegistryFixer_NonClaudeReturnsTrueNil pins R7.1/AD9: every
// non-"claude-code" provider (including the empty/unset provider) yields a true nil
// interface AND a nil error, so runCheck never shows the repair prompt.
func TestNewConfiguredRegistryFixer_NonClaudeReturnsTrueNil(t *testing.T) {
	for _, provider := range []string{"", "claude", "ollama", "bogus"} {
		t.Run("provider="+provider, func(t *testing.T) {
			f, err := newConfiguredRegistryFixer(config.LLMConfig{Provider: provider})
			if err != nil {
				t.Fatalf("newConfiguredRegistryFixer(%q): unexpected error %v", provider, err)
			}
			// Interface-level nil: must be a TRUE nil, not a boxed nil pointer.
			if f != nil {
				t.Errorf("provider %q: expected true nil RegistryFixer, got non-nil %T", provider, f)
			}
			// Defend against the untyped-nil trap even if the `!= nil` check above
			// were ever loosened: the reflect.Value must be the zero interface.
			if rv := reflect.ValueOf(f); rv.IsValid() {
				t.Errorf("provider %q: interface holds a boxed value (typed-nil trap): kind=%s", provider, rv.Kind())
			}
		})
	}
}
