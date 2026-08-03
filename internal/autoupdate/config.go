// Package autoupdate provides configuration management for ebuild autoupdate.
package autoupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// Error variables for configuration errors
var (
	// ErrPackagesConfigNotFound is returned when packages.toml is not found in the overlay
	ErrPackagesConfigNotFound = errors.New("packages.toml not found in overlay")
	// ErrInvalidParserType is returned when an invalid parser type is specified
	ErrInvalidParserType = errors.New("invalid parser type: must be 'json', 'regex', 'html', or 'script'")
	// ErrMissingURL is returned when a package configuration is missing the required URL field
	ErrMissingURL = errors.New("missing required field: url")
	// ErrMissingParser is returned when a package configuration is missing the required parser field
	ErrMissingParser = errors.New("missing required field: parser")
	// ErrMissingPath is returned when a JSON parser is missing the required path field
	ErrMissingPath = errors.New("missing required field: path (required for json parser)")
	// ErrMissingPattern is returned when a regex parser is missing the required pattern field
	ErrMissingPattern = errors.New("missing required field: pattern (required for regex parser)")
	// ErrMissingSelectorOrXPath is returned when an HTML parser is missing both selector and xpath fields
	ErrMissingSelectorOrXPath = errors.New("missing required field: selector or xpath (required for html parser)")
	// ErrMissingScript is returned when a script parser is missing the required script field
	ErrMissingScript = errors.New("missing required field: script (required for script parser)")
	// ErrInvalidSelect is returned when the select field has an unsupported value
	ErrInvalidSelect = errors.New("invalid select value: must be '', 'first', 'max', or 'last'")
	// ErrInvalidType is returned when the type field has an unsupported value
	ErrInvalidType = errors.New("invalid type value: must be '', 'bin', or 'source'")
	// ErrInvalidPackageKey is returned when a packages.toml key is not a
	// well-formed "category/package" atom, with or without a ":slot" suffix.
	ErrInvalidPackageKey = errors.New("invalid package key: want category/package or category/package:slot")
	// ErrInvalidSuffix is returned when the suffix field is not one of the Gentoo
	// version suffixes, optionally numbered (_alpha, _beta2, _pre, _rc1, _p).
	ErrInvalidSuffix = errors.New("invalid suffix: must be _alpha, _beta, _pre, _rc or _p, optionally numbered")
	// ErrSuffixWhenWithoutSuffix is returned when suffix_when is set but suffix is
	// not: the condition has nothing to gate.
	ErrSuffixWhenWithoutSuffix = errors.New("suffix_when requires suffix")
	// ErrInvalidVersion is returned when the version field is not a well-formed
	// Gentoo version string (an -rN revision suffix is allowed).
	ErrInvalidVersion = errors.New("invalid version: must be a well-formed Gentoo version, optionally with an -rN revision")
	// ErrVersionOutsideSeries is returned when version and series are both set
	// but the pinned version does not match the series regex: such an entry
	// claims an ebuild it can never select.
	ErrVersionOutsideSeries = errors.New("version does not match series")
)

// validSuffixRegex matches the value accepted by PackageConfig.Suffix: one of
// the Gentoo pre-release/patch markers, with an optional number (_rc, _rc1,
// _beta2). It deliberately allows a single suffix only — a stacked value like
// "_pre_p1" is a version string, not a channel marker.
var validSuffixRegex = regexp.MustCompile(`^_(alpha|beta|pre|rc|p)[0-9]*$`)

