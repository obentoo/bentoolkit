package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
)

// Story 068, sub-task 2.1 — R2.1-R2.4, R5.1: --lint runs the same
// authenticated-fetch parser the sweep and `bentoo distfile` run, so every path
// refuses the same record with the same text. Helpers newS068Server,
// s068Sentinel and s068ChainCarries live in authfetch_serial_scope_test.go
// (1.1); authFetchOverlay in authfetch_public_test.go.

// s068Record renders one registry record whose meta inline table is metaTOML.
func s068Record(pkg, metaTOML string) string {
	meta := ""
	if metaTOML != "" {
		meta = "meta = { " + metaTOML + " }\n"
	}
	return fmt.Sprintf(`[%q]
url = "https://upstream.test/releases.json"
parser = "json"
path = "tag_name"
%scomments = """s068 fixture"""
`, pkg, meta)
}

// s068InvalidConfigIssues returns the invalid-config messages lint reported
// for pkg.
func s068InvalidConfigIssues(issues []LintIssue, pkg string) []string {
	var out []string
	for _, is := range issues {
		if is.Package == pkg && is.Rule == LintInvalidConfig {
			out = append(out, is.Message)
		}
	}
	return out
}

func TestLint_ReportsAuthFetchParserErrors(t *testing.T) {
	withSecretsFile(t, "")
	t.Setenv("BENTOO_FETCH_LINT_UNSET_068", "")

	const dl = `fetch_url = "https://vendor.test/dl", fetch_filename = "x-{version}.tar.gz"`
	records := []struct{ pkg, meta string }{
		{"app-misc/half", dl + `, fetch_serial_field = "key"`},
		{"app-misc/getform", dl + `, fetch_method = "get", fetch_form_env = "email=BENTOO_FETCH_EMAIL"`},
		{"app-misc/leak", dl + `, fetch_serial_env = "GITHUB_TOKEN", fetch_serial_field = "key"`},
		{"app-misc/plain", ""},
		{"app-misc/notrigger", `fetch_serial_env = "BENTOO_FETCH_X"`},
		{"app-misc/goodunset", dl + `, fetch_serial_env = "BENTOO_FETCH_LINT_UNSET_068", fetch_serial_field = "key"`},
	}
	var toml strings.Builder
	for _, r := range records {
		toml.WriteString(s068Record(r.pkg, r.meta))
		toml.WriteString("\n")
	}
	toml.WriteString("# END\n")
	overlay := writeRegistry(t, toml.String())

	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("LoadPackagesConfig: %v", err)
	}
	issues, err := LintPackagesConfig(nil, overlay)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}

	// Hostile first: parser-only errors --lint used to miss, and the R1 refusal.
	for _, pkg := range []string{"app-misc/half", "app-misc/getform", "app-misc/leak"} {
		t.Run(pkg+" is reported with the parser's text", func(t *testing.T) {
			got := s068InvalidConfigIssues(issues, pkg)
			if len(got) != 1 {
				t.Fatalf("lint reported %d invalid-config issues for %s, want 1: %q", len(got), pkg, got)
			}
			_, _, perr := fetch.ParseAuthFetchSpec(cfg.Packages[pkg].Meta)
			if perr == nil {
				t.Errorf("parseAuthFetchSpec accepted %s; the fixture expects a refusal", pkg)
			} else if !strings.Contains(got[0], perr.Error()) {
				t.Errorf("lint message %q does not carry the parser's text %q", got[0], perr.Error())
			}
			if pkg == "app-misc/leak" {
				for _, needle := range []string{"GITHUB_TOKEN", fetch.MetaFetchSerialEnv, "BENTOO_FETCH_GITHUB_TOKEN"} {
					if !strings.Contains(got[0], needle) {
						t.Errorf("lint message %q does not name %q", got[0], needle)
					}
				}
			}
		})
	}

	// Converse: what lint must leave as it is.
	t.Run("a record without fetch_url is unaffected", func(t *testing.T) {
		if got := s068InvalidConfigIssues(issues, "app-misc/plain"); len(got) != 0 {
			t.Errorf("lint reported %q for a record with no [meta] fetch", got)
		}
	})
	t.Run("a fetch_* key without fetch_url keeps today's message", func(t *testing.T) {
		got := s068InvalidConfigIssues(issues, "app-misc/notrigger")
		if len(got) != 1 || !strings.Contains(got[0], fetch.ErrMetaFetchURLRequired.Error()) {
			t.Errorf("lint reported %q, want one issue carrying %q", got, fetch.ErrMetaFetchURLRequired)
		}
	})
	t.Run("a valid record naming an unset BENTOO_FETCH_ variable is clean: lint never resolves", func(t *testing.T) {
		if got := s068InvalidConfigIssues(issues, "app-misc/goodunset"); len(got) != 0 {
			t.Errorf("lint reported %q for a valid record", got)
		}
	})
}

