// Package autoupdate provides HTTP client with retry logic for version checking.
package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used -- retry jitter (defaultJitter), not a secret
	"net/http"
	"net/textproto"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/httputil"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/version"
	"github.com/sony/gobreaker"
)

// Error variables for HTTP client errors
var (
	// ErrMaxRetriesExceeded is returned when all retry attempts have failed
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")
	// ErrRequestTimeout is returned when a request times out
	ErrRequestTimeout = errors.New("request timeout")
	// ErrRetryAfterTooLong is returned, without waiting, when an upstream's
	// Retry-After asks for more than MaxRetryAfter or for more than the time
	// left before the context deadline.
	ErrRetryAfterTooLong = errors.New("retry-after too long")
	// ErrResponseTooLarge is returned when an HTTP response body exceeds the
	// MaxBodyBytes cap. It is httputil's sentinel, assigned rather than copied,
	// so errors.Is matches it on the provider paths too.
	ErrResponseTooLarge = httputil.ErrResponseTooLarge
	// ErrCredentialHostMismatch is returned, before any network I/O, when a
	// header references a credential variable that is bound to hosts other
	// than the one the request goes to (S052-R1.2).
	ErrCredentialHostMismatch = errors.New("credential header bound to another host")
)

// MaxRetryAfter is the longest Retry-After the client waits out. A longer one
// fails the request at once instead of holding a check for minutes.
const MaxRetryAfter = 60 * time.Second

// maxDurationSeconds is the largest delta-seconds value that converts to a
// time.Duration without overflowing int64 (about 292 years). It is an
// arithmetic bound, not a policy: MaxRetryAfter is the limit that applies.
const maxDurationSeconds = math.MaxInt64 / int64(time.Second)

// retryableStatusError records a retryable HTTP status and the response's
// Retry-After header. It is built where the response is drained — inside the
// circuit breaker's callback when the breaker is on — because the header is
// gone once the body is closed and the response dropped.
type retryableStatusError struct {
	status     int
	retryAfter string
}

func (e *retryableStatusError) Error() string {
	return fmt.Sprintf("server error: status %d", e.status)
}

// parseRetryAfter reads a Retry-After value as of now. Delta-seconds is one or
// more ASCII digits (RFC 9110: delay-seconds = 1*DIGIT), so a sign, a fraction
// or trailing text makes the value unparseable rather than a number; anything
// that is not delta-seconds must be an HTTP-date (http.ParseTime), and a date
// already past means no wait. ok is false for an absent or unparseable value.
//
// A delta too large for a time.Duration saturates to the longest Duration
// instead of wrapping, so retryDelay sees it as over MaxRetryAfter and fails
// the request at once. strconv.Atoi reports such a delta as ErrRange together
// with the largest value; that reading is trusted only because the string was
// checked to be all digits first — Atoi also reports ErrRange for a string
// whose leading digits overflow before a non-digit.
func parseRetryAfter(h string, now time.Time) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, false
	}
	if isASCIIDigits(h) {
		secs, err := strconv.Atoi(h)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return 0, false
		}
		if int64(secs) > maxDurationSeconds {
			return time.Duration(math.MaxInt64), true
		}
		return time.Duration(secs) * time.Second, true
	}
	at, err := http.ParseTime(h)
	if err != nil {
		return 0, false
	}
	if d := at.Sub(now); d > 0 {
		return d, true
	}
	return 0, true
}

// isASCIIDigits reports whether s is one or more of the characters 0-9.
func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// envVarPattern matches ${VAR_NAME} syntax for environment variable substitution
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

const (
	// DefaultMaxRetries is the default number of retry attempts.
	DefaultMaxRetries = 3
	// DefaultRetryBaseDelay is the default base delay before the first retry.
	DefaultRetryBaseDelay = 1 * time.Second
	// DefaultHTTPTimeout is the default timeout for each HTTP request.
	DefaultHTTPTimeout = 30 * time.Second

	// DefaultBreakerMaxFailures is the number of consecutive failures before the circuit opens.
	DefaultBreakerMaxFailures = 5
	// DefaultBreakerTimeout is how long the circuit stays open before allowing a probe.
	DefaultBreakerTimeout = 30 * time.Second
	// DefaultBreakerMaxRequests is the number of probes allowed in half-open state.
	DefaultBreakerMaxRequests = 3
	// DefaultBreakerInterval is the window over which failures are counted.
	DefaultBreakerInterval = 60 * time.Second
)

// RetryConfig holds configuration for retry behavior.
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts (default: 3)
	MaxRetries int
	// BaseDelay is the initial delay before first retry (default: 1s)
	BaseDelay time.Duration
	// MaxDelay is the maximum delay between retries (default: 4s)
	MaxDelay time.Duration
	// Timeout is the timeout for each individual request (default: 30s)
	Timeout time.Duration
}

