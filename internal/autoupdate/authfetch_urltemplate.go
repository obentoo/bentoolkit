package autoupdate

import (
	"fmt"
	"net/url"
	"strings"
)

// checkFetchURLTemplates refuses a fetch_url or fetch_id_url that does not name
// one fixed http(s) host. idURL may be empty (no id lookup configured).
//
// # Why the host must be fixed in the record
//
// Both templates are written by an overlay contributor, and both are completed
// at fetch time with a value an UPSTREAM chose: fetch_url with the {id} the
// catalogue answered, fetch_id_url with the detected {version}. A placeholder in
// the path or the query can only change what is asked of the vendor. One in the
// scheme, the userinfo or the host lets the upstream pick WHERE the request goes
// — and the form leg carries the serial and the identity fields. So a
// placeholder is allowed only after the authority ends, and the scheme must be
// http or https: a file, ftp or gopher URL is never a download endpoint.
//
// This is the guard the G704 suppressions on the fetch requests name. Every
// request authfetch builds starts from a template that passed here, so its host
// is the one the record spells out.
func checkFetchURLTemplates(fetchURL, idURL string) error {
	if err := checkFetchURLTemplate(metaFetchURL, fetchURL); err != nil {
		return err
	}
	if idURL == "" {
		return nil
	}
	return checkFetchURLTemplate(metaFetchIDURL, idURL)
}

// checkFetchURLTemplate checks one template; key names it in the error, so the
// contributor is told which line of the record to fix.
func checkFetchURLTemplate(key, template string) error {
	switch urlTemplateFault(template) {
	case templateNotHTTP:
		return fmt.Errorf("%w: %s=%q is not an absolute http(s) URL with a host", ErrAuthFetchFailed, key, template)
	case templatePlaceholderInHost:
		return fmt.Errorf("%w: %s=%q puts a placeholder in the scheme or host; %s and %s may appear only in the path or query, where an upstream value cannot choose the host",
			ErrAuthFetchFailed, key, template, idPlaceholder, versionPlaceholder)
	}
	return nil
}

// templateFault is what urlTemplateFault found wrong with a URL template.
type templateFault int

const (
	templateOK templateFault = iota
	templateNotHTTP
	templatePlaceholderInHost
)

// urlTemplateFault applies the rule checkFetchURLTemplate documents — an
// absolute http(s) URL whose scheme and authority hold no placeholder — and
// says which half failed, so each caller words its own error.
func urlTemplateFault(template string) templateFault {
	u, err := url.Parse(template)
	// url.Parse lowercases the scheme, so HTTPS:// is accepted as https://.
	// Opaque is set for "http:vendor.test" — a scheme with no authority.
	if err != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return templateNotHTTP
	}

	// The head is everything up to where the path, query or fragment begins:
	// scheme, userinfo, host and port. A successful parse with a host means
	// the template holds "scheme://", and a scheme cannot contain "/", so the
	// first "//" is the one that opens the authority.
	authority := strings.Index(template, "//") + len("//")
	end := len(template)
	if i := strings.IndexAny(template[authority:], "/?#"); i >= 0 {
		end = authority + i
	}
	if strings.ContainsAny(template[:end], "{}") {
		return templatePlaceholderInHost
	}
	return templateOK
}
