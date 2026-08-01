package autoupdate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestFetchContent_AcceptsPartialContent asserts that a record declaring a Range
// header gets the served bytes back instead of an error. A large payload whose
// version string sits near the front (www-misc/warsaw: ~590 KB into 8.2 MB) is
// only affordable to check if the 206 answer to the partial request counts as
// success.
func TestFetchContent_AcceptsPartialContent(t *testing.T) {
	const body = `{"version":"1.2.3"}`

	var gotRange string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Range", "bytes 0-18/9999999")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	checker := newContextTestChecker(t, server.URL)

	headers := map[string]string{"Range": "bytes=0-2097151"}
	content, err := checker.fetchContent(server.URL, headers, checker.operationTimeout(nil))
	if err != nil {
		t.Fatalf("fetchContent rejected a 206 response: %v", err)
	}
	if string(content) != body {
		t.Errorf("got body %q, want %q", content, body)
	}
	// The header has to reach the wire, not merely be accepted by the config:
	// without it the server would have no reason to answer 206 at all.
	if gotRange != "bytes=0-2097151" {
		t.Errorf("server saw Range %q, want the record's declared range", gotRange)
	}
}

// TestFetchContent_RejectsNonSuccessStatuses pins the guard to 200 and 206
// exactly. 204 and 205 are the reason the check is not a 2xx range: both have an
// empty body by definition, so accepting them would swap this explicit error for
// a confusing parser failure further down.
//
// 5xx is deliberately absent: the retry layer classifies it as retryable and
// fails with its own "max retries exceeded" before the guard is ever consulted.
func TestFetchContent_RejectsNonSuccessStatuses(t *testing.T) {
	for _, status := range []int{
		http.StatusNoContent,
		http.StatusResetContent,
		http.StatusAccepted,
		http.StatusNotFound,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()

			checker := newContextTestChecker(t, server.URL)

			_, err := checker.fetchContent(server.URL, nil, checker.operationTimeout(nil))
			if err == nil {
				t.Fatalf("fetchContent accepted status %d, want an error", status)
			}
			if !strings.Contains(err.Error(), "returned status") {
				t.Errorf("error %q does not name the offending status", err)
			}
		})
	}
}

// TestFetchContent_RangeGated206 locks the corrected 206 gating and the memory
// cap on the checker's headers/Range path (B1, B2, R2.1-R2.3, R5.1-R5.3, UB4).
//
// A 206 is a valid success ONLY as the answer to a Range the checker actually
// declared — matched case-insensitively over the user-supplied headers map. An
// unsolicited 206 (no Range) must fail safe with the status error, and a server
// that ignores Range and streams past httputil.MaxBodyBytes must trip
// ErrResponseTooLarge now that GetWithHeadersContext caps the body.
//
// RED (pre-fix): checker.go accepts 206 unconditionally, so "206 without a
// Range is rejected" fails; GetWithHeadersContext is uncapped, so "server
// ignores Range and streams over the cap" fails. The two accepted-206 cases
// pass today and pin UB4 / R2.1 / R2.3 against regression.
func TestFetchContent_RangeGated206(t *testing.T) {
	const body = `{"version":"1.2.3"}`
	// 11 MiB, one byte over the 10 MiB cap.
	const oversized = 11 * 1024 * 1024

	partialHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-18/9999999")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(body))
	}

	// A server that ignores the Range request and streams a full oversized body
	// over a 206, exercising the cap on the Range path.
	oversizedPartialHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-/9999999")
		w.WriteHeader(http.StatusPartialContent)
		buf := make([]byte, 64*1024)
		written := 0
		for written < oversized {
			n := len(buf)
			if oversized-written < n {
				n = oversized - written
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			written += n
		}
	}

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		headers     map[string]string
		wantErr     bool
		wantErrText string
		wantBody    string
	}{
		{
			name:     "206 answering a declared Range is accepted",
			handler:  partialHandler,
			headers:  map[string]string{"Range": "bytes=0-2097151"},
			wantErr:  false,
			wantBody: body,
		},
		{
			name:     "206 answering a lowercase range key is accepted",
			handler:  partialHandler,
			headers:  map[string]string{"range": "bytes=0-2097151"},
			wantErr:  false,
			wantBody: body,
		},
		{
			name:        "206 with no Range header is rejected with the status error",
			handler:     partialHandler,
			headers:     nil,
			wantErr:     true,
			wantErrText: "returned status",
		},
		{
			name:        "server ignores Range and streams over the cap",
			handler:     oversizedPartialHandler,
			headers:     map[string]string{"Range": "bytes=0-2097151"},
			wantErr:     true,
			wantErrText: "", // asserted via errors.Is below
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.handler))
			defer server.Close()

			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(time.Duration) {})

			checker := newContextTestChecker(t, server.URL, WithHTTPClient(client))

			content, err := checker.fetchContent(server.URL, tt.headers, checker.operationTimeout(nil))

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("fetchContent rejected a valid 206: %v", err)
				}
				if string(content) != tt.wantBody {
					t.Errorf("got body %q, want %q", content, tt.wantBody)
				}
				return
			}

			if err == nil {
				t.Fatalf("fetchContent accepted %q, want an error", tt.name)
			}
			if tt.name == "server ignores Range and streams over the cap" {
				if !errors.Is(err, ErrResponseTooLarge) {
					t.Errorf("expected errors.Is(err, ErrResponseTooLarge), got: %v", err)
				}
				return
			}
			if tt.wantErrText != "" && !strings.Contains(err.Error(), tt.wantErrText) {
				t.Errorf("error %q does not contain %q", err, tt.wantErrText)
			}
		})
	}
}
