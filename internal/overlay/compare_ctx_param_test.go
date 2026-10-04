package overlay

// Story 059, sub-task 6.1 (R1.5, R3.6): CompareWithProvider takes the context
// as its first parameter and hands a context derived from it to EVERY
// provider.GetPackageVersions call it makes. The options carry no context.
//
// The provider records the label carried by each context it receives, so a
// comparison that looked its packages up under context.Background() — or under
// a context left over from another call — is caught by the label.

import (
	"context"
	"sync"
	"testing"
)

type compareCtx059Key struct{}

type compareCtx059Provider struct {
	mu     sync.Mutex
	labels []any
	ctxs   []context.Context
}

func (p *compareCtx059Provider) GetPackageVersions(ctx context.Context, category, pkg string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.labels = append(p.labels, ctx.Value(compareCtx059Key{}))
	p.ctxs = append(p.ctxs, ctx)
	return []string{"1.0"}, nil
}
func (p *compareCtx059Provider) GetName() string   { return "ctx059" }
func (p *compareCtx059Provider) SupportsAPI() bool { return true }
func (p *compareCtx059Provider) Close() error      { return nil }

func (p *compareCtx059Provider) take() ([]any, []context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, c := p.labels, p.ctxs
	p.labels, p.ctxs = nil, nil
	return l, c
}

func TestCompareWithProviderPassesTheCallersContext(t *testing.T) {
	prov := &compareCtx059Provider{}
	pkgs := []PackageInfo{
		{Category: "cat", Package: "a", LatestVersion: "1.0"},
		{Category: "cat", Package: "b", LatestVersion: "1.0"},
		{Category: "cat", Package: "c", LatestVersion: "1.0"},
	}

	for _, label := range []string{"first-compare", "second-compare"} {
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), compareCtx059Key{}, label))
		if _, err := CompareWithProvider(ctx, pkgs, prov, CompareOptions{Concurrency: 2}); err != nil {
			cancel()
			t.Fatalf("CompareWithProvider(%s): %v", label, err)
		}
		labels, ctxs := prov.take()
		if len(labels) != len(pkgs) {
			t.Errorf("%s: provider was asked %d time(s); want %d", label, len(labels), len(pkgs))
		}
		for i, got := range labels {
			if got != label {
				t.Errorf("%s: lookup %d carried context label %v; want %q (R3.6)", label, i, got, label)
			}
		}
		// A context derived from the caller's has ended once the caller's
		// has (it may also have ended earlier, when the comparison returned).
		cancel()
		for i, c := range ctxs {
			if c.Err() == nil {
				t.Errorf("%s: lookup %d's context is live after the caller's context was cancelled; it is not derived from it (R3.6)", label, i)
			}
		}
	}
}
