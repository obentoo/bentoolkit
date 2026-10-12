package autoupdate

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// Labels of the child runs a manifest step can fail in. They name the run in
// the error text, in the Apply Result heading and in the log file name.
const (
	childRunFirstManifest   = "first manifest run"
	childRunManifestRecheck = "re-check after LLM fix"
)

// ChildRunError is one failed child process run: what the run was, the tail of
// what it printed, and where the whole of it was kept.
//
// Error() is a single line — the label, the run error and the first line of
// Output that reads as an error — so an error holding several runs stays short
// wherever it is printed whole. The output itself travels in Output, its last
// tui.MaxCapturedBytes, for a renderer that bounds it, and in the file at
// LogPath, which keeps all of it.
type ChildRunError struct {
	// Label names the run, e.g. "first manifest run".
	Label string
	// Output is the last tui.MaxCapturedBytes the run printed, stdout and
	// stderr interleaved, with every resolved secret masked. When the run
	// printed more, a first line states how many earlier bytes were dropped.
	Output string
	// LogPath is the file holding everything the run printed, from its first
	// byte, with every resolved secret masked; empty when it could not be
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
// secret in output, the run's captured tail, and keeping everything spill holds
// in <logsDir>/<category>/<package>/<version>/<label>.log. A log that cannot be
// written is a WARN and leaves LogPath empty: the run's failure is the news, and
// the tail still travels in the error. An empty logsDir writes nothing.
func (s *sweeper) newChildRunError(label, pkg, version, output string, spill *childRunSpill, runErr error) *ChildRunError {
	run := &ChildRunError{Label: label, Output: scrubResolvedSecrets(output), Err: runErr}
	if s.childLogsDir == "" {
		return run
	}
	path, err := spill.keep(s.childLogsDir, pkg, version, label)
	if err != nil {
		s.log.Warn("could not keep the child run's output in its log file (its tail still travels in the error)",
			"package", pkg, "version", version, "run", label, "err", err)
		return run
	}
	run.LogPath = path
	return run
}

// childRunSpill holds a run's whole output on disk while the run lasts, so a
// failure can keep all of it however much the run printed, while memory holds
// only the tail. Write never fails: a spill error is kept, and later turns into
// a missing log rather than into a failed write the child would see.
type childRunSpill struct {
	mu   sync.Mutex
	file *os.File
	err  error
}

// openChildRunSpill starts a run's spill file in the logs directory. It is nil
// when no logs directory is configured; a spill that could not be created
// carries its error and drops what it is given.
func (s *sweeper) openChildRunSpill() *childRunSpill {
	if s.childLogsDir == "" {
		return nil
	}
	sp := &childRunSpill{}
	if err := os.MkdirAll(s.childLogsDir, 0o750); err != nil {
		sp.err = fmt.Errorf("creating %s: %w", s.childLogsDir, err)
		return sp
	}
	f, err := os.CreateTemp(s.childLogsDir, ".childrun-spill-*.log")
	if err != nil {
		sp.err = fmt.Errorf("creating a spill file in %s: %w", s.childLogsDir, err)
		return sp
	}
	sp.file = f
	return sp
}

// Write appends p to the spill file and always reports success.
func (sp *childRunSpill) Write(p []byte) (int, error) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.file != nil && sp.err == nil {
		if _, err := sp.file.Write(p); err != nil {
			sp.err = fmt.Errorf("writing %s: %w", sp.file.Name(), err)
		}
	}
	return len(p), nil
}

// tee returns the one writer a child's stdout and stderr both go to: sc, plus
// the spill when there is one. A single writer gives the child a single pipe,
// which keeps the bytes in the order the child wrote them.
func (sp *childRunSpill) tee(sc io.Writer) io.Writer {
	if sp == nil {
		return sc
	}
	return io.MultiWriter(sc, sp)
}

