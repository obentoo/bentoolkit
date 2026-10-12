package autoupdate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// R4.1, R4.2, R4.3: --check proves a vendor bump on the generated tarball,
// never uploads it even with an upload command configured, and the result
// the validation row is built from carries the note.
func TestValidateProvesAVendorBumpWithoutUploading(t *testing.T) {
	e := s090ApplyFixture(t, s090UploadCmd, false)
	result := e.applier.Validate(t.Context(), s090Pkg, validate.DepthCompile)

	if len(e.uploads) != 0 {
		t.Errorf("--check ran the upload command: %q", e.uploads)
	}
	if len(e.exec.pkgdev) == 0 || string(e.exec.pkgdev[0].vendor) != s090TarballBytes {
		t.Errorf("the manifest gate did not digest the generated tarball (pkgdev views %+v)", e.exec.pkgdev)
	}
	if e.published() {
		t.Error("--check published the bump")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "vendor tarball generated locally, not uploaded") {
		t.Errorf("the validation result does not carry the note: %s", raw)
	}
}
