package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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
//     consumes stdin) as opposed to `copy`, which needs a source file. objectName
//     carries the per-subvolume prefix DIRECTORY (R3.1) and still needs no mkdir
//     stage: rcat creates the parent on its own (verified against rclone 1.75.0).
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

// archiveObjectName derives the deterministic remote object KEY for snap by
// delegating to ArchiveObjectName: "<sanitize(snap.Subvolume)>/<snap.ID>.zst".
// The subvolume is a DIRECTORY under the remote, not part of the filename
// (R3.1), so a listing can be scoped to one subvolume by URL. Delegating keeps
// this ship-side helper and its exported twin on ONE convention, which is what
// guarantees the restore reads back exactly the key the shipper wrote (R5.2).
//
// Why exactly ONE separator is safe when arbitrary ones are not: sanitize
// replaces every byte outside [A-Za-z0-9._-] with '-', so it can NEVER emit
// '/'. The '/' ArchiveObjectName inserts is therefore the only one in the key,
// which makes the prefix/leaf boundary unique — no sanitized subvolume can
// contain it and no snapshot ID can fake it. Handing the RAW subvolume to the
// remote would forfeit that: "/home/otaku" would scatter the key across a
// directory tree whose shape bentoolkit no longer controls. The old flat scheme
// packed both halves into a single filename joined by '-', a byte sanitize CAN
// emit, so the boundary was indistinguishable from sanitized content: subvolume
// "/home" with ID "otaku-42" and subvolume "/home/otaku" with ID "42" both
// rendered "-home-otaku-42.zst" — one key for two subvolumes.
//
// The .zst suffix matches the default zstd codec; a different codec would still
// upload here (the suffix is a naming convention, not a content guarantee).
func archiveObjectName(snap Snapshot) string {
	return ArchiveObjectName(snap.Subvolume, snap.ID)
}

// rcloneObject is the subset of an `rclone lsjson` array element bentoolkit needs.
// lsjson emits a JSON array of {"Path","Name","Size","ModTime","IsDir",...}; the
// GFS selector only consumes the leaf Name (the remote object key) and ModTime
// (the calendar instant it is bucketed by), and IsDir is decoded purely to REJECT
// the entry (see below). Every other field is ignored on decode.
type rcloneObject struct {
	Name    string    `json:"Name"`
	ModTime time.Time `json:"ModTime"`

	// IsDir marks a DIRECTORY entry, which is never a prune candidate: `rclone
	// deletefile` takes a file, so handing it a directory asks for something
	// bentoolkit cannot mean (R4.3). decodeLsjson drops these entries.
	//
	// This field looks like dead weight and is not — do NOT delete it as such.
	// A listing scoped to one subvolume's prefix contains no directories BY
	// CONSTRUCTION, so the prune paths as they stand today never see one. But
	// this story gives every subvolume its own DIRECTORY under the remote root
	// (ArchivePrefix), so a caller that listed the remote ROOT would get one
	// entry per subvolume. Without this field the struct cannot even express the
	// difference: such entries decode as ordinary objects, gfsSelect buckets them
	// by ModTime like anything else, and the losers go straight to deletefile.
	IsDir bool `json:"IsDir"`
}

// decodeLsjson decodes `rclone lsjson` output and drops every directory entry
// (R4.3). BOTH prune paths decode through here so neither can carry a copy of
// the filter that the other forgets — the exact failure mode this story exists
// to fix — and so a third caller inherits the guard for free.
//
// The drop happens BEFORE gfsSelect rather than before deletefile on purpose:
// a directory that reached the selector would still occupy a calendar bucket and,
// being newer, could win the bucket's single representative slot and evict the
// real object that belongs there. Filtering only at the delete site would spare
// the directory and delete that object instead.
//
// The unmarshal error is returned unwrapped: each caller adds the remote it was
// listing and applies its own fatality contract (pruneRemote warns and skips the
// prune, PruneRemoteOnDemand returns a failed stage).
func decodeLsjson(out []byte) ([]rcloneObject, error) {
	var decoded []rcloneObject
	if err := json.Unmarshal(out, &decoded); err != nil {
		return nil, err
	}
	// Filter in place: the decoded backing array has no other referent.
	objs := decoded[:0]
	for _, o := range decoded {
		if o.IsDir {
			continue
		}
		objs = append(objs, o)
	}
	return objs, nil
}

// rclone's "the path is not there" signature, measured against rclone 1.75.0:
// `lsjson` on a non-existent path exits 3, writes "directory not found" to
// stderr, and prints a bare "[" on stdout — invalid JSON, which is why callers
// must test for this BEFORE handing the output to decodeLsjson.
const (
	// rcloneExitDirNotFound is rclone's documented exit code for a missing
	// directory. It is the precise signal: only this condition produces it.
	rcloneExitDirNotFound = 3
	// rcloneDirNotFoundText is the message rclone writes to stderr for the same
	// condition. runnerEnv pins LC_ALL=C on every child, so it is not localized.
	rcloneDirNotFoundText = "directory not found"
)

