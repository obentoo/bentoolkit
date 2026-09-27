package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestGitHubClientLookupHonoursCancel pins R7.1 for the GitHub client: its
// GetPackageVersions takes a leading context and sends its request with it, so
// cancelling an in-flight lookup ends it within 1 s with context.Canceled.
func TestGitHubClientLookupHonoursCancel(t *testing.T) {
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
	t.Cleanup(func() { close(release) }) // runs before srv.Close (LIFO)

	c := NewClient()
	c.BaseURL = srv.URL
	c.CacheDir = ""

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.GetPackageVersions(ctx, "app-misc", "foo")
		done <- err
	}()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the lookup's request never reached the server")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want errors.Is(err, context.Canceled)", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lookup still running 1s after its context was cancelled")
	}
}
