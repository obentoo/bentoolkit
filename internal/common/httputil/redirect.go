package httputil

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// maxRedirects is net/http's own default limit. Installing any CheckRedirect
// replaces the default policy, and with it the limit, so CredentialRedirectPolicy
// re-imposes it (S052-R4.5).
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
// lets a credential header follow a redirect somewhere it was not sent to
// (audit findings A4, A5). It applies three rules, in order:
//
//  1. A chain stops after 10 redirects, as net/http's default policy does.
//  2. If the original request was https and carried a credential header, a
//     redirect to an http URL is refused with an error wrapping
//     ErrInsecureRedirect that names the target host; no request is sent to
//     it. The decision reads the ORIGINAL request, so it holds even when the
//     hop also changes host and an earlier hop already dropped the headers.
//  3. Once the chain has left the original hostname — on this hop or any
//     earlier one — every credential header is deleted from the request and
//     the redirect is followed. Hostnames are compared case-insensitively and
//     exactly: no subdomain match, port ignored.
//
// Rule 3 reads the whole chain, not the previous hop, because net/http copies
// the headers of the INITIAL request onto every hop before calling
// CheckRedirect (net/http client.go, makeHeadersCopier): a header deleted on
// one hop is back on the next, so a policy that only compared adjacent hops
// would hand the credential back on a hop that returns to the first host.
//
// Go itself strips Authorization (and cookies) on a cross-domain hop, but not
// custom headers such as X-Api-Key or Private-Token, and it does not refuse an
// https to http hop at all. The policy never logs or returns a header value.
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
