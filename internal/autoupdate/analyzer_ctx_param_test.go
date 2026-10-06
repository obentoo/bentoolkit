package autoupdate

// Story 059, sub-task 3.1 (R1.3, R3.2): one Analyzer serves many calls, and
// each fetch — and each LLM analysis — is bounded by THAT call's context. The
// Analyzer holds no context of its own.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"golang.org/x/time/rate"
)

func analyzerCtx059Upstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"2.0.0"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newAnalyzerCtx059(t *testing.T, overlayDir, upstreamURL string, opts ...AnalyzerOption) *Analyzer {
	t.Helper()
	// No waiting in the limiter: these tests are about contexts, not pacing.
	rl := fetch.NewRateLimiter()
	rl.SetLLMLimit(rate.Inf, 1)
	domain, err := fetch.ExtractDomain(upstreamURL)
	if err != nil {
		t.Fatalf("extractDomain(%s): %v", upstreamURL, err)
	}
	rl.SetHTTPLimit(domain, rate.Inf, 1)
	all := append([]AnalyzerOption{
		WithAnalyzerConfigDir(t.TempDir()),
		WithAnalyzerPackagesConfig(&registry.PackagesConfig{Packages: map[string]registry.PackageConfig{}}),
		WithAnalyzerOpTimeout(10 * time.Second),
		WithAnalyzerRateLimiter(rl),
	}, opts...)
	a, err := NewAnalyzer(overlayDir, all...)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}
	return a
}

// TestAnalyzerFetchIsBoundedByItsOwnContext pins R3.2 in both orders on ONE
// Analyzer. An Analyzer that ignored the ctx parameter would let the cancelled
// fetch succeed; one that kept the first context it saw would fail the later
// live fetch.
func TestAnalyzerFetchIsBoundedByItsOwnContext(t *testing.T) {
	srv := analyzerCtx059Upstream(t)
	source := DataSource{URL: srv.URL, Type: "provided", ContentType: "application/json"}
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	live := func(t *testing.T, a *Analyzer) {
		t.Helper()
		content, _, err := a.FetchContent(context.Background(), source)
		if err != nil {
			t.Fatalf("FetchContent with a live context: %v", err)
		}
		if string(content) != `{"version":"2.0.0"}` {
			t.Fatalf("FetchContent with a live context = %q", content)
		}
	}
	dead := func(t *testing.T, a *Analyzer) {
		t.Helper()
		_, _, err := a.FetchContent(cancelled(), source)
		if err == nil {
			t.Fatal("FetchContent with a cancelled context succeeded; the fetch is not bounded by the call's own context (R3.2)")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("FetchContent with a cancelled context: err = %v; want errors.Is(err, context.Canceled)", err)
		}
	}

	t.Run("a cancelled first fetch does not fail a later live fetch", func(t *testing.T) {
		a := newAnalyzerCtx059(t, t.TempDir(), srv.URL)
		dead(t, a)
		live(t, a)
	})
	t.Run("a live first fetch does not let a later cancelled fetch succeed", func(t *testing.T) {
		a := newAnalyzerCtx059(t, t.TempDir(), srv.URL)
		live(t, a)
		dead(t, a)
	})
}

type analyzerCtx059Key struct{}

// analyzerCtx059LLM records the label of every context its methods receive.
type analyzerCtx059LLM struct {
	mu     sync.Mutex
	labels []any
}

func (l *analyzerCtx059LLM) ExtractVersion(ctx context.Context, content []byte, prompt string) (string, error) {
	l.record(ctx)
	return "2.0.0", nil
}

func (l *analyzerCtx059LLM) AnalyzeContent(ctx context.Context, content []byte, meta *ebuilds.EbuildMetadata, hint string) (*llm.SchemaAnalysis, error) {
	l.record(ctx)
	return &llm.SchemaAnalysis{ParserType: "json", Path: "version", Confidence: 0.9}, nil
}

func (l *analyzerCtx059LLM) GetModel() string { return "ctx059" }

func (l *analyzerCtx059LLM) record(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.labels = append(l.labels, ctx.Value(analyzerCtx059Key{}))
}

// TestAnalyzeHandsItsOwnContextToTheLLM pins the other half of 3.1's
// Objective: the LLM analysis inside Analyze runs under a context derived from
// the Analyze call's own context. Two calls on one Analyzer, two labels; every
// LLM call must carry the label of the Analyze call that made it.
func TestAnalyzeHandsItsOwnContextToTheLLM(t *testing.T) {
	srv := analyzerCtx059Upstream(t)
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	const pkg = "test-cat/ctx-analyze"
	createTestEbuild(t, overlayDir, pkg, "1.0.0")

	llm := &analyzerCtx059LLM{}
	a := newAnalyzerCtx059(t, overlayDir, srv.URL, WithAnalyzerLLMClient(llm))

	for _, label := range []string{"first-analyze", "second-analyze"} {
		llm.mu.Lock()
		llm.labels = nil
		llm.mu.Unlock()

		ctx := context.WithValue(context.Background(), analyzerCtx059Key{}, label)
		_, _ = a.Analyze(ctx, pkg, AnalyzeOptions{URL: srv.URL, NoCache: true, Force: true})

		llm.mu.Lock()
		got := append([]any(nil), llm.labels...)
		llm.mu.Unlock()
		if len(got) == 0 {
			t.Fatalf("Analyze(%s) never called the LLM", label)
		}
		for i, l := range got {
			if l != label {
				t.Errorf("Analyze(%s): LLM call %d carried context label %v; want %q", label, i, l, label)
			}
		}
	}
}
