package registry

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// These tests pin the CURRENT result of ValidatePackageConfig for the branches
// the rest of the suite does not reach: the exact error text, and which error
// wins when a record is wrong in several ways at once. They describe what the
// validator does today, so a restructuring that changes either one fails here.

const s086Pkg = "test/pkg"

// s086JSON is a minimal valid version-tracked record.
func s086JSON() PackageConfig {
	return PackageConfig{URL: "https://example.com/api", Parser: "json", Path: "tag_name"}
}

// s086Commit is a minimal valid commit-tracked record.
func s086Commit() PackageConfig {
	cfg := s086JSON()
	cfg.Track = "commit"
	cfg.CommitSHAPath = "sha"
	return cfg
}

func TestS086ValidatePackageConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  func() PackageConfig
		want string // exact error text; "" means the record is accepted
	}{
		// revision
		{"negative revision", func() PackageConfig { c := s086JSON(); c.Revision = -1; return c },
			`package test/pkg: revision must be >= 0, got -1`},

		// track and its dependencies
		{"invalid track", func() PackageConfig { c := s086JSON(); c.Track = "tag"; return c },
			`package test/pkg: invalid track value: must be '' or 'commit', got "tag"`},
		{"track commit with regex parser", func() PackageConfig {
			c := s086Commit()
			c.Parser, c.Path, c.Pattern = "regex", "", `v(\d+)`
			return c
		}, `package test/pkg: track="commit" requires parser="json"`},
		{"track commit without commit_sha_path", func() PackageConfig { c := s086Commit(); c.CommitSHAPath = ""; return c },
			`package test/pkg: track="commit" requires commit_sha_path`},
		{"commit_sha_path on a version-tracked regex record", func() PackageConfig {
			c := s086JSON()
			c.Parser, c.Path, c.Pattern, c.CommitSHAPath = "regex", "", `v(\d+)`, "sha"
			return c
		}, `package test/pkg: commit_sha_path requires parser="json"`},
		{"commit_sha_path on a version-tracked json record", func() PackageConfig { c := s086JSON(); c.CommitSHAPath = "sha"; return c }, ""},
		{"commit_version_pattern without commit_message_path", func() PackageConfig {
			c := s086Commit()
			c.CommitVersionPattern = `v(\d+)`
			return c
		}, `package test/pkg: commit_version_pattern requires commit_message_path`},
		{"commit_version_pattern does not compile", func() PackageConfig {
			c := s086Commit()
			c.CommitVersionPattern, c.CommitMessagePath = "(", "commit.message"
			return c
		}, "package test/pkg: invalid commit_version_pattern \"(\": error parsing regexp: missing closing ): `(`"},
		{"commit_version_pattern and commit_message_path on a commit record", func() PackageConfig {
			c := s086Commit()
			c.CommitVersionPattern, c.CommitMessagePath = `v(\d+)`, "commit.message"
			return c
		}, ""},

		// base_from = "" (not declared)
		{"base_url without base_from", func() PackageConfig { c := s086Commit(); c.BaseURL = "https://example.com/f"; return c },
			`package test/pkg: base_url/base_pattern require base_from`},
		{"base_pattern without base_from", func() PackageConfig { c := s086Commit(); c.BasePattern = `(\d+)`; return c },
			`package test/pkg: base_url/base_pattern require base_from`},
		{"base_tag_pattern without base_from", func() PackageConfig { c := s086Commit(); c.BaseTagPattern = `v(\d+)`; return c },
			`package test/pkg: base_tag_pattern requires base_from="tag"`},

		// base_from = "file"
		{"base_from file without track commit", func() PackageConfig {
			c := s086JSON()
			c.BaseFrom, c.BaseURL, c.BasePattern = "file", "https://example.com/f", `(\d+)`
			return c
		}, `package test/pkg: base_from requires track="commit"`},
		{"base_from file without base_pattern", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL = "file", "https://example.com/f"
			return c
		}, `package test/pkg: base_from="file" requires base_url and base_pattern`},
		{"base_from file without base_url", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BasePattern = "file", `(\d+)`
			return c
		}, `package test/pkg: base_from="file" requires base_url and base_pattern`},
		{"base_from file with uncompilable base_pattern", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BasePattern = "file", "https://example.com/f", "("
			return c
		}, "package test/pkg: invalid base_pattern \"(\": error parsing regexp: missing closing ): `(`"},
		{"base_from file with no capture group", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BasePattern = "file", "https://example.com/f", `\d+`
			return c
		}, `package test/pkg: base_pattern "\\d+" must have exactly one capture group, got 0`},
		{"base_from file with two capture groups", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BasePattern = "file", "https://example.com/f", `(\d+)\.(\d+)`
			return c
		}, `package test/pkg: base_pattern "(\\d+)\\.(\\d+)" must have exactly one capture group, got 2`},
		{"base_from file with base_tag_pattern", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BasePattern, c.BaseTagPattern = "file", "https://example.com/f", `(\d+)`, `v(\d+)`
			return c
		}, `package test/pkg: base_tag_pattern requires base_from="tag"`},
		{"base_from file valid", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BasePattern = "file", "https://example.com/f", `(\d+)`
			return c
		}, ""},

		// base_from = "tag"
		{"base_from tag without track commit", func() PackageConfig {
			c := s086JSON()
			c.BaseFrom, c.BaseURL, c.BaseTagPattern = "tag", "https://example.com/t", `v(\d+)`
			return c
		}, `package test/pkg: base_from requires track="commit"`},
		{"base_from tag without base_tag_pattern", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL = "tag", "https://example.com/t"
			return c
		}, `package test/pkg: base_from="tag" requires base_url and base_tag_pattern`},
		{"base_from tag with base_pattern instead of base_tag_pattern", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BasePattern = "tag", "https://example.com/t", `v(\d+)`
			return c
		}, `package test/pkg: base_from="tag" requires base_url and base_tag_pattern`},
		{"base_from tag with uncompilable base_tag_pattern", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BaseTagPattern = "tag", "https://example.com/t", "("
			return c
		}, "package test/pkg: invalid base_tag_pattern \"(\": error parsing regexp: missing closing ): `(`"},
		{"base_from tag with two capture groups", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BaseTagPattern = "tag", "https://example.com/t", `(v)(\d+)`
			return c
		}, `package test/pkg: base_tag_pattern "(v)(\\d+)" must have exactly one capture group, got 2`},
		{"base_from tag valid", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BaseTagPattern = "tag", "https://example.com/t", `v(\d+)`
			return c
		}, ""},

		// base_from = "commit_message"
		{"base_from commit_message without track commit", func() PackageConfig {
			c := s086JSON()
			c.BaseFrom = "commit_message"
			return c
		}, `package test/pkg: base_from requires track="commit"`},
		{"base_from commit_message without commit_version_pattern", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.CommitMessagePath = "commit_message", "commit.message"
			return c
		}, `package test/pkg: base_from="commit_message" requires commit_version_pattern and commit_message_path`},
		{"base_from commit_message valid", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.CommitVersionPattern, c.CommitMessagePath = "commit_message", `v(\d+)`, "commit.message"
			return c
		}, ""},

		// base_from = "none"
		{"base_from none without track commit", func() PackageConfig { c := s086JSON(); c.BaseFrom = "none"; return c },
			`package test/pkg: base_from requires track="commit"`},
		{"base_from none with base_url", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL = "none", "https://example.com/f"
			return c
		}, `package test/pkg: base_from="none" declares there is no base source, so base_url, base_pattern, base_tag_pattern and commit_version_pattern must all be absent`},
		{"base_from none with commit_version_pattern", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.CommitVersionPattern, c.CommitMessagePath = "none", `v(\d+)`, "commit.message"
			return c
		}, `package test/pkg: base_from="none" declares there is no base source, so base_url, base_pattern, base_tag_pattern and commit_version_pattern must all be absent`},
		{"base_from none with commit_message_path only", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.CommitMessagePath = "none", "commit.message"
			return c
		}, ""},
		{"base_from none valid", func() PackageConfig { c := s086Commit(); c.BaseFrom = "none"; return c }, ""},

		// base_from = anything else
		{"unknown base_from", func() PackageConfig { c := s086Commit(); c.BaseFrom = "git"; return c },
			`package test/pkg: invalid base_from "git": must be "file", "tag", "commit_message" or "none"`},
		{"base_from is case sensitive", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BasePattern = "File", "https://example.com/f", `(\d+)`
			return c
		}, `package test/pkg: invalid base_from "File": must be "file", "tag", "commit_message" or "none"`},

		// aux_url
		// A placeholder in the host never reaches the placeholder-in-host
		// message: url.Parse rejects "{" in a host first, so the record is
		// reported as not an http(s) URL at all.
		{"aux_url with a placeholder in the host", func() PackageConfig {
			c := s086JSON()
			c.AuxVar, c.AuxPattern, c.AuxURL = "MY_P", `(\S+)`, "https://{version}.example.com/notes"
			return c
		}, `package test/pkg: aux_url "https://{version}.example.com/notes" is not an absolute http(s) URL with a host`},
		{"aux_url with a placeholder in the port", func() PackageConfig {
			c := s086JSON()
			c.AuxVar, c.AuxPattern, c.AuxURL = "MY_P", `(\S+)`, "https://example.com:{version}/notes"
			return c
		}, `package test/pkg: aux_url "https://example.com:{version}/notes" is not an absolute http(s) URL with a host`},
		{"aux_url with a placeholder in the path", func() PackageConfig {
			c := s086JSON()
			c.AuxVar, c.AuxPattern, c.AuxURL = "MY_P", `(\S+)`, "https://example.com/{version}/notes"
			return c
		}, ""},

		// mirrors: equality with url is byte equality
		{"mirror equal to url", func() PackageConfig { c := s086JSON(); c.Mirrors = []string{c.URL}; return c },
			`package test/pkg: mirror "https://example.com/api" repeats url`},
		{"mirror differing from url only by a trailing slash", func() PackageConfig {
			c := s086JSON()
			c.Mirrors = []string{c.URL + "/"}
			return c
		}, `package test/pkg: mirror "https://example.com/api/" repeats url`},
		{"mirror differing from url only by host case", func() PackageConfig {
			c := s086JSON()
			c.Mirrors = []string{"https://EXAMPLE.com/api"}
			return c
		}, `package test/pkg: mirror "https://EXAMPLE.com/api" repeats url`},

		// fallback
		{"json fallback", func() PackageConfig {
			c := s086JSON()
			c.FallbackURL, c.FallbackParser = "https://example.com/fb", "json"
			return c
		}, ""},
		{"html fallback", func() PackageConfig {
			c := s086JSON()
			c.FallbackURL, c.FallbackParser = "https://example.com/fb", "html"
			return c
		}, ""},
		{"unknown fallback parser", func() PackageConfig {
			c := s086JSON()
			c.FallbackURL, c.FallbackParser = "https://example.com/fb", "xml"
			return c
		}, `package test/pkg: invalid fallback_parser type: "xml"`},
		{"unknown fallback parser without fallback_url is not checked", func() PackageConfig {
			c := s086JSON()
			c.FallbackParser = "xml"
			return c
		}, ""},
		{"regex fallback without fallback_pattern", func() PackageConfig {
			c := s086JSON()
			c.FallbackURL, c.FallbackParser = "https://example.com/fb", "regex"
			return c
		}, `package test/pkg: fallback_pattern required for regex fallback parser`},

		// transform rules only warn
		{"malformed transform rules do not fail", func() PackageConfig {
			c := s086JSON()
			c.Transform = [][]string{{"only-one"}, {"(", "x"}}
			return c
		}, ""},

		// Several faults at once: the first check in source order wins.
		{"missing url and parser and negative revision", func() PackageConfig {
			return PackageConfig{Revision: -1, Timeout: -1}
		}, `package test/pkg: missing required field: url`},
		{"negative timeout and negative revision", func() PackageConfig {
			c := s086JSON()
			c.Timeout, c.Revision = -5, -1
			return c
		}, `package test/pkg: timeout must be >= 0 seconds, got -5`},
		{"negative revision and unknown parser", func() PackageConfig {
			c := s086JSON()
			c.Revision, c.Parser = -2, "xml"
			return c
		}, `package test/pkg: revision must be >= 0, got -2`},
		{"suffix with track commit and unknown base_from", func() PackageConfig {
			c := s086Commit()
			c.Suffix, c.BaseFrom = "_rc", "git"
			return c
		}, `package test/pkg: suffix cannot be combined with track="commit" (the snapshot suffix comes from the current ebuild)`},
		{"invalid track and unknown base_from", func() PackageConfig {
			c := s086JSON()
			c.Track, c.BaseFrom = "tag", "git"
			return c
		}, `package test/pkg: invalid track value: must be '' or 'commit', got "tag"`},
		{"track commit with regex parser and no commit_sha_path", func() PackageConfig {
			c := s086Commit()
			c.Parser, c.Path, c.Pattern, c.CommitSHAPath = "regex", "", `v(\d+)`, ""
			return c
		}, `package test/pkg: track="commit" requires parser="json"`},
		{"base_from file without track and half-set aux", func() PackageConfig {
			c := s086JSON()
			c.BaseFrom, c.AuxVar = "file", "MY_P"
			return c
		}, `package test/pkg: base_from requires track="commit"`},
		{"base_from none with base_url and base_tag_pattern", func() PackageConfig {
			c := s086Commit()
			c.BaseFrom, c.BaseURL, c.BaseTagPattern = "none", "https://example.com/f", `v(\d+)`
			return c
		}, `package test/pkg: base_from="none" declares there is no base source, so base_url, base_pattern, base_tag_pattern and commit_version_pattern must all be absent`},
		{"aux_url placeholder in host and bad mirror", func() PackageConfig {
			c := s086JSON()
			c.AuxVar, c.AuxPattern, c.AuxURL = "MY_P", `(\S+)`, "https://{version}.example.com/"
			c.Mirrors = []string{"ftp://example.com/x"}
			return c
		}, `package test/pkg: aux_url "https://{version}.example.com/" is not an absolute http(s) URL with a host`},
		{"mirror repeating url and unknown fallback parser", func() PackageConfig {
			c := s086JSON()
			c.Mirrors = []string{c.URL}
			c.FallbackURL, c.FallbackParser = "https://example.com/fb", "xml"
			return c
		}, `package test/pkg: mirror "https://example.com/api" repeats url`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg()
			err := ValidatePackageConfig(nil, s086Pkg, &cfg)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("ValidatePackageConfig() = %q, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidatePackageConfig() = nil, want %q", tt.want)
			}
			if got := err.Error(); got != tt.want {
				t.Errorf("ValidatePackageConfig() error\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// TestS086ValidatePackageConfigWarnings pins the branches that log a warning
// and still accept the record.
func TestS086ValidatePackageConfigWarnings(t *testing.T) {
	tests := []struct {
		name string
		cfg  func() PackageConfig
		want []string // substrings that must each appear in the log, in order
	}{
		{"transform rule with one element", func() PackageConfig {
			c := s086JSON()
			c.Transform = [][]string{{"only-one"}}
			return c
		}, []string{`msg="package transform rule has the wrong number of elements, want 2 ([regex, repl]); it will be ignored" package=test/pkg rule=0 elements=1`}},
		{"transform rule with uncompilable regex", func() PackageConfig {
			c := s086JSON()
			c.Transform = [][]string{{"a", "b"}, {"(", "x"}}
			return c
		}, []string{`msg="package transform rule has bad regex; it will be ignored" package=test/pkg rule=1 regex=(`}},
		{"both malformed transform rules warn in rule order", func() PackageConfig {
			c := s086JSON()
			c.Transform = [][]string{{"("}, {"(", "x"}}
			return c
		}, []string{
			`msg="package transform rule has the wrong number of elements, want 2 ([regex, repl]); it will be ignored" package=test/pkg rule=0`,
			`msg="package transform rule has bad regex; it will be ignored" package=test/pkg rule=1`,
		}},
		{"commit_version_pattern on a version-tracked record", func() PackageConfig {
			c := s086JSON()
			c.CommitVersionPattern = "("
			return c
		}, []string{`msg="package commit_version_pattern is set but track!=\"commit\"; it will be ignored" package=test/pkg`}},
		{"commit_message_path on a version-tracked record", func() PackageConfig {
			c := s086JSON()
			c.CommitMessagePath = "commit.message"
			return c
		}, []string{`msg="package commit_message_path is set but track!=\"commit\"; it will be ignored" package=test/pkg`}},
		{"both commit fields on a version-tracked record", func() PackageConfig {
			c := s086JSON()
			c.CommitVersionPattern, c.CommitMessagePath = `v(\d+)`, "commit.message"
			return c
		}, []string{
			`msg="package commit_version_pattern is set but track!=\"commit\"; it will be ignored"`,
			`msg="package commit_message_path is set but track!=\"commit\"; it will be ignored"`,
		}},
		{"script parser with transform and select", func() PackageConfig {
			return PackageConfig{
				URL: "https://example.com/api", Parser: "script", Script: "return '1.0'",
				Transform: [][]string{{"^v", ""}}, Select: "max",
			}
		}, []string{
			`msg="package transform is ignored for parser=\"script\" (the script must normalize the version itself)" package=test/pkg`,
			`msg="package select is ignored for parser=\"script\" (the script must select the version itself)" package=test/pkg select=max`,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			cfg := tt.cfg()
			if err := ValidatePackageConfig(log, s086Pkg, &cfg); err != nil {
				t.Fatalf("ValidatePackageConfig() = %q, want nil", err)
			}
			out := buf.String()
			rest := out
			for _, w := range tt.want {
				i := strings.Index(rest, w)
				if i < 0 {
					t.Fatalf("log missing (or out of order) %q\nlog:\n%s", w, out)
				}
				rest = rest[i+len(w):]
			}
			if got, want := strings.Count(out, "level=WARN"), len(tt.want); got != want {
				t.Errorf("got %d warnings, want %d\nlog:\n%s", got, want, out)
			}
		})
	}
}
