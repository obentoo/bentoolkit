// Package autoupdate provides post-extraction version transformation and
// multi-candidate selection for ebuild autoupdate.
package autoupdate

import (
	"regexp"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// applyTransforms applies ordered regex substitutions to an extracted version.
// Each rule is [regex, repl]; repl follows regexp.ReplaceAllString semantics.
// A malformed rule (wrong arity or uncompilable regex) is warned and skipped,
// so a single bad rule never aborts a check (ValidatePackageConfig warns too).
func applyTransforms(v string, rules [][]string) string {
	for _, r := range rules {
		if len(r) != 2 {
			continue
		}
		re, err := regexp.Compile(r[0])
		if err != nil {
			warnLogf("transform: bad regex %q: %v", r[0], err)
			continue
		}
		v = re.ReplaceAllString(v, r[1])
	}
	return v
}

// selectVersion picks one version from a candidate list according to mode.
// Each candidate is transformed BEFORE validation/comparison, because a raw
// candidate such as "7.1.2-24" is not a valid Gentoo version until the transform
// runs — selecting before transforming would discard every candidate.
//
//   - "max":  the highest comparable Gentoo version.
//   - "last": the last candidate that is a comparable version (document order).
//
// Non-comparable candidates (per ebuild.IsValidVersion, after transform and
// prefix stripping) are skipped. Returns "" when no candidate is comparable.
func selectVersion(cands []string, transform [][]string, mode string) string {
	best := ""
	for _, c := range cands {
		c = applyTransforms(strings.TrimSpace(c), transform)
		cc := stripVersionPrefix(c)
		if !ebuild.IsValidVersion(cc) {
			continue
		}
		switch mode {
		case "last":
			best = cc
		case "max":
			if best == "" || ebuild.CompareVersions(cc, best) > 0 {
				best = cc
			}
		}
	}
	return best
}
