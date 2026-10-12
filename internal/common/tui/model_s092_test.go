package tui

// Authored for story 092 (autoupdate-apply-observable), task 1: the live region
// fits the terminal (R1.1-R1.4) and a failure is one line (R2.1, R2.2).
// Frames are asserted ANSI-stripped at the fixed sizes 80x24 and 40x10.

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// s092Frame drives a fresh model sized w x h through msgs and returns the
// ANSI-stripped frame split into rows.
func s092Frame(w, h int, msgs ...tea.Msg) []string {
	var m tea.Model = newModel()
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return strings.Split(stripANSI(m.View()), "\n")
}

func s092Rows(rows []string) string { return strings.Join(rows, "\n") }

// s092History returns TaskStart+TaskDone pairs for n packages pkg-00..pkg-(n-1).
func s092History(n int) []tea.Msg {
	var msgs []tea.Msg
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("pkg-%02d", i)
		msgs = append(msgs, TaskStartMsg{ID: id, Label: id}, TaskDoneMsg{ID: id, OK: true, Summary: "ok"})
	}
	return msgs
}

func s092HasRow(rows []string, want string) bool {
	for _, r := range rows {
		if r == want {
			return true
		}
	}
	return false
}

// R1.1: a line wider than the terminal is cut to width-1 cells ending in "…";
// every row of the frame, the indented tail included, stays within width-1.
func TestModelWidthTruncatesWideLines(t *testing.T) {
	long := strings.Repeat("x", 150)
	for _, size := range []struct{ w, h int }{{80, 24}, {40, 10}} {
		rows := s092Frame(size.w, size.h,
			BatchStartMsg{Total: 1},
			TaskStartMsg{ID: "p1", Label: "dev-go/govulncheck"},
			TaskStageMsg{ID: "p1", Stage: "manifest"},
			TaskLineMsg{ID: "p1", Text: long, EOL: true},
			TaskLineMsg{ID: "p1", Text: "short", EOL: true},
		)
		sawCut := false
		for _, r := range rows {
			if wd := ansi.StringWidth(r); wd > size.w-1 {
				t.Errorf("%dx%d: row is %d cells wide, want <= %d: %q", size.w, size.h, wd, size.w-1, r)
			}
			if strings.Contains(r, "xxxxx") {
				sawCut = true
				if !strings.HasSuffix(r, "…") {
					t.Errorf("%dx%d: truncated row does not end in …: %q", size.w, size.h, r)
				}
				if wd := ansi.StringWidth(r); wd != size.w-1 {
					t.Errorf("%dx%d: truncated row is %d cells, want exactly %d", size.w, size.h, wd, size.w-1)
				}
			}
		}
		if !sawCut {
			t.Errorf("%dx%d: the long tail line vanished instead of being truncated:\n%s", size.w, size.h, s092Rows(rows))
		}
		if !s092HasRow(rows, "    short") {
			t.Errorf("%dx%d: a narrow tail line was altered:\n%s", size.w, size.h, s092Rows(rows))
		}
	}
}

