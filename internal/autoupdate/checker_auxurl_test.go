package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestAuxURLReadsTheValueFromAnotherURL pins aux_url: the version comes from
// url, the aux value from aux_url with {version} substituted (codex's
// RUSTY_V8_TAG lives in the release's Cargo.lock, not on the release page),
// and the record's credential header does not follow it there.
func TestAuxURLReadsTheValueFromAnotherURL(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"1.2.0"}`))
	}))
	t.Cleanup(page.Close)

	var (
		mu    sync.Mutex
		path  string
		authz string
	)
	lock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path, authz = r.URL.Path, r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = w.Write([]byte("[[package]]\nname = \"v8\"\nversion = \"137.1.0\"\n"))
	}))
	t.Cleanup(lock.Close)

	pkg := "dev-util/codex"
	c := mirrorChecker(t, pkg, PackageConfig{
		URL:        page.URL,
		Parser:     "json",
		Path:       "version",
		Headers:    map[string]string{"Authorization": "Bearer literal"},
		AuxVar:     "RUSTY_V8_TAG",
		AuxPattern: `name = "v8"\s+version = "([^"]+)"`,
		AuxURL:     lock.URL + "/rust-v{version}/Cargo.lock",
	})

	result, err := c.CheckPackage(t.Context(), pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("result.Error: %v", result.Error)
	}
	got, ok := c.pending.Get(pkg)
	if !ok {
		t.Fatal("no pending entry for an available update")
	}
	if got.AuxValue != "137.1.0" {
		t.Errorf("AuxValue = %q, want %q", got.AuxValue, "137.1.0")
	}
	mu.Lock()
	defer mu.Unlock()
	if path != "/rust-v1.2.0/Cargo.lock" {
		t.Errorf("aux_url requested %q, want {version} substituted", path)
	}
	if authz != "" {
		t.Errorf("a credential header reached aux_url: %q", authz)
	}
}

func TestValidateAuxURL(t *testing.T) {
	base := PackageConfig{URL: "https://example.org/v.json", Parser: "json", Path: "version",
		AuxVar: "MY_BUILD", AuxPattern: `(\d+)`}
	for _, tc := range []struct {
		name   string
		mutate func(*PackageConfig)
		ok     bool
	}{
		{"path placeholder", func(c *PackageConfig) { c.AuxURL = "https://example.org/{version}/latest.txt" }, true},
		{"query placeholder", func(c *PackageConfig) { c.AuxURL = "https://example.org/latest?v={version}" }, true},
		{"host placeholder", func(c *PackageConfig) { c.AuxURL = "https://{version}.example.org/latest.txt" }, false},
		{"not http", func(c *PackageConfig) { c.AuxURL = "file:///etc/passwd" }, false},
		{"without aux_pattern", func(c *PackageConfig) {
			c.AuxURL = "https://example.org/latest.txt"
			c.AuxVar, c.AuxPattern = "", ""
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			err := ValidatePackageConfig("app-misc/foo", &cfg)
			if (err == nil) != tc.ok {
				t.Errorf("ValidatePackageConfig(aux_url=%q) = %v, want ok=%v", cfg.AuxURL, err, tc.ok)
			}
		})
	}
}
