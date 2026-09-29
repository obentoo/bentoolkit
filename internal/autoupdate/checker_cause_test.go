package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCheckPackageCancelledKeepsCause pins R4.2 (and the "cancelled" wording):
// a check whose fetch fails sets result.Error so errors.Is finds ErrFetchFailed
// AND the fetch's own cause. Hostile half: a fetch that fails for a reason that
// is NOT cancellation must not be reported as cancelled.
func TestCheckPackageCancelledKeepsCause(t *testing.T) {
	const pkg = "test-cat/test-pkg" // the package newRateLimitTestChecker configures

	t.Run("cancelled check", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"version":"2.0.0"}`)
		}))
		t.Cleanup(srv.Close)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		checker := newRateLimitTestChecker(t, srv.URL, WithContext(ctx))

		result, err := checker.CheckPackage(pkg, true)
		if err == nil || result == nil || result.Error == nil {
			t.Fatalf("CheckPackage on a cancelled context: result=%v err=%v; want a failed check", result, err)
		}
		if !errors.Is(result.Error, ErrFetchFailed) {
			t.Errorf("result.Error = %v, want errors.Is(ErrFetchFailed)", result.Error)
		}
		if !errors.Is(result.Error, context.Canceled) {
			t.Errorf("result.Error = %v, want errors.Is(context.Canceled)", result.Error)
		}
		if !strings.Contains(result.Error.Error(), "cancelled") {
			t.Errorf("result.Error = %q, want it to say \"cancelled\"", result.Error)
		}
	})

	t.Run("non-cancellation failure is not called cancelled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)

		checker := newRateLimitTestChecker(t, srv.URL, WithContext(context.Background()))
		result, _ := checker.CheckPackage(pkg, true)
		if result == nil || result.Error == nil {
			t.Fatalf("CheckPackage against a 404: result=%v; want a failed check", result)
		}
		if !errors.Is(result.Error, ErrFetchFailed) {
			t.Errorf("result.Error = %v, want errors.Is(ErrFetchFailed)", result.Error)
		}
		if errors.Is(result.Error, context.Canceled) || strings.Contains(result.Error.Error(), "cancelled") {
			t.Errorf("result.Error = %q reports a cancellation that did not happen", result.Error)
		}
	})
}
