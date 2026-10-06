// Package schedule holds the pure timing functions of bentoo-tray: the startup
// delay, the jittered check interval and the failure backoff. Randomness is
// injected as r in [0,1) so every bound is testable.
package schedule

import "time"

const (
	minStartup   = time.Minute
	startupRange = 4 * time.Minute
	backoffBase  = 5 * time.Minute
	backoffCap   = 24 * time.Hour
)

// StartupDelay returns the delay before the first fetch, 1 to 5 minutes.
func StartupDelay(r float64) time.Duration {
	return minStartup + time.Duration(r*float64(startupRange))
}

// NextInterval returns base varied by ±20%: base × [0.8, 1.2).
func NextInterval(base time.Duration, r float64) time.Duration {
	return time.Duration(float64(base) * (0.8 + 0.4*r))
}

// Backoff returns the delay after the given number of consecutive failures:
// 5 minutes doubled per failure beyond the first, capped at 24 hours,
// and never shorter than retryAfter. The doubling
// stops at the cap before it could overflow; a count below 1 is treated as 1.
func Backoff(failures int, retryAfter time.Duration) time.Duration {
	d := backoffBase
	for i := 1; i < failures && d < backoffCap; i++ {
		d *= 2
	}
	d = min(d, backoffCap)
	return max(d, retryAfter)
}