// PackageConfig represents a single package's autoupdate configuration.
// It defines how to check upstream versions for a specific package.
type PackageConfig struct {
	// Enabled toggles whether the autoupdate checker processes this package.
	// A nil/absent value means enabled (the default), so existing entries need
	// no migration. Set enabled = false to silently skip the package — no
	// fetch, absent from progress and totals — without deleting its config
	// (e.g. an orphaned entry whose ebuild was removed from the overlay).
	// A pointer distinguishes "absent" (enabled) from an explicit false.
	Enabled *bool `toml:"enabled,omitempty"`
	// Hold, when true, deliberately excludes the package from autoupdate even
	// though its ebuild IS present in the overlay. It expresses an explicit
	// maintainer decision ("present, but do not auto-bump"), distinct from the
	// overlay-driven enabled flag: enabled = false is bookkeeping the checker
	// sets automatically when an ebuild vanishes and clears again when it
	// reappears (the overlay is the source of truth), whereas hold is never
	// auto-flipped by that reconciliation. Use it for a package whose bump needs
	// manual work each release (e.g. sci-ml/ollama, whose llama.cpp FetchContent
	// rearch needs a manual patchset/distfile per bump). A held package is not
	// fetched, not added to pending, and absent from progress and totals.
	Hold bool `toml:"hold,omitempty"`
	// URL is the primary URL to query for version information
	URL string `toml:"url"`
	// Parser specifies the parser type: "json", "regex", or "html"
	Parser string `toml:"parser"`
	// Path is the JSON path for extracting version (used with json parser)
	Path string `toml:"path,omitempty"`
	// Pattern is the regex pattern with capture group (used with regex parser)
	Pattern string `toml:"pattern,omitempty"`
	// Binary indicates if this is a binary package (manifest-only testing)
	Binary bool `toml:"binary,omitempty"`
	// Type classifies the package as binary ("bin") or source-built
	// ("source"). Empty means auto-detect from the ebuild (RESTRICT=bindist,
	// a -bin suffix, or a binary SRC_URI). Set it only to override/correct the
	// heuristic. Used for reporting and the --only filter; it does not change
	// apply/compile behavior.
	Type string `toml:"type,omitempty"`
	// FallbackURL is an alternative URL to try if primary fails
	FallbackURL string `toml:"fallback_url,omitempty"`
	// FallbackParser is the parser type for the fallback URL
	FallbackParser string `toml:"fallback_parser,omitempty"`
	// FallbackPattern is the pattern for the fallback parser
	FallbackPattern string `toml:"fallback_pattern,omitempty"`
	// LLMPrompt is the prompt to use for LLM-based version extraction
	LLMPrompt string `toml:"llm_prompt,omitempty"`

	// New fields for HTML parser
	// Selector is the CSS selector for extracting version (used with html parser)
	Selector string `toml:"selector,omitempty"`
	// XPath is the XPath expression for extracting version (used with html parser)
	XPath string `toml:"xpath,omitempty"`

	// New fields for authentication
	// Headers contains custom HTTP headers to send with requests
	Headers map[string]string `toml:"headers,omitempty"`

	// Timeout overrides the per-operation budget (in seconds) for THIS package,
	// i.e. the total time the checker spends fetching its version across all retry
	// attempts. Use it for hosts that are reliably slow (e.g. salsa.debian.org,
	// sources.debian.org) so they get extra retry headroom without slowing the
	// whole batch. Zero/absent means use the global budget derived from
	// autoupdate.http_timeout. The per-request cap stays the global value; if a
	// single response itself needs longer than that cap, raise autoupdate.http_timeout
	// (or pass --timeout) instead.
	Timeout int `toml:"timeout,omitempty"`

	// Meta holds free-form key/value annotations for packages with special
	// acquisition requirements (e.g. a purchased serial, a platform selector,
	// a download endpoint). It is documentation only — the checker ignores it
	// when detecting versions. Never store secrets here; reference an env var
	// instead (e.g. serial_env = "FILEZILLA_PRO_KEY").
	Meta map[string]string `toml:"meta,omitempty"`

	// New fields for version history
	// VersionsPath is the JSON path for extracting version list
	VersionsPath string `toml:"versions_path,omitempty"`
	// VersionsSelector is the CSS selector for extracting version list
	VersionsSelector string `toml:"versions_selector,omitempty"`

	// Transform applies ordered regex substitutions to the extracted version,
	// e.g. [["-", "."]] turns "7.1.2-24" into "7.1.2.24". Each rule is
	// [regex, repl]; repl follows regexp.ReplaceAllString semantics ($1 etc.).
	// Rules run in order, before selection and before the Gentoo comparison.
	Transform [][]string `toml:"transform,omitempty"`
	// Select chooses which match to return when several are present.
	// "" / "first" = current behavior; "max" = highest Gentoo version;
	// "last" = last match. Requires a parser that can extract a list
	// (json/regex/html); ignored by the "script" parser.
	Select string `toml:"select,omitempty"`

	// Suffix is the Gentoo version suffix (_alpha, _beta, _pre, _rc, _p, each
	// optionally numbered) appended to the detected version, declaring that what
	// this record extracts is a pre-release rather than a final release.
	//
	// It exists because upstream numbering rarely says so itself: LibreOffice
	// publishes 26.8.0.1 in its testing channel with a version string
	// indistinguishable from a stable one, so the bare value would land in the
	// overlay as a finished release. The suffix restores the truth AND the
	// ordering — Gentoo sorts _pre below the bare version, so 26.8.0.1_pre stays
	// older than the eventual 26.8.0.1, and the bump fires exactly when upstream
	// promotes the release.
	//
	// The suffix is applied after transform and before validation/comparison, so
	// candidates are already suffixed when select = "max" orders them. It does
	// not apply to track = "commit", whose _p<date>/_pre<date> snapshot suffix is
	// derived from the current ebuild instead.
	//
	// Ebuilds must be able to strip it when building SRC_URI — the LibreOffice
	// ebuild's MY_PV="${MY_PV/_pre/}" is the canonical shape.
	Suffix string `toml:"suffix,omitempty"`

	// SuffixWhen is a regex gating Suffix: the suffix is appended only to a
	// detected version that matches it. Absent (the common case) means the suffix
	// applies unconditionally, which is right when the probed URL *is* the
	// pre-release channel.
	//
	// It is for the opposite case: one endpoint listing several release lines at
	// once. The LibreOffice archive index carries both the stable 26.2 line and
	// the testing 26.8 one, and select = "max" always returns the latter, so
	// suffix_when = '^26\.8\.' marks that line — and only it — as _pre. Such a
	// record needs an edit when the line is promoted to stable (drop the field,
	// or point it at the next development line); the doc comment must say so.
	SuffixWhen string `toml:"suffix_when,omitempty"`

	// Series restricts the entry to one release line, given as a regex matched
	// against the version. It narrows BOTH ends of the comparison: which ebuild
	// in the overlay counts as this entry's current version, and which upstream
	// candidates survive selection.
	//
	// It exists because an overlay routinely carries more than one ebuild per
	// package, and one entry cannot track them all — selectCurrentEbuild takes
	// the directory's highest version, so the other lines are never bumped. The
	// ":slot" key suffix already solves that when the lines are separate SLOTs
	// (net-libs/webkit-gtk). Series solves the other half: lines that share a
	// SLOT and differ by version. app-office/libreoffice keeps the stable 26.2
	// series beside the testing 26.8 one, both SLOT=0; app-editors/zed-bin keeps
	// 1.13.1 stable beside 1.14.1_pre, likewise.
	//
	// zed-bin is what the absence of this costs. Its entry tracks the stable
	// channel, but the scan returns 1.14.1_pre as "current", so every stable
	// release below 1.14.1 compares older and reports "up to date": the line
	// stops being updated, and the silence looks like success.
	//
	// A package with one line does not need it. With it, give each entry of the
	// same package a distinct "@label" so the keys stay unique
	// ("app-office/libreoffice@stable" / "@testing"); the label is identity only
	// and never reaches a filesystem path.
	Series string `toml:"series,omitempty"`

	// Script is a JS expression/IIFE evaluated against the live DOM by the
	// "script" parser; its string result is the version. Inline, or "@file.js"
	// to load from .autoupdate/scripts/<file>.
	Script string `toml:"script,omitempty"`

	// Track specifies the tracking mode.
	// "" (default) = semver tag comparison.
	// "commit"     = compare commit dates on a branch; the date extracted via
	//                path/transform becomes the _pDATE suffix of the new version
	//                (base version is taken from the current ebuild by stripping
	//                the existing _p<date> suffix). CommitSHAPath must also be
	//                set so the applier can substitute the commit hash in the ebuild.
	Track string `toml:"track,omitempty"`

	// CommitSHAPath is the JSON path to extract the commit SHA from the same
	// response as the date (used with track = "commit"). The SHA is stored in
	// PendingUpdate.CommitHash and substituted into the copied ebuild at apply time.
	CommitSHAPath string `toml:"commit_sha_path,omitempty"`

	// CommitMessagePath is the JSON path, relative to each commit array element,
	// that yields the commit title/message string (used with track = "commit"
	// and commit_version_pattern). Typical values:
	//   "commit.message"  — GitHub commits list API
	//   "title"           — GitLab repository/commits API
	CommitMessagePath string `toml:"commit_message_path,omitempty"`

	// CommitVersionPattern is a regex with one capture group applied to each
	// commit title (at commit_message_path) to detect a base-version change
	// between tags (used with track = "commit"). When a commit title matches
	// and the captured version is newer than the current base, the new base
	// replaces the old one in the generated ebuild version (e.g.
	// "1.4.352_p20260515" → "1.4.353_p<today>" when the match is "1.4.353").
	CommitVersionPattern string `toml:"commit_version_pattern,omitempty"`

	// BaseFrom declares WHERE the base version of a track = "commit" package
	// comes from — the X.Y.Z that carries the _p<date>/_pre<date> snapshot
	// suffix. Absent means the legacy behaviour: the base is whatever the current
	// ebuild already has, optionally raised by commit_version_pattern.
	//
	// It exists because scanning commit titles is the weakest of the three ways
	// upstreams announce a version, and it was the only one available. The fetch
	// reads a fixed window of the most recent commits (per_page= in the URL), so
	// the pattern only ever sees what fits in it — and the window is measured in
	// COMMITS, not days. Measured on 2026-07-31: 50 commits cover ten months of
	// Vulkan-Headers but 1.3 days of zed, whose "Bump Zed to v1.15.0" had already
	// fallen to index 59 and become invisible. Worse, six of the seven registry
	// entries carrying a commit_version_pattern matched nothing at all — the
	// pattern had been copied between Khronos packages, but only Vulkan-Headers
	// writes "Update for Vulkan-Docs X.Y.Z" in its commits. The bases froze up to
	// seven releases behind while the _p<date> kept advancing, so the versions
	// looked alive and were not.
	//
	// Values:
	//
	//	"file"           — fetch base_url and apply base_pattern. The strongest
	//	                   option: one request, no window, no pagination. Use it
	//	                   whenever upstream versions itself in-tree (zed's
	//	                   crates/zed/Cargo.toml, mesa's VERSION, libqmi's
	//	                   meson.build, sqlitebrowser's CMakeLists.txt).
	//	"tag"            — fetch base_url (a tag/ref listing) and take the highest
	//	                   version whose tag name matches base_tag_pattern. For
	//	                   upstreams that mark releases with a tag and say nothing
	//	                   in-tree about the scheme the ebuild uses: glslang and
	//	                   spirv-* version themselves as "2026.3"/"1.5.5" in their
	//	                   own files, while the overlay tracks them on the
	//	                   vulkan-sdk-X.Y.Z.W scheme that exists only as tags.
	//	"commit_message" — the legacy scan, driven by commit_version_pattern +
	//	                   commit_message_path. Correct only when upstream marks
	//	                   releases in commit titles AND commits slowly enough that
	//	                   the bump stays inside the window.
	//
	// Whichever is chosen, an unresolvable base is now an ERROR rather than a
	// silent fallback to the ebuild's version: a frozen base is indistinguishable
	// from a correct one at a glance, which is exactly how the seven-release drift
	// went unnoticed. Say where the version lives, or do not declare a source.
	//
	// The source must match the scheme the EBUILD uses, not whatever upstream
	// finds prettiest. dev-util/spirv-tools publishes "v2026.3" in its CHANGES
	// file, but the overlay versions it on the vulkan-sdk scheme — for that
	// package the file is the wrong source even though it parses cleanly.
	BaseFrom string `toml:"base_from,omitempty"`

	// BaseURL is the endpoint fetched to resolve the base version when
	// base_from = "file". Point it at the raw file, not at an API wrapper:
	// "https://raw.githubusercontent.com/<owner>/<repo>/<branch>/<path>" for
	// GitHub, "https://<host>/<project>/-/raw/<branch>/<path>" for GitLab.
	BaseURL string `toml:"base_url,omitempty"`

	// BasePattern is a regex with ONE capture group applied to the body fetched
	// from base_url; the captured text is the base version. Anchor it enough to
	// survive the rest of the file — a bare `VERSION ([0-9.]+)` matches CMake's
	// own `cmake_minimum_required(VERSION 3.22.1)` long before it reaches the
	// project's version, which is how a naive probe reported Vulkan-Headers as
	// "3.22.1". Use a TOML literal string ('…'), same as every other regex field.
	//
	// The capture may carry a suffix the ebuild does not want; strip it in the
	// regex rather than post-processing (mesa's VERSION file holds
	// "26.3.0-devel", so '^([0-9][0-9.]*)-devel' captures exactly "26.3.0").
	//
	// Go's ^ and $ anchor to the whole body unless the pattern opens with (?m).
	// A version declared mid-file therefore needs it — zed's Cargo.toml wants
	// '(?m)^version = "([0-9][0-9.]*)"', and the same pattern without (?m)
	// matches nothing at all. Note too that $ does not match before a trailing
	// newline the way Perl's does.
	BasePattern string `toml:"base_pattern,omitempty"`

	// BaseTagPattern is a regex with ONE capture group matched against each tag
	// name returned by base_url, used with base_from = "tag". The highest
	// captured version becomes the base.
	//
	// Filtering by family is the whole job, not a refinement. These repos carry
	// several tag families at once — Vulkan-Loader alone has v*, sdk-*,
	// vulkan-sdk-* and windows-rt-*, and Vulkan-ValidationLayers adds
	// snapshot-2026wk*. Ranking tags without a family filter picks nonsense:
	// measured on 2026-07-31, an unfiltered scan chose "khronos-master-20141209"
	// for vulkan-loader and "benchmark-m4" for zed, both purely because they
	// contain large numbers.
	//
	// Anchor it. 'vulkan-sdk-([0-9.]+)' is right; '([0-9.]+)' would match inside
	// every other family's names too.
	BaseTagPattern string `toml:"base_tag_pattern,omitempty"`

	// AuxVar is the name of a free-text auxiliary variable in the ebuild to keep
	// in sync with upstream (e.g. "MY_BUILD" for betterbird's esr-bbNN tag, or
	// "MY_P" for nomachine's build-numbered tarball). Unlike commit_sha_path it
	// is parser-agnostic (regex/html welcome) and the captured value is free text,
	// not a 40-hex SHA. Set together with aux_pattern. The captured value is
	// stored in PendingUpdate.AuxValue and substituted into the copied ebuild at
	// apply time.
	AuxVar string `toml:"aux_var,omitempty"`

	// AuxPattern is a regex with one capture group, applied to the SAME response
	// body used for version detection, that yields the value for AuxVar. Set
	// together with aux_var.
	AuxPattern string `toml:"aux_pattern,omitempty"`

	// Revision is the -rN suffix to attach to the PV of a freshly bumped ebuild.
	// It exists for packages that ship several SLOTs out of one directory and use
	// the revision to tell them apart, which is how ::gentoo handles
	// net-libs/webkit-gtk: -r410/-r411 are SLOT 4.1 and -r600/-r601 are SLOT 6,
	// all sharing the same PV series. Such an entry is keyed with a ":slot"
	// suffix (see PackagesConfig) and declares the slot's BASE revision here.
	//
	// It must be declared rather than carried over from the source ebuild: on a
	// PV change the revision resets (foo-1.2.3-r1 bumps to foo-1.2.4), and where
	// a revision marks a slot the value to write is the slot's base — ::gentoo
	// bumps webkit-gtk-2.52.3-r411 to webkit-gtk-2.52.5-r410, not to -r411.
	//
	// Zero/absent means a plain PV with no revision, which is correct for every
	// ordinary package. It is NOT the right answer for a slot that ::gentoo
	// revisions, even when the overlay's own ebuild happens to carry a bare PV:
	// a bare PV sorts BELOW every -rN, so portage picks ::gentoo's ebuild over
	// the overlay's and whatever divergence the overlay carries stops being
	// selected at all. The bentoo overlay hit exactly that with SLOT 6
	// webkit-gtk — a bare webkit-gtk-2.52.5.ebuild losing to ::gentoo's -r600,
	// taking its non-upstream USE=webdriver with it — and that entry now
	// declares revision = 600.
	Revision int `toml:"revision,omitempty"`

	// Version is the ebuild version this entry keeps in the overlay — the pin
	// the --clean sweep preserves — not the upstream version being tracked. It
	// includes the -rN revision suffix when the ebuild carries one
	// ("2.52.4-r411"). Absent means "derive it through selectCurrentEbuild",
	// so every pre-existing record loads unchanged.
	Version string `toml:"version,omitempty"`

	// Comments is the record's documentation: why this source and parser, and
	// every caveat a future bump must know (stale endpoints, pre-release traps,
	// hand-edited ebuild variables). It is the LAST field of every record, and
	// the record ends with a `# END` marker on the following line.
	//
	// It is a field rather than a `#` comment for two reasons. First, comments do
	// not survive a rewrite: savePackagesConfig re-encodes the whole file through
	// toml.Encoder, so a single `overlay analyze --save` used to erase every doc
	// comment in the registry — as a field the text is data and comes back out.
	// Second, it gives the documentation an owner: it belongs to a record instead
	// of floating between two of them, where nothing says which one it describes.
	//
	// Write it as a TOML multi-line basic string ("""…""") starting with the
	// package name. Keep `[` off the start of any line: the raw-text surgery in
	// setPackagesEnabled scans for `[section]` headers and a line that looks like
	// one would end the record early.
	Comments string `toml:"comments,omitempty"`
}

