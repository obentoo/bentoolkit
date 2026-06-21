package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestPlainReporterIsReporter(t *testing.T) {
	var _ Reporter = newPlainReporter(io.Discard, 0)
}

// R2.2/R2.3: plain mode emits deterministic START/STAGE/tail/OK lines, prefixed
// with the task id, with NO ANSI escapes and NO carriage returns (\r updates are
// collapsed into full lines). The full capturedOutput is not dumped by the line.
func TestPlainReporterLineSequenceNoANSI(t *testing.T) {
	var buf bytes.Buffer
	pr := newPlainReporter(&buf, 0) // throttle disabled for deterministic output

	pr.BatchStart(1)
	pr.TaskStart("p1", "cat/pkg")
	pr.TaskStage("p1", "manifest")
	pr.TaskLine("p1", StreamStdout, "downloading 10%", false)
	pr.TaskLine("p1", StreamStdout, "downloading 100%", false)
	pr.TaskLine("p1", StreamStdout, "saved", true)
	pr.TaskDone("p1", true, "updated", "FULL-CAPTURED-OUTPUT")

	out := buf.String()

	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("plain output must not contain ANSI ESC (0x1b):\n%q", out)
	}
	if strings.ContainsRune(out, '\r') {
		t.Errorf("plain output must not contain a carriage return:\n%q", out)
	}
	if !strings.Contains(out, "[p1]") {
		t.Errorf("plain output should prefix lines with the task id [p1]:\n%s", out)
	}
	if strings.Contains(out, "FULL-CAPTURED-OUTPUT") {
		t.Errorf("plain reporter must not dump capturedOutput:\n%s", out)
	}

	ordered := []string{
		"START cat/pkg",
		"manifest",
		"downloading 10%",
		"downloading 100%",
		"saved",
		"OK updated",
	}
	last := -1
	for _, w := range ordered {
		idx := strings.Index(out, w)
		if idx < 0 {
			t.Errorf("missing %q in:\n%s", w, out)
			continue
		}
		if idx < last {
			t.Errorf("%q is out of order in:\n%s", w, out)
		}
		last = idx
	}
}

// A failed task renders a FAIL line carrying the summary.
func TestPlainReporterFailLine(t *testing.T) {
	var buf bytes.Buffer
	pr := newPlainReporter(&buf, 0)
	pr.TaskStart("p2", "cat/bad")
	pr.TaskDone("p2", false, "manifest failed", "err details")
	if !strings.Contains(buf.String(), "FAIL manifest failed") {
		t.Errorf("expected a FAIL line with the summary:\n%s", buf.String())
	}
}

// Each committed (eol=true) line is a full line terminated by \n — never \r.
func TestPlainReporterCommittedLinesAreFullLines(t *testing.T) {
	var buf bytes.Buffer
	pr := newPlainReporter(&buf, 0)
	pr.TaskLine("p1", StreamStdout, "a", true)
	pr.TaskLine("p1", StreamStdout, "b", true)
	out := buf.String()
	if strings.Count(out, "\n") < 2 {
		t.Errorf("expected each committed line on its own line:\n%q", out)
	}
}
