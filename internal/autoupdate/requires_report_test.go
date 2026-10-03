package autoupdate

// Authored for story 079, sub-task 5.1 — R3.1, R3.2, R3.3, R3.4, R3.5.
//
// Written from design.md "Requirement state (check report)": CheckResult gains
// Requirements []RequirementState{Package, Version, State} with State one of
// "present" | "pending" | "missing", report.PackageResult carries the same, and
// the checker learns the ::gentoo path through WithGentooPath.
//
// Hostile halves first: a package whose name extends dev-lang/dart, and a
// pending dart entry whose version does not satisfy the pin, must NOT make the
// requirement present or pending; a dart entry keyed "dev-lang/dart@stable" or
// "dev-lang/dart:0", and a revision held only in ::gentoo, MUST.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/report"
)

const requiresReportPkg = "dev-lang/flutter"

// requiresReportFixture is a flutter record whose version page bundles a Dart
// version, an overlay at flutter-3.47.0, an empty ::gentoo tree and a pending
// list the case may seed.
type requiresReportFixture struct {
	overlay string
	gentoo  string
	pending *PendingList
	server  *httptest.Server
	cfg     PackageConfig
}

func newRequiresReportFixture(t *testing.T, withRequires bool) *requiresReportFixture {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	f := &requiresReportFixture{
		overlay: filepath.Join(tmp, "overlay"),
		gentoo:  filepath.Join(tmp, "gentoo"),
	}
	if err := os.MkdirAll(f.gentoo, 0o755); err != nil {
		t.Fatalf("mkdir gentoo: %v", err)
	}
	createTestEbuild(t, f.overlay, requiresReportPkg, "3.47.0")

	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version": "3.48.0", "dart_sdk_version": "3.14.0"}`))
	}))
	t.Cleanup(f.server.Close)

	f.cfg = PackageConfig{URL: f.server.URL + "/releases_linux.json", Parser: "json", Path: "version"}
	if withRequires {
		f.cfg.Requires = map[string]RequireSpec{
			"dev-lang/dart": {Pattern: `"version": "{version}",\s+"dart_sdk_version": "([^"]+)"`, Pin: "~"},
		}
	}

	pending, err := NewPendingList(filepath.Join(tmp, "config"))
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	f.pending = pending
	return f
}

func (f *requiresReportFixture) addPending(t *testing.T, key, newVersion string) {
	t.Helper()
	if err := f.pending.Add(PendingUpdate{Package: key, CurrentVersion: "1.0.0", NewVersion: newVersion, Status: StatusPending}); err != nil {
		t.Fatalf("pending.Add(%s): %v", key, err)
	}
}

func (f *requiresReportFixture) check(t *testing.T) *CheckResult {
	t.Helper()
	tmp := filepath.Dir(f.overlay)
	c, err := NewChecker(f.overlay,
		WithConfigDir(filepath.Join(tmp, "config")),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{requiresReportPkg: f.cfg}}),
		WithPendingList(f.pending),
		WithGentooPath(f.gentoo),
		WithRateLimiter(unlimitedRateLimiter()),
		WithHTTPClient(NewRetryableHTTPClientWithConfig(RetryConfig{MaxRetries: 0, Timeout: 5 * time.Second})),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	result, err := c.CheckPackage(requiresReportPkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("result.Error: %v", result.Error)
	}
	if !result.HasUpdate {
		t.Fatalf("HasUpdate = false for 3.47.0 -> 3.48.0; the fixture is broken")
	}
	return result
}

