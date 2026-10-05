package overlay

// Authored for story 062, sub-task 3.3 (R5.2, R5.3, R5.5).
//
// Contract, from the sub-task objective: `newReviewCache(dir string, log
// *slog.Logger)`; the review cache warns through the logger it was handed,
// never through a package-level seam, and a cache handed no logger discards.
//
// The probe is the cache's own documented warning: a cache file that is present
// but cannot be parsed, or cannot be read, costs one warning and an empty cache.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

type s062CacheRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *s062CacheRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *s062CacheRecorder) records(t *testing.T) []map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(r.buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not a JSON record: %v\n%s", err, line)
		}
		out = append(out, m)
	}
	return out
}

// s062CacheCaptureFD2 runs fn with fd 2 pointed at a pipe (the os.Stderr
// variable is deliberately left alone, so every writer lands on the fd).
func s062CacheCaptureFD2(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved, err := syscall.Dup(2)
	if err != nil {
		t.Fatalf("dup fd 2: %v", err)
	}
	if err := syscall.Dup2(int(w.Fd()), 2); err != nil {
		t.Fatalf("redirect fd 2: %v", err)
	}
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	func() {
		defer func() {
			if err := syscall.Dup2(saved, 2); err != nil {
				t.Errorf("restore fd 2: %v", err)
			}
			_ = syscall.Close(saved)
			_ = w.Close()
		}()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}

// s062BrokenCacheDirs returns two cache dirs whose file cannot be used: one
// holds unparsable JSON, the other a directory where the file should be.
func s062BrokenCacheDirs(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	corrupt := filepath.Join(root, "corrupt")
	if err := os.MkdirAll(corrupt, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, reviewCacheFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(root, "unreadable")
	if err := os.MkdirAll(filepath.Join(unreadable, reviewCacheFileName), 0o750); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"unparsable file": corrupt, "unreadable file": unreadable}
}

// TestReviewCacheWarnsThroughTheInjectedLogger: the warning reaches the logger
// the cache was built with, names the file in an attribute rather than in the
// message (R5.5), and does not also reach stderr.
func TestReviewCacheWarnsThroughTheInjectedLogger(t *testing.T) {
	for name, dir := range s062BrokenCacheDirs(t) {
		t.Run(name, func(t *testing.T) {
			rec := &s062CacheRecorder{}
			log := slog.New(slog.NewJSONHandler(rec, &slog.HandlerOptions{Level: slog.LevelDebug}))
			file := filepath.Join(dir, reviewCacheFileName)

			var cache *reviewCache
			stderr := s062CacheCaptureFD2(t, func() { cache = newReviewCache(dir, log) })
			if cache == nil {
				t.Fatal("newReviewCache returned nil")
			}

			var warns int
			for _, r := range rec.records(t) {
				if msg := fmt.Sprint(r["msg"]); strings.Contains(msg, file) {
					t.Errorf("the message %q carries the path; it belongs in an attribute (R5.5)", msg)
				}
				if r["level"] != "WARN" {
					continue
				}
				for k, v := range r {
					if k != "msg" && strings.Contains(fmt.Sprint(v), file) {
						warns++
						break
					}
				}
			}
			if warns != 1 {
				t.Errorf("the injected logger got %d WARN records naming %s, want 1\n%s", warns, file, rec.buf.String())
			}
			if strings.TrimSpace(stderr) != "" {
				t.Errorf("with a logger injected, the cache still wrote to stderr:\n%s", stderr)
			}
		})
	}
}

// TestReviewCacheWithoutALoggerStillLoadsEmpty is R5.3: with no logger the same
// broken file still yields a usable, empty cache, and nothing reaches stderr.
func TestReviewCacheWithoutALoggerStillLoadsEmpty(t *testing.T) {
	for name, dir := range s062BrokenCacheDirs(t) {
		t.Run(name, func(t *testing.T) {
			var cache *reviewCache
			stderr := s062CacheCaptureFD2(t, func() { cache = newReviewCache(dir, nil) })
			if cache == nil {
				t.Fatal("newReviewCache(dir, nil) returned nil")
			}
			if _, ok := cache.get(ReviewRequest{Ours: []byte("ours"), Theirs: []byte("theirs")}); ok {
				t.Error("a cache loaded from a broken file answered a lookup; it must start empty")
			}
			if strings.TrimSpace(stderr) != "" {
				t.Errorf("a cache built without a logger wrote to stderr (R5.3: it must discard):\n%s", stderr)
			}
		})
	}
}
