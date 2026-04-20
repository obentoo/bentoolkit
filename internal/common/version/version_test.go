package version

import (
	"runtime"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestInfo verifies that Info() returns a string containing version, commit, build date, Go version, and OS/arch
func TestInfo(t *testing.T) {
	info := Info()

	// Verify the output contains all expected components
	if !strings.Contains(info, Version) {
		t.Errorf("Info() output should contain version %q, got: %s", Version, info)
	}
	if !strings.Contains(info, Commit) {
		t.Errorf("Info() output should contain commit %q, got: %s", Commit, info)
	}
	if !strings.Contains(info, BuildDate) {
		t.Errorf("Info() output should contain build date %q, got: %s", BuildDate, info)
	}
	if !strings.Contains(info, runtime.Version()) {
		t.Errorf("Info() output should contain Go version %q, got: %s", runtime.Version(), info)
	}
	expectedOSArch := runtime.GOOS + "/" + runtime.GOARCH
	if !strings.Contains(info, expectedOSArch) {
		t.Errorf("Info() output should contain OS/arch %q, got: %s", expectedOSArch, info)
	}
}

// TestShort verifies that Short() returns the version string
func TestShort(t *testing.T) {
	short := Short()
	if short != Version {
		t.Errorf("Short() = %q, want %q", short, Version)
	}
}

// TestInfoWithCustomValues verifies that Info() and Short() reflect custom values
func TestInfoWithCustomValues(t *testing.T) {
	// Save original values
	origVersion := Version
	origCommit := Commit
	origBuildDate := BuildDate

	// Restore original values after test
	defer func() {
		Version = origVersion
		Commit = origCommit
		BuildDate = origBuildDate
	}()

	// Set custom values
	Version = "1.2.3"
	Commit = "abc123def456"
	BuildDate = "2026-02-08T12:00:00Z"

	// Test Info() contains custom values
	info := Info()
	if !strings.Contains(info, "1.2.3") {
		t.Errorf("Info() should contain custom version %q, got: %s", "1.2.3", info)
	}
	if !strings.Contains(info, "abc123def456") {
		t.Errorf("Info() should contain custom commit %q, got: %s", "abc123def456", info)
	}
	if !strings.Contains(info, "2026-02-08T12:00:00Z") {
		t.Errorf("Info() should contain custom build date %q, got: %s", "2026-02-08T12:00:00Z", info)
	}

	// Test Short() returns custom version
	short := Short()
	if short != "1.2.3" {
		t.Errorf("Short() = %q, want %q", short, "1.2.3")
	}
}

// TestVersionInfoReflectsVariableValues tests Property 1: Version info reflects variable values
// **Feature: test-coverage-improvement, Property 1: Version info reflects variable values**
// **Validates: Requirements 1.3**
//
// For any set of non-empty strings assigned to Version, Commit, and BuildDate,
// calling Info() should return a string that contains all three values,
// and calling Short() should return the Version value exactly.
func TestVersionInfoReflectsVariableValues(t *testing.T) {
	// Import gopter dependencies
	properties := gopter.NewProperties(nil)

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	// Save original values
	origVersion := Version
	origCommit := Commit
	origBuildDate := BuildDate

	// Restore original values after test
	defer func() {
		Version = origVersion
		Commit = origCommit
		BuildDate = origBuildDate
	}()

	properties.Property("Info() contains all variable values and Short() returns Version",
		prop.ForAll(
			func(version, commit, buildDate string) bool {
				// Set the variables
				Version = version
				Commit = commit
				BuildDate = buildDate

				// Get the outputs
				info := Info()
				short := Short()

				// Verify Info() contains all three values
				containsVersion := strings.Contains(info, version)
				containsCommit := strings.Contains(info, commit)
				containsBuildDate := strings.Contains(info, buildDate)

				// Verify Short() equals Version exactly
				shortEqualsVersion := short == version

				return containsVersion && containsCommit && containsBuildDate && shortEqualsVersion
			},
			// Generate non-empty strings for Version, Commit, and BuildDate
			gen.RegexMatch(`^[a-zA-Z0-9._-]+$`).SuchThat(func(s string) bool { return len(s) > 0 && len(s) < 50 }),
			gen.RegexMatch(`^[a-f0-9]{7,40}$`).SuchThat(func(s string) bool { return len(s) > 0 }),
			gen.RegexMatch(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`),
		))

	properties.TestingRun(t)
}
