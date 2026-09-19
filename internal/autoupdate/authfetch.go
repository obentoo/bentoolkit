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
//
// The serial is optional. A vendor may gate the file behind the POST alone —
// no credential of any kind — and such a package configures fetch_url and the
// form without the serial pair. What it may not do is declare half of that
// pair; see parseAuthFetchSpec for why that is an error rather than a default.
package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	// metaFetchPrefix namespaces the sub-schema inside the otherwise free-form
	// [meta] map: a key carrying it is claimed by this file and validated,
	// anything else is an annotation nothing reads.
	metaFetchPrefix = "fetch_"

	metaFetchMethod      = "fetch_method"       // "post" (default) or "get"
	metaFetchURL         = "fetch_url"          // form action / endpoint
	metaFetchSerialEnv   = "fetch_serial_env"   // env var holding the serial; optional, but only together with the field
	metaFetchSerialField = "fetch_serial_field" // form field the serial goes into; optional, but only together with the env var
	metaFetchForm        = "fetch_form"         // other form fields, urlencoded
	metaFetchFilename    = "fetch_filename"     // dest name; {version} is substituted

	metaFetchBody        = "fetch_body"         // "form" (default, urlencoded) or "json"
	metaFetchResponse    = "fetch_response"     // "file" (default) or "url" (the body IS the download URL)
	metaFetchContentType = "fetch_content_type" // content type the FINAL response must carry
	metaFetchMinBytes    = "fetch_min_bytes"    // smallest believable size for the finished file
	metaFetchIDURL       = "fetch_id_url"       // JSON/text endpoint naming the per-release download id
	metaFetchIDPattern   = "fetch_id_pattern"   // regex over that body, 1 capture group = the id
	metaFetchFormEnv     = "fetch_form_env"     // form fields whose VALUES come from the secrets chain
	metaFetchTimeout     = "fetch_timeout"      // seconds for the whole download
)

// The two encodings fetch_body selects, and the two things fetch_response says
// the first reply is. They are constants rather than bare literals because the
// parser, the validator and the request builder each compare against them, and
// a literal in three places is a literal that can be misspelled in one.
const (
	fetchBodyForm = "form"
	fetchBodyJSON = "json"

	fetchResponseFile = "file"
	fetchResponseURL  = "url"

	// versionPlaceholder is substituted in fetch_filename, fetch_id_url and
	// fetch_id_pattern; idPlaceholder is substituted in fetch_url once the id
	// lookup has answered.
	versionPlaceholder = "{version}"
	idPlaceholder      = "{id}"
)

// metaFetchKeys is the closed set of meta keys above, as one list. It exists so
// the parser below and ValidatePackageConfig share a single enumeration of the
// six instead of repeating the literals: the schema drifted out of sight once
// already (the PackageConfig.Meta doc claimed nothing read this map) precisely
// because the key set was knowledge only this file held.
//
// Order matters only for the error message that offers it as a spelling guide,
// so it follows the const block.
var metaFetchKeys = []string{
	metaFetchMethod,
	metaFetchURL,
	metaFetchSerialEnv,
	metaFetchSerialField,
	metaFetchForm,
	metaFetchFilename,
	metaFetchBody,
	metaFetchResponse,
	metaFetchContentType,
	metaFetchMinBytes,
	metaFetchIDURL,
	metaFetchIDPattern,
	metaFetchFormEnv,
	metaFetchTimeout,
}

