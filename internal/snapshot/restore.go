package snapshot

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// restore.go is the snapshot RESTORE entry point (T6.1, design §5 / R5). Restore
// dispatches by driver: an "archive" restore validates the full→target delta chain
// and then replays it through `rclone cat | <decompress> | btrfs receive` (R5.2);
// a "restic" restore runs `restic restore --target` and can pull back a single
// file/subdir granularly (R5.3). Every restore is DESTRUCTIVE (it writes a
// subvolume into the target), so it is gated behind an operator confirmation
// unless --yes is given (R5.4). All subprocesses go through opts.Run (R7.2) and
// secrets are passed only as flag PATHS, never as values (R6.1).
//
// The CLI verb that wires this up is T6.2; this file is the engine only.

// ErrBrokenChain is returned when an archive restore's delta chain is not a
// contiguous full→…→target sequence — an empty chain, a first link that is not a
// full, or a gap where a delta's parent is missing. It is the G3 backstop: a
// broken chain is refused BEFORE any `btrfs receive` runs, so a restore can never
// apply a delta whose base is absent (R5.2).
var ErrBrokenChain = errors.New("archive restore chain is broken")

// ErrRestoreDeclined is returned when the operator does not approve a destructive
// restore at the confirm prompt (R5.4). When this is returned, NOTHING has been
// applied — the gate fires before any subprocess.
var ErrRestoreDeclined = errors.New("restore declined by operator")

// confirmFunc prompts the operator to approve a destructive action and reports
// their decision. It is a seam (mirroring internal/autoupdate/applier.go) so tests
// can approve/deny without real terminal I/O. There is no confirmFunc in story
// 004's snapshot package; this is created here for the restore gate (R5.4).
type confirmFunc func(prompt string) bool

