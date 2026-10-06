package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Story 070: the repository registry honours its caller's context.
//
// The eselect fallback seeded by these tests LISTS the repositories the tests
// look up, so a lookup that wrongly falls back after a cancellation succeeds
// with fallback data instead of failing - the failure the tests catch.

// s070FallbackXML is the eselect cache: it lists gentoo and guru.
const s070FallbackXML = `<repositories>` +
	`<repo><name>gentoo</name><source type="git">https://github.com/fallback/gentoo.git</source></repo>` +
	`<repo><name>guru</name><source type="git">https://github.com/fallback/guru.git</source></repo>` +
	`</repositories>`

// s070ServedXML is what the registry host answers when it answers at all.
const s070ServedXML = `<repositories>` +
	`<repo><name>gentoo</name><source type="git">https://github.com/served/gentoo.git</source></repo>` +
	`</repositories>`

// s070CachedXML is a registry cache file: it lists only cachedrepo.
const s070CachedXML = `<repositories>` +
	`<repo><name>cachedrepo</name><source type="git">https://github.com/cached/cachedrepo.git</source></repo>` +
	`</repositories>`

// s070CancelBound is the R1.1 bound: a cancelled download returns within 1 s.
const s070CancelBound = time.Second

// s070SeedFallback points HOME at a temp dir holding an eselect cache that
// lists gentoo and guru.
func s070SeedFallback(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, eselectCachePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir eselect cache dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(s070FallbackXML), 0o600); err != nil {
		t.Fatalf("write eselect cache: %v", err)
	}
}

// s070Registry builds a registry over its own temp cache dir. client may be
// nil (the default timed client).
func s070Registry(t *testing.T, url string, client *http.Client) *RepositoryRegistry {
	t.Helper()
	dir := t.TempDir()
	return &RepositoryRegistry{
		CacheDir:   dir,
		CacheTTL:   defaultCacheTTL,
		XMLPath:    filepath.Join(dir, registryXMLFile),
		url:        url,
		httpClient: client,
	}
}

// s070BlockingServer never answers: each request waits until its own context
// ends or the test releases it. arrived receives once per request; release is
// idempotent and also runs at cleanup.
func s070BlockingServer(t *testing.T) (srv *httptest.Server, arrived <-chan struct{}, release func()) {
	t.Helper()
	got := make(chan struct{}, 16)
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-gate:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(release) // runs before srv.Close (LIFO)
	return srv, got, release
}

