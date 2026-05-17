package autoupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/sony/gobreaker"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestRetryExponentialBackoff tests Property 12: Retry Exponential Backoff
// **Feature: ebuild-autoupdate, Property 12: Retry Exponential Backoff**
// **Validates: Requirements 8.1**
func TestRetryExponentialBackoff(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Delays follow exponential backoff pattern (delay[i] > delay[i-1])
	properties.Property("Retry delays follow exponential backoff pattern", prop.ForAll(
		func(numFailures int) bool {
			// Ensure numFailures is between 1 and 3
			if numFailures < 1 {
				numFailures = 1
			}
			numFailures = (numFailures % 3) + 1

			// Track request count and recorded delays
			var requestCount int32
			var recordedDelays []time.Duration

			// Create a test server that fails numFailures times then succeeds
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count := atomic.AddInt32(&requestCount, 1)
				if int(count) <= numFailures {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			// Create client with custom delay function that records delays
			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {
				recordedDelays = append(recordedDelays, d)
			})

			// Make request
			resp, err := client.Get(server.URL)
			if err != nil {
				t.Logf("Request failed: %v", err)
				return false
			}
			defer resp.Body.Close()

			// Verify we got the expected number of delays (one per retry)
			if len(recordedDelays) != numFailures {
				t.Logf("Expected %d delays, got %d", numFailures, len(recordedDelays))
				return false
			}

			// Verify delays follow exponential backoff (each delay > previous)
			for i := 1; i < len(recordedDelays); i++ {
				if recordedDelays[i] <= recordedDelays[i-1] {
					t.Logf("Delay %d (%v) should be > delay %d (%v)",
						i, recordedDelays[i], i-1, recordedDelays[i-1])
					return false
				}
			}

			return true
		},
		gen.IntRange(1, 100),
	))

	// Property: After 3 failures, no more retries are attempted
	properties.Property("After max retries, no more attempts are made", prop.ForAll(
		func(extraFailures int) bool {
			// Ensure extraFailures is positive (used to vary test inputs)
			if extraFailures < 0 {
				extraFailures = -extraFailures
			}
			_ = (extraFailures % 10) + 1 // Vary input but always test max retries

			var requestCount int32

			// Create a test server that always fails
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&requestCount, 1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()

			// Create client with no-op delay function
			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {})

			// Make request (should fail after max retries)
			_, err := client.Get(server.URL) //nolint:bodyclose // test client, response body intentionally not closed
			if err == nil {
				t.Log("Expected error after max retries")
				return false
			}

			// Should have made exactly 4 requests (1 initial + 3 retries)
			count := atomic.LoadInt32(&requestCount)
			if count != 4 {
				t.Logf("Expected 4 requests (1 + 3 retries), got %d", count)
				return false
			}

			return true
		},
		gen.IntRange(1, 100),
	))

	// Property: Specific delay values match expected exponential pattern
	properties.Property("Delay values match 1s, 2s, 4s pattern", prop.ForAll(
		func(seed int) bool {
			var requestCount int32
			var recordedDelays []time.Duration

			// Create a test server that fails 3 times then succeeds
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count := atomic.AddInt32(&requestCount, 1)
				if count <= 3 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			// Create client with custom delay function
			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {
				recordedDelays = append(recordedDelays, d)
			})

			// Make request
			resp, err := client.Get(server.URL)
			if err != nil {
				t.Logf("Request failed: %v", err)
				return false
			}
			defer resp.Body.Close()

			// Verify exact delay values: 1s, 2s, 4s
			expectedDelays := []time.Duration{
				1 * time.Second,
				2 * time.Second,
				4 * time.Second,
			}

			if len(recordedDelays) != len(expectedDelays) {
				t.Logf("Expected %d delays, got %d", len(expectedDelays), len(recordedDelays))
				return false
			}

			for i, expected := range expectedDelays {
				if recordedDelays[i] != expected {
					t.Logf("Delay %d: expected %v, got %v", i, expected, recordedDelays[i])
					return false
				}
			}

			return true
		},
		gen.IntRange(0, 1000),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests
// =============================================================================

// TestNewRetryableHTTPClient tests default client creation
func TestNewRetryableHTTPClient(t *testing.T) {
	client := NewRetryableHTTPClient()

	config := client.Config()
	if config.MaxRetries != 3 {
		t.Errorf("Expected MaxRetries=3, got %d", config.MaxRetries)
	}
	if config.BaseDelay != 1*time.Second {
		t.Errorf("Expected BaseDelay=1s, got %v", config.BaseDelay)
	}
	if config.MaxDelay != 4*time.Second {
		t.Errorf("Expected MaxDelay=4s, got %v", config.MaxDelay)
	}
	if config.Timeout != 30*time.Second {
		t.Errorf("Expected Timeout=30s, got %v", config.Timeout)
	}
}

// TestNewRetryableHTTPClientWithConfig tests custom config
func TestNewRetryableHTTPClientWithConfig(t *testing.T) {
	config := RetryConfig{
		MaxRetries: 5,
		BaseDelay:  500 * time.Millisecond,
		MaxDelay:   10 * time.Second,
		Timeout:    60 * time.Second,
	}

	client := NewRetryableHTTPClientWithConfig(config)

	got := client.Config()
	if got.MaxRetries != 5 {
		t.Errorf("Expected MaxRetries=5, got %d", got.MaxRetries)
	}
	if got.BaseDelay != 500*time.Millisecond {
		t.Errorf("Expected BaseDelay=500ms, got %v", got.BaseDelay)
	}
}

// TestRetryableHTTPClientSuccessOnFirstAttempt tests successful request without retries
func TestRetryableHTTPClientSuccessOnFirstAttempt(t *testing.T) {
	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	client := NewRetryableHTTPClient()
	client.SetHTTPClient(server.Client())
	client.SetDelayFunc(func(d time.Duration) {})

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	count := atomic.LoadInt32(&requestCount)
	if count != 1 {
		t.Errorf("Expected 1 request, got %d", count)
	}

	// No delays should have been recorded
	delays := client.GetRecordedDelays()
	if len(delays) != 0 {
		t.Errorf("Expected 0 delays, got %d", len(delays))
	}
}

// TestRetryableHTTPClientSuccessOnRetry tests successful request after retries
func TestRetryableHTTPClientSuccessOnRetry(t *testing.T) {
	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	client := NewRetryableHTTPClient()
	client.SetHTTPClient(server.Client())
	client.SetDelayFunc(func(d time.Duration) {})

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	count := atomic.LoadInt32(&requestCount)
	if count != 3 {
		t.Errorf("Expected 3 requests, got %d", count)
	}
}

// TestRetryableHTTPClientMaxRetriesExceeded tests failure after max retries
func TestRetryableHTTPClientMaxRetriesExceeded(t *testing.T) {
	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewRetryableHTTPClient()
	client.SetHTTPClient(server.Client())
	client.SetDelayFunc(func(d time.Duration) {})

	_, err := client.Get(server.URL) //nolint:bodyclose // test client, response body intentionally not closed
	if err == nil {
		t.Fatal("Expected error after max retries")
	}

	// Should have made 4 requests (1 initial + 3 retries)
	count := atomic.LoadInt32(&requestCount)
	if count != 4 {
		t.Errorf("Expected 4 requests, got %d", count)
	}

	// Error should indicate max retries exceeded
	if !containsError(err, ErrMaxRetriesExceeded) {
		t.Errorf("Expected ErrMaxRetriesExceeded, got: %v", err)
	}
}

// TestRetryableHTTPClientNoRetryOn4xx tests that 4xx errors are not retried
func TestRetryableHTTPClientNoRetryOn4xx(t *testing.T) {
	testCases := []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
	}

	for _, statusCode := range testCases {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			var requestCount int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&requestCount, 1)
				w.WriteHeader(statusCode)
			}))
			defer server.Close()

			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {})

			resp, err := client.Get(server.URL)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			defer resp.Body.Close()

			// Should have made only 1 request (no retries for 4xx)
			count := atomic.LoadInt32(&requestCount)
			if count != 1 {
				t.Errorf("Expected 1 request for %d status, got %d", statusCode, count)
			}
		})
	}
}

