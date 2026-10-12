// Package vendor handles the vendor tarball of a staged Go candidate ebuild.
//
// It does three things:
//
//   - Classification (Classify) reads an md5-cache entry and decides whether
//     it is a Go vendor candidate: a go-module ebuild that fetches one source
//     archive plus one "<name>-vendor.tar.xz" tarball from an allowed host.
//   - Generation (Generate) unpacks the source archive, runs `go mod vendor`
//     in it and packs the vendor tree into a reproducible xz tarball.
//   - Comparison (ModulesTxtDiffers) regenerates the vendor tree the same way
//     and reports whether its vendor/modules.txt differs from the one inside
//     an already fetched vendor tarball.
//
// Child processes run through the caller's Runner. The package is a leaf: it
// imports no other bentoolkit package, performs no network I/O and does not
// log. Every failure is returned as an error.
package vendor
