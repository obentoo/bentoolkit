package autoupdate

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// claim is one registry entry's hold on an ebuild in a package directory.
//
// Several entries routinely share one directory — one per SLOT
// (net-libs/webkit-gtk:4.1 and :6) or one per release line
// (media-plugins/gst-plugins-vpx@stable and @dev) — and each of them keeps its
// own ebuild there on purpose. A claim is what makes that ownership explicit
// before anything is deleted: it says which entry holds which version, so a
// sweep removes only what NO entry holds.
type claim struct {
	// Key is the registry key, suffixes included, e.g.
	// "media-plugins/gst-plugins-vpx@stable". It is identity only: never build
	// a path from it, always split it first.
	Key string
	// Pin is the version the entry declares (PackageConfig.Version), empty when
	// the entry has no pin. This is what the sweep preserves.
	Pin string
	// Version is the version actually resolved on disk through
	// selectCurrentEbuild, empty when the entry resolves to nothing (the
	// package directory is gone, or its slot/series filter matches no ebuild).
	// Pin and Version disagreeing is drift between the registry and the
	// overlay; recording both is what lets a later reconciliation see it.
	Version string
}

// resolveClaims returns one claim per registry entry whose atom is atom,
// resolving each through selectCurrentEbuild so slot and series filtering has
// exactly one implementation. Re-deriving either filter here would give the
// sweep a second, drifting notion of which ebuild belongs to an entry — and this
// one deletes files.
//
// Every matching entry claims, including a disabled or held one. That is
// deliberate and the opposite of what the linter does: a linter must not invent
// findings from a switched-off entry, whereas a sweep that ignored one would
// delete the ebuild that entry is parked on. "enabled = false" means "stop
// checking upstream", never "this ebuild is disposable".
//
// An entry that resolves to nothing is NOT an error: selectCurrentEbuild's
// sentinels describe one entry holding no ebuild, which leaves the rest of the
// directory plannable, so it is recorded as an empty claim.Version. The error
// that MUST stop a plan — an unreadable directory — is raised by planSweep,
// which does the read. Keys are visited in sorted order so every verdict
// derived from the result is stable across runs.
func resolveClaims(log *slog.Logger, overlayPath string, cfgs map[string]registry.PackageConfig, atom string) []claim {
	// Normalise the target: a caller holding a registry key must get the same
	// answer as one holding a bare atom, and neither may reach a path with its
	// ":slot" or "@label" still attached.
	category, pkgName, ok := ebuilds.SplitPkgAtom(atom)
	if !ok {
		return nil
	}
	wantAtom := category + "/" + pkgName

	var claims []claim
	for _, key := range slices.Sorted(maps.Keys(cfgs)) {
		keyAtom, _ := ebuilds.SplitPkgSlot(key) // drops "@label" too
		if keyAtom != wantAtom {
			continue
		}
		cfg := cfgs[key]
		c := claim{Key: key, Pin: cfg.Version}
		// The single selection authority: it splits the key itself, applies the
		// ":slot" filter by reading SLOT= and the `series` filter by regex, and
		// skips live ebuilds.
		if cand, err := ebuilds.SelectCurrentEbuild(log, overlayPath, key, cfg.Series); err == nil {
			c.Version = cand.Version
		}
		claims = append(claims, c)
	}
	return claims
}

