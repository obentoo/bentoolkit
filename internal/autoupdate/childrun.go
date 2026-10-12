package autoupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// Labels of the child runs a manifest step can fail in. They name the run in
// the error text, in the Apply Result heading and in the log file name.
const (
	childRunFirstManifest   = "first manifest run"
	childRunManifestRecheck = "re-check after LLM fix"
)

// ChildRunError is one failed child process run: what the run was, everything it
// printed, and where that output was kept.
//
// Error() is a single line — the label, the run error and the first line of
// Output that reads as an error — so an error holding several runs stays short
// wherever it is printed whole. The output itself travels in Output, for a
// renderer that bounds it, and in the file at LogPath, which keeps all of it.
type ChildRunError struct {
	// Label names the run, e.g. "first manifest run".
	Label string
	// Output is everything the run printed, stdout and stderr interleaved, with
	// every resolved secret masked.
	Output string
	// LogPath is the file holding the whole Output; empty when it could not be
	// written or no logs directory was configured.
	LogPath string
	// Err is how the run ended (an *exec.ExitError, a timeout); Unwrap returns
	// it, so errors.Is/errors.As still see the process failure.
	Err error
}

func (e *ChildRunError) Error() string {
	if e == nil {
		return "child run failed"
	}
	msg := e.Label + " failed"
	if e.Err != nil {
		msg += ": " + leadingLine(e.Err.Error())
	}
	if line := firstErrorLine(e.Output); line != "" {
		msg += ": " + line
	}
	return msg
}

func (e *ChildRunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ChildRuns returns every distinct *ChildRunError in err's chain, following both
// single and multi-error Unwrap, in the order they are first reached. A run
// reachable along two branches is returned once.
func ChildRuns(err error) []*ChildRunError {
	var out []*ChildRunError
	seen := map[*ChildRunError]bool{}
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if c, ok := e.(*ChildRunError); ok && c != nil && !seen[c] { //nolint:errorlint // walking the chain by hand; errors.As would stop at the first match
			seen[c] = true
			out = append(out, c)
		}
		switch u := e.(type) { //nolint:errorlint // the Unwrap shape itself is what is inspected
		case interface{ Unwrap() []error }:
			for _, x := range u.Unwrap() {
				walk(x)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	return out
}

// childRunDetail renders err for a reader that needs the child output itself —
// the LLM fixer's prompt, whose fetch allowlist is read from the URLs in it, and
// the repair bundle: the one-line error, then each run's whole output under its
// label. An error with no child run in it renders as err.Error().
func childRunDetail(err error) string {
	if err == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(err.Error())
	for _, run := range ChildRuns(err) {
		fmt.Fprintf(&b, "\n%s output:\n%s", run.Label, run.Output)
	}
	return b.String()
}

// newChildRunError builds the error of a failed run, masking every resolved
// secret in its output and keeping the whole output in
// <logsDir>/<category>/<package>/<version>/<label>.log. A log that cannot be
// written is a WARN and leaves LogPath empty: the run's failure is the news, and
// the output still travels in the error. An empty logsDir writes nothing.
func (s *sweeper) newChildRunError(label, pkg, version, output string, runErr error) *ChildRunError {
	run := &ChildRunError{Label: label, Output: scrubResolvedSecrets(output), Err: runErr}
	if s.childLogsDir == "" {
		return run
	}
	path, err := writeChildRunLog(s.childLogsDir, pkg, version, label, run.Output)
	if err != nil {
		s.log.Warn("could not keep the child run's output in its log file (the output still travels in the error)",
			"package", pkg, "version", version, "run", label, "err", err)
		return run
	}
	run.LogPath = path
	return run
}

// writeChildRunLog writes output to the run's log file and returns its path.
// The file is written beside its final name and renamed into place, so it is
// 0600 even when an earlier run's log already exists, and a reader never sees
// half of it.
func writeChildRunLog(logsDir, pkg, version, label, output string) (string, error) {
	category, name, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return "", fmt.Errorf("invalid package name format: %q", pkg)
	}
	for _, part := range []string{category, name, version} {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, filepath.Separator) {
			return "", fmt.Errorf("unusable log path component %q for %s-%s", part, pkg, version)
		}
	}
	dir := filepath.Join(logsDir, category, name, version)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: the operator's own logs directory, read with ordinary tools; the files in it are 0600
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, childRunLogName(label))
	tmp, err := os.CreateTemp(dir, ".childrun-*.log")
	if err != nil {
		return "", fmt.Errorf("creating a log file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.WriteString(output)
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("renaming %s to %s: %w", tmpName, path, err)
	}
	return path, nil
}

// childRunLogName is the label as a file name: lower case, spaces as hyphens,
// so the path printed beside a failure can be pasted into a shell unquoted.
func childRunLogName(label string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(label)), " ", "-") + ".log"
}

// scrubResolvedSecrets masks every secret value resolved so far in this
// process, longest first so a value that contains another is masked whole.
func scrubResolvedSecrets(s string) string {
	for _, v := range secrets.Resolved() {
		// A blank value would mask every space.
		if strings.TrimSpace(v) == "" {
			continue
		}
		s = secrets.Scrub(s, v)
	}
	return s
}

// errorLineMarkers are the case-insensitive fragments that make an output line
// read as the failure rather than as progress or chatter.
var errorLineMarkers = []string{"error", "!!!", "fail", "fatal", "not found", "denied", "no such", "unreachable"}

// firstErrorLine returns the first line of output that carries an error marker,
// trimmed, or "" when none does. A carriage-return-rewritten line counts by what
// was left on screen: the text after its last '\r'.
func firstErrorLine(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		if i := strings.LastIndexByte(line, '\r'); i >= 0 {
			line = line[i+1:]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		for _, m := range errorLineMarkers {
			if strings.Contains(lower, m) {
				return line
			}
		}
	}
	return ""
}

// leadingLine returns s up to its first newline.
func leadingLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
