package autoupdate

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"golang.org/x/time/rate"
)

// =============================================================================
// Unit Tests
// =============================================================================

// TestPerHostPolicies verifies the per-host HTTP rate-limit tuning: the built-in
// tuned table raises GitHub/GitLab while leaving other hosts at the fallback, and
// WithHostPolicy / WithHTTPInterval override correctly.
func TestPerHostPolicies(t *testing.T) {
	t.Run("zero-config keeps uniform 6s per host", func(t *testing.T) {
		rl := NewRateLimiter()
		for _, h := range []string{"api.github.com", "gitlab.freedesktop.org", "example.com"} {
			if got := rl.HTTPLimit(h); got != rate.Every(6*time.Second) {
				t.Errorf("%s: got %v, want %v", h, got, rate.Every(6*time.Second))
			}
		}
	})

	t.Run("tuned table raises git* hosts, others fall back", func(t *testing.T) {
		rl := NewRateLimiter(WithTunedHostPolicies())
		cases := map[string]rate.Limit{
			"api.github.com":            rate.Every(100 * time.Millisecond),
			"github.com":                rate.Every(100 * time.Millisecond),
			"raw.githubusercontent.com": rate.Every(100 * time.Millisecond),
			"gitlab.freedesktop.org":    rate.Every(300 * time.Millisecond),
			"gitlab.com":                rate.Every(300 * time.Millisecond),
			"example.com":               rate.Every(6 * time.Second), // fallback unchanged
		}
		for h, want := range cases {
			if got := rl.HTTPLimit(h); got != want {
				t.Errorf("%s: got %v, want %v", h, got, want)
			}
		}
	})

	t.Run("WithHostPolicy overrides a single host", func(t *testing.T) {
		rl := NewRateLimiter(WithHostPolicy("pypi.org", 200*time.Millisecond, 3))
		if got := rl.HTTPLimit("pypi.org"); got != rate.Every(200*time.Millisecond) {
			t.Errorf("pypi.org: got %v, want %v", got, rate.Every(200*time.Millisecond))
		}
		if got := rl.HTTPLimit("example.com"); got != rate.Every(6*time.Second) {
			t.Errorf("example.com fallback: got %v, want %v", got, rate.Every(6*time.Second))
		}
	})

	t.Run("explicit WithHostPolicy wins over tuned built-ins", func(t *testing.T) {
		rl := NewRateLimiter(
			WithHostPolicy("api.github.com", 1*time.Second, 1),
			WithTunedHostPolicies(),
		)
		if got := rl.HTTPLimit("api.github.com"); got != rate.Every(1*time.Second) {
			t.Errorf("api.github.com: got %v, want %v (explicit override must win)", got, rate.Every(1*time.Second))
		}
	})

	t.Run("WithHTTPInterval changes the fallback only", func(t *testing.T) {
		rl := NewRateLimiter(WithHTTPInterval(2*time.Second, 1), WithTunedHostPolicies())
		if got := rl.HTTPLimit("example.com"); got != rate.Every(2*time.Second) {
			t.Errorf("fallback: got %v, want %v", got, rate.Every(2*time.Second))
		}
		if got := rl.HTTPLimit("api.github.com"); got != rate.Every(100*time.Millisecond) {
			t.Errorf("tuned host should be unaffected: got %v", got)
		}
	})

	t.Run("invalid values are ignored", func(t *testing.T) {
		rl := NewRateLimiter(WithHTTPInterval(0, 0), WithHostPolicy("", -1, 0))
		if got := rl.HTTPLimit("example.com"); got != rate.Every(6*time.Second) {
			t.Errorf("invalid options must preserve defaults: got %v", got)
		}
	})
}

// TestNewRateLimiter tests that NewRateLimiter creates a valid rate limiter
func TestNewRateLimiter(t *testing.T) {
	rl := NewRateLimiter()
	if rl == nil {
		t.Fatal("Expected non-nil rate limiter")
	}
	if rl.llmLimiter == nil {
		t.Error("Expected non-nil LLM limiter")
	}
	if rl.httpLimiters == nil {
		t.Error("Expected non-nil HTTP limiters map")
	}
}

// TestLLMLimitValue tests that LLM limit is set to 5 per minute
func TestLLMLimitValue(t *testing.T) {
	rl := NewRateLimiter()
	// 5 per minute = 1 per 12 seconds = 1/12 per second
	expectedLimit := rate.Every(12 * time.Second)
	if rl.LLMLimit() != expectedLimit {
		t.Errorf("Expected LLM limit %v, got %v", expectedLimit, rl.LLMLimit())
	}
}

