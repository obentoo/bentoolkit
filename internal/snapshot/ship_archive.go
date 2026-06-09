package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// archiveShipper streams a btrfs snapshot to an rclone remote as a single
// compressed object (design §4.2). The pipeline is
// `btrfs send [-p parent] <snap> | <compressor> | rclone rcat <remote>/<obj>`,
// run end-to-end under one cancellable ctx so that cancelling the parent kills
// every stage and any stage's non-zero exit fails the whole ship (R2.1, R2.3).
// Unlike the ssh shipper, bentoolkit moves the bytes itself here, so Send is not
// delegated. All subprocesses go through run (R7.2).
//
// This file is the FULL-send path only (T3.1): no `-p`, no parent recording, no
// GFS. mode and parents are populated now but CONSUMED later — mode selects
// full vs incremental in T3.2, parents supplies the `-p` parent in T3.2, and
// retention-driven GFS lands in T5.
type archiveShipper struct {
	name      string
	remote    string      // rclone remote+path prefix, e.g. "gdrive:bentoo-backups"
	mode      string      // "incremental" (default) | "full"  (selection logic is T3.2)
	compress  string      // compressor; default "zstd"
	run       Runner      // subprocess seam (R7.2)
	parents   parentStore // CONSUMED in T3.2 (incremental parent selection).
	retention Retention   // GFS policy applied to the remote after a successful ship (T5.1, R4).
}

// Name returns the ship's configured name, or "archive" when unnamed (mirrors
// sshShipper.Name() / resticShipper.Name()).
func (a *archiveShipper) Name() string {
	if a.name != "" {
		return a.name
	}
	return "archive"
}

// Send streams snap to the rclone remote, choosing an incremental (`-p <parent>`)
// or full transfer per a.mode and the recorded parent, then advancing the lineage
// head on success (T3.2). The bytes move here, so Send is not delegated
// (Delegated=false).
//
// Mode selection (R2.2, R3.3):
//   - mode=="full": always a full send (parentPath==""); no parent lookup, no warn.
//   - otherwise (incremental, the default): consult a.parents.Last for the recorded
//     parent. A store-read error is surfaced, NOT swallowed into a silent full send.
//     With a parent, send incrementally against its on-disk Path (btrfs `-p` takes
//     the parent subvolume PATH, not its ID). With no recorded parent (the first-run
//     state), fall back to a full send AND warn — the configured-incremental-but-no-
//     parent case must never fall back silently (R3.3); the run is recorded as full
//     via the warn plus Incremental=false.
//
// Parent recording is the single most important correctness invariant (R3.2/G3):
// the new parent is recorded ONLY after the pipe succeeds. A failed ship returns its
// error and records nothing, so the lineage head never advances to a snapshot whose
// object was never uploaded (which would make the next `-p` reference a missing
// base). When recording itself fails AFTER a successful upload, the error is
// surfaced rather than swallowed: the operator must see that parent bookkeeping
// broke even though the bytes are up. The already-uploaded object is acceptable —
// per design §6, a partial/duplicate remote object is left for rclone to overwrite
// on the next run.
func (a *archiveShipper) Send(ctx context.Context, snap Snapshot) (ShipReport, error) {
	var parentPath string
	if a.mode != "full" {
		parent, ok, err := a.parents.Last(snap.Subvolume, a.Name())
		if err != nil {
			return ShipReport{}, err
		}
		if ok {
			parentPath = parent.Path
		} else {
			warnLogf("snapshot: ship %q subvolume %q: no recorded parent; sending full", a.Name(), snap.Subvolume)
		}
	}

	stages := archivePipeStages(snap, parentPath, a.remote, a.compress)
	if _, err := runPipe(ctx, a.run, stages); err != nil {
		return ShipReport{}, err
	}

	// Record THIS snapshot as the new lineage head — only now that the ship
	// succeeded (R3.2/G3). Surface a record failure: the upload is up but the
	// bookkeeping broke, and the operator must know (the partial object is left for
	// rclone to overwrite next run, design §6).
	if err := a.parents.Record(snap.Subvolume, a.Name(), snap); err != nil {
		return ShipReport{}, err
	}

	// Prune the remote AFTER the ship succeeded and the new head is recorded
	// (R4.1). Ordering and non-fatality are deliberate: pruning is post-success
	// housekeeping, not part of the backup. A list/delete failure must NOT fail
	// the ship — the bytes are up and the lineage head is recorded, so the run
	// genuinely succeeded; a prune error only means stale objects linger, which is
	// surfaced via warn and retried next run (mirrors restic, where a forget/prune
	// hiccup does not unwind a completed backup). pruneRemote therefore swallows
	// its error into a warn and Send still returns the success report.
	a.pruneRemote(ctx, snap)

	incremental := parentPath != ""
	note := "archive full send"
	if incremental {
		note = "archive incremental send"
	}
	return ShipReport{
		Target:      a.remote,
		Snapshot:    snap.ID,
		Delegated:   false,
		Note:        note,
		Incremental: incremental,
	}, nil
}

