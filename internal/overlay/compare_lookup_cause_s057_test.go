package overlay

// Authored for story 057, sub-task 1.3 — R1.1-R1.7, R4.7 (R6.1/R6.3 are pinned by
// TestCompareStatus and TestDeriveVerdict).
//
// Contract names used here: CompareResult.LookupCause, CompareResult.FailureText,
// CompareOptions.Redact ([]string), and the lookup classifier
// classifyLookupError(err). Cause values are compared through fmt.Sprint, so a
// string type or a Stringer both satisfy the test.
//
// RED ON ARRIVAL: none of those names exist.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// s057LookupWords is the closed lookup vocabulary (story Assumptions).
var s057LookupWords = []string{"rate-limited", "auth", "network", "not found upstream", "other"}

func s057Word(v any) string { return fmt.Sprint(v) }

// s057LookupProvider answers from two tables; an atom in neither is ErrNotFound.
type s057LookupProvider struct {
	errs     map[string]error
	versions map[string][]string
}

func (p *s057LookupProvider) GetPackageVersions(ctx context.Context, category, pkg string) ([]string, error) {
	if err, ok := p.errs[category+"/"+pkg]; ok {
		return nil, err
	}
	if v, ok := p.versions[category+"/"+pkg]; ok {
		return v, nil
	}
	return nil, provider.ErrNotFound
}
func (p *s057LookupProvider) GetName() string   { return "s057" }
func (p *s057LookupProvider) SupportsAPI() bool { return true }
func (p *s057LookupProvider) Close() error      { return nil }

// s057CompareAll compares every atom (category/package at 1.0) and returns the
// results keyed by atom, with every status kept.
func s057CompareAll(t *testing.T, prov provider.Provider, opts CompareOptions, atoms ...string) (*CompareReport, map[string]CompareResult) {
	t.Helper()
	opts.IncludeSynced, opts.IncludeNotInRemote = true, true
	pkgs := make([]PackageInfo, 0, len(atoms))
	for _, a := range atoms {
		cat, pkg, _ := strings.Cut(a, "/")
		pkgs = append(pkgs, PackageInfo{Category: cat, Package: pkg, LatestVersion: "1.0"})
	}
	report, err := CompareWithProvider(pkgs, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v", err)
	}
	byAtom := map[string]CompareResult{}
	for _, r := range report.Results {
		byAtom[r.Category+"/"+r.Package] = r
	}
	for _, a := range atoms {
		if _, ok := byAtom[a]; !ok {
			t.Fatalf("no result for %s", a)
		}
	}
	return report, byAtom
}

func s057RefusedConn() error {
	return &url.Error{Op: "Get", URL: "https://api.github.com/repos/x", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}}
}