// IsEnabled reports whether the checker should process this package. An absent
// (nil) enabled field counts as enabled, so the default — and every legacy
// entry that predates the field — is processed; only an explicit
// enabled = false skips it.
func (c *PackageConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// IsHeld reports whether the maintainer has explicitly held the package out of
// autoupdate (hold = true). A held package is skipped by CheckAll exactly like a
// disabled one, but — unlike enabled — the overlay-driven status reconciliation
// never clears the flag: it is a deliberate "present, but do not auto-bump"
// decision that survives the ebuild's presence in the overlay.
func (c *PackageConfig) IsHeld() bool {
	return c.Hold
}

// PackagesConfig represents the entire packages.toml configuration file.
// The keys in the map are package names in "category/package" format, optionally
// suffixed with ":slot" ("net-libs/webkit-gtk:4.1") for a package that ships
// several SLOTs out of one directory and needs one entry — and one independent
// pending/cache record — per slot. The suffix is part of the key's identity but
// never part of a filesystem path; use splitPkgAtom to build paths from a key.
type PackagesConfig struct {
	Packages map[string]PackageConfig `toml:"packages"`
}

// packagesConfigFile is the internal representation matching the TOML structure
// where each [category/package] section is a top-level key
type packagesConfigFile map[string]PackageConfig

// LoadPackagesConfig loads and parses packages.toml from the overlay.
// The configuration file is expected at overlay/.autoupdate/packages.toml
func LoadPackagesConfig(overlayPath string) (*PackagesConfig, error) {
	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")

	// Check if file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, ErrPackagesConfigNotFound
	}

	// Read and parse the TOML file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read packages.toml: %w", err)
	}

	// Parse TOML into the internal structure
	var fileConfig packagesConfigFile
	if err := toml.Unmarshal(data, &fileConfig); err != nil {
		return nil, fmt.Errorf("failed to parse packages.toml: %w", err)
	}

	// Convert to PackagesConfig
	config := &PackagesConfig{
		Packages: make(map[string]PackageConfig),
	}
	for pkg, cfg := range fileConfig {
		config.Packages[pkg] = cfg
	}

	return config, nil
}

