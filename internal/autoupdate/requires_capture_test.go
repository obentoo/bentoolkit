package autoupdate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
)

// --check captures, for every requires entry, the version its pattern reads
// once {version} is replaced by the detected version, and records it on the
// pending entry — or holds the bump when a value cannot be captured.

// requiresCapturePage is a flutter-shaped releases file. The detected version
// is 3.48.0, and two decoys sit BEFORE its release object:
//   - "3a48b0" matches the version only if {version} is substituted as a raw
//     regex (each '.' matching any byte), and carries a Dart that is wrong;
//   - 3.47.0 carries the previous release's Dart, which is what a pattern not
//     anchored to the detected version would capture first.
const requiresCapturePage = `{
  "current_release": {"stable": "3.48.0"},
  "releases": [
    {"version": "3a48b0", "dart_sdk_version": "9.9.9"},
    {"version": "3.47.0", "dart_sdk_version": "3.13.5"},
    {"version": "3.48.0", "dart_sdk_version": "3.14.0"}
  ]
}`

const (
	requiresCapturePkg  = "dev-lang/flutter"
	requiresCaptureAtom = "dev-lang/dart"
)

// requiresCaptureServer serves body and counts the requests it answered.
func requiresCaptureServer(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// requiresCaptureChecker builds a checker over a temp overlay holding
// dev-lang/flutter-3.47.0, with the per-run body cache on and no retries.
// HOME and XDG_CONFIG_HOME point at temp dirs so nothing reaches the real
// state directory.
func requiresCaptureChecker(t *testing.T, cfg PackageConfig) *Checker {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	overlayDir := filepath.Join(tmp, "overlay")
	createTestEbuild(t, overlayDir, requiresCapturePkg, "3.47.0")
	c, err := NewChecker(overlayDir,
		WithConfigDir(filepath.Join(tmp, "config")),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{requiresCapturePkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
		WithHTTPClient(fetch.NewRetryableHTTPClientWithConfig(fetch.RetryConfig{MaxRetries: 0, Timeout: 5 * time.Second})),
		WithFetchCache(true),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return c
}

// requiresCaptureConfig is the flutter record reading its version from page.
func requiresCaptureConfig(pageURL string, requires map[string]RequireSpec) PackageConfig {
	return PackageConfig{
		URL:      pageURL,
		Parser:   "json",
		Path:     "current_release.stable",
		Requires: requires,
	}
}

// requiresCaptureFlutterSpec reads the Dart of the detected release object.
func requiresCaptureFlutterSpec() RequireSpec {
	return RequireSpec{Pattern: `"version":\s*"{version}",\s*"dart_sdk_version":\s*"([^"]+)"`, Pin: "~"}
}

// TestRequiresCaptureFromTheDetectedReleaseObject: the captured Dart is the
// one in the release object whose version was detected — not the first
// release object in the file, and not a decoy that matches only when
// {version} is read as a regex. The version page is fetched once: the
// requirement reads the body the version probe already fetched in this run.
func TestRequiresCaptureFromTheDetectedReleaseObject(t *testing.T) {
	page, hits := requiresCaptureServer(t, requiresCapturePage)
	c := requiresCaptureChecker(t, requiresCaptureConfig(page.URL,
		map[string]RequireSpec{requiresCaptureAtom: requiresCaptureFlutterSpec()}))

	result, err := c.CheckPackage(t.Context(), requiresCapturePkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("result.Error: %v", result.Error)
	}
	if result.UpstreamVersion != "3.48.0" {
		t.Fatalf("UpstreamVersion = %q, want 3.48.0", result.UpstreamVersion)
	}
	got, ok := c.pending.Get(requiresCapturePkg)
	if !ok {
		t.Fatal("no pending entry for an available update")
	}
	if v := got.Requires[requiresCaptureAtom]; v != "3.14.0" {
		switch v {
		case "9.9.9":
			t.Errorf("captured %q from the decoy object: {version} was not matched literally", v)
		case "3.13.5":
			t.Errorf("captured %q from the previous release object, not the detected one", v)
		default:
			t.Errorf("Requires[%s] = %q, want 3.14.0 (Requires = %#v)", requiresCaptureAtom, v, got.Requires)
		}
	}
	if len(got.Requires) != 1 {
		t.Errorf("Requires = %#v, want exactly the one declared atom", got.Requires)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the version page was fetched %d times in one run, want 1", n)
	}
}

// TestRequiresCaptureOnTheCachedVersionPath: when the version comes from the
// version cache (no --force), the bump still records its requirement. Both
// paths of CheckPackage that queue a bump must capture.
func TestRequiresCaptureOnTheCachedVersionPath(t *testing.T) {
	page, _ := requiresCaptureServer(t, requiresCapturePage)
	c := requiresCaptureChecker(t, requiresCaptureConfig(page.URL,
		map[string]RequireSpec{requiresCaptureAtom: requiresCaptureFlutterSpec()}))

	if _, err := c.CheckPackage(t.Context(), requiresCapturePkg, true); err != nil {
		t.Fatalf("CheckPackage (seed cache): %v", err)
	}
	if err := c.pending.Delete(requiresCapturePkg); err != nil {
		t.Fatalf("pending.Delete: %v", err)
	}

	result, err := c.CheckPackage(t.Context(), requiresCapturePkg, false)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if !result.FromCache {
		t.Fatalf("the second check did not answer from the version cache; this test cannot reach the cached path")
	}
	if result.Error != nil {
		t.Fatalf("result.Error: %v", result.Error)
	}
	got, ok := c.pending.Get(requiresCapturePkg)
	if !ok {
		t.Fatal("no pending entry on the cached path")
	}
	if v := got.Requires[requiresCaptureAtom]; v != "3.14.0" {
		t.Errorf("Requires[%s] on the cached path = %q, want 3.14.0", requiresCaptureAtom, v)
	}
}

// TestRequiresCaptureFromItsOwnURL: an entry with a url fetches it with
// {version} replaced by the detected version, without the record's credential
// header, and captures from that body rather than from the version page.
func TestRequiresCaptureFromItsOwnURL(t *testing.T) {
	// The version page also mentions a Dart; reading it instead of url would
	// capture 1.0.0.
	page, _ := requiresCaptureServer(t, `{"current_release": {"stable": "3.48.0"}, "dart": "dart=1.0.0"}`)

	var (
		mu    sync.Mutex
		path  string
		authz string
		ua    string
	)
	deps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path, authz, ua = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		mu.Unlock()
		_, _ = w.Write([]byte("engine=abc\ndart=3.14.0\n"))
	}))
	t.Cleanup(deps.Close)

	cfg := requiresCaptureConfig(page.URL, map[string]RequireSpec{
		requiresCaptureAtom: {Pattern: `dart=([0-9.]+)`, URL: deps.URL + "/sdk/v{version}/deps.txt", Pin: "~"},
	})
	cfg.Headers = map[string]string{"Authorization": "Bearer literal", "User-Agent": "bentoo-test/1"}
	c := requiresCaptureChecker(t, cfg)

	result, err := c.CheckPackage(t.Context(), requiresCapturePkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("result.Error: %v", result.Error)
	}
	got, ok := c.pending.Get(requiresCapturePkg)
	if !ok {
		t.Fatal("no pending entry for an available update")
	}
	if v := got.Requires[requiresCaptureAtom]; v != "3.14.0" {
		t.Errorf("Requires[%s] = %q, want 3.14.0 from the requirement url", requiresCaptureAtom, v)
	}
	mu.Lock()
	defer mu.Unlock()
	if path != "/sdk/v3.48.0/deps.txt" {
		t.Errorf("requirement url requested %q, want {version} substituted", path)
	}
	if authz != "" {
		t.Errorf("a credential header reached the requirement url: %q", authz)
	}
	if ua != "bentoo-test/1" {
		t.Errorf("requirement url User-Agent = %q, want the record's", ua)
	}
}

