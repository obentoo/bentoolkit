package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures below imitate the endpoint this was measured against on
// 2026-09-19: a catalogue publishing a per-release download id, a form endpoint
// that answers with a short text/plain URL, and a CDN serving the archive.

const twoStepPayload = "PK\x03\x04-pretend-this-is-four-gigabytes"

// twoStepServer wires the three legs onto one test server. The returned map
// records what each leg saw, so a test can assert on the request the vendor
// would actually have received.
type twoStepServer struct {
	url         string
	postPath    string
	postBody    string
	postCT      string
	postMethod  string
	cdnHits     int
	lookupHits  int
	cdnCT       string
	cdnPayload  string
	urlReplyCT  string
	failCDNWith int
}

func newTwoStepServer(t *testing.T) *twoStepServer {
	t.Helper()
	s := &twoStepServer{cdnCT: "application/zip", cdnPayload: twoStepPayload, urlReplyCT: "text/plain; charset=utf-8"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// The catalogue: one decoy whose title would match an UNQUOTED
		// "21.1" ("2101"), placed first so a missing regexp.QuoteMeta is
		// caught by the id that comes back rather than by inspection.
		case strings.HasPrefix(r.URL.Path, "/catalogue"):
			s.lookupHits++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"downloads":[`+
				`{"urls":{"Linux":[{"downloadId":"DECOY-ID","downloadTitle":"Example 2101"}]}},`+
				`{"urls":{"Linux":[{"downloadId":"REAL-ID","downloadTitle":"Example 21.1"}]}}]}`)

		case strings.HasPrefix(r.URL.Path, "/cdn"):
			s.cdnHits++
			if s.failCDNWith != 0 {
				w.WriteHeader(s.failCDNWith)
				return
			}
			w.Header().Set("Content-Type", s.cdnCT)
			_, _ = w.Write([]byte(s.cdnPayload))

		default:
			s.postPath = r.URL.Path
			s.postMethod = r.Method
			s.postCT = r.Header.Get("Content-Type")
			body, _ := readAllBody(r)
			s.postBody = body
			w.Header().Set("Content-Type", s.urlReplyCT)
			fmt.Fprintf(w, "%s/cdn/Example_21.1_Linux.zip?Signature=abc&Expires=1", s.url)
		}
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func readAllBody(r *http.Request) (string, error) {
	defer func() { _ = r.Body.Close() }()
	b, err := io.ReadAll(r.Body)
	return string(b), err
}

// twoStepMeta is the record shape the DaVinci Resolve entry would carry.
func twoStepMeta(srv *twoStepServer) map[string]string {
	return map[string]string{
		metaFetchURL:      srv.url + "/api/register/us/download/{id}",
		metaFetchMethod:   "post",
		metaFetchBody:     fetchBodyJSON,
		metaFetchResponse: fetchResponseURL,
		metaFetchFilename: "Example_{version}_Linux.zip",
		metaFetchForm:     "product=Example&platform=Linux&policy=true&origin=vendor.test",
		metaFetchIDURL:    srv.url + "/catalogue",
		// Anchored on the platform AND the title, because a catalogue carries
		// one entry per platform per release.
		metaFetchIDPattern:   `"Linux":\[\{"downloadId":"([^"]+)","downloadTitle":"Example {version}"`,
		metaFetchContentType: "application/zip",
	}
}

