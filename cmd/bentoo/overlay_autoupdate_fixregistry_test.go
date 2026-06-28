package main

// Authored (Red-phase) INTEGRATION test for story 014 — promptRegistryFixes loop
// and the snapshot/restore helpers (sub-tasks 3.1 + 3.2).
//
// Independent contract spec. References promptRegistryFixes, which does not exist
// yet, so cmd/bentoo fails to COMPILE until Task 3 lands — that compile failure is
// the expected Red signal.
//
// This is the highest-value test and is deliberately REAL, not mocked through:
//   - newChecker is a REAL autoupdate.NewChecker factory over a temp overlay that
//     contains a real .autoupdate/packages.toml and a real ebuild on disk.
//   - The fake RegistryFixer PHYSICALLY rewrites that packages.toml (simulating the
//     agent edit), so the re-check exercises the genuine "fresh Checker reloads the
//     edited file" path (R4 config-staleness, R-Config-staleness HIGH).
//   - A local httptest server returns a JSON version payload, so a correct config
//     extracts a real upstream version and a broken config fails at parse time —
//     no network, no orphan, deterministic.
//
// Side-effect assertions (the load-bearing part):
//   - keep path: the edited packages.toml bytes SURVIVE on disk after a passing
//     re-check (the edit is kept).
//   - revert path: after a still-failing re-check answered "N", the on-disk
//     packages.toml equals the pre-edit snapshot BYTE-FOR-BYTE (R5.4).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// fakeRegistryFixer is a RegistryFixer test double that, on each call, runs onCall
// to physically mutate the temp packages.toml (simulating the agent edit). It never
// shells out to claude.
type fakeRegistryFixer struct {
	called  int
	summary string
	err     error
	onCall  func(req autoupdate.RegistryFixRequest)
	lastReq autoupdate.RegistryFixRequest
}

func (f *fakeRegistryFixer) FixRegistry(_ context.Context, req autoupdate.RegistryFixRequest) (autoupdate.RegistryFixResult, error) {
	f.called++
	f.lastReq = req
	if f.onCall != nil {
		f.onCall(req)
	}
	if f.err != nil {
		return autoupdate.RegistryFixResult{}, f.err
	}
	return autoupdate.RegistryFixResult{Summary: f.summary}, nil
}

var _ autoupdate.RegistryFixer = (*fakeRegistryFixer)(nil)

// regfixHarness builds a real overlay (ebuild + packages.toml) for one package whose
// config initially FAILS extraction (path points at a missing JSON field), plus a
// real newChecker factory and a JSON server. The returned writeConfig rewrites the
// packages.toml with the given JSON path so a fixer "edit" can flip fail↔pass.
type regfixHarness struct {
	overlayDir  string
	configPath  string
	pkg         string
	serverURL   string
	newChecker  func() (*autoupdate.Checker, error)
	writeConfig func(t *testing.T, jsonPath string)
}

func newRegfixHarness(t *testing.T) *regfixHarness {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "2.0.0"})
	}))
	t.Cleanup(server.Close)

	overlayDir := t.TempDir()
	configDir := t.TempDir() // checker state dir (cache/pending); kept off the overlay

	pkg := "media-gfx/inkscape"

	// Real ebuild so getCurrentVersion succeeds (no orphan).
	writeExitTestEbuild(t, overlayDir, pkg, "1.0.0")

	cfgDir := filepath.Join(overlayDir, ".autoupdate")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", cfgDir, err)
	}
	configPath := filepath.Join(cfgDir, "packages.toml")

	writeConfig := func(t *testing.T, jsonPath string) {
		t.Helper()
		var b strings.Builder
		b.WriteString("[\"" + pkg + "\"]\n")
		b.WriteString("url = \"" + server.URL + "\"\n")
		b.WriteString("parser = \"json\"\n")
		b.WriteString("path = \"" + jsonPath + "\"\n")
		if err := os.WriteFile(configPath, []byte(b.String()), 0o644); err != nil {
			t.Fatalf("write packages.toml: %v", err)
		}
	}

	// Initial (broken) config: path points at a field that does not exist → the
	// fetch returns 200 and extraction fails at parse time (a real ErrFetchFailed).
	writeConfig(t, "nonexistent")

	newChecker := func() (*autoupdate.Checker, error) {
		return autoupdate.NewChecker(overlayDir,
			autoupdate.WithConfigDir(configDir),
			autoupdate.WithContext(context.Background()),
			autoupdate.WithConcurrency(autoupdate.DefaultConcurrency),
		)
	}

	return &regfixHarness{
		overlayDir:  overlayDir,
		configPath:  configPath,
		pkg:         pkg,
		serverURL:   server.URL,
		newChecker:  newChecker,
		writeConfig: writeConfig,
	}
}

// failuresForFetchError builds the failures map promptRegistryFixes consumes, wiring
// the package error as a wrapped ErrFetchFailed (the only class offered, R3.5).
func (h *regfixHarness) failuresForFetchError() map[string]error {
	return map[string]error{
		h.pkg: fmt.Errorf("%w: parse: field %q not found", autoupdate.ErrFetchFailed, "nonexistent"),
	}
}