// Validation errors for the meta.fetch_* sub-schema. They are checked at config
// load / lint time, unlike the Err* pair above, which report a download that
// actually failed.
var (
	// ErrMetaFetchURLRequired is returned when a [meta] block configures an
	// authenticated fetch but its trigger, fetch_url, is missing or blank.
	ErrMetaFetchURLRequired = errors.New("meta: fetch_url is required when any fetch_* key is present (it is the trigger; without it the authenticated download is silently skipped)")
	// ErrInvalidMetaFetchMethod is returned when meta.fetch_method holds
	// something other than the two HTTP methods the fetcher can build.
	ErrInvalidMetaFetchMethod = errors.New(`invalid meta.fetch_method: must be "post" (default) or "get"`)
	// ErrInvalidMetaFetchBody is returned when meta.fetch_body names an encoding
	// the request builder cannot produce.
	ErrInvalidMetaFetchBody = errors.New(`invalid meta.fetch_body: must be "form" (default) or "json"`)
	// ErrInvalidMetaFetchResponse is returned when meta.fetch_response names
	// something other than the two things the first reply can be.
	ErrInvalidMetaFetchResponse = errors.New(`invalid meta.fetch_response: must be "file" (default) or "url"`)
	// ErrUnknownMetaFetchKey is returned for a fetch_*-prefixed key the applier
	// does not read — almost always a misspelling of one of the twelve.
	ErrUnknownMetaFetchKey = errors.New("meta: unknown authenticated-fetch key")
)

// validateMetaFetch checks the fetch_* sub-schema hiding inside a package's
// free-form [meta] map. It lives beside parseAuthFetchSpec on purpose: these
// rules only stay correct while they mirror that consumer, and holding the two
// in different files is what let the schema go undocumented.
//
// The rules are deliberately NOT the parser's full requirement set. Once
// fetch_url is present the parser already fails loudly on a missing filename and
// on a half-declared serial (one of fetch_serial_env/fetch_serial_field without
// the other), so repeating those checks here would move an already-visible
// failure earlier at the price of letting one broken record block the whole
// registry load. What the parser cannot report is the silent
// case: a [meta] block whose trigger is missing or misspelled does not read as
// broken, it reads as "no authenticated fetch", and pkgdev is then sent to
// digest a distfile that exists on no public mirror.
func validateMetaFetch(pkg string, meta map[string]string) error {
	var present, unknown []string
	for k := range meta {
		if !strings.HasPrefix(k, metaFetchPrefix) {
			continue // annotation; [meta] stays free-form outside this namespace
		}
		present = append(present, k)
		if !slices.Contains(metaFetchKeys, k) {
			unknown = append(unknown, k)
		}
	}
	if len(present) == 0 {
		return nil
	}
	// Map iteration order is random, so sort before naming keys in a message:
	// an error whose text reshuffles between runs is unusable in a diff.
	slices.Sort(present)
	slices.Sort(unknown)

	// Reported first, and with every offending key at once: when the typo is in
	// fetch_url itself the missing-trigger rule below fires too, and "you wrote
	// fetch_ur1" is the actionable half of that pair.
	if len(unknown) > 0 {
		return fmt.Errorf("package %s: %w: %s (the applier reads only: %s)", pkg, ErrUnknownMetaFetchKey,
			strings.Join(unknown, ", "), strings.Join(metaFetchKeys, ", "))
	}

	// A blank fetch_url counts as absent, because that is exactly how
	// parseAuthFetchSpec tests the trigger (TrimSpace, then compare to ""): a
	// blanked-out URL disables the download just as silently as a missing one.
	if strings.TrimSpace(meta[metaFetchURL]) == "" {
		detail := "fetch_* keys present: " + strings.Join(present, ", ")
		if _, set := meta[metaFetchURL]; set {
			detail = "fetch_url is set but blank"
		}
		return fmt.Errorf("package %s: %w; %s", pkg, ErrMetaFetchURLRequired, detail)
	}

	// Mirror the parser exactly: it trims and lowercases before comparing, and
	// an empty value is the documented "post" default rather than an error.
	if method, ok := meta[metaFetchMethod]; ok {
		switch strings.ToLower(strings.TrimSpace(method)) {
		case "", "post", "get":
			// valid
		default:
			return fmt.Errorf("package %s: %w: got %q", pkg, ErrInvalidMetaFetchMethod, method)
		}
	}

	// The other two enums, on the same terms. They are validated here — unlike
	// the companions left to the parser — because they are the keys whose
	// MISREADING is silent: a fetch_response the parser never sees spelled
	// right is a download that writes the vendor's answer verbatim.
	for _, e := range []struct {
		key   string
		valid []string
		err   error
	}{
		{metaFetchBody, []string{"", fetchBodyForm, fetchBodyJSON}, ErrInvalidMetaFetchBody},
		{metaFetchResponse, []string{"", fetchResponseFile, fetchResponseURL}, ErrInvalidMetaFetchResponse},
	} {
		value, ok := meta[e.key]
		if !ok {
			continue
		}
		if !slices.Contains(e.valid, strings.ToLower(strings.TrimSpace(value))) {
			return fmt.Errorf("package %s: %w: got %q", pkg, e.err, value)
		}
	}

	return nil
}

