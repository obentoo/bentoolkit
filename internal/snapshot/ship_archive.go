package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// archiveShipper streams a btrfs snapshot to an rclone remote as a single
// compressed object. The pipeline is
// `btrfs send [-p parent] <snap> | <compressor> | rclone rcat <remote>/<obj>`,
// run end-to-end under one cancellable ctx so that cancelling the parent kills
// every stage and any stage's non-zero exit fails the whole ship.
// Unlike the ssh shipper, bentoolkit moves the bytes itself here, so Send is not
// delegated. All subprocesses go through run.
//
// mode selects a full or incremental send, parents supplies the `-p` parent, and
// retention drives the GFS prune of the remote after a successful ship.
type archiveShipper struct {
	name      string
	remote    string       // rclone remote+path prefix, e.g. "gdrive:bentoo-backups"
	mode      string       // "incremental" (default) | "full"
	compress  string       // compressor; default "zstd"
	run       Runner       // subprocess seam
	parents   parentStore  // incremental parent selection
	retention Retention    // GFS policy applied to the remote after a successful ship
	log       *slog.Logger // nil discards; set by newShipper
}

// logger returns the shipper's logger, or a discarding one for a zero value.
func (a *archiveShipper) logger() *slog.Logger { return logging.OrDiscard(a.log) }

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
// head on success. The bytes move here, so Send is not delegated (Delegated=false).
//
// Mode selection:
//   - mode=="full": always a full send (parentPath==""); no parent lookup, no warn.
//   - otherwise (incremental, the default): a.parents.Last gives the parent, and
//     its on-disk Path (btrfs `-p` takes a PATH, not an ID) drives the send. A
//     store-read error is surfaced, NOT swallowed into a silent full send. With no
//     recorded parent (the first run) it sends full AND warns, never silently.
//
// The new parent is recorded ONLY after the pipe succeeds, so the lineage head
// never advances to a snapshot whose object was never uploaded (the next `-p`
// would reference a missing base). A recording failure after a successful upload
// is surfaced, and that complete object stays under its key. A failed pipe may
// leave a truncated object, so Send deletes it.
func (a *archiveShipper) Send(ctx context.Context, snap Snapshot) (ShipReport, error) {
	if snap.Path == "" || snap.ID == "" {
		return ShipReport{}, fmt.Errorf("ship %q subvolume %q: %w", a.Name(), snap.Subvolume, ErrSnapshotUnidentified)
	}
	var parentPath string
	if a.mode != "full" {
		parent, ok, err := a.parents.Last(snap.Subvolume, a.Name())
		if err != nil {
			return ShipReport{}, err
		}
		if ok {
			parentPath = parent.Path
		} else {
			a.logger().Warn("snapshot: ship has no recorded parent for the subvolume; sending full",
				"ship", a.Name(), "subvolume", snap.Subvolume)
		}
	}

	stages := archivePipeStages(snap, parentPath, a.remote, a.compress)
	if _, err := runPipe(ctx, a.run, stages); err != nil {
		// A streamed pipe can let rclone rcat finish a truncated upload after
		// btrfs send dies, so the object under this snapshot's key is removed,
		// best-effort. The pipe error stays the returned error.
		// The deletion outlives a cancelled Send, bounded by
		// archiveDeleteTimeout so an unreachable remote cannot hold the error.
		dest := archiveDest(a.remote, snap)
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), archiveDeleteTimeout)
		defer cancel()
		if _, derr := a.run.Run(dctx, "rclone", []string{"deletefile", dest}, nil); derr != nil {
			a.logger().Warn("snapshot: ship failed removing a possibly truncated object",
				"ship", a.Name(), "object", dest, "err", derr)
		}
		return ShipReport{}, err
	}

	// Record THIS snapshot as the new lineage head — only now that the ship
	// succeeded. Surface a record failure: the upload is up but the
	// bookkeeping broke, and the operator must know (the complete object stays
	// under its key).
	if err := a.parents.Record(snap.Subvolume, a.Name(), snap); err != nil {
		return ShipReport{}, err
	}

	// Prune the remote AFTER the ship succeeded and the new head is recorded.
	// Ordering and non-fatality are deliberate: pruning is post-success
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

