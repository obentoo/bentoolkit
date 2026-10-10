package registry

import (
	"errors"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/jsonpath"
)

// TestS087JSONPathCheckFollowsTheReader pins where the load-time JSON path
// check applies: wherever a fetch reads the field as a JSON path, and nowhere
// it would only shadow the error the record already gets today.
func TestS087JSONPathCheckFollowsTheReader(t *testing.T) {
	t.Run("a json fallback reads path", func(t *testing.T) {
		for _, primary := range []PackageConfig{
			{URL: "https://example.com/p", Parser: "html", Selector: "span.v"},
			{URL: "https://example.com/p", Parser: "regex", Pattern: `v(\S+)`},
		} {
			cfg := primary
			cfg.Path = "a..b"
			cfg.FallbackParser = "json"
			cfg.FallbackURL = "https://example.com/api"
			err := ValidatePackageConfig(nil, "test/pkg", &cfg)
			if !errors.Is(err, jsonpath.ErrInvalidJSONPath) || !strings.Contains(err.Error(), `path "a..b"`) {
				t.Errorf("parser %s with a json fallback and path a..b: error = %v; want the malformed path refused", primary.Parser, err)
			}
		}
	})

	t.Run("select max and last read path as a list", func(t *testing.T) {
		for _, sel := range []string{"max", "last"} {
			cfg := PackageConfig{URL: "https://example.com/api", Parser: "json", Path: "[*].name", Select: sel}
			if err := ValidatePackageConfig(nil, "test/pkg", &cfg); err != nil {
				t.Errorf("select %s with path [*].name: error = %v; want it accepted", sel, err)
			}
			cfg.Path = "[*]name"
			if err := ValidatePackageConfig(nil, "test/pkg", &cfg); !errors.Is(err, jsonpath.ErrInvalidJSONPath) {
				t.Errorf("select %s with path [*]name: error = %v; want it refused", sel, err)
			}
		}
		cfg := PackageConfig{URL: "https://example.com/api", Parser: "json", Path: "[*].name"}
		if err := ValidatePackageConfig(nil, "test/pkg", &cfg); !errors.Is(err, jsonpath.ErrInvalidJSONPath) {
			t.Errorf("select first with path [*].name: error = %v; want it refused", err)
		}
	})

	t.Run("commit_sha_path on a non-json record keeps its parser error first", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://example.com/p", Parser: "html", Selector: "span.v", CommitSHAPath: "bad["}
		err := ValidatePackageConfig(nil, "test/pkg", &cfg)
		if err == nil || errors.Is(err, jsonpath.ErrInvalidJSONPath) || !strings.Contains(err.Error(), "commit_sha_path") {
			t.Errorf("html record with commit_sha_path bad[: error = %v; want today's parser error, not the syntax one", err)
		}
	})
}

// TestS087ReviewEdgeCases pins the edge cases the 3.3/3.4 tech review found.
func TestS087ReviewEdgeCases(t *testing.T) {
	t.Run("a multi-line base_from declares a source", func(t *testing.T) {
		for _, raw := range []string{`"""none"""`, `'''none'''`, `"none" # pinned`} {
			if isEmptyTOMLString(raw) {
				t.Errorf("isEmptyTOMLString(%s) = true; want false", raw)
			}
		}
		for _, raw := range []string{`""`, `''`, `"" # todo`, `  ''  `} {
			if !isEmptyTOMLString(raw) {
				t.Errorf("isEmptyTOMLString(%s) = false; want true", raw)
			}
		}
	})
	t.Run("an encoded trailing slash is data", func(t *testing.T) {
		if err := validateMirrors("p", &PackageConfig{URL: "https://h/a", Mirrors: []string{"https://h/a%2F"}}); err != nil {
			t.Errorf("mirror https://h/a%%2F beside url https://h/a: %v; want them distinct", err)
		}
		if err := validateMirrors("p", &PackageConfig{URL: "https://h/a%2F", Mirrors: []string{"https://h/a%2F/"}}); err == nil {
			t.Error("mirror https://h/a%2F/ beside url https://h/a%2F: accepted; want it to repeat url")
		}
	})
	t.Run("the binary finding quotes the value without its comment", func(t *testing.T) {
		for raw, want := range map[string]string{`"yes" # legacy`: `"yes"`, `1 # x`: `1`, `'a # b'`: `'a # b'`} {
			if got := tomlValueText(raw); got != want {
				t.Errorf("tomlValueText(%s) = %s; want %s", raw, got, want)
			}
		}
	})
}
