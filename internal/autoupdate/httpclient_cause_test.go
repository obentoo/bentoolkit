package autoupdate

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
)

// causeRT fails attempt i with errs[i] (the last entry repeats).
type causeRT struct {
	mu   sync.Mutex
	n    int
	errs []error
}

func (rt *causeRT) RoundTrip(*http.Request) (*http.Response, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	i := min(rt.n, len(rt.errs)-1)
	rt.n++
	return nil, rt.errs[i]
}

// causeTimeoutErr is a net.Error-shaped timeout, as a transport reports one.
type causeTimeoutErr struct{}

func (causeTimeoutErr) Error() string   { return "i/o timeout (synthetic)" }
func (causeTimeoutErr) Timeout() bool   { return true }
func (causeTimeoutErr) Temporary() bool { return true }

// TestMaxRetriesKeepsLastCause pins R4.1: when retries are exhausted, errors.Is
// finds ErrMaxRetriesExceeded AND the last attempt's own error, and
// ErrRequestTimeout exactly when THAT attempt timed out. Hostile half: a
// timeout on an earlier attempt must not be reported for a last attempt that
// failed otherwise.
func TestMaxRetriesKeepsLastCause(t *testing.T) {
	errA := errors.New("attempt 1 failed")
	errB := errors.New("attempt 2 failed")
	errC := errors.New("attempt 3 failed")
	errLast := errors.New("attempt 4 failed")

	tests := []struct {
		name        string
		errs        []error
		wantCause   error
		wantTimeout bool
	}{
		{"transport error", []error{errA, errB, errC, errLast}, errLast, false},
		{"last attempt timed out", []error{errA, errB, errC, causeTimeoutErr{}}, causeTimeoutErr{}, true},
		{"earlier attempt timed out, last did not", []error{causeTimeoutErr{}, errB, errC, errLast}, errLast, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rt := &causeRT{errs: tt.errs}
				c := NewRetryableHTTPClient()
				c.SetHTTPClient(&http.Client{Transport: rt})

				resp, err := c.GetWithContext(context.Background(), "http://cause.test/pkg")
				if resp != nil && resp.Body != nil {
					resp.Body.Close()
				}
				if rt.n != 4 {
					t.Fatalf("attempts = %d, want 4", rt.n)
				}
				if !errors.Is(err, ErrMaxRetriesExceeded) {
					t.Errorf("err = %v, want errors.Is(err, ErrMaxRetriesExceeded)", err)
				}
				if !errors.Is(err, tt.wantCause) {
					t.Errorf("err = %v, want errors.Is to find the last attempt's error %q", err, tt.wantCause)
				}
				if got := errors.Is(err, ErrRequestTimeout); got != tt.wantTimeout {
					t.Errorf("errors.Is(err, ErrRequestTimeout) = %v, want %v (err = %v)", got, tt.wantTimeout, err)
				}
			})
		})
	}
}