// keep writes the spilled output, secrets masked, to the run's log file and
// returns its path. It must be called once the run has ended.
func (sp *childRunSpill) keep(logsDir, pkg, version, label string) (string, error) {
	if sp == nil {
		return "", errors.New("the run's output was not spilled")
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.err != nil {
		return "", sp.err
	}
	if sp.file == nil {
		return "", errors.New("the run's spill file is already discarded")
	}
	if _, err := sp.file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewinding %s: %w", sp.file.Name(), err)
	}
	return writeChildRunLog(logsDir, pkg, version, label, sp.file)
}

// discard closes and removes the spill file, which holds unmasked output and so
// never outlives the run. It is safe on nil and safe to call twice.
func (sp *childRunSpill) discard() error {
	if sp == nil {
		return nil
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.file == nil {
		return nil
	}
	name := sp.file.Name()
	closeErr := sp.file.Close()
	sp.file = nil
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.Join(closeErr, fmt.Errorf("removing %s: %w", name, err))
	}
	return nil
}

// writeChildRunLog copies src, every resolved secret masked, to the run's log
// file and returns its path. The file is written beside its final name and
// renamed into place, so it is 0600 even when an earlier run's log already
// exists, and a reader never sees half of it.
func writeChildRunLog(logsDir, pkg, version, label string, src io.Reader) (string, error) {
	dir, err := packageLogDir(logsDir, pkg, version)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: the operator's own logs directory, read with ordinary tools; the files in it are 0600
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, childRunLogName(label))
	tmp, err := os.CreateTemp(dir, ".childrun-*.log")
	if err != nil {
		return "", fmt.Errorf("creating a log file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	w := bufio.NewWriter(tmp)
	writeErr := copyScrubbed(w, src, maskableSecrets())
	if writeErr == nil {
		writeErr = w.Flush()
	}
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", fmt.Errorf("writing %s: %w", tmpName, errors.Join(err, os.Remove(tmpName)))
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("renaming %s to %s: %w", tmpName, path, errors.Join(err, os.Remove(tmpName)))
	}
	return path, nil
}

// scrubChunkBytes is how much output copyScrubbed masks and writes at a time.
const scrubChunkBytes = 64 << 10

// secretMask replaces each masked secret, as secrets.Scrub does.
var secretMask = []byte("***")

// copyScrubbed copies src to dst with every occurrence of each secret replaced
// by the mask, a chunk at a time so output of any size is masked in bounded
// memory. Each chunk is scanned together with a look-ahead one byte shorter
// than the longest secret, so a secret split across two reads is found whole.
// Occurrences that overlap are masked as one, which hides at least what
// scrubResolvedSecrets hides.
func copyScrubbed(dst io.Writer, src io.Reader, secretVals []string) error {
	if len(secretVals) == 0 {
		_, err := io.Copy(dst, src)
		return err
	}
	needles := make([][]byte, len(secretVals))
	lookahead := 0
	for i, v := range secretVals {
		needles[i] = []byte(v)
		lookahead = max(lookahead, len(v)-1)
	}
	buf := make([]byte, scrubChunkBytes+lookahead)
	held, masked := 0, 0
	for {
		n, err := io.ReadFull(src, buf[held:])
		end := held + n
		last := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		if err != nil && !last {
			return err
		}
		cut := end
		if !last {
			cut = end - lookahead
		}
		masked, err = writeMasked(dst, buf[:cut], secretSpans(buf[:end], needles, masked))
		if err != nil || last {
			return err
		}
		held = copy(buf, buf[cut:end])
	}
}

// maskSpan is a byte range of a chunk to mask. written marks the range a mask
// was already written for, the part of an earlier chunk's span carried over.
type maskSpan struct {
	start, end int
	written    bool
}

