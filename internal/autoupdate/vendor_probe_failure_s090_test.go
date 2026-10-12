package autoupdate

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
)

// s090HangingHead never answers a HEAD of uri: it waits until the request's
// context ends, which is how a probe meets the client timeout. Every other
// request goes to next.
type s090HangingHead struct {
	next http.RoundTripper
	uri  string
}

func (h s090HangingHead) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodHead && req.URL.String() == h.uri {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return h.next.RoundTrip(req)
}

// s090ProbeCase is one way the vendor URI's HEAD probe can answer.
type s090ProbeCase struct {
	name    string
	inherit string
	head    s090Resp
	hang    bool // the probe never answers; the client timeout ends it
}

// probeClient is the client the case's probe goes through: the fixture's fake
// transport, behind a 50 ms per-attempt timeout when the case hangs.
func (tc s090ProbeCase) probeClient(f *s090Fix) *fetch.RetryableHTTPClient {
	if !tc.hang {
		return s090Client(f.tr)
	}
	c := fetch.NewRetryableHTTPClient().WithCircuitBreaker(false)
	c.SetHTTPClient(&http.Client{Transport: s090HangingHead{next: f.tr, uri: f.vendorURI}})
	c.SetRequestTimeout(50 * time.Millisecond)
	c.SetDelayFunc(func(time.Duration) {})
	return c
}

func (tc s090ProbeCase) fixture(t *testing.T) *s090Fix {
	t.Helper()
	inherit := tc.inherit
	if inherit == "" {
		inherit = "go-module"
	}
	f := s090Fixture(t, "1.9.0", inherit, s090VendorOK)
	if !tc.hang {
		f.tr.set(http.MethodHead, s090VendorOK, tc.head)
	}
	return f
}

// prepare runs the vendor step alone, through the case's client.
func (tc s090ProbeCase) prepare(t *testing.T, f *s090Fix) (VendorOutcome, error) {
	t.Helper()
	sw := newSweeper(f.overlay,
		withSweeperExec(f.exec.fn),
		withSweeperHTTPClient(tc.probeClient(f)),
		withSweeperLogger(f.sink.logger()),
		withSweeperReporter(f.rec),
	)
	return sw.prepareVendorDistfile(t.Context(), f.stagedRoot, s090Pkg, f.version, f.distdir)
}

// manifestWithFailingPkgdev runs the manifest step with every pkgdev run
// failing and returns the fixer, the result and the error.
func (tc s090ProbeCase) manifestWithFailingPkgdev(t *testing.T, f *s090Fix) (*fakeFixer, *ApplyResult, error) {
	t.Helper()
	f.exec.pkgdevFail = true
	fx := &fakeFixer{summary: "rewrote SRC_URI"}
	a := f.applier(t, fx, WithApplierHTTPClient(tc.probeClient(f)))
	result := &ApplyResult{}
	distdir, err := a.runManifestWithFix(t.Context(), f.cand(), s090Pkg, "1.9.0", result)
	t.Cleanup(func() { removeStagedDistdir(nil, distdir) })
	if err == nil {
		t.Fatal("runManifestWithFix succeeded although every pkgdev run failed")
	}
	return fx, result, err
}

// s090ProbeLogged reports whether a record at level carries every part.
func s090ProbeLogged(s *s090Sink, level slog.Level, parts ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r.level != level {
			continue
		}
		all := true
		for _, p := range parts {
			all = all && strings.Contains(r.text, p)
		}
		if all {
			return true
		}
	}
	return false
}

