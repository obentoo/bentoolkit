package main

// Authored for story 060, sub-task 1.3 (U3, R3.1–R3.6): every line the
// registry-fix loop prints is pinned in full, byte for byte.
//
// Story 060 moved the snapshot → fix → re-check → revert transaction into
// autoupdate.AttemptRegistryFix; promptRegistryFixes kept the prompt, the
// printing and the tally. The expected output below was taken from the format
// strings of promptRegistryFixes as they stood before that move (HEAD 1462803),
// so a change to any stage line, the restore warning, the pass and
// still-failing lines, the keep prompt or the tally fails here.
//
// Only two run-dependent tokens are normalized: the overlay temp directory
// (<OVERLAY>) and the random suffix fileutil.WriteFileAtomic gives its temp
// file (bentoo-<PID>-<RAND>). A failed restore is produced by replacing the
// .autoupdate directory with a regular file, which fails for root as well.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
)

// linesFixer is a RegistryFixer whose edit runs before it answers.
type linesFixer struct {
	edit func()
	res  fixer.RegistryFixResult
	err  error
}

func (f *linesFixer) FixRegistry(_ context.Context, _ fixer.RegistryFixRequest) (fixer.RegistryFixResult, error) {
	if f.edit != nil {
		f.edit()
	}
	return f.res, f.err
}

var _ fixer.RegistryFixer = (*linesFixer)(nil)

// linesReader hands out one answer per Read call and runs hook just before
// the answer at index hookAt, so a test can act between two prompts.
type linesReader struct {
	answers []string
	next    int
	hookAt  int
	hook    func()
}

func (r *linesReader) Read(p []byte) (int, error) {
	if r.next >= len(r.answers) {
		return 0, io.EOF
	}
	if r.hook != nil && r.next == r.hookAt {
		r.hook()
	}
	n := copy(p, r.answers[r.next])
	r.next++
	return n, nil
}

func answers(a ...string) *linesReader { return &linesReader{answers: a, hookAt: -1} }

// linesOverlay is one overlay whose media-gfx/inkscape entry fails to extract
// until its JSON path is repaired: "version" yields 2.0.0, "nightly" yields a
// value that cannot be ordered against the ebuild's 1.0.0.
type linesOverlay struct {
	dir        string
	configDir  string
	configPath string
	serverURL  string
	stateDir   string
}

const linesPkg = "media-gfx/inkscape"

func newLinesOverlay(t *testing.T) *linesOverlay {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "2.0.0", "nightly": "nightly"})
	}))
	t.Cleanup(server.Close)

	o := &linesOverlay{dir: t.TempDir(), stateDir: t.TempDir(), serverURL: server.URL}
	o.configDir = filepath.Join(o.dir, ".autoupdate")
	o.configPath = filepath.Join(o.configDir, "packages.toml")
	writeExitTestEbuild(t, o.dir, linesPkg, "1.0.0")
	if err := os.MkdirAll(o.configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	o.write(t, "nonexistent")
	return o
}

func (o *linesOverlay) write(t *testing.T, jsonPath string) {
	t.Helper()
	entry := "[\"" + linesPkg + "\"]\nurl = \"" + o.serverURL + "\"\nparser = \"json\"\npath = \"" + jsonPath + "\"\n"
	if err := os.WriteFile(o.configPath, []byte(entry), 0o644); err != nil {
		t.Fatalf("write packages.toml: %v", err)
	}
}

func (o *linesOverlay) editTo(t *testing.T, jsonPath string) func() {
	return func() { o.write(t, jsonPath) }
}

