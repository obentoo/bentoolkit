package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

// TestSnapperListEngine_RequestsJSONOut: List asks snapper for JSON and turns
// the payload into []Snapshot (R1.3, 016 R3.1).
//
// Unlike its TestSnapperEngine_* siblings this name embeds "SnapperList", so
// `go test -run SnapperList` reaches it — "SnapperEngine_List" does not match
// that regex, which would leave the assertions below outside the gate that is
// supposed to guard them.
//
// Two things are pinned. First the request: --jsonout must lead the argument
// list. Drop it and snapper falls back to its human-readable table, whose
// U+2502 column separators parse to nothing — `snapshot list` prints "(none)"
// against a host full of snapshots, with no error raised anywhere to betray it.
// That is Bug 3 verbatim, so the flag is asserted on its own before the
// whole-argv comparison, to name the cause when it regresses.
//
// Second the round trip: the captured payload goes in through the Runner seam
// and real Snapshots come out. The TestSnapperListParse_* tests below call the
// parser directly, so they would all keep passing if List asked for the wrong
// format or called the wrong parser — only this test covers the wiring.
//
// MockRunner keeps it hermetic: no snapper subprocess runs, so the result cannot
// drift with the developer's own snapshots and holds on a host without snapper.
func TestSnapperListEngine_RequestsJSONOut(t *testing.T) {
	golden := snapperListGolden(t)
	mock := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return golden, nil
		},
	}
	e := newSnapperEngine(EngineConfig{Driver: "snapper"}, mock)

	snaps, err := e.List(context.Background(), "/home")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// The request (016 R3.1).
	if len(mock.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want 1", len(mock.Calls))
	}
	call := mock.Calls[0]
	if !slices.Contains(call.Args, "--jsonout") {
		t.Errorf("args %v carry no --jsonout: snapper would emit its U+2502 table and the listing would come back empty (R3.1)", call.Args)
	}
	wantArgs := []string{"--jsonout", "-c", "home", "list"}
	if call.Name != "snapper" || !equalStrings(call.Args, wantArgs) {
		t.Errorf("call = %s %v, want snapper %v", call.Name, call.Args, wantArgs)
	}

	// The parse, end to end through List (016 R3.2, R3.3).
	wantIDs := []string{"1", "16", "2303", "2304"}
	if len(snaps) != len(wantIDs) {
		t.Fatalf("got %d snapshots, want %d: %+v", len(snaps), len(wantIDs), snaps)
	}
	for i, want := range wantIDs {
		if snaps[i].ID != want {
			t.Errorf("snaps[%d].ID = %q, want %q", i, snaps[i].ID, want)
		}
		if snaps[i].ID == "0" {
			t.Errorf("snaps[%d] is the \"current\" pseudo-snapshot; it must be skipped (R3.3)", i)
		}
		if snaps[i].Subvolume != "/home" {
			t.Errorf("snaps[%d].Subvolume = %q, want /home", i, snaps[i].Subvolume)
		}
	}
	if want := "/home/.snapshots/1/snapshot"; snaps[0].Path != want {
		t.Errorf("snaps[0].Path = %q, want %q", snaps[0].Path, want)
	}
	if snaps[0].CreatedAt.IsZero() {
		t.Error("snaps[0].CreatedAt is the zero time; the golden's date field must survive the trip through List (R3.2)")
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
// Story 018 — provisioning through snapper's own API (R1-R4).
//
// bentoo no longer writes /etc/snapper/configs or /etc/conf.d/snapper: it calls
// create-config and set-config. So these tests assert on the COMMANDS issued,
// not on file contents — which is also what makes them meaningful, since a file
// written behind a running snapperd is precisely what does not work.
// ---------------------------------------------------------------------------

// stubSnapshotsDirSeams decides what the filesystem looks like to the leftover
// .snapshots check, so no test inherits the developer's /home/.snapshots. The
// paths listed are the ones that exist; entries names what a listed directory
// contains, which is how "empty leftover" is told from "holds snapshots".
func stubSnapshotsDirSeams(t *testing.T, entries []string, existing ...string) *[]string {
	t.Helper()
	origStat, origRead, origRemove := statPath, readDirPath, removePath
	t.Cleanup(func() { statPath, readDirPath, removePath = origStat, origRead, origRemove })
	// No mount table by default, for the same reason the others are stubbed: the
	// developer's real /proc/self/mounts would decide the outcome of the
	// mount-point guard instead of the test.
	stubMountTables(t, nil)

	present := make(map[string]bool, len(existing))
	for _, p := range existing {
		present[p] = true
	}
	statPath = func(name string) (os.FileInfo, error) {
		if present[name] {
			return nil, nil //nolint:nilnil // only the error is consulted
		}
		return nil, os.ErrNotExist
	}
	readDirPath = func(string) ([]os.DirEntry, error) {
		out := make([]os.DirEntry, len(entries))
		return out, nil
	}
	var removed []string
	removePath = func(name string) error {
		removed = append(removed, name)
		return nil
	}
	return &removed
}

// stubMountTables makes the mount tables read as the content given per path, so
// a test states what the host declares rather than inheriting it. A path absent
// from the map does not exist, which is how the guard sees a host with neither
// file — and keying by path is what lets a test say "fstab declares it but it is
// not mounted", the case the guard exists for.
func stubMountTables(t *testing.T, tables map[string]string) {
	t.Helper()
	orig := readFilePath
	t.Cleanup(func() { readFilePath = orig })
	readFilePath = func(name string) ([]byte, error) {
		content, ok := tables[name]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(content), nil
	}
}

// snapperMock returns a Runner answering `list-configs` with the coverage map
// given (config name keyed by subvolume), so a test states up front what snapper
// already knows. Every other command succeeds silently.
func snapperMock(t *testing.T, covered map[string]string, failing ...string) *MockRunner {
	t.Helper()
	type entry struct {
		Config    string `json:"config"`
		Subvolume string `json:"subvolume"`
	}
	payload := struct {
		Configs []entry `json:"configs"`
	}{}
	for sv, name := range covered {
		payload.Configs = append(payload.Configs, entry{Config: name, Subvolume: sv})
	}
	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal list-configs payload: %v", err)
	}
	fail := make(map[string]bool, len(failing))
	for _, f := range failing {
		fail[f] = true
	}
	return &MockRunner{
		RunFunc: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
			if name == "snapper" && len(args) > 1 && args[1] == "list-configs" {
				return out, nil
			}
			if fail[name] {
				return nil, errors.New("simulated failure")
			}
			return nil, nil
		},
	}
}

