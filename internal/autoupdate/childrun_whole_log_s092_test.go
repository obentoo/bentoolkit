package autoupdate

// Story 092, sub-task 2.1 fix (R3.3): the log file of a failed staged manifest
// run keeps everything the run printed, not only the 64 KiB tail the error
// carries, secrets masked, and no spill file outlives the run.
//
// Hermetic: temp overlay/staging/logs/sandbox directories, a private host
// distdir, and `pkgdev` faked through the sweeper's exec seam.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

const (
	wholeLogPkg     = "net-misc/wholelog"
	wholeLogVersion = "2.0"
	wholeLogFirst   = "WHOLE-LOG-FIRST-LINE-7c1e"
	wholeLogLast    = "WHOLE-LOG-LAST-LINE-9d4a"
)

// wholeLogSecret registers a resolved secret for this process and returns it.
func wholeLogSecret(t *testing.T) string {
	t.Helper()
	const name = "BENTOO_TEST_WHOLE_LOG_TOKEN"
	const token = "zq-whole-log-token-3b8e51"
	t.Setenv(name, token)
	if got, found, err := secrets.Lookup(name); err != nil || !found || got != token {
		t.Fatalf("fixture: secret lookup = %q %v %v", got, found, err)
	}
	return token
}

// wholeLogOutput is more than 200 KiB of wget-like progress, with a unique
// first and last line and the secret in the second line.
func wholeLogOutput(secret string) string {
	var b strings.Builder
	b.WriteString(wholeLogFirst + "\n")
	b.WriteString("--2026-10-12-- https://dl.example.org/f.tar.xz?token=" + secret + "\n")
	for i := 0; b.Len() < 220<<10; i++ {
		fmt.Fprintf(&b, "%6dK .......... .......... .......... .......... ..........  %2d%%\n", i*50, i%100)
	}
	b.WriteString("!!! Couldn't download 'f.tar.xz'. Aborting.\n")
	b.WriteString(wholeLogLast + "\n")
	return b.String()
}

// wholeLogSweeper builds a sweeper whose pkgdev prints the file at outputPath,
// half to stdout and half to stderr, and exits with exitCode. It returns the
// sweeper and the staged package directory.
func wholeLogSweeper(t *testing.T, logsDir, outputPath string, exitCode int) (*sweeper, string) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	stagedPkg := filepath.Join(tmp, "staging", "net-misc", "wholelog")
	host := filepath.Join(tmp, "host-distdir")
	sandbox := filepath.Join(tmp, "sandbox")
	for _, dir := range []string{overlayDir, stagedPkg, host, sandbox} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stagedPkg, "wholelog-2.0.ebuild"), []byte("EAPI=8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := fixSandboxRoot
	fixSandboxRoot = func(context.Context) string { return sandbox }
	t.Cleanup(func() { fixSandboxRoot = prev })

	// The first half goes to stdout and the rest to stderr, so both streams
	// have to reach the log.
	script := `size=$(wc -c < "$0"); half=$((size / 2)); head -c "$half" "$0"; tail -c +"$((half + 1))" "$0" >&2; exit "$1"`
	seam := func(ctx context.Context, name string, _ ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		return exec.CommandContext(ctx, "sh", "-c", script, outputPath, fmt.Sprint(exitCode))
	}
	s := newSweeper(overlayDir,
		withSweeperExec(seam),
		withSweeperDistdir(host, ""),
		withSweeperChildLogsDir(logsDir),
	)
	return s, stagedPkg
}

