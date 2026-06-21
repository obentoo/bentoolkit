package tui

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// The model must satisfy the Bubble Tea contract.
var _ tea.Model = newModel()

// drive applies a sequence of messages to a fresh model and returns its
// ANSI-stripped View(), so behavior can be asserted deterministically.
func drive(msgs ...tea.Msg) string {
	var m tea.Model = newModel()
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return stripANSI(m.View())
}

// R1.5: the live tail is bounded to the last 10 lines; older committed lines are
// evicted. After 15 committed lines the view shows lines 5..14 and drops 0..4.
func TestModelTailBounded(t *testing.T) {
	msgs := []tea.Msg{
		BatchStartMsg{Total: 1},
		TaskStartMsg{ID: "p1", Label: "cat/pkg"},
	}
	for i := 0; i < 15; i++ {
		msgs = append(msgs, TaskLineMsg{ID: "p1", Text: fmt.Sprintf("line-%d", i), EOL: true})
	}
	out := drive(msgs...)

	if !strings.Contains(out, "line-14") {
		t.Errorf("most-recent tail line %q missing:\n%s", "line-14", out)
	}
	if !strings.Contains(out, "line-5") {
		t.Errorf("tail line %q (within last 10) missing:\n%s", "line-5", out)
	}
	if strings.Contains(out, "line-4") {
		t.Errorf("tail line %q should have been evicted (cap 10):\n%s", "line-4", out)
	}
	if strings.Contains(out, "line-0") {
		t.Errorf("oldest tail line %q should have been evicted:\n%s", "line-0", out)
	}
}

// R1.3: concurrent task slots render in their own region — no rendered line ever
// mixes two tasks' output.
func TestModelTwoSlotsNoInterleave(t *testing.T) {
	out := drive(
		BatchStartMsg{Total: 2},
		TaskStartMsg{ID: "p1", Label: "slotA"},
		TaskStartMsg{ID: "p2", Label: "slotB"},
		TaskLineMsg{ID: "p1", Text: "aaa-1", EOL: true},
		TaskLineMsg{ID: "p2", Text: "bbb-1", EOL: true},
		TaskLineMsg{ID: "p1", Text: "aaa-2", EOL: true},
		TaskLineMsg{ID: "p2", Text: "bbb-2", EOL: true},
	)

	for _, tok := range []string{"aaa-1", "aaa-2", "bbb-1", "bbb-2"} {
		if !strings.Contains(out, tok) {
			t.Errorf("missing tail token %q:\n%s", tok, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "aaa-") && strings.Contains(line, "bbb-") {
			t.Errorf("interleaved tasks on one line: %q", line)
		}
	}
}

// R1.4: a completed task produces a ✓/✗ history line carrying its label/summary.
func TestModelHistoryGlyphs(t *testing.T) {
	out := drive(
		BatchStartMsg{Total: 2},
		TaskStartMsg{ID: "p1", Label: "pkg-ok"},
		TaskDoneMsg{ID: "p1", OK: true, Summary: "updated"},
		TaskStartMsg{ID: "p2", Label: "pkg-bad"},
		TaskDoneMsg{ID: "p2", OK: false, Summary: "failed"},
	)
	if !strings.Contains(out, "✓") {
		t.Errorf("success history should render ✓:\n%s", out)
	}
	if !strings.Contains(out, "✗") {
		t.Errorf("failure history should render ✗:\n%s", out)
	}
	if !strings.Contains(out, "pkg-ok") || !strings.Contains(out, "pkg-bad") {
		t.Errorf("history should carry task labels:\n%s", out)
	}
}

// R1.4: an overall progress indicator advances as tasks complete (done/total).
func TestModelOverallProgress(t *testing.T) {
	out := drive(
		BatchStartMsg{Total: 2},
		TaskStartMsg{ID: "p1", Label: "p1"},
		TaskDoneMsg{ID: "p1", OK: true, Summary: "ok"},
	)
	if !strings.Contains(out, "1/2") {
		t.Errorf("overall progress should show 1/2 after 1 of 2 done:\n%s", out)
	}
}

// A carriage-return style in-place update (eol=false) replaces the live line
// rather than appending; the superseded value is not retained.
func TestModelInPlaceLiveLine(t *testing.T) {
	out := drive(
		BatchStartMsg{Total: 1},
		TaskStartMsg{ID: "p1", Label: "dl"},
		TaskLineMsg{ID: "p1", Text: "downloading 10%", EOL: false},
		TaskLineMsg{ID: "p1", Text: "downloading 99%", EOL: false},
	)
	if !strings.Contains(out, "downloading 99%") {
		t.Errorf("current live line missing:\n%s", out)
	}
	if strings.Contains(out, "downloading 10%") {
		t.Errorf("superseded in-place line should not be retained:\n%s", out)
	}
}

// teatest golden frame: a full scripted run driven to a static terminal state
// (all tasks done) renders a stable final frame. Regenerate with `-update`.
func TestModelGoldenFrame(t *testing.T) {
	m := newModel()
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))

	for _, msg := range []tea.Msg{
		BatchStartMsg{Total: 2},
		TaskStartMsg{ID: "p1", Label: "cat/pkg1"},
		TaskStageMsg{ID: "p1", Stage: "manifest"},
		TaskLineMsg{ID: "p1", Stream: StreamStdout, Text: "downloading 50%", EOL: false},
		TaskLineMsg{ID: "p1", Stream: StreamStdout, Text: "downloading 100%", EOL: true},
		TaskDoneMsg{ID: "p1", OK: true, Summary: "updated"},
		TaskStartMsg{ID: "p2", Label: "cat/pkg2"},
		TaskDoneMsg{ID: "p2", OK: false, Summary: "manifest failed"},
		BatchDoneMsg{Summary: "1 ok, 1 failed"},
	} {
		tm.Send(msg)
	}

	if err := tm.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	out, err := io.ReadAll(tm.FinalOutput(t))
	if err != nil {
		t.Fatalf("read final output: %v", err)
	}
	teatest.RequireEqualOutput(t, out)
}