// TestPromptRegistryFixes_KeepOnPassingRecheck pins R4.1/R4.2/R5.2/R6.1: a "y" answer
// invokes the fixer (which edits packages.toml to a working path), the FRESH checker
// reloads the edited file, the re-check PASSES, and the edited bytes SURVIVE on disk
// (the edit is kept, not reverted).
func TestPromptRegistryFixes_KeepOnPassingRecheck(t *testing.T) {
	h := newRegfixHarness(t)

	preEdit, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatalf("read pre-edit config: %v", err)
	}

	fixer := &fakeRegistryFixer{
		summary: "changed path from nonexistent to version",
		onCall: func(req autoupdate.RegistryFixRequest) {
			// Simulate the agent's edit: rewrite to a working JSON path.
			h.writeConfig(t, "version")
		},
	}

	// Reader answers the per-package prompt "y" (fix) — and nothing else is needed
	// because the re-check passes (no keep/revert prompt).
	in := strings.NewReader("y\n")

	if err := promptRegistryFixes(context.Background(), h.overlayDir, fixer,
		h.failuresForFetchError(), in, h.newChecker); err != nil {
		t.Fatalf("promptRegistryFixes: %v", err)
	}

	if fixer.called != 1 {
		t.Errorf("fixer called %d times, want 1", fixer.called)
	}
	// The fixer must have been handed the .autoupdate dir as ConfigDir (R2.2 contract).
	wantDir := filepath.Join(h.overlayDir, ".autoupdate")
	if h.fixerConfigDir(fixer) != wantDir {
		t.Errorf("RegistryFixRequest.ConfigDir = %q, want %q", h.fixerConfigDir(fixer), wantDir)
	}

	// Side-effect: the edited bytes survive (the kept edit). The file must NOT equal
	// the pre-edit snapshot, and must contain the working path.
	postKeep, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatalf("read post-keep config: %v", err)
	}
	if bytes.Equal(postKeep, preEdit) {
		t.Error("kept edit was lost: on-disk config equals the pre-edit snapshot")
	}
	if !strings.Contains(string(postKeep), "path = \"version\"") {
		t.Errorf("kept config missing the working path; got:\n%s", postKeep)
	}
}

// TestPromptRegistryFixes_RevertOnFailingRecheck pins R5.1/R5.3/R5.4: a "y" answer
// invokes the fixer (which makes a DIFFERENT broken edit), the fresh re-check still
// FAILS, the keep/revert prompt is answered "N", and the on-disk packages.toml is
// restored BYTE-FOR-BYTE to the pre-edit snapshot.
func TestPromptRegistryFixes_RevertOnFailingRecheck(t *testing.T) {
	h := newRegfixHarness(t)

	preEdit, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatalf("read pre-edit config: %v", err)
	}

	fixer := &fakeRegistryFixer{
		summary: "tried a different (still wrong) path",
		onCall: func(req autoupdate.RegistryFixRequest) {
			// A different broken edit: still no matching field → re-check fails.
			h.writeConfig(t, "still_wrong")
		},
	}

	// "y" to fix, then "N" to the keep/revert prompt on the failing re-check.
	in := strings.NewReader("y\nN\n")

	if err := promptRegistryFixes(context.Background(), h.overlayDir, fixer,
		h.failuresForFetchError(), in, h.newChecker); err != nil {
		t.Fatalf("promptRegistryFixes: %v", err)
	}

	if fixer.called != 1 {
		t.Errorf("fixer called %d times, want 1", fixer.called)
	}

	// Side-effect (load-bearing): the on-disk config is restored byte-for-byte.
	postRevert, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatalf("read post-revert config: %v", err)
	}
	if !bytes.Equal(postRevert, preEdit) {
		t.Errorf("revert did not restore the snapshot byte-for-byte:\npre-edit:\n%s\npost-revert:\n%s",
			preEdit, postRevert)
	}
}

// TestPromptRegistryFixes_SkipsNonFetchFailures pins R3.5: a failure that is NOT
// wrapped ErrFetchFailed must never reach the fixer (no prompt, no edit).
func TestPromptRegistryFixes_SkipsNonFetchFailures(t *testing.T) {
	h := newRegfixHarness(t)

	fixer := &fakeRegistryFixer{summary: "should never run"}

	// A non-extraction failure for a different package; promptRegistryFixes must
	// filter on errors.Is(e, ErrFetchFailed) and offer nothing here.
	failures := map[string]error{
		"dev-vcs/git": errors.New("manifest verification failed"),
	}

	in := strings.NewReader("y\n") // would answer yes IF anything were offered
	if err := promptRegistryFixes(context.Background(), h.overlayDir, fixer,
		failures, in, h.newChecker); err != nil {
		t.Fatalf("promptRegistryFixes: %v", err)
	}
	if fixer.called != 0 {
		t.Errorf("fixer called %d times for a non-ErrFetchFailed failure, want 0", fixer.called)
	}
}

// TestPromptRegistryFixes_FixerErrorRestoresAndContinues pins R5.5: when FixRegistry
// itself errors, the snapshot is restored and the loop continues (no panic, file
// unchanged byte-for-byte).
func TestPromptRegistryFixes_FixerErrorRestoresAndContinues(t *testing.T) {
	h := newRegfixHarness(t)

	preEdit, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatalf("read pre-edit config: %v", err)
	}

	fixer := &fakeRegistryFixer{
		err: errors.New("claude budget exceeded"),
		onCall: func(req autoupdate.RegistryFixRequest) {
			// The agent partially edited before erroring.
			h.writeConfig(t, "partial_broken")
		},
	}

	in := strings.NewReader("y\n")
	if err := promptRegistryFixes(context.Background(), h.overlayDir, fixer,
		h.failuresForFetchError(), in, h.newChecker); err != nil {
		t.Fatalf("promptRegistryFixes should not fail on a per-package fixer error: %v", err)
	}

	postErr, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatalf("read post-error config: %v", err)
	}
	if !bytes.Equal(postErr, preEdit) {
		t.Errorf("fixer-error path did not restore the snapshot:\npre-edit:\n%s\npost:\n%s", preEdit, postErr)
	}
}

// fixerConfigDir reads the ConfigDir from the fixer's last request (helper to keep
// the assertion above readable).
func (h *regfixHarness) fixerConfigDir(f *fakeRegistryFixer) string {
	return f.lastReq.ConfigDir
}
