package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Story 007 T1.1 — snapper engine driver (R1, R6).
//
// These tests mirror engine_btrbk_test.go: a MockRunner captures every snapper
// invocation, so the driver's full code path runs without a real snapper or
// btrfs. Real snapper is exercised only by gated *_live_test.go files.
// ---------------------------------------------------------------------------

// TestSnapperConfigName: the snapper config name is derived from the subvolume
// path — "/" is the canonical "root" config, nested paths flatten with "_".
func TestSnapperConfigName(t *testing.T) {
	cases := []struct{ subvolume, want string }{
		{"/", "root"},
		{"/home", "home"},
		{"/var/log", "var_log"},
	}
	for _, c := range cases {
		if got := snapperConfigName(c.subvolume); got != c.want {
			t.Errorf("snapperConfigName(%q) = %q, want %q", c.subvolume, got, c.want)
		}
	}
}

// TestSnapperEngine_CreateInvokesSnapper: Create runs `snapper -c <config>
// create` with a bentoo-identifying description (R1.2), the timeline cleanup
// algorithm (so prune's `cleanup timeline` governs these snapshots, R1.4), and
// --print-number so the created snapshot's ID is captured.
func TestSnapperEngine_CreateInvokesSnapper(t *testing.T) {
	mock := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return []byte("42\n"), nil
		},
	}
	e := newSnapperEngine(EngineConfig{Driver: "snapper"}, mock)

	snap, err := e.Create(context.Background(), "/home")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if snap.ID != "42" {
		t.Errorf("snap.ID = %q, want %q (from --print-number output)", snap.ID, "42")
	}
	if snap.Subvolume != "/home" {
		t.Errorf("snap.Subvolume = %q, want /home", snap.Subvolume)
	}
	if len(mock.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want 1", len(mock.Calls))
	}
	c := mock.Calls[0]
	wantArgs := []string{
		"-c", "home", "create",
		"--description", "bentoo snapshot",
		"--cleanup-algorithm", "timeline",
		"--print-number",
	}
	if c.Name != "snapper" || !equalStrings(c.Args, wantArgs) {
		t.Errorf("call = %s %v, want snapper %v", c.Name, c.Args, wantArgs)
	}
}

// TestSnapperEngine_CreateWrapsNonZeroExit: a failing snapper create is wrapped
// with ErrEngineFailed so the Manager records a failed stage (R6.1).
func TestSnapperEngine_CreateWrapsNonZeroExit(t *testing.T) {
	mock := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return nil, errors.New("Unknown config")
		},
	}
	e := newSnapperEngine(EngineConfig{Driver: "snapper"}, mock)

	_, err := e.Create(context.Background(), "/home")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrEngineFailed) {
		t.Errorf("error %v is not ErrEngineFailed", err)
	}
}

// snapperListSample is a `snapper list` table in the pipe-separated layout.
// Line 0 is the "current" pseudo-snapshot and must be skipped; header and
// separator lines must be skipped too.
const snapperListSample = ` # | Type   | Pre # | Date                | User | Cleanup  | Description     | Userdata
---+--------+-------+---------------------+------+----------+-----------------+---------
0  | single |       |                     | root |          | current         |
1  | single |       | 2026-06-08 12:00:00 | root | timeline | bentoo snapshot |
2  | pre    |       | 2026-06-08 12:30:00 | root | number   | pre-emerge      |
`

// TestSnapperEngine_ListParsesOutput: List runs `snapper -c <config> list` and
// parses the table into []Snapshot (R1.3) — IDs from the number column, paths
// under <subvolume>/.snapshots/<n>/snapshot, skipping header/separator/current.
func TestSnapperEngine_ListParsesOutput(t *testing.T) {
	mock := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return []byte(snapperListSample), nil
		},
	}
	e := newSnapperEngine(EngineConfig{Driver: "snapper"}, mock)

	snaps, err := e.List(context.Background(), "/home")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mock.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want 1", len(mock.Calls))
	}
	wantArgs := []string{"-c", "home", "list"}
	if mock.Calls[0].Name != "snapper" || !equalStrings(mock.Calls[0].Args, wantArgs) {
		t.Errorf("call = %s %v, want snapper %v", mock.Calls[0].Name, mock.Calls[0].Args, wantArgs)
	}

	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2 (header, separator and #0 skipped): %+v", len(snaps), snaps)
	}
	if snaps[0].ID != "1" || snaps[1].ID != "2" {
		t.Errorf("IDs = %q, %q, want 1, 2", snaps[0].ID, snaps[1].ID)
	}
	if snaps[0].Subvolume != "/home" {
		t.Errorf("snaps[0].Subvolume = %q, want /home", snaps[0].Subvolume)
	}
	if snaps[0].Path != "/home/.snapshots/1/snapshot" {
		t.Errorf("snaps[0].Path = %q, want /home/.snapshots/1/snapshot", snaps[0].Path)
	}
	if snaps[0].CreatedAt.IsZero() {
		t.Error("snaps[0].CreatedAt is zero, want parsed date")
	}
	if got := snaps[0].CreatedAt.Format("2006-01-02 15:04:05"); got != "2026-06-08 12:00:00" {
		t.Errorf("snaps[0].CreatedAt = %q, want 2026-06-08 12:00:00", got)
	}
}

