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
		revision, _ = strconv.Atoi(matches[1])
		v = revisionRegex.ReplaceAllString(v, "")
	}

	// Extract suffix (_rc1, _beta2, etc.)
	suffixType := ""
	suffixNum := 0
	if matches := versionSuffixRegex.FindStringSubmatch(v); matches != nil {
		suffixType = matches[1]
		if matches[2] != "" {
			suffixNum, _ = strconv.Atoi(matches[2])
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
			nums[i], _ = strconv.Atoi(numStr)
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