// DefaultRetryConfig returns the default retry configuration.
// Uses exponential backoff with delays of 1s, 2s, 4s.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: DefaultMaxRetries,
		BaseDelay:  DefaultRetryBaseDelay,
		MaxDelay:   4 * time.Second,
		Timeout:    DefaultHTTPTimeout,
	}
}

// RetryableHTTPClient wraps an HTTP client with retry logic and an optional circuit breaker.
// It implements exponential backoff for failed requests and prevents cascading failures
// via a circuit breaker that opens after repeated failures.
type RetryableHTTPClient struct {
	client *http.Client
	config RetryConfig
	// breakersEnabled turns the per-host circuit breakers on (the default).
	breakersEnabled bool
	// breakerMu guards breakers: CheckAll workers share one client.
	breakerMu sync.Mutex
	// breakers holds one circuit breaker per upstream host:port (URL.Host), so
	// a failing upstream refuses only its own requests. Hosts per run are
	// bounded by the package registry, so the map is never evicted.
	breakers map[string]*gobreaker.CircuitBreaker
	// newBreaker builds the breaker for a host on first use (a test seam;
	// defaults to newDefaultBreaker).
	newBreaker func(name string) *gobreaker.CircuitBreaker
	// delayFunc replaces the retry wait when set (a test seam; see
	// SetDelayFunc). Nil means wait on a timer that stops on cancellation.
	delayFunc func(time.Duration)
	// jitter draws the wait before a retry from [0, ceiling]. Nil means
	// defaultJitter; tests replace it to make waits exact.
	jitter func(ceiling time.Duration) time.Duration
	// recordedDelays stores delays for testing purposes
	recordedDelays []time.Duration
	// defaultHeaders are headers applied to all requests
	defaultHeaders map[string]string
	// githubToken is the GitHub API token for authentication
	githubToken string
	// h1Client performs the HTTP/1.1 fallback retry (nil disables the fallback)
	h1Client *http.Client
	// log receives the client's diagnostics. It is never nil: a client built
	// without SetLogger discards them.
	log *slog.Logger
}

// newDefaultBreaker creates a circuit breaker with the default settings, named
// after the upstream host:port it guards.
func newDefaultBreaker(name string) *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        name,
		MaxRequests: DefaultBreakerMaxRequests,
		Interval:    DefaultBreakerInterval,
		Timeout:     DefaultBreakerTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= DefaultBreakerMaxFailures
		},
	})
}

// NewRetryableHTTPClient creates a new HTTP client with retry support and circuit breaker.
// Uses the default retry configuration.
func NewRetryableHTTPClient() *RetryableHTTPClient {
	return NewRetryableHTTPClientWithConfig(DefaultRetryConfig())
}

// NewRetryableHTTPClientWithConfig creates a new HTTP client with custom retry configuration.
// The circuit breaker is enabled by default.
func NewRetryableHTTPClientWithConfig(config RetryConfig) *RetryableHTTPClient {
	return &RetryableHTTPClient{
		// Both clients follow redirects under the credential-safe policy, so a
		// redirect never carries a credential header to another host or to
		// plain http (S052-R4.6).
		client: &http.Client{
			Timeout:       config.Timeout,
			Transport:     httputil.BuildTransport(),
			CheckRedirect: httputil.CredentialRedirectPolicy,
		},
		h1Client: &http.Client{
			Timeout:       config.Timeout,
			Transport:     httputil.BuildTransportHTTP1(),
			CheckRedirect: httputil.CredentialRedirectPolicy,
		},
		config:          config,
		breakersEnabled: true,
		breakers:        make(map[string]*gobreaker.CircuitBreaker),
		newBreaker:      newDefaultBreaker,
		jitter:          defaultJitter,
		log:             logging.OrDiscard(nil),
		defaultHeaders: map[string]string{
			"User-Agent": defaultUserAgent(),
		},
	}
}

// defaultUserAgent returns the User-Agent applied to every autoupdate HTTP
// request. A descriptive UA avoids Go's default "Go-http-client/1.1" string,
// which many WAF/Cloudflare-fronted upstreams reject outright with HTTP 403.
func defaultUserAgent() string {
	return "bentoolkit/" + version.Short()
}

// WithCircuitBreaker enables or disables the per-host circuit breakers on this
// client. Either way every breaker built so far is dropped, so enabling starts
// each host from a closed breaker. Pass false to disable circuit breaker
// behavior entirely.
func (c *RetryableHTTPClient) WithCircuitBreaker(enabled bool) *RetryableHTTPClient {
	c.breakerMu.Lock()
	defer c.breakerMu.Unlock()
	c.breakersEnabled = enabled
	c.breakers = make(map[string]*gobreaker.CircuitBreaker)
	return c
}

