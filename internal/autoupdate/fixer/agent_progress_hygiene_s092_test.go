package fixer

// Authored for story 092 (autoupdate-apply-observable), sub-task 7.2, test
// first (R4.8): when an agent run dropped progress lines, the run logs ONE
// WARN record naming the package, the stage and the number dropped. A run
// that dropped nothing logs no such record.
//
// Surface: runAgentStream and agentStreamSpec, unchanged. The R4.9 half of the
// sub-task lives in internal/autoupdate/agent_progress_hygiene_s092_test.go,
// because only the applier's wiring can tell "after the run returned".

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type s092v2Record struct {
	level slog.Level
	msg   string
	attrs map[string]slog.Value
}

type s092v2Records struct {
	mu   sync.Mutex
	recs []s092v2Record
}

// s092v2Handler records every record, attrs added through With included.
type s092v2Handler struct {
	store *s092v2Records
	pre   []slog.Attr
}

func (h *s092v2Handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *s092v2Handler) Handle(_ context.Context, r slog.Record) error {
	rec := s092v2Record{level: r.Level, msg: r.Message, attrs: map[string]slog.Value{}}
	for _, a := range h.pre {
		rec.attrs[a.Key] = a.Value.Resolve()
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.Resolve()
		return true
	})
	h.store.mu.Lock()
	h.store.recs = append(h.store.recs, rec)
	h.store.mu.Unlock()
	return nil
}

func (h *s092v2Handler) WithAttrs(as []slog.Attr) slog.Handler {
	return &s092v2Handler{store: h.store, pre: append(append([]slog.Attr{}, h.pre...), as...)}
}

func (h *s092v2Handler) WithGroup(string) slog.Handler { return h }

func (s *s092v2Records) at(level slog.Level) []s092v2Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []s092v2Record
	for _, r := range s.recs {
		if r.level == level {
			out = append(out, r)
		}
	}
	return out
}

// s092v2Calls is a stream of n tool calls and a successful result.
func s092v2Calls(n int) string {
	const call = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t%d","name":"Read","input":{"file_path":"/stage/f%d.ebuild"}}]}}` + "\n"
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, call, i, i)
	}
	b.WriteString(s092rResult + "\n")
	return b.String()
}

// s092v2Int reads an attr as a whole number, whatever integer kind it is.
func s092v2Int(v slog.Value) (int64, bool) {
	switch v.Kind() {
	case slog.KindInt64:
		return v.Int64(), true
	case slog.KindUint64:
		return int64(v.Uint64()), true
	case slog.KindString:
		n, err := strconv.ParseInt(v.String(), 10, 64)
		return n, err == nil
	}
	return 0, false
}

func TestAgentProgressHygieneDroppedLinesWarnOnce(t *testing.T) {
	const pkg, stage = "dev-games/godot", StageBuildFix

	// The rule must not fire when nothing was dropped: a callback that keeps
	// up with a short run leaves no WARN at all.
	t.Run("no drop, no WARN", func(t *testing.T) {
		store := &s092v2Records{}
		var delivered atomic.Int64
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		run := runAgentStream(s092rCmd(ctx, t, s092v2Calls(5), ""), agentStreamSpec{
			stage: stage, pkg: pkg,
			progress: func(string) { delivered.Add(1) },
			log:      slog.New(&s092v2Handler{store: store}),
		})
		if run.runErr != nil {
			t.Fatalf("fixture: the fake agent failed: %v", run.runErr)
		}
		if n := delivered.Load(); n != 5 {
			t.Fatalf("fixture: %d of 5 lines delivered to a callback that keeps up", n)
		}
		if w := store.at(slog.LevelWarn); len(w) != 0 {
			t.Errorf("a run that dropped nothing logged %d WARN record(s): %+v", len(w), w)
		}
	})

	// A burst that overflows the queue: the callback blocks until the fake
	// agent has printed every line and waited a second more (the parser
	// buffers the stream, so printing ends before the offers do), so most of
	// the burst is refused; it then catches up well inside the grace, so
	// nothing is abandoned and every line offered was delivered or dropped.
	t.Run("overflow, one WARN with the count", func(t *testing.T) {
		const total = 2000
		store := &s092v2Records{}
		marker := filepath.Join(t.TempDir(), "printed")
		var delivered atomic.Int64
		var waited sync.Once
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		run := runAgentStream(s092rCmd(ctx, t, s092v2Calls(total), `; sleep 1; : > '`+marker+`'`), agentStreamSpec{
			stage: stage, pkg: pkg,
			progress: func(string) {
				waited.Do(func() {
					deadline := time.Now().Add(20 * time.Second)
					for time.Now().Before(deadline) {
						if _, err := os.Stat(marker); err == nil {
							return
						}
						time.Sleep(2 * time.Millisecond)
					}
				})
				delivered.Add(1)
			},
			log: slog.New(&s092v2Handler{store: store}),
		})
		if run.runErr != nil {
			t.Fatalf("fixture: the fake agent failed: %v", run.runErr)
		}
		got := delivered.Load()
		wantDropped := total - got
		if wantDropped < int64(progressQueue) {
			t.Fatalf("fixture: only %d of %d lines were dropped; the burst did not overflow the %d-line queue", wantDropped, total, progressQueue)
		}

		warns := store.at(slog.LevelWarn)
		if len(warns) != 1 {
			t.Fatalf("a run that dropped %d progress lines logged %d WARN record(s), want exactly 1: %+v",
				wantDropped, len(warns), warns)
		}
		w := warns[0]
		if v, ok := w.attrs["package"]; !ok || v.String() != pkg {
			t.Errorf("the WARN does not name the package %q: %+v", pkg, w)
		}
		if v, ok := w.attrs["stage"]; !ok || v.String() != stage {
			t.Errorf("the WARN does not name the stage %q: %+v", stage, w)
		}
		counted := false
		for _, v := range w.attrs {
			if n, ok := s092v2Int(v); ok && n == wantDropped {
				counted = true
			}
		}
		if !counted {
			t.Errorf("the WARN does not carry the number dropped (%d of %d offered, %d delivered): %+v",
				wantDropped, total, got, w)
		}
	})
}
