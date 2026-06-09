package snapshot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestArchivePipeStages_FullSend asserts the pure stage builder for a FULL send
// (parentPath==""): stage 1 is `btrfs send <snap.Path>` with NO `-p` flag, stage 2
// is the compressor, stage 3 is `rclone rcat <remote>/<obj>` (R2.1).
func TestArchivePipeStages_FullSend(t *testing.T) {
	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	stages := archivePipeStages(snap, "", "gdrive:bentoo-backups", "zstd")

	if len(stages) != 3 {
		t.Fatalf("got %d stages, want 3", len(stages))
	}

	// Stage 1: btrfs send, no -p, ends with the snapshot path.
	s1 := stages[0]
	if s1.name != "btrfs" {
		t.Errorf("stage1 name = %q, want btrfs", s1.name)
	}
	if slices.Contains(s1.args, "-p") {
		t.Errorf("stage1 args %v must not contain -p on a full send", s1.args)
	}
	if !slices.Contains(s1.args, "send") {
		t.Errorf("stage1 args %v missing send", s1.args)
	}
	if !slices.Contains(s1.args, snap.Path) {
		t.Errorf("stage1 args %v missing snapshot path %q", s1.args, snap.Path)
	}

	// Stage 2: compressor reading stdin → stdout.
	if stages[1].name != "zstd" {
		t.Errorf("stage2 name = %q, want zstd", stages[1].name)
	}

	// Stage 3: rclone rcat <remote>/<obj>.
	s3 := stages[2]
	if s3.name != "rclone" {
		t.Errorf("stage3 name = %q, want rclone", s3.name)
	}
	if !slices.Contains(s3.args, "rcat") {
		t.Errorf("stage3 args %v missing rcat", s3.args)
	}
	dest := s3.args[len(s3.args)-1]
	if !strings.HasPrefix(dest, "gdrive:bentoo-backups/") {
		t.Errorf("stage3 dest = %q, want prefix gdrive:bentoo-backups/", dest)
	}
}

// TestArchivePipeStages_Incremental proves the builder supports the incremental
// form needed by T3.2: a non-empty parentPath puts `-p <parentPath>` BEFORE the
// snapshot path in stage 1 (R2.1).
func TestArchivePipeStages_Incremental(t *testing.T) {
	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	parent := "/snaps/home.2025"
	stages := archivePipeStages(snap, parent, "gdrive:bentoo-backups", "zstd")

	s1 := stages[0]
	pIdx := slices.Index(s1.args, "-p")
	if pIdx < 0 {
		t.Fatalf("stage1 args %v missing -p on incremental send", s1.args)
	}
	if pIdx+1 >= len(s1.args) || s1.args[pIdx+1] != parent {
		t.Fatalf("stage1 args %v: -p not followed by parent path %q", s1.args, parent)
	}
	pathIdx := slices.Index(s1.args, snap.Path)
	if pathIdx < 0 {
		t.Fatalf("stage1 args %v missing snapshot path %q", s1.args, snap.Path)
	}
	if !(pIdx < pathIdx) {
		t.Errorf("stage1 args %v: -p (at %d) must come before snap path (at %d)", s1.args, pIdx, pathIdx)
	}
}

// TestArchivePipeStages_DefaultCompressor asserts an empty compress string falls
// back to zstd compressing stdin→stdout.
func TestArchivePipeStages_DefaultCompressor(t *testing.T) {
	stages := archivePipeStages(Snapshot{ID: "x", Subvolume: "root", Path: "/s/x"}, "", "r:bkt", "")
	if stages[1].name != "zstd" {
		t.Errorf("default compressor = %q, want zstd", stages[1].name)
	}
}

// markerRunner is a MockRunner-style scripted Runner that returns a per-stage
// marker stdout keyed by the command name, so the pipe-chaining test can prove
// each stage's stdin equals the previous stage's stdout.
func markerRunner(t *testing.T, mr *MockRunner, markers map[string][]byte) {
	t.Helper()
	mr.RunFunc = func(_ context.Context, name string, _ []string, _ []byte) ([]byte, error) {
		out, ok := markers[name]
		if !ok {
			t.Fatalf("unexpected stage %q", name)
		}
		return out, nil
	}
}

