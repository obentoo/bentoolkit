package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// This file is the ONE exported door onto the authenticated fetch.
//
// # Why it exists
//
// Everything in authfetch.go is unexported and reached from a single caller:
// the sweep, through prefetchAuthDistfile, minutes before `pkgdev manifest`
// runs. That is the maintainer's path, and it is the only one there was — so a
// USER whose emerge stopped at a distfile no mirror carries had no way to run
// the download the overlay already knows how to perform. They were left with
// the manual instructions in pkg_nofetch: open the vendor page, log in, save
// the file under exactly the right name, drop it in DISTDIR.
//
// The exported surface is deliberately one function and two structs, not the
// spec type: the [meta] sub-schema is this package's own business and has
// changed twice already, while "fetch the distfile this record describes" is
// the stable question a command can be built on.

var (
	// ErrPackageNotInRegistry is returned when packages.toml holds no record
	// for the requested package. It is separate from ErrNoAuthFetch because the
	// remedies differ: one is a package this overlay does not track, the other
	// is a tracked package whose distfile needs no help.
	ErrPackageNotInRegistry = errors.New("no packages.toml record for this package")
	// ErrAmbiguousPackageKey is returned when the requested atom matches more
	// than one record — the slot and label suffixes exist precisely so one atom
	// can carry several entries, and guessing between them would fetch the
	// wrong release line in silence.
	ErrAmbiguousPackageKey = errors.New("the atom matches more than one packages.toml record")
	// ErrNoAuthFetch is returned for a record that configures no authenticated
	// download. Its distfile is on a mirror and `emerge` fetches it unaided, so
	// this is a "nothing to do here", not a failure of the download.
	ErrNoAuthFetch = errors.New("this package configures no authenticated distfile fetch")
)

// AuthDistfileRequest names one authenticated download to perform.
type AuthDistfileRequest struct {
	// OverlayPath is the overlay holding .autoupdate/packages.toml and the
	// package's ebuilds. Both are read; nothing in the overlay is written.
	OverlayPath string
	// Package is a "category/package" atom, or a full registry key carrying its
	// ":slot"/"@label" suffix when the atom alone is ambiguous.
	Package string
	// Version is the version to substitute into the record's fetch_filename.
	// Empty means "the version the overlay currently carries", resolved the same
	// way the checker resolves it — highest ebuild, honouring the record's slot
	// and series filters.
	Version string
	// DestDir is the directory the file is written into. It must already exist
	// and be writable; this package does not create it, because the caller that
	// knows whether the answer is a private distdir or the host's own DISTDIR is
	// also the one that must decide whether creating it is appropriate.
	DestDir string
}

// AuthDistfileResult describes a completed download.
type AuthDistfileResult struct {
	// Package is the registry key that was used, which is the requested atom
	// plus whatever suffix the matching record carried. Reported so a caller
	// that passed an atom can name the record it actually acted on.
	Package string
	// Version is the version used, resolved or as requested.
	Version string
	// Path is the file written, inside DestDir.
	Path string
	// SerialEnv names the env var the serial came from, or "" when the record
	// configures no serial. It is the NAME only — the value is never returned,
	// logged or rendered anywhere.
	SerialEnv string
}

// FetchAuthDistfile performs the authenticated download a package's [meta]
// block describes, and reports the file it wrote.
//
// It is the exported form of what the sweep does before `pkgdev manifest`, and
// runs the same parser, the same guards and the same writer — deliberately, so
// that a user's fetch and a maintainer's sweep cannot disagree about what the
// vendor endpoint expects or about what counts as a valid response.
//
// The error is the sentinel-wrapped kind: ErrPackageNotInRegistry,
// ErrAmbiguousPackageKey and ErrNoAuthFetch describe the request, while
// ErrAuthFetchSecretMissing and ErrAuthFetchFailed describe the download.
func FetchAuthDistfile(ctx context.Context, req AuthDistfileRequest) (AuthDistfileResult, error) {
	cfg, err := LoadPackagesConfig(req.OverlayPath)
	if err != nil {
		return AuthDistfileResult{}, err
	}

	key, pkgCfg, err := resolveRegistryKey(cfg, req.Package)
	if err != nil {
		return AuthDistfileResult{}, err
	}

	spec, enabled, err := parseAuthFetchSpec(pkgCfg.Meta)
	if err != nil {
		return AuthDistfileResult{}, err
	}
	if !enabled {
		return AuthDistfileResult{}, fmt.Errorf("%s: %w", key, ErrNoAuthFetch)
	}

	version := strings.TrimSpace(req.Version)
	if version == "" {
		best, err := selectCurrentEbuild(req.OverlayPath, key, pkgCfg.Series)
		if err != nil {
			return AuthDistfileResult{}, fmt.Errorf("resolving the version to fetch for %s: %w", key, err)
		}
		version = best.Version
	}

	path, err := spec.fetchDistfile(ctx, version, req.DestDir)
	if err != nil {
		return AuthDistfileResult{}, err
	}
	return AuthDistfileResult{Package: key, Version: version, Path: path, SerialEnv: spec.serialEnv}, nil
}

// resolveRegistryKey turns what the user typed into the registry key of exactly
// one record.
//
// An exact hit wins outright, so a caller that already knows the full key —
// "net-libs/webkit-gtk:4.1", "app-office/libreoffice@testing" — always gets
// that record, even on the day someone adds a plain-atom entry beside it.
//
// Otherwise the atom is matched against every key with its suffixes stripped.
// One match is the ordinary case. Several is refused rather than resolved:
// those records track different slots or different release lines, their
// fetch_filename templates resolve against different versions, and picking one
// would hand the operator a file for a version they are not installing.
func resolveRegistryKey(cfg *PackagesConfig, want string) (string, PackageConfig, error) {
	want = strings.TrimSpace(want)
	if pkgCfg, ok := cfg.Packages[want]; ok {
		return want, pkgCfg, nil
	}

	var matches []string
	for key := range cfg.Packages {
		atom, _ := splitPkgSlot(key)
		if atom == want {
			matches = append(matches, key)
		}
	}
	// Map iteration is random, so a message naming several keys must be sorted
	// or it reshuffles between two runs of the same failing command.
	sort.Strings(matches)

	switch len(matches) {
	case 0:
		return "", PackageConfig{}, fmt.Errorf("%s: %w", want, ErrPackageNotInRegistry)
	case 1:
		return matches[0], cfg.Packages[matches[0]], nil
	default:
		return "", PackageConfig{}, fmt.Errorf("%s: %w: %s (name one of them)",
			want, ErrAmbiguousPackageKey, strings.Join(matches, ", "))
	}
}
