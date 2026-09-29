package autoupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/httputil"
)

// TestClassifyBodyReadErrorSharedSentinel pins R5.4: the autoupdate and the
// provider paths share ONE ErrResponseTooLarge, owned by httputil, so errors.Is
// matches it whichever package raised it.
//
// Hostile halves: two sentinels that merely look alike must NOT be merged (an
// error whose text is the sentinel's text, a different sentinel, a plain read
// failure), and the one sentinel must NOT be split into two values.
func TestClassifyBodyReadErrorSharedSentinel(t *testing.T) {
	t.Run("one value, reachable both ways", func(t *testing.T) {
		if !errors.Is(ErrResponseTooLarge, httputil.ErrResponseTooLarge) {
			t.Error("errors.Is(autoupdate.ErrResponseTooLarge, httputil.ErrResponseTooLarge) = false; want one shared sentinel")
		}
		// Identity, not errors.Is with its arguments reversed (staticcheck
		// SA1032): one value means the same pointer from either side.
		if httputil.ErrResponseTooLarge != ErrResponseTooLarge { //nolint:errorlint // asserts the exact, unwrapped sentinel: both names must be one pointer, and errors.Is would also pass for a second value that merely wraps the first
			t.Error("httputil.ErrResponseTooLarge != autoupdate.ErrResponseTooLarge; want one shared sentinel")
		}
	})

	t.Run("look-alikes are not the sentinel", func(t *testing.T) {
		lookAlikes := []error{
			errors.New(httputil.ErrResponseTooLarge.Error()),
			ErrRequestTimeout,
			io.ErrUnexpectedEOF,
		}
		for _, e := range lookAlikes {
			if errors.Is(classifyBodyReadError(e), httputil.ErrResponseTooLarge) {
				t.Errorf("classifyBodyReadError(%q) matches ErrResponseTooLarge; only a cap overflow may", e)
			}
		}
	})

	t.Run("an overflow classified in autoupdate matches the httputil sentinel", func(t *testing.T) {
		got := classifyBodyReadError(&http.MaxBytesError{Limit: httputil.MaxBodyBytes})
		if !errors.Is(got, httputil.ErrResponseTooLarge) {
			t.Errorf("classifyBodyReadError(*http.MaxBytesError) = %v; want it to wrap httputil.ErrResponseTooLarge", got)
		}
	})

	t.Run("an over-cap GET body read end to end", func(t *testing.T) {
		chunk := strings.Repeat("x", 64*1024)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for written := int64(0); written <= httputil.MaxBodyBytes; written += int64(len(chunk)) {
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
			}
		}))
		t.Cleanup(srv.Close)

		client := NewRetryableHTTPClient()
		resp, err := client.GetWithContext(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("GetWithContext: %v", err)
		}
		defer resp.Body.Close()
		_, readErr := io.ReadAll(resp.Body)
		if got := classifyBodyReadError(readErr); !errors.Is(got, httputil.ErrResponseTooLarge) {
			t.Errorf("reading a body over %d bytes gave %v; want it to wrap httputil.ErrResponseTooLarge", httputil.MaxBodyBytes, got)
		}
	})
}
