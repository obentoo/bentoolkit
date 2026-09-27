package autoupdate

// Authored for story 051 (llm-agent-least-privilege), sub-task 2.2 — the
// upstream host set and WebFetch domain rules (S051-R3.3, R3.6, R8.1).
//
// Names pinned by tasks.md: upstreamHosts(pkg string, urls ...string) []string,
// webFetchDomainRule(host string) (string, error), FuzzWebFetchDomainRule. Kept
// apart from agent_permissions_test.go so 2.1 can go green before 2.2 exists.

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
)

// ---------------------------------------------------------------------------
// 2.2 — upstream hosts and WebFetch domain rules
// ---------------------------------------------------------------------------

var githubHosts = []string{"github.com", "codeload.github.com", "objects.githubusercontent.com"}

func hostSet(hosts []string) map[string]int {
	m := map[string]int{}
	for _, h := range hosts {
		m[h]++
	}
	return m
}

// TestUpstreamHosts_FromURLs is R3.3's host extraction with both hostile
// halves. Collapse: api.github.com and github.com.evil.example look like
// GitHub and must stay DISTINCT hosts rather than fold into github.com. Split:
// two URLs on one host (different port, userinfo, path) are one host, once.
func TestUpstreamHosts_FromURLs(t *testing.T) {
	hosts := upstreamHosts("dev-libs/foo",
		"https://dl.example.org/foo/1.0.tar.gz",
		"https://user:pw@dl.example.org:8443/foo/2.0.tar.gz",
		"http://mirror.example.net:8080/x",
		"https://api.github.com/repos/o/r/releases",
		"https://github.com.evil.example/o/r",
		"https://github.com/o/r/releases",
	)
	set := hostSet(hosts)
	for _, want := range []string{"dl.example.org", "mirror.example.net", "api.github.com", "github.com.evil.example"} {
		if set[want] != 1 {
			t.Errorf("host %q appears %d times, want exactly once; hosts = %q", want, set[want], hosts)
		}
	}
	for _, h := range githubHosts {
		if set[h] != 1 {
			t.Errorf("host %q appears %d times, want exactly once (R3.3); hosts = %q", h, set[h], hosts)
		}
	}
	for _, h := range hosts {
		rule, err := webFetchDomainRule(h)
		if err != nil {
			t.Errorf("webFetchDomainRule(%q) rejected a host upstreamHosts emitted: %v", h, err)
			continue
		}
		if rule != "WebFetch(domain:"+h+")" {
			t.Errorf("webFetchDomainRule(%q) = %q, want %q", h, rule, "WebFetch(domain:"+h+")")
		}
	}
}

// TestUpstreamHosts_RejectsHostileValues is R3.6: every candidate host that is
// not a lowercase DNS name is left out and logged ONCE, naming the package and
// the value quoted with %q. An uppercase host may be lowercased or dropped, but
// never emitted as given.
func TestUpstreamHosts_RejectsHostileValues(t *testing.T) {
	lc := captureWarnLogs(t)
	const pkg = "dev-libs/hostile"
	bad := map[string]string{
		"https://*.example.com/x":   "*.example.com",
		"https://localhost/x":       "localhost",
		"https://-bad.example.com/": "-bad.example.com",
		"https://exa_mple.com/":     "exa_mple.com",
		"https://example.com./":     "example.com.",
		"https://bad..example.com/": "bad..example.com",
		"https://bücher.example/":   "bücher.example",
		"https://a(b).example.com/": "a(b).example.com",
		"https://a,b.example.com/":  "a,b.example.com",
	}
	urls := []string{"https://good.example.org/x", "https://Example.COM/x"}
	for u := range bad {
		urls = append(urls, u)
	}
	hosts := upstreamHosts(pkg, urls...)
	set := hostSet(hosts)
	if set["good.example.org"] != 1 {
		t.Errorf("the valid host was lost among hostile ones; hosts = %q", hosts)
	}
	for _, h := range hosts {
		if strings.ToLower(h) != h {
			t.Errorf("host %q is not lowercase (R3.6)", h)
		}
	}
	for u, host := range bad {
		if set[host] != 0 {
			t.Errorf("hostile host %q from %q was kept (R3.6)", host, u)
		}
		quoted := quoteForWarn(host)
		n := 0
		for _, line := range lc.all() {
			if strings.Contains(line, quoted) {
				n++
				if !strings.Contains(line, pkg) {
					t.Errorf("warning %q does not name the package %s (R3.6)", line, pkg)
				}
			}
		}
		if n != 1 {
			t.Errorf("rejected host %s logged %d warnings, want exactly 1 quoted with %%q (R3.6); lines = %q", quoted, n, lc.all())
		}
	}
}

// quoteForWarn renders v as %q does.
func quoteForWarn(v string) string { return fmt.Sprintf("%q", v) }

// TestUpstreamHosts_AlwaysIncludesGitHub is R3.3's fixed part: with no URL at
// all the set is exactly the three GitHub hosts, and a GitHub URL does not
// duplicate one of them.
func TestUpstreamHosts_AlwaysIncludesGitHub(t *testing.T) {
	for _, urls := range [][]string{nil, {"https://github.com/o/r/archive/v1.tar.gz", "https://codeload.github.com/o/r/tar.gz/v1"}} {
		hosts := upstreamHosts("dev-libs/foo", urls...)
		if len(hosts) != len(githubHosts) {
			t.Errorf("upstreamHosts(%q) = %q, want exactly %q", urls, hosts, githubHosts)
		}
		set := hostSet(hosts)
		for _, h := range githubHosts {
			if set[h] != 1 {
				t.Errorf("upstreamHosts(%q): %q appears %d times, want 1", urls, h, set[h])
			}
		}
	}
}

// FuzzWebFetchDomainRule is R8.1: for ANY string the builder either rejects it
// or emits WebFetch(domain:<h>) whose host carries none of * ( ) , space / or a
// control character, and is a lowercase DNS name.
func FuzzWebFetchDomainRule(f *testing.F) {
	for _, seed := range []string{
		"example.com", "dl.example.org", "", "*", "*.example.com", "example.com)", "a(b).com",
		"example.com,evil.com", "example.com evil.com", "example.com/path", "exa\x00mple.com",
		"example.com\nWebFetch(domain:evil.com", "EXAMPLE.com", "localhost", "-a.com", "a..b",
		"github.com", "bücher.example", "xn--bcher-kva.example",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, host string) {
		rule, err := webFetchDomainRule(host)
		if err != nil {
			return
		}
		const pre, post = "WebFetch(domain:", ")"
		if !strings.HasPrefix(rule, pre) || !strings.HasSuffix(rule, post) {
			t.Fatalf("webFetchDomainRule(%q) = %q, not of the form WebFetch(domain:<h>)", host, rule)
		}
		h := strings.TrimSuffix(strings.TrimPrefix(rule, pre), post)
		if h == "" {
			t.Fatalf("webFetchDomainRule(%q) = %q with an empty host", host, rule)
		}
		for _, r := range h {
			if strings.ContainsRune("*(),/ ", r) || unicode.IsControl(r) || r > unicode.MaxASCII {
				t.Fatalf("webFetchDomainRule(%q) = %q: host carries %q (R8.1)", host, rule, r)
			}
		}
		if !oneRule.MatchString(rule) {
			t.Fatalf("webFetchDomainRule(%q) = %q is not one rule", host, rule)
		}
	})
}