// TestRequiresReportState — R3.1, R3.2, R3.3: one state per requirement, on
// the requiring package's result, with the captured version.
func TestRequiresReportState(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *requiresReportFixture)
		want  string
	}{
		// Hostile: wrongly collapse — look-alikes that are not dart-3.14.0.
		{"dart-sass in overlay and pending is not dart", func(t *testing.T, f *requiresReportFixture) {
			createTestEbuild(t, f.overlay, "dev-lang/dart-sass", "3.14.0")
			f.addPending(t, "dev-lang/dart-sass", "3.14.0")
		}, "missing"},
		{"pending dart whose version does not satisfy the pin", func(t *testing.T, f *requiresReportFixture) {
			f.addPending(t, "dev-lang/dart@stable", "3.13.9")
			f.addPending(t, "dev-lang/dart@beta", "3.15.0")
		}, "missing"},
		{"prerelease of the version in the overlay", func(t *testing.T, f *requiresReportFixture) {
			createTestEbuild(t, f.overlay, "dev-lang/dart", "3.14.0_rc1")
		}, "missing"},
		// Hostile: wrongly split — differently shaped keys and trees that ARE it.
		{"pending under a label key (dev-lang/dart@stable)", func(t *testing.T, f *requiresReportFixture) {
			f.addPending(t, "dev-lang/dart@stable", "3.14.0")
		}, "pending"},
		{"pending under a slot key (dev-lang/dart:0) beside a non-matching label", func(t *testing.T, f *requiresReportFixture) {
			f.addPending(t, "dev-lang/dart@beta", "3.15.0")
			f.addPending(t, "dev-lang/dart:0", "3.14.0")
		}, "pending"},
		{"a revision held only by ::gentoo", func(t *testing.T, f *requiresReportFixture) {
			createTestEbuild(t, f.gentoo, "dev-lang/dart", "3.14.0-r1")
		}, "present"},
		// Benign.
		{"the overlay holds it", func(t *testing.T, f *requiresReportFixture) {
			createTestEbuild(t, f.overlay, "dev-lang/dart", "3.14.0")
		}, "present"},
		{"nothing anywhere", func(*testing.T, *requiresReportFixture) {}, "missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRequiresReportFixture(t, true)
			tc.setup(t, f)

			result := f.check(t)

			if len(result.Requirements) != 1 {
				t.Fatalf("Requirements = %+v, want exactly one (dev-lang/dart)", result.Requirements)
			}
			got := result.Requirements[0]
			if got.Package != "dev-lang/dart" || got.Version != "3.14.0" || got.State != tc.want {
				t.Errorf("requirement = %+v, want {Package:dev-lang/dart Version:3.14.0 State:%s}", got, tc.want)
			}
		})
	}
}

// TestRequiresReportNoRequiresNoState — R3.5 at the checker: a record without
// requires carries no requirement state at all.
func TestRequiresReportNoRequiresNoState(t *testing.T) {
	f := newRequiresReportFixture(t, false)
	createTestEbuild(t, f.overlay, "dev-lang/dart", "3.14.0")

	result := f.check(t)

	if result.Requirements != nil {
		t.Errorf("Requirements = %+v for a record without requires, want nil", result.Requirements)
	}
}

// requiresJSONKey looks a key up case-insensitively: R3.4 fixes WHAT is carried
// (package, version, state), not the spelling of the inner keys.
func requiresJSONKey(m map[string]any, key string) (any, bool) {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

// TestRequiresReportJSONCarriesRequirements — R3.4, R3.5.
//
// The element type of report.PackageResult.Requirements is not pinned here: the
// value is decoded into the model and encoded back, so a field the model does
// not carry is silently dropped by encoding/json — which is exactly the Red.
func TestRequiresReportJSONCarriesRequirements(t *testing.T) {
	const in = `{"package":"dev-lang/flutter","current_version":"3.47.0","candidate_version":"3.48.0","has_update":true,` +
		`"requirements":[{"package":"dev-lang/dart","version":"3.14.0","state":"missing"}]}`
	var fact report.PackageResult
	if err := json.Unmarshal([]byte(in), &fact); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	out, err := json.Marshal(fact)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("re-decoding: %v", err)
	}
	reqs, ok := doc["requirements"].([]any)
	if !ok || len(reqs) != 1 {
		t.Fatalf("JSON %s: want a \"requirements\" array with one entry", out)
	}
	entry, _ := reqs[0].(map[string]any)
	for key, want := range map[string]string{"package": "dev-lang/dart", "version": "3.14.0", "state": "missing"} {
		if got, ok := requiresJSONKey(entry, key); !ok || got != want {
			t.Errorf("requirements[0].%s = %v (present %v), want %q; JSON %s", key, got, ok, want, out)
		}
	}
}

// TestRequiresReportJSONEmptyRequirementsIsAKey — R3.5 as amended: the report
// package's JSON contract (TestJSONDropsNoField) makes every field a key, so a
// package without requirements carries an explicit "requirements" key rather
// than none — an absent key could not be told from an unanswered one.
func TestRequiresReportJSONEmptyRequirementsIsAKey(t *testing.T) {
	out, err := json.Marshal(report.PackageResult{Package: "dev-lang/flutter", CurrentVersion: "3.47.0", CandidateVersion: "3.48.0", HasUpdate: true, Requirements: []report.Requirement{}})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(out), `"requirements":[]`) {
		t.Errorf("a package without requirements does not carry \"requirements\":[]: %s", out)
	}
}

