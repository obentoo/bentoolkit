package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBtrbkConfPath(t *testing.T) {
	if got := BtrbkConfPath("/etc/bentoo/snapshot.toml"); got != "/etc/bentoo/btrbk.conf" {
		t.Errorf("BtrbkConfPath = %q", got)
	}
	if got := BtrbkConfPath(""); got != DefaultBtrbkConfPath {
		t.Errorf("BtrbkConfPath(\"\") = %q, want default", got)
	}
}

func TestApply_RendersConfAndInstallsTimer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "snapshot.toml")
	mock := &MockRunner{}

	// Redirect unit writes away from the real /etc/systemd/system.
	origUnitDir := systemdUnitDir
	t.Cleanup(func() { systemdUnitDir = origUnitDir })
	systemdUnitDir = filepath.Join(dir, "systemd")

	cfg := &Config{
		Engine:   EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}, SnapshotDir: "/.snapshots"},
		Ship:     []ShipConfig{{Type: "ssh", Target: "u@h:/p"}},
		Schedule: ScheduleConfig{Backend: "systemd", OnCalendar: "daily"},
	}

	if err := Apply(context.Background(), cfg, configPath, mock); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// btrbk.conf rendered next to snapshot.toml, with the ssh target block.
	confData, err := os.ReadFile(filepath.Join(dir, "btrbk.conf"))
	if err != nil {
		t.Fatalf("btrbk.conf not written: %v", err)
	}
	if !strings.Contains(string(confData), "target ssh://u@h:/p") {
		t.Errorf("btrbk.conf missing ssh target:\n%s", confData)
	}

	// systemctl daemon-reload + enable were invoked (units installed under the
	// default unit dir — Apply uses the scheduler's defaults; we only assert the
	// systemctl seam fired in order).
	if len(mock.Calls) < 2 || mock.Calls[0].Args[0] != "daemon-reload" {
		t.Errorf("expected daemon-reload first, got %+v", mock.Calls)
	}
}

func TestApply_NoScheduleSkipsSystemctl(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "snapshot.toml")
	mock := &MockRunner{}
	cfg := &Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}}}

	if err := Apply(context.Background(), cfg, configPath, mock); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(mock.Calls) != 0 {
		t.Errorf("no schedule should mean no systemctl calls, got %+v", mock.Calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "btrbk.conf")); err != nil {
		t.Errorf("btrbk.conf still expected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Story 016 C3 — `apply` provisions <subvolume>/.snapshots (R2.1, R5.1).
//
// These are the integration tests for the wiring: ensureSnapshotSubvolumes is
// unit-tested in snapper_config_test.go, so what is asserted here is that Apply
// reaches it for the snapper driver, never for btrbk, and reaches it above the
// no-schedule early return.
//
// Every test here goes through stubSnapperApplySeams. A snapper Apply writes
// four real system locations otherwise, and this package has already been bitten
// by exactly that: /etc/conf.d/snapper, /home/.snapshots and /.snapshots all
// exist on a developer's btrfs machine, so an unstubbed run either inverts an
// assertion (a present /.snapshots turns "missing" into "already provisioned")
// or, on a root CI runner, mutates the live system.
// ---------------------------------------------------------------------------

// stubSnapperApplySeams redirects every filesystem seam a snapper Apply touches
// and returns the temp snapper configs dir and the temp /etc/conf.d/snapper
// path. existing lists the paths statPath must report as present; everything
// else reports os.ErrNotExist.
//
// It composes the four established stubs rather than reimplementing them, and
// exists because all four are mandatory together: forgetting any single one
// reintroduces a write to the developer's real /etc or /. Callers that need to
// change what exists mid-test (the idempotency case) simply call stubStatPath
// again afterwards.
func stubSnapperApplySeams(t *testing.T, existing ...string) (snapDir, confdPath string) {
	t.Helper()
	snapDir = stubSnapperConfigsDir(t)
	confdPath = stubSnapperConfdPath(t)
	stubStatPath(t, existing...)
	stubChmodPath(t, nil)
	return snapDir, confdPath
}

// btrfsCalls returns only the `btrfs` invocations a mock captured, so an
// assertion about provisioning is not perturbed by the systemctl calls a
// scheduled config also makes.
func btrfsCalls(mock *MockRunner) []RunnerCall {
	var out []RunnerCall
	for _, c := range mock.Calls {
		if c.Name == "btrfs" {
			out = append(out, c)
		}
	}
	return out
}

// TestApply_SnapperCreatesMissingSnapshotSubvolume is the integration-level
// regression test for the bug: with the snapper driver and no .snapshots on the
// subvolume, `apply` creates it — exactly one `btrfs subvolume create
// <subvolume>/.snapshots` (016 R2.1). Before this wiring `apply` wrote the
// snapper config and stopped, so the first `run` failed on the unprepared
// subvolume.
func TestApply_SnapperCreatesMissingSnapshotSubvolume(t *testing.T) {
	sv := filepath.Join(t.TempDir(), "home")
	stubSnapperApplySeams(t) // nothing exists yet
	dir := t.TempDir()
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{sv}}}
	mock := &MockRunner{}

	if err := Apply(context.Background(), cfg, filepath.Join(dir, "snapshot.toml"), mock); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	calls := btrfsCalls(mock)
	if len(calls) != 1 {
		t.Fatalf("len(btrfs calls) = %d, want 1: %+v", len(calls), mock.Calls)
	}
	wantArgs := []string{"subvolume", "create", filepath.Join(sv, ".snapshots")}
	if !equalStrings(calls[0].Args, wantArgs) {
		t.Errorf("args = %v, want %v", calls[0].Args, wantArgs)
	}
}

