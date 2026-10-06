package fetch

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// TestRetryAfterOverflowFailsAtOnce pins R3.6 (with R3.3): delta-seconds too
// large for a time.Duration is over the limit, so the request fails at once
// with ErrRetryAfterTooLong naming the host — it must never wrap into a
// negative wait (an immediate retry) or a short positive one (a wait that is
// then obeyed).
//
// Hostile halves come first: values whose seconds-to-nanoseconds product
// wraps int64. The benign halves follow: a large value that still fits
// (9223372036) and the 61 s boundary fail the same way, and exactly 60 s is
// still waited out.
func TestRetryAfterOverflowFailsAtOnce(t *testing.T) {
	const host = "big.test:8443"
	over := []struct {
		name  string
		value string
	}{
		// Hostile: each wraps today.
		{"wraps negative (9223372037)", "9223372037"},
		{"wraps to ~59.3s (18446744133)", "18446744133"},
		{"max int64 wraps to -1s", "9223372036854775807"},
		{"leading zeros, wraps negative", "0009223372037"},
		// Benign: fits in a Duration and is plainly over the limit.
		{"largest non-wrapping (9223372036)", "9223372036"},
		{"one over the limit (61)", "61"},
	}
	for _, tt := range over {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(tt.name+"/"+http.StatusText(status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					o := runRetryAfter(t, host, 0, fixedRetryAfter(status, tt.value))
					if o.err == nil {
						t.Fatalf("Retry-After: %s -> attempts=%d gap=%v err=nil; want an immediate ErrRetryAfterTooLong",
							tt.value, o.attempts, o.gap)
					}
					if !errors.Is(o.err, ErrRetryAfterTooLong) {
						t.Errorf("Retry-After: %s -> err %q is not ErrRetryAfterTooLong", tt.value, o.err)
					}
					if o.attempts != 1 || o.elapsed != 0 {
						t.Errorf("Retry-After: %s -> attempts=%d elapsed=%v; want 1 attempt and no wait",
							tt.value, o.attempts, o.elapsed)
					}
					if msg := o.err.Error(); !strings.Contains(msg, host) {
						t.Errorf("error %q does not name the host %s", msg, host)
					}
				})
			})
		}
	}

	t.Run("exactly 60s is still honoured", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := runRetryAfter(t, host, 0, fixedRetryAfter(http.StatusTooManyRequests, "60"))
			if o.err != nil || o.attempts != 2 || o.gap != 60*time.Second {
				t.Errorf("attempts=%d gap=%v err=%v; want a 60s wait then success", o.attempts, o.gap, o.err)
			}
		})
	})
}

// TestRetryAfterOverflowNeverParsesWaitable pins the same rule at the parser:
// a delta-seconds value over 60 must never come back as a duration the client
// would wait out — in particular never negative and never at or below
// MaxRetryAfter through an integer wrap. Values within the limit still parse
// to exactly their seconds.
func TestRetryAfterOverflowNeverParsesWaitable(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, value := range []string{"9223372037", "18446744133", "9223372036854775807", "0009223372037", "9223372036", "61"} {
		d, ok := parseRetryAfter(value, now)
		if ok && d <= MaxRetryAfter {
			t.Errorf("parseRetryAfter(%q) = %v, true; an over-limit value must not parse to a wait of at most %v",
				value, d, MaxRetryAfter)
		}
	}
	for value, want := range map[string]time.Duration{"0": 0, "59": 59 * time.Second, "60": 60 * time.Second} {
		if d, ok := parseRetryAfter(value, now); !ok || d != want {
			t.Errorf("parseRetryAfter(%q) = %v, %v; want %v, true", value, d, ok, want)
		}
	}
}