// TestClassifyLookupError is R1.1-R1.5 at the classifier, hostile fixtures first.
func TestClassifyLookupError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		// Would wrongly COLLAPSE: text that looks like a sentinel but wraps none.
		{"the rate-limit sentence without the sentinel", errors.New(provider.ErrRateLimit.Error() + ": rate limit resets at 1790000000"), "other"},
		{"a 401 sentence without the sentinel", errors.New("API error: status 401: 401 Unauthorized"), "other"},
		{"the not-found sentence without the sentinel", errors.New(provider.ErrNotFound.Error()), "other"},
		{"a plain API error is not auth", fmt.Errorf("%w: status 500: boom", provider.ErrAPIError), "other"},
		{"a context cancellation is not a network failure by name", errors.New("dial tcp: network is unreachable"), "other"},
		// Would wrongly SPLIT: the sentinel reached through wrapping and joining.
		{"a 401 matches both unauthorized and API error", fmt.Errorf("%w: %w: status 401", provider.ErrUnauthorized, provider.ErrAPIError), "auth"},
		{"rate limit wrapped twice", fmt.Errorf("fetch app-misc/hello: %w", fmt.Errorf("%w: rate limit resets at 1790000000", provider.ErrRateLimit)), "rate-limited"},
		{"rate limit joined with context", errors.Join(errors.New("app-misc/hello"), provider.ErrRateLimit), "rate-limited"},
		{"a refused connection behind url.Error", fmt.Errorf("fetch app-misc/hello: %w", s057RefusedConn()), "network"},
		{"ErrNotFound wrapped", fmt.Errorf("github: %w", provider.ErrNotFound), "not found upstream"},
		// Benign.
		{"bare rate limit", provider.ErrRateLimit, "rate-limited"},
		{"bare unauthorized", provider.ErrUnauthorized, "auth"},
		{"bare not found", provider.ErrNotFound, "not found upstream"},
		{"anything else", errors.New("boom"), "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s057Word(classifyLookupError(tc.err)); got != tc.want {
				t.Errorf("classifyLookupError(%q) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}

	// Third element: the five canonical inputs must give five DIFFERENT words,
	// and exactly the closed vocabulary.
	seen := map[string]bool{}
	for _, err := range []error{provider.ErrRateLimit, provider.ErrUnauthorized, s057RefusedConn(), provider.ErrNotFound, errors.New("x")} {
		w := s057Word(classifyLookupError(err))
		if seen[w] {
			t.Errorf("two different lookup failures classify as %q", w)
		}
		seen[w] = true
	}
	for _, w := range s057LookupWords {
		if !seen[w] {
			t.Errorf("the vocabulary word %q is produced by none of the canonical failures", w)
		}
	}
}

// TestLookupFailureRecordsCauseAndText is R1.1-R1.3, R1.5, R1.6 end to end, over
// the REAL GitHubProvider against a local HTTP server (production uses
// provider.NewProvider, not the github adapter).
func TestLookupFailureRecordsCauseAndText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cat/limited"):
			w.Header().Set("X-RateLimit-Reset", "1790000000")
			w.WriteHeader(http.StatusForbidden)
		case strings.HasSuffix(r.URL.Path, "/cat/denied"):
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"Bad credentials"}`)) //nolint:errcheck
		case strings.HasSuffix(r.URL.Path, "/cat/broken"):
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("upstream exploded")) //nolint:errcheck
		case strings.HasSuffix(r.URL.Path, "/cat/fine"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"name":"fine-1.0.ebuild","type":"file"}]`)) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	newProv := func(base string) *provider.GitHubProvider {
		p, err := provider.NewGitHubProvider(&provider.RepositoryInfo{Name: "s057", URL: "test/repo"})
		if err != nil {
			t.Fatalf("NewGitHubProvider: %v", err)
		}
		p.BaseURL = base
		p.CacheDir = ""
		return p
	}
	prov := newProv(srv.URL)
	direct := func(p *provider.GitHubProvider, atom string) string {
		cat, pkg, _ := strings.Cut(atom, "/")
		_, err := p.GetPackageVersions(context.Background(), cat, pkg)
		if err == nil {
			t.Fatalf("the fixture is wrong: %s did not fail", atom)
		}
		return err.Error()
	}

	_, byAtom := s057CompareAll(t, prov, CompareOptions{}, "cat/limited", "cat/denied", "cat/broken", "cat/fine")
	for atom, want := range map[string]string{"cat/limited": "rate-limited", "cat/denied": "auth", "cat/broken": "other"} {
		r := byAtom[atom]
		if r.Status != StatusError {
			t.Errorf("%s: Status %v, want StatusError (R1.1, R6.1)", atom, r.Status)
		}
		if got := s057Word(r.LookupCause); got != want {
			t.Errorf("%s: LookupCause %q, want %q", atom, got, want)
		}
		if wantText := direct(prov, atom); r.FailureText != wantText {
			t.Errorf("%s: FailureText %q, want the error's full text %q (R1.6)", atom, r.FailureText, wantText)
		}
	}
	fine := byAtom["cat/fine"]
	if fine.FailureText != "" {
		t.Errorf("a lookup that succeeded carries FailureText %q; a failure must not leak onto another row", fine.FailureText)
	}
	for _, w := range s057LookupWords {
		if s057Word(fine.LookupCause) == w {
			t.Errorf("a lookup that succeeded carries the cause %q", w)
		}
	}

	// A refused connection: the network cause.
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close()
	netProv := newProv(goneURL)
	_, netRes := s057CompareAll(t, netProv, CompareOptions{Ctx: context.Background()}, "cat/refused")
	r := netRes["cat/refused"]
	if r.Status != StatusError || s057Word(r.LookupCause) != "network" {
		t.Errorf("a refused connection gave Status %v cause %q, want StatusError and \"network\" (R1.3)", r.Status, s057Word(r.LookupCause))
	}
	if wantText := direct(netProv, "cat/refused"); r.FailureText != wantText {
		t.Errorf("FailureText %q, want %q (R1.6)", r.FailureText, wantText)
	}
}

