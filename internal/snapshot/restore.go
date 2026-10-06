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

// restore.go is the snapshot RESTORE entry point. Restore dispatches by
// driver: an "archive" restore validates the full→target delta chain and then
// replays it through `rclone cat | <decompress> | btrfs receive`; a "restic"
// restore runs `restic restore --target` and can pull back a single
// file/subdir granularly. Every restore is DESTRUCTIVE (it writes a subvolume
// into the target), so it is gated behind an operator confirmation unless
// --yes is given. All subprocesses go through opts.Run and secrets are passed
// only as flag PATHS, never as values.
//
// The CLI verb that wires this up is cmd/bentoo/snapshot_restore.go; this file
// is the engine only.

// ErrBrokenChain is returned when an archive restore's delta chain is not a
// contiguous full→…→target sequence — an empty chain, a first link that is not a
// full, or a gap where a delta's parent is missing. It is the backstop: a
// broken chain is refused BEFORE any `btrfs receive` runs, so a restore can never
// apply a delta whose base is absent.
var ErrBrokenChain = errors.New("archive restore chain is broken")

// ErrRestoreDeclined is returned when the operator does not approve a destructive
// restore at the confirm prompt. When this is returned, NOTHING has been
// applied — the gate fires before any subprocess.
var ErrRestoreDeclined = errors.New("restore declined by operator")

// confirmFunc prompts the operator to approve a destructive action and reports
// their decision. It is a seam (mirroring internal/autoupdate/applier.go) so tests
// can approve/deny without real terminal I/O.
type confirmFunc func(prompt string) bool

// defaultConfirmFunc reads a y/N answer from stdin, defaulting to NO on empty
// input or any read error — the safe default for a destructive restore. It mirrors
// internal/autoupdate/applier.go's defaultConfirmFunc.
func defaultConfirmFunc(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt) //nolint:forbidigo // interactive y/N prompt, paired with the stdin read below
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
// operator did not pass one):
//
//   - a non-empty flag must name a configured subvolume, else it fails naming
//     the value passed and listing the configured spellings;
//   - an empty flag with exactly ONE configured subvolume yields that one, so
//     the single-subvolume deployment keeps working with no new flag;
//   - an empty flag with TWO OR MORE is ambiguous and fails, naming every
//     configured subvolume so the operator can retry without opening the config.
//
// The rule lives in the package, not the cobra handler, because restore is
// DESTRUCTIVE: as a pure function over (config, flag) a unit test pins it
// branch by branch. Callers resolve BEFORE calling Restore, so an ambiguous
// request fails ahead of any subprocess. Matching is exact string equality: the
// configured spelling is the key ArchivePrefix sanitizes, so normalising the
// flag (trimming a trailing '/', say) would send the restore to the wrong objects.
func ResolveRestoreSubvolume(cfg *Config, flag string) (string, error) {
	// A nil config is a programming error upstream, not operator input; it is
	// rejected explicitly because the alternative in a destructive verb is a nil
	// dereference panic.
	if cfg == nil {
		return "", errors.New("cannot resolve the restore subvolume: no snapshot configuration was loaded")
	}

	configured := cfg.Engine.Subvolumes

	// Validate only WARNS about an empty subvolume list (config.go), so a
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
			return flag, nil
		}
		return "", fmt.Errorf("subvolume %q is not configured; configured subvolumes are %s",
			flag, quotedSubvolumes(configured))
	}

	if len(configured) == 1 {
		return configured[0], nil // no flag needed for the single-subvolume case
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

// RestoreChainFor builds the archive object chain the restore CLI replays for
// id. It is the EXPORTED seam that keeps chain construction INSIDE this
// package: chainLink is unexported and cannot be built from package main, so the
// CLI assigns the result straight into RestoreOptions.Chain. A restic ship needs
// no chain and gets nil.
//
// subvolume is the ALREADY-RESOLVED subvolume (the caller runs
// ResolveRestoreSubvolume first); this function makes no choice of its own, since
// deriving it from the FIRST configured subvolume sent multi-subvolume restores
// to the wrong prefix. For an archive ship it returns a SINGLE full link
// (ParentID "") keyed ArchiveObjectName(subvolume, id), the key the archive
// shipper wrote. cfg is unread but kept: a real chain needs the ship list and
// retention, and churning an exported signature twice costs more than that.
//
// TODO(incremental-chain): reconstruct the full→…→target delta sequence for an
// incremental id by listing the remote. Until then a delta-only id fails
// (correctly) at `btrfs receive` because its base is absent.
func RestoreChainFor(cfg *Config, ship ShipConfig, id, subvolume string) []chainLink {
	if ship.Type != "archive" {
		return nil
	}
	return []chainLink{{ID: id, ParentID: "", Object: ArchiveObjectName(subvolume, id)}}
}

// ArchivePrefix is the remote sub-path that holds one subvolume's objects: the
// subvolume path put through the same sanitize rule the parent store already
// uses for its filenames, with NO special case — including for the root
// subvolume "/", whose prefix is therefore the single directory "-".
//
// The property that makes this prefix unambiguous is that sanitize maps every
// byte outside [A-Za-z0-9._-] to '-' and so can NEVER emit '/'. The '/' that
// ArchiveObjectName appends is therefore the only one in the key: a prefix can
// never bleed into the leaf, and two nested subvolumes can never produce the
// same key. The old flat scheme joined prefix and id with '-', a byte sanitize
// CAN emit, so subvolume "/home" with id "otaku-42" and subvolume "/home/otaku"
// with id "42" both rendered as "-home-otaku-42.zst".
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
// with id "snap1" → "-home/snap1.zst". It is the EXPORTED mirror of the
// unexported archiveObjectName(Snapshot) used on the ship side, so the restore
// path can build the object key from a subvolume + id pair without holding a
// full Snapshot value. Keeping both on the same convention guarantees the
// restore reads back exactly the key the archive shipper wrote.
//
// This is a full key, NOT a listing entry: see ArchiveObjectLeaf for what
// `rclone lsjson` reports under a scoped path.
func ArchiveObjectName(subvolume, id string) string {
	return ArchivePrefix(subvolume) + "/" + ArchiveObjectLeaf(id)
}

// RestoreOptions configures a Restore. Driver selects the path; Yes/Confirm gate
// the destructive action; Run is the subprocess seam. The remaining
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
	// future work; Restore validates, orders and refuses-before-receive on this
	// already-resolved chain.
	Chain []chainLink

	// restic:
	Repo, PasswordFile string // non-secret locators: repo URL + password-FILE PATH
	Include            string // optional single file/subdir for a granular restic restore
}

