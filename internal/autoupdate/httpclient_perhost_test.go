package autoupdate

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// perHostRT is an in-memory transport that answers by URL.Host: a host listed
// as dead gets a 500, every other host a 200. It counts the requests each host
// actually received, so "refused without reaching the server" is observable.
type perHostRT struct {
	mu   sync.Mutex
	dead map[string]bool
	hits map[string]int
}

func newPerHostRT(dead ...string) *perHostRT {
	rt := &perHostRT{dead: map[string]bool{}, hits: map[string]int{}}
	for _, h := range dead {
		rt.dead[h] = true
	}
	return rt
}

func (rt *perHostRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.hits[r.URL.Host]++
	code := http.StatusOK
	if rt.dead[r.URL.Host] {
		code = http.StatusInternalServerError
	}
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: code,
		Status:     fmt.Sprintf("%d %s", code, http.StatusText(code)),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    r,
	}, nil
}

func (rt *perHostRT) count(host string) int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.hits[host]
}

// newPerHostClient returns a breaker-enabled client with no retries, so every
// call is exactly one breaker-counted attempt.
func newPerHostClient(rt http.RoundTripper) *RetryableHTTPClient {
	c := NewRetryableHTTPClientWithConfig(RetryConfig{MaxRetries: 0, Timeout: 5 * time.Second})
	if rt != nil {
		c.SetHTTPClient(&http.Client{Transport: rt, Timeout: 5 * time.Second})
	}
	return c
}

// perHostGet performs one GET and reports any failure, including a non-200.
func perHostGet(c *RetryableHTTPClient, rawURL string) error {
	resp, err := c.Get(rawURL)
	if resp != nil && resp.Body != nil {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck // test drain
		resp.Body.Close()
	}
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func perHostServer(t *testing.T, code int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	hits := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func perHostHostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", rawURL, err)
	}
	return u.Host
}

// TestHTTPClient_CircuitIsPerHost pins R1.1: five consecutive failures on one
// host:port open that host's breaker only.
func TestHTTPClient_CircuitIsPerHost(t *testing.T) {
	t.Run("a failing host does not stop a healthy one", func(t *testing.T) {
		rt := newPerHostRT("dead.test")
		c := newPerHostClient(rt)

		for i := 0; i < DefaultBreakerMaxFailures; i++ {
			_ = perHostGet(c, "http://dead.test/pkg") //nolint:errcheck // failures are the point
		}
		if err := perHostGet(c, "http://dead.test/pkg"); err == nil {
			t.Error("6th request to the failing host succeeded; want it refused")
		}
		if got := rt.count("dead.test"); got != DefaultBreakerMaxFailures {
			t.Errorf("failing host received %d requests, want %d (the 6th must be refused before the wire)", got, DefaultBreakerMaxFailures)
		}

		if err := perHostGet(c, "http://healthy.test/pkg"); err != nil {
			t.Errorf("healthy host refused after another host failed: %v", err)
		}
		if got := rt.count("healthy.test"); got != 1 {
			t.Errorf("healthy host received %d requests, want 1", got)
		}
	})

	// Wrongly collapse: failures on two different hosts must never be added
	// into one consecutive-failure count.
	t.Run("failures on two hosts are not summed", func(t *testing.T) {
		rt := newPerHostRT("a.test", "b.test")
		c := newPerHostClient(rt)

		for _, h := range []string{"a.test", "b.test", "a.test", "b.test", "a.test"} {
			_ = perHostGet(c, "http://"+h+"/pkg") //nolint:errcheck // failures are the point
		}
		_ = perHostGet(c, "http://a.test/pkg") //nolint:errcheck
		_ = perHostGet(c, "http://b.test/pkg") //nolint:errcheck

		if got := rt.count("a.test"); got != 4 {
			t.Errorf("a.test received %d requests, want 4: its breaker has seen only 3 failures and must still be closed", got)
		}
		if got := rt.count("b.test"); got != 3 {
			t.Errorf("b.test received %d requests, want 3: its breaker has seen only 2 failures and must still be closed", got)
		}
	})

	// Wrongly split: one host:port reached through different paths and queries
	// is ONE upstream, and its failures open ONE breaker.
	t.Run("one host:port shares one breaker across paths", func(t *testing.T) {
		dead, hits := perHostServer(t, http.StatusInternalServerError)
		c := newPerHostClient(nil)

		for _, p := range []string{"/a", "/b?x=1", "/c/d", "/a?x=2", "/"} {
			_ = perHostGet(c, dead.URL+p) //nolint:errcheck // failures are the point
		}
		before := hits.Load()
		if err := perHostGet(c, dead.URL+"/never-requested"); err == nil {
			t.Error("request to a new path of a failing host:port succeeded; want it refused")
		}
		if after := hits.Load(); after != before {
			t.Errorf("refused request reached the server (%d extra hits); the breaker must be keyed by host:port, not by URL", after-before)
		}
	})
}

