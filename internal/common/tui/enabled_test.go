package tui

import "testing"

// R2.1/R2.2 (AD7): the TUI is enabled iff stdout is a TTY AND none of --no-tui,
// NO_COLOR, BENTOO_NO_TUI is set. The TTY stat is faked via the package seam.
func TestEnabledTruthTable(t *testing.T) {
	orig := isTerminal
	t.Cleanup(func() { isTerminal = orig })

	cases := []struct {
		name        string
		tty         bool
		noTUI       bool
		noColor     string
		bentooNoTUI string
		want        bool
	}{
		{"tty, nothing set -> enabled", true, false, "", "", true},
		{"not a tty -> plain", false, false, "", "", false},
		{"--no-tui flag -> plain", true, true, "", "", false},
		{"NO_COLOR set -> plain", true, false, "1", "", false},
		{"BENTOO_NO_TUI set -> plain", true, false, "", "1", false},
		{"not tty + flag -> plain", false, true, "", "", false},
		{"empty NO_COLOR is not 'set'", true, false, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isTerminal = func() bool { return c.tty }
			t.Setenv("NO_COLOR", c.noColor)
			t.Setenv("BENTOO_NO_TUI", c.bentooNoTUI)
			if got := Enabled(Options{NoTUI: c.noTUI}); got != c.want {
				t.Errorf("Enabled(tty=%v,noTUI=%v,NO_COLOR=%q,BENTOO_NO_TUI=%q) = %v, want %v",
					c.tty, c.noTUI, c.noColor, c.bentooNoTUI, got, c.want)
			}
		})
	}
}