// TestRequiresReportPendingIndependentOfCheckOrder — R3.2 across a batch.
//
// flutter requires dev-lang/dart at the version its page bundles (3.14.0);
// dart's own record detects 3.14.0 upstream while the overlay holds
// dart-3.13.5, so this very run queues the dart bump. The pending list is
// EMPTY at the start, which is the hostile half: if flutter happens to be
// checked before dart, a state settled per package would read "missing" —
// and CheckAll walks a map, so which goes first varies from run to run. The
// design has CheckAll re-settle every "missing" after all workers joined, so
// the answer is "pending" every time. Concurrency 1 makes the order a strict
// sequence (no lucky overlap), and the run repeats with fresh state so both
// orders are exercised.
func TestRequiresReportPendingIndependentOfCheckOrder(t *testing.T) {
	flutterPage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version": "3.48.0", "dart_sdk_version": "3.14.0"}`))
	}))
	t.Cleanup(flutterPage.Close)
	dartPage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version": "3.14.0"}`))
	}))
	t.Cleanup(dartPage.Close)

	want := []RequirementState{{Package: "dev-lang/dart", Version: "3.14.0", State: "pending"}}

	for i := 0; i < 10; i++ {
		t.Run(fmt.Sprintf("run%02d", i), func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("HOME", filepath.Join(tmp, "home"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
			overlay := filepath.Join(tmp, "overlay")
			gentoo := filepath.Join(tmp, "gentoo")
			config := filepath.Join(tmp, "config")
			if err := os.MkdirAll(gentoo, 0o755); err != nil {
				t.Fatalf("mkdir gentoo: %v", err)
			}
			createTestEbuild(t, overlay, "dev-lang/flutter", "3.47.0")
			createTestEbuild(t, overlay, "dev-lang/dart", "3.13.5")

			pending, err := NewPendingList(config)
			if err != nil {
				t.Fatalf("NewPendingList: %v", err)
			}
			if got := pending.List(); len(got) != 0 {
				t.Fatalf("pending list not empty at start: %+v", got)
			}

			cfg := &PackagesConfig{Packages: map[string]PackageConfig{
				"dev-lang/flutter": {
					URL: flutterPage.URL + "/releases_linux.json", Parser: "json", Path: "version",
					Requires: map[string]RequireSpec{
						"dev-lang/dart": {Pattern: `"version": "{version}",\s+"dart_sdk_version": "([^"]+)"`, Pin: "~"},
					},
				},
				"dev-lang/dart": {URL: dartPage.URL + "/dart.json", Parser: "json", Path: "version"},
			}}
			c, err := NewChecker(overlay,
				WithConfigDir(config),
				WithPackagesConfig(cfg),
				WithPendingList(pending),
				WithGentooPath(gentoo),
				WithConcurrency(1),
				WithRateLimiter(unlimitedRateLimiter()),
				WithHTTPClient(NewRetryableHTTPClientWithConfig(RetryConfig{MaxRetries: 0, Timeout: 5 * time.Second})),
			)
			if err != nil {
				t.Fatalf("NewChecker: %v", err)
			}

			batch := c.CheckAll(true)

			if len(batch.Failures) != 0 {
				t.Fatalf("CheckAll failures: %v", batch.Failures)
			}
			var flutter, dart *CheckResult
			for i := range batch.Items {
				switch batch.Items[i].Package {
				case "dev-lang/flutter":
					flutter = &batch.Items[i]
				case "dev-lang/dart":
					dart = &batch.Items[i]
				}
			}
			if flutter == nil || dart == nil {
				t.Fatalf("CheckAll results %+v: want one for flutter and one for dart", batch.Items)
			}
			if !dart.HasUpdate || dart.UpstreamVersion != "3.14.0" {
				t.Fatalf("dart result %+v: want an update to 3.14.0; the fixture is broken", *dart)
			}
			if !reflect.DeepEqual(flutter.Requirements, want) {
				t.Errorf("flutter Requirements = %+v, want %+v (the answer must not depend on which package was checked first)", flutter.Requirements, want)
			}
			if dart.Requirements != nil {
				t.Errorf("dart Requirements = %+v for a record without requires, want nil", dart.Requirements)
			}
		})
	}
}
