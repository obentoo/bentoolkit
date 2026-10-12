package autoupdate

// Authored for story 092, sub-task 1.3: the applier relays an outcome hint to
// a reporter that can show one (R2.2's wiring; the CLI supplies the text).

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// hintRecorder is a tui.HintReporter that records the hint of every TaskDone.
type hintRecorder struct {
	recordingReporter
	hints []string
}

func (r *hintRecorder) TaskDoneHint(id string, ok bool, summary, hint, captured string) {
	r.hints = append(r.hints, hint)
	r.TaskDone(id, ok, summary, captured)
}

var _ tui.HintReporter = (*hintRecorder)(nil)

// The hint function sees the failed apply's result, its repair bundle
// included, and what it answers reaches the reporter beside the TaskDone.
func TestApplyRepairHintRelaysTheOutcomeHint(t *testing.T) {
	rep := &hintRecorder{}
	var seen *ApplyResult
	result := failedStagedApply(t, WithApplierReporter(rep), WithApplierOutcomeHint(func(r *ApplyResult) string {
		seen = r
		if r.RepairBundle == "" {
			return ""
		}
		return "(repair: " + r.Package + ")"
	}))
	if seen != result {
		t.Fatalf("the hint function did not see the apply's own result")
	}
	if result.RepairBundle == "" {
		t.Fatalf("fixture: the failed apply left no repair bundle")
	}
	if len(rep.hints) != 1 || rep.hints[0] != "(repair: "+result.Package+")" {
		t.Fatalf("hints = %q, want the one naming %s", rep.hints, result.Package)
	}
}

// Without the option the applier closes the task with a plain TaskDone.
func TestApplyRepairHintAbsentWithoutTheOption(t *testing.T) {
	rep := &hintRecorder{}
	failedStagedApply(t, WithApplierReporter(rep))
	if len(rep.hints) != 0 {
		t.Fatalf("hints = %q with no hint option", rep.hints)
	}
	done := 0
	for _, e := range rep.snapshot() {
		if len(e) >= 9 && e[:9] == "TaskDone:" {
			done++
		}
	}
	if done != 1 {
		t.Fatalf("want one TaskDone, got %d in %v", done, rep.snapshot())
	}
}
