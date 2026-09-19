package autoupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// This file holds the three things a gated download needs once the vendor stops
// simply answering the form with the file:
//
//   - resolveEndpointID — the endpoint's id is minted per release, so it is
//     looked up at fetch time instead of being frozen into the record;
//   - followDownloadURL — the reply names the file instead of being it;
//   - guardFinalResponse — what must be true of whatever is about to be written.
//
// They live apart from authfetch.go because that file answers "what does this
// record say" and this one answers "what came back", and the second question is
// where every silent failure this package has had was decided.

// idBodyLimit bounds the id-lookup body. The one it was written against is a
// catalogue of every download the vendor publishes (~1200 entries), so the cap
// is generous; it exists because an endpoint that answers a lookup with an
// unbounded stream must not be read into memory until the process dies.
const idBodyLimit = 16 << 20 // 16 MiB

// urlBodyLimit bounds the body that CONTAINS a URL. Measured against
// Blackmagic's endpoint that body is 492 bytes; anything past a few KiB is not
// a URL and reading further only delays saying so.
const urlBodyLimit = 8 << 10 // 8 KiB

// textualContentTypes are the content types a finished distfile never has.
//
// text/html was the original guard, and it was not enough: the reply that
// started this — a URL where a zip was expected — is text/plain, which sailed
// straight past a check for HTML into a 492-byte "distfile". The guard is
// therefore stated as "a distfile is not text", which covers the next vendor's
// version of the same mistake without anyone having to predict its content type.
var textualContentTypes = []string{
	"text/",
	"application/json",
	"application/xml",
	"application/xhtml",
}

// refuseMethodDowngrade stops the redirect that silently empties a POST.
//
// On 301, 302 and 303 Go's client — like every browser — reissues the request as
// a GET and DROPS the body. The vendor then sees a request carrying none of the
// form: no serial, no identity, no product. What comes back is the login page or
// a generic refusal, and the error this package used to report blamed the
// serial, which was correct in neither fact nor remedy.
//
// 307 and 308 preserve both method and body, so they are followed. The check is
// on the METHOD rather than on the status code because the method is what the
// damage consists of: if a future Go changed which codes rewrite the request,
// this guard would still fire on exactly the requests that lost their body.
func refuseMethodDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	original := via[0].Method
	if original == req.Method {
		return nil
	}
	return fmt.Errorf("%w: the endpoint redirected to %s and the redirect turned the %s into a %s, which drops the form body — "+
		"this is NOT a rejected serial; point %s at the address the redirect names",
		ErrAuthFetchFailed, req.URL.Redacted(), original, req.Method, metaFetchURL)
}

// resolveEndpointID fetches the document that publishes the per-release download
// id and extracts it with fetch_id_pattern.
//
// The version is quoted into the pattern with regexp.QuoteMeta, and that is not
// a detail: an unquoted "21.1" is a regex matching "2101" as happily as "21.1",
// so a catalogue holding both would answer with whichever came first. The
// operator writes the version as a literal and gets a literal.
func (s *authFetchSpec) resolveEndpointID(ctx context.Context, version string) (string, error) {
	lookupURL := strings.ReplaceAll(s.idURL, versionPlaceholder, version)

	pattern, err := regexp.Compile(strings.ReplaceAll(s.idPattern, versionPlaceholder, regexp.QuoteMeta(version)))
	if err != nil {
		// parseIDLookup already compiled this against a stand-in version, so
		// reaching here means the VERSION made it invalid, not the pattern.
		return "", fmt.Errorf("%w: %s does not compile with version %q substituted: %v", ErrAuthFetchFailed, metaFetchIDPattern, version, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lookupURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: building the %s request: %v", ErrAuthFetchFailed, metaFetchIDURL, err)
	}
	req.Header.Set("User-Agent", authFetchUserAgent)

	client := &http.Client{Timeout: authFetchTimeout}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %s request failed: %v", ErrAuthFetchFailed, metaFetchIDURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %s returned HTTP %d", ErrAuthFetchFailed, metaFetchIDURL, resp.StatusCode)
	}
	doc, err := io.ReadAll(io.LimitReader(resp.Body, idBodyLimit))
	if err != nil {
		return "", fmt.Errorf("%w: reading %s: %v", ErrAuthFetchFailed, metaFetchIDURL, err)
	}

	m := pattern.FindSubmatch(doc)
	if m == nil {
		// The catalogue is named and the version is named, because the usual
		// cause is neither a broken pattern nor a broken endpoint: it is a
		// version the vendor has not published (or has retired).
		return "", fmt.Errorf("%w: %s matched nothing in %s for version %q — the release may not be published there",
			ErrAuthFetchFailed, metaFetchIDPattern, lookupURL, version)
	}
	id := strings.TrimSpace(string(m[1]))
	if id == "" {
		return "", fmt.Errorf("%w: %s matched but captured an empty id in %s", ErrAuthFetchFailed, metaFetchIDPattern, lookupURL)
	}
	return id, nil
}

