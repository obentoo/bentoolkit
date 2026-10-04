package autoupdate

import (
	"context"
	"errors"
	"fmt"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// ErrNoLocalPackageDir reports that the ::gentoo provider exposes no on-disk
// package directory (it is not a provider.PackageDirProvider), so a revive has
// no base ebuild to seed from.
var ErrNoLocalPackageDir = errors.New("the gentoo provider has no local package directory")

// ReviveStatus is the result class of reviving one package.
type ReviveStatus string

// The three revive results the summary keys on.
const (
	ReviveRevived ReviveStatus = "revived"
	ReviveSkipped ReviveStatus = "skipped"
	ReviveFailed  ReviveStatus = "failed"
)

// ReviveOutcome is the result of reviving one package. Detail is the
// human-facing note: "<gentoo> → <upstream>" when revived, the skip reason, or
// the failing step's error.
type ReviveOutcome struct {
	Package string
	Status  ReviveStatus
	Detail  string
}

// ReviveApplier is the part of *Applier the pipeline drives. It is an interface
// so a unit test can make Apply report an obsolete bump (R4.3).
type ReviveApplier interface {
	SeedFromGentoo(pkg, srcDir, version string) error
	// MarkReenabled tells the applier the entry was re-enabled after it loaded
	// packages.toml, so its stale enabled = false does not refuse the bump.
	MarkReenabled(pkg string)
	Apply(ctx context.Context, pkg string, compile bool) (*ApplyResult, error)
}

var _ ReviveApplier = (*Applier)(nil)

// CanRevive reports, before any other work, whether prov can seed a revive. It
// returns a wrapped ErrNoLocalPackageDir when prov is not a PackageDirProvider.
func CanRevive(prov provider.Provider) error {
	if _, ok := prov.(provider.PackageDirProvider); !ok {
		return fmt.Errorf("revive with provider %T: %w", prov, ErrNoLocalPackageDir)
	}
	return nil
}

// Reviver resurrects orphaned packages one at a time: it seeds the highest
// ::gentoo ebuild into the overlay, re-enables the packages.toml entry, re-checks
// upstream with the cache bypassed and applies the bump. One Reviver serves
// every target of a run.
type Reviver struct {
	overlayPath string
	applier     ReviveApplier
	prov        provider.Provider
	pdp         provider.PackageDirProvider
	newChecker  func() (*Checker, error)
	compile     bool
}

// ReviverOption configures a Reviver.
type ReviverOption func(*Reviver)

// WithReviveCompile passes the compile request to the apply of every revived
// bump (--compile).
func WithReviveCompile(compile bool) ReviverOption {
	return func(r *Reviver) { r.compile = compile }
}

// NewReviver returns ErrNoLocalPackageDir (wrapped) when prov is not a
// provider.PackageDirProvider. newChecker must build a Checker that shares the
// applier's PendingList: the entry CheckPackage writes is read back by Apply in
// the same process, from the same in-memory map.
func NewReviver(overlayPath string, applier ReviveApplier, prov provider.Provider,
	newChecker func() (*Checker, error), opts ...ReviverOption) (*Reviver, error) {
	if err := CanRevive(prov); err != nil {
		return nil, err
	}
	pdp, _ := prov.(provider.PackageDirProvider) // CanRevive guarantees the assertion holds
	r := &Reviver{
		overlayPath: overlayPath,
		applier:     applier,
		prov:        prov,
		pdp:         pdp,
		newChecker:  newChecker,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// Revive performs the full revive of one package and returns its outcome. It
// never returns an error: every failure is an outcome, so the caller can
// continue with the remaining targets.
//
// Steps, in order: locate the ::gentoo package dir, pick the highest ::gentoo
// version, seed it into the overlay, re-enable the entry in packages.toml
// BEFORE checking (so the checker does not skip it), CheckPackage(force=true)
// on a fresh Checker to populate pending with the upstream version, then Apply.
func (r *Reviver) Revive(ctx context.Context, pkg string) ReviveOutcome {
	failed := func(detail string) ReviveOutcome {
		return ReviveOutcome{Package: pkg, Status: ReviveFailed, Detail: detail}
	}

	category, pkgName, ok := SplitPackageKey(pkg)
	if !ok {
		return failed(fmt.Sprintf("invalid package name %q (want category/package)", pkg))
	}

	// On-disk ::gentoo package dir to seed from.
	srcDir, err := r.pdp.LocalPackagePath(category, pkgName)
	if err != nil {
		return failed(fmt.Sprintf("gentoo package dir lookup failed: %v", err))
	}

	// The highest ::gentoo version is the base ebuild copied in.
	versions, err := r.prov.GetPackageVersions(ctx, category, pkgName)
	if err != nil {
		return failed(fmt.Sprintf("gentoo version lookup failed: %v", err))
	}
	gentooVersion := maxGentooVersion(versions)
	if gentooVersion == "" {
		return failed("no comparable gentoo version found")
	}

	// Seed the ::gentoo ebuild (+ metadata.xml / files/) into the overlay.
	// SeedFromGentoo takes the full "category/package" (it splits internally).
	if err := r.applier.SeedFromGentoo(pkg, srcDir, gentooVersion); err != nil {
		return failed(fmt.Sprintf("seed from gentoo failed: %v", err))
	}

	// Re-enable the entry BEFORE checking: the checker skips disabled entries, so
	// a still-disabled package would never produce a pending update.
	if err := EnablePackagesInConfig(r.overlayPath, []string{pkg}); err != nil {
		return failed(fmt.Sprintf("re-enable in packages.toml failed: %v", err))
	}
	// The shared Applier loaded packages.toml before this entry was enabled;
	// without this its stale enabled = false would refuse the bump below.
	r.applier.MarkReenabled(pkg)

	// A FRESH Checker loads the now re-enabled packages.toml; force=true bypasses
	// the cache so the pending list gets the current upstream version.
	checker, err := r.newChecker()
	if err != nil {
		return failed(fmt.Sprintf("checker init failed: %v", err))
	}
	result, err := checker.CheckPackage(ctx, pkg, true)
	if err != nil {
		return failed(fmt.Sprintf("check failed: %v", err))
	}
	if result.Skipped != "" {
		return ReviveOutcome{Package: pkg, Status: ReviveSkipped,
			Detail: fmt.Sprintf("re-enabled, but %s still keeps it out of autoupdate", result.Skipped)}
	}
	if !result.HasUpdate {
		// The seeded base already equals upstream: nothing to bump. The base
		// ebuild is in place and the entry re-enabled, so a normal --check
		// tracks it from here on.
		return ReviveOutcome{Package: pkg, Status: ReviveSkipped,
			Detail: fmt.Sprintf("gentoo %s already current with upstream %s", gentooVersion, result.UpstreamVersion)}
	}

	applyResult, err := r.applier.Apply(ctx, pkg, r.compile)
	if err != nil {
		detail := err.Error()
		if applyResult != nil && applyResult.LogPath != "" {
			detail = fmt.Sprintf("%v (log: %s)", err, applyResult.LogPath)
		}
		// S033-R3.6: a revive whose bump failed kept its staged tree exactly like
		// any other failed apply, and this outcome is the only report the operator
		// gets for it. A tree named in no report is found only by going looking.
		if applyResult != nil && applyResult.StagedPath != "" {
			detail = fmt.Sprintf("%s (staged tree kept at %s)", detail, applyResult.StagedPath)
		}
		return failed(detail)
	}
	if applyResult != nil && applyResult.Held {
		return ReviveOutcome{Package: pkg, Status: ReviveSkipped,
			Detail: "held (" + applyResult.HoldReason + "); the bump stays pending"}
	}
	if applyResult != nil && applyResult.Obsolete {
		return ReviveOutcome{Package: pkg, Status: ReviveSkipped, Detail: applyResult.ObsoleteReason}
	}

	return ReviveOutcome{Package: pkg, Status: ReviveRevived, Detail: fmt.Sprintf("%s → %s", gentooVersion, result.UpstreamVersion)}
}