// TestSnapperEngine_ListWrapsError: a failing snapper list is wrapped with
// ErrEngineFailed (R6.1).
func TestSnapperEngine_ListWrapsError(t *testing.T) {
	mock := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return nil, errors.New("Unknown config")
		},
	}
	e := newSnapperEngine(EngineConfig{Driver: "snapper"}, mock)

	if _, err := e.List(context.Background(), "/home"); !errors.Is(err, ErrEngineFailed) {
		t.Errorf("error %v is not ErrEngineFailed", err)
	}
}

// TestSnapperEngine_PruneRunsTimelineCleanup: Prune delegates retention to
// snapper's native timeline cleanup (R1.4) — the GFS counts live in the
// rendered config's TIMELINE_LIMIT_* keys, so the policy argument is accepted
// but not re-applied in Go.
func TestSnapperEngine_PruneRunsTimelineCleanup(t *testing.T) {
	mock := &MockRunner{}
	e := newSnapperEngine(EngineConfig{Driver: "snapper"}, mock)

	if _, err := e.Prune(context.Background(), "/home", Retention{Daily: 7}); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(mock.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want 1", len(mock.Calls))
	}
	wantArgs := []string{"-c", "home", "cleanup", "timeline"}
	if mock.Calls[0].Name != "snapper" || !equalStrings(mock.Calls[0].Args, wantArgs) {
		t.Errorf("call = %s %v, want snapper %v", mock.Calls[0].Name, mock.Calls[0].Args, wantArgs)
	}
}

// TestSnapperEngine_PruneWrapsError: a failing cleanup is wrapped with
// ErrEngineFailed (R6.1).
func TestSnapperEngine_PruneWrapsError(t *testing.T) {
	mock := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return nil, errors.New("cleanup failed")
		},
	}
	e := newSnapperEngine(EngineConfig{Driver: "snapper"}, mock)

	if _, err := e.Prune(context.Background(), "/home", Retention{}); !errors.Is(err, ErrEngineFailed) {
		t.Errorf("error %v is not ErrEngineFailed", err)
	}
}

// TestNewEngine_SnapperDriver: the factory's `case "snapper"` returns the
// snapper engine (R1.1, R6.2 — additive: btrbk stays the default-tested path).
func TestNewEngine_SnapperDriver(t *testing.T) {
	e, err := newEngine(EngineConfig{Driver: "snapper"}, nil, &MockRunner{})
	if err != nil {
		t.Fatalf("newEngine(snapper): %v", err)
	}
	if e.Name() != "snapper" {
		t.Errorf("Name() = %q, want snapper", e.Name())
	}
}

// ---------------------------------------------------------------------------
// Story 007 T1.2 — snapper config rendering + apply integration (R2, R5).
// ---------------------------------------------------------------------------

// stubSnapperConfigsDir points the snapperConfigsDir seam at a temp dir for the
// test's duration, mirroring how redirectStateDir handles StateDir.
//
// A test that reaches ensureSnapperConfigs must ALSO call stubSnapperConfdPath:
// ensuring configs registers them in /etc/conf.d/snapper (R1.1), so redirecting
// only this seam still writes the developer's real /etc.
func stubSnapperConfigsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := snapperConfigsDir
	t.Cleanup(func() { snapperConfigsDir = orig })
	snapperConfigsDir = dir
	return dir
}

// stubSnapperConfdPath points the snapperConfdPath seam at a file inside a temp
// dir for the test's duration, the sibling of stubSnapperConfigsDir for the
// registration half (R1.1). The returned path does not exist yet, so a caller
// can assert that registration creates it (R1.3) or seed it with content first.
func stubSnapperConfdPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapper")
	orig := snapperConfdPath
	t.Cleanup(func() { snapperConfdPath = orig })
	snapperConfdPath = path
	return path
}

