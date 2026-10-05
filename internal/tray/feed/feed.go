// Package feed fetches the bentoo notices feed over HTTPS with ETag
// revalidation, a body size cap and a request deadline. It is a library
// package: it returns errors carrying the URL, the status and the byte count,
// and leaves logging to its caller.
package feed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/httpx"
	"github.com/obentoo/bentoolkit/internal/common/version"
	"github.com/obentoo/bentoolkit/internal/notices"
)

const (
	// MaxBodyBytes is the largest feed body accepted (R2.8).
	MaxBodyBytes int64 = 1 << 20

	// Timeout bounds a whole fetch made with the default client (R2.8).
	Timeout = 30 * time.Second

	// drainLimit bounds how much of an unread body is discarded before the
	// body is closed, so a connection can be reused without letting a hostile
	// server make the drain itself unbounded.
	drainLimit int64 = 64 << 10
)

var (
	// ErrInsecureURL is returned by New for a feed URL that is not https (R2.9).
	ErrInsecureURL = errors.New("feed URL is not https")

	// ErrTooLarge is returned by Fetch when the body exceeds MaxBodyBytes (R2.8).
	ErrTooLarge = errors.New("feed body exceeds 1 MiB")
)

// Result is the outcome of a fetch that received a response. Status and Bytes
// are set whenever a response arrived, including alongside an error, so the
// caller can log them on every outcome (R13.3).
type Result struct {
	NotModified bool
	ETag        string
	Feed        notices.Feed
	Status      int
	Bytes       int64
}

// StatusError reports a response whose status is neither 200 nor 304. Bytes is
// the size of the body read, and RetryAfter the server's Retry-After header as
// a duration (zero when absent, unparsable or in the past).
type StatusError struct {
	Status     int
	RetryAfter time.Duration
	Bytes      int64
}

func (e *StatusError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("unexpected HTTP status %d (%d bytes, retry after %s)", e.Status, e.Bytes, e.RetryAfter)
	}
	return fmt.Sprintf("unexpected HTTP status %d (%d bytes)", e.Status, e.Bytes)
}

// Fetcher performs conditional GETs of one feed URL.
type Fetcher struct {
	url       string
	client    *http.Client
	userAgent string
}

// New returns a Fetcher for rawURL. A URL that is not an absolute https URL
// with a host is refused with ErrInsecureURL naming it (R2.9). A nil client is
// replaced by the default: a 30 s timeout, the shared transport and the
// credential-safe redirect policy. An empty userAgent is replaced by
// "bentoo-tray/<version>" (R2.10).
func New(rawURL string, client *http.Client, userAgent string) (*Fetcher, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrInsecureURL, rawURL)
	}
	if client == nil {
		client = DefaultClient()
	}
	if userAgent == "" {
		userAgent = DefaultUserAgent()
	}
	return &Fetcher{url: rawURL, client: client, userAgent: userAgent}, nil
}

// DefaultClient returns the client New uses when given none.
func DefaultClient() *http.Client {
	return &http.Client{
		Timeout:       Timeout,
		Transport:     httpx.BuildTransport(),
		CheckRedirect: httpx.CredentialRedirectPolicy,
	}
}

// DefaultUserAgent returns "bentoo-tray/<version>" (R2.10).
func DefaultUserAgent() string {
	return "bentoo-tray/" + version.Short()
}

// Fetch GETs the feed, sending etag as If-None-Match when it is not empty
// (R2.3). A 304 yields a Result with NotModified set and no feed (R2.12); a
// 200 yields the parsed feed and the response's ETag. Any other status is a
// *StatusError; a body past MaxBodyBytes is ErrTooLarge; a 200 whose body is
// not a valid feed is an error wrapping notices.ErrInvalidFeed. Every error
// names the URL, and every response, failed or not, reports Status and Bytes.
func (f *Fetcher) Fetch(ctx context.Context, etag string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return Result{}, fmt.Errorf("fetch %q: build request: %w", f.url, err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("fetch %q: %w", f.url, err)
	}
	// Drain a bounded remainder so the connection can be reused, then close.
	// Both errors are ignored: neither can change the fetch's outcome.
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
		_ = resp.Body.Close()
	}()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	res := Result{Status: resp.StatusCode, Bytes: int64(len(body))}

	switch {
	case resp.StatusCode == http.StatusNotModified:
		// A 304 carries no body worth reading; a read error on it changes
		// nothing about the outcome.
		res.NotModified = true
		res.ETag = etag
		if v := resp.Header.Get("ETag"); v != "" {
			res.ETag = v
		}
		return res, nil
	case resp.StatusCode != http.StatusOK:
		return res, fmt.Errorf("fetch %q: %w", f.url, &StatusError{
			Status:     resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
			Bytes:      res.Bytes,
		})
	case readErr != nil:
		return res, fmt.Errorf("fetch %q: read body: %w", f.url, readErr)
	case res.Bytes > MaxBodyBytes:
		return res, fmt.Errorf("fetch %q: %w", f.url, ErrTooLarge)
	}

	parsed, err := notices.ParseFeed(body)
	if err != nil {
		return res, fmt.Errorf("fetch %q: %w", f.url, err)
	}
	res.Feed = parsed
	res.ETag = resp.Header.Get("ETag")
	return res, nil
}

// parseRetryAfter reads a Retry-After value as delay-seconds or an HTTP date
// (RFC 9110 §10.2.3). An empty, unparsable or past value yields zero.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		// Clamp before multiplying so a huge value cannot overflow.
		if secs > int64(maxDuration/time.Second) {
			return maxDuration
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

const maxDuration = time.Duration(1<<63 - 1)
