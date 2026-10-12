package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/logging"
)

const (
	repairPkg     = "dev-go/repairdemo"
	repairCurrent = "1.0"
	repairCand    = "1.1"
)

// repairFix is a --repair fixture: a temp HOME, overlay, pending entry, staged
// tree with a bundle, and a PATH holding only stub tools.
type repairFix struct {
	overlay, configDir, staging, staged, pkgDir, bundlePath, toolDir, record string
	created                                                                  time.Time
}

// repairFixture builds the fixture. toolExit is the exit status of every stub
// gate tool (pkgdev, pkgcheck, emerge, ebuild, egencache, portageq).
func repairFixture(t *testing.T, auOpts *autoupdateOptions, toolExit int) *repairFix {
	t.Helper()
	overlay, configDir := s056AutoupdateEnv(t, auOpts)
	oRepair, oDistdir := auOpts.repair, auOpts.distdir
	t.Cleanup(func() { auOpts.repair, auOpts.distdir = oRepair, oDistdir })
	auOpts.check = false
	auOpts.repair = repairPkg
	auOpts.distdir = t.TempDir()

	f := &repairFix{overlay: overlay, configDir: configDir, staging: filepath.Join(configDir, stagingDirName)}
	pkgPub := filepath.Join(overlay, "dev-go", "repairdemo")
	if err := os.MkdirAll(pkgPub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgPub, "repairdemo-"+repairCurrent+".ebuild"), []byte("EAPI=8\nSLOT=\"0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "profiles", "repo_name"), []byte("bentoo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Add(autoupdate.PendingUpdate{Package: repairPkg, CurrentVersion: repairCurrent, NewVersion: repairCand, Status: autoupdate.StatusFailed}); err != nil {
		t.Fatal(err)
	}
	f.staged, err = validate.Stage(validate.StageRequest{
		Overlay: overlay, StagingRoot: f.staging, Key: repairPkg, Version: repairCand,
		EbuildBytes: []byte("EAPI=8\nSLOT=\"0\"\n# repaired\n"),
	})
	if err != nil {
		t.Fatalf("staging the candidate: %v", err)
	}
	f.pkgDir = filepath.Join(f.staged, "dev-go", "repairdemo")
	f.created = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	b := repair.Bundle{
		Schema: repair.SchemaVersion, Package: repairPkg, CurrentVersion: repairCurrent, Candidate: repairCand,
		Stage: "re-check", Failure: "manifest command failed: 404 Not Found",
		RefusedTools: []string{"Bash", "WebFetch(raw.example.org)"}, StagedRoot: f.staged,
		EbuildPath: filepath.Join(f.pkgDir, "repairdemo-"+repairCand+".ebuild"), Created: f.created,
	}
	if err := repair.WriteBundle(b); err != nil {
		t.Fatal(err)
	}
	f.bundlePath = filepath.Join(f.staged, repair.BundleFile)

	f.toolDir = t.TempDir()
	f.record = filepath.Join(t.TempDir(), "claude-record")
	writeStub(t, f.toolDir, "claude", fmt.Sprintf("pwd > %q.cwd\nprintf '%%s\\0' \"$@\" > %q.argv\nexit 0\n", f.record, f.record))
	for _, tool := range []string{"pkgdev", "pkgcheck", "emerge", "ebuild", "egencache", "portageq"} {
		writeStub(t, f.toolDir, tool, fmt.Sprintf("exit %d\n", toolExit))
	}
	t.Setenv("PATH", f.toolDir+":/usr/bin:/bin")
	return f
}

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// ttyDeps returns production deps whose terminal predicates answer tty.
func ttyDeps(stdinTTY, stdoutTTY bool) *deps {
	d := defaultDeps()
	d.checkInteractive = func() bool { return stdinTTY }
	d.uiIsTerminal = func() bool { return stdoutTTY }
	return d
}

// runRepair runs runAutoupdate with fds 1 and 2 captured and answer on stdin.
func runRepair(t *testing.T, auOpts *autoupdateOptions, d *deps, answer string) (code int, out string) {
	t.Helper()
	return runRepairCtx(t, context.Background(), auOpts, d, answer)
}

// runRepairCtx is func runRepair under parent, the context the command runs in.
func runRepairCtx(t *testing.T, parent context.Context, auOpts *autoupdateOptions, d *deps, answer string) (code int, out string) {
	t.Helper()
	auCmd := testAutoupdateCmd() //nolint:contextcheck // parent reaches the command through SetContext below
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	in, err := os.CreateTemp(t.TempDir(), "in")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.WriteString(answer); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	origStdin := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = origStdin }()
	saved1, err1 := syscall.Dup(1)
	saved2, err2 := syscall.Dup(2)
	if err1 != nil || err2 != nil {
		t.Fatalf("dup: %v %v", err1, err2)
	}
	if err := syscall.Dup3(int(f.Fd()), 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Dup3(int(f.Fd()), 2, 0); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			_ = syscall.Dup3(saved1, 1, 0)
			_ = syscall.Dup3(saved2, 2, 0)
			_ = syscall.Close(saved1)
			_ = syscall.Close(saved2)
		}()
		auCmd.SetContext(logging.NewContext(parent, slog.New(slog.NewTextHandler(os.Stderr, nil))))
		rerr := runAutoupdate(auCmd, nil, auOpts, d)                 //nolint:contextcheck // runAutoupdate reads parent from auCmd, set above
		if st, bare := rerr.(*exitStatus); bare && st.cause != nil { //nolint:errorlint // the same identity test as func execute
			fmt.Fprintln(os.Stderr, st.cause)
		}
		code = exitCodeFor(rerr)
	}()
	data, _ := os.ReadFile(f.Name())
	return code, string(data)
}

