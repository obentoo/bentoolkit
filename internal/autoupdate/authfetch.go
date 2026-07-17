// Package autoupdate: authenticated distfile fetching for packages whose
// upstream download is gated behind a serial/registration key (e.g. commercial
// software). Such a distfile cannot be retrieved by `pkgdev manifest` from the
// ebuild's SRC_URI, so before the manifest step we POST the form the vendor's
// download page submits — with the serial injected from a secret — and drop the
// resulting file into pkgdev's private --distdir. pkgdev then digests the local
// file instead of fetching.
//
// The behaviour is driven entirely by a package's [meta] block in packages.toml
// (free-form key/value), so adding another serial-gated package needs no code
// change. The serial itself is NEVER stored in the overlay: it is resolved at
// runtime from an env var (or a local secrets file) and is scrubbed from every
// log line and error message.
package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// Error variables for authenticated fetching.
var (
	// ErrAuthFetchSecretMissing is returned when the configured serial cannot be
	// resolved from the environment or the secrets file.
	ErrAuthFetchSecretMissing = errors.New("authenticated fetch: serial not found")
	// ErrAuthFetchFailed is returned when the download itself fails (bad config,
	// transport error, non-OK status, or an HTML "form re-shown" response).
	ErrAuthFetchFailed = errors.New("authenticated distfile fetch failed")
)

// meta keys that drive an authenticated fetch. fetch_url is the trigger: a
// package without it is fetched the normal way (pkgdev from SRC_URI).
const (
	metaFetchMethod      = "fetch_method"       // "post" (default) or "get"
	metaFetchURL         = "fetch_url"           // form action / endpoint
	metaFetchSerialEnv   = "fetch_serial_env"    // env var holding the serial
	metaFetchSerialField = "fetch_serial_field"  // form field name for the serial
	metaFetchForm        = "fetch_form"          // other form fields, urlencoded
	metaFetchFilename    = "fetch_filename"      // dest name; {version} is substituted
)

// authFetchTimeout bounds the authenticated download. The payload is a full
// binary distfile (tens of MB), so it gets a generous-but-finite budget.
const authFetchTimeout = 5 * time.Minute

// authFetchSpec is the parsed, validated description of one authenticated
// download, derived from a package's [meta] block.
type authFetchSpec struct {
	method      string     // "post" or "get"
	url         string     // endpoint
	serialEnv   string     // env var name for the serial
	serialField string     // form field the serial goes into
	form        url.Values // static form fields (platform, submit button, ...)
	filename    string     // dest filename template (may contain {version})
}

// parseAuthFetchSpec extracts an authFetchSpec from a package's meta map.
//
// It returns (nil, false, nil) when the package defines no authenticated fetch
// (no fetch_url) — the common case, handled by the normal manifest path. When
// fetch_url IS present it returns a non-nil error if any required companion
// field is missing or malformed, so a half-written config fails loudly rather
// than silently falling back to a fetch that cannot work.
func parseAuthFetchSpec(meta map[string]string) (*authFetchSpec, bool, error) {
	if meta == nil {
		return nil, false, nil
	}
	rawURL := strings.TrimSpace(meta[metaFetchURL])
	if rawURL == "" {
		return nil, false, nil
	}

	spec := &authFetchSpec{
		method:      strings.ToLower(strings.TrimSpace(meta[metaFetchMethod])),
		url:         rawURL,
		serialEnv:   strings.TrimSpace(meta[metaFetchSerialEnv]),
		serialField: strings.TrimSpace(meta[metaFetchSerialField]),
		filename:    strings.TrimSpace(meta[metaFetchFilename]),
	}
	if spec.method == "" {
		spec.method = "post"
	}
	if spec.method != "post" && spec.method != "get" {
		return nil, false, fmt.Errorf("%w: %s=%q must be \"post\" or \"get\"", ErrAuthFetchFailed, metaFetchMethod, spec.method)
	}
	if spec.serialEnv == "" {
		return nil, false, fmt.Errorf("%w: %s is required", ErrAuthFetchFailed, metaFetchSerialEnv)
	}
	if spec.serialField == "" {
		return nil, false, fmt.Errorf("%w: %s is required", ErrAuthFetchFailed, metaFetchSerialField)
	}
	if spec.filename == "" {
		return nil, false, fmt.Errorf("%w: %s is required", ErrAuthFetchFailed, metaFetchFilename)
	}

	form, err := url.ParseQuery(meta[metaFetchForm])
	if err != nil {
		return nil, false, fmt.Errorf("%w: invalid %s: %v", ErrAuthFetchFailed, metaFetchForm, err)
	}
	spec.form = form

	return spec, true, nil
}

// resolvedFilename substitutes {version} in the filename template.
func (s *authFetchSpec) resolvedFilename(version string) string {
	return strings.ReplaceAll(s.filename, "{version}", version)
}

