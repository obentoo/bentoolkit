package autoupdate

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// malformedDeltaValues are Retry-After values that are NOT delta-seconds
// (delta-seconds is one or more ASCII digits and nothing else) and are not
// HTTP-dates either, so R3.5 sends them to the jittered backoff. Each one
// starts with a run of digits (or a sign strconv accepts), which is what makes
// an integer parser mistake it for a delta.
type malformedRetryAfter struct {
	name  string
	value string
}

var malformedDeltaValues = []malformedRetryAfter{
	// Hostile: the leading digits alone overflow an int, so a parser that
	// reads them as a saturated delta fails the request at once.
	{"overflowing digits then letters", "99999999999999999999abc"},
	{"overflowing digits then a fraction", "99999999999999999999.5"},
	{"overflowing digits then a word", "99999999999999999999 x"},
	// Hostile: a sign is not a digit, so these are not delta-seconds either.
	{"plus sign, overflowing digits", "+99999999999999999999"},
	{"plus sign, small value", "+7"},
}

// TestRetryAfterMalformedOverflowUsesJitter pins R3.5 against the overflow
// rule of R3.6: a Retry-After that merely STARTS with digits — even digits too
// large for an int — is unparseable, so a 429 or 503 carrying it takes the
// jittered backoff (first retry within [0, 1s]) and succeeds. It must not be
// collapsed into the all-digits over-limit class and failed with
// ErrRetryAfterTooLong. The benign half ("1.5", a small non-integer) already
// takes the jitter.
func TestRetryAfterMalformedOverflowUsesJitter(t *testing.T) {
	const host = "malformed.test:8443"
	cases := append(append([]malformedRetryAfter(nil), malformedDeltaValues...),
		malformedRetryAfter{"benign fraction", "1.5"})

	for _, tt := range cases {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(tt.name+"/"+http.StatusText(status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					o := runRetryAfter(t, host, 0, fixedRetryAfter(status, tt.value))
					if errors.Is(o.err, ErrRetryAfterTooLong) {
						t.Fatalf("Retry-After: %q -> attempts=%d err=%v; a malformed value must take the jittered backoff, not fail as over the limit",
							tt.value, o.attempts, o.err)
					}
					if o.err != nil || o.attempts != 2 {
						t.Fatalf("Retry-After: %q -> attempts=%d err=%v; want 2 attempts ending in success",
							tt.value, o.attempts, o.err)
					}
					if o.gap < 0 || o.gap > time.Second {
						t.Errorf("Retry-After: %q -> waited %v; want the jittered backoff within [0, 1s]", tt.value, o.gap)
					}
				})
			})
		}
	}
}

// TestRetryAfterMalformedAllDigitsStillOverLimit is the converse of the test
// above (R3.6): a value made ONLY of ASCII digits is delta-seconds however many
// digits it has, so one too large for a time.Duration — or even for an int —
// must not be split off into the unparseable class. It fails at once with
// ErrRetryAfterTooLong naming the host. Surrounding whitespace does not make
// it malformed.
func TestRetryAfterMalformedAllDigitsStillOverLimit(t *testing.T) {
	const host = "malformed.test:8443"
	for _, value := range []string{
		"9999999999999999999999999",   // 25 digits, overflows an int
		"99999999999999999999",        // 20 digits, the prefix of the hostile values
		" 9999999999999999999999999 ", // padded: still all digits once trimmed
	} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(strings.TrimSpace(value)+"/"+http.StatusText(status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					o := runRetryAfter(t, host, 0, fixedRetryAfter(status, value))
					if o.err == nil {
						t.Fatalf("Retry-After: %q -> attempts=%d gap=%v err=nil; want an immediate ErrRetryAfterTooLong",
							value, o.attempts, o.gap)
					}
					if !errors.Is(o.err, ErrRetryAfterTooLong) {
						t.Errorf("Retry-After: %q -> err %q is not ErrRetryAfterTooLong", value, o.err)
					}
					if o.attempts != 1 || o.elapsed != 0 {
						t.Errorf("Retry-After: %q -> attempts=%d elapsed=%v; want 1 attempt and no wait",
							value, o.attempts, o.elapsed)
					}
					if msg := o.err.Error(); !strings.Contains(msg, host) {
						t.Errorf("error %q does not name the host %s", msg, host)
					}
				})
			})
		}
	}
}

// TestRetryAfterMalformedParse pins the same split at the parser: a value that
// is not all ASCII digits (and not an HTTP-date) is unparseable — ok=false —
// while an all-digits value too large for a Duration parses as over the limit.
func TestRetryAfterMalformedParse(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	unparseable := []string{"-0", "1.5"}
	for _, tt := range malformedDeltaValues {
		unparseable = append(unparseable, tt.value)
	}
	for _, value := range unparseable {
		if d, ok := parseRetryAfter(value, now); ok {
			t.Errorf("parseRetryAfter(%q) = %v, true; want ok=false (not delta-seconds, not an HTTP-date)", value, d)
		}
	}

	for _, value := range []string{"9999999999999999999999999", "99999999999999999999"} {
		if d, ok := parseRetryAfter(value, now); !ok || d <= MaxRetryAfter {
			t.Errorf("parseRetryAfter(%q) = %v, %v; want ok=true with a wait over %v", value, d, ok, MaxRetryAfter)
		}
	}
	if d, ok := parseRetryAfter("7", now); !ok || d != 7*time.Second {
		t.Errorf("parseRetryAfter(%q) = %v, %v; want 7s, true", "7", d, ok)
	}
}
