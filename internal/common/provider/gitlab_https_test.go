package provider

import (
	"errors"
	"strings"
	"testing"
)

// Story 052, sub-task 3.1 — S052-R6.1, R6.2: a GitLab repository URL must be
// https; the provider refuses anything else at construction, naming the URL
// with its userinfo redacted.
func TestParseGitLabURL_RequiresHTTPS(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Hostile first: every non-https scheme, including one carrying userinfo.
	for _, raw := range []string{
		"http://gitlab.example/g/p",
		"http://gitlab.com/g/p",
		"http://deploy:s3cr3t-pass@gitlab.example/g/p",
		"ssh://git@gitlab.example/g/p",
		"ftp://gitlab.example/g/p",
	} {
		t.Run("refused "+raw, func(t *testing.T) {
			p, err := NewGitLabProvider(&RepositoryInfo{Name: "x", Provider: "gitlab", URL: raw, Token: "glpat-x"})
			if err == nil {
				t.Fatalf("NewGitLabProvider(%q) = %+v, nil; want an error", raw, p)
			}
			if !errors.Is(err, ErrInvalidRepoURL) {
				t.Errorf("err = %v; want errors.Is(err, ErrInvalidRepoURL)", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "https") {
				t.Errorf("err %q does not say https is required", msg)
			}
			if !strings.Contains(msg, "gitlab.") {
				t.Errorf("err %q does not name the URL", msg)
			}
			if strings.Contains(msg, "s3cr3t-pass") {
				t.Errorf("err %q leaks the URL's password", msg)
			}
		})
	}

	// Converse: https (self-hosted or gitlab.com) and the bare form still work.
	for raw, wantBase := range map[string]string{
		"https://gitlab.example/g/p": "https://gitlab.example",
		"https://gitlab.com/g/p.git": "https://gitlab.com",
		"group/project":              "https://gitlab.com",
		"group/subgroup/project":     "https://gitlab.com",
	} {
		t.Run("accepted "+raw, func(t *testing.T) {
			p, err := NewGitLabProvider(&RepositoryInfo{Name: "x", Provider: "gitlab", URL: raw})
			if err != nil {
				t.Fatalf("NewGitLabProvider(%q): %v", raw, err)
			}
			if p.BaseURL != wantBase {
				t.Errorf("BaseURL = %q, want %q", p.BaseURL, wantBase)
			}
		})
	}
}