// TestTwoStepFetchWalksBothLegs is the whole shape end to end: id lookup, JSON
// POST, URL reply, CDN download, file on disk.
func TestTwoStepFetchWalksBothLegs(t *testing.T) {
	srv := newTwoStepServer(t)
	spec, ok, err := parseAuthFetchSpec(twoStepMeta(srv))
	if err != nil || !ok {
		t.Fatalf("parseAuthFetchSpec: ok=%v err=%v", ok, err)
	}

	dir := t.TempDir()
	got, err := spec.fetchDistfile(context.Background(), "21.1", dir)
	if err != nil {
		t.Fatalf("fetchDistfile: %v", err)
	}

	// 1. The id came from the catalogue, and it is the real one: a missing
	//    regexp.QuoteMeta would have matched the "2101" decoy that sits first.
	if want := "/api/register/us/download/REAL-ID"; srv.postPath != want {
		t.Errorf("the form was posted to %q, want %q (the id resolved from the catalogue)", srv.postPath, want)
	}
	if srv.lookupHits != 1 {
		t.Errorf("the catalogue was read %d times, want 1", srv.lookupHits)
	}

	// 2. The body is JSON, with the boolean typed as a boolean.
	if !strings.HasPrefix(srv.postCT, "application/json") {
		t.Errorf("content-type = %q, want application/json", srv.postCT)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(srv.postBody), &sent); err != nil {
		t.Fatalf("the body is not JSON (%v): %s", err, srv.postBody)
	}
	if sent["policy"] != true {
		t.Errorf(`policy = %#v, want the boolean true (a JSON "true" is a different value to the API)`, sent["policy"])
	}
	if sent["product"] != "Example" || sent["platform"] != "Linux" || sent["origin"] != "vendor.test" {
		t.Errorf("the static fields did not arrive intact: %#v", sent)
	}

	// 3. What landed on disk is the CDN's payload, under the record's name —
	//    not the URL the first leg answered with.
	if filepath.Base(got) != "Example_21.1_Linux.zip" {
		t.Errorf("wrote %q, want the name fetch_filename resolves to", got)
	}
	data, err := os.ReadFile(got) //nolint:gosec // path produced by the call under test
	if err != nil || string(data) != twoStepPayload {
		t.Fatalf("file content = %q (err %v), want the CDN payload", data, err)
	}
	if srv.cdnHits != 1 {
		t.Errorf("the CDN was fetched %d times, want 1", srv.cdnHits)
	}
}

// TestFetchResponseFileRejectsAURLBody is the measured regression, as a test.
//
// With fetch_response left at its default, Blackmagic's 492 bytes of text/plain
// used to sail past the HTML guard and land in the distdir as a "zip" that
// pkgdev digests without complaint — a green Manifest for a file that is a
// sentence. The guard must refuse it, and must say what to configure.
func TestFetchResponseFileRejectsAURLBody(t *testing.T) {
	srv := newTwoStepServer(t)
	meta := twoStepMeta(srv)
	delete(meta, metaFetchResponse)    // the default: "the reply IS the file"
	delete(meta, metaFetchContentType) // so the textual guard is what fires, alone

	spec, _, err := parseAuthFetchSpec(meta)
	if err != nil {
		t.Fatalf("parseAuthFetchSpec: %v", err)
	}

	dir := t.TempDir()
	_, err = spec.fetchDistfile(context.Background(), "21.1", dir)
	if !errors.Is(err, ErrAuthFetchFailed) {
		t.Fatalf("err = %v, want ErrAuthFetchFailed", err)
	}
	if !strings.Contains(err.Error(), "text/plain") {
		t.Errorf("err = %q, want it to name the content type it refused", err)
	}
	if !strings.Contains(err.Error(), metaFetchResponse) {
		t.Errorf("err = %q, want it to name %s, the key that fixes this", err, metaFetchResponse)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the refused body was written to the distdir anyway: %v", entries)
	}
}

