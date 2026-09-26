// Package httputil provides centralized outbound HTTP transport tuning for
// the bentoolkit. It exposes a helper for constructing a consistently
// configured *http.Transport so every HTTP client in the codebase shares the
// same connection-pool limits, timeouts, and HTTP/2 behavior, and a shared
// redirect policy, CredentialRedirectPolicy, that keeps credential headers
// from following a redirect to another host or to plain http.
//
// The package depends only on the Go standard library; it intentionally adds
// no third-party imports.
package httputil
