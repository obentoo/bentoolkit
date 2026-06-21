package tui

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// mockTerm records ReleaseTerminal/RestoreTerminal ordering for RunAttached.
type mockTerm struct {
	order      []string
	releaseErr error
}

func (m *mockTerm) ReleaseTerminal() error {
	m.order = append(m.order, "release")
	return m.releaseErr
}

func (m *mockTerm) RestoreTerminal() error {
	m.order = append(m.order, "restore")
	return nil
}

var _ terminalController = (*mockTerm)(nil)

// R4.1/AD5: RunAttached releases the terminal before the child runs and restores
// it after, so a sudo/doas password prompt reaches the real TTY.
func TestRunAttachedReleasesAndRestores(t *testing.T) {
	m := &mockTerm{}
	if err := RunAttached(m, exec.Command("true")); err != nil {
		t.Fatalf("RunAttached(true) err = %v", err)
	}
	if len(m.order) != 2 || m.order[0] != "release" || m.order[1] != "restore" {
		t.Fatalf("expected [release restore], got %v", m.order)
	}
}

// The terminal must be restored even when the child command fails.
func TestRunAttachedRestoresOnCmdError(t *testing.T) {
	m := &mockTerm{}
	if err := RunAttached(m, exec.Command("false")); err == nil {
		t.Fatalf("expected an error from a failing command")
	}
	if len(m.order) != 2 || m.order[0] != "release" || m.order[1] != "restore" {
		t.Fatalf("terminal must be released then restored even on cmd error, got %v", m.order)
	}
}

// If releasing the terminal fails, the child is NOT run and no restore happens.
func TestRunAttachedReleaseErrorAborts(t *testing.T) {
	m := &mockTerm{releaseErr: errors.New("cannot release")}
	if err := RunAttached(m, exec.Command("true")); err == nil {
		t.Fatalf("expected release error to abort RunAttached")
	}
	if len(m.order) != 1 || m.order[0] != "release" {
		t.Fatalf("on release error, expected only [release], got %v", m.order)
	}
}

// R4.2: a confirmation request renders the prompt in the UI and a 'y' keypress
// replies true without reading os.Stdin behind the program.
func TestModelConfirmYes(t *testing.T) {
	reply := make(chan bool, 1)
	var m tea.Model = newModel()
	m, _ = m.Update(ConfirmMsg{Prompt: "compile cat/pkg?", Reply: reply})

	if !strings.Contains(stripANSI(m.View()), "compile cat/pkg?") {
		t.Errorf("confirm prompt should be visible in the View:\n%s", stripANSI(m.View()))
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	select {
	case got := <-reply:
		if !got {
			t.Errorf("'y' should reply true")
		}
	default:
		t.Errorf("no reply delivered for 'y'")
	}
	// after replying, the prompt is cleared
	if strings.Contains(stripANSI(m.View()), "compile cat/pkg?") {
		t.Errorf("confirm prompt should be cleared after answering")
	}
}

// R4.2: an 'n' keypress replies false.
func TestModelConfirmNo(t *testing.T) {
	reply := make(chan bool, 1)
	var m tea.Model = newModel()
	m, _ = m.Update(ConfirmMsg{Prompt: "proceed?", Reply: reply})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	select {
	case got := <-reply:
		if got {
			t.Errorf("'n' should reply false")
		}
	default:
		t.Errorf("no reply delivered for 'n'")
	}
	// after replying, the prompt is cleared
	if strings.Contains(stripANSI(m.View()), "proceed?") {
		t.Errorf("confirm prompt should be cleared after answering")
	}
}