// tomlTableName returns the table name of a TOML section header line and true
// when the line is a standard `[name]` header. It tolerates surrounding
// whitespace and a trailing inline comment, strips one layer of basic (") or
// literal (') quotes from the name, and reports false for array-of-table
// headers (`[[name]]`), comments, array continuation lines (`["-", "."],`), and
// anything else. The strictness — requiring only whitespace or a comment after
// the closing bracket — keeps multi-line array values from being mistaken for
// section headers during the surgical edit in DisablePackagesInConfig.
func tomlTableName(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") || strings.HasPrefix(t, "[[") {
		return "", false
	}
	end := strings.IndexByte(t, ']')
	if end < 0 {
		return "", false
	}
	// Whatever follows the closing bracket must be empty or a comment, else
	// this is not a header (e.g. an array element line `["-", "."],`).
	if rest := strings.TrimSpace(t[end+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
		return "", false
	}
	inner := strings.TrimSpace(t[1:end])
	if len(inner) >= 2 {
		if (inner[0] == '"' && inner[len(inner)-1] == '"') ||
			(inner[0] == '\'' && inner[len(inner)-1] == '\'') {
			inner = inner[1 : len(inner)-1]
		}
	}
	if inner == "" {
		return "", false
	}
	return inner, true
}

// DisablePackagesInConfig sets `enabled = false` for each named package in the
// overlay's packages.toml, editing the raw text so comments, ordering, and
// formatting survive — unlike a full re-encode (toml.Encoder), which would drop
// every comment in the hand-maintained file. For each package it locates the
// [section] whose table name equals the package and either rewrites an existing
// `enabled = ...` assignment or inserts `enabled = false` immediately after the
// header. Packages whose section is absent are skipped silently. The write is
// atomic (temp file + rename) and preserves the original file mode; an empty
// package list, or a run that changes nothing, leaves the file untouched.
func DisablePackagesInConfig(overlayPath string, pkgs []string) error {
	return setPackagesEnabled(overlayPath, pkgs, false, true)
}

// EnablePackagesInConfig sets `enabled = true` for each named package in the
// overlay's packages.toml, editing the raw text so comments, ordering, and
// formatting survive — the sibling of DisablePackagesInConfig used to revive an
// orphaned entry whose ebuild has reappeared (or whose upstream has overtaken
// ::gentoo). For each package it locates the [section] whose table name equals
// the package and rewrites an existing `enabled = ...` assignment to
// `enabled = true`. Unlike disable it inserts nothing when the key is absent: a
// package with no `enabled` field is already enabled by default (see
// PackageConfig.IsEnabled), so adding the line would only churn the file.
// Packages whose section is absent are skipped silently. The write is atomic
// (temp file + rename) and preserves the original file mode; an empty package
// list, or a run that changes nothing, leaves the file untouched.
func EnablePackagesInConfig(overlayPath string, pkgs []string) error {
	return setPackagesEnabled(overlayPath, pkgs, true, false)
}

// enabledAssignRegex matches an `enabled = ...` assignment line, capturing the
// indentation so a rewrite preserves it.
var enabledAssignRegex = regexp.MustCompile(`^(\s*)enabled\s*=`)

// versionAssignRegex matches a `version = ...` assignment line, capturing the
// indentation so a rewrite preserves it. The `=` must follow `version` after
// nothing but spaces, so versions_path/versions_selector never match.
var versionAssignRegex = regexp.MustCompile(`^(\s*)version\s*=`)

// setPackagesEnabled is the shared editing policy behind
// DisablePackagesInConfig (value=false, insertIfAbsent=true) and
// EnablePackagesInConfig (value=true, insertIfAbsent=false), on top of the
// section scanner in editPackagesConfigSections. It rewrites each target
// section's `enabled = ...` assignment to `enabled = <value>`, and — only when
// insertIfAbsent is set — inserts the key immediately after the header for
// sections that lack it. Enable leaves an absent key alone because nil already
// means enabled. The write is atomic (temp file + rename) and preserves the
// original file mode; an empty package list, or a run that changes nothing,
// leaves the file untouched.
func setPackagesEnabled(overlayPath string, pkgs []string, value, insertIfAbsent bool) error {
	if len(pkgs) == 0 {
		return nil
	}

	targets := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		targets[p] = true
	}

	assign := fmt.Sprintf("enabled = %t", value)

	return editPackagesConfigSections(overlayPath, targets, func(_ string, body []string, inComments []bool) ([]string, bool) {
		out := make([]string, 0, len(body)+1)
		changed := false
		found := false
		for j, line := range body {
			if !inComments[j] {
				if m := enabledAssignRegex.FindStringSubmatch(line); m != nil {
					out = append(out, m[1]+assign)
					found = true
					changed = true
					continue
				}
			}
			out = append(out, line)
		}
		if !found && insertIfAbsent {
			out = append(append(make([]string, 0, len(body)+1), assign), out...)
			changed = true
		}
		return out, changed
	})
}

// SetPackageVersions writes `version = "<v>"` for each key in pins, editing the
// raw text so comments, ordering and formatting survive. Existing assignments
// are rewritten in place; absent ones are inserted immediately before the
// record's comments field. One read, one atomic write for the whole batch.
//
// The insertion point is the comments assignment rather than the header because
// comments is required to be the record's LAST field (see PackageConfig.Comments
// and LintCommentsNotLast): landing just before it puts the pin after every
// other field. A record with no comments field gets the pin immediately before
// its `# END` marker instead, and one that violates the model on both counts
// gets it after its last non-blank line so the pin still lands inside the
// record. No other field is moved and the comments body is never touched.
//
// Keys whose section is absent are skipped silently, matching
// DisablePackagesInConfig. The write is atomic (temp file + rename) and
// preserves the original file mode; an empty map, or a batch that changes
// nothing (every pin already written verbatim), leaves the file untouched.
func SetPackageVersions(overlayPath string, pins map[string]string) error {
	if len(pins) == 0 {
		return nil
	}

	targets := make(map[string]bool, len(pins))
	for k := range pins {
		targets[k] = true
	}

	return editPackagesConfigSections(overlayPath, targets, func(name string, body []string, inComments []bool) ([]string, bool) {
		assign := fmt.Sprintf("version = %q", pins[name])

		// An existing assignment is rewritten in place, keeping its indentation;
		// nothing else moves. Only a rewrite that actually alters the text marks
		// the batch as changed, so re-pinning the already-pinned value stays a
		// full no-op (no write, same mtime).
		found := false
		changed := false
		out := make([]string, len(body))
		copy(out, body)
		for j, line := range body {
			if inComments[j] {
				continue
			}
			if m := versionAssignRegex.FindStringSubmatch(line); m != nil {
				found = true
				if newLine := m[1] + assign; newLine != line {
					out[j] = newLine
					changed = true
				}
			}
		}
		if found {
			return out, changed
		}

		// No version key yet: insert before the comments assignment, falling
		// back to the `# END` marker, then to after the last non-blank line.
		at := -1
		for j, line := range body {
			if inComments[j] {
				continue
			}
			if m := keyAssignRegex.FindStringSubmatch(line); m != nil && m[1] == "comments" {
				at = j
				break
			}
		}
		if at < 0 {
			for j, line := range body {
				if inComments[j] {
					continue
				}
				if strings.TrimSpace(line) == recordEndMarker {
					at = j
					break
				}
			}
		}
		if at < 0 {
			at = 0
			for j, line := range body {
				if strings.TrimSpace(line) != "" {
					at = j + 1
				}
			}
		}
		out = make([]string, 0, len(body)+1)
		out = append(out, body[:at]...)
		out = append(out, assign)
		out = append(out, body[at:]...)
		return out, true
	})
}

// sectionBodyEditor rewrites the body of one target section of packages.toml.
// body holds the section's lines — header excluded, running up to the next
// header or EOF — and inComments flags, line for line, which of them sit inside
// a multi-line `comments = """` doc string and must be treated as opaque text.
// It returns the replacement body and whether it differs from the input; the
// input slices must not be mutated.
type sectionBodyEditor func(name string, body []string, inComments []bool) ([]string, bool)

// editPackagesConfigSections is the shared raw-text surgery behind
// setPackagesEnabled and SetPackageVersions: one read, one linear pass locating
// each target `[section]` by table name, one atomic write. What to change
// inside a matched section is delegated to the sectionBodyEditor; everything
// structural lives here so there is exactly one copy of the scanner to keep
// correct.
//
// Lines inside a multi-line `comments = """` block are never read as section
// headers — and are flagged to the editor — so a doc body containing
// `#`-prefixed or `[`-prefixed lines can neither end a record early nor be
// edited as if it were configuration (the hazard noted on
// PackageConfig.Comments).
//
// The write is atomic (temp file + rename) and preserves the original file
// mode; an empty target set, or an edit that changes nothing, returns before
// any write, leaving the file untouched.
func editPackagesConfigSections(overlayPath string, targets map[string]bool, edit sectionBodyEditor) error {
	if len(targets) == 0 {
		return nil
	}

	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read packages.toml: %w", err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return fmt.Errorf("failed to stat packages.toml: %w", err)
	}

	// Split on "\n" (not bufio.Scanner) so a file without a trailing newline is
	// reproduced byte-for-byte by the strings.Join below.
	lines := strings.Split(string(data), "\n")
	inComments := commentsBodyMask(lines)

	changed := false
	out := make([]string, 0, len(lines)+len(targets))
	for i := 0; i < len(lines); {
		var name string
		isHeader := false
		if !inComments[i] {
			name, isHeader = tomlTableName(lines[i])
		}
		if !isHeader || !targets[name] {
			out = append(out, lines[i])
			i++
			continue
		}

		// At the header of a target section: emit the header, then hand the body
		// (up to the next header or EOF) to the editor.
		out = append(out, lines[i])
		i++
		start := i
		for i < len(lines) {
			if !inComments[i] {
				if _, nextHeader := tomlTableName(lines[i]); nextHeader {
					break
				}
			}
			i++
		}
		body, edited := edit(name, lines[start:i], inComments[start:i])
		out = append(out, body...)
		if edited {
			changed = true
		}
	}

	if !changed {
		return nil
	}

	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(strings.Join(out, "\n")), info.Mode().Perm()); err != nil {
		return fmt.Errorf("failed to write temp config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("failed to replace packages.toml: %w", err)
	}

	return nil
}