// distfileMode is what a finished distfile is left as. Portage's own downloads
// land group- and world-readable in DISTDIR, and anything stricter cannot be
// read by the unprivileged uid that merges them; nothing here is a secret, so
// there is nothing to withhold.
const distfileMode = 0o644

// authFetchUserAgent is sent on every leg. It is one constant because the
// lookup, the form and the CDN download are one operation to the vendor, and a
// leg that identified itself differently would be the one that gets refused.
const authFetchUserAgent = "Mozilla/5.0 (X11; Linux x86_64) bentoo-autoupdate"

// authFetchTimeout bounds the authenticated download. The payload is a full
// binary distfile (tens of MB), so it gets a generous-but-finite budget.
const authFetchTimeout = 5 * time.Minute

// authFetchSpec is the parsed, validated description of one authenticated
// download, derived from a package's [meta] block.
type authFetchSpec struct {
	method      string     // "post" or "get"
	url         string     // endpoint (may contain {id})
	serialEnv   string     // env var name for the serial
	serialField string     // form field the serial goes into
	form        url.Values // static form fields (platform, submit button, ...)
	filename    string     // dest filename template (may contain {version})

	body        string // "form" or "json"
	response    string // "file" or "url"
	contentType string // required content type of the final response ("" = only the textual guard applies)
	minBytes    int64  // smallest believable finished file (0 = only the zero-byte guard applies)

	// The id lookup, for an endpoint whose path carries a per-release
	// identifier. Both are set or neither is; see parseAuthFetchSpec.
	idURL     string // where the id is published ({version} substituted)
	idPattern string // regex over that body, 1 capture group ({version} substituted, quoted)

	// formEnv maps a form field to the NAME of the variable holding its value.
	// The values themselves are resolved at fetch time and never live here.
	formEnv url.Values
	timeout time.Duration // whole-download budget; authFetchTimeout when unset
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
	// The serial is OPTIONAL, and it is optional as a PAIR.
	//
	// Not every gated download is gated by a serial: a vendor may hand the file
	// to whoever submits the form, and demanding a credential such an endpoint
	// never reads would keep the whole authenticated path out of reach of the
	// packages that need only the POST.
	//
	// Naming one half alone stays an error, because it describes a request that
	// cannot be built: an env var with no field to carry it, or a field with no
	// value to put in it. Both would silently submit the form WITHOUT the
	// credential it was configured to carry — and a gated endpoint answers that
	// with its login page, which is a body reaching writeBody, not an error.
	// Failing here names the half that is missing instead.
	switch {
	case spec.serialEnv == "" && spec.serialField != "":
		return nil, false, fmt.Errorf("%w: %s is set but %s is not — configure both or neither", ErrAuthFetchFailed, metaFetchSerialField, metaFetchSerialEnv)
	case spec.serialEnv != "" && spec.serialField == "":
		return nil, false, fmt.Errorf("%w: %s is set but %s is not — configure both or neither", ErrAuthFetchFailed, metaFetchSerialEnv, metaFetchSerialField)
	}
	if spec.filename == "" {
		return nil, false, fmt.Errorf("%w: %s is required", ErrAuthFetchFailed, metaFetchFilename)
	}

	form, err := url.ParseQuery(meta[metaFetchForm])
	if err != nil {
		return nil, false, fmt.Errorf("%w: invalid %s: %v", ErrAuthFetchFailed, metaFetchForm, err)
	}
	spec.form = form

	if err := spec.parseEncoding(meta); err != nil {
		return nil, false, err
	}
	if err := spec.parseFinalGuards(meta); err != nil {
		return nil, false, err
	}
	if err := spec.parseIDLookup(meta); err != nil {
		return nil, false, err
	}
	if err := spec.parseFormEnv(meta); err != nil {
		return nil, false, err
	}
	if err := spec.parseTimeout(meta); err != nil {
		return nil, false, err
	}

	return spec, true, nil
}

