package autoupdate

// Authored for story 051 (llm-agent-least-privilege), sub-task 6.1 — a host
// whose last label is all digits (an IPv4 literal) never becomes a
// WebFetch(domain:) rule (S051-R3.9, R8.1).
//
// Reuses hostSet, quoteForWarn and githubHosts (agent_hosts_test.go),
// captureWarnLogs (httpclient_test.go) and oneRule (agent_env_test.go).

import (
	"errors"
	"strings"
	"testing"
)

// ipLiteralHosts are IPv4 literals an upstream-controlled URL could name:
// loopback, RFC 1918, the cloud metadata endpoint, and the short form 127.1,
// which resolvers read as 127.0.0.1 and which is a spelling no dotted-quad
// check would catch — its last label is all digits all the same.
var ipLiteralHosts = []string{"127.0.0.1", "10.0.0.1", "192.168.1.10", "169.254.169.254", "127.1", "127.0.0.0x1", "0x7f.0x1"}

// digitLabelDNSNames are real DNS spellings that carry digits but whose LAST
// label is not all digits. They are the collapse half: a fix keyed on "any
// digit" or "starts like an IP" would wrongly fold them into the literals.
var digitLabelDNSNames = []string{"1password.com", "a1.b2.example", "123.example.org", "10.0.0.1.nip.io", "dl.cdn.x1", "dl.0xide"}

// lastLabelAllDigits reports whether the text after the last '.' is a NUMBER
// in the WHATWG URL parser's "ends in a number" sense (the R3.9 definition of
// an IPv4 literal): non-empty and all ASCII digits, or "0x"/"0X" followed only
// by hex digits. A WHATWG-based fetcher resolves 127.0.0.0x1 to 127.0.0.1.
func lastLabelAllDigits(host string) bool {
	last := host[strings.LastIndexByte(host, '.')+1:]
	if last == "" {
		return false
	}
	digits, base := last, "0123456789"
	if len(last) >= 2 && last[0] == '0' && (last[1] == 'x' || last[1] == 'X') {
		digits, base = last[2:], "0123456789abcdefABCDEF"
	}
	for i := 0; i < len(digits); i++ {
		if !strings.ContainsRune(base, rune(digits[i])) {
			return false
		}
	}
	return true
}

// TestUpstreamHosts_RejectsIPLiterals is R3.9 for the host set: each IPv4
// literal, with and without a port, is left out, and named — quoted with %q —
// in exactly one warning that also names the package. A valid DNS host passed
// alongside survives, and the fixed GitHub hosts are still there.
func TestUpstreamHosts_RejectsIPLiterals(t *testing.T) {
	const pkg = "dev-libs/iplit"
	for _, host := range ipLiteralHosts {
		for _, raw := range []string{"http://" + host + "/x", "https://" + host + ":8443/latest/meta-data/"} {
			t.Run(raw, func(t *testing.T) {
				lc := captureWarnLogs(t)
				hosts := upstreamHosts(lc.logger(), pkg, "https://good.example.org/x", raw)
				set := hostSet(hosts)
				if set[host] != 0 {
					t.Errorf("IPv4 literal %q from %q was kept as a WebFetch host (R3.9); hosts = %q", host, raw, hosts)
				}
				for _, h := range hosts {
					if lastLabelAllDigits(h) {
						t.Errorf("host %q has an all-digit last label (R3.9); hosts = %q", h, hosts)
					}
				}
				if set["good.example.org"] != 1 {
					t.Errorf("the valid host was lost beside %q; hosts = %q", raw, hosts)
				}
				for _, h := range githubHosts {
					if set[h] != 1 {
						t.Errorf("fixed host %q appears %d times, want 1; hosts = %q", h, set[h], hosts)
					}
				}
				quoted := quoteForWarn(host)
				n := 0
				for _, line := range lc.all() {
					if strings.Contains(line, quoted) {
						n++
						if !strings.Contains(line, pkg) {
							t.Errorf("warning %q does not name the package %s (R3.9)", line, pkg)
						}
					}
				}
				if n != 1 {
					t.Errorf("rejected host %s logged %d warnings, want exactly 1 quoted with %%q (R3.9); lines = %q", quoted, n, lc.all())
				}
			})
		}
	}
}