// TestHTTPLimitValue tests that HTTP limit is set to 10 per minute per domain
func TestHTTPLimitValue(t *testing.T) {
	rl := NewRateLimiter()
	// 10 per minute = 1 per 6 seconds = 1/6 per second
	expectedLimit := rate.Every(6 * time.Second)
	if rl.HTTPLimit("example.com") != expectedLimit {
		t.Errorf("Expected HTTP limit %v, got %v", expectedLimit, rl.HTTPLimit("example.com"))
	}
}

// TestHTTPLimiterPerDomain tests that each domain gets its own limiter
func TestHTTPLimiterPerDomain(t *testing.T) {
	rl := NewRateLimiter()

	// Access different domains
	_ = rl.HTTPLimit("example.com")
	_ = rl.HTTPLimit("github.com")
	_ = rl.HTTPLimit("pypi.org")

	if rl.DomainCount() != 3 {
		t.Errorf("Expected 3 domains, got %d", rl.DomainCount())
	}
}

// TestWaitLLMContextCancellation tests that WaitLLM respects context cancellation
func TestWaitLLMContextCancellation(t *testing.T) {
	rl := NewRateLimiter()
	// Consume the burst token first
	_ = rl.AllowLLM()

	// Now create a context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := rl.WaitLLM(ctx)
	if err != ErrRateLimitExceeded {
		t.Errorf("Expected ErrRateLimitExceeded, got %v", err)
	}
}

// TestWaitHTTPContextCancellation tests that WaitHTTP respects context cancellation
func TestWaitHTTPContextCancellation(t *testing.T) {
	rl := NewRateLimiter()
	domain := "example.com"
	// Consume the burst token first
	_ = rl.AllowHTTP(domain)

	// Now create a context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := rl.WaitHTTP(ctx, domain)
	if err != ErrRateLimitExceeded {
		t.Errorf("Expected ErrRateLimitExceeded, got %v", err)
	}
}

// TestWaitHTTPForURL tests URL domain extraction
func TestWaitHTTPForURL(t *testing.T) {
	rl := NewRateLimiter()

	ctx := context.Background()
	err := rl.WaitHTTPForURL(ctx, "https://api.github.com/repos/test/test")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// Should have created a limiter for api.github.com
	if rl.DomainCount() != 1 {
		t.Errorf("Expected 1 domain, got %d", rl.DomainCount())
	}
}

// TestReset tests that Reset clears all state
func TestReset(t *testing.T) {
	rl := NewRateLimiter()

	// Create some HTTP limiters
	_ = rl.HTTPLimit("example.com")
	_ = rl.HTTPLimit("github.com")

	if rl.DomainCount() != 2 {
		t.Errorf("Expected 2 domains before reset, got %d", rl.DomainCount())
	}

	rl.Reset()

	if rl.DomainCount() != 0 {
		t.Errorf("Expected 0 domains after reset, got %d", rl.DomainCount())
	}
}

// TestAllowLLM tests the non-blocking AllowLLM method
func TestAllowLLM(t *testing.T) {
	rl := NewRateLimiter()
	// First request should be allowed (burst of 1)
	if !rl.AllowLLM() {
		t.Error("First LLM request should be allowed")
	}
	// Second immediate request should not be allowed
	if rl.AllowLLM() {
		t.Error("Second immediate LLM request should not be allowed")
	}
}

// TestAllowHTTP tests the non-blocking AllowHTTP method
func TestAllowHTTP(t *testing.T) {
	rl := NewRateLimiter()
	domain := "example.com"
	// First request should be allowed (burst of 1)
	if !rl.AllowHTTP(domain) {
		t.Error("First HTTP request should be allowed")
	}
	// Second immediate request should not be allowed
	if rl.AllowHTTP(domain) {
		t.Error("Second immediate HTTP request should not be allowed")
	}
}

// =============================================================================
// Eviction Tests
// =============================================================================

// advanceClock is a controllable clock for eviction tests.
type advanceClock struct {
	mu  sync.Mutex
	now time.Time
}

