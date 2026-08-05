package autoupdate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// =============================================================================
// Story 024 / task 4.1 — the whole check path against a counting server
// (S024-R2.1, R2.5, R7.1, UB7, UB8)
// =============================================================================

// dedupCountingServer is an httptest server that counts requests PER PATH. The
// per-path counter is the only direct evidence this story can offer: the saving
// is defined as requests that were never issued, and nothing in a returned body
// can show that.
type dedupCountingServer struct {
	*httptest.Server
	counters   map[string]*atomic.Int64
	sharedHits *atomic.Int64
	auxHits    *atomic.Int64
}

// newDedupCountingServer serves the same small JSON document on two paths and
// counts the requests each one receives.
func newDedupCountingServer(t *testing.T) *dedupCountingServer {
	t.Helper()

	const doc = `{"version":"1.2.3","channel":"esr-bb24"}`
	s := &dedupCountingServer{
		sharedHits: &atomic.Int64{},
		auxHits:    &atomic.Int64{},
	}
	s.counters = map[string]*atomic.Int64{
		"/shared": s.sharedHits,
		"/aux":    s.auxHits,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if counter, ok := s.counters[r.URL.Path]; ok {
			counter.Add(1)
		}
		fmt.Fprint(w, doc)
	}))
	// s.Close, not s.Server.Close: the embedded field is promoted, and spelling
	// it out trips staticcheck's QF1008. Teardown only — no assertion here.
	t.Cleanup(s.Close)
	return s
}

// buildDedupRegistry wires a realistic registry shape: several records sharing
// one URL (the 170-way GStreamer case in miniature) plus one record that reads
// its OWN url twice — once for the version and once for its aux value (the
// claude-desktop-bin / nxplayer case, M3).
func buildDedupRegistry(t *testing.T, srv *dedupCountingServer, sharers int, opts ...CheckerOption) *Checker {
	t.Helper()

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	packages := make(map[string]PackageConfig, sharers+1)
	for i := 0; i < sharers; i++ {
		name := fmt.Sprintf("media-plugins/gst-plugin-%02d", i)
		packages[name] = PackageConfig{URL: srv.URL + "/shared", Parser: "json", Path: "version"}
		createTestEbuild(t, overlayDir, name, "0.9.0")
	}
	packages[dedupAuxPackage] = PackageConfig{
		URL:        srv.URL + "/aux",
		Parser:     "json",
		Path:       "version",
		AuxVar:     "MY_CHANNEL",
		AuxPattern: `"channel"\s*:\s*"([^"]+)"`,
	}
	createTestEbuild(t, overlayDir, dedupAuxPackage, "0.9.0")

	allOpts := append([]CheckerOption{
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: packages}),
		WithRateLimiter(unlimitedRateLimiter()),
	}, opts...)

	checker, err := NewChecker(overlayDir, allOpts...)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}
	return checker
}

// dedupAuxPackage is the record that reads its own URL for both a version and
// an auxiliary value.
const dedupAuxPackage = "app-misc/aux-reader"

// TestFetchDedupEndToEnd proves the saving on the real path — CheckAll over a
// registry — and, just as importantly, that nothing else moved: every package
// still resolves the same upstream version, and the aux value parsed out of the
// SHARED body is the same value a private fetch would have produced.
func TestFetchDedupEndToEnd(t *testing.T) {
	const sharers = 6

	// -------------------------------------------------------------------------
	// S024-R2.1 / R2.5 / UB7 — many records, one URL, one request; and a record
	// that reads its own URL twice pays for it once.
	// -------------------------------------------------------------------------
	t.Run("a deduplicated run issues one request per distinct URL", func(t *testing.T) {
		srv := newDedupCountingServer(t)
		checker := buildDedupRegistry(t, srv, sharers)

		// force = true: --force means "ignore the persisted version cache" and
		// must NOT disable deduplication — a body fetched seconds ago in this run
		// is already as fresh as --force can make it (UB7).
		batch := checker.CheckAll(true)

		if len(batch.Failures) != 0 {
			t.Fatalf("the run reported failures: %v", batch.Failures)
		}
		if got := len(batch.Items); got != sharers+1 {
			t.Fatalf("the run produced %d results, want %d", got, sharers+1)
		}

		// The defining assertions, both on the wire.
		if got := srv.sharedHits.Load(); got != 1 {
			t.Errorf("the shared URL received %d request(s) for %d records that declare it; want exactly 1 (S024-R2.1)",
				got, sharers)
		}
		if got := srv.auxHits.Load(); got != 1 {
			t.Errorf("the aux record's URL received %d request(s); want 1 — its version read and its "+
				"aux read are the same fetch (S024-R2.5)", got)
		}

		// Nothing else moved: every record still resolves the same version...
		for _, item := range batch.Items {
			if item.UpstreamVersion != "1.2.3" {
				t.Errorf("%s resolved upstream version %q, want %q", item.Package, item.UpstreamVersion, "1.2.3")
			}
			if !item.HasUpdate {
				t.Errorf("%s reported no update; 1.2.3 is newer than the 0.9.0 ebuild", item.Package)
			}
		}
		// ...and the aux value parsed out of the SHARED body is unchanged.
		update, ok := checker.pending.Get(dedupAuxPackage)
		if !ok {
			t.Fatalf("%s was not added to the pending list", dedupAuxPackage)
		}
		if update.AuxValue != "esr-bb24" {
			t.Errorf("aux value = %q, want %q — the aux read must parse the same body it always did",
				update.AuxValue, "esr-bb24")
		}
	})

	// -------------------------------------------------------------------------
	// S024-R7.1 — the comparison run: with deduplication off, the same registry
	// issues one request per read, exactly as before this story.
	// -------------------------------------------------------------------------
	t.Run("an un-deduplicated run issues one request per read", func(t *testing.T) {
		srv := newDedupCountingServer(t)
		checker := buildDedupRegistry(t, srv, sharers, WithFetchCache(false))

		batch := checker.CheckAll(true)

		if len(batch.Failures) != 0 {
			t.Fatalf("the run reported failures: %v", batch.Failures)
		}
		if got := srv.sharedHits.Load(); got != int64(sharers) {
			t.Errorf("the shared URL received %d request(s) with the cache off; want %d — one per record (S024-R7.1)",
				got, sharers)
		}
		if got := srv.auxHits.Load(); got != 2 {
			t.Errorf("the aux record's URL received %d request(s) with the cache off; want 2 — "+
				"the version read and the aux read, as before this story", got)
		}
		// The results must be identical to the deduplicated run: the flag changes
		// how many requests go out, never what the run concludes.
		for _, item := range batch.Items {
			if item.UpstreamVersion != "1.2.3" {
				t.Errorf("%s resolved upstream version %q, want %q", item.Package, item.UpstreamVersion, "1.2.3")
			}
		}
		update, ok := checker.pending.Get(dedupAuxPackage)
		if !ok {
			t.Fatalf("%s was not added to the pending list", dedupAuxPackage)
		}
		if update.AuxValue != "esr-bb24" {
			t.Errorf("aux value = %q with the cache off, want %q — the two runs must agree",
				update.AuxValue, "esr-bb24")
		}
	})
}
