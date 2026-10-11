package autoupdate

import (
	"errors"
	"net/http"
	"testing"
)

// R1.4, R3.3, R3.4: a manifest failure on a tarball this step generated is
// returned as is and never reaches the LLM fixer. The converses come first:
// a failure the step does NOT own must still reach the fixer.
func TestVendorFailureNeverReachesTheFixer(t *testing.T) {
	cases := []struct {
		name       string
		inherit    string
		head       s090Resp
		wantFixers int
	}{
		{"not owned: vendor probe answered 503", "go-module", s090Resp{status: 503}, 1},
		{"not a Go vendor candidate", "cargo", s090Resp{status: 404}, 1},
		{"owned: tarball generated after a 404", "go-module", s090Resp{status: 404}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := s090Fixture(t, "1.9.0", tc.inherit, s090VendorOK)
			f.tr.set(http.MethodHead, s090VendorOK, tc.head)
			f.exec.pkgdevFail = true
			fx := &fakeFixer{summary: "rewrote SRC_URI"}
			a := f.applier(t, fx)

			distdir, err := a.runManifestWithFix(t.Context(), f.cand(), s090Pkg, "1.9.0", &ApplyResult{})
			t.Cleanup(func() { removeStagedDistdir(nil, distdir) })
			if err == nil {
				t.Fatal("runManifestWithFix succeeded although every pkgdev run failed")
			}
			if fx.called != tc.wantFixers {
				t.Errorf("the LLM fixer ran %d time(s), want %d", fx.called, tc.wantFixers)
			}
			if tc.wantFixers == 0 && !errors.Is(err, ErrManifestFailed) {
				t.Errorf("error %v does not carry the manifest failure (ErrManifestFailed)", err)
			}
		})
	}
}