// sweepPlan is what a --clean would do to one package directory.
//
// On an unblocked plan Keep and Remove together partition the directory's
// parsable ebuilds, so a version absent from both was not understood as an
// ebuild at all and is left alone.
//
// A plan can be blocked for two different reasons, and a caller MUST be able to
// tell them apart because only one of them has an entry to name:
//
//   - Blocked != "" — an entry claims this directory but declares no pin, and
//     Blocked names it. Report it as "entry X has no version".
//   - Blocked == "" && len(WouldRemove) > 0 — NO registry entry claims this
//     directory at all. There is no key to name, so Blocked stays empty.
//     Report it as "no entry claims this directory".
//
// Both leave Remove empty and put the candidates in WouldRemove. An unclaimed
// directory with nothing removable (a single ebuild, held by the last-release
// floor) looks like an ordinary no-op plan — harmless, since both delete nothing.
type sweepPlan struct {
	// Keep maps a kept version to the entry key claiming it. An empty
	// value means the version is kept by a rule rather than by an entry — the
	// live-ebuild rule or the last-non-live floor — since a registry key is
	// never itself empty. It is empty when no entry claims the directory:
	// nothing is claimed by anything there, and a keep nobody asked for would
	// be fabricated evidence in the report.
	Keep map[string]string
	// Remove lists the versions to delete, ascending by ebuild.CompareVersions
	// so the report reads oldest-first and two runs never disagree on order.
	// It is empty on any blocked plan.
	Remove []string
	// WouldRemove lists the versions the sweep would have deleted had it not
	// been blocked, ascending. Empty on an unblocked plan — a caller reads
	// Remove there. Exists because a blocked directory must still report
	// its candidates, which Remove (mandated empty when blocked) cannot carry.
	//
	// It is computed under exactly the rules Remove is: live ebuilds excluded,
	// pinned versions kept, and the last-release floor respected — so it is a
	// report of what would really have happened, not a raw difference.
	WouldRemove []string
	// Blocked names the entry that lacks a pin; non-empty means remove nothing.
	// It is empty in the no-entry-claims case, which is also a block —
	// see the type comment for how to tell the two apart.
	Blocked string
}

