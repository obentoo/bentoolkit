package fetch

import (
	"context"
	"testing"
	"time"
)

// TestCacheSave tests Cache.Save persists current state
func TestCacheSave(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	// Manually add entry without auto-save
	cache.Entries["app-misc/test"] = CacheEntry{
		Version:   "1.0.0",
		Timestamp: time.Now(),
		Source:    "https://example.com",
	}

	if err := cache.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload and verify
	cache2, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("NewCache reload: %v", err)
	}
	if _, found := cache2.Get("app-misc/test"); !found {
		t.Error("Expected entry to persist after Save")
	}
}

// TestWithClock tests the WithClock option for RateLimiter
func TestWithClock(t *testing.T) {
	mockClock := &mockClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rl := NewRateLimiter(WithClock(mockClock))

	if rl.clock != mockClock {
		t.Error("Expected clock to be set via WithClock")
	}
}

// mockClock implements Clock for testing
type mockClock struct {
	now time.Time
}

func (m *mockClock) Now() time.Time { return m.now }

func (m *mockClock) Sleep(d time.Duration) { m.now = m.now.Add(d) }

// =============================================================================
// httpclient.isTimeoutError
// =============================================================================

// TestIsTimeoutError tests the isTimeoutError helper
func TestIsTimeoutError(t *testing.T) {
	if isTimeoutError(nil) {
		t.Error("nil error should not be timeout")
	}
	if !isTimeoutError(context.DeadlineExceeded) {
		t.Error("DeadlineExceeded should be timeout")
	}
}

// =============================================================================
// realClock Now/Sleep
// =============================================================================

// TestRealClockNow tests realClock.Now returns a non-zero time
func TestRealClockNow(t *testing.T) {
	c := realClock{}
	if c.Now().IsZero() {
		t.Error("realClock.Now() should not return zero time")
	}
}

// TestRealClockSleep tests realClock.Sleep does not panic
func TestRealClockSleep(t *testing.T) {
	c := realClock{}
	c.Sleep(0) // zero duration — should not block
}
