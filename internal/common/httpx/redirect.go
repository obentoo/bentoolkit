package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// maxRedirects is net/http's own default limit. Installing any CheckRedirect
// replaces the default policy, and with it the limit, so CredentialRedirectPolicy
// re-imposes it.
const maxRedirects = 10

// CredentialHeaders are the request headers that carry a credential, in
// canonical form ("PRIVATE-TOKEN" canonicalises to "Private-Token"). They are
// the headers bentoolkit expands ${VAR} credentials into and the ones its
// GitHub, GitLab and LLM clients authenticate with.
var CredentialHeaders = []string{"Authorization", "X-Api-Key", "X-Auth-Token", "Private-Token"}

// ErrInsecureRedirect is returned by CredentialRedirectPolicy when following a
// redirect would send a credential from https to plain http.
var ErrInsecureRedirect = errors.New("insecure redirect")

// CredentialRedirectPolicy is an http.Client.CheckRedirect policy that never
// lets a credential header follow a redirect somewhere it was not sent to:
//
//  1. A chain stops after 10 redirects, as net/http's default policy does.
//  2. An https request that carried a credential header refuses a redirect to
//     an http URL with an error wrapping ErrInsecureRedirect that names the
//     target host. It reads the ORIGINAL request, so it holds even when an
//     earlier hop already changed host and dropped the headers.
//  3. Once the chain has left the original hostname, on this hop or an earlier
//     one, every credential header is deleted and the redirect followed.
//     Hostnames compare case-insensitively and exactly; the port is ignored.
//
// Rule 3 reads the whole chain because net/http copies the INITIAL request's
// headers onto every hop (client.go, makeHeadersCopier): a header deleted on
// one hop is back on the next. Go strips Authorization on a cross-domain hop
// but not X-Api-Key or Private-Token, and never refuses https to http. The
// policy never logs or returns a header value.
func CredentialRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("stopped after 10 redirects")
	}
	if len(via) == 0 {
		return nil
	}

	orig := via[0]
	if orig.URL.Scheme == "https" && req.URL.Scheme == "http" && carriesCredential(orig.Header) {
		return fmt.Errorf("%w: redirect from https to http://%s would send a credential in cleartext",
			ErrInsecureRedirect, req.URL.Host)
	}

	if leftHost(orig.URL.Hostname(), req, via[1:]) {
		for _, name := range CredentialHeaders {
			req.Header.Del(name)
		}
	}
	return nil
}

// carriesCredential reports whether h holds any of CredentialHeaders.
func carriesCredential(h http.Header) bool {
	for _, name := range CredentialHeaders {
		if len(h.Values(name)) > 0 {
			return true
		}
	}
	return false
}

// leftHost reports whether req, or any of the intermediate hops, is on a
// hostname other than origHost.
func leftHost(origHost string, req *http.Request, hops []*http.Request) bool {
	if !strings.EqualFold(req.URL.Hostname(), origHost) {
		return true
	}
	for _, h := range hops {
		if !strings.EqualFold(h.URL.Hostname(), origHost) {
			return true
		}
	}
	return false
}