// planSweep computes the plan without touching the filesystem beyond reading
// the directory. The rules, in this order:
//
//  1. One claiming entry without a pin blocks the whole directory, and Blocked
//     names it: guessing which ebuild is unclaimed deletes maintained lines.
//  2. NO entry claiming the directory blocks it too: a registry that failed to
//     match is the least-informed state, and a file-deleting default must not be
//     permissive there. Blocked stays empty — there is no entry to name.
//  3. Every live -9999 ebuild is kept: selection ignores them, so no pin can.
//  4. Every version a claiming entry HOLDS — the union of its pin and the version
//     it resolves to, as unclaimedIn computes it — is kept; other non-live go.
//  5. If that would leave no non-live ebuild, the highest one is kept instead.
//
// Rules 3-5 run whether or not the plan is blocked; a block only routes the
// result to WouldRemove instead of Remove, so the blocked report is the same
// calculation. A pin naming a version not on disk keeps nothing. An unreadable
// or absent directory is an error, never an empty plan ("nothing to keep" is
// one caller away from "remove everything").
func planSweep(log *slog.Logger, overlayPath string, cfgs map[string]registry.PackageConfig, atom string) (sweepPlan, error) {
	category, pkgName, ok := ebuilds.SplitPkgAtom(atom)
	if !ok {
		return sweepPlan{}, fmt.Errorf("cannot plan sweep: %q is not a category/package atom", atom)
	}
	// Built from the split components, never from the raw key: the sweep deletes
	// files, so a ":slot" or "@label" leaking into a path is destructive rather
	// than merely wrong.
	pkgDir := filepath.Join(overlayPath, category, pkgName)

	paths, err := ebuilds.FindEbuilds(pkgDir)
	if err != nil {
		return sweepPlan{}, fmt.Errorf("cannot plan sweep for %s/%s: %w", category, pkgName, err)
	}

	plan := sweepPlan{Keep: make(map[string]string)}

	// Rule 1: collect what each entry holds, and the first entry that can say
	// nothing about what it holds.
	claims := resolveClaims(log, overlayPath, cfgs, atom)
	// heldBy maps a version to the entry holding it — by pin OR by resolution
	// (rule 4); it is deliberately NOT named pinnedBy. Pin alone is not enough:
	// `--apply all --clean` builds ONE Applier whose registry snapshot is never
	// reloaded, and cleanPackageDir freshens only the applied entry's pin
	// (sweepConfigs). So while @dev is applied, its @stable sibling is planned
	// against a pin the @stable apply moments earlier made stale, and by pin
	// alone its brand-new ebuild would be removed while the registry still pins
	// it. A failed pin write or any out-of-band bump (hand edit, pkgdev, a `git
	// pull`) leaves the same drift. Keeping one ebuild too many costs a dirty
	// directory for one run; one too few costs a maintained release line. Do not
	// "simplify" this back to the pin alone.
	heldBy := make(map[string]string)
	// Claims arrive in key order, so first-writer-wins below is stable across
	// runs rather than a map-iteration coin flip.
	for _, c := range claims {
		if c.Pin == "" {
			if plan.Blocked == "" {
				plan.Blocked = c.Key
			}
			// A pinless entry blocks the whole directory, so its
			// resolved version is not collected either. Nothing is at risk —
			// a blocked plan removes nothing — and collecting it would only
			// shrink the candidate list the block is required to REPORT.
			continue
		}
		if _, dup := heldBy[c.Pin]; !dup {
			// Two entries holding one version is pathological config; the first
			// in key order owns the report line, and both keep the ebuild.
			heldBy[c.Pin] = c.Key
		}
		// The resolved half. Equal to the pin in the steady state, and different
		// exactly when the registry has drifted from the overlay — which is when
		// this file is one plan away from being deleted.
		if c.Version != "" {
			if _, dup := heldBy[c.Version]; !dup {
				heldBy[c.Version] = c.Key
			}
		}
	}

	// The directory is read exactly once, here.
	var live, nonLive []string
	for _, p := range paths {
		name := filepath.Base(p)
		eb, err := ebuild.ParsePath(filepath.Join(category, pkgName, name))
		if err != nil {
			// Not a "<pkg>-<version>.ebuild": selection never picks it, so the
			// sweep never removes it either.
			continue
		}
		if isLiveEbuild(name, eb.Version) {
			live = append(live, eb.Version)
			continue
		}
		nonLive = append(nonLive, eb.Version)
	}

	// Rules 3 and 4: what survives, and who says so. Skipped entirely when no
	// entry claims the directory — with nothing claiming anything, a populated
	// Keep would be a claim the report cannot attribute to anyone.
	if len(claims) > 0 {
		for _, v := range nonLive {
			if key, held := heldBy[v]; held {
				plan.Keep[v] = key
			}
		}
		for _, v := range live {
			if key, held := heldBy[v]; held {
				plan.Keep[v] = key // an entry pinning a live version still owns the line
				continue
			}
			plan.Keep[v] = ""
		}
	}

	// The candidates: every non-live ebuild no claim keeps. Computed once, under
	// one set of rules, and only THEN routed to Remove or WouldRemove — so a
	// blocked plan reports exactly what an unblocked one would have done, rather
	// than a second, looser calculation of it.
	var candidates []string
	for _, v := range nonLive {
		if _, kept := plan.Keep[v]; !kept {
			candidates = append(candidates, v)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return ebuild.CompareVersions(candidates[i], candidates[j]) < 0
	})

	// Rule 5: the floor. Only parsed non-live ebuilds count as "remaining" — an
	// unparsable file left on disk is not a release the directory can fall back
	// on, so it must not license removing the last real one.
	floorSurvivor := ""
	if len(candidates) > 0 && len(candidates) == len(nonLive) {
		floorSurvivor = candidates[len(candidates)-1] // the highest: keep the most current
		candidates = candidates[:len(candidates)-1]
	}
	if len(candidates) == 0 {
		candidates = nil
	}

	// Rule 1 and its no-entry twin, rule 2: report the candidates, delete nothing.
	if plan.Blocked != "" || len(claims) == 0 {
		plan.WouldRemove = candidates
		return plan, nil
	}

	plan.Remove = candidates
	if floorSurvivor != "" {
		plan.Keep[floorSurvivor] = ""
	}

	return plan, nil
}

// DivergenceKind classifies one disagreement between the registry and the
// overlay. The three kinds are NOT
// interchangeable: only StalePin carries a version the reconciliation may
// write, so a consumer that builds a write batch MUST switch on the kind rather
// than map every divergence to Key -> Disk.
type DivergenceKind int

