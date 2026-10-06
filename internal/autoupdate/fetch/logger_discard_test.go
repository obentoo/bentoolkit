package fetch

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// s062IsolateAutoupdate points HOME and the XDG dirs at a temp root and clears
// the token variables, so no test reads the developer's config or secrets and
// nothing can be written into their state directory.
func s062IsolateAutoupdate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg-config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdg-state"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	return root
}

// s062CaptureStderr runs fn with fd 2 pointed at a pipe and returns everything
// written to it.
func s062CaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved, err := syscall.Dup(2)
	if err != nil {
		t.Fatalf("dup fd 2: %v", err)
	}
	if err := syscall.Dup2(int(w.Fd()), 2); err != nil {
		t.Fatalf("redirect fd 2: %v", err)
	}
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	func() {
		defer func() {
			if err := syscall.Dup2(saved, 2); err != nil {
				t.Errorf("restore fd 2: %v", err)
			}
			_ = syscall.Close(saved)
			_ = w.Close()
		}()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}

// TestHTTPClientWithoutALoggerWritesNothingToStderr: a denied header expansion
// on a client built with no logger goes nowhere.
func TestHTTPClientWithoutALoggerWritesNothingToStderr(t *testing.T) {
	s062IsolateAutoupdate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)

	client := NewRetryableHTTPClient()
	stderr := s062CaptureStderr(t, func() {
		resp, err := client.GetWithHeaders(srv.URL, map[string]string{"X-S062-Probe": "${S062_NOT_ALLOWED_VAR}"})
		if err == nil {
			_ = resp.Body.Close()
		}
	})
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("an HTTP client built without a logger wrote to stderr (R5.3: it must discard):\n%s", stderr)
	}
}
