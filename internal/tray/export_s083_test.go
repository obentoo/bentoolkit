package tray

import "time"

// SetNameRequestTimeout shortens the bound of the session bus name request for
// a test and returns what restores it. Call it before the App starts and
// restore it after Run has returned.
func SetNameRequestTimeout(d time.Duration) (restore func()) {
	old := nameRequestTimeout
	nameRequestTimeout = d
	return func() { nameRequestTimeout = old }
}
