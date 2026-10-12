package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// R9.1: one client is shared by concurrent workers and every request among
// them is retried. Run under -race: the delay each retry records must be
// written without a data race, and none may be lost. A reader polls the
// recorded delays while the requests run, as a test seam would.
func TestRecordedDelaysAreSafeUnderConcurrentRetries(t *testing.T) {
	const (
		workers   = 16
		perWorker = 4
		requests  = workers * perWorker
	)

	// Each request id is answered 503 the first time and 200 after, so every
	// request makes exactly one retry.
	var (
		mu   sync.Mutex
		seen = map[string]int{}
		hits atomic.Int64
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		id := r.URL.Query().Get("id")
		mu.Lock()
		seen[id]++
		n := seen[id]
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// The breaker is off: interleaved 503s from many workers would otherwise
	// open it, and the subject here is the retry path, not the breaker.
	c := NewRetryableHTTPClient().WithCircuitBreaker(false)
	c.SetHTTPClient(srv.Client())
	c.SetDelayFunc(func(time.Duration) {})

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	start := make(chan struct{})
	done := make(chan struct{})
	var readerWG sync.WaitGroup
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		<-start
		for {
			select {
			case <-done:
				return
			default:
				_ = len(c.GetRecordedDelays())
			}
		}
	}()

	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range perWorker {
				url := fmt.Sprintf("%s/?id=%d-%d", srv.URL, w, i)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					errs <- err
					return
				}
				resp, err := c.DoWithContext(ctx, req)
				if err != nil {
					errs <- fmt.Errorf("GET %s: %w", url, err)
					continue
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("GET %s: status %d, want 200 after one retry", url, resp.StatusCode)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(done)
	readerWG.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	retries := int(hits.Load()) - requests
	if retries != requests {
		t.Fatalf("the server saw %d retries, want %d (one per request)", retries, requests)
	}
	if got := len(c.GetRecordedDelays()); got != retries {
		t.Errorf("recorded %d delays for %d retries", got, retries)
	}
}