// breakerFor returns the circuit breaker guarding host (a URL.Host, port
// included), creating it on first use, or nil when breakers are disabled. The
// key keeps the port because upstreams that share a hostname — every httptest
// server on 127.0.0.1 among them — are different upstreams.
func (c *RetryableHTTPClient) breakerFor(host string) *gobreaker.CircuitBreaker {
	c.breakerMu.Lock()
	defer c.breakerMu.Unlock()
	if !c.breakersEnabled {
		return nil
	}
	if cb, ok := c.breakers[host]; ok {
		return cb
	}
	build := c.newBreaker
	if build == nil {
		build = newDefaultBreaker
	}
	if c.breakers == nil {
		c.breakers = make(map[string]*gobreaker.CircuitBreaker)
	}
	cb := build(host)
	c.breakers[host] = cb
	return cb
}

// SetHTTPClient sets a custom underlying HTTP client (useful for testing).
//
// The HTTP/1.1 fallback is disabled by this call: a caller that supplies its own
// client (a test transport, a stub, a proxy-aware client) owns the transport
// entirely, and silently re-issuing a request through a bentoolkit-built
// transport would bypass it. Re-enable the fallback explicitly with
// SetHTTP1FallbackClient if it is wanted.
func (c *RetryableHTTPClient) SetHTTPClient(client *http.Client) {
	c.client = client
	c.h1Client = nil
}

// SetLogger sets the logger the client reports its diagnostics to. Passing nil
// discards them.
func (c *RetryableHTTPClient) SetLogger(l *slog.Logger) {
	c.log = logging.OrDiscard(l)
}

// logger returns the client's logger, or a discarding one for a zero-value
// client that was not built by a constructor.
func (c *RetryableHTTPClient) logger() *slog.Logger {
	return logging.OrDiscard(c.log)
}

// SetHTTP1FallbackClient sets the client used for the HTTP/1.1 fallback retry.
// Passing nil disables the fallback.
func (c *RetryableHTTPClient) SetHTTP1FallbackClient(client *http.Client) {
	c.h1Client = client
}

// SetRequestTimeout sets the per-request timeout (the cap on a single attempt)
// on both the retry config and the underlying http.Client. Because the standard
// library resets this timer on every Do call, each retry attempt gets a fresh
// budget — so as long as the caller's per-operation deadline is larger than this
// value, the retry loop can actually run its attempts instead of the first slow
// request consuming the whole operation budget. A non-positive duration is
// ignored so callers can pass an unresolved value safely.
//
// The header wait of each *http.Transport is raised to d when d exceeds
// httputil.DefaultResponseHeaderTimeout, and never lowered, so a user's larger
// http_timeout is not silently capped by the transport default. Other
// RoundTrippers, and the shared http.DefaultTransport, are left alone.
func (c *RetryableHTTPClient) SetRequestTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	c.config.Timeout = d
	for _, hc := range []*http.Client{c.client, c.h1Client} {
		if hc == nil {
			continue
		}
		hc.Timeout = d
		// http.DefaultTransport is process-wide; mutating it would reach
		// every other client in the program.
		if tr, ok := hc.Transport.(*http.Transport); ok && hc.Transport != http.DefaultTransport {
			tr.ResponseHeaderTimeout = max(httputil.DefaultResponseHeaderTimeout, d)
		}
	}
}

// SetDelayFunc sets a custom delay function (useful for testing).
// The function receives the delay duration that would normally be waited, and
// runs in place of the wait; the context is checked once it returns.
func (c *RetryableHTTPClient) SetDelayFunc(fn func(time.Duration)) {
	c.delayFunc = fn
}

// SetJitterFunc replaces the source that draws each retry wait from
// [0, ceiling] (useful for testing). Passing nil restores defaultJitter.
func (c *RetryableHTTPClient) SetJitterFunc(fn func(ceiling time.Duration) time.Duration) {
	c.jitter = fn
}

