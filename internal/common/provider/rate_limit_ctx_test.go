package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// rateLimitJSONServer answers GET /rate_limit with the given core limit.
func rateLimitJSONServer(t *testing.T, remaining int, reset int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rate_limit" {
			t.Errorf("request path = %q, want /rate_limit", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"resources":{"core":{"remaining":%d,"reset":%d}}}`, remaining, reset)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestGitHubProviderRateLimitInfoStopsOnCancel pins R1.7 and R3.8 for the
// GitHub provider: cancelling the context of an in-flight GetRateLimitInfo
// aborts the request and returns within 2 s with context.Canceled. Hostile
// half: a request built without the context keeps waiting for the client
// timeout (30 s). The cancel belongs to that call only: the same provider,
// given a live context afterwards, still reaches a server and parses the limit.
// silentServer (lookup_ctx_test.go) releases its handler at cleanup, so a call
// that ignores its context does not leak a goroutine.
func TestGitHubProviderRateLimitInfoStopsOnCancel(t *testing.T) {
	blocking, arrived := silentServer(t)
	p := newCappedGitHubProvider(t, blocking.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := p.GetRateLimitInfo(ctx)
		done <- err
	}()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the rate-limit request never reached the server")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want errors.Is(err, context.Canceled)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("GetRateLimitInfo still running 2s after its context was cancelled (client timeout %v)", p.HTTPClient.Timeout)
	}

	live := rateLimitJSONServer(t, 4321, 1790000000)
	p.BaseURL = live.URL
	remaining, reset, err := p.GetRateLimitInfo(context.Background())
	if err != nil {
		t.Fatalf("live call after a cancelled one: err = %v, want nil", err)
	}
	if remaining != 4321 || reset.Unix() != 1790000000 {
		t.Errorf("live call after a cancelled one = (%d, %d), want (4321, 1790000000)", remaining, reset.Unix())
	}
}

// TestGitHubProviderRateLimitInfoLiveContextParsesTheLimit pins the converse
// of R3.8: a live context (one with a distant deadline) does not stop the call,
// and the parsed limit is returned unchanged.
func TestGitHubProviderRateLimitInfoLiveContextParsesTheLimit(t *testing.T) {
	srv := rateLimitJSONServer(t, 57, 1790003600)
	p := newCappedGitHubProvider(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	remaining, reset, err := p.GetRateLimitInfo(ctx)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if remaining != 57 {
		t.Errorf("remaining = %d, want 57", remaining)
	}
	if !reset.Equal(time.Unix(1790003600, 0)) {
		t.Errorf("reset = %v, want %v", reset, time.Unix(1790003600, 0))
	}
}
