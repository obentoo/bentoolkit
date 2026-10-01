package tray

import "time"

// wallPoll is how often a SystemClock timer compares its deadline with the
// wall clock. It bounds how late a timer fires after a suspend.
const wallPoll = 30 * time.Second

// Clock is the App's source of time. After is used for every schedule, so a
// test can drive the loop with a manual clock.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the wall clock. Its timers keep wall-clock deadlines:
// Go's runtime timers count CLOCK_MONOTONIC, which stops while the machine
// is suspended, so a plain time.After(d) armed at 22:00 for "until 08:00"
// would fire hours late after a night in suspend. A SystemClock timer fires
// at most wallPoll after its wall-clock deadline, suspend or not.
//
// Each pending timer is a goroutine that exits when it fires or when Done
// is closed; a nil Done keeps abandoned timers alive until their deadline.
type SystemClock struct {
	Done <-chan struct{}
}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// After returns a channel that receives the time once the wall clock has
// reached now + d.
func (c SystemClock) After(d time.Duration) <-chan time.Time {
	return wallAfter(d, time.Now, wallPoll, c.Done)
}

// wallAfter fires when now() reaches the wall-clock deadline now()+d,
// sleeping at most poll between two comparisons. Round(0) drops the
// monotonic reading, so the comparison is on the wall clock.
func wallAfter(d time.Duration, now func() time.Time, poll time.Duration, done <-chan struct{}) <-chan time.Time {
	ch := make(chan time.Time, 1)
	deadline := now().Round(0).Add(d)
	go func() {
		for {
			t := now().Round(0)
			left := deadline.Sub(t)
			if left <= 0 {
				ch <- t
				return
			}
			timer := time.NewTimer(min(left, poll))
			select {
			case <-timer.C:
			case <-done:
				timer.Stop()
				return
			}
		}
	}()
	return ch
}