// commentsBodyMask reports, line for line, whether a line lies inside a
// multi-line `comments = """` doc string — the opening assignment line
// excluded, the closing `"""` line included. It mirrors the tracking
// lintRecordModel does for the same reason: a comments body may contain
// `#`-prefixed and `[`-prefixed lines that only LOOK like structure. Bodies
// written by the analyzer never contain a `"""` run (see formatCommentsField's
// tripleQuoteRegex escaping), so plain containment is a sound terminator check.
func commentsBodyMask(lines []string) []bool {
	mask := make([]bool, len(lines))
	open := false
	for i, line := range lines {
		if open {
			mask[i] = true
			if strings.Contains(line, `"""`) {
				open = false
			}
			continue
		}
		if commentsOpenRegex.MatchString(line) {
			rest := line[strings.Index(line, `"""`)+3:]
			open = !strings.Contains(rest, `"""`)
		}
	}
	return mask
}

// ValidatePackageConfig validates a single package configuration.
// It checks for required fields and valid parser types.
func ValidatePackageConfig(pkg string, cfg *PackageConfig) error {
	// Check required fields
	if cfg.URL == "" {
		return fmt.Errorf("package %s: %w", pkg, ErrMissingURL)
	}
	if cfg.Parser == "" {
		return fmt.Errorf("package %s: %w", pkg, ErrMissingParser)
	}

	// A negative per-package timeout is almost certainly a typo; reject it so the
	// misconfiguration surfaces rather than being silently ignored. Zero means
	// "use the global budget".
	if cfg.Timeout < 0 {
		return fmt.Errorf("package %s: timeout must be >= 0 seconds, got %d", pkg, cfg.Timeout)
	}

	// The key must be a well-formed atom, optionally slot- and label-suffixed. A
	// malformed key would otherwise surface much later as a path built from
	// nonsense.
	if _, _, ok := splitPkgAtom(pkg); !ok {
		return fmt.Errorf("package %s: %w", pkg, ErrInvalidPackageKey)
	}
	// An empty label ("cat/pkg@") is a typo: it makes the key no more unique
	// than the bare atom while looking like it does.
	if rest, label := splitPkgLabel(pkg); rest != pkg && label == "" {
		return fmt.Errorf("package %s: %w: the \"@\" label is empty", pkg, ErrInvalidPackageKey)
	}

	// A negative revision cannot be written as a -rN suffix; reject the typo
	// rather than silently emitting a plain PV.
	if cfg.Revision < 0 {
		return fmt.Errorf("package %s: revision must be >= 0, got %d", pkg, cfg.Revision)
	}

	// Validate parser type and required fields
	switch cfg.Parser {
	case "json":
		if cfg.Path == "" {
			return fmt.Errorf("package %s: %w", pkg, ErrMissingPath)
		}
	case "regex":
		if cfg.Pattern == "" {
			return fmt.Errorf("package %s: %w", pkg, ErrMissingPattern)
		}
	case "html":
		if cfg.Selector == "" && cfg.XPath == "" {
			return fmt.Errorf("package %s: %w", pkg, ErrMissingSelectorOrXPath)
		}
	case "script":
		if cfg.Script == "" {
			return fmt.Errorf("package %s: %w", pkg, ErrMissingScript)
		}
	default:
		return fmt.Errorf("package %s: %w: got %q", pkg, ErrInvalidParserType, cfg.Parser)
	}

	// Validate the select field. An unrecognized value is almost certainly a
	// typo in packages.toml, so fail hard rather than silently fall back.
	switch cfg.Select {
	case "", "first", "max", "last":
		// valid
	default:
		return fmt.Errorf("package %s: %w: got %q", pkg, ErrInvalidSelect, cfg.Select)
	}

	// Validate the type field. Like select, an unrecognized value is almost
	// certainly a typo in packages.toml, so fail hard rather than silently
	// auto-detecting and masking the mistake.
	switch cfg.Type {
	case "", "bin", "source":
		// valid
	default:
		return fmt.Errorf("package %s: %w: got %q", pkg, ErrInvalidType, cfg.Type)
	}

	// Validate transform rules. A malformed rule (wrong arity or uncompilable
	// regex) is warned and ignored at apply time (applyTransforms does the same),
	// so we warn here rather than fail — a bad rule must not block the whole run.
	for i, r := range cfg.Transform {
		if len(r) != 2 {
			warnLogf("package %s: transform rule #%d has %d elements, want 2 ([regex, repl]); it will be ignored", pkg, i, len(r))
			continue
		}
		if _, err := regexp.Compile(r[0]); err != nil {
			warnLogf("package %s: transform rule #%d has bad regex %q (%v); it will be ignored", pkg, i, r[0], err)
		}
	}

	// Validate the pre-release suffix. A typo here would be written straight into
	// an ebuild filename, so reject anything that is not a Gentoo suffix rather
	// than emitting a PV no package manager can order.
	if cfg.Suffix != "" && !validSuffixRegex.MatchString(cfg.Suffix) {
		return fmt.Errorf("package %s: %w: got %q", pkg, ErrInvalidSuffix, cfg.Suffix)
	}
	if cfg.SuffixWhen != "" {
		if cfg.Suffix == "" {
			return fmt.Errorf("package %s: %w", pkg, ErrSuffixWhenWithoutSuffix)
		}
		if _, err := regexp.Compile(cfg.SuffixWhen); err != nil {
			return fmt.Errorf("package %s: invalid suffix_when %q: %w", pkg, cfg.SuffixWhen, err)
		}
	}
	// Validate the release-line filter. An uncompilable regex would silently
	// widen the scan back to the whole directory — the exact failure the field
	// exists to prevent — so reject it here.
	if cfg.Series != "" {
		if _, err := regexp.Compile(cfg.Series); err != nil {
			return fmt.Errorf("package %s: invalid series %q: %w", pkg, cfg.Series, err)
		}
	}

	// Validate the pinned version. A pin that is not a well-formed Gentoo
	// version can never name an ebuild, and one outside the entry's own series
	// claims an ebuild the entry can never select — both are config mistakes
	// the sweep would otherwise act on, so fail hard. Checked after the series
	// compile check above, so newSeriesMatcher below can never hit its
	// warn-and-pass-everything fallback for a bad regex.
	if cfg.Version != "" {
		if !ebuild.IsValidVersion(cfg.Version) {
			return fmt.Errorf("package %s: %w: got %q", pkg, ErrInvalidVersion, cfg.Version)
		}
		if cfg.Series != "" && !newSeriesMatcher(cfg.Series).matches(cfg.Version) {
			return fmt.Errorf("package %s: %w: got %q (series %q)", pkg, ErrVersionOutsideSeries, cfg.Version, cfg.Series)
		}
	}

	// track="commit" derives its own _p<date>/_pre<date> suffix from the current
	// ebuild (see extractSnapshotSuffix), so a declared suffix would either be
	// ignored or stack into a nonsense PV. Fail rather than pick one silently.
	if cfg.Suffix != "" && cfg.Track == "commit" {
		return fmt.Errorf("package %s: suffix cannot be combined with track=\"commit\" (the snapshot suffix comes from the current ebuild)", pkg)
	}

	// transform/select do not apply to the script parser: that branch bypasses
	// fetchAndParse and the JS is responsible for all normalization. Warn so the
	// config author is not misled into thinking they take effect.
	if cfg.Parser == "script" {
		if len(cfg.Transform) > 0 {
			warnLogf("package %s: transform is ignored for parser=\"script\" (the script must normalize the version itself)", pkg)
		}
		if cfg.Select != "" && cfg.Select != "first" {
			warnLogf("package %s: select=%q is ignored for parser=\"script\" (the script must select the version itself)", pkg, cfg.Select)
		}
	}

	// Validate track field and its dependencies.
	switch cfg.Track {
	case "", "commit":
		// valid
	default:
		return fmt.Errorf("package %s: invalid track value: must be '' or 'commit', got %q", pkg, cfg.Track)
	}
	if cfg.Track == "commit" {
		if cfg.Parser != "json" {
			return fmt.Errorf("package %s: track=\"commit\" requires parser=\"json\"", pkg)
		}
		if cfg.CommitSHAPath == "" {
			return fmt.Errorf("package %s: track=\"commit\" requires commit_sha_path", pkg)
		}
	}
	if cfg.CommitSHAPath != "" && cfg.Track != "commit" {
		// Version-tracked package using commit_sha_path to substitute an auxiliary
		// SHA variable in the ebuild (e.g. cursor's BUILD_ID, which is part of the
		// download URL and changes with every release). Extracting the SHA requires
		// a JSON response from the same URL used for version detection.
		if cfg.Parser != "json" {
			return fmt.Errorf("package %s: commit_sha_path requires parser=\"json\"", pkg)
		}
	}
	if cfg.CommitVersionPattern != "" {
		if cfg.Track != "commit" {
			warnLogf("package %s: commit_version_pattern is set but track!=\"commit\"; it will be ignored", pkg)
		} else if cfg.CommitMessagePath == "" {
			return fmt.Errorf("package %s: commit_version_pattern requires commit_message_path", pkg)
		} else if _, err := regexp.Compile(cfg.CommitVersionPattern); err != nil {
			return fmt.Errorf("package %s: invalid commit_version_pattern %q: %w", pkg, cfg.CommitVersionPattern, err)
		}
	}
	if cfg.CommitMessagePath != "" && cfg.Track != "commit" {
		warnLogf("package %s: commit_message_path is set but track!=\"commit\"; it will be ignored", pkg)
	}

	// Validate the declared base-version source. Every failure here is fatal
	// rather than a warning: the whole point of the field is to replace a silent
	// fallback with a stated source, so a half-declared one must not load.
	switch cfg.BaseFrom {
	case "":
		// Not declared — legacy behaviour. base_url/base_pattern would be dead
		// weight, and a reader would reasonably expect them to work.
		if cfg.BaseURL != "" || cfg.BasePattern != "" {
			return fmt.Errorf("package %s: base_url/base_pattern require base_from", pkg)
		}
	case "file":
		if cfg.Track != "commit" {
			return fmt.Errorf("package %s: base_from requires track=\"commit\"", pkg)
		}
		if cfg.BaseURL == "" || cfg.BasePattern == "" {
			return fmt.Errorf("package %s: base_from=\"file\" requires base_url and base_pattern", pkg)
		}
		re, err := regexp.Compile(cfg.BasePattern)
		if err != nil {
			return fmt.Errorf("package %s: invalid base_pattern %q: %w", pkg, cfg.BasePattern, err)
		}
		// One capture group exactly. Zero means the pattern can never yield a
		// version; more than one is almost always an unescaped group in a regex
		// whose author expected the first one to win.
		if n := re.NumSubexp(); n != 1 {
			return fmt.Errorf("package %s: base_pattern %q must have exactly one capture group, got %d",
				pkg, cfg.BasePattern, n)
		}
	case "tag":
		if cfg.Track != "commit" {
			return fmt.Errorf("package %s: base_from requires track=\"commit\"", pkg)
		}
		if cfg.BaseURL == "" || cfg.BaseTagPattern == "" {
			return fmt.Errorf("package %s: base_from=\"tag\" requires base_url and base_tag_pattern", pkg)
		}
		re, err := regexp.Compile(cfg.BaseTagPattern)
		if err != nil {
			return fmt.Errorf("package %s: invalid base_tag_pattern %q: %w", pkg, cfg.BaseTagPattern, err)
		}
		if n := re.NumSubexp(); n != 1 {
			return fmt.Errorf("package %s: base_tag_pattern %q must have exactly one capture group, got %d",
				pkg, cfg.BaseTagPattern, n)
		}
	case "commit_message":
		if cfg.Track != "commit" {
			return fmt.Errorf("package %s: base_from requires track=\"commit\"", pkg)
		}
		if cfg.CommitVersionPattern == "" || cfg.CommitMessagePath == "" {
			return fmt.Errorf("package %s: base_from=\"commit_message\" requires commit_version_pattern and commit_message_path", pkg)
		}
	default:
		return fmt.Errorf("package %s: invalid base_from %q: must be \"file\", \"tag\" or \"commit_message\"", pkg, cfg.BaseFrom)
	}
	if cfg.BaseTagPattern != "" && cfg.BaseFrom != "tag" {
		return fmt.Errorf("package %s: base_tag_pattern requires base_from=\"tag\"", pkg)
	}

	// Validate the auxiliary free-text variable substitution. Both fields are
	// mutually required, and aux_pattern must compile. Deliberately parser-
	// agnostic: this is the whole point of the feature (regex/html sources whose
	// auxiliary value is not a SHA, e.g. betterbird's MY_BUILD / nomachine's MY_P).
	if (cfg.AuxVar != "") != (cfg.AuxPattern != "") {
		return fmt.Errorf("package %s: aux_var and aux_pattern must be set together", pkg)
	}
	if cfg.AuxPattern != "" {
		if _, err := regexp.Compile(cfg.AuxPattern); err != nil {
			return fmt.Errorf("package %s: invalid aux_pattern %q: %w", pkg, cfg.AuxPattern, err)
		}
	}

	// Validate fallback configuration if present
	if cfg.FallbackURL != "" && cfg.FallbackParser != "" {
		switch cfg.FallbackParser {
		case "json":
			// JSON fallback doesn't require pattern, uses Path from main config or FallbackPattern
		case "regex":
			if cfg.FallbackPattern == "" {
				return fmt.Errorf("package %s: fallback_pattern required for regex fallback parser", pkg)
			}
		case "html":
			// HTML fallback uses Selector or XPath from main config
		default:
			return fmt.Errorf("package %s: invalid fallback_parser type: %q", pkg, cfg.FallbackParser)
		}
	}

	return nil
}