// defaultConfirmFunc reads a y/N answer from stdin, defaulting to NO on empty
// input or any read error — the safe default for a destructive restore. It mirrors
// internal/autoupdate/applier.go's defaultConfirmFunc.
func defaultConfirmFunc(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// chainLink is one object in an archive incremental chain: the full base first,
// then each delta. ID identifies the snapshot, ParentID is the snapshot this link
// was sent against ("" for the full base), and Object is the remote object key
// (under opts.Remote) holding this link's `btrfs send` stream.
type chainLink struct {
	ID, ParentID, Object string
}

// RestoreChainFor builds the archive object chain the restore CLI replays for id
// (T6.2). It is the EXPORTED seam that keeps chain construction INSIDE this
// package: chainLink is unexported and cannot be built from package main, so the
// CLI assigns the result straight into RestoreOptions.Chain (design §5 SCOPE
// NOTE). A restic ship needs no chain and gets nil.
//
// MVP: for an archive ship it returns a SINGLE full link — id treated as a FULL
// base (ParentID "" → validateChain accepts a length-one full→target chain) whose
// object key is ArchiveObjectName(<engine's first subvolume>, id), the same
// sanitize+suffix key the archive shipper wrote (R5.2). The first configured
// subvolume is taken as the snapshot's subvolume — the common single-subvolume
// case.
//
// TODO(incremental-chain): full chain reconstruction — listing the remote and
// re-deriving the full→…→target delta sequence for an incremental id, and
// per-subvolume selection — is live-test/future work (T6.1 scoped it out). Until
// then this replays only the requested object as a self-contained full; restoring
// a delta-only id this way would (correctly) fail at `btrfs receive` because its
// base is absent (the chain validation still guards against a truly empty chain).
func RestoreChainFor(cfg *Config, ship ShipConfig, id string) []chainLink {
	if ship.Type != "archive" {
		return nil
	}
	subvol := ""
	if len(cfg.Engine.Subvolumes) > 0 {
		subvol = cfg.Engine.Subvolumes[0]
	}
	return []chainLink{{ID: id, ParentID: "", Object: ArchiveObjectName(subvol, id)}}
}

// ArchiveObjectName derives the deterministic remote object name for a snapshot
// of subvolume with the given id: "<sanitized-subvolume>-<id>.zst". It is the
// EXPORTED mirror of the unexported archiveObjectName(Snapshot) used on the ship
// side, so the restore path can build the object key from a subvolume + id pair
// without holding a full Snapshot value. Keeping both on the same sanitize+suffix
// convention guarantees the restore reads back exactly the key the archive
// shipper wrote (R5.2).
func ArchiveObjectName(subvolume, id string) string {
	return sanitize(subvolume) + "-" + id + ".zst"
}

// RestoreOptions configures a Restore. Driver selects the path; Yes/Confirm gate
// the destructive action (R5.4); Run is the subprocess seam (R7.2). The remaining
// fields are split by driver — Remote/Compress/Chain drive the archive replay,
// Repo/PasswordFile/Include drive the restic restore.
type RestoreOptions struct {
	Driver  string      // "archive" | "restic" (resolved upstream from the --ship entry)
	Yes     bool        // --yes: skip the confirm prompt
	Confirm confirmFunc // nil → defaultConfirmFunc
	Run     Runner      // nil → defaultRunner()

	// archive:
	Remote   string // rclone remote+path prefix, e.g. "gdrive:bentoo-backups"
	Compress string // decompressor program; default "zstd" → `zstd -d`
	// Chain is the ordered full→target object chain to replay, RESOLVED UPSTREAM
	// by the CLI/caller. Reconstructing the chain from remote object metadata is
	// real-prod work gated behind live tests; T6.1's tested deliverable is the
	// validation + ordered application + refuse-before-receive logic (R5.2/G3),
	// which operates on this already-resolved chain.
	Chain []chainLink

	// restic:
	Repo, PasswordFile string // non-secret locators (R6.1): repo URL + password-FILE PATH
	Include            string // optional single file/subdir for a granular restic restore (R5.3)
}

// Restore restores snapshot id into target, dispatching by opts.Driver (R5.1).
// Because every restore is destructive, it first enforces the confirm gate (R5.4):
// unless opts.Yes is set, it asks opts.Confirm (or defaultConfirmFunc) to approve,
// and returns ErrRestoreDeclined — running NOTHING — if the operator declines.
// Only then does it dispatch: "archive" replays the validated delta chain,
// "restic" runs a (optionally granular) `restic restore`; an unknown driver is
// rejected with ErrInvalidDriver.
func Restore(ctx context.Context, id, target string, opts RestoreOptions) error {
	if opts.Run == nil {
		opts.Run = defaultRunner()
	}

	// Confirm gate (R5.4): BEFORE any subprocess. A declined restore is a no-op.
	if !opts.Yes {
		confirm := opts.Confirm
		if confirm == nil {
			confirm = defaultConfirmFunc
		}
		prompt := fmt.Sprintf("Restore snapshot %q into %q? This will write a subvolume to the target and is destructive.", id, target)
		if !confirm(prompt) {
			return ErrRestoreDeclined
		}
	}

	switch opts.Driver {
	case "archive":
		return restoreArchive(ctx, id, target, opts)
	case "restic":
		return restoreRestic(ctx, id, target, opts)
	default:
		return fmt.Errorf("%w: restore driver %q", ErrInvalidDriver, opts.Driver)
	}
}

// validateChain reports whether chain is a contiguous full→…→target sequence
// suitable for an ordered archive replay (the pure G3 deliverable). It returns
// ErrBrokenChain when the chain is empty, when its first link is not a full
// (ParentID != ""), or when any link's ParentID does not equal the previous
// link's ID (a gap — a missing or out-of-order delta). It returns nil only for a
// fully contiguous chain, so the caller can refuse a restore BEFORE applying any
// delta against a base that is not present (R5.2).
func validateChain(chain []chainLink) error {
	if len(chain) == 0 {
		return fmt.Errorf("%w: empty chain", ErrBrokenChain)
	}
	if chain[0].ParentID != "" {
		return fmt.Errorf("%w: first link %q is not a full (parent %q)", ErrBrokenChain, chain[0].ID, chain[0].ParentID)
	}
	for i := 1; i < len(chain); i++ {
		if chain[i].ParentID != chain[i-1].ID {
			return fmt.Errorf("%w: link %q expects parent %q but follows %q (missing delta)",
				ErrBrokenChain, chain[i].ID, chain[i].ParentID, chain[i-1].ID)
		}
	}
	return nil
}

// restoreArchive validates the delta chain and then replays it into target
// (R5.2). The chain is validated FIRST: a broken chain returns ErrBrokenChain and
// NO `btrfs receive` runs (G3) — nothing is applied against a missing base. On a
// valid chain, each link is applied in order through the existing runPipe helper
// with stages `rclone cat <remote>/<object> | <decompress> | btrfs receive
// <target>`. All subprocesses go through opts.Run (R7.2).
//
// NOTE (R-archive-memory): runPipe buffers each stage's full output in memory (the
// 004 Runner returns []byte), so a multi-GB stream is a real memory cost here just
// as on the ship side; a true streaming pipe is future work gated behind live
// tests and does not change the mock-tested correctness (argv wiring, ordered
// receive, refuse-before-receive).
func restoreArchive(ctx context.Context, id, target string, opts RestoreOptions) error {
	if err := validateChain(opts.Chain); err != nil {
		return err // refuse BEFORE any btrfs receive (R5.2/G3)
	}
	for _, link := range opts.Chain {
		stages := restorePipeStages(opts.Remote, link.Object, opts.Compress, target)
		if _, err := runPipe(ctx, opts.Run, stages); err != nil {
			return fmt.Errorf("restore archive link %q: %w", link.ID, err)
		}
	}
	return nil
}

// restorePipeStages builds the three-stage restore pipe for one chain link as pure
// data (the mirror of archivePipeStages on the ship side):
//   - Stage 1 `rclone cat <remote>/<object>`: streams the stored object to stdout
//     (cat is the streaming download, as opposed to copy which needs a dest file).
//   - Stage 2 the decompressor: defaults to `zstd -d` (the `-d` flag makes zstd
//     read the compressed stream on stdin and write the plaintext to stdout). A
//     configured decompressor is taken as a single program token invoked the same
//     stdin→stdout way with `-d`.
//   - Stage 3 `btrfs receive <target>`: reads the `btrfs send` stream on stdin and
//     materialises the subvolume under target.
func restorePipeStages(remote, object, decompress, target string) []pipeStage {
	src := remote + "/" + object
	prog, decArgs := decompressorStage(decompress)
	return []pipeStage{
		{name: "rclone", args: []string{"cat", src}},
		{name: prog, args: decArgs},
		{name: "btrfs", args: []string{"receive", target}},
	}
}

// decompressorStage resolves the decompressor program and its stdin→stdout argv,
// the inverse of compressorStage. An empty or "zstd" decompress selects `zstd -d`;
// any other value is treated as a single program token invoked with `-d`.
func decompressorStage(decompress string) (name string, args []string) {
	prog := strings.TrimSpace(decompress)
	if prog == "" {
		prog = "zstd"
	}
	return prog, []string{"-d"}
}

// restoreRestic runs `restic restore <id> --target <target> [--include <path>]
// --repo <repo> --password-file <file>` through opts.Run (R5.3). --include is
// emitted ONLY when opts.Include is non-empty, selecting a granular single
// file/subdir restore; without it the whole snapshot is restored. Secrets are
// carried as flag PATHS only (R6.1): --repo is a URL and --password-file is the
// PATH to the password file — the password VALUE is never read, placed in argv, or
// logged here.
func restoreRestic(ctx context.Context, id, target string, opts RestoreOptions) error {
	args := []string{"restore", id, "--target", target}
	if opts.Include != "" {
		args = append(args, "--include", opts.Include)
	}
	args = append(args, "--repo", opts.Repo, "--password-file", opts.PasswordFile)
	if _, err := opts.Run.Run(ctx, "restic", args, nil); err != nil {
		return fmt.Errorf("restic restore %q: %w", id, err)
	}
	return nil
}