func TestTwoStepGuards(t *testing.T) {
	t.Run("a CDN answer of the wrong type is refused", func(t *testing.T) {
		srv := newTwoStepServer(t)
		srv.cdnCT = "application/json"
		srv.cdnPayload = `{"error":"expired"}`

		spec, _, err := parseAuthFetchSpec(twoStepMeta(srv))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		dir := t.TempDir()
		_, err = spec.fetchDistfile(context.Background(), "21.1", dir)
		if !errors.Is(err, ErrAuthFetchFailed) || !strings.Contains(err.Error(), "application/json") {
			t.Fatalf("err = %v, want a refusal naming the content type", err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("distdir not empty after a refused download: %v", entries)
		}
	})

	t.Run("a file below fetch_min_bytes is refused", func(t *testing.T) {
		srv := newTwoStepServer(t)
		meta := twoStepMeta(srv)
		meta[metaFetchMinBytes] = "1048576" // 1 MiB; the payload is a few dozen bytes

		spec, _, err := parseAuthFetchSpec(meta)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		dir := t.TempDir()
		_, err = spec.fetchDistfile(context.Background(), "21.1", dir)
		if !errors.Is(err, ErrAuthFetchFailed) || !strings.Contains(err.Error(), metaFetchMinBytes) {
			t.Fatalf("err = %v, want a refusal naming %s", err, metaFetchMinBytes)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("distdir not empty after a refused download: %v", entries)
		}
	})

	t.Run("an expired signed URL is reported as such", func(t *testing.T) {
		srv := newTwoStepServer(t)
		srv.failCDNWith = http.StatusForbidden

		spec, _, err := parseAuthFetchSpec(twoStepMeta(srv))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		_, err = spec.fetchDistfile(context.Background(), "21.1", t.TempDir())
		if !errors.Is(err, ErrAuthFetchFailed) || !strings.Contains(err.Error(), "403") {
			t.Fatalf("err = %v, want the CDN status reported", err)
		}
		if !strings.Contains(err.Error(), "short-lived") {
			t.Errorf("err = %q, want the likeliest cause named", err)
		}
	})

	t.Run("a body that is not a URL is refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("Sorry, your request could not be processed."))
		}))
		defer srv.Close()

		spec, _, err := parseAuthFetchSpec(map[string]string{
			metaFetchURL:      srv.URL,
			metaFetchResponse: fetchResponseURL,
			metaFetchFilename: "x-{version}.zip",
		})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		_, err = spec.fetchDistfile(context.Background(), "1.0", t.TempDir())
		if !errors.Is(err, ErrAuthFetchFailed) || !strings.Contains(err.Error(), "not an absolute http(s) URL") {
			t.Fatalf("err = %v, want the body refused as a non-URL", err)
		}
		// The body is quoted back, because "it is not a URL" without saying
		// what it was leaves the operator to reproduce the request by hand.
		if !strings.Contains(err.Error(), "Sorry") {
			t.Errorf("err = %q, want the response quoted back", err)
		}
	})
}

// TestPostRedirectIsRefusedAsAMethodDowngrade pins the error that used to lie.
//
// A 302 makes Go reissue the POST as a GET with no body, so the vendor sees a
// request carrying none of the form and answers accordingly — and the old
// message blamed the serial, which was wrong in both fact and remedy.
func TestPostRedirectIsRefusedAsAMethodDowngrade(t *testing.T) {
	var landedAs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			landedAs = r.Method
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<form>log in</form>"))
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	spec, _, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:      srv.URL + "/submit",
		metaFetchFilename: "x-{version}.zip",
		metaFetchForm:     "product=Example",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	_, err = spec.fetchDistfile(context.Background(), "1.0", t.TempDir())
	if !errors.Is(err, ErrAuthFetchFailed) {
		t.Fatalf("err = %v, want ErrAuthFetchFailed", err)
	}
	if !strings.Contains(err.Error(), "redirect") || !strings.Contains(err.Error(), "NOT a rejected serial") {
		t.Errorf("err = %q, want it to name the redirect and disown the serial explanation", err)
	}
	if landedAs != "" {
		t.Errorf("the redirect was followed and arrived as %s; the body was already gone by then", landedAs)
	}
}

