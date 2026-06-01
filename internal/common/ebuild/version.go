package ebuild

import (
	"regexp"
	"strconv"
	"strings"
)

// Version suffix priorities (lower = earlier in release cycle)
var suffixPriority = map[string]int{
	"alpha": -4,
	"beta":  -3,
	"pre":   -2,
	"rc":    -1,
	"":      0, // release version
	"p":     1, // patch
}

// versionSuffixRegex matches suffixes like _rc1, _beta2, _alpha, _p1
var versionSuffixRegex = regexp.MustCompile(`_([a-z]+)(\d*)`)

// revisionRegex matches -r1, -r2, etc.
var revisionRegex = regexp.MustCompile(`-r(\d+)$`)

// parseVersion breaks a version string into components for comparison
// Returns: numeric parts, suffix type, suffix num, revision num
func parseVersion(v string) ([]int, string, int, int) {
	// Extract revision first (-r1, -r2, etc.)
	revision := 0
	if matches := revisionRegex.FindStringSubmatch(v); matches != nil {
		revision, _ = strconv.Atoi(matches[1]) //nolint:errcheck // invalid input treated as zero by design
		v = revisionRegex.ReplaceAllString(v, "")
	}

	// Extract suffix (_rc1, _beta2, etc.)
	suffixType := ""
	suffixNum := 0
	if matches := versionSuffixRegex.FindStringSubmatch(v); matches != nil {
		suffixType = matches[1]
		if matches[2] != "" {
			suffixNum, _ = strconv.Atoi(matches[2]) //nolint:errcheck // invalid input treated as zero by design
		}
		v = versionSuffixRegex.ReplaceAllString(v, "")
	}

	// Parse numeric parts (1.0.1 -> [1, 0, 1])
	parts := strings.Split(v, ".")
	nums := make([]int, len(parts))
	for i, p := range parts {
		// Handle letter suffixes in version numbers (e.g., 1.0a -> 1, 0)
		numStr := strings.TrimRight(p, "abcdefghijklmnopqrstuvwxyz")
		if numStr == "" {
			nums[i] = 0
		} else {
			nums[i], _ = strconv.Atoi(numStr) //nolint:errcheck // invalid input treated as zero by design
		}
	}

	return nums, suffixType, suffixNum, revision
}

// compareIntSlices compares two slices of integers
func compareIntSlices(a, b []int) int {
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}

	for i := 0; i < maxLen; i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}

		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

// validVersionRegex matches a well-formed Gentoo-style version: a numeric
// component chain (1.2.3), an optional single trailing letter (1.2.3a), zero or
// more recognized suffixes (_alpha/_beta/_pre/_rc/_p with an optional number),
// and an optional revision (-r1).
var validVersionRegex = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*[a-z]?(_(alpha|beta|pre|rc|p)[0-9]*)*(-r[0-9]+)?$`)

// IsValidVersion reports whether v is a well-formed Gentoo-style version string
// that CompareVersions can order meaningfully.
//
// This matters because parseVersion is deliberately lenient: it coerces any
// unparseable component to 0, so strings like "INKSCAPE_1_4_4", "latest", or a
// still-prefixed "v6.6.91" silently parse to a near-zero version and compare as
// *older* than any real version. Callers that compare an upstream value against
// the current ebuild version should reject non-comparable inputs up front
// rather than treat them as "no update available".
func IsValidVersion(v string) bool {
	return validVersionRegex.MatchString(strings.TrimSpace(v))
}

// CompareVersions compares two Gentoo-style version strings
// Returns: -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
func CompareVersions(v1, v2 string) int {
	nums1, suffix1, suffixNum1, rev1 := parseVersion(v1)
	nums2, suffix2, suffixNum2, rev2 := parseVersion(v2)

	// Compare numeric parts first
	if cmp := compareIntSlices(nums1, nums2); cmp != 0 {
		return cmp
	}

	// Compare suffix types (alpha < beta < pre < rc < release < p)
	priority1 := suffixPriority[suffix1]
	priority2 := suffixPriority[suffix2]
	if priority1 < priority2 {
		return -1
	}
	if priority1 > priority2 {
		return 1
	}

	// Same suffix type, compare suffix numbers
	if suffixNum1 < suffixNum2 {
		return -1
	}
	if suffixNum1 > suffixNum2 {
		return 1
	}

	// Compare revisions
	if rev1 < rev2 {
		return -1
	}
	if rev1 > rev2 {
		return 1
	}

	return 0
}