// TestLookupFailureTextIsRedacted is R4.7 at the result.
func TestLookupFailureTextIsRedacted(t *testing.T) {
	const tok, tok2 = "ghp_TESTTOKEN0000", "glpat-SECOND0000"
	leak := fmt.Errorf("%w: GET https://api.github.com/x?access_token=%s failed; retry with %s or %s", provider.ErrRateLimit, tok, tok, tok2)
	clean := errors.New("boom: connection reset by peer")
	prov := &s057LookupProvider{errs: map[string]error{"cat/leak": leak, "cat/clean": clean}}
	// "limit" is in the list on purpose: the redaction acts on TEXT, never on the
	// cause, so the cause word must survive a list that names part of it.
	opts := CompareOptions{Redact: []string{tok, "", tok2, "limit"}}

	report, byAtom := s057CompareAll(t, prov, opts, "cat/leak", "cat/clean")

	want := leak.Error()
	for _, s := range opts.Redact {
		if s != "" {
			want = strings.ReplaceAll(want, s, "***")
		}
	}
	got := byAtom["cat/leak"]
	if got.FailureText != want {
		t.Errorf("FailureText\n got: %q\nwant: %q (R4.7: every listed value scrubbed, every occurrence)", got.FailureText, want)
	}
	if strings.Contains(got.FailureText, tok) || strings.Contains(got.FailureText, tok2) {
		t.Errorf("a token survived into FailureText: %q", got.FailureText)
	}
	if s057Word(got.LookupCause) != "rate-limited" {
		t.Errorf("the redaction changed the cause: %q, want \"rate-limited\"", s057Word(got.LookupCause))
	}
	if c := byAtom["cat/clean"]; c.FailureText != clean.Error() {
		t.Errorf("a text holding no listed value was altered: %q, want %q", c.FailureText, clean.Error())
	}
	for _, f := range report.Findings {
		if strings.Contains(f.Detail, tok) || strings.Contains(f.Detail, tok2) {
			t.Errorf("finding %s leaks a token: %q", f.Atom, f.Detail)
		}
	}
}

// TestWrappedNotFoundIsNotInRemote is R1.7, with its converse first: a text that
// merely READS like ErrNotFound is not absence.
func TestWrappedNotFoundIsNotInRemote(t *testing.T) {
	prov := &s057LookupProvider{errs: map[string]error{
		"cat/lookalike": errors.New(provider.ErrNotFound.Error()),
		"cat/wrapped":   fmt.Errorf("fetch cat/wrapped: %w", provider.ErrNotFound),
		"cat/bare":      provider.ErrNotFound,
	}}
	_, byAtom := s057CompareAll(t, prov, CompareOptions{}, "cat/lookalike", "cat/wrapped", "cat/bare")

	if r := byAtom["cat/lookalike"]; r.Status != StatusError || s057Word(r.LookupCause) != "other" {
		t.Errorf("an error that only reads like ErrNotFound gave Status %v cause %q, want StatusError and \"other\"", r.Status, s057Word(r.LookupCause))
	}
	for _, atom := range []string{"cat/wrapped", "cat/bare"} {
		if r := byAtom[atom]; r.Status != StatusNotInRemote {
			t.Errorf("%s: Status %v, want StatusNotInRemote (R1.7: errors.Is, not ==)", atom, r.Status)
		}
	}
}
