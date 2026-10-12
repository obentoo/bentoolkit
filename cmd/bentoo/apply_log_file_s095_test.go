package main

// Story 095: under the live region the run's records reach the log file at
// once; only the terminal copy waits for finish.

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/logging"
)

func TestHoldApplyRegionWritesTheLogFileBeforeFinish(t *testing.T) {
	var stderr bytes.Buffer
	dir := t.TempDir()
	lg, closeLog, err := logging.New(logging.Options{
		Stderr:   &stderr,
		LogDir:   dir,
		Level:    slog.LevelInfo,
		Resolved: func() []string { return []string{"s3cr3t-value"} },
	})
	if err != nil {
		t.Fatalf("logging.New: %v", err)
	}
	t.Cleanup(func() { _ = closeLog() })
	path := filepath.Join(dir, logging.FileName)

	ar := s094ApplyRun(t, true, lg)
	if !ar.tuiEnabledForApply() {
		t.Fatal("guard: the fixture does not draw the live region")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, finish := ar.buildApplyReporter(ctx, cancel, 1)
	defer finish()

	ar.log().With("package", "dev-go/a").Info("manifest failed; invoking LLM fixer to repair the ebuild", "err", "token s3cr3t-value rejected")
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log file: %v", err)
	}
	if want := `"msg":"manifest failed; invoking LLM fixer to repair the ebuild","package":"dev-go/a","err":"token *** rejected"`; !strings.Contains(string(file), want) {
		t.Errorf("under the live region the record did not reach the log file:\nwant %s in:\n%s", want, file)
	}
	if stderr.Len() != 0 {
		t.Fatalf("a record reached the terminal under the live region:\n%s", stderr.String())
	}

	finish()
	want := "level=INFO msg=\"manifest failed; invoking LLM fixer to repair the ebuild\" package=dev-go/a err=\"token *** rejected\"\n"
	if got := stderr.String(); strings.Count(got, want) != 1 {
		t.Errorf("after finish: want exactly one %q on the terminal, got:\n%s", want, got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log file: %v", err)
	}
	if n := strings.Count(string(after), `"msg":"manifest failed; invoking LLM fixer to repair the ebuild"`); n != 1 {
		t.Errorf("the log file holds the record %d times after finish, want 1:\n%s", n, after)
	}
	if strings.Contains(stderr.String()+string(after), "s3cr3t-value") {
		t.Error("the secret reached a sink unredacted")
	}
}