// requiresCaptureAssertHeld checks the hold outcome: no pending entry, and an
// error that wraps ErrRequirementUnresolved and names every string in names.
func requiresCaptureAssertHeld(t *testing.T, c *Checker, result *CheckResult, err error, names ...string) {
	t.Helper()
	if err != nil {
		t.Fatalf("CheckPackage returned %v; a held bump is not a failed check", err)
	}
	if got, ok := c.pending.Get(requiresCapturePkg); ok {
		t.Errorf("a held bump wrote a pending entry: %+v", got)
	}
	if result.Error == nil {
		t.Fatal("result.Error is nil; a held bump must say why")
	}
	if !errors.Is(result.Error, ErrRequirementUnresolved) {
		t.Errorf("result.Error = %v, want it to wrap ErrRequirementUnresolved", result.Error)
	}
	for _, name := range names {
		if !strings.Contains(result.Error.Error(), name) {
			t.Errorf("result.Error does not name %q: %v", name, result.Error)
		}
	}
}

// TestRequiresCaptureHeldWhenPatternMatchesNothing: no capture holds the bump.
// The second case is the hostile one — one of two entries resolves and the
// other does not. Queuing the resolved half would write a pending entry with a
// partial requirement set, which the applier could not tell from a complete one.
func TestRequiresCaptureHeldWhenPatternMatchesNothing(t *testing.T) {
	t.Run("the only entry", func(t *testing.T) {
		page, _ := requiresCaptureServer(t, `{"current_release": {"stable": "3.48.0"},
  "releases": [{"version": "3.47.0", "dart_sdk_version": "3.13.5"}, {"version": "3.48.0"}]}`)
		c := requiresCaptureChecker(t, requiresCaptureConfig(page.URL,
			map[string]RequireSpec{requiresCaptureAtom: requiresCaptureFlutterSpec()}))
		result, err := c.CheckPackage(t.Context(), requiresCapturePkg, true)
		requiresCaptureAssertHeld(t, c, result, err, requiresCapturePkg, requiresCaptureAtom)
	})

	t.Run("one of two entries", func(t *testing.T) {
		page, _ := requiresCaptureServer(t, requiresCapturePage)
		c := requiresCaptureChecker(t, requiresCaptureConfig(page.URL, map[string]RequireSpec{
			requiresCaptureAtom: requiresCaptureFlutterSpec(),
			"dev-util/flutter-engine": {
				Pattern: `"version":\s*"{version}",\s*"engine_version":\s*"([^"]+)"`,
				Pin:     "=",
			},
		}))
		result, err := c.CheckPackage(t.Context(), requiresCapturePkg, true)
		requiresCaptureAssertHeld(t, c, result, err, requiresCapturePkg, "dev-util/flutter-engine")
	})
}