// isRemoteDirNotFound reports whether err is rclone's "the path is not there"
// rather than a real failure.
//
// Why this is benign at all: it never happens after a ship (the object was just
// written, so its directory exists). It happens on a MANUAL prune of a remote or
// a subvolume prefix that was never shipped — the ordinary first-run state.
// Treating that as a failure would make `snapshot prune` fail on a correctly
// configured, freshly installed system (038 R4.2).
//
// It tests two things, and BOTH are load-bearing:
//
//   - an *exec.ExitError with code rcloneExitDirNotFound — the PRECISE signal,
//     rclone's own documented code, reached through the errors.Join that
//     execRunner.Run builds (errors.As walks a Join's tree, so the joined stderr
//     alongside it does not hide the ExitError);
//   - the text rcloneDirNotFoundText anywhere in the error. This is not
//     belt-and-braces padding. The Runner joins the child's stderr onto the error
//     and pins LC_ALL=C (runnerEnv) precisely so that output is stable, and a
//     Runner MOCK cannot realistically construct an *exec.ExitError — without the
//     text branch the benign path would be untestable through the seam every
//     other prune test uses.
//
// Trade-off, deliberately accepted and recorded here so a later reader does not
// "tighten" it away unaware: an UNRELATED failure whose text happens to contain
// that phrase is read as "nothing to prune". The worst outcome is that one
// subvolume goes unpruned this run — stale objects linger and are reconsidered
// next run; nothing is ever deleted because of it. The failure mode of the
// alternative (exit code only) is the opposite kind of bug: a failed command on
// a healthy system, every first run, for every subvolume not yet shipped.
func isRemoteDirNotFound(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == rcloneExitDirNotFound {
		return true
	}
	return strings.Contains(err.Error(), rcloneDirNotFoundText)
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

// pruneRemote applies the GFS retention policy after a successful ship (R4.1),
// scoped to the shipped subvolume. It lists ONE prefix directory with `rclone
// lsjson <remote>/<ArchivePrefix(snap.Subvolume)>`, runs the pure gfsSelect over
// exactly what that listing returned, and deletefiles each out-of-policy object
// back under the same prefix. When retention is all-zero there is no policy, so
// it returns immediately without listing (matches restic skipping forget when
// retention is unconfigured).
//
// R4.2 — protection here is STRUCTURAL, not a check, and that is the point.
// Other subvolumes' recorded lineage heads are safe because they are not in the
// listing at all (R1.1, R1.3, R2.1), not because anything spares them. The old
// whole-remote listing is what let a /root ship delete /home's head: one flat
// namespace put every subvolume's objects in the same calendar bucket, a bucket
// keeps a single representative, and the losers were deletefiled — silently,
// since the delete succeeded and the damage only surfaced at restore (038).
//
// Do NOT "restore" a multi-subvolume head guard on this path. It would have no
// job — the objects it would spare are not candidates — and its presence would
// re-assert that safety depends on someone remembering to keep a comparison
// correct, which is the property this path was changed to stop depending on.
// PruneRemoteOnDemand is the path that legitimately protects many heads, because
// a user-invoked prune deliberately spans every configured subvolume.
//
// The one head IN scope is this subvolume's own, and its guard compares LEAVES:
// a scoped `rclone lsjson` reports each entry's Name relative to the LISTED path,
// so the comparable form of the object just uploaded is ArchiveObjectLeaf(snap.ID),
// never the full key ArchiveObjectName. A full key compared against a scoped
// listing matches nothing and does so silently — a guard indistinguishable from a
// working one that protects nothing. By the time this runs, Send has already
// recorded snap as the new head (and returns early if recording fails), so
// sparing snap's object IS sparing the recorded head that the next incremental
// send will reference with `-p`.
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
// NOT chain-aware here. Scoping the prune per subvolume does not change that: the
// chain lives WITHIN one subvolume, so its interior deltas are exactly the
// objects still in the listing. This remains a documented known risk for T5.1,
// not fixed here: restore-time chain validation (T6) is the backstop that detects
// a missing base. GFS is fully safe for mode="full", where each object is
// self-contained.
func (a *archiveShipper) pruneRemote(ctx context.Context, snap Snapshot) {
	if a.retention.Hourly == 0 && a.retention.Daily == 0 &&
		a.retention.Weekly == 0 && a.retention.Monthly == 0 {
		return // no GFS policy configured → keep everything, skip listing entirely.
	}

	// Every rclone call below is scoped to this snapshot's subvolume directory.
	// Listing it is what makes the prune per-subvolume (R1.1); the delete re-joins
	// the SAME string because a scoped listing yields bare leaves (R1.3).
	prefixPath := a.remote + "/" + ArchivePrefix(snap.Subvolume)

	out, err := a.run.Run(ctx, "rclone", []string{"lsjson", prefixPath}, nil)
	if err != nil {
		// Defensive only: this runs after a successful upload, so the prefix
		// directory necessarily exists. Should rclone still report the path as
		// missing, there is genuinely nothing to prune — a silent no-op, not a
		// warn, because no housekeeping was skipped (038 R4.2).
		if isRemoteDirNotFound(err) {
			return
		}
		warnLogf("snapshot: ship %q: rclone lsjson %q failed; skipping prune: %v", a.Name(), prefixPath, err)
		return
	}

	objs, err := decodeLsjson(out)
	if err != nil {
		warnLogf("snapshot: ship %q: parsing rclone lsjson output for %q failed; skipping prune: %v", a.Name(), prefixPath, err)
		return
	}

	_, del := gfsSelect(objs, a.retention)

	// The listing is scoped, so its entries are LEAVES — compare the active parent
	// as a leaf too (R4.2). See the doc comment: a full key would never match.
	active := ArchiveObjectLeaf(snap.ID)
	for _, d := range del {
		if d.Name == active {
			continue // R4.2: the active parent is the next incremental base; spare it.
		}
		target := prefixPath + "/" + d.Name // re-join the prefix the listing stripped.
		if _, err := a.run.Run(ctx, "rclone", []string{"deletefile", target}, nil); err != nil {
			warnLogf("snapshot: ship %q: rclone deletefile %q failed: %v", a.Name(), target, err)
		}
	}
}

// PruneRemoteOnDemand applies the GFS retention policy to the rclone remote for
// a user-invoked `snapshot prune` (008 R3.1). The mechanics mirror the post-ship
// pruneRemote — the all-zero-retention short-circuit, `rclone lsjson`, the pure
// gfsSelect, `rclone deletefile` per out-of-policy object — with two deliberate
// differences:
//
//   - The active-parent guard comes from the parent store, not a just-uploaded
//     snapshot: every recorded lineage head for this ship across the configured
//     subvolumes is protected (R4.2), because the remote holds objects from all
//     subvolumes and deleting any recorded head would break that subvolume's
//     next incremental `-p` reference. A subvolume with no recorded parent (the
//     normal first-run state) simply contributes nothing to protect; a store
//     READ error aborts before any deletion.
//   - Failures are RETURNED, never swallowed: the post-ship prune is best-effort
//     housekeeping after a successful backup, but a manual prune is the user's
//     primary action, so a lsjson/parse/deletefile error must surface as a
//     failed stage in the RunResult (Manager.Prune). deletefile errors are
//     accumulated with errors.Join so one bad object does not block pruning the
//     rest.
func (a *archiveShipper) PruneRemoteOnDemand(ctx context.Context, subvolumes []string) error {
	if a.retention.Hourly == 0 && a.retention.Daily == 0 &&
		a.retention.Weekly == 0 && a.retention.Monthly == 0 {
		return nil // no GFS policy configured → keep everything, skip listing entirely.
	}

	// Collect the objects to spare: each subvolume's recorded active parent for
	// THIS ship is the base its next incremental send references with `-p` (R4.2).
	protected := make(map[string]bool, len(subvolumes))
	for _, sv := range subvolumes {
		parent, ok, err := a.parents.Last(sv, a.Name())
		if err != nil {
			return err
		}
		if ok {
			protected[archiveObjectName(parent)] = true
		}
	}

	out, err := a.run.Run(ctx, "rclone", []string{"lsjson", a.remote}, nil)
	if err != nil {
		// A path that is not there is NOT a failure: it is the ordinary state of
		// a remote nothing has been shipped to yet, and failing here would make
		// `snapshot prune` fail on a freshly installed, correctly configured
		// system (038 R4.2). There is nothing to prune, so return success without
		// deleting anything. This test comes BEFORE decodeLsjson deliberately:
		// rclone prints a bare "[" on stdout in this case, which does not parse.
		//
		// Every OTHER failure is still returned, unchanged: a manual prune is the
		// user's primary action and must surface as a failed stage.
		if isRemoteDirNotFound(err) {
			return nil
		}
		return fmt.Errorf("rclone lsjson %s: %w", a.remote, err)
	}
	objs, err := decodeLsjson(out)
	if err != nil {
		return fmt.Errorf("parse rclone lsjson output for %s: %w", a.remote, err)
	}

	_, del := gfsSelect(objs, a.retention)

	var errs []error
	for _, d := range del {
		if protected[d.Name] {
			continue // R4.2: an active parent is a future incremental base; spare it.
		}
		if _, err := a.run.Run(ctx, "rclone", []string{"deletefile", a.remote + "/" + d.Name}, nil); err != nil {
			errs = append(errs, fmt.Errorf("rclone deletefile %s: %w", d.Name, err))
		}
	}
	return errors.Join(errs...)
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
