package fetch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A body that takes longer than the per-request timeout to arrive is cut off
// by Get but read whole by Download, which ctx alone bounds.
func TestDownloadOutlivesTheRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "payload")
	}))
	defer srv.Close()

	c := NewRetryableHTTPClient()
	c.SetRequestTimeout(100 * time.Millisecond)

	resp, err := c.Get(context.Background(), srv.URL)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("Get read a body slower than its request timeout; the premise of this test no longer holds")
	}

	resp, err = c.Download(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(body), "payload") || resp.StatusCode != http.StatusOK {
		t.Fatalf("Download = %d %q, %v; want 200 payload", resp.StatusCode, body, err)
	}
}

// ctx still bounds a Download.
func TestDownloadHonoursContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	resp, err := NewRetryableHTTPClient().Download(ctx, srv.URL)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("Download outlived its context")
	}
}

// DownloadRequest sends the caller's headers and fills in the client's
// defaults only where the caller set none.
func TestDownloadRequestKeepsCallerHeaders(t *testing.T) {
	got := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
		_, _ = io.WriteString(w, "payload")
	}))
	defer srv.Close()

	c := NewRetryableHTTPClient()
	c.SetDefaultHeaders(map[string]string{"Cache-Control": "max-age=60", "X-Default": "yes"})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.DownloadRequest(req)
	if err != nil {
		t.Fatalf("DownloadRequest: %v", err)
	}
	resp.Body.Close()
	h := <-got
	if h.Get("Cache-Control") != "no-cache" || h.Get("X-Default") != "yes" || h.Get("User-Agent") == "" {
		t.Errorf("headers sent = %v; want the caller's Cache-Control plus the defaults", h)
	}
}

// Wait runs the delay function in place of the timer and still reports a
// done context; without one it returns as soon as the context is done.
func TestWaitUsesTheDelayFuncAndHonoursContext(t *testing.T) {
	c := NewRetryableHTTPClient()
	var waited time.Duration
	c.SetDelayFunc(func(d time.Duration) { waited = d })
	if err := c.Wait(context.Background(), 2*time.Second); err != nil || waited != 2*time.Second {
		t.Errorf("Wait = %v after a delay of %s; want nil after 2s", err, waited)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := NewRetryableHTTPClient().Wait(ctx, time.Minute); err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("Wait on a cancelled context = %v after %s; want its error at once", err, time.Since(start))
	}
}
