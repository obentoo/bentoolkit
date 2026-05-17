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
		t.ForceAttemptHTTP2 = false
		// A non-nil (empty) TLSNextProto map disables automatic HTTP/2 in the
		// standard library: no ALPN protocol handlers are registered.
		t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}

	return t
}
