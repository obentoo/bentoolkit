package autoupdate

// Authored for story 060, sub-task 1.3 — R3.1, R3.2, R3.3, R3.4, R3.5, R3.6, R3.7.
//
// Written from the contract (design C3):
//
//	func RepairableFetchFailures(failures map[string]error) []string
//	type RegistryFixStatus int // Skipped, Reverted, Passed, StillFailing
//	type RegistryFixStage int  // Snapshot, Load, Fixer, Checker; zero for Passed/StillFailing
//	type RegistryFixAttempt struct {
//	    Package string; Status RegistryFixStatus; Stage RegistryFixStage; Result *RegistryFixResult
//	    Recheck *CheckResult; RecheckErr, Err, RestoreErr error
//	}
//	func AttemptRegistryFix(ctx context.Context, overlayPath, pkg string, fetchErr error,
//	    fixer RegistryFixer, newChecker func() (*Checker, error)) *RegistryFixAttempt
//	func (a *RegistryFixAttempt) Revert() error
//
// The transaction is driven with a stub fixer that really edits packages.toml,
// and a real Checker re-checking against a local httptest server, so "kept",
// "restored" and "never written" are statements about bytes and file mode on
// disk. Nothing in the transaction may print (R3.7): every call runs with
// os.Stdout and os.Stderr swapped for a file that must stay empty.
//
// A failed restore is produced by replacing the .autoupdate directory with a
// regular file of the same name. That fails for root as well, so the test
// holds under `unshare -r` (Q11), where a read-only directory would not.
//
// Err is the RAW cause, with the text the command prints today: the caller
// picks its line from Stage and prints Err with %v, so any prefix added here
// (the package name included — Package already carries it) would change a
// printed line (U3). The snapshot errors read "failed to read packages.toml:
// …" / "failed to stat packages.toml: …"; a failed restore reads "failed to
// restore <path>: …".
//
// Red on arrival: none of the symbols above exist.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// s060Fixer is a RegistryFixer that records its request and applies edit to
// the overlay before answering.
type s060Fixer struct {
	calls   int
	lastReq RegistryFixRequest
	edit    func(t *testing.T)
	result  RegistryFixResult
	err     error
	t       *testing.T
}

func (f *s060Fixer) FixRegistry(_ context.Context, req RegistryFixRequest) (RegistryFixResult, error) {
	f.calls++
	f.lastReq = req
	if f.edit != nil {
		f.edit(f.t)
	}
	return f.result, f.err
}

var _ RegistryFixer = (*s060Fixer)(nil)

// s060Registry is one overlay whose single entry fails to extract a version
// until its JSON path is repaired to "version".
type s060Registry struct {
	overlayDir string
	configDir  string
	configPath string
	pkg        string
	serverURL  string
	stateDir   string
	original   []byte
}

const s060RegistryMode os.FileMode = 0o640

func newS060Registry(t *testing.T) *s060Registry {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "2.0.0"})
	}))
	t.Cleanup(server.Close)

	r := &s060Registry{
		overlayDir: t.TempDir(),
		stateDir:   t.TempDir(),
		pkg:        "media-gfx/inkscape",
		serverURL:  server.URL,
	}
	r.configDir = filepath.Join(r.overlayDir, ".autoupdate")
	r.configPath = filepath.Join(r.configDir, "packages.toml")
	createTestEbuildFile(t, r.overlayDir, r.pkg, "1.0.0")
	if err := os.MkdirAll(r.configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.original = []byte(r.entry("nonexistent"))
	if err := os.WriteFile(r.configPath, r.original, s060RegistryMode); err != nil {
		t.Fatal(err)
	}
	// WriteFile honours the umask; the mode under test must be exactly this.
	if err := os.Chmod(r.configPath, s060RegistryMode); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *s060Registry) entry(jsonPath string) string {
	return "[\"" + r.pkg + "\"]\n" +
		"url = \"" + r.serverURL + "\"\n" +
		"parser = \"json\"\n" +
		"path = \"" + jsonPath + "\"\n"
}

// editTo returns a fixer edit that rewrites the entry's JSON path.
func (r *s060Registry) editTo(jsonPath string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		if err := os.WriteFile(r.configPath, []byte(r.entry(jsonPath)), 0o644); err != nil {
			t.Fatalf("fixer edit: %v", err)
		}
	}
}

