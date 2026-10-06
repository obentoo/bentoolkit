package autoupdate

// Authored for story 062, sub-task 3.1 (R5.2, R5.5).
//
// Contract, from the sub-task objective: `WithLogger(*slog.Logger) CheckerOption`
// and `(*RetryableHTTPClient).SetLogger`; the package's diagnostics go through
// the component's logger, with constant messages and key-value attributes.
//
// The probes are two warnings each component already emits:
//   - NewChecker warns once per package whose llm_prompt is set when no LLM is
//     wired into the check path;
//   - the HTTP client warns when a header's ${VAR} expansion is denied.
//
// Both tests also watch fd 2: an injected logger REPLACES the old sink, so the
// warning must not reach stderr as well. The capture helper and the isolation
// helper live in logger_discard_test.go, which is materialized with this file.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// s062Recorder is a debug-level JSON logger whose records the test can read.
type s062Recorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *s062Recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *s062Recorder) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(r, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (r *s062Recorder) records(t *testing.T) []map[string]any {
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

// s062AttrText renders every attribute (not time, level or msg) as text.
func s062AttrText(rec map[string]any) string {
	var b strings.Builder
	for k, v := range rec {
		if k == "time" || k == "level" || k == "msg" {
			continue
		}
		fmt.Fprintf(&b, "%s=%v ", k, v)
	}
	return b.String()
}

// s062WarnsNaming returns the WARN records whose attributes mention sub, and
// fails the test for any record that carries sub in its message instead (R5.5:
// variable data moves into attributes).
func s062WarnsNaming(t *testing.T, recs []map[string]any, sub string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range recs {
		msg := fmt.Sprint(r["msg"])
		if strings.Contains(msg, sub) {
			t.Errorf("the message %q carries the variable data %q; it belongs in an attribute (R5.5)", msg, sub)
		}
		if r["level"] == "WARN" && strings.Contains(s062AttrText(r), sub) {
			out = append(out, r)
		}
	}
	return out
}

// TestCheckerLogsToTheInjectedLogger: the llm_prompt warning reaches the logger
// passed with WithLogger — one record per affected package, naming it in an
// attribute — and nothing reaches stderr.
func TestCheckerLogsToTheInjectedLogger(t *testing.T) {
	root := s062IsolateAutoupdate(t)
	cfg := &PackagesConfig{Packages: map[string]PackageConfig{
		"cat-a/s062-one": {URL: "https://example.invalid/a", Parser: "json", Path: "v", LLMPrompt: "extract version"},
		"cat-b/s062-two": {URL: "https://example.invalid/b", Parser: "json", Path: "v"},
	}}

	rec := &s062Recorder{}
	stderr := s062CaptureStderr(t, func() {
		if _, err := NewChecker(filepath.Join(root, "overlay"),
			WithConfigDir(filepath.Join(root, "config")),
			WithPackagesConfig(cfg),
			WithLogger(rec.logger()),
		); err != nil {
			t.Fatalf("NewChecker: %v", err)
		}
	})

	recs := rec.records(t)
	if got := s062WarnsNaming(t, recs, "cat-a/s062-one"); len(got) != 1 {
		t.Errorf("the injected logger got %d WARN records naming the llm_prompt package, want 1\nrecords: %v", len(got), recs)
	}
	if got := s062WarnsNaming(t, recs, "cat-b/s062-two"); len(got) != 0 {
		t.Errorf("a package without llm_prompt was warned about: %v", got)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("with a logger injected, the checker still wrote to stderr:\n%s", stderr)
	}
}
