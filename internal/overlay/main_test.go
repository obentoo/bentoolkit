package overlay

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the overlay package test suite under goleak so a goroutine that
// outlives the tests (an unclosed httptest.Server, an unjoined manifest worker,
// a timer never stopped) fails the run instead of leaking silently. This guards
// the R3 context spine: a cancelled context must let every in-flight goroutine
// return.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
