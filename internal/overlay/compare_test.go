package overlay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

func TestCompare(t *testing.T) {
	// Create mock GitHub server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var entries []github.ContentEntry

		switch {
		case strings.Contains(r.URL.Path, "app-misc/hello"):
			entries = []github.ContentEntry{
				{Name: "hello-1.0.ebuild", Type: "file"},
				{Name: "hello-2.0.ebuild", Type: "file"},
			}
		case strings.Contains(r.URL.Path, "app-editors/vscode"):
			entries = []github.ContentEntry{
				{Name: "vscode-1.107.1.ebuild", Type: "file"},
				{Name: "vscode-1.108.0.ebuild", Type: "file"},
			}
		case strings.Contains(r.URL.Path, "www-client/firefox"):
			entries = []github.ContentEntry{
				{Name: "firefox-129.0.ebuild", Type: "file"},
				{Name: "firefox-130.0.ebuild", Type: "file"},
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := github.NewClient()
	client.BaseURL = server.URL

	localPackages := []PackageInfo{
		{Category: "app-misc", Package: "hello", LatestVersion: "2.0"},         // up-to-date
		{Category: "app-editors", Package: "vscode", LatestVersion: "1.107.1"}, // outdated
		{Category: "www-client", Package: "firefox", LatestVersion: "128.0"},   // outdated
		{Category: "app-misc", Package: "bentoo-only", LatestVersion: "1.0"},   // not in remote
	}

	opts := CompareOptions{
		OnlyOutdated:       true,
		IncludeNotInRemote: false,
	}

	report, err := Compare(localPackages, client, opts)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}

	// Check results
	if report.TotalPackages != 4 {
		t.Errorf("Expected 4 total packages, got %d", report.TotalPackages)
	}

	if report.OutdatedCount != 2 {
		t.Errorf("Expected 2 outdated packages, got %d", report.OutdatedCount)
	}

	if report.NotInRemoteCount != 1 {
		t.Errorf("Expected 1 not-in-remote package, got %d", report.NotInRemoteCount)
	}

	// Only outdated should be in results
	if len(report.Results) != 2 {
		t.Errorf("Expected 2 results (outdated only), got %d", len(report.Results))
	}

	// Verify outdated packages
	for _, result := range report.Results {
		if result.Status != StatusOutdated {
			t.Errorf("Expected StatusOutdated, got %v for %s", result.Status, result.Package)
		}
	}
}

func TestCompareWithAllResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var entries []github.ContentEntry

		if strings.Contains(r.URL.Path, "app-misc/hello") {
			entries = []github.ContentEntry{
				{Name: "hello-1.0.ebuild", Type: "file"},
			}
		} else {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := github.NewClient()
	client.BaseURL = server.URL

	localPackages := []PackageInfo{
		{Category: "app-misc", Package: "hello", LatestVersion: "1.0"}, // up-to-date
	}

	// Test with IncludeSynced: true to include up-to-date packages
	opts := CompareOptions{
		OnlyOutdated:  false,
		IncludeSynced: true,
	}

	report, err := Compare(localPackages, client, opts)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}

	if report.UpToDateCount != 1 {
		t.Errorf("Expected 1 up-to-date package, got %d", report.UpToDateCount)
	}

	// Should include up-to-date in results when IncludeSynced is true
	if len(report.Results) != 1 {
		t.Errorf("Expected 1 result, got %d", len(report.Results))
	}
}

func TestCompareNewerInLocal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries := []github.ContentEntry{
			{Name: "hello-1.0.ebuild", Type: "file"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := github.NewClient()
	client.BaseURL = server.URL

	localPackages := []PackageInfo{
		{Category: "app-misc", Package: "hello", LatestVersion: "2.0"}, // newer locally
	}

	opts := CompareOptions{
		OnlyOutdated: false,
	}

	report, err := Compare(localPackages, client, opts)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}

	if report.NewerCount != 1 {
		t.Errorf("Expected 1 newer package, got %d", report.NewerCount)
	}

	if len(report.Results) == 1 && report.Results[0].Status != StatusNewer {
		t.Errorf("Expected StatusNewer, got %v", report.Results[0].Status)
	}
}

func TestCompareProgressCallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries := []github.ContentEntry{{Name: "hello-1.0.ebuild", Type: "file"}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := github.NewClient()
	client.BaseURL = server.URL

	localPackages := []PackageInfo{
		{Category: "app-misc", Package: "hello", LatestVersion: "1.0"},
		{Category: "app-misc", Package: "world", LatestVersion: "1.0"},
	}

	// The callback may fire concurrently from worker goroutines, so the
	// counter must be atomic. ProgressCallback's new signature is
	// func(done, total uint64).
	var callbackCount atomic.Int64
	opts := CompareOptions{
		ProgressCallback: func(done, total uint64) {
			callbackCount.Add(1)
		},
	}

	_, err := Compare(localPackages, client, opts)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}

	if got := callbackCount.Load(); got != 2 {
		t.Errorf("Expected 2 callback calls, got %d", got)
	}
}

func TestCompareStatus(t *testing.T) {
	tests := []struct {
		status   CompareStatus
		expected string
	}{
		{StatusUpToDate, "up-to-date"},
		{StatusOutdated, "outdated"},
		{StatusNewer, "newer"},
		{StatusNotInRemote, "not-in-remote"},
		{StatusError, "error"},
		{CompareStatus(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if tt.status.String() != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, tt.status.String())
			}
		})
	}
}

// Story 047, sub-task 5.5 — S047-R8.2: TestFormatReport and
// TestFormatReportEmpty stood here and were RETIRED, not lost.
//
// Both asserted over FormatReport's text, which sub-task 4.2 deletes.
//
//   - TestFormatReport checked that a row printed its package name and its two
//     versions, and that the outdated count "2" appeared somewhere in the
//     output. The row shape is pinned far harder by
//     internal/common/report/render/testdata/TestCompareGoldenPlain.golden,
//     which pins the whole PACKAGE/BENTOO/GENTOO/STATE table rather than four
//     substrings; TestCompareJSONGolden pins the same three fields on the wire.
//     The count assertion was `strings.Contains(output, "2")` over a report
//     whose versions include "128.0" and "129.0", so it could not have failed
//     and pinned nothing.
//   - TestFormatReportEmpty checked the sentence "All packages are up-to-date!".
//     That early return is deliberately gone: runCompare now sends the empty run
//     down the SAME tail as the full one, so an emptiness the renderer cannot
//     explain is no longer announced as a clean bill of health. The empty run is
//     covered by TestBuildCompareReport's "a nil report is an empty run, not a
//     crash" and "an empty list reaches the WIRE as [] and never as null" in
//     cmd/bentoo, and the empty SECTIONS' wording by
//     testdata/TestCompareGoldenPlainNoneRead.golden.
//
// The per-status counters those two exercised (OutdatedCount, NewerCount,
// UpToDateCount) keep their own tests below and in compare_verdict_test.go;
// they are asserted on the report, which is where they live.

