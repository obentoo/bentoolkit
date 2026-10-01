package feed_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/notices"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
)

const validFeed = `{"version":"https://jsonfeed.org/version/1.1","title":"bentoo notices",` +
	`"_bentoo":{"serial":1790812800,"expires":"2026-10-31T00:00:00Z"},"items":[]}`

const ua = "bentoo-tray/1.2.3"

// seen records what the server received.
type seen struct {
	mu          sync.Mutex
	userAgent   string
	ifNoneMatch []string
	hits        int
}

func (s *seen) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userAgent = r.Header.Get("User-Agent")
	s.ifNoneMatch = r.Header.Values("If-None-Match")
	s.hits++
}

// server starts an HTTPS test server running handler and returns a fetcher for
// it.
func server(t *testing.T, handler http.HandlerFunc) (*feed.Fetcher, *seen, string) {
	t.Helper()
	s := &seen{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	url := srv.URL + "/notices.json"
	f, err := feed.New(url, srv.Client(), ua)
	if err != nil {
		t.Fatalf("New(%s): %v", url, err)
	}
	return f, s, url
}

// TestFetch_200ParsesTheFeedAndReportsTheResponse covers R2.6's success path,
// R2.10 and R13.3's inputs (status and bytes).
func TestFetch_200ParsesTheFeedAndReportsTheResponse(t *testing.T) {
	f, s, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(validFeed))
	})
	res, err := f.Fetch(context.Background(), "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.NotModified || res.Status != http.StatusOK {
		t.Errorf("Result = %+v, want a 200 that is not NotModified", res)
	}
	if res.ETag != `"v1"` {
		t.Errorf("ETag = %q, want %q", res.ETag, `"v1"`)
	}
	if res.Feed.Serial != 1790812800 {
		t.Errorf("Feed.Serial = %d, want the parsed serial", res.Feed.Serial)
	}
	if res.Bytes != int64(len(validFeed)) {
		t.Errorf("Bytes = %d, want %d", res.Bytes, len(validFeed))
	}
	if s.userAgent != ua {
		t.Errorf("User-Agent = %q, want %q", s.userAgent, ua)
	}
	if len(s.ifNoneMatch) != 0 {
		t.Errorf("If-None-Match %q sent without a stored ETag", s.ifNoneMatch)
	}
}

// TestFetch_ETagRoundTripAnd304 is R2.3 and R2.12.
func TestFetch_ETagRoundTripAnd304(t *testing.T) {
	f, s, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(validFeed))
	})
	res, err := f.Fetch(context.Background(), `"v1"`)
	if err != nil {
		t.Fatalf("Fetch with an ETag: %v", err)
	}
	if len(s.ifNoneMatch) != 1 || s.ifNoneMatch[0] != `"v1"` {
		t.Errorf("If-None-Match = %q, want exactly [\"v1\"]", s.ifNoneMatch)
	}
	if !res.NotModified || res.Status != http.StatusNotModified {
		t.Errorf("Result = %+v, want NotModified with status 304", res)
	}
	if len(res.Feed.Items) != 0 || res.Feed.Serial != 0 {
		t.Errorf("a 304 carried a feed: %+v", res.Feed)
	}
}

// TestFetch_BodyCapIsExactlyOneMiB is R2.8's size limit, both sides: exactly
// 1 MiB is read; one byte more is ErrTooLarge.
func TestFetch_BodyCapIsExactlyOneMiB(t *testing.T) {
	exact := validFeed + strings.Repeat(" ", (1<<20)-len(validFeed))
	f, _, _ := server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(exact)) })
	res, err := f.Fetch(context.Background(), "")
	if err != nil {
		t.Fatalf("a body of exactly 1 MiB was refused: %v", err)
	}
	if res.Bytes != 1<<20 {
		t.Errorf("Bytes = %d, want %d", res.Bytes, 1<<20)
	}

	over := exact + " "
	f, _, url := server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(over)) })
	_, err = f.Fetch(context.Background(), "")
	if !errors.Is(err, feed.ErrTooLarge) {
		t.Fatalf("a body of 1 MiB + 1 byte: err = %v, want ErrTooLarge", err)
	}
	if !strings.Contains(err.Error(), url) {
		t.Errorf("error %q does not name the URL", err)
	}
}