// parseEncoding reads fetch_body and fetch_response: how the request is encoded
// and what the first reply is.
//
// Both default to what every record written before they existed meant —
// urlencoded, and "the reply is the file" — so adding them changed no existing
// download.
func (s *authFetchSpec) parseEncoding(meta map[string]string) error {
	s.body = strings.ToLower(strings.TrimSpace(meta[metaFetchBody]))
	if s.body == "" {
		s.body = fetchBodyForm
	}
	if s.body != fetchBodyForm && s.body != fetchBodyJSON {
		return fmt.Errorf("%w: %s=%q must be %q (default) or %q", ErrAuthFetchFailed, metaFetchBody, s.body, fetchBodyForm, fetchBodyJSON)
	}

	s.response = strings.ToLower(strings.TrimSpace(meta[metaFetchResponse]))
	if s.response == "" {
		s.response = fetchResponseFile
	}
	if s.response != fetchResponseFile && s.response != fetchResponseURL {
		return fmt.Errorf("%w: %s=%q must be %q (default) or %q", ErrAuthFetchFailed, metaFetchResponse, s.response, fetchResponseFile, fetchResponseURL)
	}

	// A JSON object cannot hold one key twice, so a repeated field in
	// fetch_form has no JSON spelling. Refusing here is the difference between
	// an operator being told their config cannot be expressed and one value
	// being dropped on the way to the vendor, where the request simply fails
	// with whatever the vendor says about a missing field.
	if s.body == fetchBodyJSON {
		for k, vs := range s.form {
			if len(vs) > 1 {
				return fmt.Errorf("%w: %s=%q cannot encode %s: field %q is repeated %d times and a JSON object holds each key once",
					ErrAuthFetchFailed, metaFetchBody, fetchBodyJSON, metaFetchForm, k, len(vs))
			}
		}
	}
	return nil
}

// parseFinalGuards reads the two optional assertions about the FINISHED file:
// the content type it must carry and the size below which it cannot be real.
//
// Both are optional because most records need neither — the built-in textual
// guard already catches the common case. They exist for the record that KNOWS
// its answer is a 4 GB zip, where "it is not HTML" is a very low bar.
func (s *authFetchSpec) parseFinalGuards(meta map[string]string) error {
	s.contentType = strings.TrimSpace(meta[metaFetchContentType])

	raw := strings.TrimSpace(meta[metaFetchMinBytes])
	if raw == "" {
		return nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: invalid %s=%q: not a whole number of bytes", ErrAuthFetchFailed, metaFetchMinBytes, raw)
	}
	if n < 0 {
		return fmt.Errorf("%w: invalid %s=%d: a size cannot be negative", ErrAuthFetchFailed, metaFetchMinBytes, n)
	}
	s.minBytes = n
	return nil
}

