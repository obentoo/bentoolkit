package main

// Authored for story 092 (autoupdate-apply-observable), sub-task 2.2: the
// Apply Result prints each child run once, under its label, bounded to its last
// 40 lines with progress rows dropped, plus the run's log path (R3.1-R3.4).

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

func s092Lines(tag string, from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "%s line %03d\n", tag, i)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func s092FailedResult(err error) *autoupdate.ApplyResult {
	return &autoupdate.ApplyResult{Package: "dev-go/govulncheck", OldVersion: "1.1.3", NewVersion: "1.1.4", Error: err}
}

func s092Render(t *testing.T, err error) string {
	t.Helper()
	return captureStdout(t, func() { testAutoupdateRun(testAutoupdateOptions()).displayApplyResult(s092FailedResult(err)) })
}

// R3.1/R3.2/R3.3: both runs printed once each, under their own label, last 40
// lines, the omission line and the log path. Hostile halves: the same run
// reachable twice in the chain prints once (no split), and two runs with
// near-identical outputs both print (no collapse).
func TestApplyResultChildRunEachRunOnce(t *testing.T) {
	first := &autoupdate.ChildRunError{Label: "first manifest run", Output: s092Lines("FIRST", 1, 100), LogPath: "/state/logs/first.log"}
	recheck := &autoupdate.ChildRunError{Label: "re-check after LLM fix", Output: s092Lines("RECHECK", 1, 100), LogPath: "/state/logs/recheck.log"}
	err := errors.Join(
		fmt.Errorf("%w: %w", autoupdate.ErrManifestFailed, first),
		fmt.Errorf("again: %w", first), // the same run reached a second time
		recheck,
	)
	out := s092Render(t, err)

	for _, tag := range []string{"FIRST", "RECHECK"} {
		for i := 61; i <= 100; i++ {
			l := fmt.Sprintf("%s line %03d", tag, i)
			if n := strings.Count(out, l); n != 1 {
				t.Errorf("%q printed %d times, want 1", l, n)
			}
		}
		for i := 2; i <= 60; i++ {
			l := fmt.Sprintf("%s line %03d", tag, i)
			if strings.Contains(out, l) {
				t.Errorf("%q printed: only the last 40 lines belong in the Apply Result", l)
			}
		}
		if n := strings.Count(out, fmt.Sprintf("%s line 001", tag)); n > 1 {
			t.Errorf("%s line 001 printed %d times", tag, n)
		}
	}
	if n := strings.Count(out, "… 60 earlier line(s) omitted"); n != 2 {
		t.Errorf("omission line printed %d times, want once per run (2):\n%s", n, out)
	}
	for _, p := range []string{"Full log: /state/logs/first.log", "Full log: /state/logs/recheck.log"} {
		if n := strings.Count(out, p); n != 1 {
			t.Errorf("%q printed %d times, want 1:\n%s", p, n, out)
		}
	}
	// Each run's tail sits under its own label.
	fStart, fEnd := strings.Index(out, "FIRST line 061"), strings.Index(out, "FIRST line 100")
	rStart := strings.Index(out, "RECHECK line 061")
	if fStart < 0 || rStart < 0 {
		t.Fatalf("tails missing:\n%s", out)
	}
	if !strings.Contains(out[:fStart], "first manifest run") {
		t.Errorf("the first run's tail is not preceded by its label:\n%s", out)
	}
	if fEnd > rStart || !strings.Contains(out[fEnd:rStart], "re-check after LLM fix") {
		t.Errorf("the re-check's tail is not under its own label, after the first run's:\n%s", out)
	}
}

// R3.2 boundaries: exactly 40 lines print whole with no omission line; 41 drop
// one; no LogPath prints no "Full log:".
func TestApplyResultChildRunBoundaries(t *testing.T) {
	out := s092Render(t, &autoupdate.ChildRunError{Label: "first manifest run", Output: s092Lines("FORTY", 1, 40)})
	if strings.Contains(out, "omitted") {
		t.Errorf("40 lines must print whole, with no omission line:\n%s", out)
	}
	if !strings.Contains(out, "FORTY line 001") || !strings.Contains(out, "FORTY line 040") {
		t.Errorf("a 40-line output lost lines:\n%s", out)
	}
	if strings.Contains(out, "Full log:") {
		t.Errorf("no LogPath, yet a Full log line:\n%s", out)
	}
	out = s092Render(t, &autoupdate.ChildRunError{Label: "first manifest run", Output: s092Lines("FORTYONE", 1, 41), LogPath: "/l"})
	if !strings.Contains(out, "… 1 earlier line(s) omitted") || strings.Contains(out, "FORTYONE line 001") {
		t.Errorf("41 lines must drop exactly the first one:\n%s", out)
	}
}

// R3.4: wget dot rows are dropped before the bound. Hostile halves: lines that
// merely resemble progress (a size with no dot groups, wget's own status lines,
// an error) are kept; every progress shape — indented or not, wide offsets, a
// short last row — is dropped.
func TestApplyResultChildRunDropsProgressRows(t *testing.T) {
	var b strings.Builder
	b.WriteString("--2026-10-11 10:00:00--  https://proxy.golang.org/x.zip\n")
	b.WriteString("Length: 6553600 (6.2M) [application/octet-stream]\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "%6dK .......... .......... .......... .......... .......... %2d%% 1.2M 5s\n", i*50, i%100)
	}
	b.WriteString("123456K .......... ..                                       100%  9.9M=0.6s\n")
	b.WriteString("50K .......... .......... 1%\n")
	b.WriteString("100K files checked\n")
	b.WriteString("2026-10-11 10:00:05 ERROR 404: Not Found.")
	out := s092Render(t, &autoupdate.ChildRunError{Label: "first manifest run", Output: b.String(), LogPath: "/l"})
	if strings.Contains(out, "K .......... ") || strings.Contains(out, "K .......... ..") {
		t.Errorf("progress rows reached the Apply Result:\n%s", out)
	}
	for _, keep := range []string{"--2026-10-11 10:00:00--", "Length: 6553600", "100K files checked", "ERROR 404: Not Found."} {
		if !strings.Contains(out, keep) {
			t.Errorf("a meaningful line was dropped as progress: %q\n%s", keep, out)
		}
	}
}
