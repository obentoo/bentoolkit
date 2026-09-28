package autoupdate

import (
	"context"
	"errors"
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

// s068ParseThenFetch does what every caller of the parser does — the sweep's
// prefetch and FetchAuthDistfile both fetch the spec they were handed. A parser
// that accepts a record it should refuse is therefore observed as the request
// it lets through, not only as a missing error.
func s068ParseThenFetch(t *testing.T, meta map[string]string) (parseErr, fetchErr error) {
	t.Helper()
	spec, ok, err := parseAuthFetchSpec(meta)
	if err != nil {
		if ok || spec != nil {
			t.Errorf("parseAuthFetchSpec returned (spec=%v, ok=%v) beside error %v; want (nil, false)", spec != nil, ok, err)
		}
		return err, nil
	}
	if !ok || spec == nil {
		t.Fatalf("parseAuthFetchSpec: (spec=%v, ok=%v, err=nil) for a record carrying fetch_url", spec != nil, ok)
	}
	_, fetchErr = spec.fetchDistfile(context.Background(), "1.0", t.TempDir())
	return nil, fetchErr
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

func TestParseAuthFetchSpec_RefusesUnprefixedSerialEnv(t *testing.T) {
	// Hostile first: every name here must be refused, whether or not it is set.
	cases := []struct {
		name string
		raw  string // as written in the record
		want string // the name after trimming, which the error must name
	}{
		{"a foreign secret", "GITHUB_TOKEN", "GITHUB_TOKEN"},
		{"bentoolkit's own repository token", "BENTOO_REPO_MY_OVERLAY_TOKEN", "BENTOO_REPO_MY_OVERLAY_TOKEN"},
		{"bentoolkit's own ntfy token", "BENTOO_NTFY_TOKEN", "BENTOO_NTFY_TOKEN"},
		{"lower-case prefix", "bentoo_fetch_x", "bentoo_fetch_x"},
		{"mixed-case prefix", "Bentoo_Fetch_X", "Bentoo_Fetch_X"},
		{"prefix without its underscore", "BENTOO_FETCHX", "BENTOO_FETCHX"},
		{"bare prefix, nothing after it", "BENTOO_FETCH_", "BENTOO_FETCH_"},
		{"bare prefix padded with whitespace", "  BENTOO_FETCH_  ", "BENTOO_FETCH_"},
		{"foreign secret padded with whitespace", " GITHUB_TOKEN\t", "GITHUB_TOKEN"},
		{"prefix in the middle, not at the start", "MY_BENTOO_FETCH_X", "MY_BENTOO_FETCH_X"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newS068Server(t)
			meta := map[string]string{
				metaFetchURL:         srv.URL + "/download",
				metaFetchSerialEnv:   tc.raw,
				metaFetchSerialField: "key",
				metaFetchFilename:    "leak-{version}.tar.gz",
			}

			// R1.4: once with the variable absent everywhere, once SET in the
			// environment AND the secrets file. The refusal must not notice.
			withSecretsFile(t, "")
			t.Setenv(tc.want, "")
			unsetParse, unsetFetch := s068ParseThenFetch(t, meta)

			withSecretsFile(t, tc.want+"="+s068Sentinel+"\n")
			t.Setenv(tc.want, s068Sentinel)
			setParse, setFetch := s068ParseThenFetch(t, meta)

			if got := srv.received(); len(got) != 0 {
				t.Errorf("the endpoint received %d request(s) for a record naming %q; want none. Forms: %v", len(got), tc.raw, got)
			}
			for _, e := range []error{unsetFetch, setFetch} {
				if leaked, ok := s068ChainCarries(e, s068Sentinel); ok {
					t.Errorf("a fetch error carries the resolved value: %q", leaked)
				}
			}

			for _, run := range []struct {
				label string
				err   error
			}{{"unset", unsetParse}, {"set", setParse}} {
				err := run.err
				if err == nil {
					t.Errorf("[%s] parseAuthFetchSpec accepted fetch_serial_env = %q; want a refusal naming BENTOO_FETCH_%s", run.label, tc.raw, tc.want)
					continue
				}
				if !errors.Is(err, ErrAuthFetchFailed) {
					t.Errorf("[%s] err = %v; want errors.Is(ErrAuthFetchFailed)", run.label, err)
				}
				if errors.Is(err, ErrAuthFetchSecretMissing) {
					t.Errorf("[%s] err = %v is ErrAuthFetchSecretMissing: the refusal must come from the name, before any lookup", run.label, err)
				}
				msg := err.Error()
				for _, needle := range []string{tc.want, metaFetchSerialEnv, "BENTOO_FETCH_" + tc.want} {
					if !strings.Contains(msg, needle) {
						t.Errorf("[%s] refusal %q does not name %q", run.label, msg, needle)
					}
				}
				if leaked, ok := s068ChainCarries(err, s068Sentinel); ok {
					t.Errorf("[%s] the refusal carries the variable's value: %q", run.label, leaked)
				}
			}

			if unsetParse != nil && setParse != nil && unsetParse.Error() != setParse.Error() {
				t.Errorf("the refusal depends on whether the variable is set:\n  unset: %q\n  set:   %q", unsetParse, setParse)
			}
		})
	}
}

// TestParseAuthFetchSpec_PrefixedSerialEnvResolves is the converse half: a
// fix that refuses too much — a stricter prefix, a reservation that swallows
// the migrated names, a check made before trimming — breaks R1.5. Green before
// the story by design; it pins "exactly as it does today".
func TestParseAuthFetchSpec_PrefixedSerialEnvResolves(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		lookup   string // the name the value is stored under
		inFile   bool   // store it only in the user secrets file
		wantSent string
	}{
		{"a migrated vendor serial", "BENTOO_FETCH_FILEZILLA_PRO_KEY", "BENTOO_FETCH_FILEZILLA_PRO_KEY", false, "serial-filezilla-068"},
		{"one character after the prefix", "BENTOO_FETCH_A", "BENTOO_FETCH_A", false, "serial-one-char-068"},
		{"an underscore after the prefix", "BENTOO_FETCH__", "BENTOO_FETCH__", false, "serial-underscore-068"},
		{"the migrated form of a reserved name", "BENTOO_FETCH_BENTOO_NTFY_TOKEN", "BENTOO_FETCH_BENTOO_NTFY_TOKEN", false, "serial-migrated-068"},
		{"padded with whitespace, trimmed", "  BENTOO_FETCH_X\t", "BENTOO_FETCH_X", false, "serial-padded-068"},
		{"present only in the secrets file", "BENTOO_FETCH_ONLY_IN_FILE", "BENTOO_FETCH_ONLY_IN_FILE", true, "serial-file-068"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newS068Server(t)
			if tc.inFile {
				withSecretsFile(t, tc.lookup+"="+tc.wantSent+"\n")
				t.Setenv(tc.lookup, "")
			} else {
				withSecretsFile(t, "")
				t.Setenv(tc.lookup, tc.wantSent)
			}

			spec, ok, err := parseAuthFetchSpec(map[string]string{
				metaFetchURL:         srv.URL + "/download",
				metaFetchSerialEnv:   tc.raw,
				metaFetchSerialField: "key",
				metaFetchFilename:    "ok-{version}.tar.gz",
			})
			if err != nil || !ok {
				t.Fatalf("parseAuthFetchSpec refused fetch_serial_env = %q: (ok=%v, err=%v)", tc.raw, ok, err)
			}
			if _, err := spec.fetchDistfile(context.Background(), "1.0", t.TempDir()); err != nil {
				t.Fatalf("fetchDistfile: %v", err)
			}
			got := srv.received()
			if len(got) != 1 {
				t.Fatalf("the endpoint received %d requests, want 1", len(got))
			}
			if v := got[0].Get("key"); v != tc.wantSent {
				t.Errorf("serial field = %q, want %q", v, tc.wantSent)
			}
		})
	}

	t.Run("an unset prefixed name still fails as a missing secret, naming it", func(t *testing.T) {
		srv := newS068Server(t)
		withSecretsFile(t, "")
		t.Setenv("BENTOO_FETCH_ABSENT_068", "")
		spec, ok, err := parseAuthFetchSpec(map[string]string{
			metaFetchURL:         srv.URL + "/download",
			metaFetchSerialEnv:   "BENTOO_FETCH_ABSENT_068",
			metaFetchSerialField: "key",
			metaFetchFilename:    "ok-{version}.tar.gz",
		})
		if err != nil || !ok {
			t.Fatalf("parseAuthFetchSpec: (ok=%v, err=%v)", ok, err)
		}
		_, err = spec.fetchDistfile(context.Background(), "1.0", t.TempDir())
		if !errors.Is(err, ErrAuthFetchSecretMissing) || !strings.Contains(err.Error(), "BENTOO_FETCH_ABSENT_068") {
			t.Errorf("err = %v; want ErrAuthFetchSecretMissing naming BENTOO_FETCH_ABSENT_068", err)
		}
		if n := len(srv.received()); n != 0 {
			t.Errorf("the endpoint received %d requests for an unresolvable serial, want 0", n)
		}
	})
}
