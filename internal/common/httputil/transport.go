package httputil

import (
	"crypto/tls"
	"net/http"
	"os"
	"time"
)

const (
	// MaxBodyBytes is the maximum number of bytes that should be read from an
	// HTTP response body. Callers are expected to wrap response bodies with an
	// io.LimitReader (or http.MaxBytesReader) bounded by this value to guard
	// against unbounded memory growth from oversized or malicious responses.
	MaxBodyBytes int64 = 10 * 1024 * 1024 // 10 MiB

	// EnvDisableHTTP2 is the name of the environment variable that, when set to
	// "1", forces BuildTransport to disable HTTP/2 negotiation. This provides an
	// operational escape hatch for environments where HTTP/2 misbehaves.
	EnvDisableHTTP2 = "BENTOO_DISABLE_HTTP2"
)

// BuildTransport returns a freshly constructed *http.Transport tuned with the
// bentoolkit's standard connection-pool limits and timeouts.
//
// The returned transport sets MaxIdleConnsPerHost, MaxConnsPerHost,
// IdleConnTimeout, TLSHandshakeTimeout, ExpectContinueTimeout, and enables
// ForceAttemptHTTP2.
//
// When the environment variable named by EnvDisableHTTP2 is set to "1", HTTP/2
// is disabled: ForceAttemptHTTP2 is set to false and TLSNextProto is set to a
// non-nil empty map, which is the standard-library idiom for opting out of
// automatic HTTP/2 upgrades.
//
// Each call returns an independent transport; callers own the returned value
// and may mutate it further before use.
func BuildTransport() *http.Transport {
	t := &http.Transport{
		MaxIdleConnsPerHost:   16,
		MaxConnsPerHost:       32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	if os.Getenv(EnvDisableHTTP2) == "1" {
		disableHTTP2(t)
	}

	return t
}

// BuildTransportHTTP1 returns a transport identical to BuildTransport's except
// that HTTP/2 is always disabled, regardless of EnvDisableHTTP2.
//
// Some WAF-fronted upstreams (notably Cloudflare) fingerprint the HTTP/2
// connection preface and challenge Go's standard-library client with an HTTP
// 403 interstitial no matter what User-Agent it sends, while serving the exact
// same request over HTTP/1.1. This transport is the escape hatch used to retry
// such a request; see the HTTP/1.1 fallback in the autoupdate HTTP client.
func BuildTransportHTTP1() *http.Transport {
	t := BuildTransport()
	disableHTTP2(t)
	return t
}

// disableHTTP2 opts a transport out of HTTP/2 negotiation. A non-nil (empty)
// TLSNextProto map is the standard-library idiom for doing so: no ALPN protocol
// handlers are registered.
func disableHTTP2(t *http.Transport) {
	t.ForceAttemptHTTP2 = false
	t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
}
