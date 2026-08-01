package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