func newAdvanceClock(t time.Time) *advanceClock { return &advanceClock{now: t} }
func (c *advanceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *advanceClock) Sleep(_ time.Duration) {}
func (c *advanceClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestRateLimiter_DomainEviction verifies that DomainCount never exceeds maxDomains.
func TestRateLimiter_DomainEviction(t *testing.T) {
	rl := NewRateLimiter(WithMaxDomains(5), WithCleanupAge(1*time.Hour))

	for i := 0; i < 10; i++ {
		domain := "domain" + string(rune('a'+i)) + ".example.com"
		_ = rl.HTTPLimit(domain)
	}

	if rl.DomainCount() > 5 {
		t.Errorf("Expected DomainCount() <= 5 after eviction, got %d", rl.DomainCount())
	}
}

// TestRateLimiter_CleanupAge verifies that entries older than cleanupAge are evicted when
// space is needed.
func TestRateLimiter_CleanupAge(t *testing.T) {
	clock := newAdvanceClock(time.Now())
	rl := NewRateLimiter(WithMaxDomains(3), WithCleanupAge(10*time.Minute), WithClock(clock))

	// Fill to capacity
	_ = rl.HTTPLimit("alpha.example.com")
	_ = rl.HTTPLimit("beta.example.com")
	_ = rl.HTTPLimit("gamma.example.com")

	if rl.DomainCount() != 3 {
		t.Fatalf("Expected 3 domains, got %d", rl.DomainCount())
	}

	// Age all entries past cleanupAge
	clock.Advance(20 * time.Minute)

	// Adding a new domain should trigger eviction of the aged entries
	_ = rl.HTTPLimit("delta.example.com")

	if rl.DomainCount() > 3 {
		t.Errorf("Expected DomainCount() <= 3 after age eviction, got %d", rl.DomainCount())
	}
}

// TestRateLimiter_LRUEviction verifies that when all entries are within cleanupAge,
// the least recently used entry is evicted.
func TestRateLimiter_LRUEviction(t *testing.T) {
	clock := newAdvanceClock(time.Now())
	rl := NewRateLimiter(WithMaxDomains(3), WithCleanupAge(1*time.Hour), WithClock(clock))

	// Add three domains at different times so we know which is LRU
	_ = rl.HTTPLimit("oldest.example.com")
	clock.Advance(1 * time.Second)
	_ = rl.HTTPLimit("middle.example.com")
	clock.Advance(1 * time.Second)
	_ = rl.HTTPLimit("newest.example.com")

	if rl.DomainCount() != 3 {
		t.Fatalf("Expected 3 domains, got %d", rl.DomainCount())
	}

	// Adding a 4th domain should evict "oldest"
	clock.Advance(1 * time.Second)
	_ = rl.HTTPLimit("fourth.example.com")

	if rl.DomainCount() > 3 {
		t.Errorf("Expected DomainCount() <= 3 after LRU eviction, got %d", rl.DomainCount())
	}
}

// TestRateLimiter_LLMUnaffected verifies that eviction of HTTP domain limiters does not
// affect the LLM limiter.
func TestRateLimiter_LLMUnaffected(t *testing.T) {
	rl := NewRateLimiter(WithMaxDomains(2))

	// Consume the LLM burst token
	if !rl.AllowLLM() {
		t.Fatal("Expected first LLM request to be allowed")
	}
	llmLimitBefore := rl.LLMLimit()

	// Trigger eviction by filling and overflowing HTTP domains
	for i := 0; i < 5; i++ {
		domain := "domain" + string(rune('a'+i)) + ".example.com"
		_ = rl.HTTPLimit(domain)
	}

	// LLM limiter should be unchanged
	if rl.LLMLimit() != llmLimitBefore {
		t.Error("Expected LLM limit to be unchanged after HTTP domain eviction")
	}
	// LLM burst should still be consumed (2nd request denied)
	if rl.AllowLLM() {
		t.Error("Expected second immediate LLM request to be denied")
	}
}

// TestRateLimiter_ResetClears verifies that Reset clears all entries even after eviction.
func TestRateLimiter_ResetClears(t *testing.T) {
	rl := NewRateLimiter(WithMaxDomains(3))

	// Add domains and trigger some eviction
	for i := 0; i < 6; i++ {
		domain := "domain" + string(rune('a'+i)) + ".example.com"
		_ = rl.HTTPLimit(domain)
	}

	rl.Reset()

	if rl.DomainCount() != 0 {
		t.Errorf("Expected 0 domains after Reset, got %d", rl.DomainCount())
	}
}

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestLLMRateLimiting tests Property 26: LLM Rate Limiting
// **Feature: autoupdate-analyzer, Property 26: LLM Rate Limiting**
// **Validates: Requirements 11.1**
//
// For any sequence of LLM requests, the rate limiter SHALL ensure no more than
// 5 requests are made within any 60-second window.
func TestLLMRateLimiting(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: LLM rate is limited to 5 per minute (1 per 12 seconds)
	properties.Property("LLM rate limit is 5 per minute", prop.ForAll(
		func(dummy int) bool {
			rl := NewRateLimiter()
			// 5 per minute = 1 per 12 seconds
			expectedLimit := rate.Every(12 * time.Second)
			return rl.LLMLimit() == expectedLimit
		},
		gen.IntRange(1, 100),
	))

	// Property: First LLM request is always allowed (burst of 1)
	properties.Property("First LLM request is always allowed", prop.ForAll(
		func(dummy int) bool {
			rl := NewRateLimiter()
			return rl.AllowLLM()
		},
		gen.IntRange(1, 100),
	))

	// Property: Second immediate LLM request is not allowed
	properties.Property("Second immediate LLM request is not allowed", prop.ForAll(
		func(dummy int) bool {
			rl := NewRateLimiter()
			_ = rl.AllowLLM()     // First request
			return !rl.AllowLLM() // Second should be denied
		},
		gen.IntRange(1, 100),
	))

	// Property: LLM reservation delay is approximately 12 seconds
	properties.Property("LLM reservation delay is approximately 12 seconds", prop.ForAll(
		func(dummy int) bool {
			rl := NewRateLimiter()
			_ = rl.AllowLLM() // Consume the burst token

			reservation := rl.ReserveLLM()
			delay := reservation.Delay()
			reservation.Cancel()

			// Delay should be close to 12 seconds (allow some tolerance)
			return delay >= 11*time.Second && delay <= 13*time.Second
		},
		gen.IntRange(1, 100),
	))

	// Property: Multiple LLM requests require waiting
	properties.Property("Multiple LLM requests require waiting", prop.ForAll(
		func(numRequests int) bool {
			if numRequests < 2 {
				return true // Skip trivial cases
			}

			rl := NewRateLimiter()
			allowedCount := 0

			for i := 0; i < numRequests; i++ {
				if rl.AllowLLM() {
					allowedCount++
				}
			}

			// Only 1 request should be allowed immediately (burst)
			return allowedCount == 1
		},
		gen.IntRange(2, 10),
	))

	// Property: WaitLLM respects context cancellation
	properties.Property("WaitLLM respects context cancellation", prop.ForAll(
		func(dummy int) bool {
			rl := NewRateLimiter()
			// Consume the burst token first
			_ = rl.AllowLLM()

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // Cancel immediately

			err := rl.WaitLLM(ctx)
			return err == ErrRateLimitExceeded
		},
		gen.IntRange(1, 100),
	))

	// Property: Concurrent LLM requests are properly rate limited
	properties.Property("Concurrent LLM requests are properly rate limited", prop.ForAll(
		func(numGoroutines int) bool {
			if numGoroutines < 2 {
				return true
			}

			rl := NewRateLimiter()
			var allowedCount int
			var mu sync.Mutex
			var wg sync.WaitGroup

			for i := 0; i < numGoroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if rl.AllowLLM() {
						mu.Lock()
						allowedCount++
						mu.Unlock()
					}
				}()
			}

			wg.Wait()

			// Only 1 request should be allowed immediately
			return allowedCount == 1
		},
		gen.IntRange(2, 20),
	))

	properties.TestingRun(t)
}

