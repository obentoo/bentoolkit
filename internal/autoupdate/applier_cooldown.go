package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// cooldownState is the run's cooldown block, shared by every package an
// Applier promotes. The apply command loads and sweeps it, then hands it over
// with WithApplierCooldown; an Applier given none loads it from the overlay
// on first use. Every read and write of the block runs under mu: ApplyAll
// promotes packages concurrently, and Add+Save must not interleave.
type cooldownState struct {
	mu     sync.Mutex
	loaded bool
	block  *cooldown.Block // nil = cooldown disabled for this overlay
	err    error           // the block could not be loaded or saved: fail closed
}

// WithApplierCooldown hands the Applier the run's cooldown block, already
// swept by the caller. A nil block with a nil loadErr means cooldown is off
// for this overlay. A non-nil loadErr refuses every promotion that needs an
// entry (R7.3) while the rest of the run continues.
func WithApplierCooldown(block *cooldown.Block, loadErr error) ApplierOption {
	return func(a *Applier) {
		a.cooldown = &cooldownState{loaded: true, block: block, err: loadErr}
	}
}

// WithApplierNowFunc injects the clock that stamps cooldown entries. The
// default is time.Now; a nil fn is ignored.
func WithApplierNowFunc(fn func() time.Time) ApplierOption {
	return func(a *Applier) {
		if fn != nil {
			a.nowFn = fn
		}
	}
}

func (a *Applier) now() time.Time {
	if a.nowFn != nil {
		return a.nowFn()
	}
	return time.Now()
}

// cooldownLocked returns the run's block, loading it on first use. Callers
// hold a.cooldown.mu.
func (a *Applier) cooldownLocked() (*cooldown.Block, error) {
	s := a.cooldown
	if !s.loaded {
		s.block, s.err = cooldown.Load(a.overlayPath)
		s.loaded = true
	}
	return s.block, s.err
}

// CooldownGroup is the sorted set {pkg} ∪ cooldown_with joined by ",", or
// empty without cooldown_with. The registry enforces a clique, so every member
// derives the same string.
func CooldownGroup(pkg string, cfg registry.PackageConfig) string {
	if len(cfg.CooldownWith) == 0 {
		return ""
	}
	members := append([]string{pkg}, cfg.CooldownWith...)
	slices.Sort(members)
	return strings.Join(slices.Compact(members), ",")
}

// cooldownMask masks the version about to be promoted (R1.1): it runs after
// the promotion decision and before the candidate reaches the published
// overlay. An error refuses the promotion. ebuildPath is the candidate's
// ebuild (staged or in place), read for the unkeyworded exemption.
//
// The new version is the ONE candidate of its release line — the registry
// record key, so app-editors/zed@stable and @preview are separate lines. Any
// other masked version of the same line is SUPERSEDED: it is left masked here
// and listed on the result, and completeApply retires it (ebuild removed, then
// entry dropped) only once the promotion completed. Dropping it now would
// leave the old ebuild visible and unmasked if the promotion failed.
//
// This is a DELIBERATE EXCEPTION to completeApply's warn-only rule: a bump
// published unmasked because its cooldown entry could not be written is the
// exact failure the cooldown exists to prevent, so here a write error or an
// unreadable block fails closed (R1.3, R7.3).
func (a *Applier) cooldownMask(key, version, ebuildPath string, result *ApplyResult) error {
	cfg := a.configs[key]
	if cfg.CooldownExempt != nil {
		return nil // R5.1, even under a malformed block: no entry is needed
	}
	category, name, ok := ebuilds.SplitPkgAtom(key)
	if !ok {
		return fmt.Errorf("cooldown: %q is not a category/package atom", key)
	}
	pkg := category + "/" + name
	if a.graduating {
		result.CooldownSkipped = "graduation"
		return nil // already the cooldown's days old upstream
	}
	if reason := a.cooldownAutoExempt(pkg, key, name, version, ebuildPath); reason != "" {
		result.CooldownSkipped = reason
		a.logger().Info("cooldown: promoted unmasked", "package", pkg, "version", version, "reason", reason)
		return nil
	}

	a.cooldown.mu.Lock()
	defer a.cooldown.mu.Unlock()
	block, err := a.cooldownLocked()
	if err != nil {
		return fmt.Errorf("cooldown: refusing to promote %s-%s unmasked: %w", pkg, version, err)
	}
	if block == nil {
		return nil // R1.4: no block, no cooldown
	}
	if err := cooldown.ValidateAtom(pkg, version); err != nil {
		return fmt.Errorf("cooldown: refusing to promote %s-%s: %w", pkg, version, err)
	}
	var superseded []string
	line := block.LineEntries(pkg, key)
	for _, e := range line {
		if e.Version != version {
			superseded = append(superseded, e.Version)
		}
	}
	entry := cooldown.Entry{
		Package: pkg,
		Version: version,
		Since:   a.now().UTC().Truncate(time.Second),
		Origin:  cooldown.OriginAutoupdate,
		Group:   CooldownGroup(pkg, cfg),
		Line:    key,
		First:   cooldown.LineStart(line),
		Days:    cfg.CooldownDays,
	}
	if !entry.First.IsZero() && entry.First.After(entry.Since) {
		entry.First = entry.Since
	}
	// A snapshot line whose installable version graduates on its own (see
	// graduate.go) is not bounded by the line's wait: the bound would unmask
	// a commit that is a day old, the very thing graduation avoids. Its
	// candidate starts a fresh line every time.
	if _, ok := graduationURL(cfg.URL, entry.Since); ok && cfg.Track == "commit" {
		entry.First = time.Time{}
	}
	added := block.Add(entry)
	if err := block.Save(a.overlayPath); err != nil {
		// The block in memory may now disagree with the disk; every later
		// promotion of this run that needs an entry is refused too.
		a.cooldown.err = fmt.Errorf("an earlier cooldown save failed: %w", err)
		return fmt.Errorf("cooldown: refusing to promote %s-%s unmasked: %w", pkg, version, err)
	}
	result.CooldownSuperseded = superseded
	if added {
		result.CooldownAdded = append(result.CooldownAdded, entry)
		a.logger().Info("cooldown: entry added", "package", pkg, "version", version, "line", key,
			"eligible", block.EntryEligible(entry).UTC().Format(time.RFC3339))
	}
	return nil
}

