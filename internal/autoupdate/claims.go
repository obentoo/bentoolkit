package autoupdate

import (
	"errors"
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
//  4. R4.1 — every version a claiming entry HOLDS and that is present on disk is
//     kept, recorded against the entry holding it; everything else non-live is
//     removed. "Holds" is the union of both halves of a claim — the version the
//     entry PINS and the version it RESOLVES to — exactly as unclaimedIn
//     computes it. See "Why the resolved half" below: that half is what stops a
//     batch apply deleting the release line it created seconds earlier.
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
// as kept — the report must not claim a file that is not there. What happens to
// the directory then depends on what the entry still resolves to: some OTHER
// ebuild, which rule 4's resolved half keeps in that entry's name, or nothing at
// all, in which case rule 5 is the only thing between the directory and being
// emptied. Either way the drift (registry says 2.0.0, overlay has 1.0.0) is what
// a later reconciliation is meant to repair.
//
// An unreadable — or absent — package directory is an error, never an empty
// plan: an empty plan reads as "nothing to keep", which is one caller away from
// "remove everything".
//
// # Why the resolved half
//
// An entry that resolves to a file is holding it, pin or no pin. That is this
// package's stated rule — unclaimedIn says so in as many words and takes the
// same union — and rule 4 is the one place that used to ignore it, which is
// exactly the place that deletes files.
//
// `--apply all --clean` builds ONE Applier whose registry snapshot is taken at
// construction and never reloaded, and cleanPackageDir freshens the pin of the
// entry being applied ONLY (sweepConfigs). So while @dev is being applied, its
// @stable sibling is planned against the pin the run STARTED with — which the
// @stable apply, moments earlier in the same command, has already made stale.
// Claiming by pin alone, that sibling's brand-new ebuild is held by nobody and
// goes straight into Remove: UB3 broken, Success still true, no CleanWarning
// printed, and the registry left pinning a file that no longer exists. Reverse
// the order and it is the dev line that dies instead; either order loses one.
// The identical drift is reachable without a batch — R4.4 deliberately tolerates
// a failed pin write, and any out-of-band bump (a hand edit, pkgdev, a `git
// pull` of the overlay from another machine) leaves the same stale pin behind.
//
// So the keep-set is deliberately wider than R4.1 read literally, in the same
// direction and for the same reason rule 2 is: keeping one ebuild too many costs
// a directory that stays dirty one more run, keeping one too few costs a
// maintained release line — and 90 directories in the overlay have one to lose.
// Do not "simplify" it back to the pin alone.
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

	// Rule 1: collect what each entry holds, and the first entry that can say
	// nothing about what it holds.
	claims := resolveClaims(overlayPath, cfgs, atom)
	// heldBy maps a version to the entry holding it — by pin OR by resolution,
	// see rule 4. It is deliberately NOT named pinnedBy: the pin is only half of
	// what an entry holds, and treating it as the whole is the bug that deleted
	// the sibling release line a batch apply had just created.
	heldBy := make(map[string]string)
	// Claims arrive in key order, so first-writer-wins below is stable across
	// runs rather than a map-iteration coin flip.
	for _, c := range claims {
		if c.Pin == "" {
			if plan.Blocked == "" {
				plan.Blocked = c.Key
			}
			// R5.1/D3: a pinless entry blocks the whole directory, so its
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

// DivergenceKind classifies one disagreement between the registry and the
// overlay. The three kinds are R3.1's three classes and they are NOT
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
	// the ~317-entry bulk fill of A1, and the only class the write batch is
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
	// Disk is always empty, so there is nothing to write (UB4: the registry
	// never holds a version that is not on disk) and reporting it is the whole
	// of the action. The existing orphan reconciliation, not this one, is what
	// acts on a removed package (R3.5).
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
	// Kind is which of R3.1's three classes this is.
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
// to on disk, for the whole registry, and returns the divergences in R3.1's
// three classes.
//
// # Why this returns data instead of writing it
//
// The registry is a published artifact. ~/Projetos/git/bentoo auto-commits and
// pushes, so an unattended write reaches origin within minutes — a wrong pin is
// not a local mistake to be fixed before anyone sees it, it is a released one.
// That is why this function only ever reads: it hands the whole divergence set
// back so a caller can show it and take ONE confirmation covering all of it
// (R3.2), leave packages.toml byte-identical when the answer is no (R3.3), and
// refuse to write at all from a non-TTY without --yes (R3.4). Nothing here
// touches packages.toml, and nothing here should ever start to.
//
// # What is compared, and what is skipped
//
// A disabled (enabled = false) entry is skipped: the existing overlay-driven
// status reconciliation in CheckAll owns it, and S021-R3.5 requires this to
// leave it alone. A held (hold = true) entry is NOT skipped — see the skip
// itself for why the two were never the same reason (S026-R3.2). A disabled
// entry's ebuild is NOT thereby unclaimed — a switched-off entry still holds
// its file (see resolveClaims) — so the unclaimed scan below counts every entry
// of a directory, disabled ones included, while only enabled ones can be the
// SUBJECT of a divergence. The two functions differ deliberately on this point;
// do not unify them.
//
// Resolution goes through selectCurrentEbuild, so ":slot" and `series` are
// filtered by the one implementation the checker and the sweep use (D2). Its
// sentinels — ErrNoEbuildFound, ErrSlotNotFound, ErrSeriesNotFound — are facts
// about one entry rather than failures, and they are precisely R3.1's third
// class, so they land in NoEbuild. Any OTHER error is a directory that could not
// be read: that entry is skipped with a warning naming it, and NO divergence is
// invented from it. A fabricated divergence here becomes a fabricated pin in the
// registry, published.
//
// The pin is compared to the resolved version as an exact string, not through
// ebuild.CompareVersions, because an exact string is what the sweep matches on
// (planSweep's heldBy map). A pin the sweep cannot match keeps no file, so
// declaring it "not stale" because it compares equal would leave the registry
// pinning a version that does not protect its ebuild.
//
// The result is sorted, so the prompt a maintainer reads is the same list in the
// same order on two consecutive runs and a diff between them means the overlay
// changed.
//
// # Cost
//
// One directory read per enabled entry for the resolution, one per distinct
// directory for the unclaimed scan, and one more per entry of that directory to
// collect its claims: roughly 2-3 readdir per enabled entry, not the single one
// design.md's performance note assumes. That note asks for the checker's
// CheckResult data to be reused instead, which this signature cannot do — it
// takes no results — and which would not remove the scan anyway: CheckResult
// carries a resolved CurrentVersion but never the directory listing that
// UnclaimedEbuild is computed from.
func Reconcile(overlayPath string, cfgs map[string]PackageConfig) []Divergence {
	var divs []Divergence
	// Directories already scanned for unclaimed ebuilds, keyed by atom: several
	// entries routinely share one, and the finding belongs to the file, not to
	// each sibling that happens to live next to it.
	scanned := make(map[string]bool)

	for _, key := range sortedKeys(cfgs) {
		cfg := cfgs[key]
		// enabled = false is the ONLY skip here, and the two conditions this
		// once bundled were never the same reason (S026-R3.2):
		//
		//   - a disabled entry is skipped because there is nothing to record —
		//     that flag is the checker's own bookkeeping for "the ebuild
		//     vanished from the overlay" — and the overlay-driven status
		//     reconciliation in CheckAll owns the entry (S026-R3.1, S021-R3.5);
		//   - a HELD entry is skipped by the CHECKER, which must not auto-bump
		//     it. That is a statement about fetching a new version, and it says
		//     nothing about recording the one already on disk: hold means
		//     "present, but do not auto-bump", so the file IS there and writing
		//     down which version it is second-guesses no maintainer decision.
		//     It is therefore compared like any other entry (S026-R1.1) and its
		//     hold is never written back (S026-R2.1).
		if !cfg.IsEnabled() {
			continue
		}
		category, pkgName, ok := splitPkgAtom(key)
		if !ok {
			warnLogf("reconcile: skipping registry key %q: it is not a category/package atom", key)
			continue
		}

		cand, err := selectCurrentEbuild(overlayPath, key, cfg.Series)
		switch {
		case err == nil:
			if cand.Version != cfg.Version {
				divs = append(divs, Divergence{
					Key: key, Kind: StalePin, Pin: cfg.Version, Disk: cand.Version,
				})
			}
		case errors.Is(err, ErrNoEbuildFound),
			errors.Is(err, ErrSlotNotFound),
			errors.Is(err, ErrSeriesNotFound):
			divs = append(divs, Divergence{Key: key, Kind: NoEbuild, Pin: cfg.Version})
		default:
			// An unreadable directory, and nothing else: every "this entry holds
			// nothing" case is a sentinel above. Say which entry and why, and
			// report nothing about it.
			warnLogf("reconcile: skipping %s: %v", key, err)
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
		if errors.Is(err, ErrNoEbuildFound) {
			continue
		}
		scanned[atom] = true
		divs = append(divs, unclaimedIn(overlayPath, cfgs, atom)...)
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
		// Two versions the comparison calls equal ("1.0" and "1.0.0") are still
		// two files; order them by their text so the order stays total.
		return a.Disk < b.Disk
	})
	return divs
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
// Live -9999 ebuilds are never reported: selection skips them (UB2), so no pin
// can ever name one, so "no entry claims it" is true of every live ebuild in the
// overlay and means nothing. Reporting them would put the one file that cannot
// be restored by re-fetching a release at the top of a removal candidate list.
//
// An unreadable directory yields a warning and no divergences at all, never a
// guess about what is in it.
func unclaimedIn(overlayPath string, cfgs map[string]PackageConfig, atom string) []Divergence {
	category, pkgName, ok := splitPkgAtom(atom)
	if !ok {
		return nil
	}
	// Built from the split components, never from the raw key.
	pkgDir := filepath.Join(overlayPath, category, pkgName)
	paths, err := findEbuilds(pkgDir)
	if err != nil {
		warnLogf("reconcile: skipping the unclaimed-ebuild scan of %s/%s: %v", category, pkgName, err)
		return nil
	}

	// Claims are collected from EVERY entry of the atom, disabled and held
	// included: R3.5 says a switched-off entry is not a divergence to report,
	// not that its ebuild belongs to nobody. resolveClaims is the one place that
	// knows who holds what; re-deriving it here would give the report a second,
	// drifting answer to that question.
	claimed := make(map[string]bool)
	for _, c := range resolveClaims(overlayPath, cfgs, atom) {
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