// Restore restores snapshot id into target, dispatching by opts.Driver.
// Because every restore is destructive, it first enforces the confirm gate:
// unless opts.Yes is set, it asks opts.Confirm (or defaultConfirmFunc) to approve,
// and returns ErrRestoreDeclined — running NOTHING — if the operator declines.
// Only then does it dispatch: "archive" replays the validated delta chain,
// "restic" runs a (optionally granular) `restic restore`; an unknown driver is
// rejected with ErrInvalidDriver.
func Restore(ctx context.Context, id, target string, opts RestoreOptions) error {
	if opts.Run == nil {
		opts.Run = defaultRunner()
	}

	// Confirm gate: BEFORE any subprocess. A declined restore is a no-op.
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
// suitable for an ordered archive replay. It returns
// ErrBrokenChain when the chain is empty, when its first link is not a full
// (ParentID != ""), or when any link's ParentID does not equal the previous
// link's ID (a gap — a missing or out-of-order delta). It returns nil only for a
// fully contiguous chain, so the caller can refuse a restore BEFORE applying any
// delta against a base that is not present.
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

// restoreArchive validates the delta chain and then replays it into target.
// The chain is validated FIRST: a broken chain returns ErrBrokenChain and
// NO `btrfs receive` runs — nothing is applied against a missing base. On a
// valid chain, each link is applied in order, one stage after another, with
// stages `rclone cat <remote>/<object>`, `<decompress>` and `btrfs receive
// <target>`. All subprocesses go through opts.Run.
//
// Each link runs through runStagesBuffered, not the streaming runPipe the ship
// side uses: `btrfs receive` must start only once the download and the
// decompression have both succeeded. Streamed, a failed download or a truncated
// object reaches receive as a partial stream, which leaves a partial, writable
// subvolume at the target, and the retry then fails because it already exists.
// The price is that a link is held in memory, as it always was here.
func restoreArchive(ctx context.Context, id, target string, opts RestoreOptions) error {
	if err := validateChain(opts.Chain); err != nil {
		return err // refuse BEFORE any btrfs receive
	}
	for _, link := range opts.Chain {
		stages := restorePipeStages(opts.Remote, link.Object, opts.Compress, target)
		if _, err := runStagesBuffered(ctx, opts.Run, stages); err != nil {
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
func restorePipeStages(remote, object, decompress, target string) []PipeStage {
	src := remote + "/" + object
	prog, decArgs := decompressorStage(decompress)
	return []PipeStage{
		{Name: "rclone", Args: []string{"cat", src}},
		{Name: prog, Args: decArgs},
		{Name: "btrfs", Args: []string{"receive", target}},
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
// --repo <repo> --password-file <file>` through opts.Run. --include is
// emitted ONLY when opts.Include is non-empty, selecting a granular single
// file/subdir restore; without it the whole snapshot is restored. Secrets are
// carried as flag PATHS only: --repo is a URL and --password-file is the
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
