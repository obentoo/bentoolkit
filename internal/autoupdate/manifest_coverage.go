package autoupdate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

// ErrManifestIncomplete is returned (wrapped) when the manifest step succeeded
// but the new ebuild's SRC_URI names a distfile its Manifest has no DIST line
// for. Such an ebuild passes every later gate that does not fetch, and fails on
// the user's machine with "does not have a Manifest entry" — a batch bump once
// shipped 76 of them.
var ErrManifestIncomplete = errors.New("manifest is missing DIST entries")

// missingManifestRecord is the part of a pkgcheck MissingManifest JsonStream
// record this check reads: which version, and which of its distfiles.
type missingManifestRecord struct {
	Class   string   `json:"__class__"`
	Version string   `json:"version"`
	Files   []string `json:"files"`
}

// checkManifestCoverage verifies that every distfile the ebuild for version
// fetches has a DIST line in pkgDir's Manifest.
//
// The SRC_URI is expanded by pkgcheck's MissingManifest check, not here: it
// needs ${P}, $(ver_cut …), "->" renames and eclass variables resolved, which
// is Portage's parser's job — nothing in this repository expands SRC_URI (see
// expectedDistfiles for why a half-correct expander is worse than none).
//
// Only a positive finding fails. When pkgcheck is absent, fails, or prints
// something that is not a JsonStream, the check could not answer, and it says
// so at WARN instead of refusing a bump nothing proved wrong. MissingManifest
// is a package-scope check, so the scan covers every version in the directory
// and the records are narrowed to the one this bump wrote.
func (a *Applier) checkManifestCoverage(ctx context.Context, pkgDir, pkg, version string) error {
	category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return nil
	}
	if _, err := a.lookPath("pkgcheck"); err != nil {
		a.logger().Warn("manifest coverage not verified: pkgcheck is not on PATH", "package", pkg, "version", version)
		return nil
	}

	scanCtx, cancel := context.WithTimeout(ctx, qaCheckTimeout)
	defer cancel()

	// --cache=-git for the reason validate.PkgcheckFindings gives: the git addon
	// crashes on this overlay's history and exits 0 with nothing on stdout.
	cmd := a.execCommand(scanCtx, "pkgcheck", "scan", "--cache=-git", "-k", "MissingManifest",
		"-R", "JsonStream", category+"/"+pkgName)
	cmd.Dir = pkgDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	missing, decodeErr := missingDistfiles(stdout.Bytes(), version)
	switch {
	case decodeErr != nil:
		a.logger().Warn("manifest coverage not verified", "package", pkg, "version", version, "err", decodeErr)
		return nil
	case len(missing) > 0:
		return fmt.Errorf("%w: %s-%s fetches %s, which its Manifest does not list",
			ErrManifestIncomplete, pkg, version, strings.Join(missing, ", "))
	case runErr != nil:
		a.logger().Warn("manifest coverage not verified: pkgcheck failed",
			"package", pkg, "version", version, "err", runErr, "stderr", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// missingDistfiles returns, sorted, the distfiles MissingManifest records name
// for version. One undecodable line fails the whole stream, so a package with
// problems never comes back looking clean.
func missingDistfiles(out []byte, version string) ([]string, error) {
	var missing []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec missingManifestRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("reading pkgcheck output: %w", err)
		}
		if rec.Class == "MissingManifest" && rec.Version == version {
			missing = append(missing, rec.Files...)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading pkgcheck output: %w", err)
	}
	sort.Strings(missing)
	return missing, nil
}