// TestRequiresCaptureHeldOnAnInvalidVersion: a captured value that is not a
// Gentoo version is upstream-controlled text on its way into bash, so it holds
// the bump and is named. Well-formed versions, suffixes included, still pass.
func TestRequiresCaptureHeldOnAnInvalidVersion(t *testing.T) {
	for _, value := range []string{"main", "3.14.0 && id", "3.14.0;id", "$(id)", "v3.14.0", "3.14.0-dev.1"} {
		t.Run("held "+value, func(t *testing.T) {
			page, _ := requiresCaptureServer(t, `{"current_release": {"stable": "3.48.0"},
  "releases": [{"version": "3.48.0", "dart_sdk_version": "`+value+`"}]}`)
			c := requiresCaptureChecker(t, requiresCaptureConfig(page.URL,
				map[string]RequireSpec{requiresCaptureAtom: requiresCaptureFlutterSpec()}))
			result, err := c.CheckPackage(t.Context(), requiresCapturePkg, true)
			requiresCaptureAssertHeld(t, c, result, err, requiresCapturePkg, requiresCaptureAtom, value)
		})
	}

	for _, value := range []string{"3.14.0", "3.14.0_beta2", "3.14"} {
		t.Run("captured "+value, func(t *testing.T) {
			page, _ := requiresCaptureServer(t, `{"current_release": {"stable": "3.48.0"},
  "releases": [{"version": "3.48.0", "dart_sdk_version": "`+value+`"}]}`)
			c := requiresCaptureChecker(t, requiresCaptureConfig(page.URL,
				map[string]RequireSpec{requiresCaptureAtom: requiresCaptureFlutterSpec()}))
			result, err := c.CheckPackage(t.Context(), requiresCapturePkg, true)
			if err != nil || result.Error != nil {
				t.Fatalf("CheckPackage: err=%v result.Error=%v", err, result.Error)
			}
			got, ok := c.pending.Get(requiresCapturePkg)
			if !ok {
				t.Fatal("no pending entry for a well-formed requirement")
			}
			if got.Requires[requiresCaptureAtom] != value {
				t.Errorf("Requires[%s] = %q, want %q", requiresCaptureAtom, got.Requires[requiresCaptureAtom], value)
			}
		})
	}
}

// TestRequiresCaptureAbsentLeavesEntryBare: a record without requires queues
// its bump exactly as before — no requirement on the entry.
func TestRequiresCaptureAbsentLeavesEntryBare(t *testing.T) {
	page, hits := requiresCaptureServer(t, requiresCapturePage)
	c := requiresCaptureChecker(t, requiresCaptureConfig(page.URL, nil))

	result, err := c.CheckPackage(t.Context(), requiresCapturePkg, true)
	if err != nil || result.Error != nil {
		t.Fatalf("CheckPackage: err=%v result.Error=%v", err, result.Error)
	}
	got, ok := c.pending.Get(requiresCapturePkg)
	if !ok {
		t.Fatal("no pending entry for an available update")
	}
	if got.Requires != nil {
		t.Errorf("Requires = %#v on a record that declares none, want nil", got.Requires)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the version page was fetched %d times, want 1", n)
	}
}