// s070CancelMidRequest runs call in a goroutine, cancels its context once the
// request has reached the server, and fails the test unless call returns
// within s070CancelBound of the cancellation. It returns call's error.
func s070CancelMidRequest(t *testing.T, what string, arrived <-chan struct{}, release func(), call func(ctx context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	finished := make(chan struct{})
	var err error
	go func() {
		defer close(finished)
		err = call(ctx)
	}()
	// A call that ignores its context blocks until released; release it and
	// wait so it does not write into a temp dir being removed.
	t.Cleanup(func() {
		release()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
		}
	})

	select {
	case <-arrived:
	case <-finished:
		t.Fatalf("%s returned before its request reached the registry host (err=%v)", what, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: the registry request never reached the host", what)
	}

	start := time.Now()
	cancel()
	select {
	case <-finished:
	case <-time.After(s070CancelBound):
		t.Fatalf("%s did not return within %v of its context being cancelled: the download ignores the caller's context", what, s070CancelBound)
	}
	if elapsed := time.Since(start); elapsed > s070CancelBound {
		t.Fatalf("%s returned %v after cancellation, want <= %v", what, elapsed, s070CancelBound)
	}
	return err
}

// s070AssertNoCacheFile fails when the registry wrote its cache file.
func s070AssertNoCacheFile(t *testing.T, reg *RepositoryRegistry) {
	t.Helper()
	if _, err := os.Stat(reg.XMLPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registry cache file %s exists after a cancelled download (stat err=%v), want none", reg.XMLPath, err)
	}
}

// TestS070RegistryCancelDuringDownload pins R1.1 and R1.2 for Resolve, List
// and Sync: cancelling the caller's context while the download waits for a
// response returns within 1 s an error wrapping context.Canceled, with no
// eselect fallback data and no registry cache file.
func TestS070RegistryCancelDuringDownload(t *testing.T) {
	rows := []struct {
		name string
		call func(ctx context.Context, reg *RepositoryRegistry) (any, error)
	}{
		{"Resolve", func(ctx context.Context, reg *RepositoryRegistry) (any, error) {
			info, err := reg.Resolve(ctx, "gentoo")
			if info == nil {
				return nil, err
			}
			return info, err
		}},
		{"List", func(ctx context.Context, reg *RepositoryRegistry) (any, error) {
			names, err := reg.List(ctx)
			if len(names) == 0 {
				return nil, err
			}
			return names, err
		}},
		{"Sync", func(ctx context.Context, reg *RepositoryRegistry) (any, error) {
			return nil, reg.Sync(ctx)
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			s070SeedFallback(t)
			srv, arrived, release := s070BlockingServer(t)
			reg := s070Registry(t, srv.URL, nil)

			var got any
			err := s070CancelMidRequest(t, row.name, arrived, release, func(ctx context.Context) error {
				var err error
				got, err = row.call(ctx, reg)
				return err
			})

			if err == nil {
				t.Fatalf("%s returned no error after cancellation (got %v): the eselect fallback answered an interrupted lookup", row.name, got)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s error = %v, want one wrapping context.Canceled", row.name, err)
			}
			if errors.Is(err, ErrRepositoryNotFound) {
				t.Fatalf("%s error = %v, must not read as a missing repository", row.name, err)
			}
			if got != nil {
				t.Fatalf("%s returned data %v after cancellation, want none (no eselect fallback)", row.name, got)
			}
			s070AssertNoCacheFile(t, reg)
		})
	}
}

// s070RoundTripper returns err for every request without contacting a host.
type s070RoundTripper struct{ err error }

func (rt s070RoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, rt.err }

// TestS070RegistryLiveContextKeepsFallback is the hostile half of R1.2 (R1.3,
// R6.1): while the CALLER's context is live, a download failure keeps the
// eselect fallback - even when the transport error itself wraps
// context.Canceled or context.DeadlineExceeded, and when the client's own
// timeout ends the download. "Interrupted" is the caller's context, not the
// shape of the transport error.
func TestS070RegistryLiveContextKeepsFallback(t *testing.T) {
	rows := []struct {
		name   string
		client func(t *testing.T) (*http.Client, string)
	}{
		{"transport error wraps context.Canceled", func(t *testing.T) (*http.Client, string) {
			return &http.Client{Transport: s070RoundTripper{fmt.Errorf("proxy dropped the tunnel: %w", context.Canceled)}}, "https://registry.invalid/repositories.xml"
		}},
		{"transport error wraps context.DeadlineExceeded", func(t *testing.T) (*http.Client, string) {
			return &http.Client{Transport: s070RoundTripper{fmt.Errorf("dial: %w", context.DeadlineExceeded)}}, "https://registry.invalid/repositories.xml"
		}},
		{"client timeout against a host that never answers", func(t *testing.T) (*http.Client, string) {
			srv, _, _ := s070BlockingServer(t)
			return &http.Client{Timeout: 100 * time.Millisecond}, srv.URL
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			s070SeedFallback(t)
			client, url := row.client(t)
			reg := s070Registry(t, url, client)

			info, err := reg.Resolve(context.Background(), "gentoo")
			if err != nil {
				t.Fatalf("Resolve with a live context = %v, want the eselect fallback's gentoo", err)
			}
			if info == nil || info.URL != "fallback/gentoo" {
				t.Fatalf("Resolve with a live context = %+v, want URL fallback/gentoo from the eselect cache", info)
			}

			names, err := reg.List(context.Background())
			if err != nil {
				t.Fatalf("List with a live context = %v, want the eselect fallback's names", err)
			}
			if want := []string{"gentoo", "guru"}; !slices.Equal(names, want) {
				t.Fatalf("List with a live context = %v, want %v from the eselect cache", names, want)
			}
			s070AssertNoCacheFile(t, reg)
		})
	}
}

// s070CountingServer answers s070ServedXML and counts requests.
func s070CountingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(s070ServedXML))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// s070WriteCache writes the registry cache file with content and mtime age.
func s070WriteCache(t *testing.T, reg *RepositoryRegistry, content string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(reg.XMLPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write registry cache: %v", err)
	}
	mtime := time.Now().Add(-age)
	if err := os.Chtimes(reg.XMLPath, mtime, mtime); err != nil {
		t.Fatalf("age registry cache: %v", err)
	}
}

// s070AssertCacheIs fails unless the registry cache file still holds want.
func s070AssertCacheIs(t *testing.T, reg *RepositoryRegistry, want string) {
	t.Helper()
	got, err := os.ReadFile(reg.XMLPath)
	if err != nil {
		t.Fatalf("read registry cache: %v", err)
	}
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("registry cache = %q, want it untouched: %q", got, want)
	}
}

// TestS070RegistryCancelledContextStaleCache pins R1.2 for a context that is
// already cancelled when the lookup starts and a cache older than its TTL:
// the lookup fails with context.Canceled, takes no eselect fallback, does not
// download, and leaves the stale cache as it was.
func TestS070RegistryCancelledContextStaleCache(t *testing.T) {
	s070SeedFallback(t)
	srv, _ := s070CountingServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("Resolve", func(t *testing.T) {
		reg := s070Registry(t, srv.URL, nil)
		s070WriteCache(t, reg, s070CachedXML, 2*defaultCacheTTL)
		info, err := reg.Resolve(ctx, "gentoo")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Resolve(cancelled ctx) = (%+v, %v), want an error wrapping context.Canceled", info, err)
		}
		if info != nil {
			t.Fatalf("Resolve(cancelled ctx) returned %+v, want no data", info)
		}
		s070AssertCacheIs(t, reg, s070CachedXML)
	})
	t.Run("List", func(t *testing.T) {
		reg := s070Registry(t, srv.URL, nil)
		s070WriteCache(t, reg, s070CachedXML, 2*defaultCacheTTL)
		names, err := reg.List(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("List(cancelled ctx) = (%v, %v), want an error wrapping context.Canceled", names, err)
		}
		if len(names) != 0 {
			t.Fatalf("List(cancelled ctx) returned %v, want no names", names)
		}
		s070AssertCacheIs(t, reg, s070CachedXML)
	})
}