// TestApply_SnapperSecondRunCreatesNothing: applying twice over an unchanged
// config succeeds both times and converges (016 R5.1). The second pass runs with
// the .snapshots path now present — what the first pass just created — and must
// issue no btrfs call at all, since `btrfs subvolume create` over an existing
// subvolume fails. The registration file is compared byte for byte across the
// two passes as the other half of "identical on-disk state": a name appended
// twice would show up here as a changed SNAPPER_CONFIGS line.
func TestApply_SnapperSecondRunCreatesNothing(t *testing.T) {
	sv := filepath.Join(t.TempDir(), "home")
	snapshots := filepath.Join(sv, ".snapshots")
	_, confd := stubSnapperApplySeams(t) // first pass: nothing exists
	dir := t.TempDir()
	confPath := filepath.Join(dir, "snapshot.toml")
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{sv}}}

	first := &MockRunner{}
	if err := Apply(context.Background(), cfg, confPath, first); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if len(btrfsCalls(first)) != 1 {
		t.Fatalf("first Apply btrfs calls = %+v, want exactly 1", btrfsCalls(first))
	}
	afterFirst, err := os.ReadFile(confd)
	if err != nil {
		t.Fatalf("read registration after first Apply: %v", err)
	}
	// Guard the idempotency assertions themselves: an Apply that registered
	// nothing would satisfy "identical twice" without ever fixing the bug.
	if name := snapperConfigName(sv); !strings.Contains(string(afterFirst), name) {
		t.Fatalf("first Apply did not register %q:\n%s", name, afterFirst)
	}

	// Second pass: the subvolume the first pass created now exists.
	stubStatPath(t, snapshots)
	second := &MockRunner{}
	if err := Apply(context.Background(), cfg, confPath, second); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if calls := btrfsCalls(second); len(calls) != 0 {
		t.Errorf("second Apply re-created an existing .snapshots: %+v", calls)
	}
	afterSecond, err := os.ReadFile(confd)
	if err != nil {
		t.Fatalf("read registration after second Apply: %v", err)
	}
	if string(afterSecond) != string(afterFirst) {
		t.Errorf("second Apply changed the registration\n--- first ---\n%q\n--- second ---\n%q",
			afterFirst, afterSecond)
	}
}

// TestApply_SnapperProvisionsWithoutSchedule is the ordering guard, and the
// entire reason the provisioning call sits where it does. Apply returns early
// when no schedule is configured; placing the .snapshots step below that return
// would compile, pass every other test here, and silently skip provisioning for
// exactly the schedule-less configs this story set out to fix — the bug would
// survive the fix. A config with Schedule.Backend == "" must still be
// provisioned (016 R2.1), while still running no systemctl at all (R4.1).
func TestApply_SnapperProvisionsWithoutSchedule(t *testing.T) {
	sv := filepath.Join(t.TempDir(), "home")
	stubSnapperApplySeams(t) // nothing exists yet
	dir := t.TempDir()
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{sv}}}
	if cfg.Schedule.Backend != "" {
		t.Fatal("precondition: this test is meaningless unless no schedule is configured")
	}
	mock := &MockRunner{}

	if err := Apply(context.Background(), cfg, filepath.Join(dir, "snapshot.toml"), mock); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	calls := btrfsCalls(mock)
	if len(calls) != 1 {
		t.Fatalf("a schedule-less config must still be provisioned; btrfs calls = %+v (all calls: %+v)",
			calls, mock.Calls)
	}
	wantArgs := []string{"subvolume", "create", filepath.Join(sv, ".snapshots")}
	if !equalStrings(calls[0].Args, wantArgs) {
		t.Errorf("args = %v, want %v", calls[0].Args, wantArgs)
	}
	for _, c := range mock.Calls {
		if c.Name == "systemctl" {
			t.Errorf("no schedule must mean no systemctl, got %+v", c)
		}
	}
}

