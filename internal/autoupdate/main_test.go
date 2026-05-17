package autoupdate

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the autoupdate package test suite under goleak so a goroutine
// that outlives the tests (an unclosed httptest.Server, an unjoined worker, a
// timer never stopped) fails the run instead of leaking silently. This guards
// the R3 context spine: a cancelled context must let every in-flight HTTP/exec
// goroutine return.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
