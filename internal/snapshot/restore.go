package snapshot

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
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

// ResolveRestoreSubvolume picks the subvolume a restore reads from, given the
// engine's configured list and the value of the --subvolume flag ("" when the
// operator did not pass one). Its three branches ARE requirement R5:
//
//   - a non-empty flag must name a configured subvolume, else it fails naming
//     the value passed and listing the configured spellings (R5.3);
//   - an empty flag with exactly ONE configured subvolume yields that one, so
//     the single-subvolume deployment keeps working with no new flag (R5.1);
//   - an empty flag with TWO OR MORE is ambiguous and fails, naming every
//     configured subvolume so the operator can retry without opening the config
//     (R5.2).
//
// The rule lives HERE, in the package, rather than inside the cobra handler for
// one reason: restore is DESTRUCTIVE, it writes a subvolume over a target, so
// the rule that decides WHICH subvolume it reads has to be provable. Held in the
// handler it could only be exercised by driving a command; held here it is a
// pure function over (config, flag) that a unit test pins branch by branch —
// which is why the guarantee can be trusted. Callers must resolve BEFORE calling
// Restore, so an ambiguous request fails ahead of any subprocess, not midway.
//
// Matching is exact string equality against the configured entries: the
// configured spelling is the key ArchivePrefix sanitizes, so normalising the
// flag (trimming a trailing '/', say) would silently accept a spelling that does
// not correspond to the stored prefix and send the restore to the wrong objects.
func ResolveRestoreSubvolume(cfg *Config, flag string) (string, error) {
	// A nil config is a programming error upstream, not operator input; it is
	// rejected explicitly because the alternative in a destructive verb is a nil
	// dereference panic.
	if cfg == nil {
		return "", errors.New("cannot resolve the restore subvolume: no snapshot configuration was loaded")
	}

	configured := cfg.Engine.Subvolumes

	// Validate only WARNS about an empty subvolume list (config.go, R1.4), so a
	// config with none legitimately reaches here. There is no subvolume to read
	// from and no list to suggest: fail naming the empty setting. The flag is
	// echoed when present, since the operator asked for something specific.
	if len(configured) == 0 {
		if flag != "" {
			return "", fmt.Errorf("subvolume %q is not configured: engine.subvolumes is empty", flag)
		}
		return "", errors.New("cannot resolve the restore subvolume: engine.subvolumes is empty")
	}

	if flag != "" {
		if slices.Contains(configured, flag) {
			return flag, nil // R5.3, satisfied
		}
		return "", fmt.Errorf("subvolume %q is not configured; configured subvolumes are %s",
			flag, quotedSubvolumes(configured))
	}

	if len(configured) == 1 {
		return configured[0], nil // R5.1: no flag needed for the single-subvolume case
	}

	return "", fmt.Errorf("cannot tell which subvolume to restore from: %d are configured (%s); name one with --subvolume",
		len(configured), quotedSubvolumes(configured))
}

// quotedSubvolumes renders a subvolume list for an operator-facing error as
// `"/", "/home"`. The quoting is what makes an entry with a trailing space or an
// empty string visible instead of invisible — this text is read while a restore
// is being retried, so a spelling has to be copyable exactly as configured.
func quotedSubvolumes(subvolumes []string) string {
	quoted := make([]string, 0, len(subvolumes))
	for _, sv := range subvolumes {
		quoted = append(quoted, fmt.Sprintf("%q", sv))
	}
	return strings.Join(quoted, ", ")
}

