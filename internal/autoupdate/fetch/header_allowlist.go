package fetch

import (
	"errors"
	"fmt"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
)

// Header env-var expansion allow-list (S001-R1, AD-8).
//
// A malicious packages.toml must not be able to exfiltrate arbitrary process
// secrets (e.g. ANTHROPIC_API_KEY) by embedding ${VAR} in any header value.
// Expansion is therefore allow-listed on TWO axes:
//
//  1. the header name must be one of a small fixed set of auth headers, and
//  2. the referenced environment variable must be explicitly allow-listed
//     (either a known token name or carry the BENTOO_ prefix), except
//     bentoolkit's own secrets, which are never expandable (S052-R1.9), and
//     the BENTOO_FETCH_* authenticated-fetch secrets (S068-R3.1).
//
// There is intentionally NO escape hatch: a user that needs another variable
// expanded must rename it to BENTOO_*. The constants below are package-private
// and not user-tunable.

// allowedExpansionHeaders is the set of canonical header names whose values are
// eligible for ${VAR} environment-variable expansion. Keys are stored already
// canonicalised via textproto.CanonicalMIMEHeaderKey.
var allowedExpansionHeaders = map[string]struct{}{
	"Authorization": {},
	"X-Api-Key":     {},
	"X-Auth-Token":  {},
	"Private-Token": {},
}

// allowedHeaderEnvAllowList is the set of environment variable names that may be
// expanded inside an allow-listed header value even though they do not carry the
// allowedHeaderEnvPrefix.
//
// OPENAI_API_KEY and ANTHROPIC_API_KEY are deliberately absent (S052-R3): they
// are the maintainer's LLM keys, and no upstream a package record names has a
// reason to receive them. A record that needs such a key renames it to BENTOO_*.
var allowedHeaderEnvAllowList = map[string]struct{}{
	"GITHUB_TOKEN": {},
	"GITLAB_TOKEN": {},
}

// allowedHeaderEnvPrefix is the prefix that opts an environment variable into
// header expansion regardless of allowedHeaderEnvAllowList membership.
const allowedHeaderEnvPrefix = "BENTOO_"

// ContainsCRLF reports whether s contains a carriage return or line feed.
// Such characters in a header name are a header/CRLF-injection vector and are
// always rejected, independently of canonicalisation.
func ContainsCRLF(s string) bool {
	return strings.ContainsAny(s, "\r\n")
}

// IsAllowedHeaderName reports whether the given header name is eligible for
// environment-variable expansion of its value.
//
// The name is trimmed of surrounding whitespace and canonicalised with
// textproto.CanonicalMIMEHeaderKey before the allow-list lookup, so callers may
// pass values with arbitrary casing or padding. Any name containing a CR or LF
// byte is rejected outright (defence against CRLF/header injection) BEFORE
// canonicalisation, because textproto.CanonicalMIMEHeaderKey returns its input
// unchanged when it contains invalid bytes.
func IsAllowedHeaderName(name string) bool {
	// CR/LF check first and independently of canonicalisation.
	if ContainsCRLF(name) {
		return false
	}
	canonical := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
	_, ok := allowedExpansionHeaders[canonical]
	return ok
}

// isAllowedEnvVar reports whether the given environment variable name may be
// expanded inside an allow-listed header value. It is true when the name
// carries the allowedHeaderEnvPrefix or is an explicit allow-list entry, and
// false for every name isReservedBentooSecret reserves.
func isAllowedEnvVar(name string) bool {
	if isReservedBentooSecret(name) {
		return false
	}
	if strings.HasPrefix(name, allowedHeaderEnvPrefix) {
		return true
	}
	_, ok := allowedHeaderEnvAllowList[name]
	return ok
}

// repoVarNamePrefix and repoVarNameSuffix frame the per-repository
// token name BENTOO_REPO_<NAME>_TOKEN (internal/common/config.repoTokenEnvName,
// cmd/bentoo.repoTokenName).
const (
	repoVarNamePrefix = "BENTOO_REPO_"
	repoVarNameSuffix = "_TOKEN"
)

// isReservedBentooSecret reports whether name is one of bentoolkit's own
// secrets: the ntfy token and SMTP password (internal/snapshot/notify.go) or a
// per-repository token. They carry the BENTOO_ prefix, yet must never be
// expanded into a header (S052-R1.9): R1.4 binds a BENTOO_* variable to the
// host of the record's own url, and the record's author picks that url, so a
// packages.toml PR could otherwise send them to a host of its choosing.
// Authenticated-fetch secrets (authFetchSecretPrefix, bare prefix included) are
// reserved too: they travel only to their own record's fetch_url (S068-R3.1).
func isReservedBentooSecret(name string) bool {
	switch name {
	case "BENTOO_NTFY_TOKEN", "BENTOO_SMTP_PASSWORD":
		return true
	}
	if strings.HasPrefix(name, authFetchSecretPrefix) {
		return true
	}
	return len(name) > len(repoVarNamePrefix)+len(repoVarNameSuffix) &&
		strings.HasPrefix(name, repoVarNamePrefix) &&
		strings.HasSuffix(name, repoVarNameSuffix)
}