// TestArchiveShipper_Send_PipeChaining asserts Send runs exactly 3 stages in order
// (btrfs send → compressor → rclone rcat) and that each stage's stdin equals the
// previous stage's stdout — i.e. the pipe is wired through the Runner (R2.1).
func TestArchiveShipper_Send_PipeChaining(t *testing.T) {
	mr := &MockRunner{}
	markerRunner(t, mr, map[string][]byte{
		"btrfs":  []byte("BTRFS_STREAM"),
		"zstd":   []byte("ZSTD_STREAM"),
		"rclone": []byte("RCAT_DONE"),
	})
	a := &archiveShipper{remote: "gdrive:bentoo-backups", mode: "full", compress: "zstd", run: mr, parents: &fakeParentStore{}}

	rep, err := a.Send(t.Context(), Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(mr.Calls) != 3 {
		t.Fatalf("got %d calls, want 3 (btrfs, zstd, rclone)", len(mr.Calls))
	}
	wantNames := []string{"btrfs", "zstd", "rclone"}
	for i, want := range wantNames {
		if mr.Calls[i].Name != want {
			t.Errorf("call %d name = %q, want %q", i, mr.Calls[i].Name, want)
		}
	}

	// Chaining: stage1 gets no stdin (or empty); stage2 stdin == stage1 stdout;
	// stage3 stdin == stage2 stdout.
	if len(mr.Calls[0].Stdin) != 0 {
		t.Errorf("stage1 (btrfs) stdin = %q, want empty", mr.Calls[0].Stdin)
	}
	if string(mr.Calls[1].Stdin) != "BTRFS_STREAM" {
		t.Errorf("stage2 stdin = %q, want BTRFS_STREAM", mr.Calls[1].Stdin)
	}
	if string(mr.Calls[2].Stdin) != "ZSTD_STREAM" {
		t.Errorf("stage3 stdin = %q, want ZSTD_STREAM", mr.Calls[2].Stdin)
	}

	if rep.Target != "gdrive:bentoo-backups" || rep.Snapshot != "home.2026" {
		t.Errorf("report = %+v, want Target=gdrive:bentoo-backups Snapshot=home.2026", rep)
	}
	if rep.Delegated {
		t.Errorf("archive Send must not delegate (Delegated=true)")
	}
	if rep.Incremental {
		t.Errorf("T3.1 full send must report Incremental=false")
	}
}

// TestArchiveShipper_Send_StageFailureFailsShip asserts that a non-zero stage
// (the rclone upload) fails the whole ship and Send returns that error (R2.3).
func TestArchiveShipper_Send_StageFailureFailsShip(t *testing.T) {
	rcatErr := errors.New("rclone: quota exceeded")
	mr := &MockRunner{
		RunFunc: func(_ context.Context, name string, _ []string, _ []byte) ([]byte, error) {
			if name == "rclone" {
				return nil, rcatErr
			}
			return []byte("stream"), nil
		},
	}
	a := &archiveShipper{remote: "r:bkt", mode: "full", compress: "zstd", run: mr, parents: &fakeParentStore{}}

	_, err := a.Send(t.Context(), Snapshot{ID: "x", Subvolume: "root", Path: "/s/x"})
	if !errors.Is(err, rcatErr) {
		t.Fatalf("Send err = %v, want rclone stage error %v", err, rcatErr)
	}
}

// TestArchiveShipper_Send_CtxCancel asserts the whole pipe runs under one
// cancellable ctx: when the ctx is cancelled, a stage that honors ctx.Done() makes
// Send return context.Canceled (R2.3 — pipe killed under one ctx).
func TestArchiveShipper_Send_CtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancel before running so the first stage observes Done immediately.

	mr := &MockRunner{
		RunFunc: func(ctx context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return []byte("stream"), nil
			}
		},
	}
	a := &archiveShipper{remote: "r:bkt", mode: "full", compress: "zstd", run: mr, parents: &fakeParentStore{}}

	_, err := a.Send(ctx, Snapshot{ID: "x", Subvolume: "root", Path: "/s/x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send err = %v, want context.Canceled", err)
	}
}

