package main

// Story 059, sub-task 1.3 (R3.4): the two cmd review adapters hand the claude
// request a context derived from the context of the review call itself.
//
// The asker below records the label carried in the context it receives and
// keeps that context, so an adapter that passed context.Background() (or a
// context captured when the client was built) is caught twice: by the missing
// label and by a context that does not end when the caller's does.

import (
	"context"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/overlay"
)

type reviewCtx059Key struct{}

type reviewCtx059Asker struct {
	mu     sync.Mutex
	reply  string
	labels []any
	ctxs   []context.Context
}

func (a *reviewCtx059Asker) AskJSON(ctx context.Context, instruction string, content []byte, schema string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.labels = append(a.labels, ctx.Value(reviewCtx059Key{}))
	a.ctxs = append(a.ctxs, ctx)
	return a.reply, nil
}

var _ claudeAsker = (*reviewCtx059Asker)(nil)

// assertReviewCtx059 runs two review calls with distinct labelled contexts
// through review and checks each reached the asker with its own label, then
// that each context the asker saw ends with its caller's.
func assertReviewCtx059(t *testing.T, asker *reviewCtx059Asker, review func(ctx context.Context) error) {
	t.Helper()
	labels := []string{"first-review", "second-review"}
	cancels := make([]context.CancelFunc, 0, len(labels))
	for _, label := range labels {
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), reviewCtx059Key{}, label))
		cancels = append(cancels, cancel)
		if err := review(ctx); err != nil {
			t.Fatalf("review %q: unexpected error: %v", label, err)
		}
	}
	asker.mu.Lock()
	gotLabels := append([]any(nil), asker.labels...)
	gotCtxs := append([]context.Context(nil), asker.ctxs...)
	asker.mu.Unlock()

	if len(gotLabels) != len(labels) {
		t.Fatalf("asker was called %d time(s); want %d", len(gotLabels), len(labels))
	}
	for i, want := range labels {
		if gotLabels[i] != want {
			t.Errorf("call %d reached the claude request with context label %v; want %q (R3.4)", i, gotLabels[i], want)
		}
	}
	// A context derived from the caller's ends when the caller's does. (A
	// derived context may also end earlier, when the adapter returns, so only
	// "ended" is asserted, never "still live".)
	for i, cancel := range cancels {
		cancel()
		if gotCtxs[i].Err() == nil {
			t.Errorf("request %d's context is live after its review's context was cancelled; it is not derived from the review's context (R3.4)", i)
		}
	}
}

// TestDivergenceReviewerPassesEachCallsContext pins R3.4 for
// claudeDivergenceReviewer.ReviewDivergence.
func TestDivergenceReviewerPassesEachCallsContext(t *testing.T) {
	asker := &reviewCtx059Asker{reply: `{"origin":"overlay","summary":"adds a patch"}`}
	reviewer := &claudeDivergenceReviewer{asker: asker}
	req := overlay.ReviewRequest{
		Category: "kde-plasma", Package: "spectacle", Version: "6.7.4",
		Ours: []byte("EAPI=8\nPATCHES=( a.patch )\n"), Theirs: []byte("EAPI=8\n"),
	}
	assertReviewCtx059(t, asker, func(ctx context.Context) error {
		_, err := reviewer.ReviewDivergence(ctx, req)
		return err
	})
}

// TestRealignReviewerPassesEachCallsContext pins R3.4 for
// claudeRealignReviewer.ReviewRealignment.
func TestRealignReviewerPassesEachCallsContext(t *testing.T) {
	asker := &reviewCtx059Asker{reply: `{"justified":true,"why":"keeps a patch ::gentoo lacks"}`}
	reviewer := &claudeRealignReviewer{asker: asker}
	req := overlay.RealignRequest{
		Category: "kde-plasma", Package: "spectacle", Version: "6.7.4",
		Ours: []byte("EAPI=8\nPATCHES=( a.patch )\n"), Baseline: []byte("EAPI=8\n"),
	}
	assertReviewCtx059(t, asker, func(ctx context.Context) error {
		_, err := reviewer.ReviewRealignment(ctx, req)
		return err
	})
}
