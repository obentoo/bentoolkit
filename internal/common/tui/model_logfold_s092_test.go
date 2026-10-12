package tui

// Authored for story 092 (refine v2), sub-task 6.1: routed log lines fold like
// history (R1.6, R1.7). Once every history row is gone, log lines go oldest
// first, one `… N earlier log line(s)` row standing in their place; task tails
// are trimmed only after the logs; the header, a task row, the batch summary
// and the confirm prompt are never dropped; the released frame keeps every log
// line, because nothing else prints them to the scrollback.
//
// Frames are asserted ANSI-stripped at 40x10 (budget 9 rows) through the
// s092Frame helper of model_s092_test.go.

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// s092v2Logs returns n routed log lines log-00..log-(n-1) at level info.
func s092v2Logs(n int) []tea.Msg {
	msgs := make([]tea.Msg, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, LogMsg{Level: "info", Text: fmt.Sprintf("log-%02d", i)})
	}
	return msgs
}

// s092v2LogRow is the rendered row of log line i.
func s092v2LogRow(i int) string { return fmt.Sprintf("info: log-%02d", i) }

// s092v2Msgs concatenates message lists.
func s092v2Msgs(parts ...[]tea.Msg) []tea.Msg {
	var out []tea.Msg
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// s092v2Task starts one active task with n committed tail lines <id>-tail-i.
func s092v2Task(id string, n int) []tea.Msg {
	msgs := []tea.Msg{TaskStartMsg{ID: id, Label: "dev-go/" + id}}
	for i := 0; i < n; i++ {
		msgs = append(msgs, TaskLineMsg{ID: id, Text: fmt.Sprintf("%s-tail-%d", id, i), EOL: true})
	}
	return msgs
}

// s092v2Count returns how many rows equal want exactly.
func s092v2Count(rows []string, want string) int {
	n := 0
	for _, r := range rows {
		if r == want {
			n++
		}
	}
	return n
}

// s092v2TaskRows counts the rows that are the task row of id (spinner glyph,
// a space, the label), whatever the spinner frame is.
func s092v2TaskRows(rows []string, id string) int {
	n := 0
	for _, r := range rows {
		if strings.HasSuffix(r, " dev-go/"+id) {
			n++
		}
	}
	return n
}

// R1.6 hostile half (wrong fire): while history rows are left to drop, no log
// line goes. Then the converse: once every history row is gone, the log lines
// go, oldest first, and the two fold lines count their own kind only.
func TestModelLogFoldOnlyAfterHistory(t *testing.T) {
	// Header + 3 history + 6 logs = 10 > 9: two history rows go (the fold
	// line takes one), every log line stays.
	rows := s092Frame(40, 10, s092v2Msgs(
		[]tea.Msg{BatchStartMsg{Total: 3}}, s092History(3), s092v2Logs(6))...)
	want := []string{"Applying updates  3/3", "… 2 earlier package(s) finished", "✓ pkg-02 — ok"}
	for i := 0; i < 6; i++ {
		want = append(want, s092v2LogRow(i))
	}
	if got := s092Rows(rows); got != s092Rows(want) {
		t.Errorf("a log line was dropped while history was left to fold:\n--- got ---\n%s\n--- want ---\n%s", got, s092Rows(want))
	}

	// Header + 3 history + 30 logs: all history goes, then the 24 oldest
	// log lines; header, history fold, log fold and 6 logs = 9 rows.
	rows = s092Frame(40, 10, s092v2Msgs(
		[]tea.Msg{BatchStartMsg{Total: 3}}, s092History(3), s092v2Logs(30))...)
	want = []string{"Applying updates  3/3", "… 3 earlier package(s) finished", "… 24 earlier log line(s)"}
	for i := 24; i < 30; i++ {
		want = append(want, s092v2LogRow(i))
	}
	if got := s092Rows(rows); got != s092Rows(want) {
		t.Errorf("logs not folded oldest first after all history:\n--- got ---\n%s\n--- want ---\n%s", got, s092Rows(want))
	}
}

// R1.7: a frame of exactly height-1 rows shows no log fold line; one row more
// folds, the fold line itself takes a row, and its text is exactly
// `… N earlier log line(s)` with N the number of log lines dropped.
func TestModelLogFoldExactText(t *testing.T) {
	// Header + 8 logs = 9 rows: fits, nothing dropped.
	rows := s092Frame(40, 10, s092v2Msgs([]tea.Msg{BatchStartMsg{Total: 1}}, s092v2Logs(8))...)
	if len(rows) != 9 {
		t.Errorf("header + 8 log lines: got %d rows, want 9:\n%s", len(rows), s092Rows(rows))
	}
	for _, r := range rows {
		if strings.Contains(r, "earlier log line(s)") {
			t.Errorf("a frame that fits shows a log fold line: %q", r)
		}
	}

	cases := []struct {
		logs, dropped int
	}{
		{9, 2},   // 10 rows: the fold line takes one, so two lines go
		{30, 23}, // 31 rows: header, fold line, 7 newest log lines
	}
	for _, tc := range cases {
		rows := s092Frame(40, 10, s092v2Msgs([]tea.Msg{BatchStartMsg{Total: 1}}, s092v2Logs(tc.logs))...)
		want := []string{"Applying updates  0/1", fmt.Sprintf("… %d earlier log line(s)", tc.dropped)}
		for i := tc.dropped; i < tc.logs; i++ {
			want = append(want, s092v2LogRow(i))
		}
		if got := s092Rows(rows); got != s092Rows(want) {
			t.Errorf("%d log lines at 40x10:\n--- got ---\n%s\n--- want ---\n%s", tc.logs, got, s092Rows(want))
		}
	}
}

// R1.6: log lines go before any task tail is trimmed. Then the converse: once
// every log line is gone, the tails are trimmed, oldest first, and the log
// fold line stays.
func TestModelLogFoldBeforeTails(t *testing.T) {
	// Header + task row + 3 tails + 6 logs = 11 > 9: three log lines go (the
	// fold line takes one row), every tail line stays.
	rows := s092Frame(40, 10, s092v2Msgs(
		[]tea.Msg{BatchStartMsg{Total: 1}}, s092v2Task("act", 3), s092v2Logs(6))...)
	frame := s092Rows(rows)
	if len(rows) > 9 {
		t.Fatalf("frame has %d rows, want <= 9:\n%s", len(rows), frame)
	}
	if n := s092v2TaskRows(rows, "act"); n != 1 {
		t.Errorf("the active task has %d task rows, want 1:\n%s", n, frame)
	}
	for i := 0; i < 3; i++ {
		if !s092HasRow(rows, fmt.Sprintf("    act-tail-%d", i)) {
			t.Errorf("tail line %d was trimmed while log lines were left to fold:\n%s", i, frame)
		}
	}
	if !s092HasRow(rows, "… 3 earlier log line(s)") {
		t.Errorf("want the fold line for the 3 oldest log lines:\n%s", frame)
	}
	for i := 0; i < 6; i++ {
		kept := s092HasRow(rows, s092v2LogRow(i))
		if i < 3 && kept {
			t.Errorf("an OLD log line survived while newer ones were folded: %q\n%s", s092v2LogRow(i), frame)
		}
		if i >= 3 && !kept {
			t.Errorf("a NEW log line was dropped: %q\n%s", s092v2LogRow(i), frame)
		}
	}

	// Header + task row + 10 tails + 30 logs: every log line goes, then the 4
	// oldest tail lines; header, task row, 6 tails, log fold = 9 rows.
	rows = s092Frame(40, 10, s092v2Msgs(
		[]tea.Msg{BatchStartMsg{Total: 1}}, s092v2Task("act", 10), s092v2Logs(30))...)
	frame = s092Rows(rows)
	if len(rows) > 9 {
		t.Fatalf("frame has %d rows, want <= 9:\n%s", len(rows), frame)
	}
	if !strings.HasPrefix(rows[0], "Applying updates") {
		t.Errorf("the header row is gone:\n%s", frame)
	}
	if n := s092v2TaskRows(rows, "act"); n != 1 {
		t.Errorf("the active task has %d task rows, want 1:\n%s", n, frame)
	}
	if !s092HasRow(rows, "… 30 earlier log line(s)") {
		t.Errorf("want the fold line for all 30 log lines:\n%s", frame)
	}
	for _, r := range rows {
		if strings.HasPrefix(r, "info: log-") {
			t.Errorf("a log line survived while tails had to be trimmed: %q\n%s", r, frame)
		}
	}
	for i := 0; i < 10; i++ {
		kept := s092HasRow(rows, fmt.Sprintf("    act-tail-%d", i))
		if i < 4 && kept {
			t.Errorf("an OLD tail line survived the trim: act-tail-%d\n%s", i, frame)
		}
		if i >= 4 && !kept {
			t.Errorf("a NEW tail line was trimmed: act-tail-%d\n%s", i, frame)
		}
	}
}

// R1.6: the batch summary and the confirm prompt are never dropped, and they
// stay below the log lines that remain.
func TestModelLogFoldKeepsSummaryAndConfirm(t *testing.T) {
	msgs := s092v2Msgs([]tea.Msg{BatchStartMsg{Total: 1}}, s092v2Logs(30), []tea.Msg{
		BatchDoneMsg{Summary: "1 updated, 0 failed"},
		ConfirmMsg{Prompt: "Commit the bump?", Reply: make(chan bool, 1)},
	})
	rows := s092Frame(40, 10, msgs...)
	// Header + 30 logs + summary + prompt = 33: 25 log lines go.
	want := []string{"Applying updates  0/1", "… 25 earlier log line(s)"}
	for i := 25; i < 30; i++ {
		want = append(want, s092v2LogRow(i))
	}
	want = append(want, "1 updated, 0 failed", "Commit the bump? [y/n]")
	if got := s092Rows(rows); got != s092Rows(want) {
		t.Errorf("summary and prompt under folded logs:\n--- got ---\n%s\n--- want ---\n%s", got, s092Rows(want))
	}
}

// R1.6: the fold is a rendering of the live frame only. The released frame,
// the one bubbletea leaves on screen, still holds every log line once, with
// no log fold line: nothing else prints them to the scrollback.
func TestModelLogFoldReleasedFrameKeepsEveryLog(t *testing.T) {
	var m tea.Model = newModel()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	for _, msg := range s092v2Msgs([]tea.Msg{BatchStartMsg{Total: 3}}, s092History(3), s092v2Logs(30)) {
		m, _ = m.Update(msg)
	}
	m, _ = m.Update(releaseMsg{})
	rows := strings.Split(stripANSI(m.View()), "\n")
	frame := s092Rows(rows)
	for i := 0; i < 30; i++ {
		if n := s092v2Count(rows, s092v2LogRow(i)); n != 1 {
			t.Errorf("the released frame holds log line %d %d times, want once:\n%s", i, n, frame)
		}
	}
	for _, r := range rows {
		if strings.Contains(r, "earlier log line(s)") {
			t.Errorf("the released frame folds log lines: %q", r)
		}
		if strings.HasPrefix(r, "✓ pkg-") {
			t.Errorf("the released frame holds a history row: %q", r)
		}
	}
	if !strings.HasPrefix(rows[0], "Applying updates") {
		t.Errorf("the released frame lost its header:\n%s", frame)
	}
}

// R1.6: whatever mix of history, tasks, summary and 30 routed log lines, the
// live frame never exceeds height-1 rows, and the header, every task row and
// the summary are kept.
func TestModelLogFoldNeverExceedsBudget(t *testing.T) {
	sizes := []struct{ w, h int }{{40, 10}, {80, 24}}
	for _, size := range sizes {
		for _, hist := range []int{0, 1, 5, 30} {
			for _, tasks := range []int{0, 1, 3} {
				for _, summary := range []bool{false, true} {
					msgs := s092v2Msgs([]tea.Msg{BatchStartMsg{Total: hist + tasks}}, s092History(hist))
					for i := 0; i < tasks; i++ {
						msgs = append(msgs, s092v2Task(fmt.Sprintf("t%d", i), 10)...)
					}
					msgs = append(msgs, s092v2Logs(30)...)
					if summary {
						msgs = append(msgs, BatchDoneMsg{Summary: "run summary"})
					}
					rows := s092Frame(size.w, size.h, msgs...)
					name := fmt.Sprintf("%dx%d hist=%d tasks=%d summary=%v", size.w, size.h, hist, tasks, summary)
					if len(rows) > size.h-1 {
						t.Errorf("%s: frame has %d rows, want <= %d", name, len(rows), size.h-1)
					}
					if !strings.HasPrefix(rows[0], "Applying updates") {
						t.Errorf("%s: the header row is gone", name)
					}
					for i := 0; i < tasks; i++ {
						if n := s092v2TaskRows(rows, fmt.Sprintf("t%d", i)); n != 1 {
							t.Errorf("%s: task t%d has %d task rows, want 1", name, i, n)
						}
					}
					if summary && !s092HasRow(rows, "run summary") {
						t.Errorf("%s: the batch summary was dropped", name)
					}
				}
			}
		}
	}
}