// wait blocks for d before a retry, and returns early with the context's error
// as soon as ctx is done, so a cancelled operation never sleeps out its
// backoff. A non-positive d does not wait. When a delay function was set with
// SetDelayFunc it runs instead of the timer, and the context is checked after.
func (c *RetryableHTTPClient) wait(ctx context.Context, d time.Duration) error {
	if c.delayFunc != nil {
		c.delayFunc(d)
		return ctx.Err()
	}
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryDelay chooses the wait before retry attempt. A 429 or 503 whose
// Retry-After parses is obeyed exactly — unless it asks for more than
// MaxRetryAfter, or for more than the time left before ctx's deadline, in which
// case the request fails at once with ErrRetryAfterTooLong naming host and the
// wait. Any other failure, and an absent or unparseable header, gets the
// jittered backoff.
func (c *RetryableHTTPClient) retryDelay(ctx context.Context, host string, attempt int, lastErr error) (time.Duration, error) {
	var rse *retryableStatusError
	if errors.As(lastErr, &rse) &&
		(rse.status == http.StatusTooManyRequests || rse.status == http.StatusServiceUnavailable) {
		if d, ok := parseRetryAfter(rse.retryAfter, time.Now()); ok {
			if d > MaxRetryAfter {
				return 0, fmt.Errorf("%w: %s asked to retry after %s, over the %s limit",
					ErrRetryAfterTooLong, host, d, MaxRetryAfter)
			}
			if deadline, has := ctx.Deadline(); has {
				if left := time.Until(deadline); d > left {
					return 0, fmt.Errorf("%w: %s asked to retry after %s, over the %s left before the deadline",
						ErrRetryAfterTooLong, host, d, left.Round(time.Millisecond))
				}
			}
			return d, nil
		}
	}
	return c.calculateDelay(attempt), nil
}

// ctxStopError reports that the request to host stopped during phase because
// its context ended. The wording says "cancelled" or "deadline exceeded", and
// err stays reachable through errors.Is.
func ctxStopError(host, phase string, err error) error {
	how := "stopped"
	switch {
	case errors.Is(err, context.Canceled):
		how = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		how = "deadline exceeded"
	}
	return fmt.Errorf("request to %s %s during %s: %w", host, how, phase, err)
}

// GetRecordedDelays returns the delays that were recorded during requests.
// Only populated when using a custom delay function that records delays.
func (c *RetryableHTTPClient) GetRecordedDelays() []time.Duration {
	return c.recordedDelays
}

// ClearRecordedDelays clears the recorded delays.
func (c *RetryableHTTPClient) ClearRecordedDelays() {
	c.recordedDelays = nil
}

// recordDelay records a delay for testing purposes.
func (c *RetryableHTTPClient) recordDelay(d time.Duration) {
	c.recordedDelays = append(c.recordedDelays, d)
}

// Do executes an HTTP request with retry logic.
// It retries on network errors and 5xx server errors with exponential backoff.
// Returns the response and any error encountered after all retries are exhausted.
func (c *RetryableHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.DoWithContext(req.Context(), req)
}

// DoWithContext executes an HTTP request with retry logic, context support, and circuit
// breaker protection. Each individual attempt is wrapped by the circuit breaker so that
// consecutive failures cause the circuit to open and subsequent requests fail fast.
func (c *RetryableHTTPClient) DoWithContext(ctx context.Context, req *http.Request) (*http.Response, error) {
	var lastErr error
	var lastResp *http.Response

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		// Check context cancellation before each attempt
		if err := ctx.Err(); err != nil {
			return nil, ctxStopError(req.URL.Host, "request", err)
		}

		// Wait before a retry (not before the first attempt); a cancelled or
		// expired context ends the wait, and the operation, at once.
		if attempt > 0 {
			delay, err := c.retryDelay(ctx, req.URL.Host, attempt, lastErr)
			if err != nil {
				return nil, err
			}
			c.recordDelay(delay)
			if err := c.wait(ctx, delay); err != nil {
				return nil, ctxStopError(req.URL.Host, "retry wait", err)
			}
		}

		// Clone the request for retry (body needs to be re-readable)
		reqCopy := req.Clone(ctx)

		// Execute the request, optionally wrapped in the circuit breaker
		resp, err := c.executeRequest(reqCopy)
		if err != nil {
			// Propagate circuit-breaker open errors immediately (no retries)
			if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
				return nil, err
			}
			// A refused https -> http redirect is the redirect policy's verdict
			// on the request, not a transient failure: retrying would only
			// re-send the credential to the original host (S052-R4.4).
			if errors.Is(err, httputil.ErrInsecureRedirect) {
				return nil, err
			}
			// An attempt that failed because the operation's own context
			// ended is not an upstream failure: report the cancellation or
			// deadline, never "max retries exceeded".
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxStopError(req.URL.Host, "request", ctxErr)
			}
			lastErr = err
			// Check if it's a timeout error
			if isTimeoutError(err) {
				lastErr = fmt.Errorf("%w: %w", ErrRequestTimeout, err)
			}
			continue
		}

		// Check if we should retry based on status code
		if c.shouldRetry(resp.StatusCode) {
			// Close the response body before retrying
			if resp.Body != nil {
				io.Copy(io.Discard, resp.Body) //nolint:errcheck // discarding response body, error is irrelevant
				resp.Body.Close()
			}
			lastErr = &retryableStatusError{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After")}
			lastResp = resp
			continue
		}

		// A 403 over HTTP/2 is very often a WAF fingerprint challenge rather than
		// a genuine authorization failure; retry once over HTTP/1.1 before
		// accepting it (see retryOverHTTP1).
		if resp.StatusCode == http.StatusForbidden {
			if h1Resp := c.retryOverHTTP1(ctx, req, resp); h1Resp != nil {
				return h1Resp, nil
			}
		}

		// Success or non-retryable error
		return resp, nil
	}

	// All retries exhausted
	if lastErr != nil {
		return lastResp, fmt.Errorf("%w: %w", ErrMaxRetriesExceeded, lastErr)
	}
	return lastResp, ErrMaxRetriesExceeded
}

