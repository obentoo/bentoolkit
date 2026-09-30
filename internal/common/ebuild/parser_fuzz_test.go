package ebuild

import (
	"errors"
	"testing"
)

// FuzzParsePath states what ParsePath guarantees for any input it is handed.
//
// Ebuild paths come from the overlay repository, so the parser meets whatever a
// malformed or hostile tree contains. For every input it must do one of two
// things: refuse it with ErrInvalidEbuildPath, or return an Ebuild whose Name
// equals its Package and whose String() parses back to the same Ebuild. A
// result that does not survive its own String() names a different file than
// the one that was parsed.
func FuzzParsePath(f *testing.F) {
	// The same Ebuild must not come back as a different one. A category that
	// the parser's own prefix normalisation eats on the second pass is the
	// shape of that failure: String() renders it, and re-parsing drops it.
	f.Add("././x/x-1.ebuild")
	f.Add(`.\.\x\x-1.ebuild`)
	f.Add("././/x-1.ebuild")

	// Two different Ebuilds must not collapse into one string. A package whose
	// own name ends in something that looks like a version sits one hyphen
	// away from a shorter package with a longer version.
	f.Add("cat/foo-1/foo-1-2.ebuild")
	f.Add("cat/foo/foo-1-2.ebuild")
	f.Add("cat/foo-1-r1/foo-1-r1-2-r3.ebuild")
	f.Add("cat/a-1/a-1-1-1.ebuild")

	// Paths the parser plainly accepts or plainly refuses.
	f.Add("app-misc/hello/hello-1.0.ebuild")
	f.Add("./app-misc/hello/hello-1.0_rc1-r1.ebuild")
	f.Add(`app-misc\hello\hello-1.0.ebuild`)
	f.Add("www-client/firefox-bin/firefox-bin-128.0.3.ebuild")
	f.Add("app-misc/hello/other-1.0.ebuild")
	f.Add("hello/hello-1.0.ebuild")
	f.Add("../hello/hello-1.0.ebuild")
	f.Add("app-misc/hello/hello-.ebuild")
	f.Add("")

	f.Fuzz(func(t *testing.T, path string) {
		e, err := ParsePath(path)
		if err != nil {
			if !errors.Is(err, ErrInvalidEbuildPath) {
				t.Fatalf("ParsePath(%q) returned %v; the only error it may return is ErrInvalidEbuildPath", path, err)
			}
			if e != nil {
				t.Fatalf("ParsePath(%q) returned both an Ebuild %+v and the error %v", path, *e, err)
			}
			return
		}
		if e == nil {
			t.Fatalf("ParsePath(%q) returned neither an Ebuild nor an error", path)
		}
		if e.Name != e.Package {
			t.Fatalf("ParsePath(%q) = %+v: Name %q differs from Package %q", path, *e, e.Name, e.Package)
		}

		rendered := e.String()
		again, err := ParsePath(rendered)
		if err != nil {
			t.Fatalf("ParsePath(%q) = %+v, but its String() %q does not parse back: %v", path, *e, rendered, err)
		}
		if again == nil {
			t.Fatalf("ParsePath(%q) = %+v, but re-parsing its String() %q returned no Ebuild", path, *e, rendered)
		}
		if *again != *e {
			t.Fatalf("ParsePath(%q) = %+v, but its String() %q parses back as a different Ebuild %+v", path, *e, rendered, *again)
		}
	})
}
