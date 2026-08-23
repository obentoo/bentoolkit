package render

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// captureRun redirects os.Stdout for the duration of fn and returns what was
// written to it.
//
// It takes func() rather than func() error because one of the paths under test
// PANICS on purpose. If fn panics, the deferred restore still runs and the
// panic keeps propagating — so a caller testing that path must recover INSIDE
// fn, or this function never returns and the captured output is lost.
func captureRun(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating the capture pipe: %v", err)
	}
	os.Stdout = w

	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		captured <- buf.String()
	}()

	func() {
		defer func() {
			_ = w.Close()
			os.Stdout = original
		}()
		fn()
	}()

	out := <-captured
	_ = r.Close()
	return out
}

// stubRunProgram replaces the package seam that actually starts the bubbletea
// program, and returns the restore function.
//
// The seam exists because the four exit paths R4.2 enumerates — normal
// completion, the quit key, an interrupt, and a panic — cannot all be driven
// through a real alternate-screen program under `go test`, where stdout is a
// pipe and there is no terminal to take over. The repository already uses this
// shape: internal/common/tui/enabled.go documents `isTerminal` as "the package
// seam for the stdout-TTY stat ... overridden by tests".
//
// What the seam does NOT do is stub out the thing being tested. Fullscreen's
// own logic — capturing the report before the program starts, the deferred
// recover, joining the errors, and printing on the way out — runs for real in
// every test below. Only the terminal takeover is replaced.
func stubRunProgram(fake func(tea.Model, ...tea.ProgramOption) (tea.Model, error)) func() {
	original := runProgram
	runProgram = fake
	return func() { runProgram = original }
}

// ---------------------------------------------------------------------------
// Sub-task 5.1 — the alt-screen program
// ---------------------------------------------------------------------------

// TestFullscreenViewHasEverySectionHeading pins R2.4 for the third mode. The
// fullscreen renderer is the one most tempting to write as its own thing, and
// a heading that exists in plain but not here is precisely the divergence the
// view model was introduced to make impossible.
func TestFullscreenViewHasEverySectionHeading(t *testing.T) {
	model := newModel(fixtureReport(), Options{Width: 100})
	sized, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 60})

	view := ansi.Strip(sized.View())
	plain := ansi.Strip(renderPlain(t, Options{Width: 100}))

	// Every heading the plain render produces must appear here too. Headings
	// are taken from the plain output rather than hard-coded, so renaming a
	// section cannot make this test pass by accident.
	for _, heading := range headingsIn(plain) {
		if !strings.Contains(view, heading) {
			t.Errorf("the fullscreen view has no %q section (R2.4)\n--- view ---\n%s", heading, view)
		}
	}
}

// headingsIn returns the section titles of a plain render: the lines
// immediately followed by a rule of box-drawing characters.
func headingsIn(plain string) []string {
	lines := strings.Split(plain, "\n")

	var headings []string
	for i := 0; i+1 < len(lines); i++ {
		rule := strings.TrimSpace(lines[i+1])
		if rule == "" || strings.Trim(rule, "─-=") != "" {
			continue
		}
		if title := strings.TrimSpace(lines[i]); title != "" {
			headings = append(headings, title)
		}
	}
	return headings
}

// TestFullscreenOmissionIsVisible pins R2.5. Silently truncating is the failure
// mode: an operator reading a screen that shows four rows has no way to know
// the run produced forty, and a report that hides its own incompleteness is
// worse than one that will not fit.
func TestFullscreenOmissionIsVisible(t *testing.T) {
	tall := fixtureReport()

	// Twelve more scanned packages than any short viewport can hold.
	for i := range 12 {
		tall.Scanned = append(tall.Scanned, report.PackageResult{
			Package:          "dev-test/filler-" + string(rune('a'+i)),
			Type:             "source",
			CurrentVersion:   "1.0",
			CandidateVersion: "1.0",
		})
	}

	model := newModel(tall, Options{Width: 100, ShowAll: true})
	sized, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 8})
	view := ansi.Strip(sized.View())

	if strings.Count(view, "\n") > 12 {
		t.Fatalf("the view is taller than the 8-row viewport it was given:\n%s", view)
	}

	// Some count of what is not on screen has to appear. The wording is the
	// renderer's to choose; the presence of a number is not.
	if !strings.ContainsAny(view, "0123456789") || !mentionsOmission(view) {
		t.Errorf("a viewport too small for the report says nothing about what it left out (R2.5)\n--- view ---\n%s", view)
	}
}