// TestArchiveShipper_Name mirrors the other shippers: configured name, else "archive".
func TestArchiveShipper_Name(t *testing.T) {
	if got := (&archiveShipper{name: "offsite-archive"}).Name(); got != "offsite-archive" {
		t.Errorf("Name() = %q, want offsite-archive", got)
	}
	if got := (&archiveShipper{}).Name(); got != "archive" {
		t.Errorf("Name() default = %q, want archive", got)
	}
}

// fakeParentStore is a test parentStore: Last returns the scripted (last, ok,
// lastErr) and Record appends every recorded snap to `recorded` (returning
// recordErr), so the incremental-selection and record-only-on-success invariants
// (R3.2/G3) are observable without touching the filesystem.
type fakeParentStore struct {
	last      Snapshot
	ok        bool
	lastErr   error
	recordErr error
	recorded  []Snapshot // spy: every Record(snap) appended
}

func (f *fakeParentStore) Last(_, _ string) (Snapshot, bool, error) {
	return f.last, f.ok, f.lastErr
}

func (f *fakeParentStore) Record(_, _ string, snap Snapshot) error {
	f.recorded = append(f.recorded, snap)
	return f.recordErr
}

var _ parentStore = (*fakeParentStore)(nil)

// captureWarn redirects warnLogf to a recorder for the duration of the test and
// returns a func reporting whether any warn was emitted. It restores warnLogf via
// t.Cleanup (the package-var override pattern used by config.go's Validate seam).
func captureWarn(t *testing.T) (warned func() bool) {
	t.Helper()
	orig := warnLogf
	var got bool
	warnLogf = func(string, ...any) { got = true }
	t.Cleanup(func() { warnLogf = orig })
	return func() bool { return got }
}

// TestArchiveShipper_Send_Incremental: with a recorded parent and mode
// "incremental", Send must build an incremental pipe (`-p <parent.Path>` before
// snap.Path), report Incremental=true, and record THIS snap as the new head only
// after the ship succeeds (R2.2, R3.2).
func TestArchiveShipper_Send_Incremental(t *testing.T) {
	parent := Snapshot{ID: "p1", Path: "/snap/home.p1"}
	ps := &fakeParentStore{last: parent, ok: true}
	mr := &MockRunner{} // nil RunFunc → all stages succeed
	a := &archiveShipper{remote: "r:bkt", mode: "incremental", compress: "zstd", run: mr, parents: ps}

	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	rep, err := a.Send(t.Context(), snap)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Stage 1 (btrfs) argv must carry `-p <parent.Path>` immediately before snap.Path.
	s1 := mr.Calls[0]
	if s1.Name != "btrfs" {
		t.Fatalf("stage1 name = %q, want btrfs", s1.Name)
	}
	pIdx := slices.Index(s1.Args, "-p")
	if pIdx < 0 || pIdx+1 >= len(s1.Args) || s1.Args[pIdx+1] != parent.Path {
		t.Fatalf("stage1 args %v: want -p %q (the parent PATH, not its ID)", s1.Args, parent.Path)
	}
	pathIdx := slices.Index(s1.Args, snap.Path)
	if !(pIdx < pathIdx) {
		t.Errorf("stage1 args %v: -p (at %d) must precede snap path (at %d)", s1.Args, pIdx, pathIdx)
	}

	if !rep.Incremental {
		t.Errorf("report.Incremental = false, want true on a parented send")
	}
	if rep.Note != "archive incremental send" {
		t.Errorf("report.Note = %q, want %q", rep.Note, "archive incremental send")
	}

	// Recorded exactly the CURRENT snap, after success (new lineage head).
	if len(ps.recorded) != 1 || ps.recorded[0].ID != snap.ID {
		t.Fatalf("recorded = %+v, want exactly the current snap %q", ps.recorded, snap.ID)
	}
}