// breakRestore makes any later write of packages.toml fail: the directory that
// holds it becomes a regular file.
func (r *s060Registry) breakRestore(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(r.configDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.configDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *s060Registry) newChecker() (*Checker, error) {
	return NewChecker(r.overlayDir, WithConfigDir(r.stateDir), WithConcurrency(DefaultConcurrency))
}

func (r *s060Registry) fetchErr() error {
	return fmt.Errorf("%w: parse: field %q not found", ErrFetchFailed, "nonexistent")
}

// assertOnDisk checks packages.toml's bytes and permission bits.
func (r *s060Registry) assertOnDisk(t *testing.T, want []byte, wantMode os.FileMode, why string) {
	t.Helper()
	got, err := os.ReadFile(r.configPath)
	if err != nil {
		t.Fatalf("read packages.toml: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("packages.toml bytes — %s\n got: %q\nwant: %q", why, got, want)
	}
	info, err := os.Stat(r.configPath)
	if err != nil {
		t.Fatalf("stat packages.toml: %v", err)
	}
	if info.Mode().Perm() != wantMode {
		t.Errorf("packages.toml mode = %v, want %v — %s", info.Mode().Perm(), wantMode, why)
	}
}

// s060Silently runs fn with os.Stdout and os.Stderr pointed at a file, and
// fails the test if anything was written: the transaction has no terminal I/O
// (R3.7).
func s060Silently(t *testing.T, fn func()) {
	t.Helper()
	sink, err := os.CreateTemp(t.TempDir(), "stdio")
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = sink, sink
	func() {
		defer func() { os.Stdout, os.Stderr = origOut, origErr }()
		fn()
	}()
	_ = sink.Close()
	if got, _ := os.ReadFile(sink.Name()); len(got) != 0 {
		t.Errorf("the registry-fix transaction wrote to the terminal; it must return data only (R3.7):\n%s", got)
	}
}

func (r *s060Registry) attempt(t *testing.T, fixer *s060Fixer, newChecker func() (*Checker, error)) *RegistryFixAttempt {
	t.Helper()
	fixer.t = t
	var a *RegistryFixAttempt
	s060Silently(t, func() {
		a = AttemptRegistryFix(t.Context(), r.overlayDir, r.pkg, r.fetchErr(), fixer, newChecker)
	})
	if a == nil {
		t.Fatal("AttemptRegistryFix returned nil")
	}
	if a.Package != r.pkg {
		t.Errorf("attempt.Package = %q, want %q", a.Package, r.pkg)
	}
	return a
}

// assertStage pins Stage and that Err carries no prefix naming the package.
func assertStage(t *testing.T, a *RegistryFixAttempt, want RegistryFixStage) {
	t.Helper()
	if a.Stage != want {
		t.Errorf("Stage = %v, want %v", a.Stage, want)
	}
	if a.Err != nil && strings.Contains(a.Err.Error(), a.Package) {
		t.Errorf("Err = %q names the package; Err is the raw cause and the caller's line already names it (U3)", a.Err)
	}
}

// TestS060RegistryFixRepairableFetchFailures is R3.1's filter and order. The
// hostile half comes first: an error that merely SAYS "failed to fetch upstream
// version" without wrapping ErrFetchFailed is not repairable (a text match would
// collapse it into the class); a fetch failure wrapped twice, or joined with
// another error, still is (a shallow check would split it out).
func TestS060RegistryFixRepairableFetchFailures(t *testing.T) {
	failures := map[string]error{
		"net-misc/lookalike":   errors.New(ErrFetchFailed.Error() + ": but not wrapped"),
		"dev-util/deep":        fmt.Errorf("check dev-util/deep: %w", fmt.Errorf("%w: 404", ErrFetchFailed)),
		"dev-util/joined":      errors.Join(errors.New("cache miss"), ErrFetchFailed),
		"app-misc/plain":       errors.New("timeout"),
		"app-editors/zed":      fmt.Errorf("%w: no match", ErrFetchFailed),
		"app-editors/neovim":   fmt.Errorf("%w: parse", ErrFetchFailed),
		"app-editors/neovim:9": fmt.Errorf("%w: parse", ErrFetchFailed),
	}
	want := []string{"app-editors/neovim", "app-editors/neovim:9", "app-editors/zed", "dev-util/deep", "dev-util/joined"}

	if got := RepairableFetchFailures(failures); !reflect.DeepEqual(got, want) {
		t.Errorf("RepairableFetchFailures = %q\nwant %q — only failures wrapping ErrFetchFailed, in lexical order (R3.1)", got, want)
	}
	if got := RepairableFetchFailures(map[string]error{"a/b": errors.New("x")}); len(got) != 0 {
		t.Errorf("RepairableFetchFailures with no fetch failure = %q, want none", got)
	}
}

// TestS060RegistryFixPassedKeepsTheEdit is R3.4: the fresh re-check extracts a
// version, the edit stays on disk, and the attempt carries the model that made
// it.
func TestS060RegistryFixPassedKeepsTheEdit(t *testing.T) {
	r := newS060Registry(t)
	fixer := &s060Fixer{edit: r.editTo("version"), result: RegistryFixResult{Summary: "path repaired", Model: "claude-sonnet-4-5"}}

	a := r.attempt(t, fixer, r.newChecker)

	if a.Status != RegistryFixPassed {
		t.Fatalf("Status = %v, want RegistryFixPassed (Err=%v RecheckErr=%v)", a.Status, a.Err, a.RecheckErr)
	}
	if a.Result == nil || a.Result.Model != "claude-sonnet-4-5" || a.Result.Summary != "path repaired" {
		t.Errorf("Result = %+v, want the fixer's result naming the model that made the edit (R3.4)", a.Result)
	}
	if a.Recheck == nil || a.Recheck.UpstreamVersion != "2.0.0" {
		t.Errorf("Recheck = %+v, want the fresh re-check's upstream version 2.0.0", a.Recheck)
	}
	if a.Err != nil || a.RestoreErr != nil {
		t.Errorf("a pass carries Err=%v RestoreErr=%v, want both nil", a.Err, a.RestoreErr)
	}
	assertStage(t, a, 0)
	r.assertOnDisk(t, []byte(r.entry("version")), s060RegistryMode, "a passing fix keeps the edit")

	if fixer.calls != 1 {
		t.Fatalf("fixer called %d times, want 1", fixer.calls)
	}
	req := fixer.lastReq
	if req.Package != r.pkg || req.FetchError != r.fetchErr().Error() || req.ConfigDir != r.configDir {
		t.Errorf("fixer request = {Package:%q FetchError:%q ConfigDir:%q}, want {%q %q %q}",
			req.Package, req.FetchError, req.ConfigDir, r.pkg, r.fetchErr().Error(), r.configDir)
	}
	if req.Config == nil || req.Config.URL != r.serverURL {
		t.Errorf("fixer request Config = %+v, want the entry's current configuration", req.Config)
	}
}

// TestS060RegistryFixStillFailingLeavesTheDecisionToTheCaller is R3.5. The
// re-check still fails: the edit stays until the caller decides, and Revert
// restores the pre-attempt bytes AND file mode.
func TestS060RegistryFixStillFailingLeavesTheDecisionToTheCaller(t *testing.T) {
	r := newS060Registry(t)
	fixer := &s060Fixer{edit: r.editTo("stillwrong"), result: RegistryFixResult{Summary: "tried", Model: "opus", DeniedTools: []string{"WebFetch(example.com)"}}}

	a := r.attempt(t, fixer, r.newChecker)

	if a.Status != RegistryFixStillFailing {
		t.Fatalf("Status = %v, want RegistryFixStillFailing (Err=%v)", a.Status, a.Err)
	}
	if a.RecheckErr == nil {
		t.Error("RecheckErr is nil; the operator must be shown why the re-check still fails")
	}
	if a.Err != nil {
		t.Errorf("Err = %v on a still-failing attempt, want nil (the reason is RecheckErr)", a.Err)
	}
	assertStage(t, a, 0)
	if a.Result == nil || a.Result.Model != "opus" || !reflect.DeepEqual(a.Result.DeniedTools, []string{"WebFetch(example.com)"}) {
		t.Errorf("Result = %+v, want the fixer's result with its model and denied tools", a.Result)
	}
	r.assertOnDisk(t, []byte(r.entry("stillwrong")), s060RegistryMode, "the edit stays until the caller keeps or reverts it")

	var revertErr error
	s060Silently(t, func() { revertErr = a.Revert() })
	if revertErr != nil {
		t.Fatalf("Revert: %v", revertErr)
	}
	r.assertOnDisk(t, r.original, s060RegistryMode, "Revert restores the pre-attempt bytes and mode (R3.5)")
}

// TestS060RegistryFixRevertsWhenTheFixerFails is R3.3's first half: the fixer
// edited the file and then returned an error. The snapshot is already back.
func TestS060RegistryFixRevertsWhenTheFixerFails(t *testing.T) {
	r := newS060Registry(t)
	boom := errors.New("agent crashed")
	fixer := &s060Fixer{edit: r.editTo("halfway"), err: boom}

	a := r.attempt(t, fixer, r.newChecker)

	if a.Status != RegistryFixReverted {
		t.Fatalf("Status = %v, want RegistryFixReverted", a.Status)
	}
	if a.Err != boom { //nolint:errorlint // the raw cause itself, unwrapped
		t.Errorf("Err = %v, want the fixer's error itself (raw, so the printed text is unchanged)", a.Err)
	}
	assertStage(t, a, RegistryFixStageFixer)
	if a.RestoreErr != nil {
		t.Errorf("RestoreErr = %v, want nil — the restore succeeded", a.RestoreErr)
	}
	if a.Recheck != nil {
		t.Errorf("Recheck = %+v, want nil — no re-check runs after a fixer error", a.Recheck)
	}
	r.assertOnDisk(t, r.original, s060RegistryMode, "a fixer error restores the pre-attempt bytes and mode (R3.3)")
}

// TestS060RegistryFixRevertsWhenTheCheckerCannotBeBuilt is R3.3's second half.
func TestS060RegistryFixRevertsWhenTheCheckerCannotBeBuilt(t *testing.T) {
	r := newS060Registry(t)
	noChecker := errors.New("checker init failed")
	fixer := &s060Fixer{edit: r.editTo("version"), result: RegistryFixResult{Summary: "ok", Model: "sonnet"}}

	a := r.attempt(t, fixer, func() (*Checker, error) { return nil, noChecker })

	if a.Status != RegistryFixReverted {
		t.Fatalf("Status = %v, want RegistryFixReverted", a.Status)
	}
	if a.Err != noChecker { //nolint:errorlint // the raw cause itself, unwrapped
		t.Errorf("Err = %v, want the checker-construction error itself", a.Err)
	}
	assertStage(t, a, RegistryFixStageChecker)
	r.assertOnDisk(t, r.original, s060RegistryMode, "a re-check that cannot be built restores the snapshot (R3.3)")
}

// TestS060RegistryFixSkipsWithoutWriting is R3.2: with no readable, statable,
// parseable packages.toml there is no snapshot to fall back on, so the fixer is
// never called and nothing is written.
func TestS060RegistryFixSkipsWithoutWriting(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		r := newS060Registry(t)
		if err := os.Remove(r.configPath); err != nil {
			t.Fatal(err)
		}
		fixer := &s060Fixer{edit: r.editTo("version")}

		a := r.attempt(t, fixer, r.newChecker)

		if a.Status != RegistryFixSkipped || a.Err == nil {
			t.Errorf("Status = %v Err = %v, want RegistryFixSkipped with the reason", a.Status, a.Err)
		}
		if fixer.calls != 0 {
			t.Errorf("the fixer ran %d time(s) without a snapshot (R3.2)", fixer.calls)
		}
		assertStage(t, a, RegistryFixStageSnapshot)
		if a.Err != nil && (!strings.HasPrefix(a.Err.Error(), "failed to read packages.toml: ") || !errors.Is(a.Err, os.ErrNotExist)) {
			t.Errorf("Err = %q, want today's text \"failed to read packages.toml: …\" wrapping the read error", a.Err)
		}
		if _, err := os.Stat(r.configPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("packages.toml was created by a skipped attempt (stat err = %v) (R3.2)", err)
		}
	})
	t.Run("unparseable", func(t *testing.T) {
		r := newS060Registry(t)
		garbage := []byte("[[[ this is not toml\n")
		if err := os.WriteFile(r.configPath, garbage, s060RegistryMode); err != nil {
			t.Fatal(err)
		}
		fixer := &s060Fixer{edit: r.editTo("version")}

		a := r.attempt(t, fixer, r.newChecker)

		if a.Status != RegistryFixSkipped || a.Err == nil {
			t.Errorf("Status = %v Err = %v, want RegistryFixSkipped with the reason", a.Status, a.Err)
		}
		if fixer.calls != 0 {
			t.Errorf("the fixer ran %d time(s) on a registry that does not parse (R3.2)", fixer.calls)
		}
		assertStage(t, a, RegistryFixStageLoad)
		_, loadErr := registry.LoadPackagesConfig(r.overlayDir)
		if loadErr == nil {
			t.Fatal("fixture defect: the garbage registry loads")
		}
		if a.Err == nil || a.Err.Error() != loadErr.Error() {
			t.Errorf("Err = %v, want LoadPackagesConfig's own error text %q, unwrapped", a.Err, loadErr)
		}
		r.assertOnDisk(t, garbage, s060RegistryMode, "a skipped attempt never writes (R3.2)")
	})
}

