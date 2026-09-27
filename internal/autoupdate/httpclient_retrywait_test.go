package autoupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// retryWaitRT is an in-memory transport that answers every request with one
// fixed status and records the time of each attempt. Run inside a synctest
// bubble, a wait costs no real time and the gap between two recorded attempts
// is exactly the wait the client chose. No sleep is used for synchronisation.
type retryWaitRT struct {
	status int
	mu     sync.Mutex
	at     []time.Time
}

func (rt *retryWaitRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.at = append(rt.at, time.Now())
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: rt.status,
		Status:     http.StatusText(rt.status),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    r,
	}, nil
}

func (rt *retryWaitRT) attempts() []time.Time {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]time.Time(nil), rt.at...)
}

// TestJitterStaysWithinCeiling pins R2.3 (full jitter): the wait before retry n
// is drawn uniformly from [0, min(1s*2^(n-1), 4s)]. The draw is random, so the
// assertions are distribution-free: every wait inside its ceiling, and over 64
// operations the waits are spread (some below half the ceiling, some at or
// above it). A fixed schedule fails; a spread one fails with probability 2^-64.
func TestJitterStaysWithinCeiling(t *testing.T) {
	ceilings := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	const runs = 64
	gaps := make([][]time.Duration, len(ceilings))

	synctest.Test(t, func(t *testing.T) {
		for i := 0; i < runs; i++ {
			rt := &retryWaitRT{status: http.StatusInternalServerError}
			c := NewRetryableHTTPClient()
			c.SetHTTPClient(&http.Client{Transport: rt})

			resp, err := c.GetWithContext(context.Background(), "http://jitter.test/pkg")
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			if err == nil {
				t.Fatal("want an error after retries against an always-500 upstream")
			}
			at := rt.attempts()
			if len(at) != len(ceilings)+1 {
				t.Fatalf("attempts = %d, want %d", len(at), len(ceilings)+1)
			}
			for n := 1; n < len(at); n++ {
				gaps[n-1] = append(gaps[n-1], at[n].Sub(at[n-1]))
			}
		}
	})

	for i, g := range gaps {
		ceil := ceilings[i]
		var atCeil, below, atOrAbove int
		for _, d := range g {
			if d < 0 || d > ceil {
				t.Errorf("retry %d waited %v, outside [0, %v]", i+1, d, ceil)
			}
			if d == ceil {
				atCeil++
			}
			if d < ceil/2 {
				below++
			} else {
				atOrAbove++
			}
		}
		if atCeil == len(g) {
			t.Errorf("retry %d waited exactly %v in all %d operations: no jitter", i+1, ceil, len(g))
		}
		if below == 0 || atOrAbove == 0 {
			t.Errorf("retry %d waits not spread over [0, %v]: %d below half, %d at or above half", i+1, ceil, below, atOrAbove)
		}
	}
}

// TestRetryWaitStopsOnCancel pins R2.1: cancelling during a retry wait returns
// at once (well inside 100 ms), sends no further attempt, and the error is
// errors.Is(context.Canceled) with "cancelled" in its text.
func TestRetryWaitStopsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := &retryWaitRT{status: http.StatusInternalServerError}
		c := NewRetryableHTTPClient()
		c.SetHTTPClient(&http.Client{Transport: rt})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			resp, err := c.GetWithContext(ctx, "http://flaky.test/pkg")
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			done <- err
		}()

		// Every goroutine is now blocked: the client is inside its first wait.
		synctest.Wait()
		if n := len(rt.attempts()); n != 1 {
			t.Fatalf("attempts before cancel = %d, want 1", n)
		}

		cancelledAt := time.Now()
		cancel()
		synctest.Wait()

		var err error
		select {
		case err = <-done:
		default:
			t.Error("GetWithContext still waiting after cancel; want it to return at once")
			err = <-done // let the pending wait run out so the bubble can end
		}
		if elapsed := time.Since(cancelledAt); elapsed > 100*time.Millisecond {
			t.Errorf("returned %v after cancel, want within 100ms", elapsed)
		}
		if n := len(rt.attempts()); n != 1 {
			t.Errorf("attempts = %d after cancel, want 1 (no further attempt)", n)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want errors.Is(err, context.Canceled)", err)
		}
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Errorf("err = %v, want its text to contain \"cancelled\"", err)
		}
	})
}

// TestRetryWaitStopsOnDeadline pins R2.2: a context deadline passing during a
// retry wait ends the call within 100 ms of the deadline, with an error that is
// errors.Is(context.DeadlineExceeded) and says "deadline exceeded".
func TestRetryWaitStopsOnDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := &retryWaitRT{status: http.StatusServiceUnavailable}
		// Many retries and no breaker, so the deadline — not retry exhaustion
		// or an open breaker — is what ends the call.
		c := NewRetryableHTTPClientWithConfig(RetryConfig{
			MaxRetries: 10,
			BaseDelay:  time.Second,
			MaxDelay:   4 * time.Second,
			Timeout:    30 * time.Second,
		})
		c.WithCircuitBreaker(false)
		c.SetHTTPClient(&http.Client{Transport: rt})

		const deadline = 50 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		defer cancel()

		start := time.Now()
		resp, err := c.GetWithContext(ctx, "http://slow.test/pkg")
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if elapsed := time.Since(start); elapsed > deadline+100*time.Millisecond {
			t.Errorf("returned %v after start; deadline was %v, want within 100ms of it", elapsed, deadline)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want errors.Is(err, context.DeadlineExceeded)", err)
		}
		if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
			t.Errorf("err = %v, want its text to contain \"deadline exceeded\"", err)
		}
	})
}