// secretSpans returns the ranges of b holding a secret, sorted and with
// overlapping ranges merged. The first carried bytes are a range already
// masked.
func secretSpans(b []byte, needles [][]byte, carried int) []maskSpan {
	var spans []maskSpan
	if carried > 0 {
		spans = append(spans, maskSpan{start: 0, end: carried, written: true})
	}
	for _, needle := range needles {
		for i := 0; i < len(b); {
			j := bytes.Index(b[i:], needle)
			if j < 0 {
				break
			}
			spans = append(spans, maskSpan{start: i + j, end: i + j + len(needle)})
			i += j + 1
		}
	}
	slices.SortFunc(spans, func(x, y maskSpan) int { return x.start - y.start })
	merged := spans[:0]
	for _, sp := range spans {
		if n := len(merged); n > 0 && sp.start < merged[n-1].end {
			merged[n-1].end = max(merged[n-1].end, sp.end)
			merged[n-1].written = merged[n-1].written || sp.written
			continue
		}
		merged = append(merged, sp)
	}
	return merged
}

// writeMasked writes b with each span masked and returns how many bytes past
// the end of b a span written here still covers. A span starting at or past
// the end of b is left for the next chunk, which still holds it whole.
func writeMasked(dst io.Writer, b []byte, spans []maskSpan) (int, error) {
	pos := 0
	for _, sp := range spans {
		if sp.start >= len(b) {
			break
		}
		if _, err := dst.Write(b[pos:sp.start]); err != nil {
			return 0, err
		}
		if !sp.written {
			if _, err := dst.Write(secretMask); err != nil {
				return 0, err
			}
		}
		if sp.end > len(b) {
			return sp.end - len(b), nil
		}
		pos = sp.end
	}
	_, err := dst.Write(b[pos:])
	return 0, err
}

// packageLogDir is <logsDir>/<category>/<package>/<version>, the directory a
// package version's run logs are kept in. A name that would leave it is an
// error.
func packageLogDir(logsDir, pkg, version string) (string, error) {
	category, name, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return "", fmt.Errorf("invalid package name format: %q", pkg)
	}
	for _, part := range []string{category, name, version} {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, filepath.Separator) {
			return "", fmt.Errorf("unusable log path component %q for %s-%s", part, pkg, version)
		}
	}
	return filepath.Join(logsDir, category, name, version), nil
}

// childRunLogName is the label as a file name: lower case, spaces as hyphens,
// so the path printed beside a failure can be pasted into a shell unquoted.
func childRunLogName(label string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(label)), " ", "-") + ".log"
}

// scrubResolvedSecrets masks every secret value resolved so far in this
// process, longest first so a value that contains another is masked whole.
func scrubResolvedSecrets(s string) string {
	for _, v := range maskableSecrets() {
		s = secrets.Scrub(s, v)
	}
	return s
}

// maskableSecrets is every secret value resolved so far in this process,
// longest first, without the blank ones: a blank value would mask every space.
func maskableSecrets() []string {
	var out []string
	for _, v := range secrets.Resolved() {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// errorLineMarkers are the case-insensitive fragments that make an output line
// read as the failure rather than as progress or chatter.
var errorLineMarkers = []string{"error", "!!!", "fail", "fatal", "not found", "denied", "no such", "unreachable"}

// firstErrorLine returns the first line of output that carries an error marker,
// trimmed, or "" when none does. A carriage-return-rewritten line counts by what
// was left on screen: the text after its last '\r'.
func firstErrorLine(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
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

// spillSweepAge is how old a spill file must be before a later run removes it.
// A live run keeps writing to its spill, so a file this stale belongs to a run
// that was killed before it could remove it.
const spillSweepAge = time.Hour

// sweepChildRunSpills removes the spill files that runs killed mid-flight left
// directly in the logs directory. Only regular files named like a spill and
// older than spillSweepAge are touched; a failure is logged and never stops
// the run.
func (a *Applier) sweepChildRunSpills() {
	if a.logsDir == "" {
		return
	}
	entries, err := os.ReadDir(a.logsDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			a.logger().Warn("could not list the logs directory for leftover spill files",
				"dir", a.logsDir, "error", err)
		}
		return
	}
	cutoff := time.Now().Add(-spillSweepAge)
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, ".childrun-spill-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(a.logsDir, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			a.logger().Warn("could not remove a leftover spill file; it holds unmasked output",
				"path", path, "error", err)
		}
	}
}
