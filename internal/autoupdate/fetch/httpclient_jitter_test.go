package fetch

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

// TestJitterUsesInjectedSource pins R2.3 deterministically: the wait before
// retry n is whatever the client's jitter source returns for that retry's
// ceiling (1s, 2s, 4s), and the source is asked for nothing else. A fixed
// source that returns a quarter of each ceiling makes the waits exact, so the
// gaps between attempts are asserted with equality rather than statistically.
func TestJitterUsesInjectedSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := &retryWaitRT{status: http.StatusInternalServerError}
		c := NewRetryableHTTPClient()
		c.SetHTTPClient(&http.Client{Transport: rt})

		var asked []time.Duration
		c.jitter = func(ceiling time.Duration) time.Duration {
			asked = append(asked, ceiling)
			return ceiling / 4
		}

		resp, err := c.GetWithContext(context.Background(), "http://fixed-jitter.test/pkg")
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if err == nil {
			t.Fatal("want an error after retries against an always-500 upstream")
		}

		wantCeilings := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
		if len(asked) != len(wantCeilings) {
			t.Fatalf("jitter source asked %d times (%v), want %d", len(asked), asked, len(wantCeilings))
		}
		for i, want := range wantCeilings {
			if asked[i] != want {
				t.Errorf("retry %d asked the source for ceiling %v, want %v", i+1, asked[i], want)
			}
		}

		at := rt.attempts()
		if len(at) != len(wantCeilings)+1 {
			t.Fatalf("attempts = %d, want %d", len(at), len(wantCeilings)+1)
		}
		for n := 1; n < len(at); n++ {
			if got, want := at[n].Sub(at[n-1]), wantCeilings[n-1]/4; got != want {
				t.Errorf("retry %d waited %v, want exactly %v (the injected draw)", n, got, want)
			}
		}
	})
}
