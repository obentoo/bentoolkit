package autoupdate

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// retryAfterRT answers the FIRST attempt with the status and Retry-After value
// chosen by first (computed from the bubble clock, so HTTP-dates are exact) and
// every later attempt with 200. It records the time of each attempt.
type retryAfterRT struct {
	first func(now time.Time) (status int, retryAfter string)
	mu    sync.Mutex
	at    []time.Time
}

func (rt *retryAfterRT) RoundTrip(r *http.Request) (*http.Response, error) {
	now := time.Now()
	rt.mu.Lock()
	n := len(rt.at)
	rt.at = append(rt.at, now)
	rt.mu.Unlock()

	code, header := http.StatusOK, make(http.Header)
	if n == 0 {
		var ra string
		code, ra = rt.first(now)
		if ra != "" {
			header.Set("Retry-After", ra)
		}
	}
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    r,
	}, nil
}

type retryAfterOutcome struct {
	attempts int
	gap      time.Duration // wait between attempt 1 and 2 (0 if only one attempt)
	elapsed  time.Duration
	err      error
}

// runRetryAfter must be called inside a synctest bubble. The client is the
// default one — circuit breaker ENABLED — because R3.1 must hold with it on.
func runRetryAfter(t *testing.T, host string, budget time.Duration, first func(time.Time) (int, string)) retryAfterOutcome {
	t.Helper()
	rt := &retryAfterRT{first: first}
	c := NewRetryableHTTPClient()
	c.SetHTTPClient(&http.Client{Transport: rt})

	ctx := context.Background()
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}

	start := time.Now()
	resp, err := c.GetWithContext(ctx, "http://"+host+"/pkg")
	elapsed := time.Since(start)
	if resp != nil && resp.Body != nil {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck // test drain
		resp.Body.Close()
	}

	rt.mu.Lock()
	at := append([]time.Time(nil), rt.at...)
	rt.mu.Unlock()
	out := retryAfterOutcome{attempts: len(at), elapsed: elapsed, err: err}
	if len(at) >= 2 {
		out.gap = at[1].Sub(at[0])
	}
	return out
}

func fixedRetryAfter(code int, value string) func(time.Time) (int, string) {
	return func(time.Time) (int, string) { return code, value }
}

func dateRetryAfter(code int, offset time.Duration, layout string) func(time.Time) (int, string) {
	return func(now time.Time) (int, string) { return code, now.Add(offset).UTC().Format(layout) }
}

// TestRetryAfterDeltaSecondsHonoured pins R3.1: a 429 or 503 carrying
// delta-seconds <= 60 waits exactly that long instead of the jittered backoff,
// with the breaker enabled. Hostile half: a Retry-After on a status the rule
// does not cover (500) must NOT be obeyed.
func TestRetryAfterDeltaSecondsHonoured(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		value   string
		wantGap time.Duration // exact; 0 means "jitter", checked as <= 1s
	}{
		{"429 waits the delta", http.StatusTooManyRequests, "7", 7 * time.Second},
		{"503 waits the delta", http.StatusServiceUnavailable, "3", 3 * time.Second},
		{"500 ignores Retry-After", http.StatusInternalServerError, "7", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := runRetryAfter(t, "ra.test", 0, fixedRetryAfter(tt.status, tt.value))
				if o.err != nil || o.attempts != 2 {
					t.Fatalf("attempts=%d err=%v; want 2 attempts ending in success", o.attempts, o.err)
				}
				if tt.wantGap > 0 && o.gap != tt.wantGap {
					t.Errorf("waited %v before the retry, want exactly %v (Retry-After: %s)", o.gap, tt.wantGap, tt.value)
				}
				if tt.wantGap == 0 && o.gap > time.Second {
					t.Errorf("waited %v on a %d with Retry-After: %s; want the jittered backoff (<= 1s)", o.gap, tt.status, tt.value)
				}
			})
		})
	}
}

