package registry

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// unsafePkgKeys are packages.toml keys whose category or package half is not
// one directory name. Before story 064 splitPkgAtom accepted every one of them,
// and candidateIn / pkgDirFor then joined the halves under the overlay, so a
// key could name a path outside its category/package directory (S064-R1.4).
var unsafePkgKeys = []struct {
	key      string
	rejected string // the half the error must name
}{
	{"../x", ".."},
	{"../..", ".."},
	{"cat/..", ".."},
	{"./x", "."},
	{"cat/.", "."},
	{"../x:4.1", ".."},
	{"../x@label", ".."},
	{`cat\evil/x`, `cat\evil`},
	{`cat/x\..`, `x\..`},
	{"cat\x00/x", "cat\x00"},
	{"cat/x\x00", "x\x00"},
}

// TestValidatePackageConfigNamesRejectedPathElement pins the report a person
// reads: the error wraps ErrInvalidPackageKey, names the package key, and
// names the half that was refused.
func TestValidatePackageConfigNamesRejectedPathElement(t *testing.T) {
	for _, tt := range unsafePkgKeys {
		cfg := PackageConfig{URL: "https://example.com/x", Parser: "json", Path: "version"}
		err := ValidatePackageConfig(nil, tt.key, &cfg)
		if !errors.Is(err, ErrInvalidPackageKey) {
			t.Errorf("ValidatePackageConfig(nil, %q) = %v, want ErrInvalidPackageKey", tt.key, err)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "package "+tt.key+":") {
			t.Errorf("ValidatePackageConfig(nil, %q) error %q does not name the package", tt.key, msg)
		}
		if !strings.Contains(msg, strconv.Quote(tt.rejected)) {
			t.Errorf("ValidatePackageConfig(nil, %q) error %q does not name the rejected element %q", tt.key, msg, tt.rejected)
		}
	}
}
