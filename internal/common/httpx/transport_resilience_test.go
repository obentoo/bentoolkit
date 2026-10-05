package httpx

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Environment contract between TestBuildTransportHonoursProxyEnvironment (the
// parent) and TestHelperProxyChild (the re-exec'd child).
//
// http.ProxyFromEnvironment reads the environment once per process and never
// proxies a loopback host, so the proxy behaviour can only be observed in a
// fresh process asking for a non-loopback hostname. The child refuses every
// dial except the one to the recording proxy, so "went direct" is observable
// without DNS or network access.
const (
	proxyChildRoleEnv    = "BENTOO_TEST_PROXY_CHILD"   // "1" selects the child role
	proxyChildTargetEnv  = "BENTOO_TEST_PROXY_TARGET"  // URL the child requests
	proxyChildAllowEnv   = "BENTOO_TEST_PROXY_ALLOW"   // the only address the child may really dial
	proxyChildBuilderEnv = "BENTOO_TEST_PROXY_BUILDER" // "h2" = BuildTransport, "h1" = BuildTransportHTTP1

	proxyTestHost = "upstream.invalid.example"
)

// TestHelperProxyChild is the child half of the proxy test. Run directly it
// skips. In the child role it builds a transport with the selected builder,
// replaces only its dialer (Proxy is left as the builder set it), requests the
// target once and prints what happened.
func TestHelperProxyChild(t *testing.T) {
	if os.Getenv(proxyChildRoleEnv) != "1" {
		t.Skip("helper process for TestBuildTransportHonoursProxyEnvironment")
	}

	var tr *http.Transport
	if os.Getenv(proxyChildBuilderEnv) == "h1" {
		tr = BuildTransportHTTP1()
	} else {
		tr = BuildTransport()
	}

	allowed := os.Getenv(proxyChildAllowEnv)
	var dialer net.Dialer
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == allowed {
			return dialer.DialContext(ctx, network, addr)
		}
		fmt.Printf("DIRECT %s\n", addr)
		return nil, fmt.Errorf("test child: direct dial to %s refused", addr)
	}

	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	resp, err := client.Get(os.Getenv(proxyChildTargetEnv))
	if err != nil {
		fmt.Printf("ERROR %v\n", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("ERROR reading body: %v\n", err)
		return
	}
	fmt.Printf("STATUS %d BODY %s\n", resp.StatusCode, body)
}

// recordingProxy is a forward proxy that records the request line of every
// request it receives. Plain-HTTP requests (absolute-URI form) are answered
// with "via-proxy"; CONNECT tunnels are refused with 403 once recorded.
type recordingProxy struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []string
}

func newRecordingProxy(t *testing.T) *recordingProxy {
	t.Helper()
	p := &recordingProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.seen = append(p.seen, r.Method+" "+r.RequestURI)
		p.mu.Unlock()
		if r.Method == http.MethodConnect {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, "via-proxy")
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *recordingProxy) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// proxyEnvKeys are every variable http.ProxyFromEnvironment consults. They are
// stripped from the child's inherited environment so the test is hermetic.
var proxyEnvKeys = []string{
	"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy",
	"NO_PROXY", "no_proxy", "REQUEST_METHOD",
}