// R1.1 hostile half: width is measured in display cells, not bytes or runes.
// Wrong collapse: a line whose BYTES exceed the width but whose cells do not
// (multi-byte runes, zero-width escape codes) must stay whole. Wrong split: a
// line whose RUNE count fits but whose wide (2-cell) runes do not must be cut.
func TestModelWidthCountsCellsNotBytes(t *testing.T) {
	accented := strings.Repeat("é", 70)                         // 70 cells, 140 bytes
	colored := "\x1b[31m" + strings.Repeat("c", 70) + "\x1b[0m" // 70 cells
	wide := strings.Repeat("界", 50)                             // 50 runes, 100 cells
	rows := s092Frame(80, 24,
		BatchStartMsg{Total: 1},
		TaskStartMsg{ID: "p1", Label: "cat/pkg"},
		TaskLineMsg{ID: "p1", Text: accented, EOL: true},
		TaskLineMsg{ID: "p1", Text: colored, EOL: true},
		TaskLineMsg{ID: "p1", Text: wide, EOL: true},
	)
	frame := s092Rows(rows)
	if !s092HasRow(rows, "    "+accented) {
		t.Errorf("a 74-cell line of multi-byte runes was truncated at width 80 (bytes counted as cells):\n%s", frame)
	}
	if !s092HasRow(rows, "    "+strings.Repeat("c", 70)) {
		t.Errorf("a 74-cell line carrying escape codes was truncated at width 80 (escapes counted as cells):\n%s", frame)
	}
	cut := false
	for _, r := range rows {
		if strings.Contains(r, "界") {
			if ansi.StringWidth(r) > 79 {
				t.Errorf("a line of wide runes renders %d cells at width 80 (runes counted as cells): %q", ansi.StringWidth(r), r)
			}
			cut = strings.HasSuffix(r, "…")
		}
	}
	if !cut {
		t.Errorf("the 104-cell wide-rune line was not truncated with …:\n%s", frame)
	}
}

