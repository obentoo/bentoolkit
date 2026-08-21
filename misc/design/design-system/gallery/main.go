// Command gallery renders every component of the bentoo design system so the
// catalogue can be looked at rather than imagined.
//
//	go run ./misc/design/design-system/gallery            # the mode this terminal gets
//	go run ./misc/design/design-system/gallery -all       # all three, stacked
//	go run ./misc/design/design-system/gallery -mode=plain
//	go run ./misc/design/design-system/gallery | cat      # watch it degrade to Plain
//
// The last one is the demonstration that matters: piping the gallery is the
// same act as piping any bentoo command, and the mode decision is the same
// function.
//
// It reads component.Catalogue(), which is also what the contract test
// iterates. One list, so the picture and the assertions cannot drift.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/misc/design/design-system/component"
	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

func main() {
	all := flag.Bool("all", false, "render every mode, stacked, instead of the one this terminal gets")
	mode := flag.String("mode", "", "force a mode: plain, unicode or styled")
	flag.Parse()

	modes, err := selectModes(*all, *mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gallery:", err)
		os.Exit(2)
	}
	for i, m := range modes {
		if i > 0 {
			fmt.Println()
		}
		if err := render(os.Stdout, m); err != nil {
			fmt.Fprintln(os.Stderr, "gallery:", err)
			os.Exit(1)
		}
	}
}

// selectModes resolves the flags to the list of modes to draw.
//
// With neither flag it asks the SAME question a command asks — the stdout TTY
// probe from internal/common/output, and the NO_COLOR convention where an empty
// value means "not set". That is deliberate: the gallery must not be able to
// look different from the tool it documents.
func selectModes(all bool, forced string) ([]theme.Mode, error) {
	if all {
		return []theme.Mode{theme.Plain, theme.Unicode, theme.Styled}, nil
	}
	switch forced {
	case "":
		return []theme.Mode{theme.ModeFor(output.IsTerminal(), os.Getenv("NO_COLOR") != "")}, nil
	case "plain":
		return []theme.Mode{theme.Plain}, nil
	case "unicode":
		return []theme.Mode{theme.Unicode}, nil
	case "styled":
		return []theme.Mode{theme.Styled}, nil
	default:
		return nil, fmt.Errorf("unknown mode %q: want plain, unicode or styled", forced)
	}
}

// lineWriter writes whole lines and LATCHES the first error, so render makes
// one check at the end instead of one per line.
//
// The alternative is an `if err != nil` after every Fprintln, which would
// triple the length of a function whose entire job is layout, or discarding the
// error, which is what errcheck exists to stop. Writing to stdout does fail in
// practice — a full disk, a closed pipe — and exiting 0 after failing to print
// is a lie.
type lineWriter struct {
	w   io.Writer
	err error
}

// line writes one line, or does nothing once a write has already failed.
func (lw *lineWriter) line(a ...any) {
	if lw.err != nil {
		return
	}
	_, lw.err = fmt.Fprintln(lw.w, a...)
}

// render draws the whole catalogue for one mode.
//
// It takes an io.Writer rather than the *os.File it is always called with in
// main, so a test can assert on what it produced. The mode is a parameter for
// the same reason: nothing here reads the terminal.
func render(w io.Writer, m theme.Mode) error {
	t := theme.New(m)
	out := &lineWriter{w: w}

	out.line(component.Rule{Label: "mode: " + m.String(), Width: component.DefaultWidth}.Render(t))
	// The prose under each name is the GALLERY's text, not a component's, and it
	// quotes glyphs like ─ and → because those are what it is talking about. Only
	// the indented block below each description is component output, and only that
	// block is what the mode contract governs.
	out.line(t.Paint(theme.Muted, "(indented blocks are component output; the prose above each is the gallery's own)"))
	out.line()

	for _, s := range component.Catalogue() {
		out.line(t.Paint(theme.Heading, s.Name))
		for _, ln := range strings.Split(wrap(s.Why, component.DefaultWidth-2), "\n") {
			out.line(t.Paint(theme.Muted, "  "+ln))
		}
		out.line()
		for _, ln := range strings.Split(s.C.Render(t), "\n") {
			out.line("    " + ln)
		}
		out.line()
	}
	return out.err
}

// wrap breaks s at width on word boundaries. Deliberately naive: it is the
// gallery's own prose, never a component's output, and a real wrapper belongs
// in the catalogue only once a command needs one.
func wrap(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	lines := []string{words[0]}
	for _, word := range words[1:] {
		last := len(lines) - 1
		if theme.Width(lines[last])+1+theme.Width(word) <= width {
			lines[last] += " " + word
			continue
		}
		lines = append(lines, word)
	}
	return strings.Join(lines, "\n")
}
