package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// silentServer accepts a request, reports its arrival, and never answers until
// the client goes away (or the test ends).
func silentServer(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	arrived := make(chan struct{}, 1)
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
	t.Cleanup(func() { close(release) }) // runs before srv.Close (LIFO)
	return srv, arrived
}

// assertLookupStopsOnCancel starts lookup, waits until its request is in
// flight, cancels, and requires the lookup to end within 1 s with
// context.Canceled. The 1 s bound is an assertion, not synchronisation.
func assertLookupStopsOnCancel(t *testing.T, arrived <-chan struct{}, lookup func(ctx context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- lookup(ctx) }()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the lookup's request never reached the server")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want errors.Is(err, context.Canceled)", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lookup still running 1s after its context was cancelled")
	}
}

// TestGitHubProviderLookupHonoursCancel pins R7.1/R7.2 for the GitHub provider:
// its request carries the caller's context.
func TestGitHubProviderLookupHonoursCancel(t *testing.T) {
	srv, arrived := silentServer(t)
	p, err := NewGitHubProvider(&RepositoryInfo{Name: "test", Provider: "github", URL: "test/repo"})
	if err != nil {
		t.Fatalf("NewGitHubProvider: %v", err)
	}
	p.BaseURL = srv.URL
	p.CacheDir = ""
	assertLookupStopsOnCancel(t, arrived, func(ctx context.Context) error {
		_, err := p.GetPackageVersions(ctx, "app-misc", "foo")
		return err
	})
}

// TestGitLabProviderLookupHonoursCancel pins R7.1/R7.2 for the GitLab provider.
func TestGitLabProviderLookupHonoursCancel(t *testing.T) {
	srv, arrived := silentServer(t)
	p, err := NewGitLabProvider(&RepositoryInfo{Name: "test", Provider: "gitlab", URL: "test/repo"})
	if err != nil {
		t.Fatalf("NewGitLabProvider: %v", err)
	}
	p.BaseURL = srv.URL
	p.CacheDir = ""
	assertLookupStopsOnCancel(t, arrived, func(ctx context.Context) error {
		_, err := p.GetPackageVersions(ctx, "app-misc", "foo")
		return err
	})
}

// TestGitCloneLookupHonoursDoneContext pins R7.5: a context that is already
// done returns its error without touching the repository — no clone, no
// directory created.
func TestGitCloneLookupHonoursDoneContext(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"cancelled", cancelled, context.Canceled},
		{"deadline passed", expired, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			p, err := NewGitCloneProvider(&RepositoryInfo{
				Name:     "never-cloned",
				Provider: "git",
				URL:      "https://git.invalid.example/never-cloned.git",
			})
			if err != nil {
				t.Fatalf("NewGitCloneProvider: %v", err)
			}
			_, statBefore := os.Stat(p.LocalPath)

			_, err = p.GetPackageVersions(tc.ctx, "app-misc", "foo")
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want errors.Is(err, %v)", err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(p.LocalPath, ".git")); !os.IsNotExist(statErr) {
				t.Errorf("repository at %s was touched (stat .git: %v); want no clone on a done context", p.LocalPath, statErr)
			}
			if _, statAfter := os.Stat(p.LocalPath); os.IsNotExist(statBefore) != os.IsNotExist(statAfter) {
				t.Errorf("LocalPath %s existence changed across the call; want the repository untouched", p.LocalPath)
			}
		})
	}
}
