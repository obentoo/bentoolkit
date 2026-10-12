package main

// Story 094: while the live region is drawn, the run's slog records are held
// and printed once it is gone; plain output keeps writing them at once.

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// s094BufferLogger is a redacting text logger on buf, at INFO, without the
// time attribute, that scrubs secret: the shape logging.New gives stderr.
func s094BufferLogger(buf *bytes.Buffer, secret string) *slog.Logger {
	text := slog.NewTextHandler(buf, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(logging.NewRedactingHandler(text, func() []string { return []string{secret} }))
}

// s094ApplyRun is an autoupdateRun whose apply gate answers live (terminal
// stdout, pollable stdin) or plain, logging to lg. Stdout goes to a file and
// stdin is a pipe for the test's lifetime, so a live program draws nowhere
// visible.
func s094ApplyRun(t *testing.T, live bool, lg *slog.Logger) *autoupdateRun {
	t.Helper()
	for _, env := range []string{"NO_COLOR", "BENTOO_NO_TUI", "BENTOO_UI"} {
		t.Setenv(env, "")
	}
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origIn := os.Stdout, os.Stdin
	os.Stdout, os.Stdin = out, inR
	t.Cleanup(func() {
		os.Stdout, os.Stdin = origOut, origIn
		_ = inW.Close()
		_ = inR.Close()
		_ = out.Close()
	})
	d := defaultDeps()
	stubUIIsTerminal(d, live)
	d.stdinPollable = func() bool { return true }
	return &autoupdateRun{opts: testAutoupdateOptions(), deps: d, uiConfig: &config.Config{}, lg: lg}
}

func TestApplyRegionHoldsRunLogUntilFinish(t *testing.T) {
	var buf bytes.Buffer
	orig := s094BufferLogger(&buf, "s3cr3t-value")
	ar := s094ApplyRun(t, true, orig)
	if !ar.tuiEnabledForApply() {
		t.Fatal("guard: the fixture does not draw the live region")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, finish := ar.buildApplyReporter(ctx, cancel, 1)
	defer finish()

	// What the applier takes while the region is up: the run's logger, and a
	// logger derived from it as a component binds its own attributes.
	ar.log().Info("manifest failed; invoking LLM fixer to repair the ebuild", "package", "dev-go/a", "version", "1.1")
	ar.log().With("package", "dev-go/b").Warn("fixer unavailable", "err", "token s3cr3t-value rejected")
	if buf.Len() != 0 {
		t.Fatalf("a record reached the terminal under the live region:\n%s", buf.String())
	}

	finish()
	got := buf.String()
	for _, want := range []string{
		"level=INFO msg=\"manifest failed; invoking LLM fixer to repair the ebuild\" package=dev-go/a version=1.1\n",
		"level=WARN msg=\"fixer unavailable\" package=dev-go/b err=\"token *** rejected\"\n",
	} {
		if strings.Count(got, want) != 1 {
			t.Errorf("after the region: want exactly one %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "s3cr3t-value") {
		t.Errorf("a held record lost its redaction:\n%s", got)
	}
	if ar.lg != orig {
		t.Error("finish left the run on the held logger instead of the original")
	}
	ar.log().Info("summary follows")
	if !strings.HasSuffix(buf.String(), "level=INFO msg=\"summary follows\"\n") {
		t.Errorf("a record after finish was not written at once:\n%s", buf.String())
	}
}

func TestApplyPlainRunLogWritesThrough(t *testing.T) {
	var buf bytes.Buffer
	orig := s094BufferLogger(&buf, "s3cr3t-value")
	ar := s094ApplyRun(t, false, orig)
	if ar.tuiEnabledForApply() {
		t.Fatal("guard: the fixture draws the live region")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, finish := ar.buildApplyReporter(ctx, cancel, 1)
	defer finish()

	if ar.lg != orig {
		t.Fatal("plain output replaced the run's logger")
	}
	ar.log().Info("manifest failed; invoking LLM fixer to repair the ebuild", "package", "dev-go/a")
	if want := "level=INFO msg=\"manifest failed; invoking LLM fixer to repair the ebuild\" package=dev-go/a\n"; buf.String() != want {
		t.Fatalf("plain output: the record was not written at once: got %q, want %q", buf.String(), want)
	}
}

// End to end on a pseudo-terminal: every record the fixer path logs while the
// region is drawn reaches the terminal exactly once, after bubbletea shows the
// cursor again on exit, with its level and fields.
func TestLiveRegionHeldRecordsPrintBelowTheRegion(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pkgs := s094LiveFixture(t, auOpts, 3)
	code, out := s094RunApplyOnPTY(t, auOpts, 40, 120)
	if code != 1 {
		t.Fatalf("a batch of failing bumps exited %d, want 1:\n%s", code, out)
	}
	hide, show := strings.Index(out, "\x1b[?25l"), strings.LastIndex(out, "\x1b[?25h")
	if hide < 0 || show < hide {
		t.Fatalf("guard: no live region drawn (hide cursor at %d, show at %d):\n%q", hide, show, out)
	}
	if under := out[hide:show]; strings.Contains(under, "level=") {
		t.Errorf("a slog record was written under the live region:\n%q", under)
	}
	below := out[show:]
	for _, pkg := range pkgs {
		rec := "level=INFO msg=\"manifest failed; invoking LLM fixer to repair the ebuild\" package=" + pkg + " "
		if n := strings.Count(below, rec); n != 1 {
			t.Errorf("%d fixer records for %s below the region, want 1:\n%s", n, pkg, below)
		}
	}
}
