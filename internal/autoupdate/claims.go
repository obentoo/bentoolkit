package autoupdate

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
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
// exactly one implementation (design D2). Re-deriving either filter here would
// give the sweep a second, drifting notion of which ebuild belongs to an entry —
// and this one deletes files.
//
// Every matching entry claims, including a disabled or held one. That is
// deliberate and the opposite of what the linter does: a linter must not invent
// findings from a switched-off entry, whereas a sweep that ignored one would
// delete the ebuild that entry is parked on. "enabled = false" means "stop
// checking upstream", never "this ebuild is disposable".
//
// An entry that resolves to nothing is NOT an error here. selectCurrentEbuild's
// sentinels (ErrNoEbuildFound, ErrSlotNotFound, ErrSeriesNotFound) all describe
// an entry that currently holds no ebuild, which is a fact about that one entry
// and leaves the rest of the directory perfectly plannable; it is recorded as an
// empty claim.Version. The error that MUST stop a plan is the directory being
// unreadable, and that one is raised by planSweep, which does the read.
//
// Keys are visited in sorted order so the result — and every verdict derived
// from it — is stable across runs.
func resolveClaims(overlayPath string, cfgs map[string]PackageConfig, atom string) []claim {
	// Normalise the target: a caller holding a registry key must get the same
	// answer as one holding a bare atom, and neither may reach a path with its
	// ":slot" or "@label" still attached.
	category, pkgName, ok := splitPkgAtom(atom)
	if !ok {
		return nil
	}
	wantAtom := category + "/" + pkgName

	var claims []claim
	for _, key := range sortedKeys(cfgs) {
		keyAtom, _ := splitPkgSlot(key) // drops "@label" too
		if keyAtom != wantAtom {
			continue
		}
		cfg := cfgs[key]
		c := claim{Key: key, Pin: cfg.Version}
		// The single selection authority: it splits the key itself, applies the
		// ":slot" filter by reading SLOT= and the `series` filter by regex, and
		// skips live ebuilds (UB2).
		if cand, err := selectCurrentEbuild(overlayPath, key, cfg.Series); err == nil {
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
//     Blocked names it (R5.1). Report it as "entry X has no version".
//   - Blocked == "" && len(WouldRemove) > 0 — NO registry entry claims this
//     directory at all. There is no key to name, so Blocked stays empty.
//     Report it as "no entry claims this directory".
//
// Both leave Remove empty and put the candidates in WouldRemove. The pairing
// discriminates only when there WAS something to consider: a directory nothing
// claims but that has nothing removable either (a single ebuild, held by the
// R4.3 floor) is indistinguishable from an ordinary no-op plan — which is
// harmless, since both delete nothing.
type sweepPlan struct {
	// Keep maps a kept version to the entry key claiming it (R6.1). An empty
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
	// Remove there. Exists because R5.1 requires a blocked directory to report
	// its candidates, which Remove (mandated empty when blocked) cannot carry.
	//
	// It is computed under exactly the rules Remove is: live ebuilds excluded,
	// pinned versions kept, and the R4.3 floor respected — so it is a report of
	// what would really have happened, not a raw difference.
	WouldRemove []string
	// Blocked names the entry that lacks a pin; non-empty means remove nothing
	// (R5.1). It is empty in the no-entry-claims case, which is also a block —
	// see the type comment for how to tell the two apart.
	Blocked string
}

// planSweep computes the plan without touching the filesystem beyond reading
// the directory. Live -9999 ebuilds are always kept and never appear in Remove.
//
// The rules, applied in this order:
//
//  1. R5.1/D3 — one claiming entry without a pin blocks the whole directory.
//     Nothing is removed and Blocked names it. Guessing which ebuild is the
//     unclaimed one is the failure mode that deletes a maintained release line,
//     so the plan refuses to guess: 89 of the overlay's 93 multi-ebuild
//     directories are deliberate, one ebuild per entry.
//  2. D3 again — NO entry claiming the directory blocks it too. Zero claims is
//     the least-informed state there is, and R4.1 read literally would license
//     removing everything but the newest ebuild from a directory the registry
//     simply failed to match. That is the same disaster as rule 1 (a maintained
//     release line deleted) reached without any pin being wrong: an atom-to-key
//     mismatch, a registry that loaded empty, a filter that selected nothing.
//     A file-deleting default does not get to be permissive in its least
//     informed state. Blocked stays empty here — there is no entry to name.
//  3. The live rule — every -9999 ebuild is kept, whatever the pins say. This is
//     NOT UB2: UB2 is about selection ignoring live ebuilds when choosing an
//     entry's current version. Here selection has already ignored them, which is
//     precisely why no pin can ever claim one, which is precisely why they need
//     a preservation rule of their own or the sweep would delete every one.
//  4. R4.1 — every pinned version present on disk is kept, recorded against the
//     entry that pins it; everything else non-live is removed.
//  5. R4.3 — if that would leave the directory with no non-live ebuild at all,
//     the highest one is dropped from the removal list and kept instead. A
//     directory emptied of releases is unrecoverable from the overlay alone; a
//     stale ebuild left behind is not.
//
// Rules 3 to 5 are computed whether or not the plan is blocked; a block only
// decides whether the result lands in Remove or in WouldRemove. That is what
// makes the blocked report trustworthy: it is the same calculation, not a
// looser second one.
//
// A pin naming a version that is not on disk keeps nothing and is not reported
// as kept — the report must not claim a file that is not there. That drift
// (registry says 2.0.0, overlay has 1.0.0) is what rule 5 then catches, and what
// a later reconciliation is meant to repair.
//
// An unreadable — or absent — package directory is an error, never an empty
// plan: an empty plan reads as "nothing to keep", which is one caller away from
// "remove everything".
func planSweep(overlayPath string, cfgs map[string]PackageConfig, atom string) (sweepPlan, error) {
	category, pkgName, ok := splitPkgAtom(atom)
	if !ok {
		return sweepPlan{}, fmt.Errorf("cannot plan sweep: %q is not a category/package atom", atom)
	}
	// Built from the split components, never from the raw key: the sweep deletes
	// files, so a ":slot" or "@label" leaking into a path is destructive rather
	// than merely wrong.
	pkgDir := filepath.Join(overlayPath, category, pkgName)

	paths, err := findEbuilds(pkgDir)
	if err != nil {
		return sweepPlan{}, fmt.Errorf("cannot plan sweep for %s/%s: %w", category, pkgName, err)
	}

	plan := sweepPlan{Keep: make(map[string]string)}

	// Rule 1: collect the pins and the first entry that has none.
	claims := resolveClaims(overlayPath, cfgs, atom)
	pinnedBy := make(map[string]string)
	for _, c := range claims {
		if c.Pin == "" {
			if plan.Blocked == "" {
				plan.Blocked = c.Key
			}
			continue
		}
		if _, dup := pinnedBy[c.Pin]; !dup {
			// Two entries pinning one version is pathological config; the first
			// in key order owns the report line, and both keep the ebuild.
			pinnedBy[c.Pin] = c.Key
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
			if key, pinned := pinnedBy[v]; pinned {
				plan.Keep[v] = key
			}
		}
		for _, v := range live {
			if key, pinned := pinnedBy[v]; pinned {
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

	// R5.1 and its no-entry twin: report the candidates, delete nothing.
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