// pipeStage is one command in the archive pipe: a program name and its argv. It is
// the unit the pure builder emits and the executor feeds through the Runner.
type pipeStage struct {
	name string
	args []string
}

// archivePipeStages builds the three-stage archive pipe for snap as pure data, so
// the argv/wiring is unit-testable without touching btrfs or rclone (G2). When
// parentPath=="" stage 1 is a FULL send (no `-p`); a non-empty parentPath emits
// `btrfs send -p <parentPath> <snap.Path>`, the incremental form T3.2 will use.
//
//   - Stage 1 `btrfs send [-p <parentPath>] <snap.Path>`: streams the snapshot (or
//     its delta against parentPath) to stdout.
//   - Stage 2 the compressor: defaults to `zstd -c` (the `-c` flag makes zstd read
//     stdin and write the compressed stream to stdout). A configured compressor is
//     taken as a single program token and invoked the same stdin→stdout way with
//     `-c`; codecs whose stdin→stdout switch is not spelled `-c` are out of scope
//     for T3.1 and would be configured against a wrapper.
//   - Stage 3 `rclone rcat <remote>/<objectName>`: reads the compressed stream on
//     stdin and writes it to the remote object. rcat is the streaming upload (it
//     consumes stdin) as opposed to `copy`, which needs a source file.
func archivePipeStages(snap Snapshot, parentPath, remote, compress string) []pipeStage {
	send := []string{"send"}
	if parentPath != "" {
		send = append(send, "-p", parentPath)
	}
	send = append(send, snap.Path)

	prog, compArgs := compressorStage(compress)

	dest := remote + "/" + archiveObjectName(snap)

	return []pipeStage{
		{name: "btrfs", args: send},
		{name: prog, args: compArgs},
		{name: "rclone", args: []string{"rcat", dest}},
	}
}

// compressorStage resolves the compressor program and its stdin→stdout argv. An
// empty or "zstd" compress selects `zstd -c`; any other value is treated as a
// single program token invoked with `-c` as well. Returning (name, args) keeps the
// program name in pipeStage.name so the Runner/mock sees the real binary per stage.
func compressorStage(compress string) (name string, args []string) {
	prog := strings.TrimSpace(compress)
	if prog == "" {
		prog = "zstd"
	}
	return prog, []string{"-c"}
}

// archiveObjectName derives the deterministic remote object name for snap:
// "<sanitized-subvolume>-<snap.ID>.zst". The subvolume is sanitized with the same
// safe-byte rule used for parent-store filenames (sanitize) so a path like
// "/home" cannot introduce extra path separators into the remote key, and the
// snapshot ID disambiguates successive snapshots of the same subvolume. The .zst
// suffix matches the default zstd codec; a different codec would still upload here
// (the suffix is a naming convention, not a content guarantee in T3.1).
func archiveObjectName(snap Snapshot) string {
	return ArchiveObjectName(snap.Subvolume, snap.ID)
}

// rcloneObject is the subset of an `rclone lsjson` array element bentoolkit needs.
// lsjson emits a JSON array of {"Path","Name","Size","ModTime","IsDir",...}; the
// GFS selector only consumes the leaf Name (the remote object key) and ModTime
// (the calendar instant it is bucketed by). Other fields are ignored on decode.
type rcloneObject struct {
	Name    string    `json:"Name"`
	ModTime time.Time `json:"ModTime"`
}