// snapperArgs returns the argument lists of the calls to cmd, so an assertion
// can name the exact invocation it expects.
func snapperArgs(mock *MockRunner, cmd string) [][]string {
	var out [][]string
	for _, c := range mock.Calls {
		if c.Name == cmd {
			out = append(out, c.Args)
		}
	}
	return out
}

// TestEnsureSnapperConfigs_ProvisionsUncoveredSubvolume: a subvolume snapper
// does not cover is provisioned with create-config, and only then receives the
// managed keys (018 R1). The ordering matters — set-config against a config that
// does not exist yet fails.
func TestEnsureSnapperConfigs_ProvisionsUncoveredSubvolume(t *testing.T) {
	stubSnapshotsDirSeams(t, nil) // nothing on disk: no leftover .snapshots
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}
	mock := snapperMock(t, nil) // snapper covers nothing

	if err := ensureSnapperConfigs(context.Background(), cfg, mock); err != nil {
		t.Fatalf("ensureSnapperConfigs: %v", err)
	}

	calls := snapperArgs(mock, "snapper")
	if len(calls) != 3 {
		t.Fatalf("snapper calls = %d, want 3 (list-configs, create-config, set-config): %+v", len(calls), calls)
	}
	wantCreate := []string{"-c", "home", "create-config", "/home"}
	if !equalStrings(calls[1], wantCreate) {
		t.Errorf("create-config args = %v, want %v", calls[1], wantCreate)
	}
	if len(calls[2]) < 3 || calls[2][2] != "set-config" {
		t.Errorf("third call is not set-config: %v", calls[2])
	}
}