// TestFetch_OtherStatusesCarryStatusAndRetryAfter is R2.6 (status) and R2.13
// (Retry-After, in both its forms).
func TestFetch_OtherStatusesCarryStatusAndRetryAfter(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).UTC().Format(http.TimeFormat)
	cases := []struct {
		name       string
		status     int
		retryAfter string
		min, max   time.Duration
	}{
		{"500 with seconds", 500, "120", 120 * time.Second, 120 * time.Second},
		{"503 with an HTTP date", 503, future, 115 * time.Minute, 2 * time.Hour},
		{"429 with seconds", 429, "3600", time.Hour, time.Hour},
		{"404 without Retry-After", 404, "", 0, 0},
		{"204 is not a feed", 204, "", 0, 0},
		{"garbage Retry-After", 503, "soon", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := "busy"
			if c.status == 204 {
				body = "" // a 204 carries no body
			}
			f, _, url := server(t, func(w http.ResponseWriter, r *http.Request) {
				if c.retryAfter != "" {
					w.Header().Set("Retry-After", c.retryAfter)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(body))
			})
			_, err := f.Fetch(context.Background(), "")
			var se *feed.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want a *StatusError", err)
			}
			if se.Status != c.status {
				t.Errorf("Status = %d, want %d", se.Status, c.status)
			}
			if se.Bytes != int64(len(body)) {
				t.Errorf("Bytes = %d, want %d (the error body read, for the fetch log)", se.Bytes, len(body))
			}
			if se.RetryAfter < c.min || se.RetryAfter > c.max {
				t.Errorf("RetryAfter = %v, want within [%v, %v]", se.RetryAfter, c.min, c.max)
			}
			if !strings.Contains(err.Error(), url) {
				t.Errorf("error %q does not name the URL", err)
			}
		})
	}
}

// TestFetch_InvalidBodyIsAnInvalidFeed is R2.6: a 200 that is not a JSON Feed
// 1.1 document (a captive portal page, say) is an error, not an empty feed.
func TestFetch_InvalidBodyIsAnInvalidFeed(t *testing.T) {
	const portal = "<html>Log in to the Wi-Fi</html>"
	f, _, url := server(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(portal))
	})
	res, err := f.Fetch(context.Background(), "")
	if !errors.Is(err, notices.ErrInvalidFeed) {
		t.Fatalf("err = %v, want it to wrap notices.ErrInvalidFeed", err)
	}
	if res.Status != http.StatusOK || res.Bytes != int64(len(portal)) {
		t.Errorf("Result = %+v, want Status 200 and Bytes %d alongside the error (for the fetch log)", res, len(portal))
	}
	if !strings.Contains(err.Error(), url) {
		t.Errorf("error %q does not name the URL", err)
	}
}

// TestNew_RefusesNonHTTPSURLs is R2.9.
func TestNew_RefusesNonHTTPSURLs(t *testing.T) {
	for _, u := range []string{
		"http://obentoo.org/notices.json",
		"file:///etc/passwd",
		"ftp://obentoo.org/notices.json",
		"obentoo.org/notices.json",
		"",
		"https//obentoo.org/notices.json",
	} {
		_, err := feed.New(u, http.DefaultClient, ua)
		if !errors.Is(err, feed.ErrInsecureURL) {
			t.Errorf("New(%q): err = %v, want ErrInsecureURL", u, err)
			continue
		}
		if u != "" && !strings.Contains(err.Error(), u) {
			t.Errorf("New(%q): error %q does not name the URL", u, err)
		}
	}
	if _, err := feed.New("https://obentoo.org/notices.json", http.DefaultClient, ua); err != nil {
		t.Errorf("New refused an https URL: %v", err)
	}
}

// TestFetch_AStalledServerIsAbandoned is R2.8's time limit, exercised through
// both a client timeout and the caller's context (the 30 s production value is
// shortened here; the behaviour — an error instead of a hang — is the same).
func TestFetch_AStalledServerIsAbandoned(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	url := srv.URL + "/notices.json"

	client := srv.Client()
	client.Timeout = 300 * time.Millisecond
	f, err := feed.New(url, client, ua)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := f.Fetch(context.Background(), ""); err == nil {
		t.Fatal("Fetch against a stalled server returned no error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Fetch took %v with a 300ms client timeout", d)
	}

	f, err = feed.New(url, srv.Client(), ua)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = f.Fetch(ctx, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Fetch with an expiring context: err = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Fetch ignored its context for %v", d)
	}
}
