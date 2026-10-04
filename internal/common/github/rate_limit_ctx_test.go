package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// blockingRateLimitServer accepts a request, reports its arrival, and never
// answers until the client goes away or the test ends. The release runs before
// srv.Close (cleanups are LIFO), so a call that ignores its context is freed
// at cleanup and no goroutine outlives the test.
func blockingRateLimitServer(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv, arrived
}

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

// TestGitHubClientRateLimitInfoStopsOnCancel pins R1.7 and R3.8 for the GitHub
// client: cancelling the context of an in-flight GetRateLimitInfo aborts the
// request and returns within 2 s with context.Canceled. Hostile half: a request
// built without the context keeps waiting for the client timeout (30 s).
// The cancel belongs to that call only: the same client, given a live context
// afterwards, still reaches a server and parses the limit.
func TestGitHubClientRateLimitInfoStopsOnCancel(t *testing.T) {
	blocking, arrived := blockingRateLimitServer(t)
	c := NewClient()
	c.BaseURL = blocking.URL
	c.CacheDir = ""

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := c.GetRateLimitInfo(ctx)
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
		t.Fatalf("GetRateLimitInfo still running 2s after its context was cancelled (client timeout %v)", c.HTTPClient.Timeout)
	}

	live := rateLimitJSONServer(t, 4321, 1790000000)
	c.BaseURL = live.URL
	remaining, reset, err := c.GetRateLimitInfo(context.Background())
	if err != nil {
		t.Fatalf("live call after a cancelled one: err = %v, want nil", err)
	}
	if remaining != 4321 || reset.Unix() != 1790000000 {
		t.Errorf("live call after a cancelled one = (%d, %d), want (4321, 1790000000)", remaining, reset.Unix())
	}
}

// TestGitHubClientRateLimitInfoLiveContextParsesTheLimit pins the converse of
// R3.8: a live context (one with a distant deadline) does not stop the call,
// and the parsed limit is returned unchanged.
func TestGitHubClientRateLimitInfoLiveContextParsesTheLimit(t *testing.T) {
	srv := rateLimitJSONServer(t, 57, 1790003600)
	c := NewClient()
	c.BaseURL = srv.URL
	c.CacheDir = ""

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	remaining, reset, err := c.GetRateLimitInfo(ctx)
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
