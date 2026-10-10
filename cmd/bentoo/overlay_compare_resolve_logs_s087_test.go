package main

// Story 087, sub-task 4.1 (R6.1, R6.2, R10.7): when `overlay compare` cannot
// resolve a repository, the "Repository not found." record carries the
// resolution error as its err attribute, and "Registry unavailable" carries
// the registry's load error when one occurred. The exit code stays 1.
//
// Hermetic: the world is s086World's (HOME, XDG_CONFIG_HOME and PATH under
// t.TempDir); the registry is read from a cache file seeded under that HOME,
// fresh enough that nothing is downloaded, or the run is on a cancelled
// context, which the HTTP transport answers before dialling.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	s087d41Unknown        = "s087-unknown"
	s087d41RegistryListed = `<?xml version="1.0" encoding="UTF-8"?>
<repositories version="1.0">
  <repo quality="experimental" status="unofficial">
    <name>s087-registered</name>
    <source type="git">https://example.invalid/s087-registered.git</source>
  </repo>
</repositories>
`
	s087d41RegistryEmpty      = `<?xml version="1.0"?><repositories version="1.0"></repositories>`
	s087d41RegistryUnparsable = "<repositories><repo>"
	s087d41ParseErr           = "failed to parse repository registry: "
)

// s087d41OutputFlags pins the output-flag globals this file depends on and
// restores them, whatever ran before in the process.
func s087d41OutputFlags(t *testing.T) {
	t.Helper()
	q, v, nc := quiet, verbose, noColor
	t.Cleanup(func() { quiet, verbose, noColor = q, v, nc })
	quiet, verbose, noColor = false, false, true
}

// s087d41SeedRegistry writes the registry cache runCompare reads under HOME.
func s087d41SeedRegistry(t *testing.T, body string) {
	t.Helper()
	cacheDir := filepath.Join(os.Getenv("HOME"), ".cache", "bentoo")
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "repositories.xml"), []byte(body), 0o600); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
}

// s087d41Records returns every log record whose message is exactly msg.
func s087d41Records(logs, msg string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, `msg="`+msg+`"`) || strings.Contains(line, `msg=`+msg+` `) {
			out = append(out, line)
		}
	}
	return out
}

// s087d41One returns the single record for msg, failing the test unless there
// is exactly one.
func s087d41One(t *testing.T, logs, msg string) string {
	t.Helper()
	recs := s087d41Records(logs, msg)
	if len(recs) != 1 {
		t.Fatalf("records with msg %q = %d, want 1\nlogs:\n%s", msg, len(recs), logs)
	}
	return recs[0]
}

func s087d41Run(t *testing.T, registry string, cancelled bool) s086Result {
	t.Helper()
	s086World(t)
	s087d41OutputFlags(t)
	if registry != "" {
		s087d41SeedRegistry(t, registry)
	}
	return s086Run(t, []string{s087d41Unknown}, cancelled)
}

// TestS087_4_1_InterruptedResolutionIsNotANotFound is the hostile half of
// R6.1's "for a reason other than an interruption": an interrupted registry
// fetch keeps its own line and never becomes a "Repository not found." record
// carrying the cancellation as its cause.
func TestS087_4_1_InterruptedResolutionIsNotANotFound(t *testing.T) {
	res := s087d41Run(t, "", true)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1\nlogs:\n%s", res.code, res.logs)
	}
	s087d41One(t, res.logs, registryInterruptedMsg)
	if recs := s087d41Records(res.logs, "Repository not found."); len(recs) != 0 {
		t.Errorf("an interruption logged %q: %q", "Repository not found.", recs)
	}
}

// TestS087_4_1_RegistryUnavailableWithoutLoadErrorHasNoErr is the hostile half
// of R6.2's "when one occurred": a registry that loads but lists nothing has no
// load error, so the hint must not borrow the resolution error (which says the
// repository was not found, not that the registry failed to load).
func TestS087_4_1_RegistryUnavailableWithoutLoadErrorHasNoErr(t *testing.T) {
	res := s087d41Run(t, s087d41RegistryEmpty, false)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1\nlogs:\n%s", res.code, res.logs)
	}
	rec := s087d41One(t, res.logs, "Registry unavailable. Use --sync to refresh or run `eselect repository list`")
	if strings.Contains(rec, " err=") {
		t.Errorf("Registry unavailable carries an err with no load error: %q", rec)
	}
}

// TestS087_4_1_RepositoryNotFoundCarriesResolutionError pins R6.1: the
// not-found record keeps its repository attribute and gains the resolution
// error as err, both when the registry simply lacks the name and when the
// registry itself could not be parsed (two different causes, two different
// err values).
func TestS087_4_1_RepositoryNotFoundCarriesResolutionError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		registry string
		wantErr  string
		notErr   string
	}{
		{
			name:     "registry lacks the name",
			registry: s087d41RegistryListed,
			wantErr:  `err="repository not found: ` + s087d41Unknown + `"`,
			notErr:   s087d41ParseErr,
		},
		{
			name:     "registry unparsable",
			registry: s087d41RegistryUnparsable,
			wantErr:  `err="` + s087d41ParseErr,
			notErr:   `err="repository not found`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := s087d41Run(t, tc.registry, false)

			if res.code != 1 {
				t.Errorf("exit code = %d, want 1\nlogs:\n%s", res.code, res.logs)
			}
			rec := s087d41One(t, res.logs, "Repository not found.")
			if !strings.HasPrefix(rec, "level=ERROR ") {
				t.Errorf("record level: %q, want ERROR", rec)
			}
			if !strings.Contains(rec, " repository="+s087d41Unknown) {
				t.Errorf("record lost its repository attribute: %q", rec)
			}
			if !strings.Contains(rec, tc.wantErr) {
				t.Errorf("record = %q, want it to carry %s", rec, tc.wantErr)
			}
			if strings.Contains(rec, tc.notErr) {
				t.Errorf("record = %q carries the other case's cause %q", rec, tc.notErr)
			}
		})
	}
}

// TestS087_4_1_RegistryUnavailableCarriesLoadError pins R6.2: a registry that
// cannot be parsed gives the "unavailable" hint, and that hint carries the
// parse error as err.
func TestS087_4_1_RegistryUnavailableCarriesLoadError(t *testing.T) {
	res := s087d41Run(t, s087d41RegistryUnparsable, false)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1\nlogs:\n%s", res.code, res.logs)
	}
	rec := s087d41One(t, res.logs, "Registry unavailable. Use --sync to refresh or run `eselect repository list`")
	if !strings.Contains(rec, `err="`+s087d41ParseErr) {
		t.Errorf("Registry unavailable = %q, want err=%q...", rec, s087d41ParseErr)
	}
}