// TestEnsureSnapperConfigs_CoveredSubvolumeSkipsCreate: when snapper already
// covers the subvolume, create-config must NOT run — it fails with "subvolume
// already covered" (018 R2) — but the managed keys are still applied, because
// the old file merge never reached a running daemon and the config may still
// carry the template's retention.
func TestEnsureSnapperConfigs_CoveredSubvolumeSkipsCreate(t *testing.T) {
	stubSnapshotsDirSeams(t, nil, "/home/.snapshots") // provisioned already
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}
	mock := snapperMock(t, map[string]string{"/home": "home"})

	if err := ensureSnapperConfigs(context.Background(), cfg, mock); err != nil {
		t.Fatalf("ensureSnapperConfigs: %v", err)
	}

	for _, args := range snapperArgs(mock, "snapper") {
		for _, a := range args {
			if a == "create-config" {
				t.Fatalf("create-config ran against an already-covered subvolume: %v", args)
			}
		}
	}
	var sawSet bool
	for _, args := range snapperArgs(mock, "snapper") {
		if len(args) > 2 && args[2] == "set-config" {
			sawSet = true
		}
	}
	if !sawSet {
		t.Error("managed keys were not applied to the pre-existing config")
	}
}

// TestEnsureSnapperConfigs_SetConfigCarriesRetention: the retention from
// snapshot.toml reaches snapper as set-config pairs (018 R3), and SUBVOLUME is
// NOT among them — snapper rejects a set-config that tries to change it, which
// would fail the whole call and leave every other key unapplied.
func TestEnsureSnapperConfigs_SetConfigCarriesRetention(t *testing.T) {
	stubSnapshotsDirSeams(t, nil, "/home/.snapshots")
	cfg := &Config{Engine: EngineConfig{
		Driver:     "snapper",
		Subvolumes: []string{"/home"},
		Retention:  Retention{Hourly: 24, Daily: 7, Weekly: 4, Monthly: 6},
	}}
	mock := snapperMock(t, map[string]string{"/home": "home"})

	if err := ensureSnapperConfigs(context.Background(), cfg, mock); err != nil {
		t.Fatalf("ensureSnapperConfigs: %v", err)
	}

	var set []string
	for _, args := range snapperArgs(mock, "snapper") {
		if len(args) > 2 && args[2] == "set-config" {
			set = args
		}
	}
	joined := strings.Join(set, " ")
	for _, want := range []string{
		"TIMELINE_CREATE=no",
		"TIMELINE_CLEANUP=yes",
		"TIMELINE_LIMIT_HOURLY=24",
		"TIMELINE_LIMIT_DAILY=7",
		"TIMELINE_LIMIT_WEEKLY=4",
		"TIMELINE_LIMIT_MONTHLY=6",
		"TIMELINE_LIMIT_YEARLY=0",
		"NUMBER_CLEANUP=yes",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("set-config missing %s: %v", want, set)
		}
	}
	if strings.Contains(joined, "SUBVOLUME=") {
		t.Errorf("set-config carries SUBVOLUME, which snapper refuses to change: %v", set)
	}
}

