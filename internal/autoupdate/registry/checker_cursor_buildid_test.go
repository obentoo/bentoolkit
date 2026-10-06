package registry

import (
	"testing"
)

// TestValidate_CommitSHAPath_VersionTrack covers the relaxed validation: a
// version-tracked package (no track="commit") may set commit_sha_path to drive
// BUILD_ID substitution, but only with parser="json".
func TestValidate_CommitSHAPath_VersionTrack(t *testing.T) {
	t.Run("json ok", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://x", Parser: "json", Path: "version", CommitSHAPath: "commitSha"}
		if err := ValidatePackageConfig(nil, "app-editors/cursor", &cfg); err != nil {
			t.Errorf("expected valid, got %v", err)
		}
	})
	t.Run("non-json rejected", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://x", Parser: "regex", Pattern: "v(.*)", CommitSHAPath: "commitSha"}
		if err := ValidatePackageConfig(nil, "app-editors/cursor", &cfg); err == nil {
			t.Error("expected error for commit_sha_path with parser!=json")
		}
	})
}
