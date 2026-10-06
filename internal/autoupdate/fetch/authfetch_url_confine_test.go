package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// Story 064, sub-task 3.2 — R1.4 (with R1.1/R1.3): gosec G704 flags every
// request the authenticated fetch builds, and the URLs it builds are made of
// values an overlay contributor writes (fetch_url, fetch_id_url in
// packages.toml) and values an upstream answers with (the {id} captured from
// the catalogue page, the detected {version}). R1.4 forbids suppressing such a
// finding: the value must be validated or confined in code.
//
// The contract asserted here is behaviour, not a signature:
//
//   - A record whose fetch_url or fetch_id_url is not an absolute http(s) URL
//     is refused by parseAuthFetchSpec — and therefore by --lint, which runs
//     the same parser (story 068) — with ErrAuthFetchFailed naming the key.
//   - A value an upstream chose can never choose the HOST a request goes to.
//     Where the refusal happens (parse or fetch) is left open; that no request
//     reaches the other host is not.
//
// Hostile halves come first (hostile-half rule): the fixture that must be
// REFUSED and currently is not, then the fixture that must still be ACCEPTED
// and that a clumsy validator would refuse, and only then the plain case.

// s064BaseMeta is a minimal record with no serial and no form env, so no
// secret is ever resolved.
func s064BaseMeta(fetchURL string) map[string]string {
	return map[string]string{
		MetaFetchURL:      fetchURL,
		MetaFetchFilename: "s064-{version}.tar.xz",
	}
}

// s064WithIDLookup adds the id-lookup pair a {id} in fetch_url requires.
func s064WithIDLookup(meta map[string]string, idURL string) map[string]string {
	meta[metaFetchIDURL] = idURL
	meta[metaFetchIDPattern] = `id=([0-9A-Za-z.]+)`
	return meta
}

func TestS064AuthFetchURLTemplateRefusedAtParse(t *testing.T) {
	withSecretsFile(t, "")

	// Hostile, collapse direction: each is a URL no download may be built
	// from, and each parses today.
	refused := []struct {
		name string
		meta map[string]string
		key  string
	}{
		{"fetch_url with a file scheme", s064BaseMeta("file:///etc/passwd"), MetaFetchURL},
		{"fetch_url with an ftp scheme", s064BaseMeta("ftp://vendor.test/dl"), MetaFetchURL},
		{"fetch_url with a gopher scheme", s064BaseMeta("gopher://vendor.test/dl"), MetaFetchURL},
		{"fetch_url relative, no scheme", s064BaseMeta("vendor.test/dl"), MetaFetchURL},
		{"fetch_url protocol-relative", s064BaseMeta("//vendor.test/dl"), MetaFetchURL},
		{"fetch_url with {id} as the whole host", s064WithIDLookup(s064BaseMeta("http://{id}:8080/dl"), "https://vendor.test/catalog"), MetaFetchURL},
		{"fetch_url with {id} glued to the host", s064WithIDLookup(s064BaseMeta("https://dl{id}/file"), "https://vendor.test/catalog"), MetaFetchURL},
		{"fetch_id_url with a file scheme", s064WithIDLookup(s064BaseMeta("https://vendor.test/dl/{id}"), "file:///etc/passwd"), metaFetchIDURL},
		{"fetch_id_url relative, no scheme", s064WithIDLookup(s064BaseMeta("https://vendor.test/dl/{id}"), "vendor.test/catalog"), metaFetchIDURL},
		{"fetch_id_url with {version} as the whole host", s064WithIDLookup(s064BaseMeta("https://vendor.test/dl/{id}"), "http://{version}/catalog"), metaFetchIDURL},
	}
	for _, tc := range refused {
		t.Run("refused/"+tc.name, func(t *testing.T) {
			spec, ok, err := ParseAuthFetchSpec(tc.meta)
			if err == nil {
				t.Fatalf("parseAuthFetchSpec accepted %s=%q (ok=%v, spec!=nil %v); a %s that is not an absolute http(s) URL with a fixed host must be refused (R1.4)",
					tc.key, tc.meta[tc.key], ok, spec != nil, tc.key)
			}
			if !errors.Is(err, ErrAuthFetchFailed) {
				t.Errorf("err = %v; want it to match ErrAuthFetchFailed like every other refused record", err)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("err = %q; want it to name %s, the key the contributor must fix", err, tc.key)
			}
		})
	}

	// --lint runs the same parser (story 068), so it refuses the same record.
	t.Run("refused/lint path refuses what the parser refuses", func(t *testing.T) {
		if err := ValidateMetaFetch("app-misc/s064", s064BaseMeta("file:///etc/passwd")); err == nil {
			t.Fatalf("validateMetaFetch accepted fetch_url=file:///etc/passwd; --lint must refuse what the fetch refuses")
		}
	})

	// Hostile, split direction: valid templates a clumsy validator would
	// refuse. Each must keep parsing.
	accepted := []struct {
		name string
		meta map[string]string
	}{
		{"uppercase scheme and host", s064BaseMeta("HTTPS://Vendor.Test/dl")},
		{"plain http", s064BaseMeta("http://vendor.test/dl")},
		{"IPv6 literal with a port", s064BaseMeta("http://[::1]:8080/dl")},
		{"{id} in the path, explicit port, query", s064WithIDLookup(s064BaseMeta("https://vendor.test:8443/dl/{id}?x=1"), "https://vendor.test/catalog?v={version}")},
		{"{id} in the query, {version} in the id_url path", s064WithIDLookup(s064BaseMeta("https://vendor.test/dl?id={id}"), "https://vendor.test/{version}/list")},
		// Benign.
		{"plain https endpoint", s064BaseMeta("https://vendor.test/dl")},
	}
	for _, tc := range accepted {
		t.Run("accepted/"+tc.name, func(t *testing.T) {
			if _, ok, err := ParseAuthFetchSpec(tc.meta); err != nil || !ok {
				t.Fatalf("parseAuthFetchSpec(fetch_url=%q, fetch_id_url=%q) = ok %v, err %v; a valid absolute http(s) template must keep parsing",
					tc.meta[MetaFetchURL], tc.meta[metaFetchIDURL], ok, err)
			}
		})
	}
}