const (
	// StalePin — the entry resolves to an ebuild whose version is not the one
	// it declares. Disk is the version on disk (never empty) and Pin is what the
	// registry says today.
	//
	// Pin is empty for the whole first reconciliation: all 409 records are
	// pinless right now, so an enabled entry that resolves to an ebuild diverges
	// from its (absent) pin. That is deliberate and load-bearing — it is exactly
	// the ~317-entry first bulk fill, and the only class the write batch is
	// built from. A caller wording the prompt can still tell the two apart
	// (Pin == "" reads "pin 317 entries for the first time", Pin != "" reads
	// "correct N stale pins"); the reconciliation itself does not, because the
	// repair is identical: write Disk.
	StalePin DivergenceKind = iota
	// UnclaimedEbuild — a non-live ebuild that no entry of its directory pins or
	// resolves to.
	//
	// It is a property of a DIRECTORY, not of an entry: several entries share
	// one directory (":slot" and "@label" siblings), and reporting the same
	// stray file once per sibling would inflate the prompt's count of what is
	// about to happen. So it is emitted ONCE per file, and Key holds the bare
	// "category/package" atom rather than a registry key. Pin is always empty —
	// nothing pins it, that IS the finding — and nothing about it is writable:
	// the repair is a sweep or a new registry entry, both decided by a human.
	UnclaimedEbuild
	// NoEbuild — the entry is enabled but its directory holds no ebuild it can
	// select: the package was removed, or its ":slot"/`series` filter matches
	// nothing there.
	//
	// Disk is always empty, so there is nothing to write (the registry never
	// holds a version that is not on disk) and reporting it is the whole of the
	// action. The existing orphan reconciliation, not this one, is what acts on
	// a removed package.
	NoEbuild
)

// String renders a kind as the stable identifier a report and a filter can both
// use, in the kebab-case the lint rules already use.
func (k DivergenceKind) String() string {
	switch k {
	case StalePin:
		return "stale-pin"
	case UnclaimedEbuild:
		return "unclaimed-ebuild"
	case NoEbuild:
		return "no-ebuild"
	default:
		return fmt.Sprintf("DivergenceKind(%d)", int(k))
	}
}

// Divergence is one disagreement between what the registry claims and what the
// overlay holds.
//
// The invariants a consumer may rely on, per kind:
//
//	StalePin        Key = registry key · Disk != "" · Disk != Pin  → writable
//	UnclaimedEbuild Key = "category/package" atom · Pin = ""       → report only
//	NoEbuild        Key = registry key · Disk = ""                 → report only
type Divergence struct {
	// Key identifies what the divergence is about: the registry key
	// ("net-libs/webkit-gtk:4.1") for StalePin and NoEbuild, the bare atom of
	// the directory for UnclaimedEbuild — see that constant for why. Either way
	// it is identity only: never build a path from it, always split it first.
	Key string
	// Kind is which of the three classes this is.
	Kind DivergenceKind
	// Pin is the version the registry declares (PackageConfig.Version), empty
	// when the entry has no pin — which is every entry today.
	Pin string
	// Disk is the version the overlay actually holds: the ebuild the entry
	// resolves to (StalePin), or the stray ebuild itself (UnclaimedEbuild).
	// Empty for NoEbuild, where the point is that there is none.
	Disk string
}

