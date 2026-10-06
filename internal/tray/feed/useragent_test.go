package feed_test

// The feed request names the tray by its own version, never by the bentoolkit
// release it shipped in. The release is replaced by a sentinel for these
// tests, so a User-Agent built from it cannot match the tray's version by
// accident.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	release "github.com/obentoo/bentoolkit/internal/common/version"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
	trayversion "github.com/obentoo/bentoolkit/internal/tray/version"
)

// releaseSentinel stands in for the bentoolkit release while a test runs.
const releaseSentinel = "9.8.7-release"

func setReleaseSentinel(t *testing.T) {
	t.Helper()
	old := release.Version
	t.Cleanup(func() { release.Version = old })
	release.Version = releaseSentinel
}

func TestDefaultUserAgent_IsTheTrayVersion(t *testing.T) {
	setReleaseSentinel(t)
	got := feed.DefaultUserAgent()
	if want := "bentoo-tray/" + trayversion.Version(); got != want {
		t.Errorf("DefaultUserAgent() = %q, want %q", got, want)
	}
	if strings.Contains(got, releaseSentinel) {
		t.Errorf("DefaultUserAgent() = %q carries the bentoolkit release", got)
	}
}

// A Fetcher built with no User-Agent of its own sends the default one on the
// wire.
func TestFetch_DefaultUserAgentCarriesTheTrayVersion(t *testing.T) {
	setReleaseSentinel(t)
	s := &seen{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		_, _ = w.Write([]byte(validFeed))
	}))
	t.Cleanup(srv.Close)
	f, err := feed.New(srv.URL+"/notices.json", srv.Client(), "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := f.Fetch(context.Background(), ""); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if want := "bentoo-tray/" + trayversion.Version(); s.userAgent != want {
		t.Errorf("User-Agent = %q, want %q", s.userAgent, want)
	}
}
