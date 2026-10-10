package registry

import (
	"fmt"
	"strings"
	"testing"
)

// Story 087, sub-task 3.4: a mirror equal to the url after normalization —
// scheme and host compared case-insensitively, one trailing "/" of the path
// ignored — is refused with the existing "repeats url" error (R5.4); two
// mirrors equal after that normalization are refused naming the package and
// both (R5.5); a byte-identical mirror keeps today's error (R10.6); an aux_url
// with a placeholder in its host keeps today's error text (R5.6).

const s087MirrorPkg = "test/pkg"

func s087MirrorRecord(url string, mirrors ...string) PackageConfig {
	return PackageConfig{URL: url, Parser: "json", Path: "tag_name", Mirrors: mirrors}
}

func s087RepeatsURL(mirror string) string {
	return fmt.Sprintf("package %s: mirror %q repeats url", s087MirrorPkg, mirror)
}

// TestS087_3_4_MirrorRepeatingURLAfterNormalization covers R5.4 and R10.6.
// want "" means the record is accepted.
func TestS087_3_4_MirrorRepeatingURLAfterNormalization(t *testing.T) {
	const api = "https://example.com/api"
	tests := []struct {
		name    string
		url     string
		mirrors []string
		want    string
	}{
		// Hostile, wrongly collapsed: near-identical URLs that are different
		// resources must stay apart.
		{"path case differs", api, []string{"https://example.com/API"}, ""},
		{"two trailing slashes, only one is ignored", api, []string{"https://example.com/api//"}, ""},
		{"query differs", api, []string{"https://example.com/api?x=1"}, ""},
		{"port differs", api, []string{"https://example.com:8443/api"}, ""},
		{"scheme differs", api, []string{"http://example.com/api"}, ""},
		{"host differs", api, []string{"https://mirror.example.com/api"}, ""},
		{"path segment differs by a slash", "https://example.com/a/b", []string{"https://example.com/ab"}, ""},
		// Hostile, wrongly split: differently written URLs that are the url.
		{"host case and trailing slash", "https://example.com/x", []string{"https://Example.com/x/"}, s087RepeatsURL("https://Example.com/x/")},
		{"scheme case", api, []string{"HTTPS://example.com/api"}, s087RepeatsURL("HTTPS://example.com/api")},
		{"upper-case host", api, []string{"https://EXAMPLE.COM/api"}, s087RepeatsURL("https://EXAMPLE.COM/api")},
		{"trailing slash on the url side", "https://example.com/api/", []string{api}, s087RepeatsURL(api)},
		{"url written in upper case", "HTTPS://Example.com/api", []string{"https://example.com/api/"}, s087RepeatsURL("https://example.com/api/")},
		{"root path against no path", "https://example.com/", []string{"https://example.com"}, s087RepeatsURL("https://example.com")},
		{"second mirror repeats url", api, []string{"https://mirror.example.com/api", "https://Example.com/api/"}, s087RepeatsURL("https://Example.com/api/")},
		// Benign (R10.6): byte-identical, as today.
		{"byte-identical mirror", api, []string{api}, s087RepeatsURL(api)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := s087MirrorRecord(tc.url, tc.mirrors...)
			err := ValidatePackageConfig(nil, s087MirrorPkg, &cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("url %q mirrors %q refused: %v", tc.url, tc.mirrors, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("url %q mirrors %q accepted, want %q", tc.url, tc.mirrors, tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("url %q mirrors %q error = %q, want %q", tc.url, tc.mirrors, err.Error(), tc.want)
			}
		})
	}
}

// TestS087_3_4_DuplicateMirrorsAfterNormalization covers R5.5. pair names the
// two mirrors the error must name; notNamed is a third mirror it must not
// blame. pair == nil means the record is accepted.
func TestS087_3_4_DuplicateMirrorsAfterNormalization(t *testing.T) {
	const url = "https://example.com/api"
	tests := []struct {
		name     string
		mirrors  []string
		pair     []string
		notNamed string
	}{
		// Hostile, wrongly collapsed.
		{"path case differs", []string{"https://a.example.com/x", "https://a.example.com/X"}, nil, ""},
		{"one and two trailing slashes", []string{"https://a.example.com/x/", "https://a.example.com/x//"}, nil, ""},
		{"port differs", []string{"https://a.example.com/x", "https://a.example.com:8443/x"}, nil, ""},
		{"scheme differs", []string{"https://a.example.com/x", "http://a.example.com/x"}, nil, ""},
		// Hostile, wrongly split.
		{"host case and trailing slash", []string{"https://a.example.com/x", "https://A.example.com/x/"},
			[]string{"https://a.example.com/x", "https://A.example.com/x/"}, ""},
		{"scheme case, not adjacent", []string{"https://a.example.com/y", "https://b.example.com/y", "HTTPS://a.example.com/y"},
			[]string{"https://a.example.com/y", "HTTPS://a.example.com/y"}, "https://b.example.com/y"},
		// Third element: "x//" normalizes to ".../x/", which is what "x/" is
		// WRITTEN as. The collision is between the second and third mirrors;
		// the first must not be blamed.
		{"stripped slash does not reach a third mirror", []string{"https://a.example.com/x//", "https://A.example.com/x/", "https://a.example.com/x"},
			[]string{"https://A.example.com/x/", "https://a.example.com/x"}, "https://a.example.com/x//"},
		// Benign: byte-identical duplicates.
		{"byte-identical mirrors", []string{"https://a.example.com/x", "https://a.example.com/x"},
			[]string{"https://a.example.com/x"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := s087MirrorRecord(url, tc.mirrors...)
			err := ValidatePackageConfig(nil, s087MirrorPkg, &cfg)
			if tc.pair == nil {
				if err != nil {
					t.Fatalf("mirrors %q refused: %v", tc.mirrors, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("mirrors %q accepted, want them refused as duplicates", tc.mirrors)
			}
			msg := err.Error()
			if !strings.Contains(msg, s087MirrorPkg) {
				t.Errorf("error %q does not name the package %s", msg, s087MirrorPkg)
			}
			for _, m := range tc.pair {
				if !strings.Contains(msg, m) {
					t.Errorf("error %q does not name the mirror %s", msg, m)
				}
			}
			if tc.notNamed != "" && strings.Contains(msg, tc.notNamed) {
				t.Errorf("error %q blames %s, which duplicates no other mirror", msg, tc.notNamed)
			}
		})
	}
}

// TestS087_3_4_AuxURLPlaceholderInHostKeepsTodaysText pins R5.6: removing the
// unreachable placeholder-in-host branch must not change what such an aux_url
// is told today. Characterization: passes on main.
func TestS087_3_4_AuxURLPlaceholderInHostKeepsTodaysText(t *testing.T) {
	tests := []struct {
		name   string
		auxURL string
		want   string // "" means accepted
	}{
		{"placeholder is the host", "https://{version}.example.com/notes",
			`package test/pkg: aux_url "https://{version}.example.com/notes" is not an absolute http(s) URL with a host`},
		{"placeholder in the port", "https://example.com:{version}/notes",
			`package test/pkg: aux_url "https://example.com:{version}/notes" is not an absolute http(s) URL with a host`},
		{"placeholder in the userinfo", "https://{version}@example.com/notes",
			`package test/pkg: aux_url "https://{version}@example.com/notes" is not an absolute http(s) URL with a host`},
		{"placeholder in the path", "https://example.com/{version}/notes", ""},
		{"placeholder in the query", "https://example.com/notes?v={version}", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := s087MirrorRecord("https://example.com/api")
			cfg.AuxVar, cfg.AuxPattern, cfg.AuxURL = "MY_P", `(\S+)`, tc.auxURL
			err := ValidatePackageConfig(nil, s087MirrorPkg, &cfg)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tc.want {
				t.Fatalf("aux_url %q: error = %q, want %q", tc.auxURL, got, tc.want)
			}
		})
	}
}
