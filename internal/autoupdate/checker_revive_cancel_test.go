package autoupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// TestFindRevivableOrphansStopsOnCancel pins R7.3 end to end: the ::gentoo
// lookup FindRevivableOrphans makes must carry the context passed to that call
// (its ctx parameter), so cancelling THAT context — and nothing else — ends an
// in-flight lookup within 1 s. The package is a genuine orphan (disabled, no
// ebuild) whose upstream answers, so the scan reaches the provider lookup; the
// provider is the real GitHub provider pointed at a host that never answers.
//
// The cancellation must also surface as the lookup's cause: the scan's error
// is either errors.Is(context.Canceled) or at least carries its text in the
// soft-error note, and no candidate is reported.
func TestFindRevivableOrphansStopsOnCancel(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // NewGitHubProvider creates a cache dir under $HOME
	t.Setenv("GITHUB_TOKEN", "")

	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	gentoo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(gentoo.Close)
	t.Cleanup(func() { close(release) }) // runs first (LIFO)

	prov, err := provider.NewGitHubProvider(&provider.RepositoryInfo{Name: "gentoo", Provider: "github", URL: "test/repo"})
	if err != nil {
		t.Fatalf("NewGitHubProvider: %v", err)
	}
	prov.BaseURL = gentoo.URL
	prov.CacheDir = ""

	const pkg = "app-editors/orphan"
	upstream := jsonVersionServer(t, "2.0.0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker, err := NewChecker(t.TempDir(),
		WithConfigDir(t.TempDir()),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			pkg: {Parser: "json", Path: "version", URL: upstream.URL, Enabled: boolPtr(false)},
		}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	type outcome struct {
		got []ReviveCandidate
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		got, err := checker.FindRevivableOrphans(ctx, prov)
		done <- outcome{got, err}
	}()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the ::gentoo lookup never reached the provider")
	}
	cancel()

	var res outcome
	select {
	case res = <-done:
	case <-time.After(time.Second):
		t.Fatal("FindRevivableOrphans still running 1s after the call's context was cancelled: the ::gentoo lookup does not carry the call's ctx")
	}
	if len(res.got) != 0 {
		t.Errorf("candidates = %+v, want none from a cancelled lookup", res.got)
	}
	if res.err == nil {
		t.Fatal("err = nil, want the cancelled lookup reported")
	}
	if !errors.Is(res.err, context.Canceled) && !strings.Contains(res.err.Error(), context.Canceled.Error()) {
		t.Errorf("err = %v, want the lookup's cause to be context.Canceled", res.err)
	}
}