// resolveSecret looks up the named serial via the shared resolution chain in
// internal/common/secrets (env var first, then the user-scope secrets file, then
// the system-scope /etc/bentoo/secrets; ".env" style, value never logged).
//
// A total miss is reported as ErrAuthFetchSecretMissing so errors.Is callers and
// the existing tests keep working; a present-but-unreadable secrets file
// (secrets.ErrUnreadable) is surfaced wrapped rather than degraded to a silent
// miss, and stays distinguishable via errors.Is.
func resolveSecret(envName string) (string, error) {
	v, found, err := secrets.Lookup(envName)
	if err != nil {
		return "", fmt.Errorf("%w: resolving %s: %w", ErrAuthFetchSecretMissing, envName, err)
	}
	if !found {
		return "", fmt.Errorf("%w: %s (export %s=... or add it to one of: %s)",
			ErrAuthFetchSecretMissing, envName, envName, strings.Join(secrets.Paths(), ", "))
	}
	return v, nil
}

// fetchDistfile resolves the serial, submits the form, and writes the response
// body into destDir under the resolved filename (which must match the basename
// of the ebuild's SRC_URI so pkgdev digests it). It returns the written path.
//
// Failure modes are mapped to clear errors, and the serial is scrubbed from any
// message that could echo it (notably transport errors on the GET path, where
// the serial rides in the query string).
func (s *authFetchSpec) fetchDistfile(ctx context.Context, version, destDir string) (string, error) {
	secret, err := resolveSecret(s.serialEnv)
	if err != nil {
		return "", err
	}

	filename := s.resolvedFilename(version)
	// Defend the distdir: the filename becomes a path under destDir, so it must
	// be a bare name (no separators, no traversal).
	if filename == "" || strings.ContainsAny(filename, `/\`) || strings.Contains(filename, "..") {
		return "", fmt.Errorf("%w: resolved %s=%q is not a bare file name", ErrAuthFetchFailed, metaFetchFilename, filename)
	}
	destPath := filepath.Join(destDir, filename)

	req, err := s.buildRequest(ctx, secret)
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: authFetchTimeout}
	// Close the keep-alive connection once we are done: this is a one-shot
	// download, so a pooled idle connection would otherwise outlive the call
	// (and trip goroutine-leak detection in tests).
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: request failed: %v", ErrAuthFetchFailed, secrets.Scrub(err.Error(), secret))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: server returned HTTP %d", ErrAuthFetchFailed, resp.StatusCode)
	}
	// A serial-gated endpoint serves the file as a binary attachment; an HTML
	// body means the form was re-shown (invalid serial or rejected request),
	// which would otherwise be digested as a bogus "distfile".
	if ct := resp.Header.Get("Content-Type"); strings.Contains(strings.ToLower(ct), "text/html") {
		return "", fmt.Errorf("%w: server returned HTML (content-type %q), not a file — likely an invalid serial or a rejected request", ErrAuthFetchFailed, ct)
	}

	written, err := writeBody(destDir, destPath, resp.Body, secret)
	if err != nil {
		return "", err
	}
	return written, nil
}

// buildRequest constructs the POST (or GET) carrying the static form fields plus
// the serial in its configured field.
func (s *authFetchSpec) buildRequest(ctx context.Context, secret string) (*http.Request, error) {
	body := url.Values{}
	for k, vs := range s.form {
		for _, v := range vs {
			body.Add(k, v)
		}
	}
	body.Set(s.serialField, secret)

	var (
		req *http.Request
		err error
	)
	switch s.method {
	case "post":
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(body.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	case "get":
		sep := "?"
		if strings.Contains(s.url, "?") {
			sep = "&"
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, s.url+sep+body.Encode(), nil)
	default:
		return nil, fmt.Errorf("%w: unsupported method %q", ErrAuthFetchFailed, s.method)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: building request: %v", ErrAuthFetchFailed, secrets.Scrub(err.Error(), secret))
	}
	// Some vendor endpoints reject empty/bot User-Agents.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) bentoo-autoupdate")
	return req, nil
}

// writeBody streams the response into a temp file in destDir, then atomically
// renames it into place. A zero-byte body is rejected. The temp file is removed
// on any failure so a partial download never lands in the distdir.
func writeBody(destDir, destPath string, body io.Reader, secret string) (string, error) {
	tmp, err := os.CreateTemp(destDir, ".authfetch-*")
	if err != nil {
		return "", fmt.Errorf("%w: creating temp file: %v", ErrAuthFetchFailed, err)
	}
	tmpName := tmp.Name()

	n, copyErr := io.Copy(tmp, body)
	closeErr := tmp.Close()

	switch {
	case copyErr != nil:
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: writing body: %v", ErrAuthFetchFailed, secrets.Scrub(copyErr.Error(), secret))
	case closeErr != nil:
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: closing temp file: %v", ErrAuthFetchFailed, closeErr)
	case n == 0:
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: downloaded zero bytes", ErrAuthFetchFailed)
	}

	if err := os.Rename(tmpName, destPath); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: finalizing %s: %v", ErrAuthFetchFailed, filepath.Base(destPath), err)
	}
	return destPath, nil
}