// retryOverHTTP1 re-issues req over HTTP/1.1 after an HTTP/2 attempt came back
// with a 403, and returns the new response — or nil to keep h2Resp, the original
// response, which the caller still owns and must not have consumed.
//
// Cloudflare (and other WAFs) fingerprint the HTTP/2 connection preface, and
// challenge Go's standard-library client with a "Just a moment..." 403
// interstitial regardless of the User-Agent it sends. The very same request over
// HTTP/1.1 is served normally. claude.ai's desktop release endpoint behaves
// exactly this way, and a fix that only rewrites the User-Agent cannot help.
//
// The fallback is skipped — nil is returned, and the original 403 stands — when:
//   - it is disabled (h1Client is nil, e.g. after SetHTTPClient),
//   - the response did not come over HTTP/2, so HTTP/1.1 is not a new signal,
//   - the request carries a body that cannot be replayed (no GetBody), or
//   - the HTTP/1.1 attempt fails at the transport level.
//
// The retry deliberately bypasses the circuit breaker and the retry loop: it is
// a single extra request on an error path that already lost the h2 attempt, so
// it must not amplify load or trip the breaker on its own.
func (c *RetryableHTTPClient) retryOverHTTP1(ctx context.Context, req *http.Request, h2Resp *http.Response) *http.Response {
	if c.h1Client == nil || h2Resp.ProtoMajor < 2 {
		return nil
	}
	if req.Body != nil && req.GetBody == nil {
		return nil
	}

	h1Req := req.Clone(ctx)
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil
		}
		h1Req.Body = body
	}

	h1Resp, err := c.h1Client.Do(h1Req)
	if err != nil {
		// Transport-level failure: the caller keeps the untouched h2 response.
		return nil
	}

	c.logger().Debug("HTTP/2 request returned 403; HTTP/1.1 fallback answered",
		"url", req.URL.Redacted(), "status", h1Resp.StatusCode)

	// The h2 response is being dropped in favour of this one, so drain and close
	// it to release the connection back to the pool.
	if h2Resp.Body != nil {
		io.Copy(io.Discard, h2Resp.Body) //nolint:errcheck // discarding a response we are replacing
		h2Resp.Body.Close()
	}

	return h1Resp
}