// parseIDLookup reads the pair that resolves an endpoint whose path carries a
// per-release identifier.
//
// # Why a lookup exists at all
//
// Some vendors mint a fresh download id for every release, so the endpoint that
// serves 21.1 is not the one that serves 21.2. A fetch_url with that id baked in
// is correct exactly until the next bump, and then it quietly serves the
// PREVIOUS version's installer under the new version's file name — a Manifest
// computed against a file that is not what it claims to be. So the id is looked
// up at fetch time, from wherever the vendor publishes it.
//
// # Why both rules below are errors rather than defaults
//
// The two keys are a pair, for the same reason the serial keys are: one alone
// describes a lookup that cannot run. And the pair must be matched by an {id} in
// fetch_url — a lookup whose answer has nowhere to go is a request the operator
// believes is version-specific while it is not, which is precisely the failure
// the lookup was added to remove.
func (s *authFetchSpec) parseIDLookup(meta map[string]string) error {
	s.idURL = strings.TrimSpace(meta[metaFetchIDURL])
	s.idPattern = strings.TrimSpace(meta[metaFetchIDPattern])

	switch {
	case s.idURL == "" && s.idPattern != "":
		return fmt.Errorf("%w: %s is set but %s is not — configure both or neither", ErrAuthFetchFailed, metaFetchIDPattern, metaFetchIDURL)
	case s.idURL != "" && s.idPattern == "":
		return fmt.Errorf("%w: %s is set but %s is not — configure both or neither", ErrAuthFetchFailed, metaFetchIDURL, metaFetchIDPattern)
	}

	hasPlaceholder := strings.Contains(s.url, idPlaceholder)
	switch {
	case s.idURL == "" && hasPlaceholder:
		return fmt.Errorf("%w: %s contains %s but no %s/%s is configured to resolve it",
			ErrAuthFetchFailed, metaFetchURL, idPlaceholder, metaFetchIDURL, metaFetchIDPattern)
	case s.idURL != "" && !hasPlaceholder:
		return fmt.Errorf("%w: %s/%s resolve an id that %s never uses — put %s in the endpoint",
			ErrAuthFetchFailed, metaFetchIDURL, metaFetchIDPattern, metaFetchURL, idPlaceholder)
	case s.idURL == "":
		return nil
	}

	// The pattern is compiled for real at fetch time, once the version is known
	// and can be quoted into it. Compiling it HERE, against a stand-in version,
	// is what makes a malformed pattern a config error reported before any
	// request goes out rather than a failure in the middle of a sweep.
	probe, err := regexp.Compile(strings.ReplaceAll(s.idPattern, versionPlaceholder, "0"))
	if err != nil {
		return fmt.Errorf("%w: invalid %s: %v", ErrAuthFetchFailed, metaFetchIDPattern, err)
	}
	if got := probe.NumSubexp(); got != 1 {
		return fmt.Errorf("%w: %s must have exactly 1 capture group (the id), it has %d", ErrAuthFetchFailed, metaFetchIDPattern, got)
	}
	return nil
}

// parseFormEnv reads the fields whose values must NOT be written down.
//
// # Why this exists
//
// A vendor may gate a download behind a registration form rather than behind a
// credential, and such a form asks for a person: name, e-mail, telephone,
// address. fetch_form cannot carry those — it lives in packages.toml, which
// lives in the overlay, which is public — and yet without them the record
// describes a request that cannot be sent.
//
// So fetch_form_env states the field NAMES and, for each, the name of the
// variable holding its value. It is the serial pair generalised: the same
// "field here, variable there" shape, resolved through the same chain (env var,
// then the user secrets file, then the system one), so each operator sends
// their own details and the overlay records none of them.
func (s *authFetchSpec) parseFormEnv(meta map[string]string) error {
	raw := strings.TrimSpace(meta[metaFetchFormEnv])
	if raw == "" {
		return nil
	}
	fields, err := url.ParseQuery(raw)
	if err != nil {
		return fmt.Errorf("%w: invalid %s: %v", ErrAuthFetchFailed, metaFetchFormEnv, err)
	}

	// A GET puts every field in the query string, where it is written to the
	// vendor's access log, to every proxy in between, and to this machine's own
	// shell history if anyone reproduces the call. That is an acceptable place
	// for "platform=linux" and not for somebody's telephone number, so the
	// combination is refused rather than merely discouraged.
	if s.method == "get" {
		return fmt.Errorf("%w: %s cannot be used with %s=%q — the values would ride in the query string, where they are logged by every hop",
			ErrAuthFetchFailed, metaFetchFormEnv, metaFetchMethod, s.method)
	}

	for field, names := range fields {
		switch {
		case len(names) > 1:
			return fmt.Errorf("%w: %s names %d variables for the field %q; one field takes one value",
				ErrAuthFetchFailed, metaFetchFormEnv, len(names), field)
		case strings.TrimSpace(names[0]) == "":
			return fmt.Errorf("%w: %s gives the field %q an empty variable name", ErrAuthFetchFailed, metaFetchFormEnv, field)
		case s.form.Has(field):
			// Two sources for one field is not a precedence question worth
			// answering: whichever won, the operator would be reading the
			// other one in the record.
			return fmt.Errorf("%w: the field %q is set by both %s and %s — it must come from exactly one",
				ErrAuthFetchFailed, field, metaFetchForm, metaFetchFormEnv)
		case field == s.serialField:
			return fmt.Errorf("%w: the field %q is already the serial field (%s); do not repeat it in %s",
				ErrAuthFetchFailed, field, metaFetchSerialField, metaFetchFormEnv)
		}
		fields.Set(field, strings.TrimSpace(names[0]))
	}
	s.formEnv = fields
	return nil
}

