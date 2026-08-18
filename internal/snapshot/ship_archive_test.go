package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestArchivePipeStages_FullSend asserts the pure stage builder for a FULL send
// (parentPath==""): stage 1 is `btrfs send <snap.Path>` with NO `-p` flag, stage 2
// is the compressor, stage 3 is `rclone rcat <remote>/<prefix>/<leaf>` — the
// destination is pinned EXACTLY, so a regression to the old flat key turns this
// test red (R2.1, R3.1).
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
	// The upload destination is pinned EXACTLY, and by TWO assertions that bite
	// different regressions. A `HasPrefix(remote+"/")` check would not: it is
	// true of ANY key, the old flat one included, so it can never notice the
	// layout changing back.
	//
	//  1. Composed HERE from ArchivePrefix + "/" + ArchiveObjectLeaf — never
	//     from ArchiveObjectName, which is the builder under test: re-deriving
	//     the expectation through it would be tautological and stay green under
	//     any separator it chose. This is what catches a change to the JOIN.
	//  2. A literal, which catches a change INSIDE either half — a regression
	//     the composition in (1) would silently follow.
	dest := s3.args[len(s3.args)-1]
	if want := "gdrive:bentoo-backups/" + ArchivePrefix(snap.Subvolume) + "/" + ArchiveObjectLeaf(snap.ID); dest != want {
		t.Errorf("stage3 dest = %q, want %q — the key is <remote>/<prefix>/<leaf> (R3.1)", dest, want)
	}
	if want := "gdrive:bentoo-backups/-home/home.2026.zst"; dest != want {
		t.Errorf("stage3 dest = %q, want literal %q", dest, want)
	}
}

// TestArchivePipeStages_Incremental proves the builder supports the incremental
// form needed by T3.2: a non-empty parentPath puts `-p <parentPath>` BEFORE the
// snapshot path in stage 1 (R2.1). It also pins that the parent stays OUT of the
// remote key: an incremental send uploads to the same <remote>/<prefix>/<leaf> a
// full send would (R3.1) — the key identifies the snapshot, not the delta.
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
	if pIdx >= pathIdx {
		t.Errorf("stage1 args %v: -p (at %d) must come before snap path (at %d)", s1.args, pIdx, pathIdx)
	}

	// Stage 3 destination, pinned exactly (see FullSend for why the composed
	// form deliberately avoids ArchiveObjectName).
	s3 := stages[2]
	dest := s3.args[len(s3.args)-1]
	if want := "gdrive:bentoo-backups/" + ArchivePrefix(snap.Subvolume) + "/" + ArchiveObjectLeaf(snap.ID); dest != want {
		t.Errorf("stage3 dest = %q, want %q — the key is <remote>/<prefix>/<leaf> (R3.1)", dest, want)
	}
	if want := "gdrive:bentoo-backups/-home/home.2026.zst"; dest != want {
		t.Errorf("stage3 dest = %q, want literal %q", dest, want)
	}

	full := archivePipeStages(snap, "", "gdrive:bentoo-backups", "zstd")
	if fullDest := full[2].args[len(full[2].args)-1]; dest != fullDest {
		t.Errorf("incremental dest %q != full-send dest %q — the parent must not leak into the remote key",
			dest, fullDest)
	}
}