// TestRenderSnapperConfig_ManagedKeys: rendering from scratch emits every
// managed key — the subvolume, timeline create/cleanup switches, the
// TIMELINE_LIMIT_* counts mapped from [engine.retention] (R2.1, R1.4), and
// NUMBER_CLEANUP for the hook's pre/post pairs.
func TestRenderSnapperConfig_ManagedKeys(t *testing.T) {
	cfg := EngineConfig{
		Driver:    "snapper",
		Retention: Retention{Hourly: 24, Daily: 7, Weekly: 4, Monthly: 6},
	}
	got := string(renderSnapperConfig(cfg, "/home", nil))

	for _, want := range []string{
		`SUBVOLUME="/home"`,
		`TIMELINE_CREATE="no"`,
		`TIMELINE_CLEANUP="yes"`,
		`TIMELINE_LIMIT_HOURLY="24"`,
		`TIMELINE_LIMIT_DAILY="7"`,
		`TIMELINE_LIMIT_WEEKLY="4"`,
		`TIMELINE_LIMIT_MONTHLY="6"`,
		`TIMELINE_LIMIT_YEARLY="0"`,
		`NUMBER_CLEANUP="yes"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered config missing %s\n--- got ---\n%s", want, got)
		}
	}
}

// TestRenderSnapperConfig_PreservesUnmanagedKeys: re-rendering over an existing
// config updates the managed keys in place and preserves everything else —
// user settings and comments survive, and no key is duplicated (R2.2).
func TestRenderSnapperConfig_PreservesUnmanagedKeys(t *testing.T) {
	existing := []byte(`# keep me
ALLOW_USERS="alice"
SUBVOLUME="/old"
TIMELINE_CLEANUP="no"
`)
	cfg := EngineConfig{Driver: "snapper", Retention: Retention{Daily: 7}}
	got := string(renderSnapperConfig(cfg, "/home", existing))

	if !strings.Contains(got, "# keep me") {
		t.Errorf("comment line was clobbered:\n%s", got)
	}
	if !strings.Contains(got, `ALLOW_USERS="alice"`) {
		t.Errorf("unmanaged ALLOW_USERS was clobbered:\n%s", got)
	}
	if !strings.Contains(got, `SUBVOLUME="/home"`) || strings.Contains(got, `SUBVOLUME="/old"`) {
		t.Errorf("managed SUBVOLUME not updated in place:\n%s", got)
	}
	if !strings.Contains(got, `TIMELINE_CLEANUP="yes"`) || strings.Contains(got, `TIMELINE_CLEANUP="no"`) {
		t.Errorf("managed TIMELINE_CLEANUP not updated in place:\n%s", got)
	}
	if strings.Count(got, "SUBVOLUME=") != 1 {
		t.Errorf("SUBVOLUME duplicated:\n%s", got)
	}
	if !strings.Contains(got, `TIMELINE_LIMIT_DAILY="7"`) {
		t.Errorf("missing managed key appended:\n%s", got)
	}
}

// TestEnsureSnapperConfigs_WritesPerSubvolume: ensure writes one config per
// managed subvolume under snapperConfigsDir (R2.1) and is idempotent — a
// second run changes nothing and user keys survive re-ensure (R2.2).
func TestEnsureSnapperConfigs_WritesPerSubvolume(t *testing.T) {
	dir := stubSnapperConfigsDir(t)
	stubSnapperConfdPath(t)
	cfg := &Config{Engine: EngineConfig{
		Driver:     "snapper",
		Subvolumes: []string{"/", "/home"},
		Retention:  Retention{Daily: 7},
	}}

	if err := ensureSnapperConfigs(cfg); err != nil {
		t.Fatalf("ensureSnapperConfigs: %v", err)
	}
	rootCfg, err := os.ReadFile(filepath.Join(dir, "root"))
	if err != nil {
		t.Fatalf("root config not written: %v", err)
	}
	if !strings.Contains(string(rootCfg), `SUBVOLUME="/"`) {
		t.Errorf("root config SUBVOLUME wrong:\n%s", rootCfg)
	}
	if _, err := os.Stat(filepath.Join(dir, "home")); err != nil {
		t.Fatalf("home config not written: %v", err)
	}

	// Simulate a user edit, then re-ensure: the edit survives (R2.2).
	homePath := filepath.Join(dir, "home")
	user := append([]byte(`ALLOW_USERS="bob"`+"\n"), rootCfg...)
	if err := os.WriteFile(homePath, user, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := ensureSnapperConfigs(cfg); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	after, _ := os.ReadFile(homePath)
	if !strings.Contains(string(after), `ALLOW_USERS="bob"`) {
		t.Errorf("re-ensure clobbered user key:\n%s", after)
	}
}

// TestWriteEngineConfig_DispatchesByDriver: the engine-config writer is
// driver-aware — btrbk renders btrbk.conf next to snapshot.toml, snapper
// ensures /etc/snapper/configs/<name> and writes NO btrbk.conf (R2.1, R6.2).
func TestWriteEngineConfig_DispatchesByDriver(t *testing.T) {
	t.Run("btrbk", func(t *testing.T) {
		stubSnapperConfigsDir(t)
		stubSnapperConfdPath(t)
		dir := t.TempDir()
		confPath := filepath.Join(dir, "snapshot.toml")
		cfg := &Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}}}
		if err := WriteEngineConfig(cfg, confPath); err != nil {
			t.Fatalf("WriteEngineConfig(btrbk): %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "btrbk.conf")); err != nil {
			t.Errorf("btrbk.conf not written: %v", err)
		}
	})

	t.Run("snapper", func(t *testing.T) {
		snapDir := stubSnapperConfigsDir(t)
		stubSnapperConfdPath(t)
		dir := t.TempDir()
		confPath := filepath.Join(dir, "snapshot.toml")
		cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}
		if err := WriteEngineConfig(cfg, confPath); err != nil {
			t.Fatalf("WriteEngineConfig(snapper): %v", err)
		}
		if _, err := os.Stat(filepath.Join(snapDir, "home")); err != nil {
			t.Errorf("snapper config not ensured: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "btrbk.conf")); !os.IsNotExist(err) {
			t.Errorf("snapper driver must not write btrbk.conf (err=%v)", err)
		}
	})
}

// TestApply_SnapperEnsuresConfigs: `apply` with the snapper driver ensures the
// snapper configs (R2.2) and, with no schedule configured, runs no subprocess.
func TestApply_SnapperEnsuresConfigs(t *testing.T) {
	snapDir := stubSnapperConfigsDir(t)
	stubSnapperConfdPath(t)
	dir := t.TempDir()
	confPath := filepath.Join(dir, "snapshot.toml")
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/"}}}
	mock := &MockRunner{}

	if err := Apply(context.Background(), cfg, confPath, mock); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapDir, "root")); err != nil {
		t.Errorf("apply did not ensure snapper config: %v", err)
	}
	if len(mock.Calls) != 0 {
		t.Errorf("apply with no schedule ran %d subprocess(es), want 0: %+v", len(mock.Calls), mock.Calls)
	}
}

// TestValidate_SnapperDriver: Config.Validate accepts engine.driver = "snapper"
// when the binary is on PATH (R1.1) and fails with the actionable
// ErrDriverUnavailable naming the Portage package when it is absent (R5.1).
func TestValidate_SnapperDriver(t *testing.T) {
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/"}}}

	stubLookPath(t, "snapper")
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate with snapper on PATH: %v, want nil", err)
	}

	stubLookPath(t) // nothing present
	err := cfg.Validate()
	if !errors.Is(err, ErrDriverUnavailable) {
		t.Fatalf("Validate without snapper = %v, want ErrDriverUnavailable", err)
	}
	if !strings.Contains(err.Error(), "app-backup/snapper") {
		t.Errorf("error %q does not name app-backup/snapper", err)
	}
}

// TestApply_DoesNotInstallEmergeHook is the R4.3 guard: `apply` with the
// snapper driver materializes engine configs only — nothing is ever written
// under EmergeHookRoot's /etc/portage. Hook installation happens exclusively
// through the explicit `snapshot hook --install` command.
func TestApply_DoesNotInstallEmergeHook(t *testing.T) {
	stubSnapperConfigsDir(t)
	stubSnapperConfdPath(t)
	hookRoot := t.TempDir()
	origRoot := EmergeHookRoot
	t.Cleanup(func() { EmergeHookRoot = origRoot })
	EmergeHookRoot = hookRoot

	dir := t.TempDir()
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/"}}}
	if err := Apply(context.Background(), cfg, filepath.Join(dir, "snapshot.toml"), &MockRunner{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := os.Stat(filepath.Join(hookRoot, "etc", "portage")); !os.IsNotExist(err) {
		t.Errorf("apply wrote under %s/etc/portage; the emerge hook must be opt-in only (err=%v)", hookRoot, err)
	}
}