// Reconcile compares every enabled entry's pin against the ebuild it resolves
// to on disk, for the whole registry, and returns the divergences in three
// classes: StalePin, UnclaimedEbuild and NoEbuild.
//
// It only ever reads. The registry is a published artifact — the overlay
// auto-commits and pushes, so a wrong pin is a released one — so the caller
// shows the whole set, takes ONE confirmation for all of it, leaves
// packages.toml byte-identical on "no" and refuses to write from a non-TTY
// without --yes. Nothing here should ever start writing packages.toml.
//
// Only enabled entries can be the SUBJECT of a divergence, but the unclaimed
// scan counts every entry of a directory: a switched-off entry still holds its
// file. Resolution goes through selectCurrentEbuild; its not-found sentinels
// land in NoEbuild, and any other error skips that entry with a warning — a
// fabricated divergence would become a fabricated, published pin. The result is
// sorted, so two runs differ only when the overlay did. It costs about 2-3
// readdir per enabled entry; CheckResult could not replace the scan, as it
// carries no directory listing. Skips are warned about to log; nil discards them.
func Reconcile(log *slog.Logger, overlayPath string, cfgs map[string]registry.PackageConfig) []Divergence {
	log = logging.OrDiscard(log)
	var divs []Divergence
	// Directories already scanned for unclaimed ebuilds, keyed by atom: several
	// entries routinely share one, and the finding belongs to the file, not to
	// each sibling that happens to live next to it.
	scanned := make(map[string]bool)

	for _, key := range slices.Sorted(maps.Keys(cfgs)) {
		cfg := cfgs[key]
		// enabled = false is the ONLY skip here, and the two conditions this
		// once bundled were never the same reason:
		//
		//   - a disabled entry is skipped because there is nothing to record —
		//     that flag is the checker's own bookkeeping for "the ebuild
		//     vanished from the overlay" — and the overlay-driven status
		//     reconciliation in CheckAll owns the entry;
		//   - a HELD entry is skipped by the CHECKER, which must not auto-bump
		//     it. That is a statement about fetching a new version, and it says
		//     nothing about recording the one already on disk: hold means
		//     "present, but do not auto-bump", so the file IS there and writing
		//     down which version it is second-guesses no maintainer decision.
		//     It is therefore compared like any other entry and its hold is
		//     never written back.
		if !cfg.IsEnabled() {
			continue
		}
		category, pkgName, ok := ebuilds.SplitPkgAtom(key)
		if !ok {
			log.Warn("reconcile: skipping registry key: it is not a category/package atom", "key", key)
			continue
		}

		cand, err := ebuilds.SelectCurrentEbuild(log, overlayPath, key, cfg.Series)
		switch {
		case err == nil:
			// An exact string, not ebuild.CompareVersions: the sweep matches pins
			// exactly (planSweep's heldBy), so a pin that merely compares equal
			// protects no file and must still be reported stale.
			if cand.Version != cfg.Version {
				divs = append(divs, Divergence{
					Key: key, Kind: StalePin, Pin: cfg.Version, Disk: cand.Version,
				})
			}
		case errors.Is(err, ebuilds.ErrNoEbuildFound),
			errors.Is(err, ebuilds.ErrSlotNotFound),
			errors.Is(err, ebuilds.ErrSeriesNotFound):
			divs = append(divs, Divergence{Key: key, Kind: NoEbuild, Pin: cfg.Version})
		default:
			// An unreadable directory, and nothing else: every "this entry holds
			// nothing" case is a sentinel above. Say which entry and why, and
			// report nothing about it.
			log.Warn("reconcile: skipping registry key", "key", key, "err", err)
			continue
		}

		atom := category + "/" + pkgName
		if scanned[atom] {
			continue
		}
		// ErrNoEbuildFound is the one sentinel that does not prove the directory
		// is scannable, and it is also the one that guarantees the scan would
		// find nothing: selectCurrentEbuild returns it either because the
		// directory is absent, or — with no ":slot" and no `series` narrowing
		// the search — because the directory holds no parsable non-live ebuild
		// at all, and only such an ebuild can ever be reported as unclaimed.
		if errors.Is(err, ebuilds.ErrNoEbuildFound) {
			continue
		}
		scanned[atom] = true
		divs = append(divs, unclaimedIn(log, overlayPath, cfgs, atom)...)
	}

	// Sorted by key, then by class, so the order is total and no map iteration
	// can reach the output: two runs over an unchanged overlay must produce the
	// identical prompt, or a maintainer cannot tell a real change from a
	// reshuffle. Within one directory's unclaimed files the versions are ordered
	// the way the sweep reports its removals — Gentoo order, oldest first — so
	// the two lists about the same files never disagree.
	sort.SliceStable(divs, func(i, j int) bool {
		a, b := divs[i], divs[j]
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if c := ebuild.CompareVersions(a.Disk, b.Disk); c != 0 {
			return c < 0
		}
		// Two versions the comparison calls equal ("1.0" and "1.0-r0") are still
		// two files; order them by their text so the order stays total.
		return a.Disk < b.Disk
	})
	return divs
}

