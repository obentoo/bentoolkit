// Package autoupdate provides HTTP client with retry logic for version checking.
package autoupdate

import (
	"net/textproto"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// Header env-var expansion allow-list (R1, AD-8).
//
// A malicious packages.toml must not be able to exfiltrate arbitrary process
// secrets (e.g. ANTHROPIC_API_KEY) by embedding ${VAR} in any header value.
// Expansion is therefore allow-listed on TWO axes:
//
//  1. the header name must be one of a small fixed set of auth headers, and
//  2. the referenced environment variable must be explicitly allow-listed
//     (either a known token name or carry the BENTOO_ prefix).
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
var allowedHeaderEnvAllowList = map[string]struct{}{
	"GITHUB_TOKEN":      {},
	"GITLAB_TOKEN":      {},
	"OPENAI_API_KEY":    {},
	"ANTHROPIC_API_KEY": {},
}

// allowedHeaderEnvPrefix is the prefix that opts an environment variable into
// header expansion regardless of allowedHeaderEnvAllowList membership.
const allowedHeaderEnvPrefix = "BENTOO_"

// warnLogf is the sink used to emit Warn-level diagnostics from the env-var
// substitution path. It defaults to the shared logger and is a package-private
// variable so tests can capture the emitted lines. Its signature mirrors
// logger.Warn exactly.
var warnLogf = logger.Warn

// containsCRLF reports whether s contains a carriage return or line feed.
// Such characters in a header name are a header/CRLF-injection vector and are
// always rejected, independently of canonicalisation.
func containsCRLF(s string) bool {
	return strings.ContainsAny(s, "\r\n")
}

// isAllowedHeaderName reports whether the given header name is eligible for
// environment-variable expansion of its value.
//
// The name is trimmed of surrounding whitespace and canonicalised with
// textproto.CanonicalMIMEHeaderKey before the allow-list lookup, so callers may
// pass values with arbitrary casing or padding. Any name containing a CR or LF
// byte is rejected outright (defence against CRLF/header injection) BEFORE
// canonicalisation, because textproto.CanonicalMIMEHeaderKey returns its input
// unchanged when it contains invalid bytes.
func isAllowedHeaderName(name string) bool {
	// CR/LF check first and independently of canonicalisation.
	if containsCRLF(name) {
		return false
	}
	canonical := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
	_, ok := allowedExpansionHeaders[canonical]
	return ok
}

// isAllowedEnvVar reports whether the given environment variable name may be
// expanded inside an allow-listed header value. It is true when the name
// carries the allowedHeaderEnvPrefix or is an explicit allow-list entry.
func isAllowedEnvVar(name string) bool {
	if strings.HasPrefix(name, allowedHeaderEnvPrefix) {
		return true
	}
	_, ok := allowedHeaderEnvAllowList[name]
	return ok
}
