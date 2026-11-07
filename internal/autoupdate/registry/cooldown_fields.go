package registry

import (
	"fmt"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

// keyAtom is recordAtom without the well-formedness flag: a malformed key
// is refused by validatePackageKey, and here it simply matches nothing.
func keyAtom(key string) string {
	atom, _ := recordAtom(key)
	return atom
}

// validateCooldownFields checks one record's cooldown fields: a non-empty
// cooldown_exempt reason, and a cooldown_with of sorted, distinct, bare
// cat/pkg atoms that never names the record's own package.
func validateCooldownFields(pkg string, cfg *PackageConfig) error {
	if cfg.CooldownExempt != nil && strings.TrimSpace(*cfg.CooldownExempt) == "" {
		return fmt.Errorf("package %s: cooldown_exempt must say why the package skips the cooldown", pkg)
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
