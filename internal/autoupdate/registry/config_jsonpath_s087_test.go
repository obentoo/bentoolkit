package registry

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Story 087, sub-task 3.2: a record whose path (json parser), commit_sha_path
// or versions_path (outside its leading [*] wildcard) is not a well-formed JSON
// path is refused when the registry is loaded (R4.6) and reported by --lint
// (R4.7); every path the real registry uses, and the [*] wildcard, still load
// (R10.1, R10.3).

const s087PathPkg = "test/pkg"

// s087PathRecord is a minimal valid version-tracked json record.
func s087PathRecord() PackageConfig {
	return PackageConfig{URL: "https://example.com/api", Parser: "json", Path: "tag_name"}
}

// s087PathCommitRecord is a minimal valid commit-tracked record.
func s087PathCommitRecord() PackageConfig {
	cfg := s087PathRecord()
	cfg.Track = "commit"
	cfg.CommitSHAPath = "[0].sha"
	return cfg
}

var s087PathFields = []string{"path", "commit_sha_path", "versions_path"}

// s087FieldWord matches field as a whole word, so "path" is not found inside
// "commit_sha_path".
func s087FieldWord(field string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(field) + `($|[^A-Za-z0-9_])`)
}

// s087CheckNamesFieldAndPath asserts msg names the package, the field and the
// quoted path, and names no OTHER JSON-path field — an error blaming the wrong
// field is the collapse this guards against.
func s087CheckNamesFieldAndPath(t *testing.T, msg, pkg, field, path string) {
	t.Helper()
	if !strings.Contains(msg, pkg) {
		t.Errorf("error %q does not name the package %s", msg, pkg)
	}
	if !s087FieldWord(field).MatchString(msg) {
		t.Errorf("error %q does not name the field %s", msg, field)
	}
	if q := strconv.Quote(path); !strings.Contains(msg, q) {
		t.Errorf("error %q does not quote the path as %s", msg, q)
	}
	for _, other := range s087PathFields {
		if other != field && other != "path" && s087FieldWord(other).MatchString(msg) {
			t.Errorf("error %q for field %s names the other field %s", msg, field, other)
		}
	}
}

// TestS087_3_2_MalformedJSONPathsAreRefused is the hostile half of R4.6.
func TestS087_3_2_MalformedJSONPathsAreRefused(t *testing.T) {
	tests := []struct {
		name  string
		field string
		path  string
		cfg   func(p string) PackageConfig
	}{
		{"path trailing dot", "path", "tag_name.", func(p string) PackageConfig { c := s087PathRecord(); c.Path = p; return c }},
		{"path doubled dot", "path", "info..version", func(p string) PackageConfig { c := s087PathRecord(); c.Path = p; return c }},
		{"path field glued to an index", "path", "[0]name", func(p string) PackageConfig { c := s087PathRecord(); c.Path = p; return c }},
		{"path plus-signed index", "path", "versions[+0].version", func(p string) PackageConfig { c := s087PathRecord(); c.Path = p; return c }},
		{"path stray closing bracket", "path", "name]", func(p string) PackageConfig { c := s087PathRecord(); c.Path = p; return c }},
		{"commit_sha_path glued to an index", "commit_sha_path", "[0]sha", func(p string) PackageConfig { c := s087PathCommitRecord(); c.CommitSHAPath = p; return c }},
		{"commit_sha_path doubled dot", "commit_sha_path", "[0]..sha", func(p string) PackageConfig { c := s087PathCommitRecord(); c.CommitSHAPath = p; return c }},
		{"commit_sha_path leading dot", "commit_sha_path", ".sha", func(p string) PackageConfig { c := s087PathCommitRecord(); c.CommitSHAPath = p; return c }},
		{"commit_sha_path on a version-tracked record", "commit_sha_path", "sha.", func(p string) PackageConfig { c := s087PathRecord(); c.CommitSHAPath = p; return c }},
		{"versions_path doubled dot after the wildcard", "versions_path", "[*]..tag_name", func(p string) PackageConfig { c := s087PathRecord(); c.VersionsPath = p; return c }},
		{"versions_path trailing dot after the wildcard", "versions_path", "[*].", func(p string) PackageConfig { c := s087PathRecord(); c.VersionsPath = p; return c }},
		{"versions_path field glued to the wildcard", "versions_path", "[*]tag_name", func(p string) PackageConfig { c := s087PathRecord(); c.VersionsPath = p; return c }},
		{"versions_path second wildcard", "versions_path", "[*][*].tag_name", func(p string) PackageConfig { c := s087PathRecord(); c.VersionsPath = p; return c }},
		{"versions_path non-leading wildcard", "versions_path", "releases[*]", func(p string) PackageConfig { c := s087PathRecord(); c.VersionsPath = p; return c }},
		{"versions_path malformed inside the remainder", "versions_path", "[*].assets[+0].name", func(p string) PackageConfig { c := s087PathRecord(); c.VersionsPath = p; return c }},
		{"versions_path without wildcard", "versions_path", "releases..tag", func(p string) PackageConfig { c := s087PathRecord(); c.VersionsPath = p; return c }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg(tc.path)
			err := ValidatePackageConfig(nil, s087PathPkg, &cfg)
			if err == nil {
				t.Fatalf("ValidatePackageConfig accepted %s = %q, want it refused", tc.field, tc.path)
			}
			s087CheckNamesFieldAndPath(t, err.Error(), s087PathPkg, tc.field, tc.path)
		})
	}
}