// s064Hosts starts the vendor host (catalogue + file) and a second host that
// no request may reach, and returns them with the second host's port.
type s064Hosts struct {
	vendor, other *httptest.Server
	otherHits     atomic.Int64
	otherPort     string
}

func s064NewHosts(t *testing.T, catalogueID string) *s064Hosts {
	t.Helper()
	h := &s064Hosts{}
	serveFile := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("s064 distfile body"))
	}
	h.vendor = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/catalog"):
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("release id=" + catalogueID + "\n"))
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			serveFile(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.vendor.Close)
	h.other = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.otherHits.Add(1)
		if strings.HasPrefix(r.URL.Path, "/catalog") {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("release id=4711\n"))
			return
		}
		serveFile(w)
	}))
	t.Cleanup(h.other.Close)
	u, err := url.Parse(h.other.URL)
	if err != nil {
		t.Fatalf("parsing the other host's URL: %v", err)
	}
	h.otherPort = u.Port()
	return h
}

// s064FetchMustNotReachOther runs the whole download for meta and version and
// asserts that the other host saw no request, whether the parser or the fetch
// was the one to refuse.
func s064FetchMustNotReachOther(t *testing.T, h *s064Hosts, meta map[string]string, version string) {
	t.Helper()
	dir := t.TempDir()
	spec, ok, err := ParseAuthFetchSpec(meta)
	if err == nil && ok {
		_, err = spec.FetchDistfile(context.Background(), version, dir)
	}
	if hits := h.otherHits.Load(); hits != 0 {
		t.Fatalf("%d request(s) reached %s, a host chosen by an upstream value (fetch_url=%q, fetch_id_url=%q, version=%q); R1.4 requires that value to be confined",
			hits, h.other.URL, meta[MetaFetchURL], meta[metaFetchIDURL], version)
	}
	if err == nil {
		t.Fatalf("the download succeeded without reaching the other host, but a template whose host is an upstream value must be refused, not resolved elsewhere")
	}
	if !errors.Is(err, ErrAuthFetchFailed) {
		t.Errorf("err = %v; want ErrAuthFetchFailed", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the distdir holds %d entr(ies) after a refused download; want none", len(entries))
	}
}

func TestS064AuthFetchUpstreamValueCannotChooseHost(t *testing.T) {
	withSecretsFile(t, "")

	// Hostile: the catalogue answers "127.0.0.1" — a plain-looking id that
	// passes any character-set check — and fetch_url puts {id} where the host
	// goes. Today the POST goes to whatever host the catalogue named.
	t.Run("captured id as the fetch_url host", func(t *testing.T) {
		h := s064NewHosts(t, "127.0.0.1")
		meta := s064WithIDLookup(s064BaseMeta("http://{id}:"+h.otherPort+"/dl/file"), h.vendor.URL+"/catalog")
		s064FetchMustNotReachOther(t, h, meta, "1.0")
	})

	// Hostile: the detected version "127.0.0.1" is a valid Gentoo version, and
	// fetch_id_url puts {version} where the host goes. Today the catalogue GET
	// goes to whatever host the upstream version names.
	t.Run("upstream version as the fetch_id_url host", func(t *testing.T) {
		h := s064NewHosts(t, "4711")
		meta := s064WithIDLookup(s064BaseMeta(h.vendor.URL+"/dl/{id}"), "http://{version}:"+h.otherPort+"/catalog")
		s064FetchMustNotReachOther(t, h, meta, "127.0.0.1")
	})

	// Converse: an upstream value in the PATH and QUERY, where it cannot move
	// the host, is how every id-lookup record works and must keep working —
	// including an id carrying dots, the character the hostile case used.
	t.Run("captured id and version in path and query still download", func(t *testing.T) {
		h := s064NewHosts(t, "20.1.3")
		meta := s064WithIDLookup(s064BaseMeta(h.vendor.URL+"/dl/{id}"), h.vendor.URL+"/catalog?v={version}")
		spec, ok, err := ParseAuthFetchSpec(meta)
		if err != nil || !ok {
			t.Fatalf("parseAuthFetchSpec = ok %v, err %v; a template with {id} in the path must parse", ok, err)
		}
		dir := t.TempDir()
		got, err := spec.FetchDistfile(context.Background(), "20.1", dir)
		if err != nil {
			t.Fatalf("fetchDistfile: %v; an id in the path must still download", err)
		}
		if body, rerr := os.ReadFile(got); rerr != nil || string(body) != "s064 distfile body" {
			t.Errorf("downloaded file %q = %q, %v; want the vendor's body", got, body, rerr)
		}
		if hits := h.otherHits.Load(); hits != 0 {
			t.Errorf("%d request(s) reached the other host on a benign download", hits)
		}
	})
}