func mentionsOmission(view string) bool {
	lower := strings.ToLower(view)
	for _, word := range []string{"more", "omitted", "not shown", "hidden", "remaining", "scroll"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Sub-task 5.2 — the exit dump on every path
// ---------------------------------------------------------------------------

// reportWasDumped reports whether captured stdout carries the report rather
// than merely some output. It looks for content only the report has.
func reportWasDumped(out string) bool {
	stripped := ansi.Strip(out)
	return strings.Contains(stripped, "app-misc/jq") &&
		strings.Contains(stripped, "inconclusive")
}

// TestDumpOnNormalExit — R4.1. The program ran and returned; the scrollback
// gets the report.
func TestDumpOnNormalExit(t *testing.T) {
	out := captureRun(t, func() {
		defer stubRunProgram(func(m tea.Model, _ ...tea.ProgramOption) (tea.Model, error) {
			return m, nil
		})()

		if err := Fullscreen(fixtureReport(), Options{Width: 100}); err != nil {
			t.Errorf("Fullscreen returned an error on the normal path: %v", err)
		}
	})

	if !reportWasDumped(out) {
		t.Errorf("nothing was printed to the scrollback on normal exit (R4.1)\n--- stdout ---\n%s", out)
	}
}

// TestDumpOnQuit — R4.2, the quit key. The model returns tea.Quit and the
// program ends without error; the report still lands.
func TestDumpOnQuit(t *testing.T) {
	out := captureRun(t, func() {
		defer stubRunProgram(func(m tea.Model, _ ...tea.ProgramOption) (tea.Model, error) {
			quit, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
			if cmd == nil {
				t.Error("pressing q produced no command — nothing asked the program to quit")
			}
			return quit, nil
		})()

		if err := Fullscreen(fixtureReport(), Options{Width: 100}); err != nil {
			t.Errorf("Fullscreen returned an error on the quit path: %v", err)
		}
	})

	if !reportWasDumped(out) {
		t.Errorf("nothing was printed to the scrollback after the quit key (R4.2)\n--- stdout ---\n%s", out)
	}
}

// TestDumpOnPanic is the path D7 exists for, and the reason the report must
// never be read from the bubbletea model: a panic inside View() would take the
// report with it. The report is captured before the program starts, so the
// deferred branch has something whole to print.
//
// Both halves are asserted. Printing the report while swallowing the panic
// would turn a crash into a silent wrong answer, which is worse than the crash.
func TestDumpOnPanic(t *testing.T) {
	var recovered any

	out := captureRun(t, func() {
		defer func() { recovered = recover() }()
		defer stubRunProgram(func(tea.Model, ...tea.ProgramOption) (tea.Model, error) {
			panic("View() blew up")
		})()

		_ = Fullscreen(fixtureReport(), Options{Width: 100})
	})

	if !reportWasDumped(out) {
		t.Errorf("the report was lost when the program panicked (R4.2)\n--- stdout ---\n%s", out)
	}
	if recovered == nil {
		t.Error("Fullscreen swallowed the panic — a crash reported as success is worse than a crash")
	}
}

// TestDumpOnInterrupt — R4.2's fourth path. ctrl+c reaches the model through
// its real Update, and the report still reaches the scrollback.
//
// This asserts only that the report survived. That an interrupted report also
// SAYS it is partial is R4.3, and it is asserted by sub-task 5.3, which appends
// its own test for the interrupt path to this file. Splitting them keeps this
// test failing for one reason.
func TestDumpOnInterrupt(t *testing.T) {
	out := captureRun(t, func() {
		defer stubRunProgram(func(m tea.Model, _ ...tea.ProgramOption) (tea.Model, error) {
			interrupted, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			return interrupted, nil
		})()

		_ = Fullscreen(fixtureReport(), Options{Width: 100})
	})

	if !reportWasDumped(out) {
		t.Fatalf("the report was lost on the interrupt path (R4.2)\n--- stdout ---\n%s", out)
	}
}

// TestFullscreenJoinsTheProgramError — R4.1 must not cost the caller the error.
// A program that failed still prints its report, and the failure still reaches
// the caller.
func TestFullscreenJoinsTheProgramError(t *testing.T) {
	boom := errors.New("the program could not start")

	out := captureRun(t, func() {
		defer stubRunProgram(func(m tea.Model, _ ...tea.ProgramOption) (tea.Model, error) {
			return m, boom
		})()

		if err := Fullscreen(fixtureReport(), Options{Width: 100}); !errors.Is(err, boom) {
			t.Errorf("Fullscreen returned %v, want an error wrapping %v", err, boom)
		}
	})

	if !reportWasDumped(out) {
		t.Errorf("a failed program cost the operator the report (R4.1)\n--- stdout ---\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Sub-task 5.3 — an interrupted run says so. APPENDED to fullscreen_test.go.
// ---------------------------------------------------------------------------

// saysIncomplete reports whether a rendered report admits it is partial. The
// wording is the renderer's to choose; that it says SOMETHING, and states a
// number, is not.
func saysIncomplete(rendered string) bool {
	lower := strings.ToLower(rendered)
	for _, word := range []string{"incomplete", "interrupted", "partial", "not evaluated"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// TestIncompleteIsLabelledOnTheInterruptPath closes R4.3 where it actually
// happens. An operator who pressed ctrl+c knows they interrupted the run; the
// person reading the log they pasted does not, and a partial report that reads
// as a complete one is a wrong answer rather than a short one.
func TestIncompleteIsLabelledOnTheInterruptPath(t *testing.T) {
	out := captureRun(t, func() {
		defer stubRunProgram(func(m tea.Model, _ ...tea.ProgramOption) (tea.Model, error) {
			interrupted, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			return interrupted, nil
		})()

		_ = Fullscreen(fixtureReport(), Options{Width: 100})
	})

	stripped := ansi.Strip(out)
	if !saysIncomplete(stripped) {
		t.Errorf("an interrupted run printed a report that does not admit it is partial (R4.3)\n--- stdout ---\n%s", stripped)
	}
}

// TestCompleteRunIsNotLabelledOnTheFullscreenPath is the guard. A label printed
// unconditionally would satisfy the test above and make every report look
// interrupted — which would train the reader to ignore the one that matters.
func TestCompleteRunIsNotLabelledOnTheFullscreenPath(t *testing.T) {
	out := captureRun(t, func() {
		defer stubRunProgram(func(m tea.Model, _ ...tea.ProgramOption) (tea.Model, error) {
			return m, nil
		})()

		_ = Fullscreen(fixtureReport(), Options{Width: 100})
	})

	if saysIncomplete(ansi.Strip(out)) {
		t.Errorf("a run that completed normally was labelled incomplete\n--- stdout ---\n%s", ansi.Strip(out))
	}
}

// ---------------------------------------------------------------------------
// Quality Gate — "R2.4 honoured: the three modes compared on stripped content,
// not merely rendered without error". Authored at gate-settlement time, after
// the task list closed: the sub-tasks compared plain against inline
// (TestInlineMatchesPlainContent) and checked fullscreen only for the PRESENCE
// of every section heading, which is a weaker claim than the gate makes.
// ---------------------------------------------------------------------------

// unframe strips the chrome the fullscreen mode adds and returns the content
// inside it: the box drawn with borderStyle, and the key-binding line beneath.
//
// Removing them is not weakening the comparison — it IS the comparison. R2.4
// permits the modes to differ in presentation and nothing else, and a border is
// the clearest case of presentation there is. What must survive unframing is
// every character of the report.
func unframe(view string) string {
	var content []string

	for _, line := range strings.Split(view, "\n") {
		trimmed := strings.TrimSpace(line)

		// The box's top and bottom edges.
		if strings.HasPrefix(trimmed, "╭") || strings.HasPrefix(trimmed, "╰") {
			continue
		}
		// A framed line: │ … │
		if strings.HasPrefix(trimmed, "│") && strings.HasSuffix(trimmed, "│") {
			inner := strings.TrimSuffix(strings.TrimPrefix(trimmed, "│"), "│")
			// The box pads its contents by exactly one cell on each side.
			// Trim that one, never more: TrimLeft would eat the report's own
			// indentation, which IS content — it is how a detail line is told
			// from the row it belongs to.
			inner = strings.TrimPrefix(inner, " ")
			content = append(content, strings.TrimRight(inner, " "))
			continue
		}
		// Anything outside the box is chrome — the key-binding line.
	}
	return strings.Join(content, "\n")
}

// TestAllThreeModesAgreeOnContent settles the gate. It is the only assertion in
// the story that puts all three renderers side by side on the same fixture.
//
// The three table styles this story removed were not written by a careless
// author; they were written by three careful ones, independently, with nothing
// to compare against. This is the comparison.
func TestAllThreeModesAgreeOnContent(t *testing.T) {
	opts := Options{Width: 100}

	plain := strings.TrimRight(ansi.Strip(renderPlain(t, opts)), "\n")

	inline := strings.TrimRight(trimTrailing(ansi.Strip(captureStdout(t, func() error {
		return Inline(fixtureReport(), opts)
	}))), "\n")

	// A viewport tall enough that nothing is paginated away — pagination is a
	// property of the screen, not of the report, and R2.5 covers the case where
	// it does have to omit.
	model := newModel(fixtureReport(), opts)
	sized, _ := model.Update(tea.WindowSizeMsg{Width: 104, Height: 200})
	fullscreen := strings.TrimRight(unframe(ansi.Strip(sized.View())), "\n")

	for _, mode := range []struct {
		name string
		got  string
	}{
		{"inline", inline},
		{"fullscreen", fullscreen},
	} {
		if mode.got == plain {
			continue
		}

		plainLines, gotLines := strings.Split(plain, "\n"), strings.Split(mode.got, "\n")
		for i := 0; i < len(plainLines) || i < len(gotLines); i++ {
			var p, g string
			if i < len(plainLines) {
				p = plainLines[i]
			}
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if p != g {
				t.Errorf("%s and plain diverge at line %d of %d/%d (R2.4)\n  plain:      %q\n  %s: %q",
					mode.name, i+1, len(plainLines), len(gotLines), p, mode.name, g)
				break
			}
		}
	}
}
