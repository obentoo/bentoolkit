// Package autoupdate provides rate limiting for LLM and HTTP requests.
package autoupdate

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Error variables for rate limiting errors
var (
	// ErrRateLimitExceeded is returned when a rate limit is exceeded and context is cancelled
	ErrRateLimitExceeded = errors.New("rate limit exceeded")
)

const (
	// DefaultLLMInterval is the minimum interval between LLM requests (5 per minute).
	DefaultLLMInterval = 12 * time.Second
	// DefaultHTTPInterval is the minimum interval between HTTP requests per domain (10 per minute).
	DefaultHTTPInterval = 6 * time.Second
	// DefaultLLMBurst is the burst size for LLM rate limiting.
	DefaultLLMBurst = 1
	// DefaultHTTPBurst is the burst size for HTTP rate limiting per domain.
	DefaultHTTPBurst = 1
	// DefaultMaxDomains is the maximum number of HTTP domain limiters to keep in memory.
	DefaultMaxDomains = 256
	// DefaultCleanupAge is the age after which an unused domain entry can be evicted.
	DefaultCleanupAge = 1 * time.Hour
)

// domainEntry holds a rate limiter and its last-used timestamp for eviction tracking.
type domainEntry struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// RateLimiter manages request rate limiting for LLM and HTTP requests.
// It enforces:
// - LLM rate limiting: 5 requests per minute
// - HTTP rate limiting: 10 requests per minute per domain (bounded to maxDomains entries)
type RateLimiter struct {
	// llmLimiter limits LLM API requests to 5 per minute
	llmLimiter *rate.Limiter
	// httpLimiters maps domain names to their rate limiter entries
	httpLimiters map[string]*domainEntry
	// mu protects httpLimiters map
	mu sync.Mutex
	// clock allows overriding time functions for testing
	clock Clock
	// maxDomains caps the number of tracked domains to prevent unbounded memory growth
	maxDomains int
	// cleanupAge is how old an entry must be before it can be evicted by age
	cleanupAge time.Duration
}

// Clock interface allows mocking time for testing
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

// realClock implements Clock using actual time functions
type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }

// RateLimiterOption configures a RateLimiter
type RateLimiterOption func(*RateLimiter)

// WithClock sets a custom clock for testing
func WithClock(clock Clock) RateLimiterOption {
	return func(r *RateLimiter) {
		r.clock = clock
	}
}

// WithMaxDomains sets the maximum number of HTTP domain limiters to keep in memory.
func WithMaxDomains(n int) RateLimiterOption {
	return func(r *RateLimiter) {
		r.maxDomains = n
	}
}

// WithCleanupAge sets the age after which an unused domain entry is eligible for eviction.
func WithCleanupAge(d time.Duration) RateLimiterOption {
	return func(r *RateLimiter) {
		r.cleanupAge = d
	}
}