// executeRequest performs a single HTTP attempt, optionally through the
// circuit breaker of the request's own host:port.
func (c *RetryableHTTPClient) executeRequest(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	breaker := c.breakerFor(host)
	if breaker == nil {
		return c.client.Do(req)
	}

	result, err := breaker.Execute(func() (interface{}, error) {
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		// Treat retryable status codes as circuit-breaker failures
		if c.shouldRetry(resp.StatusCode) {
			if resp.Body != nil {
				io.Copy(io.Discard, resp.Body) //nolint:errcheck // best-effort drain so the connection can be reused; the retryable status is the error returned
				resp.Body.Close()
			}
			return nil, &retryableStatusError{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After")}
		}
		return resp, nil
	})

	if err != nil {
		if errors.Is(err, gobreaker.ErrOpenState) {
			return nil, fmt.Errorf("circuit breaker open for %s (upstream failing, next probe in %v): %w",
				host, DefaultBreakerTimeout, err)
		}
		if errors.Is(err, gobreaker.ErrTooManyRequests) {
			return nil, fmt.Errorf("circuit breaker open for %s (too many requests in half-open state): %w", host, err)
		}
		return nil, err
	}

	return result.(*http.Response), nil
}

// Get performs an HTTP GET request with retry logic. The request is bound to
// ctx, so cancelling it aborts the attempt in flight and any retry wait.
func (c *RetryableHTTPClient) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// GetWithContext performs an HTTP GET request with retry logic and context support.
//
// The returned response body is wrapped in an http.MaxBytesReader bounded by
// httputil.MaxBodyBytes (10 MiB). A subsequent read that exceeds the cap yields
// an *http.MaxBytesError; callers should pass such read errors through
// classifyBodyReadError so the overflow surfaces as ErrResponseTooLarge.
func (c *RetryableHTTPClient) GetWithContext(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.DoWithContext(ctx, req)
	if err != nil {
		return resp, err
	}
	if resp != nil && resp.Body != nil {
		// Cap the body so an oversized or malicious response cannot exhaust
		// memory when a caller reads it (S001-R11.1, AD-12).
		resp.Body = http.MaxBytesReader(nil, resp.Body, httputil.MaxBodyBytes)
	}
	return resp, nil
}

// classifyBodyReadError maps an error returned while reading an HTTP response
// body to a domain error. When the read tripped an http.MaxBytesReader cap the
// standard library yields an *http.MaxBytesError; this is translated into an
// error wrapping ErrResponseTooLarge (S001-R11.3). Any other non-nil error is
// returned unchanged, and a nil error yields nil. It delegates to
// httputil.ClassifyBodyReadError, which owns the shared sentinel.
func classifyBodyReadError(err error) error {
	return httputil.ClassifyBodyReadError(err)
}

// readBodyForStatus validates an HTTP response status against the accepted set
// supplied by the caller and, when the status is accepted, reads the body in
// full and classifies an overflow (S019-R3.1). A status outside the set yields
// "HTTP request returned status %d" without touching the body.
//
// The accepted statuses are the caller's to choose (S019-R3.2):
// Checker.fetchContent passes http.StatusOK plus http.StatusPartialContent when
// the request that was actually sent carried a Range header (the
// sentRequestDeclaresRange predicate in checker.go, which reads the request
// recorded on the response rather than the record's declared header map),
// Analyzer.fetchContentFromURL passes http.StatusOK alone. Both list codes
// explicitly rather than accepting the whole 2xx range, since 204 and 205 have
// an empty body by definition.
//
// This helper imposes no cap of its own: the GET helpers already wrap the body
// in an http.MaxBytesReader bounded by httputil.MaxBodyBytes, so the bound has a
// single source. A read that trips that cap is translated into an error wrapping
// ErrResponseTooLarge via classifyBodyReadError (S001-R11.3). The LLM path keeps
// its own per-client, raisable cap (readCappedBody in llm.go) and deliberately
// does not route through here (S019-R3.3, S019-UB3).
func readBodyForStatus(resp *http.Response, accepted ...int) ([]byte, error) {
	if !slices.Contains(accepted, resp.StatusCode) {
		return nil, fmt.Errorf("HTTP request returned status %d", resp.StatusCode)
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", classifyBodyReadError(err))
	}

	return content, nil
}

// calculateDelay draws the wait before a retry attempt: a uniform draw from
// [0, backoffCeiling(attempt)] (full jitter), where the ceiling is the
// exponential backoff baseDelay*2^(attempt-1) capped at MaxDelay — at most 1s,
// 2s and 4s for attempts 1, 2 and 3 by default.
func (c *RetryableHTTPClient) calculateDelay(attempt int) time.Duration {
	jitter := c.jitter
	if jitter == nil {
		jitter = defaultJitter
	}
	return jitter(c.backoffCeiling(attempt))
}

// backoffCeiling is the longest wait before retry attempt for this client's
// config; see backoffCeilingFor.
func (c *RetryableHTTPClient) backoffCeiling(attempt int) time.Duration {
	return backoffCeilingFor(c.config, attempt)
}

// backoffCeilingFor is the single source of the exponential backoff cap:
// BaseDelay×2^(attempt-1), capped at MaxDelay (1s, 2s, 4s, 4s… by default),
// and 0 for attempt <= 0. The retry wait draws below it and deriveOpTimeout
// sums it, so the operation budget always covers the longest possible waits.
func backoffCeilingFor(rc RetryConfig, attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	multiplier := 1 << (attempt - 1) // 2^(attempt-1): 1, 2, 4, ...
	delay := rc.BaseDelay * time.Duration(multiplier)
	if delay > rc.MaxDelay {
		delay = rc.MaxDelay
	}
	return delay
}

// defaultJitter draws a wait uniformly from [0, ceiling] (full jitter), so
// many packages retrying one host spread out instead of retrying in lockstep.
// A non-positive ceiling yields 0 (rand.N panics on a non-positive bound).
func defaultJitter(ceiling time.Duration) time.Duration {
	if ceiling <= 0 {
		return 0
	}
	return rand.N(ceiling + 1) //nolint:gosec // G404: retry jitter, not a secret
}

// shouldRetry determines if a request should be retried based on status code.
// Retries on 5xx server errors and 429 (Too Many Requests).
func (c *RetryableHTTPClient) shouldRetry(statusCode int) bool {
	// Retry on server errors (5xx)
	if statusCode >= 500 && statusCode < 600 {
		return true
	}
	// Retry on rate limiting
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	return false
}

// isTimeoutError checks if an error is a timeout error.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	// Check for context deadline exceeded
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Check for net.Error timeout
	type timeoutError interface {
		Timeout() bool
	}
	if te, ok := err.(timeoutError); ok {
		return te.Timeout()
	}
	return false
}