// StalePinBatch builds the write batch, and is the ONLY place a Divergence
// becomes something written to the registry.
//
// It switches on Kind because the three classes are not interchangeable, and
// mapping all of them to Key -> Disk corrupts the registry in two distinct ways:
//
//   - NoEbuild carries an empty Disk, so writing it would ERASE the entry's pin
//     — and an entry with no pin blocks its whole directory's next --clean;
//   - UnclaimedEbuild's Key is a bare "category/package" atom, not a registry
//     key, and the two routinely coincide (net-misc/rclone is both). Writing it
//     would point that entry's pin at the stray ebuild the finding is asking a
//     human to delete: the exact opposite of the intent.
//
// A future fourth kind falls through unwritten, which is the safe direction.
func StalePinBatch(divs []Divergence) map[string]string {
	pins := make(map[string]string, len(divs))
	for _, d := range divs {
		switch d.Kind {
		case StalePin:
			// The only writable class: Key is a registry key and Disk is a
			// version that exists on disk (UB4).
			pins[d.Key] = d.Disk
		case UnclaimedEbuild:
			// Report only: the repair is a sweep or a new entry, both human
			// decisions.
		case NoEbuild:
			// Report only: there is no version on disk to record.
		}
	}
	if len(pins) == 0 {
		return nil
	}
	return pins
}

// unclaimedIn returns one UnclaimedEbuild per non-live ebuild in atom's package
// directory that no entry of that atom accounts for.
//
// "Accounted for" is the union of both halves of a claim: the version an entry
// PINS and the version it RESOLVES to. The second half is what keeps the first
// reconciliation honest — with the registry entirely pinless, claiming by pin
// alone would report every ebuild in the overlay as unclaimed and bury the ~317
// pins that actually need writing. An entry that resolves to a file is holding
// it, pin or no pin.
//
// Live -9999 ebuilds are never reported: selection skips them, so no pin
// can ever name one, so "no entry claims it" is true of every live ebuild in the
// overlay and means nothing. Reporting them would put the one file that cannot
// be restored by re-fetching a release at the top of a removal candidate list.
//
// An unreadable directory yields a warning and no divergences at all, never a
// guess about what is in it.
func unclaimedIn(log *slog.Logger, overlayPath string, cfgs map[string]registry.PackageConfig, atom string) []Divergence {
	category, pkgName, ok := ebuilds.SplitPkgAtom(atom)
	if !ok {
		return nil
	}
	// Built from the split components, never from the raw key.
	pkgDir := filepath.Join(overlayPath, category, pkgName)
	paths, err := ebuilds.FindEbuilds(pkgDir)
	if err != nil {
		logging.OrDiscard(log).Warn("reconcile: skipping the unclaimed-ebuild scan", "atom", category+"/"+pkgName, "err", err)
		return nil
	}

	// Claims are collected from EVERY entry of the atom, disabled and held
	// included: a switched-off entry is not a divergence to report,
	// not that its ebuild belongs to nobody. resolveClaims is the one place that
	// knows who holds what; re-deriving it here would give the report a second,
	// drifting answer to that question.
	claimed := make(map[string]bool)
	for _, c := range resolveClaims(log, overlayPath, cfgs, atom) {
		if c.Pin != "" {
			claimed[c.Pin] = true
		}
		if c.Version != "" {
			claimed[c.Version] = true
		}
	}

	var divs []Divergence
	for _, p := range paths {
		name := filepath.Base(p)
		eb, err := ebuild.ParsePath(filepath.Join(category, pkgName, name))
		if err != nil {
			// Not a "<pkg>-<version>.ebuild": selection never picks it and the
			// sweep never removes it, so calling it unclaimed would report a
			// file nothing was ever going to act on.
			continue
		}
		if isLiveEbuild(name, eb.Version) || claimed[eb.Version] {
			continue
		}
		divs = append(divs, Divergence{Key: atom, Kind: UnclaimedEbuild, Disk: eb.Version})
	}
	return divs
}

// isLiveEbuild reports whether an ebuild is a live one, i.e. built from VCS HEAD
// rather than from a release, conventionally versioned 9999.
//
// It deliberately takes the union of the two tests already used in this package:
// the filename test selectCurrentEbuild applies when skipping live ebuilds, and
// the version-prefix test ExtractEbuildMetadata applies when setting IsLive. The
// union is required in this direction because the two disagree at the edges
// ("pkg-9999-r1.ebuild" fails the first, passes the second) and every
// disagreement resolved the narrow way ends with a live ebuild in Remove — the
// one file in an overlay that cannot be restored by re-fetching a release.
func isLiveEbuild(filename, version string) bool {
	return strings.Contains(filename, "-9999.ebuild") ||
		version == "9999" ||
		strings.HasPrefix(version, "9999")
}