// PipeStage is one command in the archive pipe: a program name and its argv. It is
// the unit the pure builder emits and the executor feeds through the Runner.
type PipeStage struct {
	Name string
	Args []string
}

// archivePipeStages builds the three-stage archive pipe for snap as pure data, so
// the argv/wiring is unit-testable without touching btrfs or rclone. When
// parentPath=="" stage 1 is a FULL send (no `-p`); a non-empty parentPath emits
// `btrfs send -p <parentPath> <snap.Path>`, the incremental form.
//
//   - Stage 1 `btrfs send [-p <parentPath>] <snap.Path>`: streams the snapshot (or
//     its delta against parentPath) to stdout.
//   - Stage 2 the compressor: defaults to `zstd -c` (the `-c` flag makes zstd read
//     stdin and write the compressed stream to stdout). A configured compressor is
//     taken as a single program token and invoked the same stdin→stdout way with
//     `-c`; codecs whose stdin→stdout switch is not spelled `-c` are not supported
//     directly and would be configured against a wrapper.
//   - Stage 3 `rclone rcat <remote>/<objectName>`: reads the compressed stream on
//     stdin and writes it to the remote object. rcat is the streaming upload (it
//     consumes stdin) as opposed to `copy`, which needs a source file. objectName
//     carries the per-subvolume prefix DIRECTORY and still needs no mkdir
//     stage: rcat creates the parent on its own (verified against rclone 1.75.0).
func archivePipeStages(snap Snapshot, parentPath, remote, compress string) []PipeStage {
	send := []string{"send"}
	if parentPath != "" {
		send = append(send, "-p", parentPath)
	}
	send = append(send, snap.Path)

	prog, compArgs := compressorStage(compress)

	dest := archiveDest(remote, snap)

	return []PipeStage{
		{Name: "btrfs", Args: send},
		{Name: prog, Args: compArgs},
		{Name: "rclone", Args: []string{"rcat", dest}},
	}
}

// archiveDest is the rclone destination of snap's archive object: the rcat
// target of archivePipeStages and the object a failed ship deletes.
func archiveDest(remote string, snap Snapshot) string {
	return remote + "/" + archiveObjectName(snap)
}

// archiveDeleteTimeout bounds the best-effort `rclone deletefile` that follows
// a failed archive pipe. It is a var only so tests can shrink it.
var archiveDeleteTimeout = 30 * time.Second

// compressorStage resolves the compressor program and its stdin→stdout argv. An
// empty or "zstd" compress selects `zstd -c`; any other value is treated as a
// single program token invoked with `-c` as well. Returning (name, args) keeps the
// program name in PipeStage.Name so the Runner/mock sees the real binary per stage.
func compressorStage(compress string) (name string, args []string) {
	prog := strings.TrimSpace(compress)
	if prog == "" {
		prog = "zstd"
	}
	return prog, []string{"-c"}
}

// archiveObjectName derives the deterministic remote object KEY for snap by
// delegating to ArchiveObjectName: "<sanitize(snap.Subvolume)>/<snap.ID>.zst".
// The subvolume is a DIRECTORY under the remote, not part of the filename, so a
// listing can be scoped to one subvolume by URL. Delegating keeps this ship-side
// helper and its exported twin on ONE convention, which guarantees the restore
// reads back exactly the key the shipper wrote.
//
// Exactly ONE separator is safe because sanitize can NEVER emit '/': the '/'
// ArchiveObjectName inserts is the only one in the key, so no sanitized
// subvolume or snapshot ID can fake the prefix/leaf boundary. The RAW subvolume
// would scatter the key across a directory tree bentoolkit does not control,
// and a '-' separator (which sanitize CAN emit) rendered subvolume "/home" with
// ID "otaku-42" and "/home/otaku" with ID "42" as one key, "-home-otaku-42.zst".
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
	// bentoolkit cannot mean. decodeLsjson drops these entries.
	//
	// This field looks like dead weight and is not — do NOT delete it as such.
	// A listing scoped to one subvolume's prefix contains no directories BY
	// CONSTRUCTION, so the prune paths as they stand today never see one. But
	// every subvolume has its own DIRECTORY under the remote root
	// (ArchivePrefix), so a caller that listed the remote ROOT would get one
	// entry per subvolume. Without this field the struct cannot even express the
	// difference: such entries decode as ordinary objects, gfsSelect buckets them
	// by ModTime like anything else, and the losers go straight to deletefile.
	IsDir bool `json:"IsDir"`
}

