// Package repo scans the category/package/*.ebuild layout of a Gentoo
// repository (an overlay) and reports the packages and versions it holds.
//
// It depends only on the standard library and internal/common/ebuild, so both
// internal/overlay and internal/autoupdate/validate can import it without one
// importing the other (story 061, audit finding F6).
package repo