// TestRetryAfterHTTPDateHonoured pins R3.2: an HTTP-date waits until that
// instant, or not at all when it is already past.
func TestRetryAfterHTTPDateHonoured(t *testing.T) {
	t.Run("future date", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := runRetryAfter(t, "ra.test", 0, dateRetryAfter(http.StatusServiceUnavailable, 9*time.Second, http.TimeFormat))
			if o.err != nil || o.attempts != 2 {
				t.Fatalf("attempts=%d err=%v; want 2 attempts ending in success", o.attempts, o.err)
			}
			if o.gap < 8*time.Second || o.gap > 9*time.Second {
				t.Errorf("waited %v, want until the date 9s ahead (8s..9s after one-second truncation)", o.gap)
			}
		})
	})
	t.Run("past date", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := runRetryAfter(t, "ra.test", 0, dateRetryAfter(http.StatusTooManyRequests, -30*time.Second, http.TimeFormat))
			if o.err != nil || o.attempts != 2 {
				t.Fatalf("attempts=%d err=%v; want 2 attempts ending in success", o.attempts, o.err)
			}
			if o.gap != 0 {
				t.Errorf("waited %v for a Retry-After date already past, want no wait", o.gap)
			}
		})
	})
}

// TestRetryAfterOverLimitFailsNamingHost pins R3.3: a Retry-After over 60 s
// fails at once, without waiting, naming the host and the requested wait.
// Hostile half: exactly 60 s is within the limit and must be waited out.
func TestRetryAfterOverLimitFailsNamingHost(t *testing.T) {
	const host = "slow.test:8443"
	over := []struct {
		name  string
		first func(time.Time) (int, string)
		want  []string // any of these renders the requested wait
	}{
		{"delta-seconds 61", fixedRetryAfter(http.StatusTooManyRequests, "61"), []string{"61", "1m1s"}},
		{"HTTP-date 90s ahead", dateRetryAfter(http.StatusServiceUnavailable, 90*time.Second, http.TimeFormat), []string{"90", "1m30s"}},
	}
	for _, tt := range over {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := runRetryAfter(t, host, 0, tt.first)
				if o.err == nil {
					t.Fatalf("attempts=%d, err=nil; want the request to fail on an over-limit Retry-After", o.attempts)
				}
				if o.attempts != 1 || o.elapsed != 0 {
					t.Errorf("attempts=%d elapsed=%v; want 1 attempt and no wait", o.attempts, o.elapsed)
				}
				msg := o.err.Error()
				if !strings.Contains(msg, host) {
					t.Errorf("error %q does not name the host %s", msg, host)
				}
				named := false
				for _, w := range tt.want {
					named = named || strings.Contains(msg, w)
				}
				if !named {
					t.Errorf("error %q does not name the requested wait (any of %q)", msg, tt.want)
				}
			})
		})
	}

	t.Run("exactly 60s is honoured", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := runRetryAfter(t, host, 0, fixedRetryAfter(http.StatusTooManyRequests, "60"))
			if o.err != nil || o.attempts != 2 || o.gap != 60*time.Second {
				t.Errorf("attempts=%d gap=%v err=%v; want a 60s wait then success", o.attempts, o.gap, o.err)
			}
		})
	})
}

// TestRetryAfterBeyondBudgetFails pins R3.4: a Retry-After longer than the time
// left before the context deadline fails at once, naming the host, the wait and
// the remaining budget. Hostile halves: a shorter Retry-After inside the budget,
// and any Retry-After with no deadline at all, are waited out.
func TestRetryAfterBeyondBudgetFails(t *testing.T) {
	const host = "budget.test:9443"

	t.Run("30s asked, 10s left", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := runRetryAfter(t, host, 10*time.Second, fixedRetryAfter(http.StatusTooManyRequests, "30"))
			if o.err == nil {
				t.Fatalf("attempts=%d, err=nil; want the request to fail when Retry-After exceeds the budget", o.attempts)
			}
			if o.attempts != 1 || o.elapsed != 0 {
				t.Errorf("attempts=%d elapsed=%v; want 1 attempt and no wait", o.attempts, o.elapsed)
			}
			msg := o.err.Error()
			for _, want := range []string{host, "30s", "10s"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q lacks %q (host, requested wait, remaining budget)", msg, want)
				}
			}
		})
	})

	t.Run("5s asked, 10s left", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := runRetryAfter(t, host, 10*time.Second, fixedRetryAfter(http.StatusTooManyRequests, "5"))
			if o.err != nil || o.attempts != 2 || o.gap != 5*time.Second {
				t.Errorf("attempts=%d gap=%v err=%v; want a 5s wait then success", o.attempts, o.gap, o.err)
			}
		})
	})

	t.Run("30s asked, no deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := runRetryAfter(t, host, 0, fixedRetryAfter(http.StatusServiceUnavailable, "30"))
			if o.err != nil || o.attempts != 2 || o.gap != 30*time.Second {
				t.Errorf("attempts=%d gap=%v err=%v; want a 30s wait then success", o.attempts, o.gap, o.err)
			}
		})
	})
}