// TestAuthFetchRefusal_SameTextOnEveryPath drives the reproduction's record
// through the sweep's prefetch step, FetchAuthDistfile (what `bentoo distfile`
// calls) and --lint, and requires the parser's exact text on each (R2.4).
func TestAuthFetchRefusal_SameTextOnEveryPath(t *testing.T) {
	withSecretsFile(t, "")
	t.Setenv("GITHUB_TOKEN", s068Sentinel)
	t.Setenv("BENTOO_FETCH_OK_068", "good-serial-068")
	srv := newS068Server(t)

	serial := `, fetch_serial_field = "key", fetch_filename = "x-{version}.tar.gz"`
	overlay := authFetchOverlay(t,
		s068Record("app-misc/leak", fmt.Sprintf(`fetch_url = %q, fetch_serial_env = "GITHUB_TOKEN"`, srv.URL+"/leak")+serial)+"\n"+
			s068Record("app-misc/fine", fmt.Sprintf(`fetch_url = %q, fetch_serial_env = "BENTOO_FETCH_OK_068"`, srv.URL+"/fine")+serial)+"\n# END\n",
		"app-misc/leak/leak-1.0.ebuild", "app-misc/fine/fine-1.0.ebuild")

	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("LoadPackagesConfig: %v", err)
	}
	_, _, parserErr := fetch.ParseAuthFetchSpec(cfg.Packages["app-misc/leak"].Meta)
	if parserErr == nil {
		t.Error("parseAuthFetchSpec accepted fetch_serial_env = \"GITHUB_TOKEN\"; want the R1 refusal")
	}

	check := func(path string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: no error for the refused record", path)
			return
		}
		msg := err.Error()
		for _, needle := range []string{"GITHUB_TOKEN", fetch.MetaFetchSerialEnv, "BENTOO_FETCH_GITHUB_TOKEN"} {
			if !strings.Contains(msg, needle) {
				t.Errorf("%s: %q does not name %q", path, msg, needle)
			}
		}
		if parserErr != nil && !strings.Contains(msg, parserErr.Error()) {
			t.Errorf("%s: %q does not carry the parser's text %q", path, msg, parserErr.Error())
		}
		if leaked, ok := s068ChainCarries(err, s068Sentinel); ok {
			t.Errorf("%s: the error carries the variable's value: %q", path, leaked)
		}
	}

	// The sweep: the refused package fails its manifest step, the next one runs.
	sw := newSweeper(overlay, withSweeperConfigs(cfg.Packages))
	sweepErr := sw.prefetchAuthDistfile(t.Context(), "app-misc/leak", "1.0", t.TempDir())
	check("sweep", sweepErr)
	if sweepErr != nil && !errors.Is(sweepErr, ErrManifestFailed) {
		t.Errorf("sweep: %v is not ErrManifestFailed", sweepErr)
	}
	if err := sw.prefetchAuthDistfile(t.Context(), "app-misc/fine", "1.0", t.TempDir()); err != nil {
		t.Errorf("sweep: the next, valid package failed after the refused one: %v", err)
	}

	// bentoo distfile.
	_, distErr := FetchAuthDistfile(context.Background(), nil, AuthDistfileRequest{
		OverlayPath: overlay, Package: "app-misc/leak", Version: "1.0", DestDir: t.TempDir(),
	})
	check("distfile", distErr)
	if distErr != nil && !errors.Is(distErr, fetch.ErrAuthFetchFailed) {
		t.Errorf("distfile: %v is not ErrAuthFetchFailed", distErr)
	}

	// --lint.
	issues, err := LintPackagesConfig(nil, overlay)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	got := s068InvalidConfigIssues(issues, "app-misc/leak")
	if len(got) != 1 {
		t.Errorf("lint reported %d invalid-config issues for app-misc/leak, want 1: %q", len(got), got)
	} else {
		check("lint", errors.New(got[0]))
	}

	// Only the valid record reached the endpoint.
	forms := srv.received()
	if len(forms) != 1 || forms[0].Get("key") != "good-serial-068" {
		t.Errorf("the endpoint received %v; want exactly the valid record's one request (key=good-serial-068)", forms)
	}
}
