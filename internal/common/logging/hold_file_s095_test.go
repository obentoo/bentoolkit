package logging

// Story 095: a held invocation logger keeps only its terminal copy back; the
// log file gets every record at once, and the terminal queue is bounded.

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// s095Logger is the invocation logger New builds, its terminal sink on stderr
// and its file under a temporary log directory, scrubbing secret. It returns
// the log file's path.
func s095Logger(t *testing.T, stderr *bytes.Buffer, secret string) (*slog.Logger, string) {
	t.Helper()
	dir := t.TempDir()
	l, closeFn, err := New(Options{
		Stderr:   stderr,
		LogDir:   dir,
		Level:    slog.LevelInfo,
		Resolved: func() []string { return []string{secret} },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = closeFn() })
	return l, filepath.Join(dir, FileName)
}

func s095ReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log file: %v", err)
	}
	return string(b)
}

func TestHoldWritesTheLogFileAtOnceAndTheTerminalAtRelease(t *testing.T) {
	var stderr bytes.Buffer
	l, path := s095Logger(t, &stderr, "s3cr3t-value")
	held, release := Hold(l)

	held.Info("manifest fix: distdir prepared", "package", "dev-go/a", "url", "https://x/?k=s3cr3t-value")
	held.With("package", "dev-go/b").WithGroup("fix").Warn("fixer failed", "api_token", "plain-token", "attempt", 2)

	file := s095ReadFile(t, path)
	for _, want := range []string{
		`"msg":"manifest fix: distdir prepared","package":"dev-go/a","url":"https://x/?k=***"`,
		`"msg":"fixer failed","package":"dev-go/b","fix":{"api_token":"***","attempt":2}`,
	} {
		if strings.Count(file, want) != 1 {
			t.Errorf("while held: want exactly one %s in the log file:\n%s", want, file)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("a held logger wrote to the terminal before its release:\n%s", stderr.String())
	}

	release()
	want := "level=INFO msg=\"manifest fix: distdir prepared\" package=dev-go/a url=\"https://x/?k=***\"\n" +
		"level=WARN msg=\"fixer failed\" package=dev-go/b fix.api_token=*** fix.attempt=2\n"
	if got := stderr.String(); got != want {
		t.Fatalf("terminal after the release:\n%s\nwant:\n%s", got, want)
	}
	// The release replays the terminal copy only: the file is not written twice.
	if got := s095ReadFile(t, path); got != file {
		t.Errorf("the release rewrote the log file:\nbefore:\n%s\nafter:\n%s", file, got)
	}
	for _, leak := range []string{"s3cr3t-value", "plain-token"} {
		if strings.Contains(stderr.String()+s095ReadFile(t, path), leak) {
			t.Errorf("%q reached a sink unredacted", leak)
		}
	}

	held.Error("after the release", "package", "dev-go/c")
	if !strings.HasSuffix(stderr.String(), "level=ERROR msg=\"after the release\" package=dev-go/c\n") {
		t.Errorf("a record after the release did not reach the terminal at once:\n%s", stderr.String())
	}
	if !strings.Contains(s095ReadFile(t, path), `"msg":"after the release"`) {
		t.Error("a record after the release did not reach the log file")
	}
}

func TestHoldBoundKeepsTheNewestRecordsAndReplaysTheDropLineFirst(t *testing.T) {
	// A small overflow is trimmed at the release; one past twice the bound is
	// trimmed while records are still being kept.
	for _, extra := range []int{3, maxHeldRecords + 5} {
		t.Run(fmt.Sprintf("extra=%d", extra), func(t *testing.T) {
			var stderr bytes.Buffer
			l, path := s095Logger(t, &stderr, "s3cr3t-value")
			held, release := Hold(l)

			total := maxHeldRecords + extra
			for i := range total {
				held.Info("tick", "i", i)
			}
			if got := strings.Count(s095ReadFile(t, path), `"msg":"tick"`); got != total {
				t.Errorf("log file holds %d records, want every one of the %d", got, total)
			}
			release()

			lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
			if got, want := len(lines), maxHeldRecords+1; got != want {
				t.Fatalf("terminal: %d lines after the release, want %d (the drop line and the newest %d)", got, want, maxHeldRecords)
			}
			if want := fmt.Sprintf("level=WARN msg=\"… %d earlier record(s) dropped\"", extra); lines[0] != want {
				t.Errorf("first replayed line = %q, want %q", lines[0], want)
			}
			if want := fmt.Sprintf("level=INFO msg=tick i=%d", extra); lines[1] != want {
				t.Errorf("oldest kept record = %q, want %q", lines[1], want)
			}
			if want := fmt.Sprintf("level=INFO msg=tick i=%d", total-1); lines[len(lines)-1] != want {
				t.Errorf("newest kept record = %q, want %q", lines[len(lines)-1], want)
			}
		})
	}
}

// A queue that never reached the bound replays no drop line.
func TestHoldUnderTheBoundReplaysNoDropLine(t *testing.T) {
	var buf bytes.Buffer
	held, release := Hold(s094TextLogger(&buf, "s3cr3t-value"))
	for i := range maxHeldRecords {
		held.Info("tick", "i", i)
	}
	release()
	if strings.Contains(buf.String(), "dropped") {
		t.Errorf("a queue at the bound replayed a drop line:\n%.200s", buf.String())
	}
	if got := strings.Count(buf.String(), "msg=tick"); got != maxHeldRecords {
		t.Errorf("%d records replayed, want %d", got, maxHeldRecords)
	}
}