// breakRestore makes every later write of packages.toml fail.
func (o *linesOverlay) breakRestore(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(o.configDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.configDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (o *linesOverlay) newChecker() (*autoupdate.Checker, error) {
	return autoupdate.NewChecker(o.dir,
		autoupdate.WithConfigDir(o.stateDir),
		autoupdate.WithConcurrency(autoupdate.DefaultConcurrency))
}

func linesFailures(pkgs ...string) map[string]error {
	f := map[string]error{linesPkg: fmt.Errorf("%w: parse: field %q not found", autoupdate.ErrFetchFailed, "nonexistent")}
	for _, p := range pkgs {
		f[p] = fmt.Errorf("%w: no match", autoupdate.ErrFetchFailed)
	}
	return f
}

var linesTempSuffix = regexp.MustCompile(`bentoo-\d+-\d+`)

// Pieces of the expected output, spelled from the pre-060 format strings.
const (
	linesPrompt     = "Fix registry for " + linesPkg + " with LLM? [y/N/a/q] "
	linesKeep       = "Keep the edit anyway? [y/N] "
	linesStillWrong = "  " + linesPkg + " still failing after fix using model \"claude-sonnet-4-5\": tried\n" +
		"  error: failed to fetch upstream version: all version extraction methods failed: failed to parse version: JSON path not found in response: field \"wrong\" not found\n"
	linesRestoreWarn = "  warning: could not restore packages.toml for " + linesPkg + ": failed to restore <OVERLAY>/.autoupdate/packages.toml: " +
		"creating a temporary file beside <OVERLAY>/.autoupdate/packages.toml: open <OVERLAY>/.autoupdate/.packages.toml.bentoo-<PID>-<RAND>: not a directory\n"
)

// TestPromptRegistryFixes_PrintsHEADLines drives promptRegistryFixes through
// every outcome of the transaction and every answer of the prompt, and
// compares the whole of stdout with the lines the command printed before
// story 060 (U3).
func TestPromptRegistryFixes_PrintsHEADLines(t *testing.T) {
	pinned := fixer.RegistryFixResult{Summary: "tried", Model: "claude-sonnet-4-5"}
	alias := fixer.RegistryFixResult{Summary: "path repaired", Model: "sonnet", DeniedTools: []string{"WebFetch(example.com)"}}

	type setup func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error))
	cases := []struct {
		name  string
		setup setup
		want  string
	}{
		{
			name: "snapshot failure skips",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				if err := os.Remove(o.configPath); err != nil {
					t.Fatal(err)
				}
				return &linesFixer{res: pinned}, linesFailures(), answers("y\n"), o.newChecker
			},
			want: linesPrompt +
				"  skipping " + linesPkg + ": could not snapshot packages.toml: failed to read packages.toml: open <OVERLAY>/.autoupdate/packages.toml: no such file or directory\n" +
				"fixed 0 · reverted 0 · skipped 1\n",
		},
		{
			name: "load failure skips",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				if err := os.WriteFile(o.configPath, []byte("[[[ not toml\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return &linesFixer{res: pinned}, linesFailures(), answers("y\n"), o.newChecker
			},
			want: linesPrompt +
				"  skipping " + linesPkg + ": could not load packages.toml: failed to parse packages.toml: toml: line 1: expected '.' or ']' to end table name, but got '[' instead\n" +
				"fixed 0 · reverted 0 · skipped 1\n",
		},
		{
			name: "fixer error reverts",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				return &linesFixer{edit: o.editTo(t, "halfway"), err: errors.New("agent crashed")}, linesFailures(), answers("y\n"), o.newChecker
			},
			want: linesPrompt +
				"  " + linesPkg + ": registry fix failed: agent crashed\n" +
				"fixed 0 · reverted 1 · skipped 0\n",
		},
		{
			name: "fixer error whose restore fails warns",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				return &linesFixer{edit: func() { o.breakRestore(t) }, err: errors.New("agent crashed")}, linesFailures(), answers("y\n"), o.newChecker
			},
			want: linesPrompt +
				"  " + linesPkg + ": registry fix failed: agent crashed\n" +
				linesRestoreWarn +
				"fixed 0 · reverted 1 · skipped 0\n",
		},
		{
			name: "checker error reverts",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				return &linesFixer{edit: o.editTo(t, "version"), res: alias}, linesFailures(), answers("y\n"),
					func() (*autoupdate.Checker, error) { return nil, errors.New("checker init failed") }
			},
			want: linesPrompt +
				"  " + linesPkg + ": could not build checker for re-check: checker init failed\n" +
				"fixed 0 · reverted 1 · skipped 0\n",
		},
		{
			name: "pass keeps the edit",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				return &linesFixer{edit: o.editTo(t, "version"), res: alias}, linesFailures(), answers("y\n"), o.newChecker
			},
			want: linesPrompt +
				"✔ " + linesPkg + " fixed using model alias \"sonnet\": path repaired (resolved upstream 2.0.0)\n" +
				"fixed 1 · reverted 0 · skipped 0\n",
		},
		{
			name: "pass with a version that cannot be ordered warns",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				return &linesFixer{edit: o.editTo(t, "nightly"), res: pinned}, linesFailures(), answers("y\n"), o.newChecker
			},
			want: linesPrompt +
				"✔ " + linesPkg + " fixed using model \"claude-sonnet-4-5\": tried (resolved upstream nightly)\n" +
				"  warning: " + linesPkg + " extracted version \"nightly\" is not orderable against the current version; the parser may need more work\n" +
				"fixed 1 · reverted 0 · skipped 0\n",
		},
		{
			name: "still failing, kept",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				return &linesFixer{edit: o.editTo(t, "wrong"), res: alias}, linesFailures(), answers("y\n", "y\n"), o.newChecker
			},
			want: linesPrompt +
				"  " + linesPkg + " still failing after fix using model alias \"sonnet\": path repaired (agent was refused: WebFetch(example.com))\n" +
				"  error: failed to fetch upstream version: all version extraction methods failed: failed to parse version: JSON path not found in response: field \"wrong\" not found\n" +
				linesKeep + "fixed 1 · reverted 0 · skipped 0\n",
		},
		{
			name: "still failing, reverted",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				return &linesFixer{edit: o.editTo(t, "wrong"), res: pinned}, linesFailures(), answers("y\n", "N\n"), o.newChecker
			},
			want: linesPrompt + linesStillWrong + linesKeep + "fixed 0 · reverted 1 · skipped 0\n",
		},
		{
			name: "still failing, revert whose restore fails warns",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				in := &linesReader{answers: []string{"y\n", "N\n"}, hookAt: 1, hook: func() { o.breakRestore(t) }}
				return &linesFixer{edit: o.editTo(t, "wrong"), res: pinned}, linesFailures(), in, o.newChecker
			},
			want: linesPrompt + linesStillWrong + linesKeep + linesRestoreWarn + "fixed 0 · reverted 1 · skipped 0\n",
		},
		{
			name: "n then a over several packages, non-fetch failures never offered",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				f := linesFailures("app-misc/aaa", "app-misc/bbb")
				f["zzz-misc/plain"] = errors.New("timeout")
				return &linesFixer{edit: o.editTo(t, "version"), res: alias}, f, answers("n\n", "a\n", "N\n"), o.newChecker
			},
			want: "Fix registry for app-misc/aaa with LLM? [y/N/a/q] " +
				"Fix registry for app-misc/bbb with LLM? [y/N/a/q] " +
				"  app-misc/bbb still failing after fix using model alias \"sonnet\": path repaired (agent was refused: WebFetch(example.com))\n" +
				"  error: package not found in configuration: app-misc/bbb\n" +
				linesKeep +
				"✔ " + linesPkg + " fixed using model alias \"sonnet\": path repaired (resolved upstream 2.0.0)\n" +
				"fixed 1 · reverted 1 · skipped 1\n",
		},
		{
			name: "q stops before the remaining packages",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				return &linesFixer{res: pinned}, linesFailures("app-misc/aaa"), answers("q\n"), o.newChecker
			},
			want: "Fix registry for app-misc/aaa with LLM? [y/N/a/q] fixed 0 · reverted 0 · skipped 0\n",
		},
		{
			name: "unrecognized answer skips, then EOF stops",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				return &linesFixer{res: pinned}, linesFailures("app-misc/aaa", "app-misc/bbb"), answers("maybe\n"), o.newChecker
			},
			want: "Fix registry for app-misc/aaa with LLM? [y/N/a/q] " +
				"Fix registry for app-misc/bbb with LLM? [y/N/a/q] " +
				"fixed 0 · reverted 0 · skipped 1\n",
		},
		{
			name: "no fetch failure prints nothing",
			setup: func(t *testing.T, o *linesOverlay) (fixer.RegistryFixer, map[string]error, io.Reader, func() (*autoupdate.Checker, error)) {
				return &linesFixer{res: pinned}, map[string]error{linesPkg: errors.New("timeout")}, answers("y\n"), o.newChecker
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newLinesOverlay(t)
			fixer, failures, in, newChecker := tc.setup(t, o)
			var loopErr error
			out := captureStdout(t, func() {
				loopErr = promptRegistryFixes(context.Background(), o.dir, fixer, failures, in, newChecker)
			})
			if loopErr != nil {
				t.Fatalf("promptRegistryFixes returned %v; a per-package outcome is never fatal", loopErr)
			}
			out = strings.ReplaceAll(out, o.dir, "<OVERLAY>")
			out = linesTempSuffix.ReplaceAllString(out, "bentoo-<PID>-<RAND>")
			if out != tc.want {
				t.Errorf("stdout differs from the pre-060 lines (U3)\n got: %q\nwant: %q", out, tc.want)
			}
		})
	}
}