// TestArchivePipeStages_DefaultCompressor asserts an empty compress string falls
// back to zstd compressing stdin→stdout, and that the destination is unaffected
// by that fallback. The subvolume here ("root") contains no separator of its own,
// which is the point: sanitize leaves it untouched, the single '/' is inserted by
// the key builder, and the object still lands in its OWN directory (R3.1) — the
// prefix is never folded into the filename.
func TestArchivePipeStages_DefaultCompressor(t *testing.T) {
	stages := archivePipeStages(Snapshot{ID: "x", Subvolume: "root", Path: "/s/x"}, "", "r:bkt", "")
	if stages[1].name != "zstd" {
		t.Errorf("default compressor = %q, want zstd", stages[1].name)
	}

	s3 := stages[2]
	dest := s3.args[len(s3.args)-1]
	if want := "r:bkt/" + ArchivePrefix("root") + "/" + ArchiveObjectLeaf("x"); dest != want {
		t.Errorf("stage3 dest = %q, want %q — the key is <remote>/<prefix>/<leaf> (R3.1)", dest, want)
	}
	if want := "r:bkt/root/x.zst"; dest != want {
		t.Errorf("stage3 dest = %q, want literal %q", dest, want)
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
// returns a func yielding every warning emitted so far, FORMATTED. It restores
// warnLogf via t.Cleanup (the package-var override pattern used by config.go's
// Validate seam).
//
// It returns the messages rather than a bare "did it warn" because some warnings
// are required to say WHICH subject they are about: 038 R4.2 asks the on-demand
// prune to name the subvolume whose prefix is missing, and a boolean cannot tell
// a warning that names it from one that does not. Callers that only care whether
// a warn happened test len(...) instead.
func captureWarn(t *testing.T) (warnings func() []string) {
	t.Helper()
	orig := warnLogf
	var got []string
	warnLogf = func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { warnLogf = orig })
	return func() []string { return slices.Clone(got) }
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
	if pIdx >= pathIdx {
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

	warnings := captureWarn(t)

	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	rep, err := a.Send(t.Context(), snap)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if slices.Contains(mr.Calls[0].Args, "-p") {
		t.Errorf("stage1 args %v must not contain -p when no parent is recorded", mr.Calls[0].Args)
	}
	if len(warnings()) == 0 {
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

	warnings := captureWarn(t)

	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
	rep, err := a.Send(t.Context(), snap)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if slices.Contains(mr.Calls[0].Args, "-p") {
		t.Errorf("stage1 args %v must not contain -p in mode=full", mr.Calls[0].Args)
	}
	if got := warnings(); len(got) != 0 {
		t.Errorf("mode=full is an explicit choice and must NOT warn; got %v", got)
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
// command prints {Path,Name,Size,ModTime,IsDir,...}; the prune paths consume
// Name+ModTime and reject IsDir). ModTimes use RFC3339 with the offset rclone
// uses. IsDir is serialised from the ENTRY, not hardcoded false, so a fixture can
// express a directory at all (R4.3); the zero value is false, so every fixture
// that lists only objects is unaffected.
func scriptedLsjson(objs []rcloneObject) []byte {
	var b strings.Builder
	b.WriteByte('[')
	for i, o := range objs {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"Path":%q,"Name":%q,"Size":123,"ModTime":%q,"IsDir":%t}`,
			o.Name, o.Name, o.ModTime.Format(time.RFC3339Nano), o.IsDir)
	}
	b.WriteByte(']')
	return []byte(b.String())
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

	// The prune listed ONE subvolume's prefix directory, not the remote root. This
	// is the mechanism, asserted at the argv boundary: everything the selector can
	// possibly delete came from under this path (R1.1).
	prefixPath := a.remote + "/" + ArchivePrefix(snap.Subvolume)
	if got := lsjsonTargets(mr.Calls); !slices.Equal(got, []string{prefixPath}) {
		t.Errorf("rclone lsjson targets = %v, want exactly [%q] — a post-ship prune must list the shipped subvolume's prefix, not the whole remote (R1.1)", got, prefixPath)
	}

	// Assert the RAW deletefile arguments, prefix included. A scoped listing yields
	// bare LEAVES, so the delete has to re-join the prefix (R1.3) — and every way of
	// getting that join wrong ("<remote>/B" with the prefix dropped,
	// "<remote>/-home/-home/B" with it applied twice) truncates to the same "B",
	// which is why this no longer asserts on the basename.
	wantDel := []string{prefixPath + "/B", prefixPath + "/F", prefixPath + "/G"}
	got := deleteTargets(mr.Calls)
	sort.Strings(got)
	if !slices.Equal(got, wantDel) {
		t.Errorf("deletefile targets = %v, want %v", got, wantDel)
	}
}

// TestArchiveShipper_Send_NeverDeletesActiveParent is the R4.2 guard: even when
// GFS would drop the just-uploaded object (it is old enough to be out of policy),
// the active parent must NEVER be deletefiled, because the next incremental send
// uses it as the `-p` base — and by the time the prune runs it is also the head
// the parent store has just recorded.
//
// The guard's comparable form is ArchiveObjectLeaf(snap.ID), not the full key
// ArchiveObjectName: a scoped listing reports leaves. Get that wrong and nothing
// errors — the comparison is simply never true and the guard protects nothing,
// which is why the fixture below is deliberately built as a leaf.
func TestArchiveShipper_Send_NeverDeletesActiveParent(t *testing.T) {
	const remote = "gdrive:bentoo-backups"
	snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}

	// The fixture entry is a LEAF, and that is load-bearing. pruneRemote lists
	// "<remote>/<prefix>", and rclone reports every entry's Name relative to the
	// LISTED path, so a full key (ArchiveObjectName) is a string a real scoped
	// listing can never contain. Putting one here would also make this test green
	// against ANY guard: production would compare a full key too, both sides would
	// move together, and the assertion would measure nothing.
	activeLeaf := ArchiveObjectLeaf(snap.ID)
	activeTarget := remote + "/" + ArchivePrefix(snap.Subvolume) + "/" + activeLeaf

	// Build a listing where the active object is OLD (April) — GFS under
	// Hourly:1,Daily:1 would otherwise delete it, proving the guard is what spares it.
	listing := []rcloneObject{
		{Name: "recent-head.zst", ModTime: time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)},
		{Name: activeLeaf, ModTime: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
	}
	mr := archivePruneRunner(listing, nil)
	a := &archiveShipper{
		remote:    remote,
		mode:      "incremental",
		compress:  "zstd",
		run:       mr,
		parents:   &fakeParentStore{ok: false}, // first run → full send, still prunes
		retention: Retention{Hourly: 1, Daily: 1},
	}

	if _, err := a.Send(t.Context(), snap); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Assert on the RAW deletefile arguments and match the snapshot ID as well as
	// the exact target, so a guard that fails to spare the object is caught however
	// the delete path happens to join it (R6.2: assert the object SURVIVES, not the
	// shape of the comparison that spares it).
	for _, target := range deleteTargets(mr.Calls) {
		if target == activeTarget || strings.Contains(target, snap.ID) {
			t.Fatalf("active parent %q was deletefiled as %q — R4.2 violated", activeLeaf, target)
		}
	}
	// Sanity: gfsSelect alone WOULD have put the active object in del, so the guard
	// (not the policy) is what spared it.
	_, del := gfsSelect(listing, Retention{Hourly: 1, Daily: 1})
	var gfsWouldDelete bool
	for _, d := range del {
		if d.Name == activeLeaf {
			gfsWouldDelete = true
		}
	}
	if !gfsWouldDelete {
		t.Fatalf("test is not exercising the guard: GFS already keeps %q", activeLeaf)
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

	warnings := captureWarn(t)

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
	if len(warnings()) == 0 {
		t.Errorf("prune failure must emit a warn (surfaced, not swallowed)")
	}
	// No deletefile should have happened since listing failed.
	if got := deleteTargets(mr.Calls); len(got) != 0 {
		t.Errorf("deletefile calls = %v, want none after lsjson failure", got)
	}
}

// ---------------------------------------------------------------------------
// 038 — the post-ship prune must be scoped to ONE subvolume (R1.1, R6.1, R6.2)
// ---------------------------------------------------------------------------

// mapParentStore is a SUBVOLUME-AWARE test parentStore: Record files the snapshot
// under its (subvol, ship) pair and Last reads that same pair back. It exists
// because fakeParentStore.Last discards both arguments and returns one scripted
// head for every subvolume, which cannot model a fixture shipping TWO subvolumes
// through one shipper (R6.1) — each needs its own lineage head.
//
// lastErr scripts a read failure for INDIVIDUAL subvolumes (see failLast).
// fakeParentStore can fail a read too, but only for every subvolume at once,
// which cannot express the case 038 D3 turns on: the error has to land on a
// subvolume the loop reaches AFTER one it could already have pruned.
type mapParentStore struct {
	heads   map[string]Snapshot
	lastErr map[string]error // subvolume → the error Last returns for it
}

func newMapParentStore() *mapParentStore {
	return &mapParentStore{heads: make(map[string]Snapshot)}
}

// parentKey joins a (subvol, ship) pair with a byte that occurs in neither, so
// two distinct pairs can never collide onto one entry.
func parentKey(subvol, ship string) string { return subvol + "\x00" + ship }

// failLast makes Last(subvol, …) return err while every OTHER subvolume stays
// readable. That asymmetry is the whole point: it is what lets a test place the
// store failure on the SECOND subvolume and ask what happened to the first.
func (m *mapParentStore) failLast(subvol string, err error) {
	if m.lastErr == nil {
		m.lastErr = make(map[string]error)
	}
	m.lastErr[subvol] = err
}

func (m *mapParentStore) Last(subvol, ship string) (Snapshot, bool, error) {
	if err, scripted := m.lastErr[subvol]; scripted {
		return Snapshot{}, false, err
	}
	snap, ok := m.heads[parentKey(subvol, ship)]
	return snap, ok, nil
}

func (m *mapParentStore) Record(subvol, ship string, snap Snapshot) error {
	m.heads[parentKey(subvol, ship)] = snap
	return nil
}

var _ parentStore = (*mapParentStore)(nil)

// growingArchiveRunner scripts a MockRunner over a remote whose CONTENT CHANGES
// — the one thing archivePruneRunner cannot do, since it returns one fixed
// listing to every lsjson call:
//
//   - `rclone rcat <remote>/<key>` stores <key>, stamped with .modTime (which the
//     test sets before each ship — never time.Now(), so calendar bucketing is
//     deterministic and cannot straddle UTC midnight);
//   - `rclone lsjson <path>` serialises what is stored under <path>, with names
//     relative to the LISTED path (the behaviour measured against rclone 1.75.0);
//   - `rclone deletefile <remote>/<key>` removes <key>;
//   - the pipe stages (btrfs, the compressor) succeed.
//
// Modelling growth is what makes a two-ship fixture mean anything: the SECOND
// ship's prune has to see the FIRST ship's object, and that is the entire
// mechanism of the bug. Object keys are derived from the argv rather than rebuilt
// with archiveObjectName, so the helper keeps working when the remote key layout
// changes. Access is single-goroutine — runPipe runs the stages sequentially
// through this one Runner — so no locking is needed.
type growingArchiveRunner struct {
	*MockRunner
	remote  string
	modTime time.Time        // ModTime stamped on the next rcat upload
	objects []rcloneObject   // remote content; Name is the key RELATIVE to the remote root
	served  [][]rcloneObject // spy: the listing returned by each lsjson call

	// lsjsonErr fails `rclone lsjson` for SPECIFIC paths (see failLsjson).
	// archivePruneRunner also takes an lsjson error, but applies it to every
	// listing regardless of path, so it cannot model "one subvolume's prefix is
	// missing while another's is present" (R4.2) — only this can.
	lsjsonErr map[string]error // listed path → the error rclone returns for it
}

func newGrowingArchiveRunner(remote string) *growingArchiveRunner {
	g := &growingArchiveRunner{MockRunner: &MockRunner{}, remote: remote}
	g.RunFunc = func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
		if name != "rclone" || len(args) == 0 {
			return []byte("stream"), nil // btrfs / compressor stages succeed
		}
		target := args[len(args)-1] // rcat, lsjson and deletefile all take the path last
		switch args[0] {
		case "rcat":
			g.objects = append(g.objects, rcloneObject{Name: g.key(target), ModTime: g.modTime})
			return []byte("RCAT_DONE"), nil
		case "lsjson":
			if err, scripted := g.lsjsonErr[target]; scripted {
				return nil, err
			}
			listing := g.list(target)
			g.served = append(g.served, listing)
			return scriptedLsjson(listing), nil
		case "deletefile":
			g.remove(g.key(target))
			return nil, nil
		}
		return []byte("stream"), nil
	}
	return g
}

// key maps an rclone path argument ("<remote>/<key>") to the object key relative
// to the remote root. Reading it off the argv — instead of rebuilding it with
// archiveObjectName — is what keeps this helper honest if the key layout gains a
// per-subvolume prefix.
func (g *growingArchiveRunner) key(target string) string {
	return strings.TrimPrefix(strings.TrimPrefix(target, g.remote), "/")
}

// list returns what the remote holds under target, with names relative to the
// listed path (rclone's own convention). Listing the remote ROOT returns every
// object under today's flat layout, which creates no directories.
func (g *growingArchiveRunner) list(target string) []rcloneObject {
	prefix := g.key(target) // "" when the remote root itself is listed
	var out []rcloneObject
	for _, o := range g.objects {
		if prefix == "" {
			out = append(out, o)
			continue
		}
		leaf, under := strings.CutPrefix(o.Name, prefix+"/")
		if !under {
			continue
		}
		out = append(out, rcloneObject{Name: leaf, ModTime: o.ModTime})
	}
	return out
}

// failLsjson makes `rclone lsjson <remote>/<ArchivePrefix(subvol)>` return err,
// leaving every other path listable — the state of a remote where one configured
// subvolume has been shipped and another never has (038 R4.2).
func (g *growingArchiveRunner) failLsjson(subvol string, err error) {
	if g.lsjsonErr == nil {
		g.lsjsonErr = make(map[string]error)
	}
	g.lsjsonErr[g.remote+"/"+ArchivePrefix(subvol)] = err
}

// remove drops key from the modelled remote, so a later listing reflects a
// deletion that already happened.
func (g *growingArchiveRunner) remove(key string) {
	g.objects = slices.DeleteFunc(g.objects, func(o rcloneObject) bool { return o.Name == key })
}

// deleteTargets returns the RAW `rclone deletefile <target>` arguments a
// MockRunner recorded. It does NOT truncate to the last path segment, and that is
// the entire point: once the key carries a per-subvolume prefix,
// "<remote>/old.zst", "<remote>/-home/old.zst" and "<remote>/-home/-home/old.zst"
// all truncate to "old.zst", so a basename assertion cannot tell a correct
// re-join from a dropped or a doubled prefix. It REPLACED a truncating helper for
// that reason; assert on the full argument (R1.3).
func deleteTargets(calls []RunnerCall) []string {
	var out []string
	for _, c := range calls {
		if c.Name == "rclone" && len(c.Args) >= 2 && c.Args[0] == "deletefile" {
			out = append(out, c.Args[1])
		}
	}
	return out
}

// lsjsonTargets returns the RAW `rclone lsjson <target>` path arguments a
// MockRunner recorded, in call order. It is what pins WHICH slice of the remote a
// prune considered: the post-ship prune must list one subvolume's prefix
// directory, never the remote root, and that argument is the whole mechanism —
// objects outside it are not spared by a check, they are never candidates (R1.1).
func lsjsonTargets(calls []RunnerCall) []string {
	var out []string
	for _, c := range calls {
		if c.Name == "rclone" && len(c.Args) >= 2 && c.Args[0] == "lsjson" {
			out = append(out, c.Args[1])
		}
	}
	return out
}

// shippedHead reads back the lineage head Send recorded for subvol, failing if
// none was recorded. Reading the STORE (rather than assuming the snapshot value)
// is what ties the assertion to the object production would reference as the next
// incremental `-p` base (R2.1).
func shippedHead(t *testing.T, ps *mapParentStore, subvol, ship string) Snapshot {
	t.Helper()
	head, ok, err := ps.Last(subvol, ship)
	if err != nil {
		t.Fatalf("parent store Last(%q, %q): %v", subvol, ship, err)
	}
	if !ok {
		t.Fatalf("parent store recorded no head for %q — a successful Send must record one", subvol)
	}
	return head
}

// recordHead files snap as subvol's lineage head through the store's own API,
// leaving the state a previous successful ship would have left behind — the input
// the on-demand prune reads in its first phase.
func recordHead(t *testing.T, ps *mapParentStore, subvol, ship string, snap Snapshot) {
	t.Helper()
	if err := ps.Record(subvol, ship, snap); err != nil {
		t.Fatalf("recording the lineage head of %q: %v", subvol, err)
	}
}

// assertHeadNotDeleted fails if any recorded deletefile names head's object —
// either as the exact remote key, or by carrying head's snapshot ID, which
// catches the deletion under any key layout. It asserts the head SURVIVES, never
// the shape of the comparison that spares it (R6.2).
func assertHeadNotDeleted(t *testing.T, calls []RunnerCall, remote string, head Snapshot, why string) {
	t.Helper()
	headObject := remote + "/" + ArchiveObjectName(head.Subvolume, head.ID)
	for _, target := range deleteTargets(calls) {
		if target == headObject || strings.Contains(target, head.ID) {
			t.Errorf("rclone deletefile %q deleted %s's recorded head %q — %s", target, head.Subvolume, headObject, why)
		}
	}
}

// TestArchiveShipper_Send_DoesNotPruneOtherSubvolumes is the 038 regression:
// objects from different subvolumes share one flat remote namespace, so they land
// in the same calendar bucket and compete for the single representative slot it
// has. Shipping /root therefore deletes /home's lineage head — silently, because
// the deletefile SUCCEEDS, the next /home send still works (btrfs `-p` takes the
// on-disk parent PATH), and the damage only surfaces at restore. Pruning after a
// ship must consider only that snapshot's subvolume (R1.1) and must leave every
// other subvolume's recorded head alone (R2.1, asserted as "the head survives",
// R6.2).
//
// The fixture must ship TWO subvolumes into ONE remote, because a
// single-subvolume fixture cannot fail for this reason (R6.1) — which is exactly
// what the control subtest demonstrates: same sequence, same policy, same growing
// remote, only the subvolume COUNT differs, and it passes.
func TestArchiveShipper_Send_DoesNotPruneOtherSubvolumes(t *testing.T) {
	const remote = "gdrive:bentoo-backups"

	// Both instants fall in the SAME UTC calendar day, so Retention{Daily: 1}
	// yields exactly ONE bucket holding ONE representative: the newest object.
	// The second ship is the newer, so it wins the bucket and the first ship's
	// object is what a whole-remote prune selects for deletion.
	firstShipAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	secondShipAt := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)

	// newShipper builds the shipper under test: one shipper, one remote, the
	// production default mode ("incremental", newArchiveShipper) and a daily policy.
	newShipper := func(run Runner, ps parentStore) *archiveShipper {
		return &archiveShipper{
			remote:    remote,
			mode:      "incremental",
			compress:  "zstd",
			run:       run,
			parents:   ps,
			retention: Retention{Daily: 1},
		}
	}

	t.Run("two subvolumes: shipping /root must not delete /home's head", func(t *testing.T) {
		home := Snapshot{ID: "home.A", Subvolume: "/home", Path: "/snaps/home.A"}
		root := Snapshot{ID: "root.B", Subvolume: "/root", Path: "/snaps/root.B"}

		run := newGrowingArchiveRunner(remote)
		ps := newMapParentStore()
		a := newShipper(run, ps)

		// Each subvolume's FIRST ship has no recorded parent, so Send warns and
		// falls back to a full send — expected, and not what is under test.
		_ = captureWarn(t)

		run.modTime = firstShipAt
		if _, err := a.Send(t.Context(), home); err != nil {
			t.Fatalf("Send(%s): %v", home.Subvolume, err)
		}

		// Fixture integrity, half one: /home's object is REALLY on the remote by
		// the time /root ships. A fixture whose remote does not grow would make
		// this test pass for the wrong reason — hiding the bug instead of proving
		// it. Asserted on the modelled remote, not on what lsjson returned, so
		// scoping the listing later does not change what this means.
		if len(run.objects) != 1 || !strings.Contains(run.objects[0].Name, home.ID) {
			t.Fatalf("remote holds %v after shipping %s, want exactly its object", names(run.objects), home.Subvolume)
		}

		run.modTime = secondShipAt
		if _, err := a.Send(t.Context(), root); err != nil {
			t.Fatalf("Send(%s): %v", root.Subvolume, err)
		}

		// Fixture integrity, half two: /root's ship did prune, so it really did
		// decide what to keep against that grown remote.
		if len(run.served) < 2 {
			t.Fatalf("rclone lsjson served %d listings, want one per ship — a ship did not prune at all", len(run.served))
		}

		assertHeadNotDeleted(t, run.Calls, remote, shippedHead(t, ps, home.Subvolume, a.Name()),
			"pruning after /root's ship must consider only /root's own objects (R1.1, R2.1)")
	})

	t.Run("one subvolume control: its own recorded head survives", func(t *testing.T) {
		// The SAME sequence — two ships, one remote, same calendar day,
		// Retention{Daily: 1}, a remote that grows — with both ships in ONE
		// subvolume. The older object is legitimately pruned (that IS the policy,
		// applied within the subvolume); the recorded head must survive. Keeping
		// the control here makes the contrast visible in one place: only the
		// subvolume COUNT differs from the case above, so its failure cannot be
		// blamed on the fixture's shape.
		first := Snapshot{ID: "home.A", Subvolume: "/home", Path: "/snaps/home.A"}
		second := Snapshot{ID: "home.C", Subvolume: "/home", Path: "/snaps/home.C"}

		run := newGrowingArchiveRunner(remote)
		ps := newMapParentStore()
		a := newShipper(run, ps)

		_ = captureWarn(t) // the first ship has no parent yet; the second does.

		run.modTime = firstShipAt
		if _, err := a.Send(t.Context(), first); err != nil {
			t.Fatalf("Send(%s): %v", first.ID, err)
		}
		if len(run.objects) != 1 || !strings.Contains(run.objects[0].Name, first.ID) {
			t.Fatalf("remote holds %v after the first ship, want exactly its object", names(run.objects))
		}

		run.modTime = secondShipAt
		if _, err := a.Send(t.Context(), second); err != nil {
			t.Fatalf("Send(%s): %v", second.ID, err)
		}

		if len(run.served) < 2 {
			t.Fatalf("rclone lsjson served %d listings, want one per ship — a ship did not prune at all", len(run.served))
		}

		assertHeadNotDeleted(t, run.Calls, remote, shippedHead(t, ps, first.Subvolume, a.Name()),
			"a subvolume's own prune must never delete its current lineage head (R2.1)")
	})
}

// TestArchiveShipper_Send_LeavesObjectsOutsideEveryPrefixUntouched is R4.1 on the
// POST-SHIP path. TestArchiveShipper_PruneOnDemand_PrunesEachSubvolume asserts the
// same rule on the on-demand path; this is the half it does not reach, and the
// story's quality gate asks for both.
//
// The stray sits under "-var", a prefix no configured subvolume maps to — left by
// hand, by another tool, or by the pre-038 flat layout. Two guards keep the green
// honest. The stray's ModTime is old enough that gfsSelect WOULD drop it if it
// ever reached the selector (asserted, not assumed), so its survival can only come
// from the scoping and never from retention happening to keep it. And an equally
// old object under the SHIPPED subvolume's own prefix must be deleted in the same
// run, which proves the prune was actively deleting rather than skipped — without
// it, a prune that did nothing at all would pass this test.
func TestArchiveShipper_Send_LeavesObjectsOutsideEveryPrefixUntouched(t *testing.T) {
	const remote = "gdrive:bentoo-backups"
	shipAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	oldAt := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) // a different, older day bucket

	strayKey := ArchiveObjectName("/var", "v.old")   // under NO configured subvolume
	doomedKey := ArchiveObjectName("/home", "h.old") // under the shipped subvolume

	run := newGrowingArchiveRunner(remote)
	run.objects = append(run.objects,
		rcloneObject{Name: strayKey, ModTime: oldAt},
		rcloneObject{Name: doomedKey, ModTime: oldAt},
	)

	// Guard one: under this policy the stray is genuinely out of policy, so a
	// prune that saw it would delete it.
	_, del := gfsSelect([]rcloneObject{
		{Name: strayKey, ModTime: oldAt},
		{Name: "fresh.zst", ModTime: shipAt},
	}, Retention{Daily: 1})
	if !slices.ContainsFunc(del, func(o rcloneObject) bool { return o.Name == strayKey }) {
		t.Fatalf("fixture does not exercise R4.1: retention would keep %q anyway, so surviving proves nothing", strayKey)
	}

	a := &archiveShipper{
		remote:    remote,
		mode:      "incremental",
		compress:  "zstd",
		run:       run,
		parents:   newMapParentStore(),
		retention: Retention{Daily: 1},
	}
	_ = captureWarn(t) // a subvolume's first ship has no recorded parent and warns

	run.modTime = shipAt
	if _, err := a.Send(t.Context(), Snapshot{ID: "home.A", Subvolume: "/home", Path: "/snaps/home.A"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	targets := deleteTargets(run.Calls)

	// Guard two: the prune ran and deleted, so "the stray survived" is a decision
	// and not the absence of one.
	if doomed := remote + "/" + doomedKey; !slices.Contains(targets, doomed) {
		t.Fatalf("deletefile targets = %v, want %q among them — the prune deleted nothing, so this test cannot show R4.1", targets, doomed)
	}

	if stray := remote + "/" + strayKey; slices.Contains(targets, stray) {
		t.Errorf("rclone deletefile %q — an object under no configured subvolume's prefix must be left untouched (R4.1)", stray)
	}
}

// ---------------------------------------------------------------------------
// 038 — a directory entry is never a prune candidate (R4, R4.3)
// ---------------------------------------------------------------------------

// isDirRetention is the policy the R4.3 fixture is hand-derived against: two day
// buckets kept, everything else deleted.
var isDirRetention = Retention{Daily: 2}

// isDirListing is what a caller listing the remote ROOT sees once this story
// gives every subvolume its own directory: two DIRECTORY entries mixed in with
// real objects. It is built so that letting the directories reach gfsSelect
// breaks BOTH halves of R4.3 at once, each in a way a deletefile assertion can
// observe. Under Retention{Daily: 2}:
//
//   - "-root" (a directory, Jun 1 12:00) LOSES: Jun 1 is the third-newest day
//     bucket, so an unfiltered prune puts the directory in the delete set and
//     runs `rclone deletefile` on it. That is the "never deleted" half.
//   - "-home" (a directory, Jun 8 12:00) WINS: it is newer than the only real
//     object in the newest day bucket, so an unfiltered prune keeps the
//     DIRECTORY as that bucket's representative and deletes "recent.zst"
//     instead. That is the "never kept" half — a directory must not occupy a
//     retention slot that belongs to a real backup.
//
// Filtered, the listing is three objects across three day buckets and the policy
// deletes exactly the oldest, "old.zst". Both halves therefore show up as a
// difference in the deletefile calls, which is what makes the mutation (removing
// the filter) turn this test red instead of passing vacuously.
func isDirListing() []rcloneObject {
	utc := time.UTC
	return []rcloneObject{
		{Name: ArchivePrefix("/home"), ModTime: time.Date(2026, 6, 8, 12, 0, 0, 0, utc), IsDir: true},
		{Name: ArchiveObjectLeaf("recent"), ModTime: time.Date(2026, 6, 8, 9, 0, 0, 0, utc)},
		{Name: ArchiveObjectLeaf("mid"), ModTime: time.Date(2026, 6, 7, 9, 0, 0, 0, utc)},
		{Name: ArchivePrefix("/root"), ModTime: time.Date(2026, 6, 1, 12, 0, 0, 0, utc), IsDir: true},
		{Name: ArchiveObjectLeaf("old"), ModTime: time.Date(2026, 6, 1, 9, 0, 0, 0, utc)},
	}
}

// assertIsDirEntriesFiltered checks the three consequences of R4.3 on whichever
// prune path produced calls: no directory was deletefiled (never deleted), the
// object a directory would have evicted survived (never kept — the directory did
// not take a bucket representative slot), and the policy still applied normally
// to the real objects (exactly the oldest was pruned).
//
// deleteBase is the remote path the path under test joins its deletes under.
// Both prune paths are now scoped to one subvolume, so both pass
// "<remote>/<prefix>" and delete "<remote>/<prefix>/<leaf>" — but it stays a
// PARAMETER rather than being rebuilt in here, because that is what keeps the
// assertions on the full deletefile argument. Truncating to the basename would
// hide a prefix dropped, doubled or re-joined wrong, which is exactly the class
// of defect the scoping introduced the risk of (R1.3).
func assertIsDirEntriesFiltered(t *testing.T, calls []RunnerCall, deleteBase string) {
	t.Helper()
	targets := deleteTargets(calls)

	// Compare LEAVES for this one check: a directory entry that reached deletefile
	// arrives joined under deleteBase, whichever path produced it.
	for _, target := range targets {
		leaf := target[strings.LastIndex(target, "/")+1:]
		for _, subvol := range []string{"/home", "/root"} {
			if leaf == ArchivePrefix(subvol) {
				t.Errorf("rclone deletefile %q handed a DIRECTORY entry to deletefile — R4.3 violated", target)
			}
		}
	}

	evicted := deleteBase + "/" + ArchiveObjectLeaf("recent")
	if slices.Contains(targets, evicted) {
		t.Errorf("rclone deletefile %q deleted a real object — a directory took its bucket representative slot, so it was KEPT in its place (R4.3)", evicted)
	}

	got := slices.Clone(targets)
	sort.Strings(got)
	want := []string{deleteBase + "/" + ArchiveObjectLeaf("old")}
	if !slices.Equal(got, want) {
		t.Errorf("deletefile targets = %v, want %v — with directories dropped the policy prunes only the out-of-policy object", got, want)
	}
}

// TestArchiveShipper_Prune_DropsIsDirEntries asserts that a directory entry in an
// `rclone lsjson` listing is neither kept nor deleted by either prune path: it is
// dropped before gfsSelect, so it never occupies a retention bucket and is never
// handed to `rclone deletefile`, which takes a file (R4.3).
//
// Both paths are covered on purpose. They decode the same listing shape and feed
// the same selector, and a guard that exists on only one of them is the exact
// defect this story is fixing elsewhere in this file.
func TestArchiveShipper_Prune_DropsIsDirEntries(t *testing.T) {
	const remote = "gdrive:bentoo-backups"

	// Fixture integrity: on the RAW listing — the input the paths would see with
	// no filter — gfsSelect must BOTH select a directory for deletion AND delete
	// the object a directory outranked. Without either property this test would
	// still pass with the filter removed, and would measure nothing.
	_, del := gfsSelect(isDirListing(), isDirRetention)
	var dirSelected, objEvicted bool
	for _, d := range del {
		if d.Name == ArchivePrefix("/root") {
			dirSelected = true
		}
		if d.Name == ArchiveObjectLeaf("recent") {
			objEvicted = true
		}
	}
	if !dirSelected || !objEvicted {
		t.Fatalf("fixture does not exercise the guard: unfiltered gfsSelect deletes %v, want it to include both %q (directory) and %q (object outranked by a directory)",
			names(del), ArchivePrefix("/root"), ArchiveObjectLeaf("recent"))
	}

	t.Run("post-ship prune", func(t *testing.T) {
		mr := archivePruneRunner(isDirListing(), nil)
		a := &archiveShipper{
			remote:    remote,
			mode:      "full",
			compress:  "zstd",
			run:       mr,
			parents:   &fakeParentStore{},
			retention: isDirRetention,
		}

		// The shipped object's own key is not in the listing, so the R4.2
		// active-parent guard removes nothing here and cannot mask the filter.
		snap := Snapshot{ID: "home.2026", Subvolume: "/home", Path: "/snaps/home.2026"}
		if _, err := a.Send(t.Context(), snap); err != nil {
			t.Fatalf("Send: %v", err)
		}

		// Deliberately counterfactual for this path: the post-ship prune lists ONE
		// prefix directory, which by construction holds no directories at all, so
		// the scripted listing is one it cannot receive in production. That is the
		// point — the filter must not depend on the caller's scope being tidy, and
		// this is the only way to exercise it here. Deletes land under the scoped
		// prefix, so that is what the targets are checked against.
		assertIsDirEntriesFiltered(t, mr.Calls, remote+"/"+ArchivePrefix(snap.Subvolume))
	})

	t.Run("on-demand prune", func(t *testing.T) {
		mr := archivePruneRunner(isDirListing(), nil)
		a := &archiveShipper{
			remote:    remote,
			mode:      "full",
			compress:  "zstd",
			run:       mr,
			parents:   newMapParentStore(), // no recorded head → nothing protected
			retention: isDirRetention,
		}

		// ONE subvolume, because archivePruneRunner answers every lsjson with the
		// same listing: with two, this path would list two prefixes, see the same
		// five entries twice and delete the same leaf under each — a fixture
		// artefact, not behaviour. Per-subvolume independence is measured against a
		// path-aware remote in TestArchiveShipper_PruneOnDemand_PrunesEachSubvolume.
		const subvol = "/home"
		if err := a.PruneRemoteOnDemand(t.Context(), []string{subvol}); err != nil {
			t.Fatalf("PruneRemoteOnDemand: %v", err)
		}

		// Since 3.4 this path is scoped like the post-ship one, so its deletes are
		// joined under the subvolume prefix it listed. The listing itself stays
		// counterfactual here for the same reason as above: a scoped listing holds
		// no directories, and the filter must not depend on that.
		assertIsDirEntriesFiltered(t, mr.Calls, remote+"/"+ArchivePrefix(subvol))
	})
}

// TestDecodeLsjson_DropsIsDirEntries pins the filter at its source: a listing
// mixing directories with objects decodes to the objects ALONE, so a directory is
// not merely spared at the delete site — it never enters the selector's input at
// all. Decoding real `rclone lsjson` bytes (scriptedLsjson) keeps the assertion
// on the JSON boundary, tag included, rather than on an in-memory struct.
func TestDecodeLsjson_DropsIsDirEntries(t *testing.T) {
	objs, err := decodeLsjson(scriptedLsjson(isDirListing()))
	if err != nil {
		t.Fatalf("decodeLsjson: %v", err)
	}
	want := []string{ArchiveObjectLeaf("mid"), ArchiveObjectLeaf("old"), ArchiveObjectLeaf("recent")}
	if got := names(objs); !slices.Equal(got, want) {
		t.Errorf("decodeLsjson names = %v, want %v (directory entries dropped)", got, want)
	}
}

// ---------------------------------------------------------------------------
// 038 — a missing remote path is nothing to prune, not a failure (R4, R4.2)
// ---------------------------------------------------------------------------

// exitErrWithCode runs a trivial `sh -c "exit N"` to manufacture a REAL
// *exec.ExitError carrying the given code, so the errors.As/ExitCode() branch of
// isRemoteDirNotFound is exercised against the same type os/exec produces in
// production rather than a hand-rolled stand-in. It asserts the fixture itself:
// if sh is missing the process never starts, the error is an *exec.Error, and
// the test must say so loudly instead of silently proving nothing.
func exitErrWithCode(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("fixture broken: `sh -c \"exit %d\"` gave %[2]T (%[2]v), want a real *exec.ExitError", code, err)
	}
	if got := exitErr.ExitCode(); got != code {
		t.Fatalf("fixture broken: exit code = %d, want %d", got, code)
	}
	return err
}

// runnerShapedErr rebuilds the error execRunner.Run returns for a failed command:
// the *exec.ExitError joined with the child's trimmed stderr (runner.go:82-86).
// The predicate has to see through that Join, which is the whole reason the
// exit-code branch is reachable at all in production.
func runnerShapedErr(t *testing.T, code int, stderr string) error {
	t.Helper()
	return errors.Join(exitErrWithCode(t, code), errors.New(stderr))
}

// TestIsRemoteDirNotFound covers BOTH branches of the predicate and both of their
// negatives (038 R4.2). The exit-code branch is tested with a real *exec.ExitError
// reached THROUGH an errors.Join, because that traversal is exactly what a
// refactor of the Runner's error shape would silently break; its stderr text
// deliberately omits the phrase, so only the code branch can be answering.
func TestIsRemoteDirNotFound(t *testing.T) {
	// rclone 1.75.0's real stderr for a missing path, under LC_ALL=C.
	const rcloneStderr = "2026/08/18 12:00:00 ERROR : : error listing: directory not found"

	t.Run("text phrase alone is benign", func(t *testing.T) {
		// The shape a Runner MOCK can produce: no ExitError anywhere, text only.
		if !isRemoteDirNotFound(errors.New(rcloneStderr)) {
			t.Errorf("isRemoteDirNotFound(%q) = false, want true (text branch)", rcloneStderr)
		}
	})

	t.Run("exit code 3 through errors.Join is benign", func(t *testing.T) {
		// Stderr WITHOUT the phrase, so the text branch cannot answer: a true
		// result here can only come from errors.As finding the ExitError.
		err := runnerShapedErr(t, rcloneExitDirNotFound, "rclone: transfer log line")
		if strings.Contains(err.Error(), rcloneDirNotFoundText) {
			t.Fatalf("fixture leaks the phrase into the text, so it cannot isolate the exit-code branch: %v", err)
		}
		if !isRemoteDirNotFound(err) {
			t.Errorf("isRemoteDirNotFound(%v) = false, want true (exit code %d through errors.Join)",
				err, rcloneExitDirNotFound)
		}
	})

	t.Run("unrelated error is not benign", func(t *testing.T) {
		err := errors.New("rclone: connection reset by peer")
		if isRemoteDirNotFound(err) {
			t.Errorf("isRemoteDirNotFound(%v) = true, want false — a real failure must stay a failure", err)
		}
	})

	t.Run("another exit code without the phrase is not benign", func(t *testing.T) {
		err := runnerShapedErr(t, 1, "rclone: NOTICE: config file not found")
		if isRemoteDirNotFound(err) {
			t.Errorf("isRemoteDirNotFound(%v) = true, want false — only exit %d means directory not found",
				err, rcloneExitDirNotFound)
		}
	})

	t.Run("nil is not benign", func(t *testing.T) {
		if isRemoteDirNotFound(nil) {
			t.Error("isRemoteDirNotFound(nil) = true, want false")
		}
	})
}

// TestArchiveShipper_PruneOnDemand_MissingRemoteIsNotAFailure is the R4.2 wiring:
// a manual prune of a remote that was never shipped to must succeed with nothing
// deleted, not fail. Both error SHAPES are covered — the text-only one a mock
// produces and the ExitError+stderr Join production actually builds — because the
// predicate has two branches and the caller must be reached by either.
//
// The lsjson call itself is asserted, not just the nil return: with an all-zero
// retention PruneRemoteOnDemand returns nil BEFORE listing, so "no error, no
// deletes" would otherwise pass without the benign path ever being taken.
func TestArchiveShipper_PruneOnDemand_MissingRemoteIsNotAFailure(t *testing.T) {
	const remote = "gdrive:bentoo-backups"

	cases := []struct {
		name   string
		lsjson func(t *testing.T) error
	}{
		{
			name:   "text-only error (Runner mock shape)",
			lsjson: func(*testing.T) error { return errors.New("2026/08/18 ERROR : : error listing: directory not found") },
		},
		{
			name: "ExitError joined with stderr (production Runner shape)",
			lsjson: func(t *testing.T) error {
				return runnerShapedErr(t, rcloneExitDirNotFound,
					"2026/08/18 ERROR : : error listing: directory not found")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A non-empty listing is scripted on purpose: it never reaches the
			// selector (lsjson fails), so if the benign branch ever regressed into
			// "parse whatever came back", the deletes would show up here.
			mr := archivePruneRunner(gfsFixture(), tc.lsjson(t))
			a := &archiveShipper{
				remote:    remote,
				mode:      "full",
				compress:  "zstd",
				run:       mr,
				parents:   newMapParentStore(), // nothing shipped yet → no protected head
				retention: Retention{Hourly: 2, Daily: 3},
			}

			subvolumes := []string{"/home", "/root"}
			warnings := captureWarn(t)

			if err := a.PruneRemoteOnDemand(t.Context(), subvolumes); err != nil {
				t.Fatalf("PruneRemoteOnDemand returned %v, want nil — a path that was never shipped to is nothing to prune", err)
			}
			if got := deleteTargets(mr.Calls); len(got) != 0 {
				t.Errorf("deletefile calls = %v, want none — the benign path must never delete", got)
			}

			// The listings themselves are asserted, not just the nil return: with an
			// all-zero retention this returns nil BEFORE listing, so "no error, no
			// deletes" would otherwise pass without the benign path ever being taken.
			// One listing per subvolume, each scoped to that subvolume's prefix.
			var want []string
			for _, sv := range subvolumes {
				want = append(want, remote+"/"+ArchivePrefix(sv))
			}
			if got := lsjsonTargets(mr.Calls); !slices.Equal(got, want) {
				t.Fatalf("rclone lsjson targets = %v, want %v — one scoped listing per subvolume, and without them this test proves nothing", got, want)
			}

			// R4.2: a missing prefix is skipped out loud, naming the subvolume — the
			// operator has to be able to tell WHICH one went unpruned.
			got := warnings()
			if len(got) != len(subvolumes) {
				t.Fatalf("warnings = %v, want one per skipped subvolume (%d)", got, len(subvolumes))
			}
			for i, sv := range subvolumes {
				if !strings.Contains(got[i], sv) {
					t.Errorf("warning %q does not name subvolume %q (R4.2)", got[i], sv)
				}
			}
		})
	}
}

// TestArchiveShipper_PruneOnDemand_SurfacesRealLsjsonFailure is the other half of
// the contract (Unchanged Behavior: a manual prune still returns its errors). A
// missing directory is the ONE newly-benign case; a genuine listing failure must
// still come back as an error, with its cause preserved for the failed stage.
func TestArchiveShipper_PruneOnDemand_SurfacesRealLsjsonFailure(t *testing.T) {
	lsErr := errors.New("rclone: connection reset by peer")
	mr := archivePruneRunner(gfsFixture(), lsErr)
	a := &archiveShipper{
		remote:    "gdrive:bentoo-backups",
		mode:      "full",
		compress:  "zstd",
		run:       mr,
		parents:   newMapParentStore(),
		retention: Retention{Hourly: 2, Daily: 3},
	}

	err := a.PruneRemoteOnDemand(t.Context(), []string{"/home"})
	if err == nil {
		t.Fatal("PruneRemoteOnDemand returned nil, want the lsjson failure surfaced as a failed stage")
	}
	if !errors.Is(err, lsErr) {
		t.Errorf("returned error %v does not wrap the lsjson cause %v", err, lsErr)
	}
	if !strings.Contains(err.Error(), "rclone lsjson") {
		t.Errorf("returned error %q does not name the failing command", err)
	}
	if got := deleteTargets(mr.Calls); len(got) != 0 {
		t.Errorf("deletefile calls = %v, want none after a failed listing", got)
	}
}

// ---------------------------------------------------------------------------
// 038 — the on-demand prune is per-subvolume, and two-phase (R1.2, R1.3, R2.1,
// R2.2, R4.1, R4.2 — design D3)
// ---------------------------------------------------------------------------

// onDemandRetention is the policy every fixture below is hand-derived against:
// the 2 newest DAY buckets survive, everything older is out of policy.
var onDemandRetention = Retention{Daily: 2}

// onDemandRemote is the modelled remote the on-demand prune tests run against:
// four objects for /home, four for /root, and one under "-var" — a prefix no
// configured subvolume names.
//
// The two configured subvolumes deliberately share the SAME four day buckets,
// with /root's object an hour newer than /home's in each. That is what makes a
// scoped prune measurably different from an unscoped one, which is the whole
// mechanism of the bug this story fixes:
//
//   - listed per prefix, each subvolume keeps its own 2 newest days (Jun 8, Jun 7)
//     and loses its own 2 older ones (Jun 1, May 15) — 2 deletes each, and neither
//     subvolume can even name the other's objects;
//   - listed at the remote ROOT, those four buckets have ONE representative each
//     and /root wins all four on ModTime, so every /home object becomes a delete
//     candidate, "-var"'s along with them.
//
// A fixture whose subvolumes fell in disjoint buckets would yield the same delete
// set either way and would measure nothing.
func onDemandRemote() []rcloneObject {
	at := func(month time.Month, day, hour int) time.Time {
		return time.Date(2026, month, day, hour, 0, 0, 0, time.UTC)
	}
	return []rcloneObject{
		{Name: ArchiveObjectName("/home", "h-jun8"), ModTime: at(time.June, 8, 10)},
		{Name: ArchiveObjectName("/home", "h-jun7"), ModTime: at(time.June, 7, 10)},
		{Name: ArchiveObjectName("/home", "h-jun1"), ModTime: at(time.June, 1, 10)},
		{Name: ArchiveObjectName("/home", "h-may15"), ModTime: at(time.May, 15, 10)},
		{Name: ArchiveObjectName("/root", "r-jun8"), ModTime: at(time.June, 8, 11)},
		{Name: ArchiveObjectName("/root", "r-jun7"), ModTime: at(time.June, 7, 11)},
		{Name: ArchiveObjectName("/root", "r-jun1"), ModTime: at(time.June, 1, 11)},
		{Name: ArchiveObjectName("/root", "r-may15"), ModTime: at(time.May, 15, 11)},
		{Name: ArchiveObjectName("/var", "v-jun1"), ModTime: at(time.June, 1, 9)},
	}
}

// onDemandRunner is the PATH-AWARE Runner these tests need: growingArchiveRunner
// preloaded with onDemandRemote, so `rclone lsjson <remote>/<prefix>` answers with
// that prefix's own objects as LEAVES and `rclone deletefile` removes what it is
// handed. archivePruneRunner cannot serve here — it answers every listing with one
// fixed slice regardless of path, so under it two subvolumes are indistinguishable.
func onDemandRunner(remote string) *growingArchiveRunner {
	g := newGrowingArchiveRunner(remote)
	g.objects = onDemandRemote()
	return g
}

// onDemandShipper wires an archiveShipper over the fixture Runner and store.
func onDemandShipper(g *growingArchiveRunner, ps *mapParentStore) *archiveShipper {
	return &archiveShipper{
		remote:    g.remote,
		mode:      "incremental",
		compress:  "zstd",
		run:       g,
		parents:   ps,
		retention: onDemandRetention,
	}
}

// scopedDeletes runs the REAL selector over exactly what a scoped listing of
// subvol returns, so a test can prove its fixture gives that subvolume something
// to delete before asserting on what was or was not deleted.
//
// Every test below asserts an absence somewhere ("this object survived", "nothing
// was deleted at all"), and an absence is also what a fixture with nothing out of
// policy produces no matter how the code is shaped. This is what tells the two
// apart — without it the mutations these tests exist to catch stay green.
func scopedDeletes(t *testing.T, g *growingArchiveRunner, subvol string) []string {
	t.Helper()
	_, del := gfsSelect(g.list(g.remote+"/"+ArchivePrefix(subvol)), onDemandRetention)
	return names(del)
}

// TestArchiveShipper_PruneOnDemand_PrunesEachSubvolume is R1.2/R1.3/R4.1: a manual
// prune applies the retention policy to each configured subvolume INDEPENDENTLY,
// inside that subvolume's own prefix directory. One subvolume's policy is decided
// by its own listing alone, so it can neither evict another subvolume's object
// from a shared calendar bucket nor name it in a deletefile — and an object under
// no configured prefix at all is never even a candidate.
func TestArchiveShipper_PruneOnDemand_PrunesEachSubvolume(t *testing.T) {
	const remote = "gdrive:bentoo-backups"
	subvolumes := []string{"/home", "/root"}

	g := onDemandRunner(remote)
	// No recorded head for either subvolume: the ordinary first-run state, which
	// must protect nothing WITHOUT being an error (R2.2). It also keeps the head
	// guard out of the way of what this test measures.
	a := onDemandShipper(g, newMapParentStore())

	// Fixture integrity, both halves: each subvolume must have something out of
	// policy, or "deleted exactly its own" would hold vacuously.
	for _, sv := range subvolumes {
		if got := scopedDeletes(t, g, sv); len(got) == 0 {
			t.Fatalf("fixture gives %q nothing out of policy under %+v — an assertion over its deletes would measure nothing", sv, onDemandRetention)
		}
	}
	// And the scope itself must be observable: the whole-remote selection has to
	// DIFFER from the per-prefix one, or a prune that ignored the scoping entirely
	// would produce the same deletes and this test could not tell the two apart.
	var scopedKeys []string
	for _, sv := range subvolumes {
		for _, leaf := range scopedDeletes(t, g, sv) {
			scopedKeys = append(scopedKeys, ArchivePrefix(sv)+"/"+leaf)
		}
	}
	sort.Strings(scopedKeys)
	_, unscoped := gfsSelect(g.list(remote), onDemandRetention)
	if slices.Equal(scopedKeys, names(unscoped)) {
		t.Fatalf("fixture cannot distinguish a scoped prune from an unscoped one: both select %v", scopedKeys)
	}

	if err := a.PruneRemoteOnDemand(t.Context(), subvolumes); err != nil {
		t.Fatalf("PruneRemoteOnDemand: %v", err)
	}

	// One scoped listing per subvolume, and never the remote root (R1.2). This is
	// the mechanism, not a detail: objects outside the listed prefix are not spared
	// by a check, they are never candidates.
	var wantListed []string
	for _, sv := range subvolumes {
		wantListed = append(wantListed, remote+"/"+ArchivePrefix(sv))
	}
	if got := lsjsonTargets(g.Calls); !slices.Equal(got, wantListed) {
		t.Errorf("rclone lsjson targets = %v, want %v — each subvolume's own prefix, never the remote root", got, wantListed)
	}

	// Exactly each subvolume's own out-of-policy objects, deleted back under its
	// own prefix (R1.3). Asserted on the FULL argument: a dropped or a doubled
	// prefix truncates to the same basename.
	want := []string{
		remote + "/" + ArchiveObjectName("/home", "h-jun1"),
		remote + "/" + ArchiveObjectName("/home", "h-may15"),
		remote + "/" + ArchiveObjectName("/root", "r-jun1"),
		remote + "/" + ArchiveObjectName("/root", "r-may15"),
	}
	got := slices.Clone(deleteTargets(g.Calls))
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("deletefile targets = %v, want %v — each subvolume pruned against its OWN listing, under its OWN prefix", got, want)
	}

	// R4.1, stated separately from the set equality above so a regression says why
	// it matters: "-var" is under no configured subvolume, so it is out of reach.
	stray := remote + "/" + ArchiveObjectName("/var", "v-jun1")
	if slices.Contains(deleteTargets(g.Calls), stray) {
		t.Errorf("rclone deletefile %q — an object under no configured subvolume's prefix must be left untouched (R4.1)", stray)
	}
}

// TestArchiveShipper_PruneOnDemand_StoreReadErrorDeletesNothing is design D3's
// ordering requirement and the regression it exists to prevent: a parent-store
// READ error must abort the prune BEFORE any deletion, whichever subvolume it
// lands on.
//
// The error is scripted on the SECOND subvolume, which is the only placement that
// can tell a two-phase loop from an interleaved one. Interleaved — read subvolume
// N's head, prune subvolume N, move on — the first subvolume is fully pruned and
// only then does the read fail: a READ failure that mutated the remote. Two-phase,
// every head is read first, so the same failure deletes nothing.
//
// This is why the deletes are asserted rather than only the error: both shapes
// return the error, and only the delete count separates them.
func TestArchiveShipper_PruneOnDemand_StoreReadErrorDeletesNothing(t *testing.T) {
	const remote = "gdrive:bentoo-backups"
	subvolumes := []string{"/home", "/root"} // the read error lands on the SECOND

	g := onDemandRunner(remote)
	ps := newMapParentStore()
	storeErr := errors.New("parent store: reading lineage head: permission denied")
	ps.failLast(subvolumes[1], storeErr)
	a := onDemandShipper(g, ps)

	// Fixture integrity: the FIRST subvolume must have something to delete. With
	// nothing out of policy there, "zero deletes" would hold whatever the loop
	// shape is and the interleaved mutation would stay green.
	if got := scopedDeletes(t, g, subvolumes[0]); len(got) == 0 {
		t.Fatalf("fixture gives %q nothing out of policy: pruning it would delete nothing anyway, so this test could not detect an interleaved loop", subvolumes[0])
	}

	err := a.PruneRemoteOnDemand(t.Context(), subvolumes)
	if err == nil {
		t.Fatal("PruneRemoteOnDemand returned nil, want the parent-store read error surfaced as a failed stage")
	}
	if !errors.Is(err, storeErr) {
		t.Errorf("returned error %v does not wrap the store cause %v", err, storeErr)
	}
	if !strings.Contains(err.Error(), subvolumes[1]) {
		t.Errorf("returned error %q does not name %q, the subvolume whose head could not be read", err, subvolumes[1])
	}

	// The assertion this test exists for (038 D3).
	if got := deleteTargets(g.Calls); len(got) != 0 {
		t.Errorf("a parent-store read error on %q had already deleted %d object(s): %v — D3: phase one must read EVERY head and abort before phase two deletes anything, or a READ failure leaves the remote half-pruned",
			subvolumes[1], len(got), got)
	}
}

// TestArchiveShipper_PruneOnDemand_MissingPrefixStillPrunesTheRest is R4.2: a
// subvolume whose prefix does not exist yet — nothing has ever been shipped for
// it — is nothing to prune, not a failure. It is skipped with a warning NAMING
// it, and the remaining subvolumes are pruned normally.
//
// The missing subvolume is listed FIRST on purpose: last, a `return` where the
// code must `continue` would leave every other subvolume already pruned and the
// test would pass on the broken shape.
func TestArchiveShipper_PruneOnDemand_MissingPrefixStillPrunesTheRest(t *testing.T) {
	const remote = "gdrive:bentoo-backups"
	const missing = "/home"
	subvolumes := []string{missing, "/root"}

	g := onDemandRunner(remote)
	// Model the remote honestly: a prefix that does not exist holds nothing.
	g.objects = slices.DeleteFunc(g.objects, func(o rcloneObject) bool {
		return strings.HasPrefix(o.Name, ArchivePrefix(missing)+"/")
	})
	g.failLsjson(missing, runnerShapedErr(t, rcloneExitDirNotFound,
		"2026/08/18 12:00:00 ERROR : : error listing: directory not found"))
	a := onDemandShipper(g, newMapParentStore())

	// Fixture integrity: without something out of policy under /root, "the others
	// were still pruned" would be unobservable.
	if got := scopedDeletes(t, g, "/root"); len(got) == 0 {
		t.Fatalf("fixture gives %q nothing out of policy — that the remaining subvolume was still pruned would be unobservable", "/root")
	}

	warnings := captureWarn(t)
	if err := a.PruneRemoteOnDemand(t.Context(), subvolumes); err != nil {
		t.Fatalf("PruneRemoteOnDemand returned %v, want nil — a prefix that was never shipped to is nothing to prune (R4.2)", err)
	}

	// It CONTINUED: the second subvolume was listed at all, and pruned fully.
	wantListed := []string{remote + "/" + ArchivePrefix(missing), remote + "/" + ArchivePrefix("/root")}
	if got := lsjsonTargets(g.Calls); !slices.Equal(got, wantListed) {
		t.Errorf("rclone lsjson targets = %v, want %v — a missing prefix must not stop the remaining subvolumes", got, wantListed)
	}
	want := []string{
		remote + "/" + ArchiveObjectName("/root", "r-jun1"),
		remote + "/" + ArchiveObjectName("/root", "r-may15"),
	}
	got := slices.Clone(deleteTargets(g.Calls))
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("deletefile targets = %v, want %v — the present subvolume is pruned, and fully", got, want)
	}

	// Skipped OUT LOUD, naming the subvolume: the operator has to be able to tell
	// which one went unpruned (R4.2).
	msgs := warnings()
	if len(msgs) != 1 {
		t.Fatalf("warnings = %v, want exactly one — the skipped subvolume, and only it", msgs)
	}
	if !strings.Contains(msgs[0], missing) {
		t.Errorf("warning %q does not name the skipped subvolume %q (R4.2)", msgs[0], missing)
	}
}

// TestArchiveShipper_PruneOnDemand_NeverDeletesRecordedHeads is R2.1: a manual
// prune spans every configured subvolume, so it must spare EVERY subvolume's
// recorded lineage head — the object each one's next incremental send will
// reference with `-p` — not just one.
//
// Two things make this test able to fail. Each head is deliberately an
// OUT-OF-POLICY object: a head the retention would keep anyway is spared by the
// policy, not by the guard, and a test built on one passes with the guard
// deleted. And the surviving heads are asserted alongside the objects that DID
// go, so a prune that deleted nothing at all cannot pass either.
//
// The guard's comparable form is a LEAF (ArchiveObjectLeaf), because a scoped
// listing reports names relative to the listed path. Compared as the full key
// ArchiveObjectName it would match nothing and would do so silently — no error,
// no empty result, just a guard that never fires — which is exactly what this
// test turns into a red.
func TestArchiveShipper_PruneOnDemand_NeverDeletesRecordedHeads(t *testing.T) {
	const remote = "gdrive:bentoo-backups"
	subvolumes := []string{"/home", "/root"}

	g := onDemandRunner(remote)
	ps := newMapParentStore()
	a := onDemandShipper(g, ps)

	heads := map[string]Snapshot{
		"/home": {ID: "h-jun1", Subvolume: "/home", Path: "/snaps/h-jun1"},
		"/root": {ID: "r-jun1", Subvolume: "/root", Path: "/snaps/r-jun1"},
	}
	for _, sv := range subvolumes {
		recordHead(t, ps, sv, a.Name(), heads[sv])
	}

	// Fixture integrity: unguarded, the selector picks each head for deletion.
	for _, sv := range subvolumes {
		if !slices.Contains(scopedDeletes(t, g, sv), ArchiveObjectLeaf(heads[sv].ID)) {
			t.Fatalf("fixture: %q's recorded head %q is not out of policy under %+v, so the retention would spare it anyway and the guard would go unmeasured",
				sv, heads[sv].ID, onDemandRetention)
		}
	}

	if err := a.PruneRemoteOnDemand(t.Context(), subvolumes); err != nil {
		t.Fatalf("PruneRemoteOnDemand: %v", err)
	}

	for _, sv := range subvolumes {
		assertHeadNotDeleted(t, g.Calls, remote, heads[sv],
			"a manual prune spans every configured subvolume, so every recorded head is the base its next incremental send needs (R2.1)")
	}

	// The prune still did its job: each subvolume's other out-of-policy object
	// went. Without this the test would pass on a prune that deleted nothing.
	want := []string{
		remote + "/" + ArchiveObjectName("/home", "h-may15"),
		remote + "/" + ArchiveObjectName("/root", "r-may15"),
	}
	got := slices.Clone(deleteTargets(g.Calls))
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("deletefile targets = %v, want %v — the recorded heads spared, everything else out of policy pruned", got, want)
	}
}
