package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSubstituteAuxVar rewrites a free-text auxiliary variable (betterbird's
// MY_BUILD="esr-bbNN", which is part of the SRC_URI tag and is not a SHA). The
// sibling substituteCommitHash cannot do this because it is locked to 40-hex.
func TestSubstituteAuxVar(t *testing.T) {
	old := "esr-bb23"
	want := "esr-bb24"
	dir := t.TempDir()
	path := filepath.Join(dir, "betterbird-bin-128.7.0.ebuild")
	content := "EAPI=8\nMY_BUILD=\"" + old + "\"\n" +
		"SRC_URI=\"https://www.betterbird.eu/downloads/${PV}${MY_BUILD}/betterbird.tar.bz2\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write ebuild: %v", err)
	}

	if err := substituteAuxVar(path, "MY_BUILD", want); err != nil {
		t.Fatalf("substituteAuxVar: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ebuild: %v", err)
	}
	if strings.Contains(string(got), old) {
		t.Errorf("old MY_BUILD still present:\n%s", got)
	}
	if !strings.Contains(string(got), "MY_BUILD=\""+want+"\"") {
		t.Errorf("new MY_BUILD not written:\n%s", got)
	}
}

// TestSubstituteAuxVar_Errors covers the two refusal paths: an empty variable
// name (would otherwise produce a dangerously broad regex) and a variable that
// is absent from the ebuild (no-op write must surface as an error).
func TestSubstituteAuxVar_Errors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x-1.0.ebuild")
	if err := os.WriteFile(path, []byte("EAPI=8\nMY_BUILD=\"esr-bb23\"\n"), 0o644); err != nil {
		t.Fatalf("write ebuild: %v", err)
	}

	if err := substituteAuxVar(path, "", "v"); err == nil {
		t.Error("expected error for empty aux_var name")
	}
	if err := substituteAuxVar(path, "NOPE", "v"); err == nil {
		t.Error("expected error when aux_var not found in ebuild")
	}
}

// TestValidate_AuxVar covers the parser-agnostic validation: both fields are
// mutually required, the pattern must compile, and a regex/html parser is
// explicitly allowed (that is the whole point of the feature).
func TestValidate_AuxVar(t *testing.T) {
	t.Run("regex ok", func(t *testing.T) {
		cfg := PackageConfig{
			URL:        "https://x",
			Parser:     "regex",
			Pattern:    `Betterbird\s*([0-9.]+)esr-bb[0-9]+`,
			AuxVar:     "MY_BUILD",
			AuxPattern: `Betterbird\s*[0-9.]+(esr-bb[0-9]+)`,
		}
		if err := ValidatePackageConfig("mail-client/betterbird-bin", &cfg); err != nil {
			t.Errorf("expected valid, got %v", err)
		}
	})
	t.Run("aux_var without aux_pattern rejected", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://x", Parser: "regex", Pattern: "v(.*)", AuxVar: "MY_BUILD"}
		if err := ValidatePackageConfig("mail-client/betterbird-bin", &cfg); err == nil {
			t.Error("expected error: aux_var without aux_pattern")
		}
	})
	t.Run("aux_pattern without aux_var rejected", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://x", Parser: "regex", Pattern: "v(.*)", AuxPattern: "(x)"}
		if err := ValidatePackageConfig("mail-client/betterbird-bin", &cfg); err == nil {
			t.Error("expected error: aux_pattern without aux_var")
		}
	})
	t.Run("invalid regex rejected", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://x", Parser: "regex", Pattern: "v(.*)", AuxVar: "MY_BUILD", AuxPattern: "([0-9]+"}
		if err := ValidatePackageConfig("mail-client/betterbird-bin", &cfg); err == nil {
			t.Error("expected error: invalid aux_pattern regex")
		}
	})
}

// TestCheckPackage_AuxValue_StoredInPending verifies the end-to-end checker
// behaviour for a betterbird-style package: version and the free-text MY_BUILD
// value are captured from the SAME regex/html page and the aux value lands in
// the pending update so the applier can substitute MY_BUILD.
func TestCheckPackage_AuxValue_StoredInPending(t *testing.T) {
	pkg := "mail-client/betterbird-bin"
	currentVer := "128.6.0"

	body := "<p>Current version: Betterbird 128.7.0esr-bb24</p>"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	overlayDir := t.TempDir()
	configDir := t.TempDir()
	cfg := PackageConfig{
		Parser:     "regex",
		Pattern:    `Current version:\s*Betterbird\s*([0-9]+\.[0-9]+\.[0-9]+)esr-bb[0-9]+`,
		AuxVar:     "MY_BUILD",
		AuxPattern: `Current version:\s*Betterbird\s*[0-9.]+(esr-bb[0-9]+)`,
		URL:        server.URL,
	}
	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{pkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	createTestEbuild(t, overlayDir, pkg, currentVer)

	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if !result.HasUpdate {
		t.Fatalf("expected HasUpdate=true (128.6.0 -> 128.7.0)")
	}
	if result.UpstreamVersion != "128.7.0" {
		t.Errorf("UpstreamVersion = %q, want 128.7.0", result.UpstreamVersion)
	}

	update, ok := checker.pending.Get(pkg)
	if !ok {
		t.Fatal("package not in pending list")
	}
	if update.AuxValue != "esr-bb24" {
		t.Errorf("pending AuxValue = %q, want esr-bb24", update.AuxValue)
	}
}
