package fetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/httpx"
)

// TestSetRequestTimeoutRaisesHeaderTimeout pins R6.3 (and R6.2's 30 s default
// on the retrying client): the header wait starts at 30 s on both the primary
// and the HTTP/1.1 fallback transport, follows a per-request timeout ABOVE 30 s
// upward, and is never lowered by a smaller or non-positive one.
func TestSetRequestTimeoutRaisesHeaderTimeout(t *testing.T) {
	headerTimeouts := func(t *testing.T, c *RetryableHTTPClient) (primary, fallback time.Duration) {
		t.Helper()
		p, ok := c.client.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("primary transport is %T, want *http.Transport", c.client.Transport)
		}
		f, ok := c.h1Client.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("fallback transport is %T, want *http.Transport", c.h1Client.Transport)
		}
		return p.ResponseHeaderTimeout, f.ResponseHeaderTimeout
	}

	tests := []struct {
		name string
		set  *time.Duration
		want time.Duration
	}{
		{"default", nil, 30 * time.Second},
		{"45s raises it", ptrDuration(45 * time.Second), 45 * time.Second},
		{"120s raises it", ptrDuration(120 * time.Second), 120 * time.Second},
		{"30s keeps it", ptrDuration(30 * time.Second), 30 * time.Second},
		{"10s never lowers it", ptrDuration(10 * time.Second), 30 * time.Second},
		{"zero is ignored", ptrDuration(0), 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewRetryableHTTPClient()
			if tt.set != nil {
				c.SetRequestTimeout(*tt.set)
			}
			primary, fallback := headerTimeouts(t, c)
			if primary != tt.want || fallback != tt.want {
				t.Errorf("ResponseHeaderTimeout primary=%v fallback=%v, want %v on both", primary, fallback, tt.want)
			}
		})
	}
}

func ptrDuration(d time.Duration) *time.Duration { return &d }

// TestHeaderTimeoutRetriedLikeTimeout pins R6.2's retry half: an attempt ended
// by the header timeout is retried like any other timeout, and when every
// attempt ends that way the error carries ErrRequestTimeout as well as
// ErrMaxRetriesExceeded (R4.1). The header timeout is shortened on an injected
// transport so no test waits 30 s.
func TestHeaderTimeoutRetriedLikeTimeout(t *testing.T) {
	newHangingServer := func(t *testing.T, hangFirst int32) (*httptest.Server, *atomic.Int32) {
		t.Helper()
		hits := new(atomic.Int32)
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hits.Add(1) <= hangFirst {
				// Accept the request but never send headers.
				select {
				case <-r.Context().Done():
				case <-release:
				}
				return
			}
			io.WriteString(w, "ok")
		}))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) }) // runs before srv.Close (LIFO)
		return srv, hits
	}
	newClient := func() *RetryableHTTPClient {
		c := NewRetryableHTTPClientWithConfig(RetryConfig{
			MaxRetries: 1,
			BaseDelay:  time.Second,
			MaxDelay:   4 * time.Second,
			Timeout:    10 * time.Second,
		})
		tr := httpx.BuildTransport()
		tr.ResponseHeaderTimeout = 50 * time.Millisecond
		c.SetHTTPClient(&http.Client{Transport: tr, Timeout: 10 * time.Second})
		return c
	}

	t.Run("one silent attempt, then success", func(t *testing.T) {
		srv, hits := newHangingServer(t, 1)
		resp, err := newClient().GetWithContext(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("GetWithContext: %v; want the header timeout retried and the 2nd attempt to succeed", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Errorf("got %d %q, want 200 \"ok\"", resp.StatusCode, body)
		}
		if got := hits.Load(); got != 2 {
			t.Errorf("server received %d requests, want 2", got)
		}
	})

	t.Run("every attempt silent", func(t *testing.T) {
		srv, hits := newHangingServer(t, 1<<30)
		resp, err := newClient().GetWithContext(context.Background(), srv.URL)
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if got := hits.Load(); got != 2 {
			t.Errorf("server received %d requests, want 2 (1 + MaxRetries)", got)
		}
		if !errors.Is(err, ErrMaxRetriesExceeded) {
			t.Errorf("err = %v, want errors.Is(err, ErrMaxRetriesExceeded)", err)
		}
		if !errors.Is(err, ErrRequestTimeout) {
			t.Errorf("err = %v, want errors.Is(err, ErrRequestTimeout): the last attempt ended on the header timeout", err)
		}
	})
}