// gfsSelect partitions objects into keep/delete under a grandfather-father-son
// policy. For each granularity with a positive count in policy, objects are
// bucketed by the CALENDAR period of their ModTime (in UTC): hour, day, ISO-week,
// month. Within each bucket the NEWEST object is the representative; the
// representatives of the `count` most-recent buckets are kept. An object kept by
// ANY granularity is retained (the union, so a daily survivor is not dropped just
// because it lost its hourly bucket). It is pure and deterministic — it takes no
// clock, only the objects' own ModTimes — so the keep/delete split is fully
// unit-testable.
//
// If ALL of policy.{Hourly,Daily,Weekly,Monthly} are zero, every object is kept
// (del empty): "no GFS configured" means retain everything, and the caller
// (pruneRemote) skips listing/pruning entirely in that case.
func gfsSelect(objects []rcloneObject, policy Retention) (keep, del []rcloneObject) {
	// No granularity configured → retain everything (del empty). Without this the
	// index-union below would keep nothing and delete all, the opposite of the "no
	// GFS configured" contract. pruneRemote also short-circuits this case before
	// listing, but gfsSelect must be correct on its own as the pure, tested core.
	if policy.Hourly == 0 && policy.Daily == 0 && policy.Weekly == 0 && policy.Monthly == 0 {
		return append([]rcloneObject(nil), objects...), nil
	}

	kept := make(map[int]bool, len(objects)) // indices into objects retained by some granularity

	// bucketBy buckets objects under a key derived from each ModTime (UTC), then
	// keeps the newest object of the `count` most-recent buckets. keyOf must be a
	// comparable derived purely from the instant so buckets are stable.
	bucketBy := func(count int, keyOf func(t time.Time) bucketKey) {
		if count <= 0 {
			return
		}
		// bucket key -> index of the newest object seen in that bucket.
		newest := make(map[bucketKey]int)
		for i, o := range objects {
			k := keyOf(o.ModTime.UTC())
			if cur, ok := newest[k]; !ok || o.ModTime.After(objects[cur].ModTime) {
				newest[k] = i
			}
		}
		// Order the distinct buckets newest-first by their key and keep the first
		// `count`. Keys are constructed to sort chronologically (year, then unit).
		keys := make([]bucketKey, 0, len(newest))
		for k := range newest {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].after(keys[j]) })
		for n, k := range keys {
			if n >= count {
				break
			}
			kept[newest[k]] = true
		}
	}

	bucketBy(policy.Hourly, func(t time.Time) bucketKey {
		return bucketKey{a: t.Year(), b: int(t.Month()), c: t.Day(), d: t.Hour()}
	})
	bucketBy(policy.Daily, func(t time.Time) bucketKey {
		return bucketKey{a: t.Year(), b: int(t.Month()), c: t.Day()}
	})
	bucketBy(policy.Weekly, func(t time.Time) bucketKey {
		iy, iw := t.ISOWeek()
		return bucketKey{a: iy, b: iw}
	})
	bucketBy(policy.Monthly, func(t time.Time) bucketKey {
		return bucketKey{a: t.Year(), b: int(t.Month())}
	})

	for i, o := range objects {
		if kept[i] {
			keep = append(keep, o)
		} else {
			del = append(del, o)
		}
	}
	return keep, del
}

// bucketKey is a chronologically-ordered calendar key for GFS bucketing. The
// fields are filled most-significant-first (e.g. year, month, day, hour) and zero
// for unused positions, so `after` gives a total order matching real time without
// allocating a time.Time per bucket. Comparable, so it is a valid map key.
type bucketKey struct{ a, b, c, d int }

// after reports whether k is chronologically later than other under the
// most-significant-first field ordering.
func (k bucketKey) after(other bucketKey) bool {
	switch {
	case k.a != other.a:
		return k.a > other.a
	case k.b != other.b:
		return k.b > other.b
	case k.c != other.c:
		return k.c > other.c
	default:
		return k.d > other.d
	}
}