// followDownloadURL reads the URL out of the first reply and fetches it,
// returning the live response whose body is the actual file.
//
// The caller owns the returned response and must close it. The first reply is
// closed by the caller's own defer, which still holds.
func (s *authFetchSpec) followDownloadURL(ctx context.Context, first *http.Response, secret string) (*http.Response, error) {
	raw, err := io.ReadAll(io.LimitReader(first.Body, urlBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the download URL: %v", ErrAuthFetchFailed, secrets.Scrub(err.Error(), secret))
	}

	target, err := parseDownloadURL(string(raw), first.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}

	// No credential rides on this leg: the vendor signed the URL for this
	// request and the signature IS the authorisation, so sending the serial to
	// a CDN would only widen where it can appear.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: building the download request: %v", ErrAuthFetchFailed, err)
	}
	req.Header.Set("User-Agent", authFetchUserAgent)

	// Redirects are FOLLOWED here, unlike on the form leg: this is already a GET
	// with no body, so nothing can be dropped, and a CDN edge redirecting to a
	// region is ordinary.
	client := &http.Client{Timeout: authFetchTimeout}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: downloading from the URL the endpoint returned: %v", ErrAuthFetchFailed, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		// A signed URL is short-lived — three hours, on the endpoint this was
		// written against — so an expired one is the likeliest reading of a 403
		// and the operator should not have to guess that.
		return nil, fmt.Errorf("%w: the URL the endpoint returned answered HTTP %d (a signed link is short-lived; fetch again rather than reusing one)",
			ErrAuthFetchFailed, resp.StatusCode)
	}
	return resp, nil
}

// parseDownloadURL turns the first reply's body into the address to fetch.
//
// It accepts the bare URL and the JSON-quoted one, because both are a string to
// the JavaScript the vendor's own page runs and neither is unusual; anything
// else is refused with the body's opening characters quoted, since a body that
// is not a URL is nearly always an error page nobody parsed.
func parseDownloadURL(body, contentType string) (string, error) {
	raw := strings.TrimSpace(body)
	raw = strings.Trim(raw, `"`)
	raw = strings.TrimSpace(raw)

	if raw == "" {
		return "", fmt.Errorf("%w: %s=%q but the response body is empty", ErrAuthFetchFailed, metaFetchResponse, fetchResponseURL)
	}

	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("%w: %s=%q but the response (content-type %q) is not an absolute http(s) URL: %s",
			ErrAuthFetchFailed, metaFetchResponse, fetchResponseURL, contentType, firstChars(raw, 120))
	}
	return raw, nil
}

// guardFinalResponse is the last thing standing between a response and the
// distdir. It judges the response that is ABOUT TO BE WRITTEN — the second leg's
// when there is one — and never the one before it.
func (s *authFetchSpec) guardFinalResponse(resp *http.Response) error {
	ct := resp.Header.Get("Content-Type")
	lower := strings.ToLower(ct)

	// Kept as its own case, ahead of the general rule, because it is the one
	// textual answer whose CAUSE is knowable: the vendor re-showed the form.
	if strings.Contains(lower, "text/html") {
		return fmt.Errorf("%w: server returned HTML (content-type %q), not a file — likely an invalid serial or a rejected request", ErrAuthFetchFailed, ct)
	}
	for _, textual := range textualContentTypes {
		if strings.HasPrefix(lower, textual) {
			hint := ""
			if s.response == fetchResponseFile {
				// The 492-byte case, named: a record left at the default while
				// the endpoint answers with a URL lands exactly here.
				hint = fmt.Sprintf(" (if this endpoint answers with the download URL, set %s=%q)", metaFetchResponse, fetchResponseURL)
			}
			return fmt.Errorf("%w: server returned text (content-type %q), not a file%s", ErrAuthFetchFailed, ct, hint)
		}
	}

	if s.contentType != "" && !strings.HasPrefix(lower, strings.ToLower(s.contentType)) {
		return fmt.Errorf("%w: content-type %q does not match the configured %s=%q", ErrAuthFetchFailed, ct, metaFetchContentType, s.contentType)
	}

	// Content-Length is advisory — it is absent on a chunked response, and
	// writeBody checks the bytes that actually arrive — but when it IS present
	// and already too small, saying so now saves transferring the rest.
	if s.minBytes > 0 && resp.ContentLength >= 0 && resp.ContentLength < s.minBytes {
		return fmt.Errorf("%w: the response announces %d bytes, below the %s of %d",
			ErrAuthFetchFailed, resp.ContentLength, metaFetchMinBytes, s.minBytes)
	}
	return nil
}

// firstChars quotes the opening of a body for a diagnostic, without letting a
// whole error page into the message.
func firstChars(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%q…", s[:n])
}
