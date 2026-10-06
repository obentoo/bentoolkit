package fetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// s062Recorder is a debug-level JSON logger whose records the test can read.
type s062Recorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *s062Recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *s062Recorder) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(r, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (r *s062Recorder) records(t *testing.T) []map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(r.buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not a JSON record: %v\n%s", err, line)
		}
		out = append(out, m)
	}
	return out
}

// s062AttrText renders every attribute (not time, level or msg) as text.
func s062AttrText(rec map[string]any) string {
	var b strings.Builder
	for k, v := range rec {
		if k == "time" || k == "level" || k == "msg" {
			continue
		}
		fmt.Fprintf(&b, "%s=%v ", k, v)
	}
	return b.String()
}

// s062WarnsNaming returns the WARN records whose attributes mention sub, and
// fails the test for any record that carries sub in its message instead (R5.5:
// variable data moves into attributes).
func s062WarnsNaming(t *testing.T, recs []map[string]any, sub string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range recs {
		msg := fmt.Sprint(r["msg"])
		if strings.Contains(msg, sub) {
			t.Errorf("the message %q carries the variable data %q; it belongs in an attribute (R5.5)", msg, sub)
		}
		if r["level"] == "WARN" && strings.Contains(s062AttrText(r), sub) {
			out = append(out, r)
		}
	}
	return out
}

// TestHTTPClientLogsToTheInjectedLogger: a denied header expansion is reported
// to the logger given with SetLogger, naming the variable in an attribute, and
// nothing reaches stderr. A header name carrying CR/LF is reported the same way.
func TestHTTPClientLogsToTheInjectedLogger(t *testing.T) {
	s062IsolateAutoupdate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		name    string
		headers map[string]string
		naming  string
	}{
		{"denied expansion", map[string]string{"X-S062-Probe": "${S062_NOT_ALLOWED_VAR}"}, "S062_NOT_ALLOWED_VAR"},
		{"CR/LF in the header name", map[string]string{"X-S062-Bad\r\nInjected": "v"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &s062Recorder{}
			client := NewRetryableHTTPClient()
			client.SetLogger(rec.logger())

			stderr := s062CaptureStderr(t, func() {
				resp, err := client.GetWithHeaders(srv.URL, tc.headers)
				if err == nil {
					_ = resp.Body.Close()
				}
			})

			recs := rec.records(t)
			var warns []map[string]any
			if tc.naming != "" {
				warns = s062WarnsNaming(t, recs, tc.naming)
			} else {
				for _, r := range recs {
					if r["level"] == "WARN" {
						warns = append(warns, r)
					}
				}
			}
			if len(warns) == 0 {
				t.Errorf("the injected logger got no WARN record for the %s\nrecords: %v", tc.name, recs)
			}
			if strings.TrimSpace(stderr) != "" {
				t.Errorf("with a logger injected, the HTTP client still wrote to stderr:\n%s", stderr)
			}
		})
	}
}