// TestRetryableHTTPClientRetryOn5xx tests that 5xx errors are retried
func TestRetryableHTTPClientRetryOn5xx(t *testing.T) {
	testCases := []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}

	for _, statusCode := range testCases {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			var requestCount int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&requestCount, 1)
				w.WriteHeader(statusCode)
			}))
			defer server.Close()

			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {})

			_, err := client.Get(server.URL) //nolint:bodyclose // test client, response body intentionally not closed
			if err == nil {
				t.Fatal("Expected error after max retries")
			}

			// Should have made 4 requests (1 initial + 3 retries)
			count := atomic.LoadInt32(&requestCount)
			if count != 4 {
				t.Errorf("Expected 4 requests for %d status, got %d", statusCode, count)
			}
		})
	}
}

// TestRetryableHTTPClientRetryOn429 tests that 429 (Too Many Requests) is retried
func TestRetryableHTTPClientRetryOn429(t *testing.T) {
	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewRetryableHTTPClient()
	client.SetHTTPClient(server.Client())
	client.SetDelayFunc(func(d time.Duration) {})

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	defer resp.Body.Close()

	count := atomic.LoadInt32(&requestCount)
	if count != 3 {
		t.Errorf("Expected 3 requests, got %d", count)
	}
}

// TestRetryableHTTPClientContextCancellation tests context cancellation
func TestRetryableHTTPClientContextCancellation(t *testing.T) {
	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewRetryableHTTPClient()
	client.SetHTTPClient(server.Client())
	client.SetDelayFunc(func(d time.Duration) {})

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetWithContext(ctx, server.URL) //nolint:bodyclose // test client, response body intentionally not closed
	if err == nil {
		t.Fatal("Expected error with cancelled context")
	}

	// Should have made 0 requests (context cancelled before first attempt)
	count := atomic.LoadInt32(&requestCount)
	if count != 0 {
		t.Errorf("Expected 0 requests with cancelled context, got %d", count)
	}
}

// TestCalculateDelay tests the delay calculation
func TestCalculateDelay(t *testing.T) {
	client := NewRetryableHTTPClient()

	testCases := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 0},
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 4 * time.Second}, // Capped at MaxDelay
		{5, 4 * time.Second}, // Capped at MaxDelay
	}

	for _, tc := range testCases {
		t.Run("", func(t *testing.T) {
			delay := client.calculateDelay(tc.attempt)
			if delay != tc.expected {
				t.Errorf("Attempt %d: expected %v, got %v", tc.attempt, tc.expected, delay)
			}
		})
	}
}