// TestS070RegistryCancelledContextFreshCache is the converse (R6.3): a cache
// younger than its TTL still answers Resolve and List under a cancelled
// context, with no request - cancellation does not hide data already on disk.
// Sync still forces a download, so under a cancelled context it fails with
// context.Canceled and leaves the fresh cache untouched.
func TestS070RegistryCancelledContextFreshCache(t *testing.T) {
	s070SeedFallback(t)
	srv, hits := s070CountingServer(t)
	reg := s070Registry(t, srv.URL, nil)
	s070WriteCache(t, reg, s070CachedXML, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	info, err := reg.Resolve(ctx, "cachedrepo")
	if err != nil {
		t.Fatalf("Resolve(cancelled ctx) with a fresh cache = %v, want the cached cachedrepo", err)
	}
	if info == nil || info.URL != "cached/cachedrepo" {
		t.Fatalf("Resolve(cancelled ctx) with a fresh cache = %+v, want URL cached/cachedrepo", info)
	}
	names, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List(cancelled ctx) with a fresh cache = %v, want the cached names", err)
	}
	if want := []string{"cachedrepo"}; !slices.Equal(names, want) {
		t.Fatalf("List(cancelled ctx) with a fresh cache = %v, want %v", names, want)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("fresh-cache lookups made %d request(s), want 0", n)
	}

	if err := reg.Sync(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync(cancelled ctx) = %v, want an error wrapping context.Canceled", err)
	}
	s070AssertCacheIs(t, reg, s070CachedXML)
}

// TestS070FactoryPassesContextToRegistry pins R1.4: ResolveRepository and
// ListAvailableRepositories hand their context to the registry, so a
// cancellation mid-download surfaces context.Canceled from ResolveRepository
// (not ErrRepositoryNotFound) and leaves ListAvailableRepositories with only
// the configured part - both within 1 s, with no eselect fallback names.
func TestS070FactoryPassesContextToRegistry(t *testing.T) {
	configRepos := map[string]*RepositoryInfo{
		"local": {Name: "local", Provider: "git", URL: "https://example.invalid/local.git", Branch: "master"},
	}

	t.Run("ResolveRepository", func(t *testing.T) {
		s070SeedFallback(t)
		srv, arrived, release := s070BlockingServer(t)
		reg := s070Registry(t, srv.URL, nil)
		var info *RepositoryInfo
		err := s070CancelMidRequest(t, "ResolveRepository", arrived, release, func(ctx context.Context) error {
			var err error
			info, err = ResolveRepository(ctx, "gentoo", configRepos, reg)
			return err
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ResolveRepository after cancellation = (%+v, %v), want an error wrapping context.Canceled", info, err)
		}
		if errors.Is(err, ErrRepositoryNotFound) {
			t.Fatalf("ResolveRepository after cancellation = %v, must not read as a missing repository", err)
		}
		if info != nil {
			t.Fatalf("ResolveRepository after cancellation returned %+v, want none", info)
		}
		s070AssertNoCacheFile(t, reg)
	})

	t.Run("ListAvailableRepositories", func(t *testing.T) {
		s070SeedFallback(t)
		srv, arrived, release := s070BlockingServer(t)
		reg := s070Registry(t, srv.URL, nil)
		var names []string
		_ = s070CancelMidRequest(t, "ListAvailableRepositories", arrived, release, func(ctx context.Context) error {
			names = ListAvailableRepositories(ctx, configRepos, reg)
			return nil
		})
		if want := []string{"local"}; !slices.Equal(names, want) {
			t.Fatalf("ListAvailableRepositories after cancellation = %v, want only the configured %v (no eselect fallback names)", names, want)
		}
		s070AssertNoCacheFile(t, reg)
	})

	// Hostile half (R6.4): a configured repository still wins under a
	// cancelled context, without touching the registry.
	t.Run("configured repository under a cancelled context", func(t *testing.T) {
		s070SeedFallback(t)
		srv, hits := s070CountingServer(t)
		reg := s070Registry(t, srv.URL, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		info, err := ResolveRepository(ctx, "local", configRepos, reg)
		if err != nil {
			t.Fatalf("ResolveRepository(cancelled ctx, configured local) = %v, want the configured repository", err)
		}
		if info == nil || info.URL != "https://example.invalid/local.git" {
			t.Fatalf("ResolveRepository(cancelled ctx, configured local) = %+v, want the configured entry", info)
		}
		if n := hits.Load(); n != 0 {
			t.Fatalf("a configured lookup made %d registry request(s), want 0", n)
		}
	})
}
