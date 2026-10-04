package main

import (
	"reflect"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/obentoo/bentoolkit/internal/realign"
)

// TestS060GuardDepsDefaultsOfTheOtherCommands extends R6.2 to the 13 seams
// sub-task 4.2 moved (validate, staged, compare, prune): each defaultDeps()
// field is the function its package variable held before story 060. The
// autoupdate fields are pinned by TestS060DepsDefaultsAreTheProductionImplementations.
//
// newClaudeAsker had an anonymous default before, so it is pinned to the named
// function that replaced it, and realignPublishIsInteractive must be the SAME
// function as registryPromptIsInteractive (design C6).
func TestS060GuardDepsDefaultsOfTheOtherCommands(t *testing.T) {
	d := defaultDeps()
	tests := []struct {
		name      string
		got, want any
	}{
		{"validateRunner", d.validateRunner, validate.Run},
		{"confirmStagedClean", d.confirmStagedClean, confirmAction},
		{"realignProve", d.realignProve, realign.Prove},
		{"confirmRealignPlan", d.confirmRealignPlan, confirmAction},
		{"realignPromote", d.realignPromote, realign.Promote},
		{"confirmRealignPublish", d.confirmRealignPublish, confirmAction},
		{"realignPublishIsInteractive", d.realignPublishIsInteractive, d.registryPromptIsInteractive},
		{"realignPublishIsInteractive (production)", d.realignPublishIsInteractive, stdinAndStdoutAreTerminals},
		{"reviewWarnf", d.reviewWarnf, logger.Warn},
		{"newClaudeAsker", d.newClaudeAsker, newClaudeCodeAsker},
		{"prunePlanner", d.prunePlanner, overlay.PlanPrune},
		{"pruneExecutor", d.pruneExecutor, overlay.ExecutePrune},
		{"confirmPrune", d.confirmPrune, confirmAction},
		{"pruneInteractive", d.pruneInteractive, stdinIsTerminal},
	}
	for _, tt := range tests {
		got := reflect.ValueOf(tt.got)
		if got.Kind() != reflect.Func || got.IsNil() {
			t.Errorf("defaultDeps().%s is %v, want a non-nil function", tt.name, tt.got)
			continue
		}
		if got.Pointer() != reflect.ValueOf(tt.want).Pointer() {
			t.Errorf("defaultDeps().%s is not the production implementation it replaced", tt.name)
		}
	}
}

// TestS060GuardSnapshotDepsDefaultsAreNil extends R6.2 to the two snapshot
// seams sub-task 4.3 moved. Both package variables defaulted to nil, and nil
// is what selects production behaviour: the snapshot package resolves a nil
// Runner to its execRunner, and snapshot.Rollback a nil Confirm to its stdin
// prompt. A non-nil default would change that resolution, so the guard pins
// nil rather than a function pointer.
func TestS060GuardSnapshotDepsDefaultsAreNil(t *testing.T) {
	d := defaultDeps()
	if d.snapshotRunner != nil {
		t.Errorf("defaultDeps().snapshotRunner is %T, want nil (the snapshot package's own execRunner)", d.snapshotRunner)
	}
	if d.snapshotRollbackConfirm != nil {
		t.Error("defaultDeps().snapshotRollbackConfirm is non-nil, want nil (snapshot.Rollback's own stdin prompt)")
	}
	if d.snapshotRestoreConfirm != nil {
		t.Error("defaultDeps().snapshotRestoreConfirm is non-nil, want nil (snapshot.Restore's own stdin prompt)")
	}
}
