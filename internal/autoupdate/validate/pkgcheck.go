package validate

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// lookPath is the seam that answers "is pkgcheck installed". It is separate
// from execCommand so the absent-binary branch stays reachable in tests on a
// host that does have pkgcheck — which is every host this is developed on.
var lookPath = exec.LookPath

// qaTimeout bounds one pkgcheck scan.
//
// Two minutes, matching the budget internal/autoupdate.qaCheckTimeout already
// gives pkgcheck in applier.go. Written out rather than imported: autoupdate
// imports this package for the isolation probe, so taking the constant back
// would be an import cycle. If one moves, move the other.
const qaTimeout = 2 * time.Minute

// PkgcheckFindings collects the QA findings pkgcheck reports for one package,
// beside the option gate and without touching its verdict.
//
// The reason is non-empty exactly when the outcome is SKIPPED: "pkgcheck found
// nothing" and "pkgcheck did not run" are different answers, and rendering them
// alike is a silent pass.
//
// --cache=-git, because pkgcheck's GitAddon raises on this overlay's history: it
// prints a traceback to STDERR, writes nothing to stdout and EXITS 0, so every
// scan looks clean. Disabling it costs only the git-history checks (commit
// messages, dropped blockers), none of which say whether an ebuild matches its
// source.
//
// The exit code is not the verdict: pkgcheck exits 0 with findings AND for an
// atom the repository does not hold. Records that did arrive are reported even
// after a non-zero exit, because findings on stdout are findings whatever the
// process did afterwards. Findings are carried at Gate "qa" so Report.ExitCode
// can exclude them.
func PkgcheckFindings(ctx context.Context, pkgDir, atom string) ([]Finding, Outcome, string) {
	if _, err := lookPath("pkgcheck"); err != nil {
		return nil, OutcomeSkipped, "pkgcheck was not found on PATH, so no QA findings were collected"
	}

	ctx, cancel := context.WithTimeout(ctx, qaTimeout)
	defer cancel()

	cmd := execCommand(ctx, "pkgcheck", "scan", "--cache=-git", "-R", "JsonStream", atom)
	// Run from the package directory so pkgcheck resolves the overlay repo from
	// cwd, the way runQACheck already does.
	cmd.Dir = pkgDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	findings, decodeErr := decodePkgcheckStream(stdout.Bytes())

	if decodeErr != nil {
		return nil, OutcomeSkipped, fmt.Sprintf("pkgcheck output for %s could not be read: %v", atom, decodeErr)
	}
	if runErr != nil && len(findings) == 0 {
		return nil, OutcomeSkipped, fmt.Sprintf("pkgcheck failed for %s: %v%s", atom, runErr, diagnostic(stderr.String()))
	}

	return findings, OutcomePass, ""
}

// decodePkgcheckStream decodes every record in a JsonStream body.
//
// One undecodable line fails the whole stream rather than being skipped. The
// alternative — dropping what cannot be read and returning the rest — is how a
// package with problems comes back looking quiet, and the caller has no way to
// know how much it did not see.
func decodePkgcheckStream(out []byte) ([]Finding, error) {
	var findings []Finding

	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), maxArchiveBytes)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		finding, err := decodePkgcheckRecord([]byte(line))
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading pkgcheck output: %w", err)
	}
	return findings, nil
}

// diagnostic renders pkgcheck's stderr for a reason line, or nothing when it
// said nothing.
func diagnostic(stderr string) string {
	if flat := flattenDiagnostic(stderr); flat != "" {
		return ": " + flat
	}
	return ""
}