// parseTimeout reads the whole-download budget.
//
// The 5-minute default was sized for a distfile of tens of megabytes. It is not
// a ceiling a 4 GiB archive can meet on an ordinary connection — 3.81 GiB needs
// better than 13 MB/s sustained to finish inside it — so a record that knows it
// downloads something that large can say so.
func (s *authFetchSpec) parseTimeout(meta map[string]string) error {
	s.timeout = authFetchTimeout

	raw := strings.TrimSpace(meta[metaFetchTimeout])
	if raw == "" {
		return nil
	}
	secs, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%w: invalid %s=%q: not a whole number of seconds", ErrAuthFetchFailed, metaFetchTimeout, raw)
	}
	if secs <= 0 {
		return fmt.Errorf("%w: invalid %s=%d: a budget must be positive (omit the key for the %s default)",
			ErrAuthFetchFailed, metaFetchTimeout, secs, authFetchTimeout)
	}
	s.timeout = time.Duration(secs) * time.Second
	return nil
}

// usesIDLookup reports whether the endpoint's id is resolved at fetch time. It
// tests the URL alone because parseIDLookup has already refused every spec
// where the keys and the placeholder do not agree.
func (s *authFetchSpec) usesIDLookup() bool { return s.idURL != "" }

// usesSerial reports whether this download carries a credential at all.
//
// It tests the env var alone because parseAuthFetchSpec has already refused
// every spec where exactly one half of the pair is set, so inside a parsed spec
// the two are set together or not at all. It exists as a method rather than as
// three inline comparisons so that the three sites that must agree — secret
// resolution, request building and the log line naming the provenance — cannot
// drift into disagreeing about what "no serial" means.
func (s *authFetchSpec) usesSerial() bool { return s.serialEnv != "" }

