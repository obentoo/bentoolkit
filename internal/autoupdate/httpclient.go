// Package autoupdate provides HTTP client with retry logic for version checking.
package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Error variables for HTTP client errors
var (
	// ErrMaxRetriesExceeded is returned when all retry attempts have failed
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")
	// ErrRequestTimeout is returned when a request times out
	ErrRequestTimeout = errors.New("request timeout")
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
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   4 * time.Second,
		Timeout:    30 * time.Second,
	}
}

// RetryableHTTPClient wraps an HTTP client with retry logic.
// It implements exponential backoff for failed requests.
type RetryableHTTPClient struct {
	client *http.Client
	config RetryConfig
	// delayFunc allows overriding the delay function for testing
	delayFunc func(time.Duration)
	// recordedDelays stores delays for testing purposes
	recordedDelays []time.Duration
}

// NewRetryableHTTPClient creates a new HTTP client with retry support.
// Uses the default retry configuration.
func NewRetryableHTTPClient() *RetryableHTTPClient {
	return NewRetryableHTTPClientWithConfig(DefaultRetryConfig())
}

// NewRetryableHTTPClientWithConfig creates a new HTTP client with custom retry configuration.
func NewRetryableHTTPClientWithConfig(config RetryConfig) *RetryableHTTPClient {
	return &RetryableHTTPClient{
		client: &http.Client{
			Timeout: config.Timeout,
		},
		config:    config,
		delayFunc: time.Sleep,
	}
}

// SetHTTPClient sets a custom underlying HTTP client (useful for testing).
func (c *RetryableHTTPClient) SetHTTPClient(client *http.Client) {
	c.client = client
}

// SetDelayFunc sets a custom delay function (useful for testing).
// The function receives the delay duration that would normally be slept.
func (c *RetryableHTTPClient) SetDelayFunc(fn func(time.Duration)) {
	c.delayFunc = fn
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

// DoWithContext executes an HTTP request with retry logic and context support.
// It retries on network errors and 5xx server errors with exponential backoff.
func (c *RetryableHTTPClient) DoWithContext(ctx context.Context, req *http.Request) (*http.Response, error) {
	var lastErr error
	var lastResp *http.Response

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		// Check context cancellation before each attempt
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Apply delay before retry (not on first attempt)
		if attempt > 0 {
			delay := c.calculateDelay(attempt)
			c.recordDelay(delay)
			c.delayFunc(delay)
		}

		// Clone the request for retry (body needs to be re-readable)
		reqCopy := req.Clone(ctx)

		// Execute the request
		resp, err := c.client.Do(reqCopy)
		if err != nil {
			lastErr = err
			// Check if it's a timeout error
			if isTimeoutError(err) {
				lastErr = fmt.Errorf("%w: %v", ErrRequestTimeout, err)
			}
			continue
		}

		// Check if we should retry based on status code
		if c.shouldRetry(resp.StatusCode) {
			// Close the response body before retrying
			if resp.Body != nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			lastErr = fmt.Errorf("server error: status %d", resp.StatusCode)
			lastResp = resp
			continue
		}

		// Success or non-retryable error
		return resp, nil
	}

	// All retries exhausted
	if lastErr != nil {
		return lastResp, fmt.Errorf("%w: %v", ErrMaxRetriesExceeded, lastErr)
	}
	return lastResp, ErrMaxRetriesExceeded
}

// Get performs an HTTP GET request with retry logic.
func (c *RetryableHTTPClient) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// GetWithContext performs an HTTP GET request with retry logic and context support.
func (c *RetryableHTTPClient) GetWithContext(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.DoWithContext(ctx, req)
}

// calculateDelay calculates the delay for a given retry attempt.
// Uses exponential backoff: delay = baseDelay * 2^(attempt-1)
// Attempt 1: 1s, Attempt 2: 2s, Attempt 3: 4s
func (c *RetryableHTTPClient) calculateDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}

	// Calculate exponential delay: baseDelay * 2^(attempt-1)
	multiplier := 1 << (attempt - 1) // 2^(attempt-1): 1, 2, 4, ...
	delay := c.config.BaseDelay * time.Duration(multiplier)

	// Cap at max delay
	if delay > c.config.MaxDelay {
		delay = c.config.MaxDelay
	}

	return delay
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
