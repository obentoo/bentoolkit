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

// existingSuffixRegex matches a version that already carries a Gentoo suffix,
// with or without a trailing revision (1.2.3_pre, 1.2.3_rc1-r2).
var existingSuffixRegex = regexp.MustCompile(`_(alpha|beta|pre|rc|p)[0-9]*(-r[0-9]+)?$`)

// applySuffix appends cfg.Suffix to v when the record declares a pre-release
// channel, i.e. when the version upstream publishes is a development release its
// own numbering does not mark (LibreOffice's testing line ships a plain
// "26.8.0.1"). With cfg.SuffixWhen set the suffix applies only to a version
// matching that regex, which is how one endpoint listing several release lines
// marks the development one alone.
//
// It is idempotent in two ways, because the pipeline applies it both per
// candidate (inside selectVersion, so "max" orders the values that will actually
// become the PV) and once on the final version (in fetchUpstreamVersion, which
// also covers the script/fallback/LLM paths): a version that already ends in a
// suffix is returned untouched, so neither a second pass nor an upstream string
// that is already marked (2.0.0_rc1) can stack into "2.0.0_rc1_pre".
//
// A malformed suffix_when is warned and ignored rather than fatal: the record is
// rejected up front by ValidatePackageConfig, and a check that got this far must
// not die on the annotation.
func applySuffix(v string, cfg *PackageConfig) string {
	if cfg == nil || cfg.Suffix == "" || v == "" {
		return v
	}
	if existingSuffixRegex.MatchString(v) {
		return v
	}
	if cfg.SuffixWhen != "" {
		re, err := regexp.Compile(cfg.SuffixWhen)
		if err != nil {
			warnLogf("suffix_when: bad regex %q: %v; suffix not applied", cfg.SuffixWhen, err)
			return v
		}
		if !re.MatchString(v) {
			return v
		}
	}
	return v + cfg.Suffix
}

// selectVersion picks one version from a candidate list according to cfg.Select.
// Each candidate is transformed BEFORE validation/comparison, because a raw
// candidate such as "7.1.2-24" is not a valid Gentoo version until the transform
// runs — selecting before transforming would discard every candidate. The
// pre-release suffix is applied next, so candidates are ordered as the versions
// they will become: a record marking one release line as _pre must compare that
// line's _pre value, not the bare one.
//
//   - "max":  the highest comparable Gentoo version.
//   - "last": the last candidate that is a comparable version (document order).
//
// Non-comparable candidates (per ebuild.IsValidVersion, after transform and
// prefix stripping) are skipped. Returns "" when no candidate is comparable.
func selectVersion(cands []string, cfg *PackageConfig) string {
	var transform [][]string
	mode, series := "", ""
	if cfg != nil {
		transform = cfg.Transform
		mode = cfg.Select
		series = cfg.Series
	}
	matcher := newSeriesMatcher(series)
	best := ""
	for _, c := range cands {
		c = applyTransforms(strings.TrimSpace(c), transform)
		cc := applySuffix(stripVersionPrefix(c), cfg)
		if !ebuild.IsValidVersion(cc) {
			continue
		}
		// An entry restricted to a release line must not select outside it: with
		// two entries per package, the other line has its own entry, and "max"
		// over the whole listing would make both chase the same version.
		if !matcher.matches(cc) {
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