// Credential host binding (S052-R1).
//
// The allow-list above decides WHETHER a variable may be expanded; it says
// nothing about WHERE the result goes. packages.toml lives in the overlay
// repository, so without a binding one contributor record pairing
// url = "https://evil.example" with X-Api-Key = "${GITHUB_TOKEN}" would ship the
// maintainer's token to that host. Every allow-listed variable is therefore
// bound to a set of hosts, and a request to any other host is refused before
// it is built into a network call:
//
//   - GITHUB_TOKEN: https only, and exactly the hosts in githubCredentialHosts.
//   - GITLAB_TOKEN: https only, and exactly gitlab.com. A self-hosted GitLab
//     uses a BENTOO_* variable instead.
//   - BENTOO_*: the package's own hosts (its url and base_url, see
//     credentialScope), by hostname only — a user's own server may be plain
//     http, so the scheme is not checked.
//
// Hostnames are compared with url.URL.Hostname(), case-insensitively and
// exactly: no subdomain match, port ignored (S052-R1.7). The decision is made
// from the variable's NAME, never from its value, so a record is refused
// whether or not the variable is set on the machine running it (S052-R1.6).

// githubCredentialHosts is the set of hostnames GITHUB_TOKEN may be sent to.
var githubCredentialHosts = []string{
	"api.github.com",
	"github.com",
	"codeload.github.com",
	"objects.githubusercontent.com",
	"raw.githubusercontent.com",
}

// gitlabCredentialHost is the one hostname GITLAB_TOKEN may be sent to.
const gitlabCredentialHost = "gitlab.com"

// CredentialScope carries the hostnames a BENTOO_* credential may be sent to:
// the hosts of the package's own url and base_url, or, for a caller with no
// package (GetWithHeadersContext), the request's own host (S052-R1.5).
type CredentialScope struct {
	OwnHosts []string
}

// requestOwnScope is the scope of a request made without a package: the
// request URL's own hostname is the package host (S052-R1.5). An unparseable
// URL yields an empty scope, so a BENTOO_* reference to it is refused.
func requestOwnScope(rawURL string) CredentialScope {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return CredentialScope{}
	}
	return CredentialScope{OwnHosts: []string{u.Hostname()}}
}

// credentialRef is one ${VAR} reference to an allow-listed variable inside an
// expansion-eligible header.
type credentialRef struct {
	header   string // canonical header name
	variable string
}

// credentialRefs lists, in a deterministic order, every reference in headers
// that SubstituteEnvVars would be allowed to expand. A reference in a header
// outside the header allow-list is passed through literally by
// SubstituteEnvVars, carries no credential, and is not listed (S052-R1.8).
func credentialRefs(headers map[string]string) []credentialRef {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	slices.Sort(names)

	var refs []credentialRef
	for _, name := range names {
		if !IsAllowedHeaderName(name) {
			continue
		}
		canonical := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
		for _, m := range envVarPattern.FindAllStringSubmatch(headers[name], -1) {
			if isAllowedEnvVar(m[1]) {
				refs = append(refs, credentialRef{header: canonical, variable: m[1]})
			}
		}
	}
	return refs
}

// CheckCredentialBinding returns an error wrapping ErrCredentialHostMismatch
// when a header in headers references an allow-listed variable that is not
// bound to rawURL's host. It is a pure check: it reads no environment variable
// and performs no I/O, and its error never carries a variable's value.
func CheckCredentialBinding(rawURL string, headers map[string]string, scope CredentialScope) error {
	refs := credentialRefs(headers)
	if len(refs) == 0 {
		return nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		// url.Error repeats the whole URL, query included; keep only its cause
		// so a credential carried in the query cannot reach the message.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("%w: header %s references ${%s}, but the request URL cannot be parsed to check its host: %w",
			ErrCredentialHostMismatch, refs[0].header, refs[0].variable, err)
	}

	host := u.Hostname()
	for _, ref := range refs {
		bound, allowed := credentialBinding(ref.variable, u.Scheme, host, scope)
		if !allowed {
			return fmt.Errorf("%w: header %s references ${%s}, which is bound to %s; refusing to send it to %s",
				ErrCredentialHostMismatch, ref.header, ref.variable, bound, u.Host)
		}
	}
	return nil
}

// credentialBinding reports where variable may be sent, as text for the
// refusal, and whether a request with the given scheme and hostname is one of
// those places. variable must already satisfy isAllowedEnvVar.
func credentialBinding(variable, scheme, host string, scope CredentialScope) (string, bool) {
	switch variable {
	case "GITHUB_TOKEN":
		return "https://" + strings.Join(githubCredentialHosts, ", https://"),
			scheme == "https" && hostIn(host, githubCredentialHosts)
	case "GITLAB_TOKEN":
		return "https://" + gitlabCredentialHost,
			scheme == "https" && strings.EqualFold(host, gitlabCredentialHost)
	default:
		if len(scope.OwnHosts) == 0 {
			return "the package's own url or base_url host (none could be read)", false
		}
		return "the package's own host (" + strings.Join(scope.OwnHosts, ", ") + ")",
			hostIn(host, scope.OwnHosts)
	}
}

// hostIn reports whether host equals one of hosts, case-insensitively. An
// empty host matches nothing.
func hostIn(host string, hosts []string) bool {
	if host == "" {
		return false
	}
	return slices.ContainsFunc(hosts, func(h string) bool { return strings.EqualFold(h, host) })
}
