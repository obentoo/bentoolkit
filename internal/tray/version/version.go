// Package version is bentoo-tray's own version, independent of the bentoolkit
// release it ships in.
package version

import (
	_ "embed"
	"strings"

	release "github.com/obentoo/bentoolkit/internal/common/version"
)

// raw is embedded rather than injected with -ldflags, so every build — make,
// the ebuild or a plain go build — reports the same version.
//
//go:embed VERSION
var raw string

// Version returns the tray's version, the VERSION file without surrounding
// whitespace.
func Version() string {
	return strings.TrimSpace(raw)
}

// Info returns the text bentoo-tray --version prints: the tray's version, the
// bentoolkit release it was built from, then the build lines bentoo's own
// Info prints after its first line.
func Info() string {
	lines := []string{
		"bentoo-tray version " + Version(),
		"  bentoolkit: " + release.Version,
	}
	if _, rest, ok := strings.Cut(release.Info(), "\n"); ok {
		lines = append(lines, rest)
	}
	return strings.Join(lines, "\n")
}
