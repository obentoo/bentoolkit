package validate

import "testing"

// TestFindDistfile_S089WrapperAnswersLikeTheSelection: the exported
// FindDistfile is the very selection the options gate uses, so the reviewer
// and the gate cannot answer differently about the same package — the same
// path when a distfile is chosen, the same refusal when none is.
func TestFindDistfile_S089WrapperAnswersLikeTheSelection(t *testing.T) {
	cases := []struct {
		desc, version   string
		manifest, there []string
	}{
		{"chosen among several", "1.0", []string{"m-1.0.tar.gz", "m-1.0.1.tar.gz"}, []string{"m-1.0.tar.gz", "m-1.0.1.tar.gz"}},
		{"declined", "1.0", []string{"m-1.0.tar.gz", "m-1.0.1.tar.gz"}, []string{"m-1.0.1.tar.gz"}},
		{"revision stripped", "1.0-r1", []string{"m-1.0.tar.gz", "m-2.0.tar.gz"}, []string{"m-1.0.tar.gz"}},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			pkgDir, distdir := s089vFixture(t, c.manifest, c.there)
			wantPath, wantErr := findDistfile(pkgDir, distdir, c.version)
			gotPath, gotErr := FindDistfile(pkgDir, distdir, c.version)
			if gotPath != wantPath {
				t.Errorf("FindDistfile returned %q, findDistfile %q", gotPath, wantPath)
			}
			if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
				t.Errorf("FindDistfile error %v, findDistfile error %v; want the same refusal", gotErr, wantErr)
			}
		})
	}
}
