package logging

// Story 094: a held logger keeps every record off its sink until release, then
// writes them as the unheld logger would, redaction included.

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// s094TextLogger is a redacting text logger on buf at INFO that scrubs secret.
func s094TextLogger(buf *bytes.Buffer, secret string) *slog.Logger {
	text := slog.NewTextHandler(buf, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(NewRedactingHandler(text, func() []string { return []string{secret} }))
}

func TestHoldKeepsRecordsUntilRelease(t *testing.T) {
	var buf bytes.Buffer
	held, release := Hold(s094TextLogger(&buf, "s3cr3t-value"))

	held.Info("manifest fix: distdir prepared", "package", "dev-go/a", "url", "https://x/?k=s3cr3t-value")
	held.With("package", "dev-go/b").WithGroup("fix").Warn("fixer failed", "api_token", "plain", "attempt", 2)
	held.Debug("below the level")
	if buf.Len() != 0 {
		t.Fatalf("a held logger wrote before its release:\n%s", buf.String())
	}

	release()
	want := "level=INFO msg=\"manifest fix: distdir prepared\" package=dev-go/a url=\"https://x/?k=***\"\n" +
		"level=WARN msg=\"fixer failed\" package=dev-go/b fix.api_token=*** fix.attempt=2\n"
	if got := buf.String(); got != want {
		t.Fatalf("records after the release:\n%s\nwant (in order, level and fields kept, secrets redacted):\n%s", got, want)
	}

	// Released: a later record writes through at once, and a second release
	// replays nothing.
	held.Error("after the release", "package", "dev-go/c")
	release()
	if got, want := strings.Count(buf.String(), "\n"), 3; got != want {
		t.Fatalf("after the release: %d lines, want %d:\n%s", got, want, buf.String())
	}
	if !strings.HasSuffix(buf.String(), "level=ERROR msg=\"after the release\" package=dev-go/c\n") {
		t.Fatalf("a record logged after the release was not written through:\n%s", buf.String())
	}
}

func TestHoldNilLoggerDiscards(t *testing.T) {
	held, release := Hold(nil)
	held.Info("nowhere")
	release()
	held.Info("still nowhere")
}

// Records logged from many goroutines while the release runs are each written
// exactly once, and nothing kept before the release comes after a record that
// went straight through.
func TestHoldConcurrentLogAndRelease(t *testing.T) {
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	l := slog.New(slog.NewTextHandler(&s094LockedWriter{mu: &mu, w: &buf}, nil))
	held, release := Hold(l)
	const workers, each = 8, 50
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				held.Info("tick", "worker", w, "i", i)
			}
		}()
	}
	release()
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Count(buf.String(), "msg=tick"); got != workers*each {
		t.Fatalf("%d records written, want %d", got, workers*each)
	}
	for w := range workers {
		last := -1
		for i := range each {
			at := strings.Index(buf.String(), fmt.Sprintf("worker=%d i=%d\n", w, i))
			if at < last {
				t.Fatalf("worker %d: record %d written before record %d", w, i, i-1)
			}
			last = at
		}
	}
}

type s094LockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *s094LockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