// NewRateLimiter creates a new rate limiter with default settings.
// LLM requests are limited to 5 per minute.
// HTTP requests are limited to 10 per minute per domain, with a maximum of
// DefaultMaxDomains tracked concurrently (LRU eviction when full).
func NewRateLimiter(opts ...RateLimiterOption) *RateLimiter {
	r := &RateLimiter{
		// 5 requests per minute = 5/60 = 1 request per 12 seconds
		// Allow burst of 1 to ensure strict rate limiting
		llmLimiter:   rate.NewLimiter(rate.Every(DefaultLLMInterval), DefaultLLMBurst),
		httpLimiters: make(map[string]*domainEntry),
		clock:        realClock{},
		maxDomains:   DefaultMaxDomains,
		cleanupAge:   DefaultCleanupAge,
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// WaitLLM waits for LLM rate limit before proceeding.
// It blocks until a token is available or the context is cancelled.
// Returns ErrRateLimitExceeded if the context is cancelled while waiting.
func (r *RateLimiter) WaitLLM(ctx context.Context) error {
	err := r.llmLimiter.Wait(ctx)
	if err != nil {
		// Check for context cancellation or deadline exceeded
		if ctx.Err() != nil {
			return ErrRateLimitExceeded
		}
		// For other errors (like burst exceeded), wrap them
		return err
	}
	return nil
}

// WaitHTTP waits for HTTP rate limit for a specific domain before proceeding.
// It blocks until a token is available or the context is cancelled.
// Returns ErrRateLimitExceeded if the context is cancelled while waiting.
func (r *RateLimiter) WaitHTTP(ctx context.Context, domain string) error {
	limiter := r.getHTTPLimiter(domain)
	err := limiter.Wait(ctx)
	if err != nil {
		// Check for context cancellation or deadline exceeded
		if ctx.Err() != nil {
			return ErrRateLimitExceeded
		}
		// For other errors (like burst exceeded), wrap them
		return err
	}
	return nil
}

// WaitHTTPForURL waits for HTTP rate limit for a URL's domain before proceeding.
// It extracts the domain from the URL and applies rate limiting.
func (r *RateLimiter) WaitHTTPForURL(ctx context.Context, rawURL string) error {
	domain, err := extractDomain(rawURL)
	if err != nil {
		// If we can't parse the URL, use the raw URL as the domain
		domain = rawURL
	}
	return r.WaitHTTP(ctx, domain)
}

// getHTTPLimiter returns the rate limiter for a specific domain.
// Creates a new limiter if one doesn't exist, evicting old entries if at capacity.
func (r *RateLimiter) getHTTPLimiter(domain string) *rate.Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()

	// If entry already exists, refresh lastUsed and return the limiter
	if entry, exists := r.httpLimiters[domain]; exists {
		entry.lastUsed = r.clock.Now()
		return entry.limiter
	}

	// Need to create a new entry. If at capacity, evict first.
	if len(r.httpLimiters) >= r.maxDomains {
		r.evict()
	}

	// Create new entry
	entry := &domainEntry{
		limiter:  rate.NewLimiter(rate.Every(DefaultHTTPInterval), DefaultHTTPBurst),
		lastUsed: r.clock.Now(),
	}
	r.httpLimiters[domain] = entry
	return entry.limiter
}

// evict removes stale or least-recently-used entries to make room for a new one.
// Must be called with r.mu held.
func (r *RateLimiter) evict() {
	now := r.clock.Now()

	// First pass: remove entries older than cleanupAge
	for domain, entry := range r.httpLimiters {
		if now.Sub(entry.lastUsed) > r.cleanupAge {
			delete(r.httpLimiters, domain)
		}
	}

	// If still at capacity, evict the least recently used entry
	if len(r.httpLimiters) >= r.maxDomains {
		var lruDomain string
		var lruTime time.Time
		for domain, entry := range r.httpLimiters {
			if lruDomain == "" || entry.lastUsed.Before(lruTime) {
				lruDomain = domain
				lruTime = entry.lastUsed
			}
		}
		if lruDomain != "" {
			delete(r.httpLimiters, lruDomain)
		}
	}
}

// AllowLLM reports whether an LLM request may happen now.
// It does not block or consume a token.
func (r *RateLimiter) AllowLLM() bool {
	return r.llmLimiter.Allow()
}

// AllowHTTP reports whether an HTTP request to the domain may happen now.
// It does not block or consume a token.
func (r *RateLimiter) AllowHTTP(domain string) bool {
	limiter := r.getHTTPLimiter(domain)
	return limiter.Allow()
}

// ReserveLLM returns a Reservation that indicates how long the caller must wait
// before an LLM request can proceed.
func (r *RateLimiter) ReserveLLM() *rate.Reservation {
	return r.llmLimiter.Reserve()
}

// ReserveHTTP returns a Reservation that indicates how long the caller must wait
// before an HTTP request to the domain can proceed.
func (r *RateLimiter) ReserveHTTP(domain string) *rate.Reservation {
	limiter := r.getHTTPLimiter(domain)
	return limiter.Reserve()
}

// LLMLimit returns the current LLM rate limit (requests per second).
func (r *RateLimiter) LLMLimit() rate.Limit {
	return r.llmLimiter.Limit()
}

// HTTPLimit returns the current HTTP rate limit for a domain (requests per second).
func (r *RateLimiter) HTTPLimit(domain string) rate.Limit {
	limiter := r.getHTTPLimiter(domain)
	return limiter.Limit()
}

// SetLLMLimit sets a custom LLM rate limit (for testing).
func (r *RateLimiter) SetLLMLimit(limit rate.Limit, burst int) {
	r.llmLimiter.SetLimit(limit)
	r.llmLimiter.SetBurst(burst)
}

// SetHTTPLimit sets a custom HTTP rate limit for a domain (for testing).
func (r *RateLimiter) SetHTTPLimit(domain string, limit rate.Limit, burst int) {
	limiter := r.getHTTPLimiter(domain)
	limiter.SetLimit(limit)
	limiter.SetBurst(burst)
}

// extractDomain extracts the domain from a URL string.
func extractDomain(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return parsed.Host, nil
}

// DomainCount returns the number of domains being tracked for HTTP rate limiting.
func (r *RateLimiter) DomainCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.httpLimiters)
}

// Reset clears all HTTP domain limiters and resets the LLM limiter.
// Useful for testing.
func (r *RateLimiter) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.httpLimiters = make(map[string]*domainEntry)
	r.llmLimiter = rate.NewLimiter(rate.Every(DefaultLLMInterval), DefaultLLMBurst)
}
