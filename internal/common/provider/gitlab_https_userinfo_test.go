package provider

import (
	"errors"
	"strings"
	"testing"
)

// Story 052, sub-task 5.2 — S052-R6.1 (v2): the error refusing a non-https
// GitLab repository URL names the URL with its userinfo removed entirely.
// url.URL.Redacted() masks only the password, so a token carried as the
// username — http://TOKEN@gitlab.example/g/p — reached the error, which
// cmd/bentoo/overlay_compare.go logs.
func TestParseGitLabURL_RequiresHTTPS_DropsUserinfo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Hostile first: every userinfo shape, each carrying a secret that must not
	// reach the error text.
	for _, tc := range []struct {
		name    string
		raw     string
		secrets []string
		scheme  string
	}{
		{"token as the username", "http://glpat-USERONLYSECRET@gitlab.example/g/p", []string{"glpat-USERONLYSECRET"}, "http"},
		{"user and password", "http://deploy-USERNAME:glpat-PASSWORDSECRET@gitlab.example/g/p", []string{"deploy-USERNAME", "glpat-PASSWORDSECRET"}, "http"},
		{"user with an empty password", "http://glpat-EMPTYPASSUSER:@gitlab.example/g/p", []string{"glpat-EMPTYPASSUSER"}, "http"},
		{"ssh with a token as the username", "ssh://glpat-SSHUSER@gitlab.example/g/p", []string{"glpat-SSHUSER"}, "ssh"},
	} {
		t.Run("refused without userinfo: "+tc.name, func(t *testing.T) {
			p, err := NewGitLabProvider(&RepositoryInfo{Name: "x", Provider: "gitlab", URL: tc.raw, Token: "glpat-x"})
			if err == nil {
				t.Fatalf("NewGitLabProvider(%q) = %+v, nil; want an error", tc.raw, p)
			}
			if !errors.Is(err, ErrInvalidRepoURL) {
				t.Errorf("err = %v; want errors.Is(err, ErrInvalidRepoURL)", err)
			}
			msg := err.Error()
			for _, s := range tc.secrets {
				if strings.Contains(msg, s) {
					t.Errorf("err %q leaks the userinfo component %q", msg, s)
				}
			}
			if strings.Contains(msg, "@") {
				t.Errorf("err %q still carries a userinfo separator '@'", msg)
			}
			if !strings.Contains(msg, "gitlab.example") {
				t.Errorf("err %q does not name the host", msg)
			}
			if !strings.Contains(msg, tc.scheme) {
				t.Errorf("err %q does not name the scheme %q", msg, tc.scheme)
			}
			if !strings.Contains(msg, "https") {
				t.Errorf("err %q does not say https is required", msg)
			}
		})
	}

	// Hostile, third route: a URL whose "://" sits after the authority parses as
	// OPAQUE (no Host, no User), so clearing url.URL.User leaves the token in the
	// rendered string. The same token-as-username must not reach the error.
	t.Run("refused without userinfo: opaque form", func(t *testing.T) {
		raw := "http:glpat-OPAQUESECRET@gitlab.example/g/p#://"
		p, err := NewGitLabProvider(&RepositoryInfo{Name: "x", Provider: "gitlab", URL: raw, Token: "glpat-x"})
		if err == nil {
			t.Fatalf("NewGitLabProvider(%q) = %+v, nil; want an error", raw, p)
		}
		if !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("err = %v; want errors.Is(err, ErrInvalidRepoURL)", err)
		}
		if msg := err.Error(); strings.Contains(msg, "glpat-OPAQUESECRET") {
			t.Errorf("err %q leaks the token written as the username", msg)
		}
	})

	// Converse: https with userinfo is not refused (R6.1 is about the scheme),
	// and the resolved base URL carries no userinfo either.
	t.Run("https with userinfo still resolves", func(t *testing.T) {
		raw := "https://glpat-HTTPSUSER@gitlab.example/g/p.git"
		p, err := NewGitLabProvider(&RepositoryInfo{Name: "x", Provider: "gitlab", URL: raw})
		if err != nil {
			t.Fatalf("NewGitLabProvider(%q): %v", raw, err)
		}
		if p.BaseURL != "https://gitlab.example" {
			t.Errorf("BaseURL = %q, want %q", p.BaseURL, "https://gitlab.example")
		}
		if strings.Contains(p.BaseURL, "HTTPSUSER") || strings.Contains(p.BaseURL, "@") {
			t.Errorf("BaseURL %q carries the userinfo", p.BaseURL)
		}
	})
}