// TestRetryAfterUnparseableUsesJitter pins R3.5: an absent or unparseable
// Retry-After falls back to the full-jitter wait of R2.3 — within [0, 1s] for
// the first retry, and spread rather than fixed.
func TestRetryAfterUnparseableUsesJitter(t *testing.T) {
	const runs = 48
	for _, value := range []string{"", "soon", "-5", "1.5", "7s"} {
		t.Run("Retry-After="+value, func(t *testing.T) {
			var gaps []time.Duration
			synctest.Test(t, func(t *testing.T) {
				for i := 0; i < runs; i++ {
					o := runRetryAfter(t, "ra.test", 0, fixedRetryAfter(http.StatusServiceUnavailable, value))
					if o.err != nil || o.attempts != 2 {
						t.Fatalf("attempts=%d err=%v; want 2 attempts ending in success", o.attempts, o.err)
					}
					gaps = append(gaps, o.gap)
				}
			})
			var atCeil, below, atOrAbove int
			for _, g := range gaps {
				if g < 0 || g > time.Second {
					t.Errorf("waited %v, outside the jitter range [0, 1s]", g)
				}
				if g == time.Second {
					atCeil++
				}
				if g < 500*time.Millisecond {
					below++
				} else {
					atOrAbove++
				}
			}
			if atCeil == len(gaps) || below == 0 || atOrAbove == 0 {
				t.Errorf("waits not jittered: %d of %d exactly 1s, %d below 500ms, %d at or above", atCeil, len(gaps), below, atOrAbove)
			}
		})
	}
}

// TestParseRetryAfter covers the accepted Retry-After spellings end to end:
// delta-seconds including 0 and the 60 boundary, and the three HTTP-date forms
// http.ParseTime accepts (IMF-fixdate, RFC 850, ANSI C).
func TestParseRetryAfter(t *testing.T) {
	const rfc850 = "Monday, 02-Jan-06 15:04:05 GMT"
	tests := []struct {
		name    string
		first   func(time.Time) (int, string)
		minWait time.Duration
		maxWait time.Duration
	}{
		{"delta 0", fixedRetryAfter(http.StatusServiceUnavailable, "0"), 0, 0},
		{"delta 60", fixedRetryAfter(http.StatusServiceUnavailable, "60"), 60 * time.Second, 60 * time.Second},
		{"delta with leading zeros", fixedRetryAfter(http.StatusTooManyRequests, "007"), 7 * time.Second, 7 * time.Second},
		{"IMF-fixdate +5s", dateRetryAfter(http.StatusServiceUnavailable, 5*time.Second, http.TimeFormat), 4 * time.Second, 5 * time.Second},
		{"RFC 850 +5s", dateRetryAfter(http.StatusServiceUnavailable, 5*time.Second, rfc850), 4 * time.Second, 5 * time.Second},
		{"ANSI C +5s", dateRetryAfter(http.StatusTooManyRequests, 5*time.Second, time.ANSIC), 4 * time.Second, 5 * time.Second},
		{"IMF-fixdate in 1994", fixedRetryAfter(http.StatusServiceUnavailable, "Sun, 06 Nov 1994 08:49:37 GMT"), 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := runRetryAfter(t, "ra.test", 0, tt.first)
				if o.err != nil || o.attempts != 2 {
					t.Fatalf("attempts=%d err=%v; want 2 attempts ending in success", o.attempts, o.err)
				}
				if o.gap < tt.minWait || o.gap > tt.maxWait {
					t.Errorf("waited %v, want %v..%v", o.gap, tt.minWait, tt.maxWait)
				}
			})
		})
	}
}
