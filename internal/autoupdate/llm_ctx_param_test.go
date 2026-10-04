package autoupdate

// Story 059, sub-task 1.1 (R1.4, R3.5): an HTTP LLM provider sends its request
// with the context the CALLER passes, so cancelling that context aborts the
// request in flight and the error says so (errors.Is(err, context.Canceled)).
//
// Every case below would still fail if the new ctx parameter were accepted and
// then ignored: the server never answers on its own, so a request built without
// the caller's context runs until the client's own 30 s / 120 s timeout, far past
// the 2 s this test allows.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// llmCtx059KeyEnv names the API key variable the keyed providers read.
const llmCtx059KeyEnv = "BENTOO_CTX059_LLM_KEY"

// llmCtx059Server answers every request with HTTP 500 unless block is set, in
// which case it holds the request until the request's own context ends or the
// test finishes. It counts the requests it saw.
type llmCtx059Server struct {
	srv     *httptest.Server
	block   atomic.Bool
	hits    atomic.Int64
	arrived chan struct{}
}

func newLLMCtx059Server(t *testing.T) *llmCtx059Server {
	t.Helper()
	s := &llmCtx059Server{arrived: make(chan struct{}, 16)}
	release := make(chan struct{})
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if !s.block.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		select {
		case s.arrived <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(s.srv.Close)
	t.Cleanup(func() { close(release) }) // runs first (LIFO), so Close does not wait on a held handler
	return s
}

// isolateLLMCtx059Env keeps the secrets chain off the developer's real files and
// provides the one key the keyed providers need.
func isolateLLMCtx059Env(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(llmCtx059KeyEnv, "ctx059-test-key")
}

// llmCtx059Provider builds one provider pointed at baseURL.
type llmCtx059Provider struct {
	name  string
	build func(t *testing.T, baseURL string) LLMProvider
}

func llmCtx059Providers() []llmCtx059Provider {
	return []llmCtx059Provider{
		{name: "claude", build: func(t *testing.T, baseURL string) LLMProvider {
			t.Helper()
			t.Setenv("CLAUDE_API_ENDPOINT", baseURL)
			c, err := NewClaudeClient(LLMConfig{Provider: "claude", APIKeyEnv: llmCtx059KeyEnv})
			if err != nil {
				t.Fatalf("NewClaudeClient: %v", err)
			}
			return c
		}},
		{name: "openai", build: func(t *testing.T, baseURL string) LLMProvider {
			t.Helper()
			c, err := NewOpenAIClient(LLMConfig{Provider: "openai", APIKeyEnv: llmCtx059KeyEnv, BaseURL: baseURL})
			if err != nil {
				t.Fatalf("NewOpenAIClient: %v", err)
			}
			return c
		}},
		{name: "ollama", build: func(t *testing.T, baseURL string) LLMProvider {
			t.Helper()
			c, err := NewOllamaClient(LLMConfig{Provider: "ollama", BaseURL: baseURL})
			if err != nil {
				t.Fatalf("NewOllamaClient: %v", err)
			}
			return c
		}},
	}
}

// llmCtx059Call is one of the two LLMProvider methods under test.
type llmCtx059Call func(ctx context.Context, p LLMProvider) error

func llmCtx059ExtractVersion(ctx context.Context, p LLMProvider) error {
	_, err := p.ExtractVersion(ctx, []byte(`{"version":"1.2.3"}`), "")
	return err
}

func llmCtx059AnalyzeContent(ctx context.Context, p LLMProvider) error {
	_, err := p.AnalyzeContent(ctx, []byte(`{"version":"1.2.3"}`), &EbuildMetadata{}, "")
	return err
}

// assertLLMCtx059Contract runs the three halves of R3.5 against one client:
//
//  1. HOSTILE — a context cancelled while the request is in flight ends the call
//     within 2 s with an error that is context.Canceled (a provider that ignores
//     the ctx blocks until its own client timeout);
//  2. HOSTILE — a context that is already cancelled sends nothing at all and
//     still reports context.Canceled;
//  3. CONVERSE — a cancelled call does not poison the client: the next call on
//     the SAME client with a live context reaches the server and fails for the
//     server's reason (HTTP 500), not for a cancellation.
func assertLLMCtx059Contract(t *testing.T, s *llmCtx059Server, client LLMProvider, call llmCtx059Call) {
	t.Helper()

	// 1. In-flight cancel.
	s.block.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- call(ctx, client) }()
	select {
	case <-s.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the server")
	}
	cancelAt := time.Now()
	cancel()
	select {
	case err := <-done:
		if waited := time.Since(cancelAt); waited > 2*time.Second {
			t.Errorf("returned %v after cancel; want <= 2s (R3.5)", waited)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("in-flight cancel: err = %v; want errors.Is(err, context.Canceled) (R3.5)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("still running 2s after the caller's context was cancelled: the request was not sent with that context (R3.5)")
	}

	// 2. Already-cancelled context: nothing is sent.
	s.block.Store(false)
	before := s.hits.Load()
	dead, deadCancel := context.WithCancel(context.Background())
	deadCancel()
	err := call(dead, client)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("pre-cancelled call: err = %v; want errors.Is(err, context.Canceled)", err)
	}
	if got := s.hits.Load() - before; got != 0 {
		t.Errorf("pre-cancelled call reached the server %d time(s); want 0", got)
	}

	// 3. Converse: a live context on the same client is not affected.
	before = s.hits.Load()
	err = call(context.Background(), client)
	if err == nil {
		t.Fatal("live call against an HTTP 500 server returned nil error")
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("live call after a cancelled one failed as cancelled (%v); a cancellation leaked into a later call", err)
	}
	if !errors.Is(err, ErrLLMRequestFailed) {
		t.Errorf("live call: err = %v; want the server's own failure (ErrLLMRequestFailed)", err)
	}
	if got := s.hits.Load() - before; got != 1 {
		t.Errorf("live call reached the server %d time(s); want 1", got)
	}
}

// TestLLMProvidersExtractVersionStopsOnCancel pins R3.5 for ExtractVersion on
// the Claude API, OpenAI and Ollama providers.
func TestLLMProvidersExtractVersionStopsOnCancel(t *testing.T) {
	for _, p := range llmCtx059Providers() {
		t.Run(p.name, func(t *testing.T) {
			isolateLLMCtx059Env(t)
			s := newLLMCtx059Server(t)
			assertLLMCtx059Contract(t, s, p.build(t, s.srv.URL), llmCtx059ExtractVersion)
		})
	}
}

// TestLLMProvidersAnalyzeContentStopsOnCancel pins R3.5 for AnalyzeContent on
// the Claude API, OpenAI and Ollama providers.
func TestLLMProvidersAnalyzeContentStopsOnCancel(t *testing.T) {
	for _, p := range llmCtx059Providers() {
		t.Run(p.name, func(t *testing.T) {
			isolateLLMCtx059Env(t)
			s := newLLMCtx059Server(t)
			assertLLMCtx059Contract(t, s, p.build(t, s.srv.URL), llmCtx059AnalyzeContent)
		})
	}
}

// TestLLMClientWrapperForwardsTheContext pins R1.4 for the legacy LLMClient
// wrapper: it must hand the caller's context to the provider it wraps. A
// wrapper that forwards context.Background() instead keeps the request alive
// after the caller cancels.
func TestLLMClientWrapperForwardsTheContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		call llmCtx059Call
	}{
		{"ExtractVersion", llmCtx059ExtractVersion},
		{"AnalyzeContent", llmCtx059AnalyzeContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateLLMCtx059Env(t)
			s := newLLMCtx059Server(t)
			client, err := NewLLMClient(LLMConfig{Provider: "claude", APIKeyEnv: llmCtx059KeyEnv})
			if err != nil {
				t.Fatalf("NewLLMClient: %v", err)
			}
			client.SetBaseURL(s.srv.URL)
			assertLLMCtx059Contract(t, s, client, tc.call)
		})
	}
}