// TestS087_3_2_WellFormedJSONPathsStillLoad is the converse (R10.1, R10.3): the
// paths the real registry uses and the [*] wildcard forms must not be refused.
func TestS087_3_2_WellFormedJSONPathsStillLoad(t *testing.T) {
	realPaths := []string{
		"tag_name", "[0].name", "info.version", "[0].commit.committer.date", "[0].sha", "version",
		"[0].committed_date", "[0].id", "dist-tags.latest", "[0].tag_name", "LATEST_THUNDERBIRD_VERSION",
		"versions[0].version", "Version", "STABLE[0].version", "Releases[0].Version", "name", "latestVersion",
		"entries[0].version", "currentRelease", "crate.max_stable_version", "commit.committer.date",
		"[0].version", "sha", "commitSha",
	}
	for _, p := range realPaths {
		t.Run("path "+p, func(t *testing.T) {
			cfg := s087PathRecord()
			cfg.Path = p
			if err := ValidatePackageConfig(nil, s087PathPkg, &cfg); err != nil {
				t.Fatalf("path %q refused: %v", p, err)
			}
		})
	}
	for _, p := range []string{"[0].id", "[0].sha", "commitSha", "sha"} {
		t.Run("commit_sha_path "+p, func(t *testing.T) {
			cfg := s087PathCommitRecord()
			cfg.CommitSHAPath = p
			if err := ValidatePackageConfig(nil, s087PathPkg, &cfg); err != nil {
				t.Fatalf("commit_sha_path %q refused: %v", p, err)
			}
		})
	}
	for _, p := range []string{"[*]", "[*].tag_name", "[*].commit.sha", "[*].assets[0].name", "releases", "data.releases", "[0].versions"} {
		t.Run("versions_path "+p, func(t *testing.T) {
			cfg := s087PathRecord()
			cfg.VersionsPath = p
			if err := ValidatePackageConfig(nil, s087PathPkg, &cfg); err != nil {
				t.Fatalf("versions_path %q refused: %v", p, err)
			}
		})
	}
}

// TestS087_3_2_LintReportsMalformedJSONPath pins R4.7 through the --lint entry
// point: the record and the field are reported, and a well-formed record is not.
func TestS087_3_2_LintReportsMalformedJSONPath(t *testing.T) {
	const pkg = "app-misc/foo"
	record := func(body string) string {
		return `["` + pkg + `"]
url = "https://example.com/api"
parser = "json"
` + body + `comments = """
foo — a test record.
"""
# END
`
	}
	tests := []struct {
		name  string
		body  string
		field string
		path  string // "" means the record is well formed
	}{
		{"malformed path", "path = 'tag_name.'\n", "path", "tag_name."},
		{"malformed commit_sha_path", "track = \"commit\"\npath = '[0].commit.committer.date'\ncommit_sha_path = '[0]sha'\nbase_from = \"none\"\n", "commit_sha_path", "[0]sha"},
		{"malformed versions_path", "path = 'tag_name'\nversions_path = '[*]..tag_name'\n", "versions_path", "[*]..tag_name"},
		{"well formed", "path = 'tag_name'\nversions_path = '[*].tag_name'\n", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issues, err := LintPackagesConfig(nil, writeRegistry(t, record(tc.body)))
			if err != nil {
				t.Fatalf("LintPackagesConfig: %v", err)
			}
			var hits []LintIssue
			for _, iss := range issues {
				if iss.Package == pkg && (strings.Contains(iss.Message, "path") && iss.Rule != LintFieldOrder) {
					hits = append(hits, iss)
				}
			}
			if tc.path == "" {
				if len(hits) != 0 {
					t.Fatalf("well-formed record reported: %v", hits)
				}
				return
			}
			if len(hits) != 1 {
				t.Fatalf("got %d path findings for [%s], want 1: all issues %v", len(hits), pkg, issues)
			}
			s087CheckNamesFieldAndPath(t, hits[0].Message, pkg, tc.field, tc.path)
		})
	}
}