// TestProvision_RemovesEmptyLeftoverSnapshotsDir: create-config refuses to run
// when .snapshots already exists, so an EMPTY leftover is removed first and the
// creation proceeds (018 R4).
func TestProvision_RemovesEmptyLeftoverSnapshotsDir(t *testing.T) {
	stubSnapshotsDirSeams(t, nil, "/home/.snapshots") // exists, and is empty
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}
	mock := snapperMock(t, nil) // not covered: provisioning must happen

	if err := ensureSnapperConfigs(context.Background(), cfg, mock); err != nil {
		t.Fatalf("ensureSnapperConfigs: %v", err)
	}

	btrfs := snapperArgs(mock, "btrfs")
	want := []string{"subvolume", "delete", "/home/.snapshots"}
	if len(btrfs) != 1 || !equalStrings(btrfs[0], want) {
		t.Fatalf("btrfs calls = %v, want exactly one %v", btrfs, want)
	}
	var sawCreate bool
	for _, args := range snapperArgs(mock, "snapper") {
		if len(args) > 2 && args[2] == "create-config" {
			sawCreate = true
		}
	}
	if !sawCreate {
		t.Error("create-config did not run after the empty leftover was cleared")
	}
}

// TestProvision_RefusesToDeleteSnapshotsHoldingEntries is the safety guard: a
// .snapshots with entries holds snapshots, and removing it to fix a
// configuration problem would destroy backups. The pass aborts, nothing is
// deleted, no config is created, and the error names the command the operator
// can run instead (018 R4).
func TestProvision_RefusesToDeleteSnapshotsHoldingEntries(t *testing.T) {
	removed := stubSnapshotsDirSeams(t, []string{"1", "2"}, "/home/.snapshots")
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}
	mock := snapperMock(t, nil)

	err := ensureSnapperConfigs(context.Background(), cfg, mock)
	if err == nil {
		t.Fatal("ensureSnapperConfigs succeeded over a .snapshots holding snapshots")
	}
	if !strings.Contains(err.Error(), "create-config") {
		t.Errorf("error does not tell the operator what to run: %v", err)
	}
	if len(*removed) != 0 {
		t.Errorf("a non-empty .snapshots was removed: %v", *removed)
	}
	if btrfs := snapperArgs(mock, "btrfs"); len(btrfs) != 0 {
		t.Errorf("btrfs was invoked against a non-empty .snapshots: %v", btrfs)
	}
	for _, args := range snapperArgs(mock, "snapper") {
		if len(args) > 2 && args[2] == "create-config" {
			t.Errorf("create-config ran despite the aborted provisioning: %v", args)
		}
	}
}

// TestProvision_FallsBackToUnlinkWhenBtrfsDeleteFails: an empty leftover that is
// a plain directory rather than a subvolume — `btrfs subvolume delete` fails on
// it — is still cleared, so provisioning is not blocked by how the directory
// happens to have been made (018 R4).
func TestProvision_FallsBackToUnlinkWhenBtrfsDeleteFails(t *testing.T) {
	removed := stubSnapshotsDirSeams(t, nil, "/home/.snapshots")
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}
	mock := snapperMock(t, nil, "btrfs") // btrfs refuses

	if err := ensureSnapperConfigs(context.Background(), cfg, mock); err != nil {
		t.Fatalf("ensureSnapperConfigs: %v", err)
	}
	if len(*removed) != 1 || (*removed)[0] != "/home/.snapshots" {
		t.Errorf("unlink fallback did not run: %v", *removed)
	}
}