// TestHTTPClient_CircuitRefusalNamesHost pins R1.2: a refusal says "circuit
// breaker open" and names the refused host:port — its own, never a sibling's.
func TestHTTPClient_CircuitRefusalNamesHost(t *testing.T) {
	t.Run("real upstream", func(t *testing.T) {
		dead, _ := perHostServer(t, http.StatusInternalServerError)
		c := newPerHostClient(nil)

		for i := 0; i < DefaultBreakerMaxFailures; i++ {
			_ = perHostGet(c, dead.URL) //nolint:errcheck // failures are the point
		}
		err := perHostGet(c, dead.URL)
		if err == nil {
			t.Fatal("6th request to the failing host succeeded; want a refusal")
		}
		if !strings.Contains(err.Error(), "circuit breaker open") {
			t.Errorf("refusal %q lacks \"circuit breaker open\"", err)
		}
		if host := perHostHostOf(t, dead.URL); !strings.Contains(err.Error(), host) {
			t.Errorf("refusal %q does not name the refused host:port %q", err, host)
		}
	})

	// Two open breakers whose keys share a prefix: each refusal must carry
	// exactly its own key. "api.test:80" is a prefix of "api.test:8080", so the
	// :80 refusal is also checked NOT to mention :8080.
	t.Run("each refusal names its own host, never a sibling", func(t *testing.T) {
		const long, short = "api.test:8080", "api.test:80"
		rt := newPerHostRT(long, short)
		c := newPerHostClient(rt)

		for i := 0; i < DefaultBreakerMaxFailures; i++ {
			_ = perHostGet(c, "http://"+long+"/x") //nolint:errcheck
		}
		for i := 0; i < DefaultBreakerMaxFailures; i++ {
			_ = perHostGet(c, "http://"+short+"/x") //nolint:errcheck
		}

		errLong := perHostGet(c, "http://"+long+"/x")
		errShort := perHostGet(c, "http://"+short+"/x")
		if errLong == nil || errShort == nil {
			t.Fatalf("want both hosts refused, got %v / %v", errLong, errShort)
		}
		if !strings.Contains(errLong.Error(), long) {
			t.Errorf("refusal for %s = %q; want it to name %s", long, errLong, long)
		}
		if !strings.Contains(errShort.Error(), short) || strings.Contains(errShort.Error(), long) {
			t.Errorf("refusal for %s = %q; want it to name %s and not %s", short, errShort, short, long)
		}
		if got := rt.count(short); got != DefaultBreakerMaxFailures {
			t.Errorf("%s received %d requests, want %d: it must fail on its own before it is refused", short, got, DefaultBreakerMaxFailures)
		}
	})
}