// pruneRemote applies the GFS retention policy to the rclone remote after a
// successful ship (R4.1). It lists the remote with `rclone lsjson`, runs the pure
// gfsSelect, and deletefiles each out-of-policy object — EXCEPT the active parent
// (R4.2): archiveObjectName(snap) is the object just uploaded and the base the
// next incremental send will reference with `-p`, so it is never deleted even
// when GFS would drop it. When retention is all-zero there is no policy, so it
// returns immediately without listing (matches restic skipping forget when
// retention is unconfigured).
//
// Non-fatal by contract: every failure here is reported via warnLogf and
// swallowed, never returned, because Send has already succeeded and recorded the
// new head by the time this runs (see the call site in Send). A failed prune only
// leaves stale objects for the next run to reconsider.
//
// NOTE (R-incremental-chain, HIGH risk): for mode="incremental" the remote
// objects form a delta chain, and GFS deleting a MID-CHAIN delta would break
// restorability of every later snapshot that depends on it. The active-parent
// guard only protects the CURRENT head, not arbitrary interior deltas, so GFS is
// NOT chain-aware here. This is a documented known risk for T5.1, not fixed in it:
// restore-time chain validation (T6) is the backstop that detects a missing base.
// GFS is fully safe for mode="full", where each object is self-contained.
func (a *archiveShipper) pruneRemote(ctx context.Context, snap Snapshot) {
	if a.retention.Hourly == 0 && a.retention.Daily == 0 &&
		a.retention.Weekly == 0 && a.retention.Monthly == 0 {
		return // no GFS policy configured → keep everything, skip listing entirely.
	}

	out, err := a.run.Run(ctx, "rclone", []string{"lsjson", a.remote}, nil)
	if err != nil {
		warnLogf("snapshot: ship %q: rclone lsjson %q failed; skipping prune: %v", a.Name(), a.remote, err)
		return
	}

	var objs []rcloneObject
	if err := json.Unmarshal(out, &objs); err != nil {
		warnLogf("snapshot: ship %q: parsing rclone lsjson output failed; skipping prune: %v", a.Name(), err)
		return
	}

	_, del := gfsSelect(objs, a.retention)

	active := archiveObjectName(snap) // the object just uploaded — never delete (R4.2).
	for _, d := range del {
		if d.Name == active {
			continue // R4.2: the active parent is the next incremental base; spare it.
		}
		if _, err := a.run.Run(ctx, "rclone", []string{"deletefile", a.remote + "/" + d.Name}, nil); err != nil {
			warnLogf("snapshot: ship %q: rclone deletefile %q failed: %v", a.Name(), d.Name, err)
		}
	}
}

// runPipe runs stages sequentially through run, feeding each stage's stdout as the
// next stage's stdin, and returns the final stage's stdout. Any stage error fails
// the whole pipe immediately (R2.3); because every stage shares the single ctx,
// cancelling it kills the pipe (the Runner binds each child to ctx, R7.2).
//
// NOTE (R-archive-memory): this buffers each stage's FULL output in memory because
// the 004 Runner returns []byte. For a multi-GB `btrfs send` stream that is a real
// memory cost. A true streaming pipe (io.Pipe between exec.Cmds) is FUTURE WORK
// gated behind *_live_test.go; it does not change the mock-tested correctness here
// (argv wiring, stage-failure-fails-ship, ctx-cancel), which this buffered form
// already satisfies.
func runPipe(ctx context.Context, run Runner, stages []pipeStage) ([]byte, error) {
	var prevOut []byte
	for _, stage := range stages {
		out, err := run.Run(ctx, stage.name, stage.args, prevOut)
		if err != nil {
			return nil, fmt.Errorf("archive pipe stage %q: %w", stage.name, err)
		}
		prevOut = out
	}
	return prevOut, nil
}

// newArchiveShipper assembles an archiveShipper from cfg, the subprocess seam, and
// the engine retention policy. mode defaults to "incremental" when unset (selection
// logic in T3.2); the parent store is wired now and consumed in T3.2. retention is
// the [engine.retention] GFS policy threaded in for the post-ship remote prune
// (T5.1, R4) — an all-zero policy makes pruneRemote a no-op.
func newArchiveShipper(cfg ShipConfig, run Runner, retention Retention) *archiveShipper {
	mode := cfg.Mode
	if mode == "" {
		mode = "incremental"
	}
	return &archiveShipper{
		name:      cfg.Name,
		remote:    cfg.Remote,
		mode:      mode,
		compress:  cfg.Compress,
		run:       run,
		parents:   newParentStore(),
		retention: retention,
	}
}

// Compile-time assertion that archiveShipper satisfies Shipper.
var _ Shipper = (*archiveShipper)(nil)