func runProxyChild(t *testing.T, builder, target, allow string, extra map[string]string) string {
	t.Helper()

	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, k := range proxyEnvKeys {
			if key == k {
				drop = true
				break
			}
		}
		if !drop {
			env = append(env, kv)
		}
	}
	env = append(env,
		proxyChildRoleEnv+"=1",
		proxyChildTargetEnv+"="+target,
		proxyChildAllowEnv+"="+allow,
		proxyChildBuilderEnv+"="+builder,
	)
	for k, v := range extra {
		env = append(env, k+"="+v)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProxyChild$", "-test.count=1", "-test.v")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("proxy child process failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestBuildTransportHonoursProxyEnvironment pins R6.1: every client built on
// BuildTransport or BuildTransportHTTP1 routes by HTTP_PROXY / HTTPS_PROXY /
// NO_PROXY. The hostile halves are the requests the proxy must NOT see: a host
// listed in NO_PROXY, and an https request when only HTTP_PROXY is set.
func TestBuildTransportHonoursProxyEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		builder string
		target  string
		env     func(proxyURL string) map[string]string
		// wantProxied is the request line the proxy must record, or "" when the
		// proxy must record nothing.
		wantProxied string
		// wantDirect is the address the child must have dialled directly, or ""
		// when it must not dial anything but the proxy.
		wantDirect string
		wantOut    string
	}{
		{
			name:    "HTTP_PROXY routes a BuildTransport request",
			builder: "h2",
			target:  "http://" + proxyTestHost + "/ping",
			env: func(p string) map[string]string {
				return map[string]string{"HTTP_PROXY": p}
			},
			wantProxied: "GET http://" + proxyTestHost + "/ping",
			wantOut:     "STATUS 200 BODY via-proxy",
		},
		{
			name:    "HTTP_PROXY routes a BuildTransportHTTP1 request",
			builder: "h1",
			target:  "http://" + proxyTestHost + "/ping",
			env: func(p string) map[string]string {
				return map[string]string{"HTTP_PROXY": p}
			},
			wantProxied: "GET http://" + proxyTestHost + "/ping",
			wantOut:     "STATUS 200 BODY via-proxy",
		},
		{
			name:    "HTTPS_PROXY tunnels an https request with CONNECT",
			builder: "h2",
			target:  "https://" + proxyTestHost + "/ping",
			env: func(p string) map[string]string {
				return map[string]string{"HTTPS_PROXY": p}
			},
			wantProxied: "CONNECT " + proxyTestHost + ":443",
		},
		{
			name:    "NO_PROXY naming the host keeps the request direct",
			builder: "h2",
			target:  "http://" + proxyTestHost + "/ping",
			env: func(p string) map[string]string {
				return map[string]string{"HTTP_PROXY": p, "NO_PROXY": proxyTestHost}
			},
			wantDirect: proxyTestHost + ":80",
		},
		{
			name:    "HTTP_PROXY alone does not proxy an https request",
			builder: "h1",
			target:  "https://" + proxyTestHost + "/ping",
			env: func(p string) map[string]string {
				return map[string]string{"HTTP_PROXY": p}
			},
			wantDirect: proxyTestHost + ":443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := newRecordingProxy(t)
			allow := proxy.srv.Listener.Addr().String()

			out := runProxyChild(t, tt.builder, tt.target, allow, tt.env(proxy.srv.URL))
			seen := proxy.requests()

			if tt.wantProxied != "" {
				if len(seen) != 1 || seen[0] != tt.wantProxied {
					t.Errorf("proxy recorded %q, want exactly [%q]\nchild output:\n%s", seen, tt.wantProxied, out)
				}
				if strings.Contains(out, "DIRECT ") {
					t.Errorf("child dialled the upstream directly although the proxy environment names a proxy\nchild output:\n%s", out)
				}
			} else if len(seen) != 0 {
				t.Errorf("proxy recorded %q, want no request\nchild output:\n%s", seen, out)
			}

			if tt.wantDirect != "" && !strings.Contains(out, "DIRECT "+tt.wantDirect) {
				t.Errorf("child did not dial %s directly\nchild output:\n%s", tt.wantDirect, out)
			}
			if tt.wantOut != "" && !strings.Contains(out, tt.wantOut) {
				t.Errorf("child output lacks %q\nchild output:\n%s", tt.wantOut, out)
			}
		})
	}
}

// TestBuildTransportBoundsHeaderWait pins R6.2's bound: both builders end an
// attempt whose response headers take longer than 30 s, whether or not HTTP/2
// is disabled through BENTOO_DISABLE_HTTP2.
func TestBuildTransportBoundsHeaderWait(t *testing.T) {
	const want = 30 * time.Second

	for _, disable := range []string{"", "1"} {
		t.Run("BENTOO_DISABLE_HTTP2="+disable, func(t *testing.T) {
			t.Setenv(EnvDisableHTTP2, disable)

			if got := BuildTransport().ResponseHeaderTimeout; got != want {
				t.Errorf("BuildTransport().ResponseHeaderTimeout = %v, want %v", got, want)
			}
			if got := BuildTransportHTTP1().ResponseHeaderTimeout; got != want {
				t.Errorf("BuildTransportHTTP1().ResponseHeaderTimeout = %v, want %v", got, want)
			}
		})
	}
}
