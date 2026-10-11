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