// cooldownAutoExempt returns why the version needs no cooldown entry, or "".
//
//   - --urgent named the package or its record: a security fix must not wait.
//   - ::gentoo ships the very same version: the overlay copy (a PYTHON_COMPAT
//     mirror, say) adds no release ::gentoo users do not already get, so
//     holding it back protects nobody.
//   - the ebuild carries no KEYWORDS: Portage already refuses it without an
//     explicit ** acceptance, which is a stronger gate than the cooldown.
func (a *Applier) cooldownAutoExempt(pkg, key, name, version, ebuildPath string) string {
	if a.urgent[pkg] || a.urgent[key] {
		return "urgent"
	}
	if a.gentooPath != "" {
		category, _, _ := ebuilds.SplitPkgAtom(pkg)
		if _, err := os.Stat(filepath.Join(a.gentooPath, category, name, name+"-"+version+".ebuild")); err == nil {
			return "same version in ::gentoo"
		}
	}
	if ebuildPath != "" && !ebuildHasKeywords(ebuildPath) {
		return "unkeyworded"
	}
	return ""
}

// keywordsAssignRe matches an uncommented KEYWORDS assignment with a value.
var keywordsAssignRe = regexp.MustCompile(`(?m)^[ \t]*KEYWORDS="[^"]*[^"\s][^"]*"`)

// ebuildHasKeywords reports whether the ebuild assigns a non-empty KEYWORDS
// anywhere outside a comment. A conditional assignment (the usual 9999 /
// release split) counts. An unreadable ebuild counts as keyworded, so a read
// failure never turns into a skipped cooldown.
func ebuildHasKeywords(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	return keywordsAssignRe.Match(data)
}

// retireSuperseded removes the line candidates this promotion superseded:
// first their ebuilds (and the Manifest's distfiles with them), then their
// block entries. It runs only after a completed promotion and is warn-only,
// like the rest of completeApply: a version that could not be removed simply
// stays masked, and the next apply or sweep retries — its entry is dropped
// only once its ebuild is gone.
func (a *Applier) retireSuperseded(ctx context.Context, pkg, newVersion string, result *ApplyResult) {
	if len(result.CooldownSuperseded) == 0 {
		return
	}
	category, name, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return
	}
	atom := category + "/" + name
	var remove []string
	for _, v := range result.CooldownSuperseded {
		if v != newVersion {
			remove = append(remove, v)
		}
	}
	slices.SortFunc(remove, ebuild.CompareVersions)
	plan, err := a.sweeper().execute(ctx, atom, sweepPlan{Remove: remove}, newVersion)
	if err != nil {
		a.logger().Warn("cooldown: superseded candidate not fully removed; it stays masked",
			"package", atom, "versions", strings.Join(remove, ","), "err", err)
	}
	gone := plan.Remove
	for _, v := range remove {
		if _, statErr := os.Stat(a.sweeper().ebuildPath(atom, v)); errors.Is(statErr, os.ErrNotExist) && !slices.Contains(gone, v) {
			gone = append(gone, v) // already absent: its entry can go too
		}
	}
	if len(gone) == 0 {
		return
	}
	a.cooldown.mu.Lock()
	defer a.cooldown.mu.Unlock()
	block, err := a.cooldownLocked()
	if err != nil || block == nil {
		return
	}
	for _, v := range gone {
		block.Remove(atom, v)
	}
	if err := block.Save(a.overlayPath); err != nil {
		a.cooldown.err = fmt.Errorf("an earlier cooldown save failed: %w", err)
		a.logger().Warn("cooldown: superseded entries removed from disk but the block could not be saved; the sweep drops them as stale",
			"package", atom, "err", err)
		return
	}
	result.CooldownRetired = append(result.CooldownRetired, gone...)
	for _, v := range gone {
		a.logger().Info("cooldown: superseded candidate retired", "package", atom, "version", v, "by", newVersion)
	}
}