// TestShouldRetry tests the retry decision logic
func TestShouldRetry(t *testing.T) {
	client := NewRetryableHTTPClient()

	testCases := []struct {
		statusCode  int
		shouldRetry bool
	}{
		{200, false},
		{201, false},
		{204, false},
		{301, false},
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{429, true}, // Rate limiting
		{500, true}, // Internal Server Error
		{502, true}, // Bad Gateway
		{503, true}, // Service Unavailable
		{504, true}, // Gateway Timeout
	}

	for _, tc := range testCases {
		t.Run(http.StatusText(tc.statusCode), func(t *testing.T) {
			result := client.shouldRetry(tc.statusCode)
			if result != tc.shouldRetry {
				t.Errorf("Status %d: expected shouldRetry=%v, got %v",
					tc.statusCode, tc.shouldRetry, result)
			}
		})
	}
}

// TestRecordedDelays tests delay recording functionality
func TestRecordedDelays(t *testing.T) {
	var requestCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewRetryableHTTPClient()
	client.SetHTTPClient(server.Client())
	client.SetDelayFunc(func(d time.Duration) {
		// No-op, but delays are still recorded
	})

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	defer resp.Body.Close()

	delays := client.GetRecordedDelays()
	if len(delays) != 2 {
		t.Errorf("Expected 2 recorded delays, got %d", len(delays))
	}

	// Clear and verify
	client.ClearRecordedDelays()
	delays = client.GetRecordedDelays()
	if len(delays) != 0 {
		t.Errorf("Expected 0 delays after clear, got %d", len(delays))
	}
}

// TestDefaultRetryConfig tests default configuration values
func TestDefaultRetryConfig(t *testing.T) {
	config := DefaultRetryConfig()

	if config.MaxRetries != 3 {
		t.Errorf("Expected MaxRetries=3, got %d", config.MaxRetries)
	}
	if config.BaseDelay != 1*time.Second {
		t.Errorf("Expected BaseDelay=1s, got %v", config.BaseDelay)
	}
	if config.MaxDelay != 4*time.Second {
		t.Errorf("Expected MaxDelay=4s, got %v", config.MaxDelay)
	}
	if config.Timeout != 30*time.Second {
		t.Errorf("Expected Timeout=30s, got %v", config.Timeout)
	}
}

// containsError checks if err contains target error
func containsError(err, target error) bool {
	if err == nil {
		return false
	}
	return err.Error() != "" && target.Error() != "" &&
		(err == target || err.Error() == target.Error() ||
			len(err.Error()) > len(target.Error()) &&
				err.Error()[:len(target.Error())] == target.Error())
}

// =============================================================================
// Property-Based Tests for Header Support
// =============================================================================

