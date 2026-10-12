package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/output"
)

// childRunTailLines is how many of a failed child run's last lines the Apply
// Result prints. The run's log file keeps the whole output.
const childRunTailLines = 40

// progressRow matches a wget dot-progress row ("  1250K .......... ...."): a
// byte offset in K followed by groups of dots. Such rows say nothing about the
// failure, so they are dropped before the tail is bounded.
var progressRow = regexp.MustCompile(`^\s*\d+K( \.+)+`)

// displayChildRuns prints every child run reachable from err once, under its
// label: the last childRunTailLines meaningful lines of its output, the count of
// earlier lines left out, and the path of the log that keeps all of them.
func displayChildRuns(err error) {
	for _, run := range autoupdate.ChildRuns(err) {
		lines, omitted := childRunTail(run.Output)
		output.Info.Printf("    %s output:\n", run.Label)
		if omitted > 0 {
			output.Info.Printf("      … %d earlier line(s) omitted\n", omitted)
		}
		if len(lines) == 0 {
			output.Info.Println("      (no output)")
		}
		for _, line := range lines {
			fmt.Printf("      %s\n", displayChildRunLine(line))
		}
		if run.LogPath != "" {
			output.Info.Printf("      Full log: %s\n", displayPendingField(run.LogPath))
		}
	}
}

// childRunTail returns the last childRunTailLines lines of out that are neither
// blank nor progress rows, and how many such lines precede them. A line
// rewritten in place with '\r' counts by what was left on screen: the text
// after its last '\r'.
func childRunTail(out string) (lines []string, omitted int) {
	for line := range strings.SplitSeq(out, "\n") {
		if i := strings.LastIndexByte(line, '\r'); i >= 0 {
			line = line[i+1:]
		}
		if strings.TrimSpace(line) == "" || progressRow.MatchString(line) {
			continue
		}
		lines = append(lines, line)
	}
	if excess := len(lines) - childRunTailLines; excess > 0 {
		return lines[excess:], excess
	}
	return lines, 0
}

// displayChildRunLine returns line unchanged when every rune is printable or a
// tab, and quoted and escaped otherwise: the output comes from a child process
// fed by upstream content, and a raw escape sequence in it would reach the
// operator's terminal.
func displayChildRunLine(line string) string {
	if strings.IndexFunc(line, func(r rune) bool { return r != '\t' && !unicode.IsPrint(r) }) < 0 {
		return line
	}
	return strconv.Quote(line)
}
