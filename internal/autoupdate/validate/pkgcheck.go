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
// The return is (findings, outcome, reason), and the reason is non-empty
// exactly when the outcome is SKIPPED. That is the same invariant the option
// gate keeps, applied to the QA half: "pkgcheck found nothing" and "pkgcheck
// did not run" are different answers, and a report that renders them alike is
// the silent pass this story exists to remove.
//
// # Why --cache=-git
//
// This was measured, not assumed. pkgcheck's GitAddon raises on this overlay's
// history: it prints a Python traceback to STDERR, writes nothing to stdout and
// EXITS 0. Three scans during design looked like clean packages for that
// reason, and design.md D7 recorded the record shape as unobserved because of
// it. With the git cache off, the same scans report findings immediately.
//
// Disabling it costs the git-history checks only — commit messages, blockers
// dropped upstream — none of which say anything about whether an ebuild matches
// the source it points at. Silently losing every finding costs everything.
//
// # Why the exit code is not the verdict
//
// Measured on the installed pkgcheck: it exits 0 with findings AND 0 for an
// atom the repository does not hold. The code carries almost nothing, so a
// non-zero one means something genuinely abnormal — and even then, records that
// did arrive are reported rather than discarded, because findings on stdout are
// findings whatever the process did afterwards.
//
// Findings are carried at Gate "qa" so Report.ExitCode can exclude them (D8).
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
// said nothing. It is flattened to one line so a skip stays one record.
func diagnostic(stderr string) string {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return ""
	}
	return ": " + strings.Join(strings.Fields(trimmed), " ")
}