// TestS060RegistryFixReportsAFailedRestore is R3.6's domain half: the restore
// itself fails. The attempt still counts as reverted, carries the restore
// error for the caller's warning, and returns instead of stopping the run. The
// same holds for an explicit Revert after a still-failing re-check.
func TestS060RegistryFixReportsAFailedRestore(t *testing.T) {
	t.Run("automatic restore after a fixer error", func(t *testing.T) {
		r := newS060Registry(t)
		boom := errors.New("agent crashed")
		fixer := &s060Fixer{edit: func(t *testing.T) { r.breakRestore(t) }, err: boom}

		a := r.attempt(t, fixer, r.newChecker)

		if a.Status != RegistryFixReverted {
			t.Errorf("Status = %v, want RegistryFixReverted — a failed restore is still counted as reverted (R3.6)", a.Status)
		}
		if a.RestoreErr == nil {
			t.Error("RestoreErr is nil although packages.toml could not be restored; the caller cannot warn (R3.6)")
		} else if want := "failed to restore " + r.configPath + ": "; !strings.HasPrefix(a.RestoreErr.Error(), want) {
			t.Errorf("RestoreErr = %q, want today's prefix %q", a.RestoreErr, want)
		}
		assertStage(t, a, RegistryFixStageFixer)
		if a.Err != boom { //nolint:errorlint // the raw cause itself
			t.Errorf("Err = %v, want the fixer's error kept beside the restore error", a.Err)
		}
	})
	t.Run("explicit Revert", func(t *testing.T) {
		r := newS060Registry(t)
		fixer := &s060Fixer{edit: r.editTo("stillwrong"), result: RegistryFixResult{Summary: "tried"}}

		a := r.attempt(t, fixer, r.newChecker)
		if a.Status != RegistryFixStillFailing {
			t.Fatalf("Status = %v, want RegistryFixStillFailing", a.Status)
		}
		r.breakRestore(t)
		var revertErr error
		s060Silently(t, func() { revertErr = a.Revert() })
		if revertErr == nil {
			t.Error("Revert returned nil although packages.toml could not be written (R3.6)")
		} else if want := "failed to restore " + r.configPath + ": "; !strings.HasPrefix(revertErr.Error(), want) {
			t.Errorf("Revert error = %q, want today's prefix %q", revertErr, want)
		}
	})
}

// TestS060RegistryFixAttemptsAreIndependent drives two packages through the
// transaction in turn, the way the caller's loop does: the first one's
// reverted edit must not leak into the second one's snapshot.
func TestS060RegistryFixAttemptsAreIndependent(t *testing.T) {
	r := newS060Registry(t)

	first := r.attempt(t, &s060Fixer{edit: r.editTo("halfway"), err: errors.New("crash")}, r.newChecker)
	if first.Status != RegistryFixReverted {
		t.Fatalf("first attempt Status = %v, want RegistryFixReverted", first.Status)
	}
	second := r.attempt(t, &s060Fixer{edit: r.editTo("stillwrong")}, r.newChecker)
	if second.Status != RegistryFixStillFailing {
		t.Fatalf("second attempt Status = %v, want RegistryFixStillFailing", second.Status)
	}
	if err := second.Revert(); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	r.assertOnDisk(t, r.original, s060RegistryMode, "each attempt snapshots the file as it stands before it")
	if strings.Contains(string(r.original), "halfway") {
		t.Fatal("fixture defect")
	}
}
