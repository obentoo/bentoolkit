package autoupdate

import (
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
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

// cooldownGroup is the sorted set {pkg} ∪ cooldown_with joined by ",", or
// empty without cooldown_with. The registry enforces a clique, so every member
// derives the same string.
func cooldownGroup(pkg string, cfg registry.PackageConfig) string {
	if len(cfg.CooldownWith) == 0 {
		return ""
	}
	members := append([]string{pkg}, cfg.CooldownWith...)
	slices.Sort(members)
	return strings.Join(slices.Compact(members), ",")
}

// cooldownMask masks the version about to be promoted (R1.1): it runs after
// the promotion decision and before the candidate reaches the published
// overlay. An error refuses the promotion.
//
// This is a DELIBERATE EXCEPTION to completeApply's warn-only rule: a bump
// published unmasked because its cooldown entry could not be written is the
// exact failure the cooldown exists to prevent, so here a write error or an
// unreadable block fails closed (R1.3, R7.3).
func (a *Applier) cooldownMask(key, version string, result *ApplyResult) error {
	cfg := a.configs[key]
	if cfg.CooldownExempt != nil {
		return nil // R5.1, even under a malformed block: no entry is needed
	}
	category, name, ok := ebuilds.SplitPkgAtom(key)
	if !ok {
		return fmt.Errorf("cooldown: %q is not a category/package atom", key)
	}
	pkg := category + "/" + name

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
	entry := cooldown.Entry{
		Package: pkg,
		Version: version,
		Since:   a.now().UTC().Truncate(time.Second),
		Origin:  cooldown.OriginAutoupdate,
		Group:   cooldownGroup(pkg, cfg),
	}
	added := block.Add(entry)
	if err := block.Save(a.overlayPath); err != nil {
		// The block in memory may now disagree with the disk; every later
		// promotion of this run that needs an entry is refused too.
		a.cooldown.err = fmt.Errorf("an earlier cooldown save failed: %w", err)
		return fmt.Errorf("cooldown: refusing to promote %s-%s unmasked: %w", pkg, version, err)
	}
	if added {
		result.CooldownAdded = append(result.CooldownAdded, entry)
		a.logger().Info("cooldown: entry added", "package", pkg, "version", version,
			"eligible", block.Eligible(pkg).UTC().Format(time.RFC3339))
	}
	return nil
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