// TestArchiveShipper_Send_AbsentParentFallback: mode "incremental" but no recorded
// parent (ok=false) must fall back to a FULL send, WARN (no silent fallback, R3.3),
// report Incremental=false, and still record THIS snap as the new head.
func TestArchiveShipper_Send_AbsentParentFallback(t *testing.T) {
	ps := &fakeParentStore{ok: false}
	mr := &MockRunner{}
	a := &archiveShipper{remote: "r:bkt", mode: "incremental", compress: "zstd", run: mr, parents: ps}

	warned := captureWarn(t)

	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	rep, err := a.Send(t.Context(), snap)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if slices.Contains(mr.Calls[0].Args, "-p") {
		t.Errorf("stage1 args %v must not contain -p when no parent is recorded", mr.Calls[0].Args)
	}
	if !warned() {
		t.Errorf("absent-parent fallback must emit a warn (R3.3 — no silent fallback)")
	}
	if rep.Incremental {
		t.Errorf("report.Incremental = true, want false on a fallback full send")
	}
	if rep.Note != "archive full send" {
		t.Errorf("report.Note = %q, want %q", rep.Note, "archive full send")
	}
	if len(ps.recorded) != 1 || ps.recorded[0].ID != snap.ID {
		t.Fatalf("recorded = %+v, want the current snap recorded as the new head", ps.recorded)
	}
}

// TestArchiveShipper_Send_FullModeAlwaysFull: mode "full" must send full even when
// a parent IS recorded — no `-p`, no warn, Incremental=false — and still record the
// snap as the new head.
func TestArchiveShipper_Send_FullModeAlwaysFull(t *testing.T) {
	ps := &fakeParentStore{last: Snapshot{ID: "p1", Path: "/snap/home.p1"}, ok: true}
	mr := &MockRunner{}
	a := &archiveShipper{remote: "r:bkt", mode: "full", compress: "zstd", run: mr, parents: ps}

	warned := captureWarn(t)

	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	rep, err := a.Send(t.Context(), snap)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if slices.Contains(mr.Calls[0].Args, "-p") {
		t.Errorf("stage1 args %v must not contain -p in mode=full", mr.Calls[0].Args)
	}
	if warned() {
		t.Errorf("mode=full is an explicit choice and must NOT warn")
	}
	if rep.Incremental {
		t.Errorf("report.Incremental = true, want false in mode=full")
	}
	if len(ps.recorded) != 1 || ps.recorded[0].ID != snap.ID {
		t.Fatalf("recorded = %+v, want the current snap recorded as the new head", ps.recorded)
	}
}

// TestArchiveShipper_Send_RecordOnlyOnSuccess is the critical chain-integrity
// assertion (R3.2/G3): when a pipe stage FAILS, Send returns that error and records
// NO parent — a failed ship must never advance the lineage head, or the next
// incremental send would `-p` against a parent whose object was never uploaded.
func TestArchiveShipper_Send_RecordOnlyOnSuccess(t *testing.T) {
	stageErr := errors.New("rclone: quota exceeded")
	ps := &fakeParentStore{last: Snapshot{ID: "p1", Path: "/snap/home.p1"}, ok: true}
	mr := &MockRunner{
		RunFunc: func(_ context.Context, name string, _ []string, _ []byte) ([]byte, error) {
			if name == "rclone" {
				return nil, stageErr
			}
			return []byte("stream"), nil
		},
	}
	a := &archiveShipper{remote: "r:bkt", mode: "incremental", compress: "zstd", run: mr, parents: ps}

	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	_, err := a.Send(t.Context(), snap)
	if !errors.Is(err, stageErr) {
		t.Fatalf("Send err = %v, want stage error %v", err, stageErr)
	}
	if len(ps.recorded) != 0 {
		t.Fatalf("recorded = %+v, want NONE — a failed ship must not advance the lineage head (G3)", ps.recorded)
	}
}

