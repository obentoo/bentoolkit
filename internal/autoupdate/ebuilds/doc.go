// Package ebuilds reads the ebuild tree of an overlay: it locates a package
// directory, lists its ebuilds, selects the current ebuild for a slot or a
// version series, splits package atoms, labels and slots, and extracts
// EbuildMetadata from an ebuild file.
//
// It walks directories and parses ebuild files; version parsing and comparison
// live in internal/common/ebuild. It sits at the bottom of the autoupdate
// package graph and imports no other autoupdate package (story 061).
package ebuilds