// TestHTTPClient_CircuitKeyIncludesPort pins R1.3: the key is URL.Host, port
// included, so upstreams that share a hostname but not a port never share a
// breaker — and neither do ports that are textual prefixes of one another.
func TestHTTPClient_CircuitKeyIncludesPort(t *testing.T) {
	t.Run("two local servers differ only by port", func(t *testing.T) {
		dead, _ := perHostServer(t, http.StatusInternalServerError)
		healthy, healthyHits := perHostServer(t, http.StatusOK)
		du, _ := url.Parse(dead.URL)
		hu, _ := url.Parse(healthy.URL)
		if du.Hostname() != hu.Hostname() || du.Port() == hu.Port() {
			t.Fatalf("fixture broken: want same hostname, different ports; got %s and %s", du.Host, hu.Host)
		}

		c := newPerHostClient(nil)
		for i := 0; i < DefaultBreakerMaxFailures; i++ {
			_ = perHostGet(c, dead.URL) //nolint:errcheck
		}
		if err := perHostGet(c, healthy.URL); err != nil {
			t.Errorf("%s refused after %s failed: %v", hu.Host, du.Host, err)
		}
		if got := healthyHits.Load(); got != 1 {
			t.Errorf("healthy server received %d requests, want 1", got)
		}
	})

	t.Run("prefix and suffix ports are distinct hosts", func(t *testing.T) {
		const failing = "svc.test:8080"
		rt := newPerHostRT(failing)
		c := newPerHostClient(rt)
		for i := 0; i < DefaultBreakerMaxFailures; i++ {
			_ = perHostGet(c, "http://"+failing+"/x") //nolint:errcheck
		}

		for _, other := range []string{"svc.test:808", "svc.test:80", "svc.test", "svc.test:18080", "svc.test:8081"} {
			if err := perHostGet(c, "http://"+other+"/x"); err != nil {
				t.Errorf("%s refused after %s failed: %v", other, failing, err)
			}
			if got := rt.count(other); got != 1 {
				t.Errorf("%s received %d requests, want 1", other, got)
			}
		}
		if err := perHostGet(c, "http://"+failing+"/other"); err == nil {
			t.Errorf("%s served after 5 failures; want it refused", failing)
		}
	})
}

// TestHTTPClient_CircuitConcurrentHosts pins R1.4: breakers for many hosts are
// created and looked up concurrently on one client without a data race (the
// test is meaningful under -race), and a host that fails concurrently with
// healthy ones never refuses them.
func TestHTTPClient_CircuitConcurrentHosts(t *testing.T) {
	const hosts, workersPerHost, requestsPerWorker = 8, 4, 10

	var dead []string
	for i := 0; i < hosts; i += 2 {
		dead = append(dead, fmt.Sprintf("h%d.test", i))
	}
	rt := newPerHostRT(dead...)
	c := newPerHostClient(rt)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		healthy = map[string][]error{}
		start   = make(chan struct{})
	)
	for i := 0; i < hosts; i++ {
		host := fmt.Sprintf("h%d.test", i)
		isDead := i%2 == 0
		for w := 0; w < workersPerHost; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for r := 0; r < requestsPerWorker; r++ {
					err := perHostGet(c, "http://"+host+"/pkg")
					if !isDead && err != nil {
						mu.Lock()
						healthy[host] = append(healthy[host], err)
						mu.Unlock()
					}
				}
			}()
		}
	}
	close(start)
	wg.Wait()

	for host, errs := range healthy {
		t.Errorf("healthy host %s failed %d of %d requests while other hosts were failing; first: %v",
			host, len(errs), workersPerHost*requestsPerWorker, errs[0])
	}
	for i := 1; i < hosts; i += 2 {
		host := fmt.Sprintf("h%d.test", i)
		if got := rt.count(host); got != workersPerHost*requestsPerWorker {
			t.Errorf("healthy host %s received %d requests, want %d", host, got, workersPerHost*requestsPerWorker)
		}
	}
	for _, host := range dead {
		err := perHostGet(c, "http://"+host+"/pkg")
		if err == nil || !strings.Contains(err.Error(), "circuit breaker open") || !strings.Contains(err.Error(), host) {
			t.Errorf("after concurrent failures, %s gave %v; want a refusal naming it", host, err)
		}
	}
}