// TestProvision_RefusesToRemoveDeclaredMountpoint: the leftover .snapshots is a
// separately mounted subvolume that is currently UNMOUNTED — the classic
// @snapshots in fstab. ReadDir sees an empty directory and the removal path
// would clear it, destroying no snapshot but breaking the mount point, so the
// next boot cannot mount it. The guard refuses before touching anything and
// names the file that declares it (018 R6).
func TestProvision_RefusesToRemoveDeclaredMountpoint(t *testing.T) {
	removed := stubSnapshotsDirSeams(t, nil, "/home/.snapshots") // exists, reads as empty
	stubMountTables(t, map[string]string{
		// Declared, and deliberately absent from the live table: unmounted is the
		// case ReadDir cannot tell apart from "leftover empty directory".
		"/etc/fstab": "# comment\n" +
			"UUID=abc /home/.snapshots btrfs subvol=@snapshots,noatime 0 0\n",
	})
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}
	mock := snapperMock(t, nil) // not covered: provisioning would otherwise run

	err := ensureSnapperConfigs(context.Background(), cfg, mock)
	if err == nil {
		t.Fatal("ensureSnapperConfigs cleared a declared mount point")
	}
	if !strings.Contains(err.Error(), "/etc/fstab") {
		t.Errorf("error does not name the table that declares the mount: %v", err)
	}
	if len(*removed) != 0 {
		t.Errorf("a declared mount point was unlinked: %v", *removed)
	}
	if btrfs := snapperArgs(mock, "btrfs"); len(btrfs) != 0 {
		t.Errorf("btrfs was invoked against a declared mount point: %v", btrfs)
	}
	for _, args := range snapperArgs(mock, "snapper") {
		if len(args) > 2 && args[2] == "create-config" {
			t.Errorf("create-config ran despite the aborted provisioning: %v", args)
		}
	}
}

// TestEnsureSnapperConfigs_WarnsWhenConfigNameDiverges: snapper covers the
// subvolume under a name bentoo would not have chosen — an operator-made config.
// create-config must not run (snapper would refuse: already covered) and the
// managed keys go to the name snapper actually holds, not the derived one, or
// set-config would fail against a config that does not exist. The divergence is
// warned about because `snapshot create`/`list` derive the name from the
// subvolume and will not find this config (018 R7).
func TestEnsureSnapperConfigs_WarnsWhenConfigNameDiverges(t *testing.T) {
	stubSnapshotsDirSeams(t, nil, "/home/.snapshots")
	var warnings []string
	origWarn := warnLogf
	t.Cleanup(func() { warnLogf = origWarn })
	warnLogf = func(format string, args ...interface{}) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}

	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}
	mock := snapperMock(t, map[string]string{"/home": "operator-home"})

	if err := ensureSnapperConfigs(context.Background(), cfg, mock); err != nil {
		t.Fatalf("ensureSnapperConfigs: %v", err)
	}

	var set []string
	for _, args := range snapperArgs(mock, "snapper") {
		if len(args) > 2 && args[2] == "create-config" {
			t.Errorf("create-config ran against an already-covered subvolume: %v", args)
		}
		if len(args) > 2 && args[2] == "set-config" {
			set = args
		}
	}
	if len(set) < 2 || set[1] != "operator-home" {
		t.Errorf("set-config went to %v, not to the config snapper actually holds", set)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "operator-home") {
		t.Errorf("the name divergence was not reported to the operator: %v", warnings)
	}
}