// TestParseAuthFetchSpecRejectsBrokenTwoStepConfig covers the configs that
// cannot describe a request. Each one is a shape an operator can reach by
// editing half a record, and each fails at parse time — before a sweep spends a
// request discovering it.
func TestParseAuthFetchSpecRejectsBrokenTwoStepConfig(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			metaFetchURL:      "https://vendor.test/dl",
			metaFetchFilename: "x-{version}.zip",
		}
	}

	for _, tc := range []struct {
		name   string
		mutate func(map[string]string)
		wants  []string
	}{
		{
			name:   "an encoding the builder cannot produce",
			mutate: func(m map[string]string) { m[metaFetchBody] = "multipart" },
			wants:  []string{metaFetchBody, "form", "json"},
		},
		{
			name:   "a first reply that is neither of the two things it can be",
			mutate: func(m map[string]string) { m[metaFetchResponse] = "redirect" },
			wants:  []string{metaFetchResponse, "file", "url"},
		},
		{
			name:   "an id lookup with no pattern",
			mutate: func(m map[string]string) { m[metaFetchIDURL] = "https://vendor.test/catalogue" },
			wants:  []string{metaFetchIDPattern, "both or neither"},
		},
		{
			name:   "a pattern with nothing to fetch",
			mutate: func(m map[string]string) { m[metaFetchIDPattern] = `id":"([a-z]+)"` },
			wants:  []string{metaFetchIDURL, "both or neither"},
		},
		{
			name:   "an {id} nothing resolves",
			mutate: func(m map[string]string) { m[metaFetchURL] = "https://vendor.test/dl/{id}" },
			wants:  []string{idPlaceholder, metaFetchIDURL},
		},
		{
			name: "a lookup whose answer has nowhere to go",
			mutate: func(m map[string]string) {
				m[metaFetchIDURL] = "https://vendor.test/catalogue"
				m[metaFetchIDPattern] = `id":"([a-z]+)"`
			},
			wants: []string{"never uses", idPlaceholder},
		},
		{
			name: "a pattern that captures more than the id",
			mutate: func(m map[string]string) {
				m[metaFetchURL] = "https://vendor.test/dl/{id}"
				m[metaFetchIDURL] = "https://vendor.test/catalogue"
				m[metaFetchIDPattern] = `(a)id":"([a-z]+)"`
			},
			wants: []string{"exactly 1 capture group", "it has 2"},
		},
		{
			name: "a pattern that does not compile",
			mutate: func(m map[string]string) {
				m[metaFetchURL] = "https://vendor.test/dl/{id}"
				m[metaFetchIDURL] = "https://vendor.test/catalogue"
				m[metaFetchIDPattern] = `id":"([a-z]+`
			},
			wants: []string{"invalid " + metaFetchIDPattern},
		},
		{
			name:   "a size floor that is not a size",
			mutate: func(m map[string]string) { m[metaFetchMinBytes] = "4 GB" },
			wants:  []string{metaFetchMinBytes, "whole number of bytes"},
		},
		{
			name: "a repeated field with no JSON spelling",
			mutate: func(m map[string]string) {
				m[metaFetchBody] = fetchBodyJSON
				m[metaFetchForm] = "tag=a&tag=b"
			},
			wants: []string{"repeated", "JSON object holds each key once"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := base()
			tc.mutate(meta)

			_, ok, err := parseAuthFetchSpec(meta)
			if err == nil || ok {
				t.Fatalf("got (ok=%v, err=%v), want (false, error)", ok, err)
			}
			if !errors.Is(err, ErrAuthFetchFailed) {
				t.Errorf("err = %v, want it to wrap ErrAuthFetchFailed", err)
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

// TestValidateMetaFetchMirrorsTheNewEnums keeps the load-time validator and the
// parser saying the same thing about the two keys whose misreading is SILENT: a
// fetch_response nobody validated is a download that writes the vendor's answer
// verbatim.
func TestValidateMetaFetchMirrorsTheNewEnums(t *testing.T) {
	for _, tc := range []struct {
		key      string
		bad      string
		good     []string
		sentinel error
	}{
		{metaFetchBody, "multipart", []string{"", "form", "json", "JSON", " json "}, ErrInvalidMetaFetchBody},
		{metaFetchResponse, "redirect", []string{"", "file", "url", "URL", " url "}, ErrInvalidMetaFetchResponse},
	} {
		t.Run(tc.key, func(t *testing.T) {
			meta := map[string]string{metaFetchURL: "https://vendor.test/dl", tc.key: tc.bad}
			err := validateMetaFetch("test/pkg", meta)
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("err = %v, want %v", err, tc.sentinel)
			}
			// Trimming and case-folding must match the parser's, or the lint
			// would reject a record the applier accepts.
			for _, good := range tc.good {
				meta[tc.key] = good
				if err := validateMetaFetch("test/pkg", meta); err != nil {
					t.Errorf("%s=%q rejected by the validator but accepted by the parser: %v", tc.key, good, err)
				}
			}
		})
	}
}

// TestREADMEExampleIsAConfigThisParserAccepts reads the documented record out of
// the README and puts it through the real loader and the real parser.
//
// A documented example nobody executes is a plausible-looking string: the
// previous draft of this one used backslash line continuations, which TOML does
// not have, and it read perfectly well. An operator who copies a broken example
// discovers it as a failed sweep.
func TestREADMEExampleIsAConfigThisParserAccepts(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}

	const marker = `meta = { fetch_url = "https://vendor.example`
	var line string
	for _, l := range strings.Split(string(readme), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), marker) {
			line = strings.TrimSpace(l)
			break
		}
	}
	if line == "" {
		t.Fatalf("the README no longer carries the documented two-step meta example (looked for %q);\n"+
			"if it was renamed or moved, move this guard with it rather than deleting it", marker)
	}

	cfg, err := decodePackagesConfig([]byte("[\"app-misc/example\"]\n" +
		"url = \"https://upstream.test/releases.json\"\nparser = \"json\"\npath = \"tag_name\"\n" +
		line + "\ncomments = \"\"\"the README's own example\"\"\"\n"))
	if err != nil {
		t.Fatalf("the README example does not load: %v", err)
	}
	pkg := cfg.Packages["app-misc/example"]
	if err := ValidatePackageConfig("app-misc/example", &pkg); err != nil {
		t.Fatalf("the README example does not pass --lint's validation: %v", err)
	}

	spec, ok, err := parseAuthFetchSpec(pkg.Meta)
	if err != nil || !ok {
		t.Fatalf("the README example does not parse: ok=%v err=%v", ok, err)
	}
	if !spec.usesIDLookup() || spec.body != fetchBodyJSON || spec.response != fetchResponseURL {
		t.Errorf("the example no longer demonstrates what its prose claims: %+v", spec)
	}
	if spec.minBytes == 0 || spec.contentType == "" {
		t.Errorf("the example no longer demonstrates the final-response guards: %+v", spec)
	}
}

// TestWrittenDistfileIsReadableByTheMergingUser pins the mode.
//
// os.CreateTemp makes 0600 and the rename preserves it, which was invisible
// while the only caller was the sweep writing into a private distdir it owns.
// `bentoo distfile fetch` writes into the HOST's DISTDIR, where the process
// that reads the file at merge time is uid `portage` under FEATURES="userfetch
// userpriv" — and a 0600 distfile is unreadable to it. The merge then fails on
// a file that is present, complete and digest-correct, which is as misleading
// as a failure gets.
func TestWrittenDistfileIsReadableByTheMergingUser(t *testing.T) {
	srv := newTwoStepServer(t)
	spec, _, err := parseAuthFetchSpec(twoStepMeta(srv))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, err := spec.fetchDistfile(context.Background(), "21.1", t.TempDir())
	if err != nil {
		t.Fatalf("fetchDistfile: %v", err)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != distfileMode {
		t.Errorf("distfile mode = %#o, want %#o — uid portage must be able to read it", mode, distfileMode)
	}
}
