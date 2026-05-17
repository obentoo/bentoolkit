// Package httputil provides centralized outbound HTTP transport tuning for
// the bentoolkit. It exposes a single helper for constructing a consistently
// configured *http.Transport so every HTTP client in the codebase shares the
// same connection-pool limits, timeouts, and HTTP/2 behavior.
//
// The package depends only on the Go standard library; it intentionally adds
// no third-party imports.
package httputil
