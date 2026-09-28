package distfiles

import (
	"strings"
	"testing"
)

// FuzzManifestDistFilenames states what the Manifest DIST parser guarantees for
// any Manifest body it is handed.
//
// A Manifest comes from the overlay repository, so the parser meets whatever a
// malformed or hostile tree contains, and every name it returns is later joined
// onto a directory (the distdir, the distfile cache). Each name must therefore
// be one file inside that directory: non-empty, neither "." nor ".." (".."
// would point at the distdir's parent), with no "/" and no "\", and taken from
// the second field of a DIST line of that same body.
//
// The converse is checked too: a declared name that is safe must come back. A
// refusal that also drops ".foo", "..foo" or "foo..tar.gz" would make a caller
// that reads the list as authoritative report "this package publishes no such
// archive" about one it does publish.
func FuzzManifestDistFilenames(f *testing.F) {
	// Names that must be refused, though they sit exactly where a name goes:
	// the directory itself and its parent, in every record layout a Manifest
	// can carry them in.
	f.Add([]byte("DIST .. 1234 BLAKE2B 00 SHA512 00\n"))
	f.Add([]byte("DIST . 1234 BLAKE2B 00 SHA512 00\n"))
	f.Add([]byte("DIST\t..\t1234\r\n"))
	f.Add([]byte("  DIST   .   1234\n"))
	f.Add([]byte("DIST ../etc/passwd 1\nDIST ..\\x 1\nDIST a/b 1\nDIST a\\b 1\n"))

	// Strings that are not a DIST record's name and must not be returned as
	// one: other record types, a keyword that only starts with DIST, a third
	// field, and a line split across a newline.
	f.Add([]byte("EBUILD foo-1.ebuild 1 BLAKE2B 00\nMISC metadata.xml 1\nAUX x.patch 1\n"))
	f.Add([]byte("DISTX foo.tar.gz 1\ndist bar.tar.gz 1\nxDIST baz.tar.gz 1\n"))
	f.Add([]byte("DIST\nfoo.tar.gz 1\n"))

	// Names that look like "." or ".." but are ordinary files and must be kept.
	f.Add([]byte("DIST ... 1\nDIST .foo 1\nDIST ..foo 1\nDIST foo.. 1\nDIST foo..tar.gz 1\nDIST .. 1\n"))

	// Bodies the parser plainly accepts or plainly finds nothing in.
	f.Add([]byte("DIST hello-1.0.tar.gz 12345 BLAKE2B abc SHA512 def\n" +
		"DIST hello-1.0.tar.gz.asc 488 BLAKE2B abc SHA512 def\n" +
		"EBUILD hello-1.0.ebuild 900 BLAKE2B abc SHA512 def\n" +
		"MISC metadata.xml 300 BLAKE2B abc SHA512 def\n"))
	f.Add([]byte("DIST\n"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, body []byte) {
		got := manifestDistFilenames(body)

		declared := map[string]int{}
		for _, line := range strings.Split(string(body), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "DIST" {
				declared[fields[1]]++
			}
		}

		returned := map[string]int{}
		for _, name := range got {
			switch {
			case name == "":
				t.Fatalf("manifestDistFilenames(%q) returned an empty name in %q", body, got)
			case name == "." || name == "..":
				t.Fatalf("manifestDistFilenames(%q) returned %q in %q; a name joined onto the distdir must not "+
					"denote the distdir itself or its parent", body, name, got)
			case strings.ContainsAny(name, `/\`):
				t.Fatalf("manifestDistFilenames(%q) returned %q in %q; a name must not contain a path separator",
					body, name, got)
			}
			returned[name]++
			if returned[name] > declared[name] {
				t.Fatalf("manifestDistFilenames(%q) returned %q %d time(s) in %q, but the body declares it as the "+
					"second field of a DIST line only %d time(s)", body, name, returned[name], got, declared[name])
			}
		}

		for name, n := range declared {
			if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
				continue
			}
			if returned[name] != n {
				t.Fatalf("manifestDistFilenames(%q) returned %q %d time(s) in %q, but the body declares that safe "+
					"name in %d DIST line(s); a name that is one file inside the distdir must not be dropped",
					body, name, returned[name], got, n)
			}
		}
	})
}