// decodeLsjson decodes `rclone lsjson` output and drops every directory entry
// BOTH prune paths decode through here so neither can carry a copy of the
// filter that the other forgets, and so a third caller inherits the guard for
// free.
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
// rather than a real failure. It is benign: it never happens after a ship (the
// object was just written), only on a MANUAL prune of a remote or subvolume
// prefix never shipped — the ordinary first-run state, which must not fail
// `snapshot prune` on a freshly installed system.
//
// It tests two things, and BOTH are load-bearing:
//
//   - an *exec.ExitError with code rcloneExitDirNotFound — the PRECISE signal,
//     reached through execRunner.Run's errors.Join (errors.As walks its tree);
//   - the text rcloneDirNotFoundText anywhere in the error. The Runner joins the
//     child's stderr and pins LC_ALL=C (runnerEnv) so that text is stable, and a
//     Runner MOCK cannot construct an *exec.ExitError: without this branch the
//     benign path would be untestable through the seam the prune tests use.
//
// Accepted trade-off — do not "tighten" it away: an UNRELATED failure whose
// text contains that phrase reads as "nothing to prune", so one subvolume goes
// unpruned this run and nothing is deleted. Exit code alone would instead fail
// every first run for every subvolume not yet shipped.
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

// pruneRemote applies the GFS retention policy after a successful ship, scoped
// to the shipped subvolume: it lists ONE prefix directory with `rclone lsjson
// <remote>/<ArchivePrefix(snap.Subvolume)>`, runs gfsSelect over that listing,
// and deletefiles each out-of-policy object under the same prefix. An all-zero
// retention returns at once without listing.
//
// Other subvolumes' heads are protected STRUCTURALLY: they are not in the
// listing. The old whole-remote listing let a /root ship silently delete /home's
// head, since a calendar bucket keeps one representative. Do NOT add a
// multi-subvolume head guard here; PruneRemoteOnDemand spans every subvolume.
// This subvolume's own head — snap, already recorded by Send — is spared as a
// LEAF, ArchiveObjectLeaf(snap.ID): a scoped listing reports names relative to
// the listed path, so a full key would silently match nothing.
//
// Non-fatal: every failure is warned and swallowed, because Send has already
// succeeded. Known risk: in mode="incremental" GFS is not chain-aware and may
// delete a MID-CHAIN delta that later snapshots depend on. Nothing catches that
// before a restore: RestoreChainFor returns a single full link today, so the
// backstop is `btrfs receive` failing. mode="full" objects are self-contained.
func (a *archiveShipper) pruneRemote(ctx context.Context, snap Snapshot) {
	if a.retention.Hourly == 0 && a.retention.Daily == 0 &&
		a.retention.Weekly == 0 && a.retention.Monthly == 0 {
		return // no GFS policy configured → keep everything, skip listing entirely.
	}

	// Every rclone call below is scoped to this snapshot's subvolume directory.
	// Listing it is what makes the prune per-subvolume; the delete re-joins
	// the SAME string because a scoped listing yields bare leaves.
	prefixPath := a.remote + "/" + ArchivePrefix(snap.Subvolume)

	out, err := a.run.Run(ctx, "rclone", []string{"lsjson", prefixPath}, nil)
	if err != nil {
		// Defensive only: this runs after a successful upload, so the prefix
		// directory necessarily exists. Should rclone still report the path as
		// missing, there is genuinely nothing to prune — a silent no-op, not a
		// warn, because no housekeeping was skipped.
		if isRemoteDirNotFound(err) {
			return
		}
		a.logger().Warn("snapshot: ship rclone lsjson failed; skipping prune",
			"ship", a.Name(), "path", prefixPath, "err", err)
		return
	}

	objs, err := decodeLsjson(out)
	if err != nil {
		a.logger().Warn("snapshot: ship parsing rclone lsjson output failed; skipping prune",
			"ship", a.Name(), "path", prefixPath, "err", err)
		return
	}

	_, del := gfsSelect(objs, a.retention)

	// The listing is scoped, so its entries are LEAVES — compare the active parent
	// as a leaf too. See the doc comment: a full key would never match.
	active := ArchiveObjectLeaf(snap.ID)
	for _, d := range del {
		if d.Name == active {
			continue // the active parent is the next incremental base; spare it.
		}
		target := prefixPath + "/" + d.Name // re-join the prefix the listing stripped.
		if _, err := a.run.Run(ctx, "rclone", []string{"deletefile", target}, nil); err != nil {
			a.logger().Warn("snapshot: ship rclone deletefile failed",
				"ship", a.Name(), "target", target, "err", err)
		}
	}
}