func readFileOrEmpty(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

// assertNoSession fails when the fake claude ran.
func assertNoSession(t *testing.T, f *repairFix) {
	t.Helper()
	if _, err := os.Stat(f.record + ".argv"); err == nil {
		t.Fatalf("a claude session was started")
	}
}

// TestRepairFlagConflictsWithEveryOtherMode covers R2.1.
func TestRepairFlagConflictsWithEveryOtherMode(t *testing.T) {
	cases := []struct {
		flag string
		set  func(o *autoupdateOptions)
	}{
		{"--apply", func(o *autoupdateOptions) { o.apply = repairPkg }},
		{"--check", func(o *autoupdateOptions) { o.check = true }},
		{"--revive", func(o *autoupdateOptions) { o.revive = repairPkg }},
		{"--clean", func(o *autoupdateOptions) { o.clean = true }},
		{"--list", func(o *autoupdateOptions) { o.list = true }},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			auOpts := testAutoupdateOptions()
			f := repairFixture(t, auOpts, 1)
			tc.set(auOpts)
			before := readFileOrEmpty(f.bundlePath)
			code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
			if code != 1 || !strings.Contains(out, "--repair") || !strings.Contains(out, tc.flag) {
				t.Fatalf("--repair with %s: exit %d, want 1 naming %s:\n%s", tc.flag, code, tc.flag, out)
			}
			assertNoSession(t, f)
			if readFileOrEmpty(f.bundlePath) != before {
				t.Fatalf("a refused --repair changed the bundle")
			}
		})
	}
}

// TestRepairRefusesWithoutATerminal covers R2.2 for stdin and stdout apart.
func TestRepairRefusesWithoutATerminal(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stdin, stdout bool
	}{{"stdin not a terminal", false, true}, {"stdout not a terminal", true, false}, {"neither", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			auOpts := testAutoupdateOptions()
			f := repairFixture(t, auOpts, 1)
			code, out := runRepair(t, auOpts, ttyDeps(tc.stdin, tc.stdout), "y\n")
			if code != 1 || !strings.Contains(strings.ToLower(out), "terminal") {
				t.Fatalf("exit %d, want 1 with an error about an interactive terminal:\n%s", code, out)
			}
			assertNoSession(t, f)
		})
	}
}

// TestRepairRefusesWhenClaudeIsMissing covers R2.3.
func TestRepairRefusesWhenClaudeIsMissing(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 1)
	if err := os.Remove(filepath.Join(f.toolDir, "claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", f.toolDir)
	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
	if code != 1 || !strings.Contains(out, "claude") {
		t.Fatalf("exit %d, want 1 naming the missing claude executable:\n%s", code, out)
	}
}

// TestRepairRefusesWhenNoBundleExists covers R2.4: the error lists the
// packages that do have one; a near-identical name does not count.
func TestRepairRefusesWhenNoBundleExists(t *testing.T) {
	auOpts := testAutoupdateOptions()
	repairFixture(t, auOpts, 1)
	auOpts.repair = "dev-go/repairdem"
	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
	if code != 1 || !strings.Contains(out, repairPkg) {
		t.Fatalf("exit %d, want 1 listing %s as the package with a bundle:\n%s", code, repairPkg, out)
	}
}

// TestRepairRefusesAnUnreadableBundle covers R2.5.
func TestRepairRefusesAnUnreadableBundle(t *testing.T) {
	for _, content := range []string{`{"schema":9}`, "not json"} {
		auOpts := testAutoupdateOptions()
		f := repairFixture(t, auOpts, 1)
		if err := os.WriteFile(f.bundlePath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
		if code != 1 || !strings.Contains(out, f.bundlePath) {
			t.Fatalf("bundle %q: exit %d, want 1 naming %s:\n%s", content, code, f.bundlePath, out)
		}
		assertNoSession(t, f)
	}
}

// TestRepairRefusesAGoneStagedTree covers R2.6.
func TestRepairRefusesAGoneStagedTree(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 1)
	gone := filepath.Join(t.TempDir(), "moved", repairCand)
	data := strings.Replace(readFileOrEmpty(f.bundlePath), `"staged_root": "`+f.staged+`"`, `"staged_root": "`+gone+`"`, 1)
	if !strings.Contains(data, gone) {
		data = strings.Replace(readFileOrEmpty(f.bundlePath), `"staged_root":"`+f.staged+`"`, `"staged_root":"`+gone+`"`, 1)
	}
	if !strings.Contains(data, gone) {
		t.Fatalf("fixture: could not rewrite staged_root in %s", f.bundlePath)
	}
	if err := os.WriteFile(f.bundlePath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
	if code != 1 || !strings.Contains(out, gone) {
		t.Fatalf("exit %d, want 1 naming the missing %s:\n%s", code, gone, out)
	}
	assertNoSession(t, f)
}

// TestRepairFlagNeedsTheOverlayLock: --repair is a writing mode.
func TestRepairFlagNeedsTheOverlayLock(t *testing.T) {
	o := testAutoupdateOptions()
	o.repair = repairPkg
	if !testAutoupdateRun(o).autoupdateNeedsOverlayLock() {
		t.Fatalf("autoupdateNeedsOverlayLock() = false for --repair")
	}
}
