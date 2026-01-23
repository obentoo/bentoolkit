package autoupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
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
			_, err := client.Get(server.URL)
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

	_, err := client.Get(server.URL)
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

			_, err := client.Get(server.URL)
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

	_, err := client.GetWithContext(ctx, server.URL)
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