// PruneRemoteOnDemand applies the GFS retention policy to the rclone remote for
// a user-invoked `snapshot prune`, ONE configured subvolume at a time. Each is
// listed, selected and deleted inside its own prefix `<remote>/<ArchivePrefix(sv)>`,
// so one subvolume's retention never reaches another's objects, and an object
// under no configured prefix is never a candidate.
//
// The loop is TWO-PHASE and must stay so: phase one reads EVERY recorded head
// and returns on the first parent-store error; only then may phase two delete.
// Folded into one loop, a READ failure on a later subvolume would surface after
// the earlier ones were already pruned — a partial mutation of the remote.
//
// Unlike pruneRemote it protects MANY heads, each compared as a LEAF
// (ArchiveObjectLeaf) inside its OWN subvolume's listing; no full key is kept,
// since one would silently match nothing. A subvolume with no recorded head
// protects nothing. A missing prefix is warned naming the subvolume, not failed.
// Other failures are RETURNED as a failed stage, since a manual prune is the
// user's primary action; phase-two errors accumulate with errors.Join so one bad
// object or prefix does not block the independent rest.
func (a *archiveShipper) PruneRemoteOnDemand(ctx context.Context, subvolumes []string) error {
	if a.retention.Hourly == 0 && a.retention.Daily == 0 &&
		a.retention.Weekly == 0 && a.retention.Monthly == 0 {
		return nil // no GFS policy configured → keep everything, skip listing entirely.
	}

	// scoped is one subvolume's prune plan: the single remote path it may touch
	// and the single object it must spare there. protectedLeaf is a LEAF because
	// that is the form a scoped listing reports; no full key is kept alongside it,
	// so the silent key-vs-leaf mismatch cannot be written by accident.
	type scoped struct {
		subvolume     string // named in the missing-prefix warning
		prefixPath    string // "<remote>/<ArchivePrefix(subvolume)>"
		protectedLeaf string // this subvolume's recorded head as a leaf; "" when none
	}

	// PHASE ONE — read every recorded head BEFORE deleting anything (see the
	// ordering requirement above). A store-read error returns from here, with
	// nothing deleted yet, whichever subvolume it came from.
	plans := make([]scoped, 0, len(subvolumes))
	for _, sv := range subvolumes {
		parent, ok, err := a.parents.Last(sv, a.Name())
		if err != nil {
			return fmt.Errorf("reading the recorded lineage head of %s: %w", sv, err)
		}
		p := scoped{subvolume: sv, prefixPath: a.remote + "/" + ArchivePrefix(sv)}
		if ok {
			p.protectedLeaf = ArchiveObjectLeaf(parent.ID)
		}
		plans = append(plans, p)
	}

	// PHASE TWO — every head is known, so deleting is now safe to start.
	var errs []error
	for _, p := range plans {
		out, err := a.run.Run(ctx, "rclone", []string{"lsjson", p.prefixPath}, nil)
		if err != nil {
			// A path that is not there is NOT a failure: it is the ordinary state
			// of a subvolume nothing has been shipped for yet, and failing here
			// would make `snapshot prune` fail on a freshly installed, correctly
			// configured system. There is nothing to prune for this
			// subvolume — name it and continue with the rest. This test comes
			// BEFORE decodeLsjson deliberately: rclone prints a bare "[" on stdout
			// in this case, which does not parse.
			if isRemoteDirNotFound(err) {
				a.logger().Warn("snapshot: ship has nothing for the subvolume at its remote prefix yet; skipping its prune",
					"ship", a.Name(), "subvolume", p.subvolume, "path", p.prefixPath)
				continue
			}
			// Every OTHER failure still surfaces as a failed stage.
			errs = append(errs, fmt.Errorf("rclone lsjson %s: %w", p.prefixPath, err))
			continue
		}
		objs, err := decodeLsjson(out)
		if err != nil {
			errs = append(errs, fmt.Errorf("parse rclone lsjson output for %s: %w", p.prefixPath, err))
			continue
		}

		_, del := gfsSelect(objs, a.retention)
		for _, d := range del {
			// The listing is scoped, so its entries are LEAVES; the only head that
			// can appear in it is this subvolume's own. An empty protectedLeaf means
			// no recorded head, which protects nothing.
			if p.protectedLeaf != "" && d.Name == p.protectedLeaf {
				continue // a recorded head is the next incremental base; spare it.
			}
			target := p.prefixPath + "/" + d.Name // re-join the prefix the listing stripped.
			if _, err := a.run.Run(ctx, "rclone", []string{"deletefile", target}, nil); err != nil {
				errs = append(errs, fmt.Errorf("rclone deletefile %s: %w", target, err))
			}
		}
	}
	return errors.Join(errs...)
}