// TestArchiveShipper_Send_StoreReadError: a parentStore.Last failure is a real
// error, not a silent full-send — Send returns it and ships/records nothing.
func TestArchiveShipper_Send_StoreReadError(t *testing.T) {
	readErr := errors.New("parents: corrupt record")
	ps := &fakeParentStore{lastErr: readErr}
	mr := &MockRunner{}
	a := &archiveShipper{remote: "r:bkt", mode: "incremental", compress: "zstd", run: mr, parents: ps}

	_, err := a.Send(t.Context(), Snapshot{ID: "x", Subvolume: "/home", Path: "/s/x"})
	if !errors.Is(err, readErr) {
		t.Fatalf("Send err = %v, want store read error %v", err, readErr)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("store read error must ship nothing; got %d stage calls", len(mr.Calls))
	}
	if len(ps.recorded) != 0 {
		t.Errorf("store read error must record nothing; got %+v", ps.recorded)
	}
}

// ---------------------------------------------------------------------------
// T5.1 — archive GFS retention (R4, R4.1, R4.2)
// ---------------------------------------------------------------------------

// gfsFixture is the shared sample for the GFS tests. ModTimes are explicit UTC
// instants chosen so the keep/delete split exercises every rule:
//   - A and B fall in the SAME hour bucket (2026-06-08 10:00) and the SAME day
//     bucket (2026-06-08) — A is newer, so A is the representative of both.
//   - C is a second hour bucket on the same day (09:00).
//   - D, E, F, G walk back across days/weeks/months so the count cutoffs bite.
//
// Under Retention{Hourly:2, Daily:3}:
//   - Hourly keeps the 2 newest hour buckets: 10:00→A, 09:00→C  => {A,C}
//   - Daily keeps the 3 newest day buckets:   Jun8→A, Jun7→D, Jun1→E => {A,D,E}
//   - Union kept = {A,C,D,E};  deleted = {B,F,G}.
func gfsFixture() []rcloneObject {
	utc := time.UTC
	return []rcloneObject{
		{Name: "A", ModTime: time.Date(2026, 6, 8, 10, 30, 0, 0, utc)},
		{Name: "B", ModTime: time.Date(2026, 6, 8, 10, 5, 0, 0, utc)},
		{Name: "C", ModTime: time.Date(2026, 6, 8, 9, 0, 0, 0, utc)},
		{Name: "D", ModTime: time.Date(2026, 6, 7, 23, 0, 0, 0, utc)},
		{Name: "E", ModTime: time.Date(2026, 6, 1, 12, 0, 0, 0, utc)},
		{Name: "F", ModTime: time.Date(2026, 5, 15, 12, 0, 0, 0, utc)},
		{Name: "G", ModTime: time.Date(2026, 4, 10, 12, 0, 0, 0, utc)},
	}
}

// names extracts a sorted slice of object names for set-equality assertions.
func names(objs []rcloneObject) []string {
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = o.Name
	}
	sort.Strings(out)
	return out
}

// TestGFSSelect_MixedGranularities asserts the pure selector keeps the newest
// object per calendar bucket, the `count` most-recent buckets per granularity,
// and the UNION across granularities (R4.1). See gfsFixture for the hand-derived
// expected split under Retention{Hourly:2, Daily:3}.
func TestGFSSelect_MixedGranularities(t *testing.T) {
	keep, del := gfsSelect(gfsFixture(), Retention{Hourly: 2, Daily: 3})

	wantKeep := []string{"A", "C", "D", "E"}
	wantDel := []string{"B", "F", "G"}
	if got := names(keep); !slices.Equal(got, wantKeep) {
		t.Errorf("keep = %v, want %v", got, wantKeep)
	}
	if got := names(del); !slices.Equal(got, wantDel) {
		t.Errorf("del = %v, want %v", got, wantDel)
	}
	// keep and del must partition the input exactly (no loss, no duplication).
	if len(keep)+len(del) != len(gfsFixture()) {
		t.Errorf("keep(%d)+del(%d) != input(%d)", len(keep), len(del), len(gfsFixture()))
	}
}