// RestoreChainFor builds the archive object chain the restore CLI replays for id
// (T6.2). It is the EXPORTED seam that keeps chain construction INSIDE this
// package: chainLink is unexported and cannot be built from package main, so the
// CLI assigns the result straight into RestoreOptions.Chain (design §5 SCOPE
// NOTE). A restic ship needs no chain and gets nil.
//
// subvolume is the ALREADY-RESOLVED subvolume the restore reads from: the caller
// runs ResolveRestoreSubvolume first, so an ambiguous or unconfigured request has
// already failed before this point (R5.2, R5.3). This function makes no choice of
// its own. It used to derive the key from the engine's FIRST configured subvolume,
// which on any config with more than one sent every restore to the wrong prefix —
// the reason the value is passed in now.
//
// MVP: for an archive ship it returns a SINGLE full link — id treated as a FULL
// base (ParentID "" → validateChain accepts a length-one full→target chain) whose
// object key is ArchiveObjectName(subvolume, id) — the "<prefix>/<leaf>" form
// "<ArchivePrefix(subvolume)>/<id>.zst", which is exactly the key the archive
// shipper wrote under that subvolume's own prefix directory (R3.2).
//
// cfg is unread today. It is kept because the chain this returns is still a
// placeholder — see the TODO — and reconstructing a real chain needs the config
// (the ship list and retention live there); the seam is exported, so churning its
// signature twice costs more than one unread parameter.
//
// TODO(incremental-chain): full chain reconstruction — listing the remote and
// re-deriving the full→…→target delta sequence for an incremental id — is
// live-test/future work (T6.1 scoped it out). Resolving the subvolume changed
// WHICH prefix the chain reads from, NOT how many links it has: this still
// replays only the requested object as a self-contained full, so restoring a
// delta-only id this way would (correctly) fail at `btrfs receive` because its
// base is absent (the chain validation still guards against a truly empty chain).
func RestoreChainFor(cfg *Config, ship ShipConfig, id, subvolume string) []chainLink {
	if ship.Type != "archive" {
		return nil
	}
	return []chainLink{{ID: id, ParentID: "", Object: ArchiveObjectName(subvolume, id)}}
}

// ArchivePrefix is the remote sub-path that holds one subvolume's objects: the
// subvolume path put through the same sanitize rule the parent store already
// uses for its filenames, with NO special case — including for the root
// subvolume "/", whose prefix is therefore the single directory "-" (R3.3).
//
// The property that makes this prefix unambiguous is that sanitize maps every
// byte outside [A-Za-z0-9._-] to '-' and so can NEVER emit '/'. The '/' that
// ArchiveObjectName appends is therefore the only one in the key: a prefix can
// never bleed into the leaf, and two nested subvolumes can never produce the
// same key. The old flat scheme joined prefix and id with '-', a byte sanitize
// CAN emit, so subvolume "/home" with id "otaku-42" and subvolume "/home/otaku"
// with id "42" both rendered as "-home-otaku-42.zst" (R3.1).
func ArchivePrefix(subvolume string) string {
	return sanitize(subvolume)
}

// ArchiveObjectLeaf is the object name RELATIVE to its prefix directory:
// "<id>.zst". This is exactly what `rclone lsjson <remote>/<prefix>` reports in
// each entry's Name — relative to the LISTED path, carrying no prefix — so a
// scoped listing of "-home" yields "snap1.zst", not "-home/snap1.zst".
//
// Comparing a listing entry against a FULL key (ArchiveObjectName) therefore
// matches nothing, and it does so SILENTLY: no error, no empty result, just a
// comparison that is never true. Where that comparison is a guard, "matches
// nothing" means "the guard protects nothing". Compare listing entries with this
// function; use ArchiveObjectName only for keys relative to the remote root.
func ArchiveObjectLeaf(id string) string {
	return id + ".zst"
}

// ArchiveObjectName derives the deterministic FULL remote object key — relative
// to the remote ROOT — for a snapshot of subvolume with the given id:
// "<ArchivePrefix(subvolume)>/<ArchiveObjectLeaf(id)>", e.g. subvolume "/home"
// with id "snap1" → "-home/snap1.zst" (R3.1). It is the EXPORTED mirror of the
// unexported archiveObjectName(Snapshot) used on the ship side, so the restore
// path can build the object key from a subvolume + id pair without holding a
// full Snapshot value. Keeping both on the same convention guarantees the
// restore reads back exactly the key the archive shipper wrote (R5.2).
//
// This is a full key, NOT a listing entry: see ArchiveObjectLeaf for what
// `rclone lsjson` reports under a scoped path.
func ArchiveObjectName(subvolume, id string) string {
	return ArchivePrefix(subvolume) + "/" + ArchiveObjectLeaf(id)
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