// TestWebFetchDomainRule_RejectsIPLiterals is R3.9 for the rule builder: an
// IPv4 literal is refused with an error wrapping errUnsafeHost. Converse,
// pinned (already true): IPv6 literals are refused too.
func TestWebFetchDomainRule_RejectsIPLiterals(t *testing.T) {
	for _, host := range ipLiteralHosts {
		rule, err := webFetchDomainRule(host)
		if err == nil {
			t.Errorf("webFetchDomainRule(%q) = %q, want an error: an IP literal grants WebFetch to loopback/internal/metadata (R3.9)", host, rule)
			continue
		}
		if !errors.Is(err, errUnsafeHost) {
			t.Errorf("webFetchDomainRule(%q) error %v does not wrap errUnsafeHost", host, err)
		}
		if !strings.Contains(err.Error(), quoteForWarn(host)) {
			t.Errorf("webFetchDomainRule(%q) error %q does not name the host quoted", host, err)
		}
	}
	for _, host := range []string{"::1", "[::1]", "fe80::1", "::ffff:127.0.0.1"} {
		rule, err := webFetchDomainRule(host)
		if err == nil {
			t.Errorf("webFetchDomainRule(%q) = %q, want an error for an IPv6 literal (converse of R3.9)", host, rule)
			continue
		}
		if !errors.Is(err, errUnsafeHost) {
			t.Errorf("webFetchDomainRule(%q) error %v does not wrap errUnsafeHost", host, err)
		}
	}
}

// TestUpstreamHosts_KeepsDigitLabelDNSNames is the non-vacuity converse of
// R3.9: DNS names carrying digits — even an IP-shaped prefix — are still
// accepted by both functions, with no warning. Only an all-digit LAST label is
// an IP literal.
func TestUpstreamHosts_KeepsDigitLabelDNSNames(t *testing.T) {
	const pkg = "dev-libs/digits"
	lc := captureWarnLogs(t)
	var urls []string
	for _, h := range digitLabelDNSNames {
		urls = append(urls, "https://"+h+"/dist/x.tar.gz")
	}
	hosts := upstreamHosts(lc.logger(), pkg, urls...)
	set := hostSet(hosts)
	for _, h := range digitLabelDNSNames {
		if set[h] != 1 {
			t.Errorf("DNS name %q appears %d times, want exactly once (it is not an IP literal); hosts = %q", h, set[h], hosts)
		}
		for _, line := range lc.all() {
			if strings.Contains(line, quoteForWarn(h)) {
				t.Errorf("DNS name %q was warned about: %q", h, line)
			}
		}
		rule, err := webFetchDomainRule(h)
		if err != nil {
			t.Errorf("webFetchDomainRule(%q) rejected a DNS name: %v", h, err)
			continue
		}
		if rule != "WebFetch(domain:"+h+")" {
			t.Errorf("webFetchDomainRule(%q) = %q, want %q", h, rule, "WebFetch(domain:"+h+")")
		}
	}
	if len(hosts) != len(digitLabelDNSNames)+len(githubHosts) {
		t.Errorf("upstreamHosts = %q, want the %d DNS names plus the %d GitHub hosts", hosts, len(digitLabelDNSNames), len(githubHosts))
	}
}

// FuzzWebFetchDomainRuleRejectsIPLiterals is R8.1's last-label clause: for ANY
// string, an accepted rule names a host whose last label is not all digits.
func FuzzWebFetchDomainRuleRejectsIPLiterals(f *testing.F) {
	seeds := append([]string{}, ipLiteralHosts...)
	seeds = append(seeds, digitLabelDNSNames...)
	seeds = append(seeds, "::1", "[::1]", "0.0.0.0", "255.255.255.255", "1.2.3.4", "example.com", "a.1")
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, host string) {
		rule, err := webFetchDomainRule(host)
		if err != nil {
			return
		}
		const pre, post = "WebFetch(domain:", ")"
		if !strings.HasPrefix(rule, pre) || !strings.HasSuffix(rule, post) || !oneRule.MatchString(rule) {
			t.Fatalf("webFetchDomainRule(%q) = %q, not one WebFetch(domain:<h>) rule", host, rule)
		}
		h := strings.TrimSuffix(strings.TrimPrefix(rule, pre), post)
		if h == "" || lastLabelAllDigits(h) {
			t.Fatalf("webFetchDomainRule(%q) = %q: host has an empty or all-digit last label (R3.9, R8.1)", host, rule)
		}
	})
}
