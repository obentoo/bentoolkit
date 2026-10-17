package tray

import "time"

// SetCallTimeout shortens the deadline of the loop's D-Bus calls for a test
// and returns what restores it. Call it before the App starts and restore it
// after Run has returned.
func SetCallTimeout(d time.Duration) (restore func()) {
	old := callTimeout
	callTimeout = d
	return func() { callTimeout = old }
}

// SetCheckStopWait shortens how long a stop waits for the update check to
// leave its command runs, and returns what restores it. Call it before the
// App starts and restore it after Run has returned.
func SetCheckStopWait(d time.Duration) (restore func()) {
	old := checkStopWait
	checkStopWait = d
	return func() { checkStopWait = old }
}