// R1.7, R1.8: a probe that cannot answer — a timeout, a transport error or a
// 5xx — is the vendor step's failure. It is logged at WARN with the package,
// the version and the URI, it is reported as ProbeFailed (never as a
// generated tarball), and the manifest failure that follows never reaches the
// LLM fixer. The unlike-shaped failures come first: each must land on the same
// verdict as the plain 503.
func TestVendorProbeFailureIsWarnedAndOwned(t *testing.T) {
	cases := []s090ProbeCase{
		{name: "the probe times out", hang: true},
		{name: "transport error", head: s090Resp{err: errors.New("dial tcp: connection refused")}},
		{name: "500", head: s090Resp{status: http.StatusInternalServerError}},
		{name: "503", head: s090Resp{status: http.StatusServiceUnavailable}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("vendor step", func(t *testing.T) {
				f := tc.fixture(t)
				out, err := tc.prepare(t, f)
				if err != nil {
					t.Fatalf("prepareVendorDistfile error = %v, want none: the manifest step decides the package", err)
				}
				if !out.ProbeFailed {
					t.Errorf("outcome %+v: ProbeFailed is false after a failed probe", out)
				}
				if out.Generated || out.Tarball != "" || out.SHA512 != "" {
					t.Errorf("outcome %+v claims a tarball although none was generated", out)
				}
				if out.File != "govulncheck-1.9.0-vendor.tar.xz" || out.URI != s090VendorOK {
					t.Errorf("outcome File/URI = %q / %q, want %q / %q",
						out.File, out.URI, "govulncheck-1.9.0-vendor.tar.xz", s090VendorOK)
				}
				if !f.sink.warned(s090Pkg, "1.9.0", s090VendorOK) {
					t.Errorf("no WARN naming %s, 1.9.0 and %s; records: %+v", s090Pkg, s090VendorOK, f.sink.recs)
				}
				if n := len(f.exec.named("go")); n != 0 {
					t.Errorf("go ran %d time(s) without a definite 404", n)
				}
			})
			t.Run("manifest step", func(t *testing.T) {
				f := tc.fixture(t)
				fx, result, err := tc.manifestWithFailingPkgdev(t, f)
				if fx.called != 0 {
					t.Errorf("the LLM fixer ran %d time(s) after a failed vendor probe, want 0", fx.called)
				}
				if !errors.Is(err, ErrManifestFailed) {
					t.Errorf("error %v does not carry the manifest failure (ErrManifestFailed)", err)
				}
				if !result.Vendor.ProbeFailed || result.Vendor.Generated {
					t.Errorf("result.Vendor = %+v, want ProbeFailed and not Generated", result.Vendor)
				}
				if !f.sink.warned(s090Pkg, "1.9.0", s090VendorOK) {
					t.Errorf("no WARN naming %s, 1.9.0 and %s; records: %+v", s090Pkg, s090VendorOK, f.sink.recs)
				}
			})
		})
	}
}

// R1.7, R1.8, converse: an answer that is not a failure to answer — a 403, a
// 410 — leaves the candidate to the ordinary path: INFO "not owned", no WARN,
// no ProbeFailed, and the fixer still runs. A non-candidate is never probed,
// so even a vendor URI that would answer 503 keeps the fixer.
func TestVendorProbeForbiddenIsNotOwned(t *testing.T) {
	cases := []s090ProbeCase{
		{name: "403", head: s090Resp{status: http.StatusForbidden}},
		{name: "410", head: s090Resp{status: http.StatusGone}},
		{name: "not a Go vendor candidate, URI would answer 503", inherit: "cargo", head: s090Resp{status: http.StatusServiceUnavailable}},
	}
	for _, tc := range cases {
		candidate := tc.inherit == ""
		t.Run(tc.name, func(t *testing.T) {
			t.Run("vendor step", func(t *testing.T) {
				f := tc.fixture(t)
				out, err := tc.prepare(t, f)
				if err != nil || out.ProbeFailed || out.Generated {
					t.Fatalf("prepareVendorDistfile = (%+v, %v), want a zero outcome and no error", out, err)
				}
				if s090ProbeLogged(f.sink, slog.LevelWarn, s090VendorOK) {
					t.Errorf("a WARN names %s although its probe did not fail; records: %+v", s090VendorOK, f.sink.recs)
				}
				if candidate && !s090ProbeLogged(f.sink, slog.LevelInfo, "not owned", s090Pkg, "1.9.0", s090VendorOK) {
					t.Errorf("no INFO \"not owned\" naming %s, 1.9.0 and %s; records: %+v", s090Pkg, s090VendorOK, f.sink.recs)
				}
			})
			t.Run("manifest step", func(t *testing.T) {
				f := tc.fixture(t)
				fx, result, _ := tc.manifestWithFailingPkgdev(t, f)
				if fx.called != 1 {
					t.Errorf("the LLM fixer ran %d time(s), want 1: the failure is not the vendor step's", fx.called)
				}
				if result.Vendor.ProbeFailed || result.Vendor.Generated {
					t.Errorf("result.Vendor = %+v, want a zero outcome", result.Vendor)
				}
			})
		})
	}
}