// resolvedFilename substitutes {version} in the filename template.
func (s *authFetchSpec) resolvedFilename(version string) string {
	return strings.ReplaceAll(s.filename, versionPlaceholder, version)
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

// minScrubLength is the shortest resolved value worth substituting out of a
// diagnostic.
//
// Scrubbing works by replacing a value wherever it appears, so a two-character
// one — a state abbreviation, an initial — would blank out unrelated fragments
// of the very URL or message somebody needs in order to act. Anything that
// short is also not distinctive enough to identify a person on its own, which
// is what the scrubbing is for. The serial is exempt from the question: a
// credential is redacted whatever its length.
const minScrubLength = 4

// authFetchCredentials holds everything one download resolved out of the
// secrets chain: the serial, when the record configures one, and the identity
// fields fetch_form_env names.
//
// It exists so the values travel together with the rule about how they may
// appear in text. Before it, one secret was threaded through four functions as
// a bare string and each of them called Scrub itself; adding a second kind of
// value would have meant remembering all four.
type authFetchCredentials struct {
	serial string     // "" when the record configures none
	fields url.Values // resolved fetch_form_env values, by field name
	redact []string   // every value above that is long enough to substitute
}

// scrub removes every resolved value from a message.
func (c authFetchCredentials) scrub(msg string) string {
	for _, v := range c.redact {
		msg = secrets.Scrub(msg, v)
	}
	return msg
}

// resolveCredentials looks up the serial and every fetch_form_env value.
//
// It resolves ALL of them before the first byte goes out, so a record missing
// one variable fails naming it rather than halfway through a submission the
// vendor has already recorded.
func (s *authFetchSpec) resolveCredentials() (authFetchCredentials, error) {
	creds := authFetchCredentials{fields: url.Values{}}

	if s.usesSerial() {
		v, err := resolveSecret(s.serialEnv)
		if err != nil {
			return authFetchCredentials{}, err
		}
		creds.serial = v
		// Unconditionally, unlike the fields below: a credential is redacted
		// whatever its length.
		creds.redact = append(creds.redact, v)
	}

	for field, names := range s.formEnv {
		v, err := resolveSecret(names[0])
		if err != nil {
			return authFetchCredentials{}, fmt.Errorf("%w (%s names it for the %q field)", err, metaFetchFormEnv, field)
		}
		creds.fields.Set(field, v)
		if len(v) >= minScrubLength {
			creds.redact = append(creds.redact, v)
		}
	}
	return creds, nil
}

// fetchDistfile resolves the serial, submits the form, and writes the finished
// file into destDir under the resolved filename (which must match the basename
// of the ebuild's SRC_URI so pkgdev digests it). It returns the written path.
//
// # The two shapes a gated download takes
//
// With fetch_response = "file" (the default) the reply to the form IS the
// distfile, and it is streamed straight to disk.
//
// With fetch_response = "url" the reply is a short text body holding the URL of
// a CDN the vendor signed for this request; the file is fetched from there. The
// difference is not cosmetic: measured against Blackmagic's endpoint on
// 2026-09-19, the first reply is 492 bytes of text/plain holding a URL whose
// signature expires in three hours. Written to disk as-is, that is a 492-byte
// "DaVinci_Resolve_21.1_Linux.zip" — a file pkgdev digests without complaint,
// producing a green Manifest for a distfile that is a sentence.
//
// Failure modes are mapped to clear errors, and the serial is scrubbed from any
// message that could echo it (notably transport errors on the GET path, where
// the serial rides in the query string).
func (s *authFetchSpec) fetchDistfile(ctx context.Context, version, destDir string) (string, error) {
	creds, err := s.resolveCredentials()
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

	endpoint := s.url
	if s.usesIDLookup() {
		id, err := s.resolveEndpointID(ctx, version)
		if err != nil {
			return "", err
		}
		endpoint = strings.ReplaceAll(endpoint, idPlaceholder, id)
	}

	req, err := s.buildRequest(ctx, endpoint, creds)
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: s.timeout, CheckRedirect: refuseMethodDowngrade}
	// Close the keep-alive connection once we are done: this is a one-shot
	// download, so a pooled idle connection would otherwise outlive the call
	// (and trip goroutine-leak detection in tests).
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: request failed: %v", ErrAuthFetchFailed, creds.scrub(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: server returned HTTP %d", ErrAuthFetchFailed, resp.StatusCode)
	}

	// The second leg, when the first reply only NAMES the file. It returns a
	// live response whose body is the download, and which this function owns
	// from here on — hence the second deferred close.
	if s.response == fetchResponseURL {
		next, err := s.followDownloadURL(ctx, resp, creds)
		if err != nil {
			return "", err
		}
		defer func() { _ = next.Body.Close() }()
		resp = next
	}

	if err := s.guardFinalResponse(resp); err != nil {
		return "", err
	}

	written, err := writeBody(destDir, destPath, resp.Body, creds, s.minBytes)
	if err != nil {
		return "", err
	}
	return written, nil
}

// buildRequest constructs the POST (or GET) carrying the static form fields plus
// the serial in its configured field.
//
// The endpoint is a parameter rather than s.url because it may have had its
// {id} resolved moments earlier; passing it in keeps the place that substitutes
// and the place that requests from disagreeing.
func (s *authFetchSpec) buildRequest(ctx context.Context, endpoint string, creds authFetchCredentials) (*http.Request, error) {
	body := url.Values{}
	for k, vs := range s.form {
		for _, v := range vs {
			body.Add(k, v)
		}
	}
	// A spec with no serial submits the form exactly as configured. Setting the
	// field unconditionally would post a pair under the empty name ("=") — one
	// the endpoint never asked for, carrying a value that was never resolved.
	if s.usesSerial() {
		body.Set(s.serialField, creds.serial)
	}
	// The fields whose values came from the secrets chain. parseFormEnv has
	// already refused every collision, so none of these overwrites a field the
	// record spells out.
	for field, vs := range creds.fields {
		body.Set(field, vs[0])
	}

	var (
		req *http.Request
		err error
	)
	switch {
	case s.method == "post" && s.body == fetchBodyJSON:
		var encoded []byte
		if encoded, err = jsonForm(body); err == nil {
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
			if err == nil {
				// The charset is spelled out because it is what the vendor's own
				// page sends (the AngularJS default), so an API that sniffs this
				// header rather than parsing it has one fewer reason to differ.
				req.Header.Set("Content-Type", "application/json;charset=utf-8")
			}
		}
	case s.method == "post":
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	case s.method == "get":
		sep := "?"
		if strings.Contains(endpoint, "?") {
			sep = "&"
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint+sep+body.Encode(), nil)
	default:
		return nil, fmt.Errorf("%w: unsupported method %q", ErrAuthFetchFailed, s.method)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: building request: %v", ErrAuthFetchFailed, creds.scrub(err.Error()))
	}
	// Some vendor endpoints reject empty/bot User-Agents.
	req.Header.Set("User-Agent", authFetchUserAgent)
	return req, nil
}