// TestHTTPRateLimiting tests Property 27: HTTP Rate Limiting
// **Feature: autoupdate-analyzer, Property 27: HTTP Rate Limiting**
// **Validates: Requirements 11.2**
//
// For any sequence of HTTP requests to the same domain, the rate limiter SHALL
// ensure no more than 10 requests are made within any 60-second window.
func TestHTTPRateLimiting(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: HTTP rate is limited to 10 per minute (1 per 6 seconds)
	properties.Property("HTTP rate limit is 10 per minute per domain", prop.ForAll(
		func(domain string) bool {
			rl := NewRateLimiter()
			// 10 per minute = 1 per 6 seconds
			expectedLimit := rate.Every(6 * time.Second)
			return rl.HTTPLimit(domain) == expectedLimit
		},
		gen.OneConstOf(
			"example.com",
			"github.com",
			"pypi.org",
			"api.github.com",
		),
	))

	// Property: First HTTP request to a domain is always allowed (burst of 1)
	properties.Property("First HTTP request to a domain is always allowed", prop.ForAll(
		func(domain string) bool {
			rl := NewRateLimiter()
			return rl.AllowHTTP(domain)
		},
		gen.OneConstOf(
			"example.com",
			"github.com",
			"pypi.org",
			"npmjs.com",
		),
	))

	// Property: Second immediate HTTP request to same domain is not allowed
	properties.Property("Second immediate HTTP request to same domain is not allowed", prop.ForAll(
		func(domain string) bool {
			rl := NewRateLimiter()
			_ = rl.AllowHTTP(domain)     // First request
			return !rl.AllowHTTP(domain) // Second should be denied
		},
		gen.OneConstOf(
			"example.com",
			"github.com",
			"pypi.org",
			"crates.io",
		),
	))

	// Property: HTTP reservation delay is approximately 6 seconds
	properties.Property("HTTP reservation delay is approximately 6 seconds", prop.ForAll(
		func(domain string) bool {
			rl := NewRateLimiter()
			_ = rl.AllowHTTP(domain) // Consume the burst token

			reservation := rl.ReserveHTTP(domain)
			delay := reservation.Delay()
			reservation.Cancel()

			// Delay should be close to 6 seconds (allow some tolerance)
			return delay >= 5*time.Second && delay <= 7*time.Second
		},
		gen.OneConstOf(
			"example.com",
			"github.com",
			"pypi.org",
		),
	))

	// Property: Different domains have independent rate limits
	properties.Property("Different domains have independent rate limits", prop.ForAll(
		func(domain1, domain2 string) bool {
			if domain1 == domain2 {
				return true // Skip when domains are the same
			}

			rl := NewRateLimiter()

			// First request to domain1 should be allowed
			allowed1 := rl.AllowHTTP(domain1)
			// First request to domain2 should also be allowed (independent)
			allowed2 := rl.AllowHTTP(domain2)

			return allowed1 && allowed2
		},
		gen.OneConstOf("example.com", "github.com"),
		gen.OneConstOf("pypi.org", "npmjs.com"),
	))

	// Property: Multiple HTTP requests to same domain require waiting
	properties.Property("Multiple HTTP requests to same domain require waiting", prop.ForAll(
		func(numRequests int, domain string) bool {
			if numRequests < 2 {
				return true // Skip trivial cases
			}

			rl := NewRateLimiter()
			allowedCount := 0

			for i := 0; i < numRequests; i++ {
				if rl.AllowHTTP(domain) {
					allowedCount++
				}
			}

			// Only 1 request should be allowed immediately (burst)
			return allowedCount == 1
		},
		gen.IntRange(2, 10),
		gen.OneConstOf("example.com", "github.com", "pypi.org"),
	))

	// Property: WaitHTTP respects context cancellation
	properties.Property("WaitHTTP respects context cancellation", prop.ForAll(
		func(domain string) bool {
			rl := NewRateLimiter()
			// Consume the burst token first
			_ = rl.AllowHTTP(domain)

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // Cancel immediately

			err := rl.WaitHTTP(ctx, domain)
			return err == ErrRateLimitExceeded
		},
		gen.OneConstOf(
			"example.com",
			"github.com",
			"pypi.org",
		),
	))

	// Property: Each domain gets its own limiter
	properties.Property("Each domain gets its own limiter", prop.ForAll(
		func(domains []string) bool {
			if len(domains) == 0 {
				return true
			}

			rl := NewRateLimiter()

			// Access each domain
			uniqueDomains := make(map[string]bool)
			for _, domain := range domains {
				_ = rl.HTTPLimit(domain)
				uniqueDomains[domain] = true
			}

			// Domain count should match unique domains
			return rl.DomainCount() == len(uniqueDomains)
		},
		gen.SliceOfN(5, gen.OneConstOf(
			"example.com",
			"github.com",
			"pypi.org",
			"npmjs.com",
			"crates.io",
		)),
	))

	// Property: Concurrent HTTP requests to same domain are properly rate limited
	properties.Property("Concurrent HTTP requests to same domain are properly rate limited", prop.ForAll(
		func(numGoroutines int, domain string) bool {
			if numGoroutines < 2 {
				return true
			}

			rl := NewRateLimiter()
			var allowedCount int
			var mu sync.Mutex
			var wg sync.WaitGroup

			for i := 0; i < numGoroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if rl.AllowHTTP(domain) {
						mu.Lock()
						allowedCount++
						mu.Unlock()
					}
				}()
			}

			wg.Wait()

			// Only 1 request should be allowed immediately
			return allowedCount == 1
		},
		gen.IntRange(2, 20),
		gen.OneConstOf("example.com", "github.com"),
	))

	// Property: URL domain extraction works correctly
	properties.Property("URL domain extraction works correctly", prop.ForAll(
		func(url string) bool {
			rl := NewRateLimiter()
			ctx := context.Background()

			// Should not error on valid URLs
			err := rl.WaitHTTPForURL(ctx, url)
			return err == nil
		},
		gen.OneConstOf(
			"https://api.github.com/repos/test/test",
			"https://pypi.org/pypi/requests/json",
			"https://registry.npmjs.org/express",
			"https://crates.io/api/v1/crates/serde",
		),
	))

	properties.TestingRun(t)
}