// TestApply_SnapperRootSubvolumeSkipsCreateAndStaysRegistered covers the real
// "/" case: the operator mounted @snapshots at /.snapshots themselves, so the
// subvolume already exists. `apply` must leave it completely alone — no
// re-create, which would fail — while still registering the root config in
// SNAPPER_CONFIGS (016 R1.1, R2.2). Registration is asserted from the file
// because "no btrfs call" alone would also be satisfied by an apply that did
// nothing whatsoever.
//
// This is the one test using the literal "/" as a subvolume. That is safe ONLY
// because statPath and chmodPath are stubbed: with real seams it would stat the
// host's own /.snapshots and could chmod it. Do not relax the stubbing here.
func TestApply_SnapperRootSubvolumeSkipsCreateAndStaysRegistered(t *testing.T) {
	_, confd := stubSnapperApplySeams(t, "/.snapshots") // hand-mounted by the operator
	dir := t.TempDir()
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/"}}}
	mock := &MockRunner{}

	if err := Apply(context.Background(), cfg, filepath.Join(dir, "snapshot.toml"), mock); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if calls := btrfsCalls(mock); len(calls) != 0 {
		t.Errorf("an existing /.snapshots must not be re-created: %+v", calls)
	}
	got, err := os.ReadFile(confd)
	if err != nil {
		t.Fatalf("apply registered nothing: %v", err)
	}
	want := `SNAPPER_CONFIGS="root"` + "\n"
	if string(got) != want {
		t.Errorf("registration\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// TestApply_BtrbkSkipsSnapshotProvisioning is the Unchanged Behavior guard: the
// .snapshots provisioning is snapper-only, so a btrbk apply must still render
// btrbk.conf and must not invoke btrfs — btrbk manages its own snapshot
// directory and has no per-subvolume .snapshots concept.
//
// statPath is deliberately stubbed to "nothing exists", which is what makes the
// test meaningful: it is the condition under which provisioning WOULD fire, so
// an implementation that dropped the driver gate fails here instead of passing
// by luck.
func TestApply_BtrbkSkipsSnapshotProvisioning(t *testing.T) {
	sv := filepath.Join(t.TempDir(), "home")
	stubSnapperApplySeams(t) // nothing exists: provisioning would fire if ungated
	dir := t.TempDir()
	cfg := &Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{sv}, SnapshotDir: "/.snapshots"}}
	mock := &MockRunner{}

	if err := Apply(context.Background(), cfg, filepath.Join(dir, "snapshot.toml"), mock); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if calls := btrfsCalls(mock); len(calls) != 0 {
		t.Errorf("the btrbk driver must not provision .snapshots: %+v", calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "btrbk.conf")); err != nil {
		t.Errorf("btrbk.conf still expected: %v", err)
	}
}

func TestManagerList_PerSubvolume(t *testing.T) {
	sample := "/.snapshots/home.20260608T120000\n/.snapshots/home.20260607T120000\n"
	mock := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return []byte(sample), nil
		},
	}
	cfg := Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}}}
	m, err := NewManager(cfg, "/etc/bentoo/snapshot.toml", mock)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	got, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got["/home"]) != 2 {
		t.Errorf("List[/home] = %+v, want 2 snapshots", got["/home"])
	}
}

func TestTimerState(t *testing.T) {
	enabled := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return []byte("enabled\n"), nil
		},
	}
	if got := TimerState(context.Background(), enabled); got != "enabled" {
		t.Errorf("TimerState = %q, want enabled", got)
	}

	empty := &MockRunner{}
	if got := TimerState(context.Background(), empty); got != "unknown" {
		t.Errorf("TimerState empty = %q, want unknown", got)
	}
}