// Config returns the current retry configuration.
func (c *RetryableHTTPClient) Config() RetryConfig {
	return c.config
}

// SetGitHubToken sets the GitHub API token for authentication.
// When set, requests to GitHub API will include the Authorization header.
func (c *RetryableHTTPClient) SetGitHubToken(token string) {
	c.githubToken = token
}

// GetGitHubToken returns the configured GitHub token.
func (c *RetryableHTTPClient) GetGitHubToken() string {
	return c.githubToken
}

// SetDefaultHeaders sets default headers that will be applied to all requests.
// These headers are applied before any request-specific headers.
// Provided headers are merged into existing defaults so built-in headers
// (for example User-Agent) are preserved unless explicitly overridden.
func (c *RetryableHTTPClient) SetDefaultHeaders(headers map[string]string) {
	if c.defaultHeaders == nil {
		c.defaultHeaders = make(map[string]string)
	}
	for k, v := range headers {
		c.defaultHeaders[k] = v
	}
}

// GetDefaultHeaders returns the configured default headers.
func (c *RetryableHTTPClient) GetDefaultHeaders() map[string]string {
	return c.defaultHeaders
}

// GetWithHeaders performs an HTTP GET request with custom headers and retry logic.
// Headers are processed for environment variable substitution using ${VAR_NAME} syntax.
// If the URL is a GitHub API URL and a GitHub token is configured, it will be included.
//
// Callers that need cancellation should use GetWithHeadersContext directly.
func (c *RetryableHTTPClient) GetWithHeaders(url string, headers map[string]string) (*http.Response, error) {
	// This convenience wrapper is intentionally non-cancellable; the Checker and
	// Analyzer context spine (S001-R3) uses GetWithContext /
	// GetWithHeadersContext, which context-aware callers must use instead.
	//
	// The trailing "(R3)" on the return statement below is deliberately left
	// bare rather than qualified as S001-R3: that line carries the annotation
	// the audit-ctx Makefile target greps for to whitelist this deliberate
	// non-cancellable call, and it is kept byte-identical so the audit keeps
	// matching it (S020-UB5). The asymmetry with the line above is intentional;
	// do not "fix" it.
	return c.GetWithHeadersContext(context.Background(), url, headers) // SAFE: non-cancellable convenience wrapper (R3)
}

// GetWithHeadersContext performs an HTTP GET request with custom headers, context, and retry logic.
// Headers are processed for environment variable substitution using ${VAR_NAME} syntax.
// If the URL is a GitHub API URL and a GitHub token is configured, it will be included.
//
// The returned response body is wrapped in an http.MaxBytesReader bounded by
// httputil.MaxBodyBytes (10 MiB), exactly as GetWithContext does. A subsequent
// read that exceeds the cap yields an *http.MaxBytesError; callers should pass
// such read errors through classifyBodyReadError so the overflow surfaces as
// ErrResponseTooLarge. The cap holds even when a caller sent a Range header: a
// Range is only a request, and a server that ignores it and streams the full
// body is bounded here rather than at the caller's read (S019-R1.1, S019-R1.2).
//
// A header that references a credential variable bound to another host is
// refused before any network I/O with an error wrapping
// ErrCredentialHostMismatch. With no package to consult, the request URL's own
// host is taken as the package host for BENTOO_* variables (S052-R1.5).
func (c *RetryableHTTPClient) GetWithHeadersContext(ctx context.Context, url string, headers map[string]string) (*http.Response, error) {
	return c.getWithHeadersScopedContext(ctx, url, headers, requestOwnScope(url))
}

// getWithHeadersScopedContext is GetWithHeadersContext with the package's
// credential scope supplied by the caller (the checker passes the hosts of the
// package's url and base_url, S052-R1.4).
func (c *RetryableHTTPClient) getWithHeadersScopedContext(ctx context.Context, url string, headers map[string]string, scope credentialScope) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	// Apply headers to request; a credential bound elsewhere stops it here.
	if err := c.applyHeaders(req, url, headers, scope); err != nil {
		return nil, err
	}

	resp, err := c.DoWithContext(ctx, req)
	if err != nil {
		return resp, err
	}
	if resp != nil && resp.Body != nil {
		// Cap the body so an oversized or malicious response cannot exhaust
		// memory when a caller reads it (S019-R1.1, AD-12).
		resp.Body = http.MaxBytesReader(nil, resp.Body, httputil.MaxBodyBytes)
	}
	return resp, nil
}

