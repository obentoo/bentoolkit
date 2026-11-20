package main

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// TestApplierValidateOptions_TurnTheHostSandboxOn: every applier the command
// builds (check, apply, revive, repair) takes applierValidateOptions, so this
// is the one place the sandbox must be switched on for autoupdate builds.
func TestApplierValidateOptions_TurnTheHostSandboxOn(t *testing.T) {
	ar := &autoupdateRun{}
	a, err := autoupdate.NewApplier(t.TempDir(), t.TempDir(), ar.applierValidateOptions(t.TempDir())...)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	if !a.HostSandboxed() {
		t.Error("the command's applier options leave the host sandbox off: autoupdate would build a bump's " +
			"code next to the operator's files and secrets")
	}
}
