package llm

// Story 059, sub-task 1.2 (R1.4, R3.3): one ClaudeCodeClient serves many calls,
// and each call spawns its `claude` child from a context derived from THAT
// call's own context. The client stores no context of its own.
//
// The seam records a label carried in the context it is handed, so a client
// that took the ctx parameter but spawned from context.Background() (or from a
// context remembered from an earlier call) is caught by the label, not only by
// a timing.

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

type claudeCodeCtx059Key struct{}

func claudeCodeCtx059Labelled(label string) context.Context {
	return context.WithValue(context.Background(), claudeCodeCtx059Key{}, label)
}

// claudeCodeCtx059Seam records, per spawn, the label and the context itself.
type claudeCodeCtx059Seam struct {
	mu     sync.Mutex
	labels []any
	ctxs   []context.Context
	script string
}

func (s *claudeCodeCtx059Seam) exec(ctx context.Context, name string, arg ...string) *exec.Cmd {
	s.mu.Lock()
	s.labels = append(s.labels, ctx.Value(claudeCodeCtx059Key{}))
	s.ctxs = append(s.ctxs, ctx)
	s.mu.Unlock()
	return exec.CommandContext(ctx, "sh", "-c", s.script)
}

func (s *claudeCodeCtx059Seam) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labels, s.ctxs = nil, nil
}

func (s *claudeCodeCtx059Seam) snapshot() ([]any, []context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]any(nil), s.labels...), append([]context.Context(nil), s.ctxs...)
}

const claudeCodeCtx059OK = `printf '%s' '{"type":"result","is_error":false,"result":"1.2.3"}'`

func newClaudeCodeCtx059Client(t *testing.T, seam *claudeCodeCtx059Seam, opts ...ClaudeCodeOption) *ClaudeCodeClient {
	t.Helper()
	stubLookPathFound(t)
	all := append([]ClaudeCodeOption{WithClaudeCodeExecCommand(seam.exec)}, opts...)
	c, err := NewClaudeCodeClient(LLMConfig{Provider: "claude-code", Bare: "false"}, all...)
	if err != nil {
		t.Fatalf("NewClaudeCodeClient: %v", err)
	}
	return c
}

// TestClaudeCodeEachCallSpawnsFromItsOwnContext pins R3.3: three calls on ONE
// client, each with its own labelled context, and every child each call spawns
// carries that call's label — never a neighbour's, never none. The spawn
// context is also DERIVED from the call's context: cancelling the caller's
// context afterwards is visible on the context the child was given.
func TestClaudeCodeEachCallSpawnsFromItsOwnContext(t *testing.T) {
	seam := &claudeCodeCtx059Seam{script: claudeCodeCtx059OK}
	c := newClaudeCodeCtx059Client(t, seam)

	calls := []struct {
		label string
		run   func(ctx context.Context) error
	}{
		{"ask-json", func(ctx context.Context) error {
			_, err := c.AskJSON(ctx, "answer as JSON", []byte("content"), `{"type":"object"}`)
			return err
		}},
		{"extract-version", func(ctx context.Context) error {
			v, err := c.ExtractVersion(ctx, []byte("content"), "")
			if err == nil && v != "1.2.3" {
				t.Errorf("ExtractVersion = %q, want 1.2.3", v)
			}
			return err
		}},
		// AnalyzeContent may retry unstructured after a structured attempt;
		// every spawn it makes must carry its label, and its result does not
		// matter here.
		{"analyze-content", func(ctx context.Context) error {
			_, _ = c.AnalyzeContent(ctx, []byte("content"), &ebuilds.EbuildMetadata{}, "")
			return nil
		}},
	}

	for _, call := range calls {
		seam.reset()
		ctx, cancel := context.WithCancel(claudeCodeCtx059Labelled(call.label))
		if err := call.run(ctx); err != nil {
			t.Errorf("%s: unexpected error: %v", call.label, err)
		}
		labels, ctxs := seam.snapshot()
		if len(labels) == 0 {
			t.Errorf("%s spawned no child", call.label)
		}
		for i, got := range labels {
			if got != call.label {
				t.Errorf("%s: spawn %d carried context label %v; want %q (the child must be spawned from the call's own context, R3.3)", call.label, i, got, call.label)
			}
		}
		cancel()
		for i, spawned := range ctxs {
			if spawned.Err() == nil {
				t.Errorf("%s: spawn %d's context is still live after the caller's context was cancelled; it is not derived from the call's context (R3.3)", call.label, i)
			}
		}
	}
}

// TestClaudeCodeCancelledCallDoesNotLeakIntoTheNext pins R3.3's two converse
// failure directions on one client, plus the classification that depends on
// WHOSE context ended a run.
func TestClaudeCodeCancelledCallDoesNotLeakIntoTheNext(t *testing.T) {
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	t.Run("a cancelled first call does not fail a later live call", func(t *testing.T) {
		seam := &claudeCodeCtx059Seam{script: claudeCodeCtx059OK}
		c := newClaudeCodeCtx059Client(t, seam)
		if _, err := c.ExtractVersion(cancelled(), []byte("content"), ""); err == nil {
			t.Fatal("ExtractVersion with a cancelled context returned nil error")
		}
		v, err := c.ExtractVersion(context.Background(), []byte("content"), "")
		if err != nil || v != "1.2.3" {
			t.Fatalf("live call after a cancelled one = (%q, %v); want (1.2.3, nil)", v, err)
		}
	})

	t.Run("a live first call does not let a later cancelled call succeed", func(t *testing.T) {
		seam := &claudeCodeCtx059Seam{script: claudeCodeCtx059OK}
		c := newClaudeCodeCtx059Client(t, seam)
		if _, err := c.AskJSON(context.Background(), "q", []byte("content"), `{"type":"object"}`); err != nil {
			t.Fatalf("live AskJSON: %v", err)
		}
		if got, err := c.AskJSON(cancelled(), "q", []byte("content"), `{"type":"object"}`); err == nil {
			t.Fatalf("AskJSON with a cancelled context succeeded (%q); the call is not bounded by its own context", got)
		}
	})

	t.Run("a call ended by its own parent deadline is stopped, not timed out", func(t *testing.T) {
		seam := &claudeCodeCtx059Seam{script: "sleep 3600"}
		c := newClaudeCodeCtx059Client(t, seam, WithClaudeCodeTimeout(10*time.Second))
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := c.AskJSON(ctx, "q", []byte("content"), `{"type":"object"}`)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("AskJSON returned %v after a 200ms parent deadline", elapsed)
		}
		if !errors.Is(err, ErrClaudeStopped) {
			t.Errorf("err = %v; want ErrClaudeStopped: the CALLER's context ended this run", err)
		}
		if errors.Is(err, ErrClaudeTimedOut) {
			t.Errorf("err = %v reports the client's own budget elapsing, but the 10s budget never ran out", err)
		}
	})

	t.Run("the client's own budget elapsing under a live caller is a timeout", func(t *testing.T) {
		seam := &claudeCodeCtx059Seam{script: "sleep 3600"}
		c := newClaudeCodeCtx059Client(t, seam, WithClaudeCodeTimeout(150*time.Millisecond))
		_, err := c.AskJSON(context.Background(), "q", []byte("content"), `{"type":"object"}`)
		if !errors.Is(err, ErrClaudeTimedOut) {
			t.Errorf("err = %v; want ErrClaudeTimedOut", err)
		}
		if errors.Is(err, ErrClaudeStopped) {
			t.Errorf("err = %v blames the caller, whose context never ended", err)
		}
	})
}