// applyHeaders applies headers to a request in the following order:
// 1. Default headers (set via SetDefaultHeaders)
// 2. GitHub token (if URL is GitHub API and token is configured)
// 3. Custom headers (passed to the method)
// All header values are processed for environment variable substitution.
//
// Any header whose name contains a CR or LF byte is rejected and skipped as a
// defence against header/CRLF injection. The canonical header name is passed to
// SubstituteEnvVars so that ${VAR} expansion is gated by the header allow-list.
//
// Before anything is applied, the default and custom headers are checked
// against scope with checkCredentialBinding — both are expanded by setHeader,
// so both are bound; its error is returned unchanged and req is left untouched
// (S052-R1.2).
func (c *RetryableHTTPClient) applyHeaders(req *http.Request, url string, customHeaders map[string]string, scope credentialScope) error {
	for _, headers := range []map[string]string{c.defaultHeaders, customHeaders} {
		if err := checkCredentialBinding(url, headers, scope); err != nil {
			return err
		}
	}

	// Apply default headers first
	for key, value := range c.defaultHeaders {
		c.setHeader(req, key, value)
	}

	// Apply GitHub token for GitHub API requests
	if c.githubToken != "" && isGitHubAPIURL(url) {
		req.Header.Set("Authorization", "Bearer "+c.githubToken)
	}

	// Apply custom headers (can override defaults and GitHub token)
	for key, value := range customHeaders {
		c.setHeader(req, key, value)
	}
	return nil
}

// setHeader sets a single header on req after rejecting names that contain CR
// or LF (header/CRLF injection) and substituting allow-listed environment
// variables in the value. The canonical header name is used both for the
// allow-list lookup performed by SubstituteEnvVars and as the header key.
func (c *RetryableHTTPClient) setHeader(req *http.Request, name, value string) {
	if containsCRLF(name) {
		c.logger().Warn("rejecting header with CR/LF in its name (possible header injection)")
		return
	}
	canonical := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
	req.Header.Set(canonical, SubstituteEnvVars(c.logger(), value, canonical))
}

// SubstituteEnvVars replaces ${VAR_NAME} patterns in a header value with the
// corresponding environment variable values, subject to a strict allow-list
// (S001-R1, AD-8).
//
// A ${VAR} reference is expanded ONLY when BOTH of the following hold:
//   - headerName is an allow-listed header (see isAllowedHeaderName), AND
//   - VAR is an allow-listed environment variable (see isAllowedEnvVar).
//
// Substitution is single-pass: a value produced by expanding one ${VAR} is
// never re-scanned, so a secret whose own value contains ${OTHER} cannot
// trigger a second expansion.
//
// On any denial — header not allow-listed, variable not allow-listed, or an
// allow-listed variable that is unset/empty — the literal ${VAR} text is passed
// through unchanged and a Warn-level line is emitted identifying the header and
// variable. The line goes to log; nil discards it.
func SubstituteEnvVars(log *slog.Logger, value, headerName string) string {
	log = logging.OrDiscard(log)
	headerAllowed := isAllowedHeaderName(headerName)
	canonicalHeader := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(headerName))

	// envVarPattern.ReplaceAllStringFunc walks each ${...} match exactly once;
	// the replacement text it returns is not re-scanned, which gives the
	// required single-pass (non-recursive) behaviour.
	return envVarPattern.ReplaceAllStringFunc(value, func(match string) string {
		// Extract variable name from ${VAR_NAME}.
		varName := match[2 : len(match)-1]

		if !headerAllowed {
			log.Warn("env-var expansion denied: header is not in the expansion allow-list; passing the reference through literally",
				"header", headerName, "variable", varName)
			return match
		}

		if !isAllowedEnvVar(varName) {
			log.Warn("env-var expansion denied: variable is not allow-listed for header; passing the reference through literally (rename it with the allowed prefix to allow)",
				"variable", varName, "header", canonicalHeader, "allowed_prefix", allowedHeaderEnvPrefix)
			return match
		}

		resolved, ok := os.LookupEnv(varName)
		if !ok || resolved == "" {
			log.Warn("env-var expansion skipped: allow-listed variable is unset or empty for header; passing the reference through literally",
				"variable", varName, "header", canonicalHeader)
			return match
		}

		return resolved
	})
}

// isGitHubAPIURL reports whether url is a GitHub API URL the automatic token
// may be attached to. Only https qualifies: a token sent over plain http
// travels in cleartext to anyone on the path, and api.github.com serves https
// only, so the http form can only ever be a downgrade (S052-R5.1).
func isGitHubAPIURL(url string) bool {
	return strings.HasPrefix(url, "https://api.github.com/")
}