// jsonForm renders the same field set buildRequest assembled as a JSON object.
//
// # Why only booleans are typed
//
// Every value in fetch_form arrives as text, and JSON distinguishes "true" from
// true. The one API this was written against needs a real boolean for its
// privacy-policy field and strings for everything else — including a postcode,
// which a "looks numeric, emit a number" rule would have turned into a number
// and, for a postcode with a leading zero, into a different postcode.
//
// So the rule is deliberately the smallest one that works: exactly the two
// literals true and false become booleans, everything else stays a string. It is
// documented beside the key in the README, because a coercion nobody can predict
// produces a request nobody can explain.
func jsonForm(fields url.Values) ([]byte, error) {
	obj := make(map[string]any, len(fields))
	for k, vs := range fields {
		// parseEncoding has already refused a repeated key in JSON mode, so
		// there is exactly one value here.
		switch v := vs[0]; v {
		case "true":
			obj[k] = true
		case "false":
			obj[k] = false
		default:
			obj[k] = v
		}
	}
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding %s as %s: %v", ErrAuthFetchFailed, metaFetchForm, fetchBodyJSON, err)
	}
	return encoded, nil
}

// writeBody streams the response into a temp file in destDir, then atomically
// renames it into place. A body shorter than minBytes — zero always, plus
// whatever fetch_min_bytes states — is rejected. The temp file is removed on any
// failure so a partial download never lands in the distdir.
func writeBody(destDir, destPath string, body io.Reader, creds authFetchCredentials, minBytes int64) (string, error) {
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
		return "", fmt.Errorf("%w: writing body: %v", ErrAuthFetchFailed, creds.scrub(copyErr.Error()))
	case closeErr != nil:
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: closing temp file: %v", ErrAuthFetchFailed, closeErr)
	case n == 0:
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: downloaded zero bytes", ErrAuthFetchFailed)
	case n < minBytes:
		// Judged on what was actually WRITTEN, not on Content-Length: a
		// truncated transfer that advertised the right size fails here and
		// nowhere else.
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: downloaded %d bytes, below the %s of %d — the response is not the file it claims to be",
			ErrAuthFetchFailed, n, metaFetchMinBytes, minBytes)
	}

	// os.CreateTemp makes the file 0600, and the rename preserves it. That was
	// invisible while the only caller was the sweep, which downloads into a
	// private distdir it owns — but this also writes into the HOST's DISTDIR
	// now, and the process that reads a distfile at merge time is not the one
	// that fetched it: under FEATURES="userfetch userpriv", which running
	// `ebuild` as root switches on, Portage reads as uid `portage`. A 0600
	// distfile is unreadable to it, and the merge fails on a file that is
	// present, complete and digest-correct. See portage_access.go for the same
	// lesson learned on the staged tree.
	//
	// The mode is set on the TEMP file, before the rename, so the name a
	// concurrent reader can see never exists with the wrong bits.
	if err := os.Chmod(tmpName, distfileMode); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: making %s readable: %v", ErrAuthFetchFailed, filepath.Base(destPath), err)
	}

	if err := os.Rename(tmpName, destPath); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("%w: finalizing %s: %v", ErrAuthFetchFailed, filepath.Base(destPath), err)
	}
	return destPath, nil
}
