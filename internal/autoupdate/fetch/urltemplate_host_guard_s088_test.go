package fetch

// URLTemplateFault keeps a TemplatePlaceholderInHost branch that no template
// reaches today: url.Parse already refuses a brace before the path. These tests
// pin that fact, so the day a Go release accepts one of these inputs the test
// names it — and the guard, kept for exactly that day, is then live.

import (
	"net/url"
	"testing"
)

// TestS088_2_3_ParseRefusesABraceBeforeThePath pins the parser half: every
// position ahead of the path rejects a placeholder, so URLTemplateFault reports
// the template as not an http(s) URL rather than reaching its host check.
func TestS088_2_3_ParseRefusesABraceBeforeThePath(t *testing.T) {
	for _, tc := range []struct{ where, template string }{
		{"host", "https://{id}.example.test/v"},
		{"userinfo", "https://u{id}@example.test/v"},
		{"port", "https://example.test:{id}/v"},
		{"scheme", "ht{id}tp://example.test/v"},
	} {
		t.Run(tc.where, func(t *testing.T) {
			if _, err := url.Parse(tc.template); err == nil {
				t.Errorf("url.Parse(%q) accepted a brace in the %s: the TemplatePlaceholderInHost guard in URLTemplateFault is now reachable, and its comment must say so", tc.template, tc.where)
			}
			if got := URLTemplateFault(tc.template); got != TemplateNotHTTP {
				t.Errorf("URLTemplateFault(%q) = %d, want TemplateNotHTTP (%d)", tc.template, got, TemplateNotHTTP)
			}
		})
	}
}

// TestS088_2_3_BraceAfterTheHostIsAccepted is the hostile half: the path and
// the query are where a placeholder belongs.
func TestS088_2_3_BraceAfterTheHostIsAccepted(t *testing.T) {
	for _, template := range []string{
		"https://example.test/v/{id}",
		"https://example.test/v?id={id}&v=" + VersionPlaceholder,
	} {
		if got := URLTemplateFault(template); got != templateOK {
			t.Errorf("URLTemplateFault(%q) = %d, want templateOK (%d)", template, got, templateOK)
		}
	}
}