// TestGFSSelect_AllZeroPolicyKeepsAll asserts that an all-zero policy keeps every
// object and deletes nothing — the "no retention configured" case the caller uses
// to skip pruning entirely (matches restic skipping forget when unconfigured).
func TestGFSSelect_AllZeroPolicyKeepsAll(t *testing.T) {
	keep, del := gfsSelect(gfsFixture(), Retention{})
	if len(del) != 0 {
		t.Errorf("del = %v, want empty under all-zero policy", names(del))
	}
	if got := names(keep); !slices.Equal(got, []string{"A", "B", "C", "D", "E", "F", "G"}) {
		t.Errorf("keep = %v, want all objects", got)
	}
}

// scriptedLsjson returns a JSON array as `rclone lsjson` would emit it (the real
// command prints {Path,Name,Size,ModTime,IsDir,...}; gfsSelect only consumes
// Name+ModTime). ModTimes use RFC3339 with the offset rclone uses.
func scriptedLsjson(objs []rcloneObject) []byte {
	var b strings.Builder
	b.WriteByte('[')
	for i, o := range objs {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"Path":%q,"Name":%q,"Size":123,"ModTime":%q,"IsDir":false}`,
			o.Name, o.Name, o.ModTime.Format(time.RFC3339Nano))
	}
	b.WriteByte(']')
	return []byte(b.String())
}

// deletedNames scans a MockRunner's calls for `rclone deletefile <remote>/<obj>`
// and returns the object basenames, so a test can assert exactly which remote
// objects were pruned. The pipe also calls rclone with rcat — args[0] disambiguates.
func deletedNames(calls []RunnerCall) []string {
	var out []string
	for _, c := range calls {
		if c.Name != "rclone" || len(c.Args) < 2 || c.Args[0] != "deletefile" {
			continue
		}
		target := c.Args[1] // "<remote>/<obj>"
		if i := strings.LastIndex(target, "/"); i >= 0 {
			out = append(out, target[i+1:])
		} else {
			out = append(out, target)
		}
	}
	sort.Strings(out)
	return out
}

// archivePruneRunner scripts a MockRunner for the prune integration tests: the
// pipe stages (btrfs/zstd/rclone rcat) succeed; `rclone lsjson` returns the
// scripted listing; `rclone deletefile` succeeds and is recorded via Calls. An
// optional lsjsonErr forces the listing call to fail (non-fatal-prune test).
func archivePruneRunner(listing []rcloneObject, lsjsonErr error) *MockRunner {
	return &MockRunner{
		RunFunc: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
			if name == "rclone" && len(args) > 0 {
				switch args[0] {
				case "lsjson":
					if lsjsonErr != nil {
						return nil, lsjsonErr
					}
					return scriptedLsjson(listing), nil
				case "deletefile":
					return nil, nil
				case "rcat":
					return []byte("RCAT_DONE"), nil
				}
			}
			// pipe stages btrfs/zstd (and any other rclone subcommand) succeed.
			return []byte("stream"), nil
		},
	}
}

// TestArchiveShipper_Send_PrunesOutOfPolicy asserts the full flow: after a
// successful ship + Record, Send lists the remote (lsjson), applies GFS, and
// deletefiles exactly the out-of-policy objects (R4.1). The active-parent guard
// is exercised by the next test; here the active object is NOT in the listing, so
// every GFS-deleted object is actually deleted.
func TestArchiveShipper_Send_PrunesOutOfPolicy(t *testing.T) {
	mr := archivePruneRunner(gfsFixture(), nil)
	a := &archiveShipper{
		remote:    "gdrive:bentoo-backups",
		mode:      "full",
		compress:  "zstd",
		run:       mr,
		parents:   &fakeParentStore{},
		retention: Retention{Hourly: 2, Daily: 3},
	}

	// snap's object name is not among A..G, so the active-parent guard removes
	// nothing from the GFS delete set here.
	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	rep, err := a.Send(t.Context(), snap)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if rep.Target != "gdrive:bentoo-backups" {
		t.Errorf("report.Target = %q, want gdrive:bentoo-backups", rep.Target)
	}

	wantDel := []string{"B", "F", "G"}
	if got := deletedNames(mr.Calls); !slices.Equal(got, wantDel) {
		t.Errorf("deletefile objects = %v, want %v", got, wantDel)
	}
}

// TestArchiveShipper_Send_NeverDeletesActiveParent is the R4.2 guard: even when
// GFS would drop the just-uploaded object (it is old enough to be out of policy),
// the active parent — archiveObjectName(snap) — must NEVER be deletefiled, because
// the next incremental send uses it as the `-p` base.
func TestArchiveShipper_Send_NeverDeletesActiveParent(t *testing.T) {
	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	active := archiveObjectName(snap)

	// Build a listing where the active object is OLD (April) — GFS under
	// Hourly:1,Daily:1 would otherwise delete it, proving the guard is what spares it.
	listing := []rcloneObject{
		{Name: "recent-head.zst", ModTime: time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)},
		{Name: active, ModTime: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
	}
	mr := archivePruneRunner(listing, nil)
	a := &archiveShipper{
		remote:    "gdrive:bentoo-backups",
		mode:      "incremental",
		compress:  "zstd",
		run:       mr,
		parents:   &fakeParentStore{ok: false}, // first run → full send, still prunes
		retention: Retention{Hourly: 1, Daily: 1},
	}

	if _, err := a.Send(t.Context(), snap); err != nil {
		t.Fatalf("Send: %v", err)
	}

	for _, n := range deletedNames(mr.Calls) {
		if n == active {
			t.Fatalf("active parent %q was deletefiled — R4.2 violated", active)
		}
	}
	// Sanity: gfsSelect alone WOULD have put the active object in del, so the guard
	// (not the policy) is what spared it.
	_, del := gfsSelect(listing, Retention{Hourly: 1, Daily: 1})
	var gfsWouldDelete bool
	for _, d := range del {
		if d.Name == active {
			gfsWouldDelete = true
		}
	}
	if !gfsWouldDelete {
		t.Fatalf("test is not exercising the guard: GFS already keeps %q", active)
	}
}

// TestArchiveShipper_Send_PruneFailureNonFatal asserts a prune error (here the
// lsjson listing fails) does NOT fail the ship: the backup already succeeded and
// the parent is recorded, so Send returns the success report and surfaces the
// prune failure via a warn (R4 — pruning is post-success housekeeping).
func TestArchiveShipper_Send_PruneFailureNonFatal(t *testing.T) {
	lsErr := errors.New("rclone: lsjson connection reset")
	mr := archivePruneRunner(nil, lsErr)
	ps := &fakeParentStore{}
	a := &archiveShipper{
		remote:    "gdrive:bentoo-backups",
		mode:      "full",
		compress:  "zstd",
		run:       mr,
		parents:   ps,
		retention: Retention{Hourly: 2, Daily: 3},
	}

	warned := captureWarn(t)

	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	rep, err := a.Send(t.Context(), snap)
	if err != nil {
		t.Fatalf("Send returned error %v, want nil — a prune failure must not fail the ship", err)
	}
	if rep.Snapshot != "home.2026" {
		t.Errorf("report.Snapshot = %q, want home.2026 (ship still succeeded)", rep.Snapshot)
	}
	// The successful ship still recorded the new lineage head before pruning ran.
	if len(ps.recorded) != 1 || ps.recorded[0].ID != snap.ID {
		t.Fatalf("recorded = %+v, want the snap recorded despite prune failure", ps.recorded)
	}
	if !warned() {
		t.Errorf("prune failure must emit a warn (surfaced, not swallowed)")
	}
	// No deletefile should have happened since listing failed.
	if got := deletedNames(mr.Calls); len(got) != 0 {
		t.Errorf("deletefile calls = %v, want none after lsjson failure", got)
	}
}
