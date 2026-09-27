package overlay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// TestCompareLookupStopsOnCancel pins R7.2 end to end, with R8.13 kept: a
// compare against a real GitHub provider whose upstream never answers must end
// its in-flight lookup within 1 s of cancellation, return an error on which
// errors.Is finds context.Canceled, and still hand back a partial report marked
// Interrupted.
//
// Concurrency 1 with two packages makes the cancel land while dispatch is
// blocked on the second package's slot, so Interrupted is the loop's own
// verdict and not a timing accident.
func TestCompareLookupStopsOnCancel(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // NewGitHubProvider creates a cache dir under $HOME
	t.Setenv("GITHUB_TOKEN", "")

	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first (LIFO): unblocks a handler a failing lookup left behind

	prov, err := provider.NewGitHubProvider(&provider.RepositoryInfo{Name: "gentoo", Provider: "github", URL: "test/repo"})
	if err != nil {
		t.Fatalf("NewGitHubProvider: %v", err)
	}
	prov.BaseURL = srv.URL
	prov.CacheDir = ""

	pkgs := []PackageInfo{
		{Category: "app-misc", Package: "first", LatestVersion: "1.0"},
		{Category: "app-misc", Package: "second", LatestVersion: "1.0"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type outcome struct {
		report *CompareReport
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := CompareWithProvider(pkgs, prov, CompareOptions{Ctx: ctx, Concurrency: 1, IncludeSynced: true}) //nolint:contextcheck // ctx is injected via CompareOptions.Ctx
		done <- outcome{r, err}
	}()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the compare's first lookup never reached the provider")
	}
	cancel()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("CompareWithProvider still running 1s after cancel: the in-flight provider lookup ignores the compare context")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Errorf("err = %v, want errors.Is(err, context.Canceled)", got.err)
	}
	if got.report == nil {
		t.Fatal("report = nil, want a partial report")
	}
	if !got.report.Interrupted {
		t.Error("report.Interrupted = false, want true for a cancelled compare")
	}
	if got.report.ComparedPackages >= len(pkgs) {
		t.Errorf("ComparedPackages = %d, want a partial count (< %d)", got.report.ComparedPackages, len(pkgs))
	}
}
