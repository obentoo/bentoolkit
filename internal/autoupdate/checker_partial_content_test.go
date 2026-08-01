package autoupdate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// TestFetchContent_RejectsNonSuccessStatuses pins the statuses no request can
// make acceptable. The guard is asymmetric: 200 always counts as success, while
// 206 counts only when the request that actually reached the server carried a
// Range header. None of the statuses below is 206, so each must be rejected
// with or without a Range on the wire — the Range-dependent half of the policy
// belongs to TestFetchContent_ObservedRangeGate.
//
// 204 and 205 are the reason the check is not a 2xx range: both have an
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

// observedRanges records the Range header each request actually carried on the
// wire. The handler runs on the server's goroutine while the test reads from
// its own, so the mutex is what makes the assertions safe under -race rather
// than merely usually correct.
type observedRanges struct {
	mu   sync.Mutex
	seen []string
}

func (o *observedRanges) record(v string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, v)
}

// last returns the Range observed on the most recent request, or "" when no
// request arrived. Under a redirect the last request is the one that produced
// the 206, which is exactly the request the gate must be observing.
func (o *observedRanges) last() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.seen) == 0 {
		return ""
	}
	return o.seen[len(o.seen)-1]
}

func (o *observedRanges) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.seen)
}

// TestSentRequestDeclaresRange pins the fail-safe contract of the predicate the
// 206 gate consults (R1.4, R5.4).
//
// The predicate answers one question — did the request that actually reached
// the wire ask for a byte range? — from the response alone. Absent evidence is
// not permission: a nil response, or one whose Request the transport did not
// record, must read as "no Range declared" so an unsolicited 206 is rejected
// rather than accepted on a guess.
func TestSentRequestDeclaresRange(t *testing.T) {
	withRange := httptest.NewRequest(http.MethodGet, "http://example.com/f.json", nil)
	withRange.Header.Set("Range", "bytes=0-2097151")

	emptyRange := httptest.NewRequest(http.MethodGet, "http://example.com/f.json", nil)
	emptyRange.Header.Set("Range", "")

	tests := []struct {
		name string
		resp *http.Response
		want bool
	}{
		{
			name: "nil response declares nothing",
			resp: nil,
			want: false,
		},
		{
			name: "response without a recorded request declares nothing",
			resp: &http.Response{StatusCode: http.StatusPartialContent},
			want: false,
		},
		{
			name: "request without a Range header declares nothing",
			resp: &http.Response{
				StatusCode: http.StatusPartialContent,
				Request:    httptest.NewRequest(http.MethodGet, "http://example.com/f.json", nil),
			},
			want: false,
		},
		{
			name: "request with an empty Range value declares nothing",
			resp: &http.Response{StatusCode: http.StatusPartialContent, Request: emptyRange},
			want: false,
		},
		{
			name: "request carrying a Range declares one",
			resp: &http.Response{StatusCode: http.StatusPartialContent, Request: withRange},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sentRequestDeclaresRange(tt.resp); got != tt.want {
				t.Errorf("sentRequestDeclaresRange() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFetchContent_ObservedRangeGate asserts that the 206 gate is decided by the
// request that reached the server, not by the caller's header map (B1, B2, R1.3,
// R2.2, R2.3, R3.1, R5.1-R5.3, and assumption A2).
//
// The map and the wire disagree at three edges, and each edge is a case here:
//   - " Range " is trimmed and canonicalised by setHeader, so a Range DOES reach
//     the server and its 206 is legitimate (R5.1 / R2.2).
//   - "Range\n" is dropped outright by setHeader's CR/LF guard, so NO Range
//     reaches the server and its 206 is unsolicited — it must be rejected with
//     the status error. This is the fail-open trap: a repair that merely trims
//     the key before comparing would open the gate on a header that was never
//     sent (R5.2 / R2.3).
//   - a Range supplied through the client's default headers is applied by
//     applyHeaders just like a per-package one, so it must open the gate too
//     (R5.3 / R3.1).
//
// Every accepted case also asserts the Range the server actually observed, as
// TestFetchContent_AcceptsPartialContent does. Without that assertion a gate
// that guessed right by luck would be indistinguishable from one that observed.
func TestFetchContent_ObservedRangeGate(t *testing.T) {
	const body = `{"version":"1.2.3"}`
	const rangeValue = "bytes=0-2097151"

	tests := []struct {
		name string
		// headers is the per-package map handed to fetchContent.
		headers map[string]string
		// defaultHeaders, when set, goes through client.SetDefaultHeaders and
		// never appears in the per-package map.
		defaultHeaders map[string]string
		// viaRedirect fetches a first server that 302s to the 206 server, so the
		// response's recorded request is the redirected one (A2).
		viaRedirect bool
		wantErr     bool
		wantErrText string
		// wantSeenRange is the Range the 206 server must have observed; "" means
		// no Range must have reached it.
		wantSeenRange string
	}{
		{
			name:          "whitespace-padded Range key is trimmed on the wire and its 206 is accepted",
			headers:       map[string]string{" Range ": rangeValue},
			wantErr:       false,
			wantSeenRange: rangeValue,
		},
		{
			name:          "CRLF-bearing Range key sends no Range, so its 206 is rejected",
			headers:       map[string]string{"Range\n": rangeValue},
			wantErr:       true,
			wantErrText:   "returned status",
			wantSeenRange: "",
		},
		{
			name:           "Range from the client default headers is accepted",
			headers:        nil,
			defaultHeaders: map[string]string{"Range": rangeValue},
			wantErr:        false,
			wantSeenRange:  rangeValue,
		},
		{
			name:          "no Range from any source is rejected with the status error",
			headers:       nil,
			wantErr:       true,
			wantErrText:   "returned status",
			wantSeenRange: "",
		},
		{
			name:          "206 answering a Range that survived a redirect is accepted",
			headers:       map[string]string{"Range": rangeValue},
			viaRedirect:   true,
			wantErr:       false,
			wantSeenRange: rangeValue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observed := &observedRanges{}

			partial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed.record(r.Header.Get("Range"))
				w.Header().Set("Content-Range", "bytes 0-18/9999999")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte(body))
			}))
			defer partial.Close()

			target := partial.URL
			if tt.viaRedirect {
				redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, partial.URL, http.StatusFound)
				}))
				defer redirector.Close()
				target = redirector.URL
			}

			client := NewRetryableHTTPClient()
			client.SetHTTPClient(partial.Client())
			client.SetDelayFunc(func(time.Duration) {})
			if tt.defaultHeaders != nil {
				client.SetDefaultHeaders(tt.defaultHeaders)
			}

			checker := newContextTestChecker(t, target, WithHTTPClient(client))

			content, err := checker.fetchContent(target, tt.headers, checker.operationTimeout(nil))

			if observed.count() == 0 {
				t.Fatalf("the 206 server was never reached; the case proves nothing about the gate")
			}
			if got := observed.last(); got != tt.wantSeenRange {
				t.Errorf("server observed Range %q on the wire, want %q", got, tt.wantSeenRange)
			}

			if tt.wantErr {
				if err == nil {
					t.Fatalf("fetchContent accepted the 206, want an error")
				}
				if !strings.Contains(err.Error(), tt.wantErrText) {
					t.Errorf("error %q does not contain %q", err, tt.wantErrText)
				}
				return
			}

			if err != nil {
				t.Fatalf("fetchContent rejected a 206 answering an observed Range: %v", err)
			}
			if string(content) != body {
				t.Errorf("got body %q, want %q", content, body)
			}
		})
	}
}
