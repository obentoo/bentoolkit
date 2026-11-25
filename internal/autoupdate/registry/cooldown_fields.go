package registry

import (
	"fmt"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

// maxCooldownDays mirrors cooldown.MaxDays; registry cannot import the
// cooldown package (it is the lower layer), so the bound is restated here.
const maxCooldownDays = 3650

// keyAtom is recordAtom without the well-formedness flag: a malformed key
// is refused by validatePackageKey, and here it simply matches nothing.
func keyAtom(key string) string {
	atom, _ := recordAtom(key)
	return atom
}

// CooldownOff reports whether the record is out of the cooldown
// (cooldown_days = 0): its promotions land unmasked.
func (c *PackageConfig) CooldownOff() bool {
	return c.CooldownDays != nil && *c.CooldownDays == 0
}

// CooldownDaysOverride is the record's own wait in days, or 0 when it takes the
// block's days=N. A record out of the cooldown also answers 0; ask CooldownOff
// first.
func (c *PackageConfig) CooldownDaysOverride() int {
	if c.CooldownDays == nil {
		return 0
	}
	return *c.CooldownDays
}

// validateCooldownFields checks one record's cooldown fields: a cooldown_days
// from 0 to the bound that always carries a cooldown_reason (and a reason that
// always has an override to justify), and a cooldown_with of sorted, distinct,
// bare cat/pkg atoms that never names the record's own package.
func validateCooldownFields(pkg string, cfg *PackageConfig) error {
	if cfg.CooldownDays != nil {
		if d := *cfg.CooldownDays; d < 0 || d > maxCooldownDays {
			return fmt.Errorf("package %s: cooldown_days = %d must be a whole number of days from 0 (no cooldown) to %d", pkg, d, maxCooldownDays)
		}
		if cfg.CooldownReason == nil || strings.TrimSpace(*cfg.CooldownReason) == "" {
			return fmt.Errorf("package %s: cooldown_days needs a cooldown_reason saying why the package does not wait the block's days", pkg)
		}
	} else if cfg.CooldownReason != nil {
		return fmt.Errorf("package %s: cooldown_reason without cooldown_days justifies nothing: set cooldown_days, or drop the reason", pkg)
	}
	if cfg.CooldownOff() && len(cfg.CooldownWith) > 0 {
		return fmt.Errorf("package %s: cooldown_days = 0 cannot be combined with cooldown_with: a group is masked and released together, so no member can skip the cooldown alone", pkg)
	}
	self := keyAtom(pkg)
	for i, t := range cfg.CooldownWith {
		if strings.ContainsAny(t, ":@") {
			return fmt.Errorf("package %s: cooldown_with entry %q must be a bare cat/pkg (no slot, no label)", pkg, t)
		}
		if _, _, err := ebuilds.ParsePkgAtom(t); err != nil {
			return fmt.Errorf("package %s: cooldown_with entry %q: %w", pkg, t, err)
		}
		if t == self {
			return fmt.Errorf("package %s: cooldown_with names %s itself", pkg, self)
		}
		if i > 0 && cfg.CooldownWith[i-1] >= t {
			return fmt.Errorf("package %s: cooldown_with must be sorted with no duplicate, got %v", pkg, cfg.CooldownWith)
		}
	}
	return nil
}

// validateCooldownGroups checks cooldown_with across records: every target has
// a record, and every group is a clique — each member's record lists every
// other member — so the applier derives the same group from any member. The
// error names both records of the first missing link.
func validateCooldownGroups(pkgs map[string]PackageConfig) error {
	byAtom := map[string][]string{}
	for _, key := range sortedKeys(pkgs) {
		byAtom[keyAtom(key)] = append(byAtom[keyAtom(key)], key)
	}
	for _, key := range sortedKeys(pkgs) {
		cfg := pkgs[key]
		if len(cfg.CooldownWith) == 0 {
			continue
		}
		self := keyAtom(key)
		group := append([]string{self}, cfg.CooldownWith...)
		for _, target := range cfg.CooldownWith {
			records, ok := byAtom[target]
			if !ok {
				return fmt.Errorf("package %s: cooldown_with names %s, which has no record in the registry", key, target)
			}
			for _, other := range records {
				otherCfg := pkgs[other]
				if a, b := cfg.CooldownDaysOverride(), otherCfg.CooldownDaysOverride(); a != b {
					return fmt.Errorf("package %s: cooldown_days differs from %s's (%d vs %d; 0 is the block's): a cooldown_with group is released whole, so its members share one wait",
						key, other, a, b)
				}
				have := append([]string{target}, pkgs[other].CooldownWith...)
				for _, member := range group {
					if member != target && !slices.Contains(have, member) {
						return fmt.Errorf("package %s: cooldown_with must list %s: %s groups %s, and every member of a cooldown group must list every other member",
							other, member, key, strings.Join(sortedCopy(group), ", "))
					}
				}
			}
		}
	}
	return nil
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