// cooldownHeldFor returns the versions of atom the run's block holds in the
// tree: what --clean must keep. cfgs are the registry records, whose release
// lines each keep their own predecessor. A block that cannot be read is an
// error, so the sweep refuses rather than delete what might be held.
func (a *Applier) cooldownHeldFor(atom string, cfgs map[string]registry.PackageConfig) ([]string, error) {
	a.cooldown.mu.Lock()
	defer a.cooldown.mu.Unlock()
	block, err := a.cooldownLocked()
	if err != nil {
		return nil, fmt.Errorf("the cooldown block cannot be read, so nothing is swept: %w", err)
	}
	return heldVersions(a.logger(), a.overlayPath, block, cfgs)[atom], nil
}

// heldVersions returns, per package with an entry, the versions that must
// stay in the tree (R2.1): Block.Held over the directory — every masked
// version and the predecessor below the oldest — plus, for each registry
// record (release line: ":slot" and series) holding a masked version, the
// highest unmasked version of THAT line below its oldest masked one. A
// directory with two lines under cooldown at once keeps one installable
// version per line. nil for a nil block.
func heldVersions(log *slog.Logger, overlayPath string, block *cooldown.Block, cfgs map[string]registry.PackageConfig) map[string][]string {
	if block == nil {
		return nil
	}
	held := block.Held(func(pkg string) []string {
		category, name, ok := ebuilds.SplitPkgAtom(pkg)
		if !ok {
			return nil
		}
		paths, err := ebuilds.FindEbuilds(filepath.Join(overlayPath, category, name))
		if err != nil {
			return nil // no directory: nothing present to hold
		}
		_, nonLive := splitEbuildVersions(paths, category, name)
		return nonLive
	})
	masked := map[string][]string{}
	for _, e := range block.Entries {
		masked[e.Package] = append(masked[e.Package], e.Version)
	}
	for _, key := range slices.Sorted(maps.Keys(cfgs)) {
		category, name, ok := ebuilds.SplitPkgAtom(key)
		if !ok {
			continue
		}
		atom := category + "/" + name
		if len(masked[atom]) == 0 {
			continue
		}
		line, err := ebuilds.LineVersions(log, overlayPath, key, cfgs[key].Series)
		if err != nil {
			continue // unreadable directory: planSweep fails on it anyway
		}
		if pred := linePredecessor(line, masked[atom]); pred != "" && !slices.Contains(held[atom], pred) {
			held[atom] = append(held[atom], pred)
			slices.SortFunc(held[atom], ebuild.CompareVersions)
		}
	}
	return held
}

// linePredecessor is the highest version of line that is not masked and lies
// below the oldest masked version OF THE LINE; "" when the line has no masked
// version or nothing below it.
func linePredecessor(line, masked []string) string {
	var inLine []string
	for _, v := range line {
		if slices.Contains(masked, v) {
			inLine = append(inLine, v)
		}
	}
	if len(inLine) == 0 {
		return ""
	}
	oldest := slices.MinFunc(inLine, ebuild.CompareVersions)
	pred := ""
	for _, v := range line {
		if slices.Contains(masked, v) || ebuild.CompareVersions(v, oldest) >= 0 {
			continue
		}
		if pred == "" || ebuild.CompareVersions(v, pred) > 0 {
			pred = v
		}
	}
	return pred
}

// loadHeldVersions loads the overlay's block and returns heldVersions for it.
func loadHeldVersions(log *slog.Logger, overlayPath string, cfgs map[string]registry.PackageConfig) (map[string][]string, error) {
	block, err := cooldown.Load(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("the cooldown block cannot be read, so nothing is swept or reported unclaimed: %w", err)
	}
	return heldVersions(log, overlayPath, block, cfgs), nil
}