func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"},
		{"hi", 2, "hi"},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc"},     // maxLen <= 3 returns truncated without ellipsis
		{"abcdef", 5, "abcd…"}, // maxLen > 3 uses ellipsis
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncateString(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

// TestGetStatusColor is GONE, together with the function it exercised
// (S046-R5.2, story 046 sub-task 7.1).
//
// It asserted that this package answers "what colour is `outdated`?" for every
// CompareStatus — which is the defect, stated as a contract. A library that
// picks the colour has decided how its facts look before its caller has seen
// them, and a value that arrives carrying an escape sequence cannot be exported
// to JSON, written into Markdown or logged plain.
//
// Nothing replaces it, because nothing replaced the function: Status is a value
// with a String(), TestCompareStatus above pins that word for all five, and what
// the word looks like belongs to whoever renders it.

// =============================================================================
// Task T15 / R4 — Parallel CompareWithProvider
// =============================================================================

// fakeProvider is a concurrency-safe test double for provider.Provider. Each
// GetPackageVersions call sleeps for delay (so a parallel run is measurably
// faster than a serial one), tracks the in-flight count, and returns versions
// from a per-package table.
type fakeProvider struct {
	delay    time.Duration
	versions map[string][]string // keyed by "category/pkg"
	// errs makes a package fail with something other than provider.ErrNotFound,
	// which is how a caller reaches StatusError. It is keyed like versions and
	// consulted first; a nil map (the usual case) changes nothing.
	errs map[string]error

	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	callCount   atomic.Int64
}

func (f *fakeProvider) GetPackageVersions(_ context.Context, category, pkg string) ([]string, error) {
	f.callCount.Add(1)
	cur := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	// Record the high-water mark of concurrent calls.
	for {
		prev := f.maxInFlight.Load()
		if cur <= prev || f.maxInFlight.CompareAndSwap(prev, cur) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if err, ok := f.errs[category+"/"+pkg]; ok {
		return nil, err
	}
	v, ok := f.versions[category+"/"+pkg]
	if !ok {
		return nil, provider.ErrNotFound
	}
	return v, nil
}

func (f *fakeProvider) GetName() string   { return "fake" }
func (f *fakeProvider) SupportsAPI() bool { return true }
func (f *fakeProvider) Close() error      { return nil }

// TestCompareOptions_DefaultConcurrency verifies that a non-positive
// Concurrency on CompareOptions is sanitized to DefaultCompareConcurrency:
// a zero-valued CompareOptions still drives a working parallel comparison.
func TestCompareOptions_DefaultConcurrency(t *testing.T) {
	const numPkgs = 30
	prov := &fakeProvider{
		delay:    20 * time.Millisecond,
		versions: map[string][]string{},
	}
	pkgs := make([]PackageInfo, 0, numPkgs)
	for i := 0; i < numPkgs; i++ {
		name := fmt.Sprintf("pkg%02d", i)
		// Provider returns plain version strings (matching the provider.Provider
		// contract); remote == local 1.0 -> every package is up-to-date.
		prov.versions["cat/"+name] = []string{"1.0"}
		pkgs = append(pkgs, PackageInfo{Category: "cat", Package: name, LatestVersion: "1.0"})
	}

	// Concurrency left at zero -> must be sanitized to the default (10).
	opts := CompareOptions{IncludeSynced: true}

	start := time.Now()
	report, err := CompareWithProvider(pkgs, prov, opts)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("CompareWithProvider returned an error: %v", err)
	}
	if report.ComparedPackages != numPkgs {
		t.Errorf("ComparedPackages = %d, want %d", report.ComparedPackages, numPkgs)
	}

	// A serial run would take >= 30*20ms = 600ms. With the default concurrency
	// of 10 it should finish well under that; allow generous slack for CI.
	if elapsed >= 500*time.Millisecond {
		t.Errorf("comparison took %v; expected default concurrency to parallelize (serial would be ~600ms)", elapsed)
	}
	// The high-water mark must exceed 1 (proves parallelism) and never exceed
	// the default cap.
	if hi := prov.maxInFlight.Load(); hi <= 1 {
		t.Errorf("maxInFlight = %d; expected > 1 (parallel execution)", hi)
	}
	if hi := prov.maxInFlight.Load(); hi > int64(DefaultCompareConcurrency) {
		t.Errorf("maxInFlight = %d; exceeds default concurrency cap %d", hi, DefaultCompareConcurrency)
	}
}

// TestCompareWithProvider_Parallel verifies that CompareWithProvider processes
// packages in parallel, never exceeds the requested concurrency, and produces
// a complete, correctly-counted, sorted report.
func TestCompareWithProvider_Parallel(t *testing.T) {
	const numPkgs = 40
	const concurrency = 8

	prov := &fakeProvider{
		delay:    10 * time.Millisecond,
		versions: map[string][]string{},
	}
	pkgs := make([]PackageInfo, 0, numPkgs)
	for i := 0; i < numPkgs; i++ {
		name := fmt.Sprintf("pkg%02d", i)
		// Remote has version 2.0 -> every local 1.0 package is outdated.
		prov.versions["cat/"+name] = []string{"2.0"}
		pkgs = append(pkgs, PackageInfo{Category: "cat", Package: name, LatestVersion: "1.0"})
	}

	var progressCalls atomic.Int64
	opts := CompareOptions{
		Concurrency:      concurrency,
		ProgressCallback: func(done, total uint64) { progressCalls.Add(1) },
	}

	report, err := CompareWithProvider(pkgs, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned an error: %v", err)
	}

	if report.ComparedPackages != numPkgs {
		t.Errorf("ComparedPackages = %d, want %d", report.ComparedPackages, numPkgs)
	}
	if report.OutdatedCount != numPkgs {
		t.Errorf("OutdatedCount = %d, want %d", report.OutdatedCount, numPkgs)
	}
	if len(report.Results) != numPkgs {
		t.Errorf("len(Results) = %d, want %d", len(report.Results), numPkgs)
	}
	if got := progressCalls.Load(); got != numPkgs {
		t.Errorf("progress callback fired %d times, want %d", got, numPkgs)
	}

	// The in-flight high-water mark must respect the requested cap.
	if hi := prov.maxInFlight.Load(); hi > concurrency {
		t.Errorf("maxInFlight = %d; exceeds requested concurrency %d", hi, concurrency)
	}
	// And must show genuine parallelism.
	if hi := prov.maxInFlight.Load(); hi <= 1 {
		t.Errorf("maxInFlight = %d; expected > 1 (parallel execution)", hi)
	}

	// Results must be sorted by category then package, deterministically.
	for i := 1; i < len(report.Results); i++ {
		prev, cur := report.Results[i-1], report.Results[i]
		if prev.Category > cur.Category ||
			(prev.Category == cur.Category && prev.Package > cur.Package) {
			t.Errorf("Results not sorted at index %d: %q/%q before %q/%q",
				i, prev.Category, prev.Package, cur.Category, cur.Package)
		}
	}
}

// TestCompareWithProvider_ContextCancel verifies the T9 cancellation contract
// is preserved under the parallel implementation: cancelling opts.Ctx stops
// dispatch and the partial report is returned together with the context error.
func TestCompareWithProvider_ContextCancel(t *testing.T) {
	const numPkgs = 200
	prov := &fakeProvider{
		delay:    30 * time.Millisecond,
		versions: map[string][]string{},
	}
	pkgs := make([]PackageInfo, 0, numPkgs)
	for i := 0; i < numPkgs; i++ {
		name := fmt.Sprintf("pkg%03d", i)
		prov.versions["cat/"+name] = []string{"1.0"}
		pkgs = append(pkgs, PackageInfo{Category: "cat", Package: name, LatestVersion: "1.0"})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const concurrency = 5
	opts := CompareOptions{Concurrency: concurrency, IncludeSynced: true, Ctx: ctx}

	// Cancel shortly after dispatch begins.
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()

	report, err := CompareWithProvider(pkgs, prov, opts)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// The partial report must contain strictly fewer than all packages: the
	// cancellation stopped dispatch before every package was compared.
	if report.ComparedPackages >= numPkgs {
		t.Errorf("ComparedPackages = %d; expected a partial result (< %d)", report.ComparedPackages, numPkgs)
	}
	// Results stay sorted even on the cancellation path.
	for i := 1; i < len(report.Results); i++ {
		prev, cur := report.Results[i-1], report.Results[i]
		if prev.Category > cur.Category ||
			(prev.Category == cur.Category && prev.Package > cur.Package) {
			t.Errorf("partial Results not sorted at index %d", i)
		}
	}
}

// TestCompareWithProvider_ContextCancelledUpfront verifies that a context
// cancelled before CompareWithProvider is called dispatches no work at all.
func TestCompareWithProvider_ContextCancelledUpfront(t *testing.T) {
	prov := &fakeProvider{versions: map[string][]string{}}
	pkgs := []PackageInfo{
		{Category: "cat", Package: "a", LatestVersion: "1.0"},
		{Category: "cat", Package: "b", LatestVersion: "1.0"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the call

	report, err := CompareWithProvider(pkgs, prov, CompareOptions{Ctx: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if report.ComparedPackages != 0 {
		t.Errorf("ComparedPackages = %d; expected 0 (no dispatch on pre-cancelled ctx)", report.ComparedPackages)
	}
	if got := prov.callCount.Load(); got != 0 {
		t.Errorf("provider was called %d time(s); expected 0", got)
	}
}
