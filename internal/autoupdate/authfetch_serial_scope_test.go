package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Story 068, sub-task 1.1 — R1.1, R1.3, R1.4, R1.5, R1.6: a record's
// fetch_serial_env may name only a BENTOO_FETCH_ variable.
//
// packages.toml lives in the public overlay, and the record's author also picks
// fetch_url. Before this story any variable name was resolved through the whole
// secrets chain and posted to that url, so one PR could name GITHUB_TOKEN and
// receive it. The refusal is decided from the NAME at parse time, before any
// lookup, so it reads the same on every machine and never hints whether the
// secret exists.

// s068Sentinel is the value every hostile fixture sets. Finding it anywhere —
// in an error, in a request — is the leak this story closes.
const s068Sentinel = "S068-SENTINEL-must-never-leave-9c41f0"

// s068Server records every form the endpoint receives. It answers like a
// vendor that accepted the form, so a request that should never have been sent
// completes and is visible here instead of failing somewhere else.
type s068Server struct {
	URL   string
	mu    sync.Mutex
	forms []url.Values
}

func newS068Server(t *testing.T) *s068Server {
	t.Helper()
	s := &s068Server{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		s.mu.Lock()
		s.forms = append(s.forms, r.PostForm)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("BINARY-DISTFILE"))
	}))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func (s *s068Server) received() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.forms...)
}

// s068ChainCarries reports the text of the first error in err's Unwrap chain
// that contains value.
func s068ChainCarries(err error, value string) (string, bool) {
	for _, e := range authFetchErrorChain(err) {
		if strings.Contains(e.Error(), value) {
			return e.Error(), true
		}
	}
	return "", false
}