// R1.2: a resize re-renders with the new width, in both directions.
func TestModelResizeRerendersFrame(t *testing.T) {
	line := strings.Repeat("r", 60)
	var m tea.Model = newModel()
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 80, Height: 24},
		BatchStartMsg{Total: 1},
		TaskStartMsg{ID: "p1", Label: "cat/pkg"},
		TaskLineMsg{ID: "p1", Text: line, EOL: true},
	} {
		m, _ = m.Update(msg)
	}
	if !strings.Contains(stripANSI(m.View()), "    "+line) {
		t.Fatalf("at 80 columns the 64-cell line must render whole:\n%s", stripANSI(m.View()))
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	narrow := stripANSI(m.View())
	if strings.Contains(narrow, line) {
		t.Errorf("after shrinking to 40 columns the line still renders whole:\n%s", narrow)
	}
	for _, r := range strings.Split(narrow, "\n") {
		if ansi.StringWidth(r) > 39 {
			t.Errorf("after the resize a row is %d cells wide, want <= 39: %q", ansi.StringWidth(r), r)
		}
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !strings.Contains(stripANSI(m.View()), "    "+line) {
		t.Errorf("after growing back to 80 columns the line is still truncated:\n%s", stripANSI(m.View()))
	}
}

// R1.3/R1.4 hostile half (wrong fire): a frame of exactly height-1 rows drops
// nothing and shows no fold line; one row more folds, and the fold line itself
// takes a row.
func TestModelHeightFitsExactlyWithoutFold(t *testing.T) {
	// 40x10: budget 9 rows. Header + 8 history rows = 9: fits.
	rows := s092Frame(40, 10, append([]tea.Msg{BatchStartMsg{Total: 9}}, s092History(8)...)...)
	if len(rows) != 9 {
		t.Errorf("header + 8 history rows: got %d rows, want 9:\n%s", len(rows), s092Rows(rows))
	}
	for _, r := range rows {
		if strings.Contains(r, "earlier package(s) finished") {
			t.Errorf("a frame that fits shows a fold line: %q", r)
		}
	}
	// Header + 9 history rows = 10 > 9: two history rows go (the fold takes one).
	rows = s092Frame(40, 10, append([]tea.Msg{BatchStartMsg{Total: 9}}, s092History(9)...)...)
	if len(rows) > 9 {
		t.Errorf("an overflowing frame has %d rows, want <= 9:\n%s", len(rows), s092Rows(rows))
	}
	if !s092HasRow(rows, "… 2 earlier package(s) finished") {
		t.Errorf("want the fold line for 2 dropped packages:\n%s", s092Rows(rows))
	}
}

// R1.3/R1.4: history is dropped OLDEST first, the fold line stands in their
// place (above the kept history), its count names exactly the dropped entries,
// and the header and the active task row survive.
func TestModelHeightFoldsOldestHistoryFirst(t *testing.T) {
	msgs := append([]tea.Msg{BatchStartMsg{Total: 11}}, s092History(10)...)
	msgs = append(msgs,
		TaskStartMsg{ID: "act", Label: "dev-go/active"},
		TaskLineMsg{ID: "act", Text: "tail-a", EOL: true},
		TaskLineMsg{ID: "act", Text: "tail-b", EOL: true},
	)
	rows := s092Frame(40, 10, msgs...)
	frame := s092Rows(rows)
	if len(rows) > 9 {
		t.Fatalf("frame has %d rows, want <= 9 at height 10:\n%s", len(rows), frame)
	}
	if !strings.HasPrefix(rows[0], "Applying updates") {
		t.Errorf("the header row is gone:\n%s", frame)
	}
	if !strings.Contains(frame, "dev-go/active") {
		t.Errorf("the active task row was dropped:\n%s", frame)
	}
	// Budget 9: header, task row, 2 tails = 4; fold + 4 history = 5.
	fold := -1
	for i, r := range rows {
		if r == "… 6 earlier package(s) finished" {
			fold = i
		}
	}
	if fold < 0 {
		t.Fatalf("want the fold line for the 6 oldest packages:\n%s", frame)
	}
	for i := 0; i < 10; i++ {
		row := fmt.Sprintf("✓ pkg-%02d — ok", i)
		kept := s092HasRow(rows, row)
		if i < 6 && kept {
			t.Errorf("an OLD history row survived while newer ones were folded: %q\n%s", row, frame)
		}
		if i >= 6 && !kept {
			t.Errorf("a NEW history row was dropped: %q\n%s", row, frame)
		}
	}
	for i, r := range rows {
		if strings.HasPrefix(r, "✓ pkg-") && i < fold {
			t.Errorf("a kept history row renders above the fold line: %q\n%s", r, frame)
		}
	}
}

// R1.3: once no history is left to drop, tail lines go — never the header nor
// one row per active task.
func TestModelHeightTrimsTailsAfterHistory(t *testing.T) {
	msgs := append([]tea.Msg{BatchStartMsg{Total: 23}}, s092History(20)...)
	for _, id := range []string{"act-1", "act-2", "act-3"} {
		msgs = append(msgs, TaskStartMsg{ID: id, Label: "dev-go/" + id})
		for i := 0; i < 10; i++ {
			msgs = append(msgs, TaskLineMsg{ID: id, Text: fmt.Sprintf("%s-line-%d", id, i), EOL: true})
		}
	}
	rows := s092Frame(40, 10, msgs...)
	frame := s092Rows(rows)
	if len(rows) > 9 {
		t.Fatalf("frame has %d rows, want <= 9:\n%s", len(rows), frame)
	}
	if !strings.HasPrefix(rows[0], "Applying updates") {
		t.Errorf("the header row is gone:\n%s", frame)
	}
	for _, id := range []string{"act-1", "act-2", "act-3"} {
		n := 0
		for _, r := range rows {
			if strings.HasSuffix(r, " dev-go/"+id) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("active task %s has %d task rows, want 1:\n%s", id, n, frame)
		}
	}
	if !s092HasRow(rows, "… 20 earlier package(s) finished") {
		t.Errorf("want the fold line for all 20 finished packages:\n%s", frame)
	}
	for _, r := range rows {
		if strings.HasPrefix(r, "✓ pkg-") {
			t.Errorf("a history row survived while tails had to be trimmed: %q", r)
		}
	}
}

// Golden frames at the two fixed sizes: history folded and a failure line cut.
func TestModelHeightGoldenFrames(t *testing.T) {
	fail := TaskDoneMsg{ID: "bad", OK: false, Summary: "manifest failed: " + strings.Repeat("y", 100) + "\nOutput: wget dot rows"}
	msgs := append([]tea.Msg{BatchStartMsg{Total: 31}}, s092History(30)...)
	msgs = append(msgs, TaskStartMsg{ID: "bad", Label: "dev-go/govulncheck"}, fail)

	failRow := "✗ dev-go/govulncheck — manifest failed: " + strings.Repeat("y", 100)
	rows40 := []string{"Applying updates  31/31", "… 24 earlier package(s) finished"}
	for i := 24; i < 30; i++ {
		rows40 = append(rows40, fmt.Sprintf("✓ pkg-%02d — ok", i))
	}
	rows40 = append(rows40, ansi.Truncate(failRow, 39, "…"))
	want40 := strings.Join(rows40, "\n")
	if got := s092Rows(s092Frame(40, 10, msgs...)); got != want40 {
		t.Errorf("40x10 frame:\n--- got ---\n%s\n--- want ---\n%s", got, want40)
	}

	rows80 := []string{"Applying updates  31/31", "… 10 earlier package(s) finished"}
	for i := 10; i < 30; i++ {
		rows80 = append(rows80, fmt.Sprintf("✓ pkg-%02d — ok", i))
	}
	rows80 = append(rows80, ansi.Truncate(failRow, 79, "…"))
	want80 := strings.Join(rows80, "\n")
	if got := s092Rows(s092Frame(80, 24, msgs...)); got != want80 {
		t.Errorf("80x24 frame:\n--- got ---\n%s\n--- want ---\n%s", got, want80)
	}
}

// R2.1: a failed package's history row carries the first line of the error
// only; nothing of the embedded child output reaches the frame.
func TestModelFailureLineShowsFirstErrorLineOnly(t *testing.T) {
	summary := "manifest command failed: staged manifest for dev-go/govulncheck-1.1.4: exit status 1\n" +
		"Output: --2026-10-11-- https://proxy.golang.org/x.zip\n" +
		"     0K .......... .......... ..........  1% 1.2M 5s\n" +
		"ERROR 404: Not Found."
	rows := s092Frame(80, 24,
		BatchStartMsg{Total: 1},
		TaskStartMsg{ID: "p1", Label: "dev-go/govulncheck"},
		TaskDoneMsg{ID: "p1", OK: false, Summary: summary},
	)
	frame := s092Rows(rows)
	if len(rows) != 2 {
		t.Errorf("header + one ✗ row expected, got %d rows:\n%s", len(rows), frame)
	}
	for _, leak := range []string{"Output:", "proxy.golang.org", "0K ..........", "ERROR 404"} {
		if strings.Contains(frame, leak) {
			t.Errorf("the ✗ row carries the child output (%q):\n%s", leak, frame)
		}
	}
	if !strings.HasPrefix(rows[len(rows)-1], "✗ dev-go/govulncheck — manifest command failed: staged manifest") {
		t.Errorf("the ✗ row lost the error's first line:\n%s", frame)
	}
	// Converse: a one-line success summary is unchanged.
	ok := s092Frame(80, 24, BatchStartMsg{Total: 1}, TaskStartMsg{ID: "p2", Label: "dev-go/ok"},
		TaskDoneMsg{ID: "p2", OK: true, Summary: "updated"})
	if !s092HasRow(ok, "✓ dev-go/ok — updated") {
		t.Errorf("the success row changed:\n%s", s092Rows(ok))
	}
}

// R2.1: the first line is itself truncated as in R1.1.
func TestModelFailureLineTruncatedToWidth(t *testing.T) {
	rows := s092Frame(40, 10,
		BatchStartMsg{Total: 1},
		TaskStartMsg{ID: "p1", Label: "dev-go/govulncheck"},
		TaskDoneMsg{ID: "p1", OK: false, Summary: strings.Repeat("e", 80) + "\nsecond"},
	)
	last := rows[len(rows)-1]
	if ansi.StringWidth(last) != 39 || !strings.HasSuffix(last, "…") {
		t.Errorf("the ✗ row is %d cells (want 39, ending in …): %q", ansi.StringWidth(last), last)
	}
}