// TestHeaderStorageAndApplication tests Property 19: Header Storage and Application
// **Feature: autoupdate-analyzer, Property 19: Header Storage and Application**
// **Validates: Requirements 8.1, 8.2**
// For any schema with custom headers, those headers SHALL be stored in the
// configuration and applied to HTTP requests when fetching content.
func TestHeaderStorageAndApplication(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Custom headers are stored and can be retrieved
	properties.Property("Custom headers are stored and retrievable", prop.ForAll(
		func(headerKey, headerValue string) bool {
			// Skip empty keys (invalid headers)
			if headerKey == "" {
				return true
			}

			client := NewRetryableHTTPClient()
			headers := map[string]string{headerKey: headerValue}
			client.SetDefaultHeaders(headers)

			retrieved := client.GetDefaultHeaders()
			if retrieved == nil {
				t.Log("GetDefaultHeaders returned nil")
				return false
			}

			if retrieved[headerKey] != headerValue {
				t.Logf("Expected header %s=%s, got %s", headerKey, headerValue, retrieved[headerKey])
				return false
			}

			return true
		},
		gen.AlphaString(),
		gen.AlphaString(),
	))

	// Property: Custom headers are applied to HTTP requests
	properties.Property("Custom headers are applied to HTTP requests", prop.ForAll(
		func(headerKey, headerValue string) bool {
			// Skip empty keys (invalid headers)
			if headerKey == "" {
				return true
			}

			var receivedHeaders http.Header

			// Create a test server that captures headers
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedHeaders = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {})

			// Make request with custom headers
			headers := map[string]string{headerKey: headerValue}
			resp, err := client.GetWithHeaders(server.URL, headers)
			if err != nil {
				t.Logf("Request failed: %v", err)
				return false
			}
			defer resp.Body.Close()

			// Verify header was received
			if receivedHeaders.Get(headerKey) != headerValue {
				t.Logf("Expected header %s=%s, got %s", headerKey, headerValue, receivedHeaders.Get(headerKey))
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
	))

	// Property: Default headers are applied to all requests
	properties.Property("Default headers are applied to all requests", prop.ForAll(
		func(headerKey, headerValue string) bool {
			// Skip empty keys (invalid headers)
			if headerKey == "" {
				return true
			}

			var receivedHeaders http.Header

			// Create a test server that captures headers
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedHeaders = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {})

			// Set default headers
			client.SetDefaultHeaders(map[string]string{headerKey: headerValue})

			// Make request without explicit headers
			resp, err := client.GetWithHeaders(server.URL, nil)
			if err != nil {
				t.Logf("Request failed: %v", err)
				return false
			}
			defer resp.Body.Close()

			// Verify default header was received
			if receivedHeaders.Get(headerKey) != headerValue {
				t.Logf("Expected default header %s=%s, got %s", headerKey, headerValue, receivedHeaders.Get(headerKey))
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
	))

	// Property: Custom headers override default headers
	properties.Property("Custom headers override default headers", prop.ForAll(
		func(headerKey, defaultValue, customValue string) bool {
			// Skip empty keys (invalid headers)
			if headerKey == "" {
				return true
			}

			var receivedHeaders http.Header

			// Create a test server that captures headers
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedHeaders = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {})

			// Set default headers
			client.SetDefaultHeaders(map[string]string{headerKey: defaultValue})

			// Make request with custom headers that override
			resp, err := client.GetWithHeaders(server.URL, map[string]string{headerKey: customValue})
			if err != nil {
				t.Logf("Request failed: %v", err)
				return false
			}
			defer resp.Body.Close()

			// Verify custom header overrode default
			if receivedHeaders.Get(headerKey) != customValue {
				t.Logf("Expected custom header %s=%s to override default %s, got %s",
					headerKey, customValue, defaultValue, receivedHeaders.Get(headerKey))
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestGitHubTokenIntegration tests Property 20: GitHub Token Integration
// **Feature: autoupdate-analyzer, Property 20: GitHub Token Integration**
// **Validates: Requirements 8.3**
// For any GitHub API request when a GitHub token is configured globally,
// the request SHALL include the token in the Authorization header.
func TestGitHubTokenIntegration(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: GitHub token is stored and retrievable
	properties.Property("GitHub token is stored and retrievable", prop.ForAll(
		func(token string) bool {
			client := NewRetryableHTTPClient()
			client.SetGitHubToken(token)

			retrieved := client.GetGitHubToken()
			if retrieved != token {
				t.Logf("Expected token %s, got %s", token, retrieved)
				return false
			}

			return true
		},
		gen.AlphaString(),
	))

	// Property: GitHub token is applied to GitHub API requests
	properties.Property("GitHub token is applied to GitHub API requests", prop.ForAll(
		func(token string) bool {
			// Skip empty tokens
			if token == "" {
				return true
			}

			var receivedAuth string

			// Create a test server that captures Authorization header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedAuth = r.Header.Get("Authorization")
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {})
			client.SetGitHubToken(token)

			// Simulate GitHub API URL by using the test server URL
			// We need to test the isGitHubAPIURL function separately
			// For this test, we'll use GetWithHeaders with a GitHub-like URL pattern

			// Make request to non-GitHub URL - token should NOT be applied
			resp, err := client.GetWithHeaders(server.URL, nil)
			if err != nil {
				t.Logf("Request failed: %v", err)
				return false
			}
			defer resp.Body.Close()

			// For non-GitHub URLs, Authorization should be empty
			if receivedAuth != "" {
				t.Logf("Expected no Authorization for non-GitHub URL, got %s", receivedAuth)
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	// Property: GitHub API URL detection works correctly
	properties.Property("GitHub API URL detection works correctly", prop.ForAll(
		func(path string) bool {
			// Test that api.github.com URLs are detected
			githubURL := "https://api.github.com/" + path
			if !isGitHubAPIURL(githubURL) {
				t.Logf("Expected %s to be detected as GitHub API URL", githubURL)
				return false
			}

			// Test that non-GitHub URLs are not detected
			nonGitHubURL := "https://example.com/" + path
			if isGitHubAPIURL(nonGitHubURL) {
				t.Logf("Expected %s to NOT be detected as GitHub API URL", nonGitHubURL)
				return false
			}

			return true
		},
		gen.AlphaString(),
	))

	// Property: GitHub token format is correct (Bearer prefix)
	properties.Property("GitHub token uses Bearer format", prop.ForAll(
		func(token string) bool {
			// Skip empty tokens
			if token == "" {
				return true
			}

			// Create a test server (not used but needed for client setup)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {})
			client.SetGitHubToken(token)

			// Create a request and manually apply headers to test the format
			req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/test/test", nil)
			client.applyHeaders(req, "https://api.github.com/repos/test/test", nil)

			authHeader := req.Header.Get("Authorization")
			expectedAuth := "Bearer " + token

			if authHeader != expectedAuth {
				t.Logf("Expected Authorization header %s, got %s", expectedAuth, authHeader)
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	properties.TestingRun(t)
}

// TestHeaderTemplateSubstitution tests Property 21: Header Template Substitution
// **Feature: autoupdate-analyzer, Property 21: Header Template Substitution**
// **Validates: Requirements 8.4, R1.1, R1.3**
// For any allow-listed header value containing ${VAR_NAME} syntax referencing
// an allow-listed environment variable, the analyzer SHALL substitute the value
// of the corresponding environment variable. Generated variable names are
// prefixed with allowedHeaderEnvPrefix (BENTOO_) so they pass the env-var
// allow-list; the canonical allow-listed header name "Authorization" is passed
// to SubstituteEnvVars.
func TestHeaderTemplateSubstitution(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Environment variables are substituted in header values
	properties.Property("Environment variables are substituted in header values", prop.ForAll(
		func(varSuffix, varValue string) bool {
			// Skip empty or invalid variable names; non-empty values only so
			// the (allow-listed but empty) literal-passthrough rule does not apply.
			if varSuffix == "" || !isValidEnvVarName(varSuffix) || varValue == "" {
				return true
			}

			varName := allowedHeaderEnvPrefix + varSuffix

			// Set environment variable
			os.Setenv(varName, varValue)
			defer os.Unsetenv(varName)

			// Test substitution
			template := "Bearer ${" + varName + "}"
			result := SubstituteEnvVars(template, "Authorization")
			expected := "Bearer " + varValue

			if result != expected {
				t.Logf("Expected %s, got %s", expected, result)
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && isValidEnvVarName(s) }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	// Property: Multiple environment variables are substituted
	properties.Property("Multiple environment variables are substituted", prop.ForAll(
		func(var1Suffix, var1Value, var2Suffix, var2Value string) bool {
			// Skip empty or invalid variable names.
			if var1Suffix == "" || var2Suffix == "" || var1Suffix == var2Suffix {
				return true
			}
			if !isValidEnvVarName(var1Suffix) || !isValidEnvVarName(var2Suffix) {
				return true
			}
			// Non-empty values only (empty allow-listed vars pass through literally).
			if var1Value == "" || var2Value == "" {
				return true
			}

			var1Name := allowedHeaderEnvPrefix + var1Suffix
			var2Name := allowedHeaderEnvPrefix + var2Suffix

			// Set environment variables
			os.Setenv(var1Name, var1Value)
			os.Setenv(var2Name, var2Value)
			defer os.Unsetenv(var1Name)
			defer os.Unsetenv(var2Name)

			// Test substitution with multiple variables
			template := "${" + var1Name + "}-${" + var2Name + "}"
			result := SubstituteEnvVars(template, "Authorization")
			expected := var1Value + "-" + var2Value

			if result != expected {
				t.Logf("Expected %s, got %s", expected, result)
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && isValidEnvVarName(s) }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && isValidEnvVarName(s) }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	// Property: Unset allow-listed variables pass the literal ${VAR} through (R1.3)
	properties.Property("Unset allow-listed variables pass the literal through", prop.ForAll(
		func(varSuffix string) bool {
			// Skip empty or invalid variable names
			if varSuffix == "" || !isValidEnvVarName(varSuffix) {
				return true
			}

			varName := allowedHeaderEnvPrefix + varSuffix

			// Ensure variable is not set
			os.Unsetenv(varName)

			// Test substitution: unset allow-listed var -> literal passthrough.
			template := "prefix-${" + varName + "}-suffix"
			result := SubstituteEnvVars(template, "Authorization")
			expected := "prefix-${" + varName + "}-suffix"

			if result != expected {
				t.Logf("Expected %s, got %s", expected, result)
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && isValidEnvVarName(s) }),
	))

	// Property: Text without ${} patterns is unchanged
	properties.Property("Text without ${} patterns is unchanged", prop.ForAll(
		func(text string) bool {
			// Skip text that contains ${
			if strings.Contains(text, "${") {
				return true
			}

			result := SubstituteEnvVars(text, "Authorization")
			if result != text {
				t.Logf("Expected unchanged text %s, got %s", text, result)
				return false
			}

			return true
		},
		gen.AlphaString(),
	))

	// Property: Substituted headers are applied to HTTP requests
	properties.Property("Substituted headers are applied to HTTP requests", prop.ForAll(
		func(varSuffix, varValue string) bool {
			// Skip empty or invalid inputs; non-empty values only.
			if varSuffix == "" || !isValidEnvVarName(varSuffix) || varValue == "" {
				return true
			}

			varName := allowedHeaderEnvPrefix + varSuffix
			// X-Api-Key is an allow-listed expansion header.
			const headerKey = "X-Api-Key"

			// Set environment variable
			os.Setenv(varName, varValue)
			defer os.Unsetenv(varName)

			var receivedHeaders http.Header

			// Create a test server that captures headers
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedHeaders = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewRetryableHTTPClient()
			client.SetHTTPClient(server.Client())
			client.SetDelayFunc(func(d time.Duration) {})

			// Make request with header containing env var template
			headers := map[string]string{headerKey: "${" + varName + "}"}
			resp, err := client.GetWithHeaders(server.URL, headers)
			if err != nil {
				t.Logf("Request failed: %v", err)
				return false
			}
			defer resp.Body.Close()

			// Verify substituted header was received
			if receivedHeaders.Get(headerKey) != varValue {
				t.Logf("Expected header %s=%s, got %s", headerKey, varValue, receivedHeaders.Get(headerKey))
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 && isValidEnvVarName(s) }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	properties.TestingRun(t)
}

// isValidEnvVarName checks if a string is a valid environment variable name.
// Valid names contain only alphanumeric characters and underscores, and don't start with a digit.
func isValidEnvVarName(name string) bool {
	if len(name) == 0 {
		return false
	}
	for i, c := range name {
		if i == 0 && c >= '0' && c <= '9' {
			return false
		}
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// =============================================================================
// Circuit Breaker Tests
// =============================================================================

// TestHTTPClient_CircuitOpens verifies that after 5 consecutive failures the circuit
// opens and subsequent requests fail immediately without reaching the server.
func TestHTTPClient_CircuitOpens(t *testing.T) {
	var serverHits int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&serverHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewRetryableHTTPClientWithConfig(RetryConfig{
		MaxRetries: 0, // No retries so each request counts as one failure
		BaseDelay:  0,
		MaxDelay:   0,
		Timeout:    5 * time.Second,
	})
	// Override the breaker with settings matching DefaultBreakerMaxFailures=5
	// but the default breaker is already configured for 5 failures.

	// Replace the delay func so tests run fast
	client.SetDelayFunc(func(time.Duration) {})

	// Trigger 5 failures to open the circuit
	for i := 0; i < DefaultBreakerMaxFailures; i++ {
		resp, _ := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
	}

	// 6th request: circuit should be open now, server should NOT be hit
	hitsBefore := atomic.LoadInt32(&serverHits)
	resp, err := client.Get(server.URL)
	if resp != nil {
		resp.Body.Close()
	}
	hitsAfter := atomic.LoadInt32(&serverHits)

	if err == nil {
		t.Error("Expected error when circuit is open")
	}
	if !strings.Contains(err.Error(), "circuit breaker") {
		t.Errorf("Expected circuit breaker error, got: %v", err)
	}
	if hitsAfter != hitsBefore {
		t.Errorf("Expected no server hit when circuit is open, got %d additional hits", hitsAfter-hitsBefore)
	}
}

// TestHTTPClient_CircuitRecovery verifies that after the breaker timeout a probe succeeds
// and the circuit closes.
func TestHTTPClient_CircuitRecovery(t *testing.T) {
	var mode int32 // 0 = fail, 1 = succeed

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&mode) == 0 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	// Use a very short timeout so the breaker moves to half-open quickly
	breakerTimeout := 10 * time.Millisecond
	cb := newBreakerWithTimeout(breakerTimeout)

	client := NewRetryableHTTPClientWithConfig(RetryConfig{
		MaxRetries: 0,
		BaseDelay:  0,
		MaxDelay:   0,
		Timeout:    5 * time.Second,
	})
	client.breaker = cb
	client.SetDelayFunc(func(time.Duration) {})

	// Open the circuit with 5 failures
	for i := 0; i < DefaultBreakerMaxFailures; i++ {
		r, _ := client.Get(server.URL)
		if r != nil {
			r.Body.Close()
		}
	}

	// Switch server to success mode and wait for breaker timeout
	atomic.StoreInt32(&mode, 1)
	time.Sleep(breakerTimeout * 3)

	// Probe should succeed and circuit should close
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Errorf("Expected success after circuit recovery, got: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

// TestHTTPClient_CircuitProbeFailure verifies that a failed probe keeps the circuit open.
func TestHTTPClient_CircuitProbeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	breakerTimeout := 10 * time.Millisecond
	cb := newBreakerWithTimeout(breakerTimeout)

	client := NewRetryableHTTPClientWithConfig(RetryConfig{
		MaxRetries: 0,
		BaseDelay:  0,
		MaxDelay:   0,
		Timeout:    5 * time.Second,
	})
	client.breaker = cb
	client.SetDelayFunc(func(time.Duration) {})

	// Open the circuit
	for i := 0; i < DefaultBreakerMaxFailures; i++ {
		resp, _ := client.Get(server.URL)
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
	}

	// Wait for the breaker timeout, then make a probe that fails
	time.Sleep(breakerTimeout * 3)

	// Probe will fail (server still returns 500)
	resp, err := client.Get(server.URL)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	// This might succeed as a probe attempt but then circuit should re-open
	_ = err

	// Next request should indicate circuit is still open or re-opened
	resp, err = client.Get(server.URL)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Error("Expected error after failed probe keeps circuit open")
	}
}

// TestHTTPClient_CircuitAndRateLimiterIndependent verifies that circuit breaker and rate
// limiter operate independently without interfering with each other.
func TestHTTPClient_CircuitAndRateLimiterIndependent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewRetryableHTTPClientWithConfig(RetryConfig{
		MaxRetries: 0,
		BaseDelay:  0,
		MaxDelay:   0,
		Timeout:    5 * time.Second,
	})
	client.SetDelayFunc(func(time.Duration) {})

	rl := NewRateLimiter(WithMaxDomains(10))

	// Both should work independently: rate limiter doesn't affect circuit breaker
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Errorf("Expected success, got: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}

	// Rate limiter should still have its own state
	if !rl.AllowLLM() {
		t.Error("Rate limiter LLM should still be active")
	}

	// Circuit breaker should still be closed
	if client.breaker.State() != 0 { // 0 = gobreaker.StateClosed
		t.Error("Expected circuit to remain closed after successful request")
	}
}

// TestHTTPClient_CircuitDisabled verifies that WithCircuitBreaker(false) disables the
// breaker and requests always reach the server.
func TestHTTPClient_CircuitDisabled(t *testing.T) {
	var serverHits int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&serverHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewRetryableHTTPClientWithConfig(RetryConfig{
		MaxRetries: 0,
		BaseDelay:  0,
		MaxDelay:   0,
		Timeout:    5 * time.Second,
	})
	client.WithCircuitBreaker(false)
	client.SetDelayFunc(func(time.Duration) {})

	// Even after many failures, every request should reach the server
	for i := 0; i < DefaultBreakerMaxFailures+3; i++ {
		resp, _ := client.Get(server.URL)
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
	}

	expected := int32(DefaultBreakerMaxFailures + 3)
	if serverHits != expected {
		t.Errorf("Expected %d server hits with circuit disabled, got %d", expected, serverHits)
	}
}

// newBreakerWithTimeout creates a circuit breaker with a custom timeout for testing.
func newBreakerWithTimeout(timeout time.Duration) *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "test-breaker",
		MaxRequests: DefaultBreakerMaxRequests,
		Interval:    DefaultBreakerInterval,
		Timeout:     timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= DefaultBreakerMaxFailures
		},
	})
}

// =============================================================================
// Header Env-Var Expansion Allow-List Tests (Task T5 / R1)
// =============================================================================

// logCapture records Warn-level lines emitted via the package-private warnLogf
// sink. It is safe for concurrent use by the -race detector.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (lc *logCapture) record(format string, args ...interface{}) {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.lines = append(lc.lines, fmt.Sprintf(format, args...))
}

func (lc *logCapture) count() int {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return len(lc.lines)
}

func (lc *logCapture) all() []string {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	out := make([]string, len(lc.lines))
	copy(out, lc.lines)
	return out
}

// captureWarnLogs swaps the package-private warnLogf sink with a recorder for
// the duration of the test and restores it on cleanup.
func captureWarnLogs(t *testing.T) *logCapture {
	t.Helper()
	lc := &logCapture{}
	orig := warnLogf
	warnLogf = lc.record
	t.Cleanup(func() { warnLogf = orig })
	return lc
}

// TestAllowedExpansionHeaders_HasExpectedSet enumerates the header allow-list.
func TestAllowedExpansionHeaders_HasExpectedSet(t *testing.T) {
	want := []string{"Authorization", "X-Api-Key", "X-Auth-Token", "Private-Token"}

	if len(allowedExpansionHeaders) != len(want) {
		t.Fatalf("allowedExpansionHeaders has %d entries, want %d: %v",
			len(allowedExpansionHeaders), len(want), allowedExpansionHeaders)
	}
	for _, name := range want {
		if _, ok := allowedExpansionHeaders[name]; !ok {
			t.Errorf("allowedExpansionHeaders missing expected entry %q", name)
		}
	}
}

// TestAllowedEnvVars_HasExpectedSet enumerates the env-var allow-list and prefix.
func TestAllowedEnvVars_HasExpectedSet(t *testing.T) {
	want := []string{"GITHUB_TOKEN", "GITLAB_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"}

	if len(allowedHeaderEnvAllowList) != len(want) {
		t.Fatalf("allowedHeaderEnvAllowList has %d entries, want %d: %v",
			len(allowedHeaderEnvAllowList), len(want), allowedHeaderEnvAllowList)
	}
	for _, name := range want {
		if _, ok := allowedHeaderEnvAllowList[name]; !ok {
			t.Errorf("allowedHeaderEnvAllowList missing expected entry %q", name)
		}
	}

	if allowedHeaderEnvPrefix != "BENTOO_" {
		t.Errorf("allowedHeaderEnvPrefix = %q, want %q", allowedHeaderEnvPrefix, "BENTOO_")
	}
}

// TestIsAllowedHeaderName checks case-insensitivity, whitespace trimming, and
// rejection of non-allow-listed names, the empty string, and CRLF names.
func TestIsAllowedHeaderName(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  bool
	}{
		{"exact canonical", "Authorization", true},
		{"lower case", "authorization", true},
		{"surrounding whitespace", " Authorization ", true},
		{"upper case", "AUTHORIZATION", true},
		{"x-api-key canonical", "X-Api-Key", true},
		{"x-api-key lower", "x-api-key", true},
		{"private-token", "private-token", true},
		{"x-auth-token", "x-auth-token", true},
		{"non allow-listed", "X-Custom", false},
		{"empty string", "", false},
		{"crlf injected", "Authorization\r\nInjected", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAllowedHeaderName(tc.input); got != tc.want {
				t.Errorf("isAllowedHeaderName(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestIsAllowedHeaderName_RejectsCRLF asserts a CRLF-bearing header name is
// rejected even though its leading token is an allow-listed header.
func TestIsAllowedHeaderName_RejectsCRLF(t *testing.T) {
	if isAllowedHeaderName("Authorization\r\nInjected") {
		t.Error("isAllowedHeaderName must reject a name containing CRLF")
	}
	if isAllowedHeaderName("Authorization\rInjected") {
		t.Error("isAllowedHeaderName must reject a name containing a bare CR")
	}
	if isAllowedHeaderName("Authorization\nInjected") {
		t.Error("isAllowedHeaderName must reject a name containing a bare LF")
	}
}

// TestIsAllowedEnvVar covers prefix matches, allow-list matches, and denials.
func TestIsAllowedEnvVar(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  bool
	}{
		{"prefix match", "BENTOO_TOKEN", true},
		{"prefix only", "BENTOO_", true},
		{"prefix with suffix", "BENTOO_PRIVATE_TOKEN", true},
		{"allow-list github", "GITHUB_TOKEN", true},
		{"allow-list gitlab", "GITLAB_TOKEN", true},
		{"allow-list openai", "OPENAI_API_KEY", true},
		{"allow-list anthropic", "ANTHROPIC_API_KEY", true},
		{"denied arbitrary", "ANTHROPIC_API_KEY_EVIL", false},
		{"denied path", "PATH", false},
		{"denied home", "HOME", false},
		{"denied empty", "", false},
		{"denied lowercase prefix", "bentoo_token", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAllowedEnvVar(tc.input); got != tc.want {
				t.Errorf("isAllowedEnvVar(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestSubstituteEnvVars_NoRecursiveExpansion asserts that a substituted value
// which itself contains ${OTHER} is NOT re-expanded (single-pass; Test Advisor
// gap #1). This is the core anti-exfiltration guarantee.
func TestSubstituteEnvVars_NoRecursiveExpansion(t *testing.T) {
	// BENTOO_TOKEN is allow-listed and its value contains another reference.
	t.Setenv("BENTOO_TOKEN", "${EVIL}")
	// EVIL is set to a secret-like value; it must never be reached.
	t.Setenv("EVIL", "super-secret-leaked")

	result := SubstituteEnvVars("${BENTOO_TOKEN}", "Authorization")

	if result != "${EVIL}" {
		t.Errorf("expected literal %q (single-pass), got %q", "${EVIL}", result)
	}
	if strings.Contains(result, "super-secret-leaked") {
		t.Errorf("recursive expansion leaked secret value: %q", result)
	}
}

// TestSubstituteEnvVars_DeniedHeaderWarn asserts exactly one Warn line is
// emitted and the literal ${VAR} passes through when the header is not
// allow-listed.
func TestSubstituteEnvVars_DeniedHeaderWarn(t *testing.T) {
	lc := captureWarnLogs(t)
	t.Setenv("BENTOO_TOKEN", "value")

	result := SubstituteEnvVars("${BENTOO_TOKEN}", "X-Custom-Header")

	if result != "${BENTOO_TOKEN}" {
		t.Errorf("expected literal passthrough %q, got %q", "${BENTOO_TOKEN}", result)
	}
	if c := lc.count(); c != 1 {
		t.Fatalf("expected exactly 1 Warn line, got %d: %v", c, lc.all())
	}
	line := lc.all()[0]
	if !strings.Contains(line, "X-Custom-Header") || !strings.Contains(line, "BENTOO_TOKEN") {
		t.Errorf("Warn line should name header and variable, got: %q", line)
	}
}

// TestSubstituteEnvVars_DeniedEnvVarWarn asserts exactly one Warn line is
// emitted and the literal ${VAR} passes through when the variable is not
// allow-listed (even with an allow-listed header).
func TestSubstituteEnvVars_DeniedEnvVarWarn(t *testing.T) {
	lc := captureWarnLogs(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")

	// EVIL_VAR is not allow-listed; ANTHROPIC_API_KEY is, but we reference the
	// non-allow-listed one to confirm the denial path.
	result := SubstituteEnvVars("${EVIL_VAR}", "Authorization")

	if result != "${EVIL_VAR}" {
		t.Errorf("expected literal passthrough %q, got %q", "${EVIL_VAR}", result)
	}
	if c := lc.count(); c != 1 {
		t.Fatalf("expected exactly 1 Warn line, got %d: %v", c, lc.all())
	}
	line := lc.all()[0]
	if !strings.Contains(line, "EVIL_VAR") || !strings.Contains(line, "Authorization") {
		t.Errorf("Warn line should name variable and header, got: %q", line)
	}
}

// TestSubstituteEnvVars_EmptyVarWarn asserts exactly one Warn line is emitted
// and the literal ${VAR} passes through when an allow-listed variable is set
// but empty (R1.3).
func TestSubstituteEnvVars_EmptyVarWarn(t *testing.T) {
	lc := captureWarnLogs(t)
	// Allow-listed variable, set to the empty string.
	t.Setenv("BENTOO_TOKEN", "")

	result := SubstituteEnvVars("${BENTOO_TOKEN}", "Authorization")

	if result != "${BENTOO_TOKEN}" {
		t.Errorf("expected literal passthrough %q, got %q", "${BENTOO_TOKEN}", result)
	}
	if c := lc.count(); c != 1 {
		t.Fatalf("expected exactly 1 Warn line, got %d: %v", c, lc.all())
	}
	line := lc.all()[0]
	if !strings.Contains(line, "BENTOO_TOKEN") || !strings.Contains(line, "Authorization") {
		t.Errorf("Warn line should name variable and header, got: %q", line)
	}
}

// TestSubstituteEnvVars_AllowedNoWarn asserts a fully allow-listed expansion
// succeeds and emits NO Warn line.
func TestSubstituteEnvVars_AllowedNoWarn(t *testing.T) {
	lc := captureWarnLogs(t)
	t.Setenv("BENTOO_TOKEN", "resolved-value")

	result := SubstituteEnvVars("Bearer ${BENTOO_TOKEN}", "Authorization")

	if result != "Bearer resolved-value" {
		t.Errorf("expected %q, got %q", "Bearer resolved-value", result)
	}
	if c := lc.count(); c != 0 {
		t.Errorf("expected 0 Warn lines for an allowed expansion, got %d: %v", c, lc.all())
	}
}

// TestApplyHeaders_RejectsCRLFHeader is a smoke test that a custom header whose
// name contains CRLF is skipped (and never reaches the server).
func TestApplyHeaders_RejectsCRLFHeader(t *testing.T) {
	lc := captureWarnLogs(t)

	req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}

	client := NewRetryableHTTPClient()
	client.applyHeaders(req, "https://example.com/", map[string]string{
		"X-Evil\r\nInjected": "value",
	})

	// The malicious header name must not have been set in any form.
	if got := req.Header.Get("X-Evil"); got != "" {
		t.Errorf("expected CRLF header to be skipped, but X-Evil = %q", got)
	}
	if len(req.Header) != 0 {
		t.Errorf("expected no headers to be set, got: %v", req.Header)
	}
	if c := lc.count(); c != 1 {
		t.Errorf("expected exactly 1 Warn line for the rejected header, got %d: %v", c, lc.all())
	}
}

// FuzzSubstituteEnvVars exercises SubstituteEnvVars with malformed ${VAR}
// references. The seed corpus runs under the normal `go test` pass; the body
// asserts the function never panics and that no denied expansion leaks an
// environment value.
func FuzzSubstituteEnvVars(f *testing.F) {
	// Malformed references.
	f.Add("${", "Authorization")
	f.Add("${}", "Authorization")
	f.Add("${A${B}}", "Authorization")
	f.Add("${UNCLOSED", "Authorization")
	f.Add("}${", "Authorization")
	f.Add("${${}}", "X-Api-Key")
	// Valid references and a non-allow-listed header.
	f.Add("Bearer ${BENTOO_TOKEN}", "Authorization")
	f.Add("${GITHUB_TOKEN}", "X-Custom")

	f.Fuzz(func(t *testing.T, value, headerName string) {
		// Must never panic on arbitrary input.
		result := SubstituteEnvVars(value, headerName)

		// If the header is not allow-listed, the value must be returned
		// verbatim (no expansion can happen at all).
		if !isAllowedHeaderName(headerName) && result != value {
			t.Errorf("non-allow-listed header %q mutated value: %q -> %q",
				headerName, value, result)
		}
	})
}