// spillLeftovers lists every file under logsDir that is not a final run log.
func spillLeftovers(t *testing.T, logsDir string) []string {
	t.Helper()
	var left []string
	err := filepath.WalkDir(logsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			left = append(left, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", logsDir, err)
	}
	return left
}

// A failed run printing ~220 KiB keeps all of it in its log: the first line the
// 64 KiB tail has long dropped, the last line, both streams, the secret near
// the beginning masked, mode 0600 and no spill file left behind.
func TestChildRunLogHoldsWholeOutputOnFailure(t *testing.T) {
	isolateSecretsPaths(t)
	secret := wholeLogSecret(t)
	raw := wholeLogOutput(secret)
	outputPath := filepath.Join(t.TempDir(), "pkgdev-output.txt")
	if err := os.WriteFile(outputPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	logsDir := filepath.Join(t.TempDir(), "logs")
	s, stagedPkg := wholeLogSweeper(t, logsDir, outputPath, 1)

	distdir, err := s.runStagedManifestIn(t.Context(), "", stagedPkg, wholeLogPkg, wholeLogVersion)
	if distdir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(distdir) })
	}
	if !errors.Is(err, ErrManifestFailed) {
		t.Fatalf("runStagedManifestIn = %v, want ErrManifestFailed", err)
	}
	var run *ChildRunError
	if !errors.As(err, &run) {
		t.Fatalf("no *ChildRunError in %v", err)
	}
	if run.LogPath == "" {
		t.Fatalf("LogPath is empty: the whole output was not kept")
	}

	body, err := os.ReadFile(run.LogPath)
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	log := string(body)
	if !strings.HasPrefix(log, wholeLogFirst+"\n") {
		t.Errorf("the log does not start with the run's first line %q; it starts:\n%.200s", wholeLogFirst, log)
	}
	if !strings.HasSuffix(log, wholeLogLast+"\n") {
		t.Errorf("the log does not end with the run's last line %q", wholeLogLast)
	}
	if strings.Contains(log, secret) {
		t.Errorf("the log holds the resolved secret unmasked")
	}
	if !strings.Contains(log, "?token=***\n") {
		t.Errorf("the secret near the beginning is not masked in place")
	}
	if want := strings.ReplaceAll(raw, secret, "***"); log != want {
		t.Errorf("the log is not the whole output, masked: %d bytes, want %d", len(log), len(want))
	}
	if minSize := len(raw) - (len(secret) - 3); len(body) < minSize || len(body) < 200<<10 {
		t.Errorf("log size = %d bytes, want at least %d (the output, secret masked) and 200 KiB", len(body), minSize)
	}
	if fi, err := os.Stat(run.LogPath); err != nil {
		t.Errorf("stat %s: %v", run.LogPath, err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode = %v, want 0600", fi.Mode().Perm())
	}

	// The error itself still carries the bounded tail.
	if strings.Contains(run.Output, wholeLogFirst) || !strings.Contains(run.Output, wholeLogLast) ||
		!strings.Contains(run.Output, "earlier bytes dropped") {
		t.Errorf("Output is not the 64 KiB tail (len %d)", len(run.Output))
	}

	if left := spillLeftovers(t, logsDir); len(left) != 0 {
		t.Errorf("temp files left in the logs dir after a failure: %v", left)
	}
}

// A successful run leaves nothing in the logs dir: no log and no spill file.
func TestChildRunLogHoldsWholeOutputLeavesNothingOnSuccess(t *testing.T) {
	isolateSecretsPaths(t)
	secret := wholeLogSecret(t)
	outputPath := filepath.Join(t.TempDir(), "pkgdev-output.txt")
	if err := os.WriteFile(outputPath, []byte(wholeLogOutput(secret)), 0o600); err != nil {
		t.Fatal(err)
	}
	logsDir := filepath.Join(t.TempDir(), "logs")
	s, stagedPkg := wholeLogSweeper(t, logsDir, outputPath, 0)

	distdir, err := s.runStagedManifestIn(t.Context(), "", stagedPkg, wholeLogPkg, wholeLogVersion)
	if distdir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(distdir) })
	}
	if err != nil {
		t.Fatalf("runStagedManifestIn: %v", err)
	}
	if left := spillLeftovers(t, logsDir); len(left) != 0 {
		t.Errorf("temp files left in the logs dir after a success: %v", left)
	}
	err = filepath.WalkDir(logsDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("a successful run left %s", path)
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	})
	if err != nil {
		t.Fatalf("walking %s: %v", logsDir, err)
	}
}

// The log is masked a chunk at a time: a secret wherever it falls against the
// chunk boundary, including split across it, is masked exactly as
// scrubResolvedSecrets masks a string, longest secret first.
func TestChildRunLogHoldsWholeOutputMasksSecretsAcrossChunks(t *testing.T) {
	long, short := "tok-LONG-secret-value-0d2f", "secret"
	vals := []string{long, short}
	for off := scrubChunkBytes - len(long) - 2; off <= scrubChunkBytes+len(long)+2; off++ {
		raw := strings.Repeat("a", off) + long + "|" + short + short + "\n" + strings.Repeat("b", scrubChunkBytes) + long
		want := strings.ReplaceAll(strings.ReplaceAll(raw, long, "***"), short, "***")
		var got bytes.Buffer
		if err := copyScrubbed(&got, strings.NewReader(raw), vals); err != nil {
			t.Fatalf("offset %d: copyScrubbed: %v", off, err)
		}
		if got.String() != want {
			t.Fatalf("offset %d: masked output differs from the whole-string scrub (got %d bytes, want %d; leaked=%v)",
				off, got.Len(), len(want), strings.Contains(got.String(), short))
		}
	}
}