// TestEnsureSnapperConfigs_NilRunnerIsNormalized: a nil Runner means "use the
// production one" throughout this package, and the cmd layer depends on it —
// snapshotRunner is only ever assigned by tests, so every real apply arrives
// with nil. A consumer that dereferences it directly panics on every host; that
// shipped once already. execCommand is stubbed so the production path runs
// without spawning snapper.
func TestEnsureSnapperConfigs_NilRunnerIsNormalized(t *testing.T) {
	stubSnapshotsDirSeams(t, nil)
	oldExec := execCommand
	t.Cleanup(func() { execCommand = oldExec })
	execCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"configs":[]}`)
	}

	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}
	if err := ensureSnapperConfigs(context.Background(), cfg, nil); err != nil {
		t.Fatalf("ensureSnapperConfigs with a nil Runner: %v", err)
	}
}

// TestWriteEngineConfig_DispatchesByDriver: the engine-config writer is
// driver-aware — btrbk renders btrbk.conf next to snapshot.toml and touches no
// subprocess; snapper drives snapper and writes NO btrbk.conf.
func TestWriteEngineConfig_DispatchesByDriver(t *testing.T) {
	t.Run("btrbk", func(t *testing.T) {
		dir := t.TempDir()
		confPath := filepath.Join(dir, "snapshot.toml")
		cfg := &Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}}}
		mock := &MockRunner{}
		if err := WriteEngineConfig(context.Background(), cfg, confPath, mock); err != nil {
			t.Fatalf("WriteEngineConfig(btrbk): %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "btrbk.conf")); err != nil {
			t.Errorf("btrbk.conf not written: %v", err)
		}
		if len(mock.Calls) != 0 {
			t.Errorf("btrbk driver ran subprocesses: %+v", mock.Calls)
		}
	})

	t.Run("snapper", func(t *testing.T) {
		stubSnapshotsDirSeams(t, nil)
		dir := t.TempDir()
		confPath := filepath.Join(dir, "snapshot.toml")
		cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}
		mock := snapperMock(t, nil)
		if err := WriteEngineConfig(context.Background(), cfg, confPath, mock); err != nil {
			t.Fatalf("WriteEngineConfig(snapper): %v", err)
		}
		if len(snapperArgs(mock, "snapper")) == 0 {
			t.Error("snapper driver issued no snapper command")
		}
		if _, err := os.Stat(filepath.Join(dir, "btrbk.conf")); !os.IsNotExist(err) {
			t.Errorf("snapper driver must not write btrbk.conf (err=%v)", err)
		}
	})
}

// TestApply_SnapperProvisionsConfigs: `apply` with the snapper driver
// provisions through snapper and, with no schedule configured, issues no
// systemctl call.
func TestApply_SnapperProvisionsConfigs(t *testing.T) {
	stubSnapshotsDirSeams(t, nil)
	dir := t.TempDir()
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/"}}}
	mock := snapperMock(t, nil)

	if err := Apply(context.Background(), cfg, filepath.Join(dir, "snapshot.toml"), mock); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var sawCreate bool
	for _, args := range snapperArgs(mock, "snapper") {
		if len(args) > 2 && args[2] == "create-config" {
			sawCreate = true
		}
	}
	if !sawCreate {
		t.Error("apply did not provision the snapper config")
	}
	if calls := snapperArgs(mock, "systemctl"); len(calls) != 0 {
		t.Errorf("apply with no schedule called systemctl: %v", calls)
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
	stubSnapshotsDirSeams(t, nil)
	hookRoot := t.TempDir()
	origRoot := EmergeHookRoot
	t.Cleanup(func() { EmergeHookRoot = origRoot })
	EmergeHookRoot = hookRoot

	dir := t.TempDir()
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/"}}}
	if err := Apply(context.Background(), cfg, filepath.Join(dir, "snapshot.toml"), snapperMock(t, nil)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := os.Stat(filepath.Join(hookRoot, "etc", "portage")); !os.IsNotExist(err) {
		t.Errorf("apply wrote under %s/etc/portage; the emerge hook must be opt-in only (err=%v)", hookRoot, err)
	}
}

// ---------------------------------------------------------------------------
// Story 016 T4.1 — JSON snapshot listing (R3.2, R3.3, R3.4).
//
// testdata/snapper-list.json is a real `snapper --jsonout -c root list` capture
// from a Gentoo host running snapper 0.13.1 on btrfs under a pt_BR locale,
// trimmed to five representative entries: the "current" pseudo-snapshot 0 with
// its blank date, two bentoo timeline snapshots, and a pre/post pair written by
// the emerge hook. Every entry keeps all thirteen fields snapper emits, so these
// tests also pin that the parser tolerates the nine it does not consume.
//
// The fixture is ground truth: regenerating or tidying it would hollow out the
// assertions below, above all the date-format guard.
// ---------------------------------------------------------------------------

// snapperListGolden reads the captured `snapper --jsonout list` payload. The
// parse is pure — no subprocess, no filesystem beyond this fixture read.
func snapperListGolden(t *testing.T) []byte {
	t.Helper()
	out, err := os.ReadFile(filepath.Join("testdata", "snapper-list.json"))
	if err != nil {
		t.Fatalf("read snapper list fixture: %v", err)
	}
	return out
}

// TestSnapperListParse_GoldenFixture: the captured payload yields one Snapshot
// per real entry, in snapper's own order, with the "current" pseudo-snapshot 0
// excluded (016 R3.2, R3.3).
func TestSnapperListParse_GoldenFixture(t *testing.T) {
	snaps := parseSnapperListJSON(snapperListGolden(t), "/")

	wantIDs := []string{"1", "16", "2303", "2304"}
	if len(snaps) != len(wantIDs) {
		t.Fatalf("got %d snapshots, want %d (entry 0 excluded): %+v", len(snaps), len(wantIDs), snaps)
	}
	for i, want := range wantIDs {
		if snaps[i].ID != want {
			t.Errorf("snaps[%d].ID = %q, want %q", i, snaps[i].ID, want)
		}
		if snaps[i].ID == "0" {
			t.Errorf("snaps[%d] is the \"current\" pseudo-snapshot; it must be skipped (R3.3)", i)
		}
	}
}

// TestSnapperListParse_CreatedAtMatchesGoldenDate is the date-format guard, and
// the load-bearing assertion of this sub-task.
//
// snapperDateLayout must match what snapper actually prints ("2006-01-02
// 15:04:05"). If it did not, time.Parse would fail and CreatedAt would silently
// stay the zero time — every snapshot would list without a date and nothing else
// in the suite would notice. So the check is deliberately doubled: CreatedAt
// must be NON-ZERO, and it must equal the exact instant the fixture records for
// entry 1. The expected value is built with time.Date rather than by re-parsing
// through snapperDateLayout, because a wrong layout would fail both sides
// identically and let the comparison pass on two zero times.
func TestSnapperListParse_CreatedAtMatchesGoldenDate(t *testing.T) {
	snaps := parseSnapperListJSON(snapperListGolden(t), "/")
	if len(snaps) == 0 {
		t.Fatal("no snapshots parsed from the fixture")
	}

	got := snaps[0].CreatedAt // entry 1, "date": "2026-06-28 11:42:09"
	if got.IsZero() {
		t.Fatalf("snaps[0].CreatedAt is the zero time: snapperDateLayout (%q) does not match the date format snapper emits", snapperDateLayout)
	}
	// time.Parse with a zone-less layout yields UTC.
	want := time.Date(2026, time.June, 28, 11, 42, 9, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("snaps[0].CreatedAt = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	// Cross-check against the fixture's literal text with a hardcoded layout, so
	// the assertion cannot drift along with snapperDateLayout.
	if s := got.Format("2006-01-02 15:04:05"); s != "2026-06-28 11:42:09" {
		t.Errorf("snaps[0].CreatedAt renders as %q, want %q", s, "2026-06-28 11:42:09")
	}
}

// TestSnapperListParse_DerivesPathAndSubvolume: Subvolume is the caller's
// argument and Path follows snapper's fixed on-disk layout
// <subvolume>/.snapshots/<id>/snapshot — the derivation carried over unchanged
// from the table parser. A non-"/" subvolume is used so a stray hardcoded root
// would show up.
func TestSnapperListParse_DerivesPathAndSubvolume(t *testing.T) {
	snaps := parseSnapperListJSON(snapperListGolden(t), "/home")
	if len(snaps) < 2 {
		t.Fatalf("got %d snapshots, want at least 2", len(snaps))
	}
	for _, s := range snaps {
		if s.Subvolume != "/home" {
			t.Errorf("snapshot %s: Subvolume = %q, want /home", s.ID, s.Subvolume)
		}
	}
	if want := "/home/.snapshots/1/snapshot"; snaps[0].Path != want {
		t.Errorf("snaps[0].Path = %q, want %q", snaps[0].Path, want)
	}
	if want := "/home/.snapshots/16/snapshot"; snaps[1].Path != want {
		t.Errorf("snaps[1].Path = %q, want %q", snaps[1].Path, want)
	}
}

// TestSnapperListParse_EntryFieldsFromGolden: R3.2 asks for id, type, date and
// description off every entry. Snapshot carries no description (and the CLI
// renders none), so type and description are pinned where they are parsed — on
// snapperListEntry — while the fixture's nine unconsumed fields are ignored
// without error, which is the schema-drift tolerance the design asks for.
func TestSnapperListParse_EntryFieldsFromGolden(t *testing.T) {
	var configs map[string][]snapperListEntry
	if err := json.Unmarshal(snapperListGolden(t), &configs); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	entries, ok := configs["root"]
	if !ok {
		t.Fatalf("fixture has no \"root\" config, got keys %v", slices.Sorted(maps.Keys(configs)))
	}
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want 5 (including the \"current\" pseudo-snapshot)", len(entries))
	}

	want := []snapperListEntry{
		{Number: 0, Type: "single", Date: "", Description: "current"},
		{Number: 1, Type: "single", Date: "2026-06-28 11:42:09", Description: "bentoo snapshot"},
		{Number: 16, Type: "single", Date: "2026-06-29 00:00:49", Description: "bentoo snapshot"},
		{Number: 2303, Type: "pre", Date: "2026-07-19 18:11:40", Description: "bentoo: emerge app-portage/bentoolkit-0.14.0"},
		{Number: 2304, Type: "post", Date: "2026-07-19 18:11:57", Description: "bentoo: emerge app-portage/bentoolkit-0.14.0"},
	}
	for i, w := range want {
		if entries[i] != w {
			t.Errorf("entries[%d] = %+v, want %+v", i, entries[i], w)
		}
	}
}

// TestSnapperListParse_EmptyPayloads: a config with no snapshots, and no output
// at all, each yield an empty list and no error — the parse returns no error by
// signature, so "no error" means it must not panic or invent entries either
// (016 R3.4). Neither case is a malformed payload, so neither may warn.
func TestSnapperListParse_EmptyPayloads(t *testing.T) {
	cases := []struct{ name, payload string }{
		{"empty config slice", `{"root":[]}`},
		{"no configs", `{}`},
		{"zero-length output", ""},
		{"blank output", "\n  \n"},
		{"only the current pseudo-snapshot", `{"root":[{"number":0,"type":"single","date":"","description":"current"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			warnings := captureWarn(t)
			if got := parseSnapperListJSON([]byte(c.payload), "/"); len(got) != 0 {
				t.Errorf("got %d snapshots, want 0: %+v", len(got), got)
			}
			if got := warnings(); len(got) != 0 {
				t.Errorf("an empty listing warned %v; only a malformed payload should (R3.4)", got)
			}
		})
	}
}

// TestSnapperListParse_MalformedPayloadWarns: the parse cannot return an error,
// so unparseable output would otherwise degrade to an empty list — the exact
// shape of the bug this story fixes, where `snapshot list` reported "(none)"
// against a host full of snapshots. A schema drift or a snapper that stops
// emitting JSON must therefore be audible, mirroring how pruneRemote reports
// unparseable `rclone lsjson` output.
func TestSnapperListParse_MalformedPayloadWarns(t *testing.T) {
	for _, payload := range []string{"not json at all", `{"root":`, `{"root":"unexpected"}`} {
		t.Run(payload, func(t *testing.T) {
			warnings := captureWarn(t)
			if got := parseSnapperListJSON([]byte(payload), "/"); len(got) != 0 {
				t.Errorf("got %d snapshots, want 0: %+v", len(got), got)
			}
			if len(warnings()) == 0 {
				t.Error("malformed payload parsed silently; it must warn rather than look like an empty listing")
			}
		})
	}
}
