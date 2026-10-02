package schedule_test

import (
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/tray/schedule"
)

// almostOne is the largest float64 below 1, the top of the injected [0,1).
const almostOne = 0.9999999999999999

// TestStartupDelay_StaysWithinOneToFiveMinutes is R2.1.
func TestStartupDelay_StaysWithinOneToFiveMinutes(t *testing.T) {
	if got := schedule.StartupDelay(0); got != time.Minute {
		t.Errorf("StartupDelay(0) = %v, want 1m", got)
	}
	if got := schedule.StartupDelay(0.5); got != 3*time.Minute {
		t.Errorf("StartupDelay(0.5) = %v, want 3m", got)
	}
	prev := time.Duration(0)
	for _, r := range []float64{0, 0.1, 0.25, 0.5, 0.75, 0.9, almostOne} {
		got := schedule.StartupDelay(r)
		if got < time.Minute || got > 5*time.Minute {
			t.Errorf("StartupDelay(%v) = %v, outside [1m, 5m]", r, got)
		}
		if got < prev {
			t.Errorf("StartupDelay(%v) = %v is below StartupDelay of a smaller r (%v)", r, got, prev)
		}
		prev = got
	}
	if got := schedule.StartupDelay(almostOne); got < 4*time.Minute+59*time.Second {
		t.Errorf("StartupDelay(~1) = %v; the range does not reach 5 minutes", got)
	}
}

// TestNextInterval_VariesByTwentyPercent is R2.2.
func TestNextInterval_VariesByTwentyPercent(t *testing.T) {
	base := 6 * time.Hour
	cases := []struct {
		r    float64
		want time.Duration
	}{
		{0, 4*time.Hour + 48*time.Minute}, // 0.8 x 6h
		{0.5, 6 * time.Hour},              // 1.0 x 6h
	}
	for _, c := range cases {
		if got := schedule.NextInterval(base, c.r); got != c.want {
			t.Errorf("NextInterval(6h, %v) = %v, want %v", c.r, got, c.want)
		}
	}
	top := schedule.NextInterval(base, almostOne)
	if top > 7*time.Hour+12*time.Minute || top < 7*time.Hour+11*time.Minute {
		t.Errorf("NextInterval(6h, ~1) = %v, want just under 7h12m (1.2 x 6h)", top)
	}
	for _, r := range []float64{0, 0.3, 0.7, almostOne} {
		got := schedule.NextInterval(time.Hour, r)
		if got < 48*time.Minute || got > 72*time.Minute {
			t.Errorf("NextInterval(1h, %v) = %v, outside [48m, 72m]", r, got)
		}
	}
}

// TestBackoff_DoublesFromFiveMinutes is R2.7 below the cap.
func TestBackoff_DoublesFromFiveMinutes(t *testing.T) {
	want := map[int]time.Duration{
		1: 5 * time.Minute,
		2: 10 * time.Minute,
		3: 20 * time.Minute,
		4: 40 * time.Minute,
		9: 1280 * time.Minute, // 21h20m, the last value under the cap
	}
	for failures, w := range want {
		if got := schedule.Backoff(failures, 0); got != w {
			t.Errorf("Backoff(%d, 0) = %v, want %v", failures, got, w)
		}
	}
}

// TestBackoff_CapsAtTwentyFourHoursWithoutOverflow is the hostile half: a
// shift computed before capping overflows for large failure counts and wraps
// to zero or a negative duration, which would hammer the server.
func TestBackoff_CapsAtTwentyFourHoursWithoutOverflow(t *testing.T) {
	for _, failures := range []int{10, 11, 20, 40, 63, 64, 65, 100, 1 << 20, int(^uint(0) >> 1)} {
		if got := schedule.Backoff(failures, 0); got != 24*time.Hour {
			t.Errorf("Backoff(%d, 0) = %v, want the 24h cap", failures, got)
		}
	}
}

// TestBackoff_HonoursRetryAfter is R2.13: at least Retry-After, even when the
// computed delay is shorter, and even beyond the 24h cap; a shorter
// Retry-After does not shorten the backoff.
func TestBackoff_HonoursRetryAfter(t *testing.T) {
	cases := []struct {
		failures   int
		retryAfter time.Duration
		want       time.Duration
	}{
		{1, 2 * time.Hour, 2 * time.Hour},
		{1, time.Minute, 5 * time.Minute},
		{3, 20 * time.Minute, 20 * time.Minute},
		{20, 48 * time.Hour, 48 * time.Hour},
		{2, 0, 10 * time.Minute},
	}
	for _, c := range cases {
		if got := schedule.Backoff(c.failures, c.retryAfter); got != c.want {
			t.Errorf("Backoff(%d, %v) = %v, want %v", c.failures, c.retryAfter, got, c.want)
		}
	}
}

// TestBackoff_IsNeverShorterThanFiveMinutes: a zero or negative failure count
// (a caller bug) must not produce a zero delay.
func TestBackoff_IsNeverShorterThanFiveMinutes(t *testing.T) {
	for _, failures := range []int{0, -1} {
		if got := schedule.Backoff(failures, 0); got < 5*time.Minute {
			t.Errorf("Backoff(%d, 0) = %v, below 5 minutes", failures, got)
		}
	}
}