// ValidateAll validates all package configurations in the PackagesConfig.
// Returns the first validation error encountered, or nil if all are valid.
func (c *PackagesConfig) ValidateAll() error {
	for _, pkg := range sortedKeys(c.Packages) {
		cfgCopy := c.Packages[pkg] // Create a copy to get a pointer
		if err := ValidatePackageConfig(pkg, &cfgCopy); err != nil {
			return err
		}
	}
	return validateDistinctEntries(c.Packages)
}

// validateDistinctEntries rejects two entries that would scan the same ebuilds.
//
// Entries for one package only make sense when each can say which ebuilds are
// its own: a distinct slot, or a distinct series. Two that agree on both resolve
// to the same current version and race each other — both bump the same ebuild,
// each overwriting the other's pending record — which looks like a checker bug
// rather than the config mistake it is. Two entries differing only by their
// "@label" are exactly that mistake: the label is identity, not a filter.
func validateDistinctEntries(pkgs map[string]PackageConfig) error {
	type scan struct{ atom, slot, series string }
	seen := make(map[scan]string, len(pkgs))
	for _, pkg := range sortedKeys(pkgs) {
		atom, slot := splitPkgSlot(pkg)
		key := scan{atom: atom, slot: slot, series: pkgs[pkg].Series}
		if other, dup := seen[key]; dup {
			return fmt.Errorf(
				"packages %s and %s select the same ebuilds: entries for one package must differ by slot or series (an \"@label\" alone does not filter anything)",
				other, pkg)
		}
		seen[key] = pkg
	}
	return nil
}