// runPipe runs stages as one streaming pipe through run's Piper seam and returns
// the final stage's stdout. Any stage error fails the whole pipe, and
// cancelling ctx kills every stage.
//
// A Runner without the seam is refused rather than driven stage by stage: the
// buffered chain it would need holds each stage's whole output in memory, about
// twice a multi-GB `btrfs send` stream at peak.
func runPipe(ctx context.Context, run Runner, stages []PipeStage) ([]byte, error) {
	p, ok := run.(Piper)
	if !ok {
		return nil, fmt.Errorf("runner %T cannot stream the archive pipe", run)
	}
	return p.Pipe(ctx, stages)
}

// runStagesBuffered runs stages one after another through run, feeding each
// stage's whole stdout to the next as stdin, and returns the last stage's
// stdout. A stage starts only after every earlier one succeeded, which is what
// restoreArchive needs and what the streaming runPipe cannot give; the cost is
// that each stage's output is held in memory.
func runStagesBuffered(ctx context.Context, run Runner, stages []PipeStage) ([]byte, error) {
	var prev []byte
	for _, st := range stages {
		out, err := run.Run(ctx, st.Name, st.Args, prev)
		if err != nil {
			return nil, fmt.Errorf("archive pipe stage %q: %w", st.Name, err)
		}
		prev = out
	}
	return prev, nil
}

// newArchiveShipper assembles an archiveShipper from cfg, the subprocess seam, and
// the engine retention policy. mode defaults to "incremental" when unset; the
// parent store supplies the incremental parent. retention is the
// [engine.retention] GFS policy threaded in for the post-ship remote prune
// — an all-zero policy makes pruneRemote a no-op.
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
