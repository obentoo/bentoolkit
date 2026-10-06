package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// mounter mounts a snapshot read-only and returns the mount path plus a
// cleanup that unmounts it. Faked in tests so no real mount/umount runs.
type mounter interface {
	Mount(ctx context.Context, snap Snapshot) (path string, cleanup func() error, err error)
}

// resticShipper backs up a snapshot into a restic repository (design §4.1). It
// never moves bytes from a live subvolume directly: a transient read-only mount
// (via mount) exposes the snapshot, restic reads from there, and the mount is
// always torn down afterward. All subprocesses go through run. The
// actual `restic backup`/`forget` lives in Send; this file is the mount
// machinery only.
type resticShipper struct {
	name         string
	repo         string
	passwordFile string
	compression  string
	retention    Retention
	mount        mounter
	run          Runner
}

// Name returns the ship's configured name, or "restic" when unnamed (mirrors
// sshShipper.Name()).
func (r *resticShipper) Name() string {
	if r.name != "" {
		return r.name
	}
	return "restic"
}

// Send backs snap up into the restic repository: it exposes the snapshot
// through a transient read-only mount and runs `restic backup <mountPath>`, then
// — when a retention policy is configured — prunes the repo with
// `restic forget --prune`. Both subprocesses go through r.run; the
// mount is always torn down afterward by runWithMount.
//
// Secrets: the repo URL and the password-FILE PATH are passed as argv
// flags (--repo / --password-file). Those are non-secret locators; the password
// VALUE itself lives only inside the file and is never read here, never placed in
// argv/stdin, and never logged.
func (r *resticShipper) Send(ctx context.Context, snap Snapshot) (ShipReport, error) {
	if snap.Path == "" || snap.ID == "" {
		return ShipReport{}, fmt.Errorf("ship %q subvolume %q: %w", r.Name(), snap.Subvolume, ErrSnapshotUnidentified)
	}
	err := r.runWithMount(ctx, snap, func(path string) error {
		args := []string{"backup", path, "--tag", "bentoo," + snap.Subvolume}
		if r.compression != "" {
			args = append(args, "--compression", r.compression)
		}
		args = append(args, r.repoFlags()...)
		if _, err := r.run.Run(ctx, "restic", args, nil); err != nil {
			return err
		}

		// Prune is skipped entirely when no retention is configured: an
		// empty policy means "keep everything", so issuing forget would be wrong.
		if keep := r.retentionFlags(); len(keep) > 0 {
			forget := append([]string{"forget", "--prune"}, keep...)
			forget = append(forget, r.repoFlags()...)
			if _, err := r.run.Run(ctx, "restic", forget, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ShipReport{}, err
	}
	return ShipReport{
		Target:    r.repo,
		Snapshot:  snap.ID,
		Delegated: false,
		Note:      "restic backup",
	}, nil
}

// resticSnapshotJSON mirrors the fields consumed from `restic snapshots --json`:
// the short id, creation time, and backed-up paths of one snapshot.
// restic marshals times as RFC 3339, which time.Time decodes natively.
type resticSnapshotJSON struct {
	ShortID string    `json:"short_id"`
	Time    time.Time `json:"time"`
	Paths   []string  `json:"paths"`
}

// ListRemote enumerates the repository's snapshots via
// `restic --repo X --password-file Y snapshots --json`. The argv is
// the shared repo locator (repoFlags — non-secret paths/URLs) plus the
// read-only `snapshots --json` query; the JSON array maps to Snapshot values
// (ID = short_id, CreatedAt = time, Subvolume = the backed-up paths).
func (r *resticShipper) ListRemote(ctx context.Context) ([]Snapshot, error) {
	args := append(r.repoFlags(), "snapshots", "--json")
	out, err := r.run.Run(ctx, "restic", args, nil)
	if err != nil {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	var raw []resticSnapshotJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse restic snapshots: %w", err)
	}
	snaps := make([]Snapshot, 0, len(raw))
	for _, s := range raw {
		snaps = append(snaps, Snapshot{
			ID:        s.ShortID,
			Subvolume: strings.Join(s.Paths, ","),
			CreatedAt: s.Time,
		})
	}
	return snaps, nil
}

// repoFlags returns the repository locator flags shared by backup and forget.
// Both values are non-secret paths/URLs: --repo is the repository URL and
// --password-file is the PATH to the password file, not the password itself.
func (r *resticShipper) repoFlags() []string {
	return []string{"--repo", r.repo, "--password-file", r.passwordFile}
}

// retentionFlags maps the retention policy to restic's --keep-* flags. Each
// count > 0 contributes its interval flag; PreserveMin "latest" maps to
// --keep-last 1 (always retain the most recent snapshot). Any other non-empty
// PreserveMin (e.g. a btrbk-style duration like "2d") has no restic equivalent
// here, so it is intentionally ignored — restic expresses minimums only through
// --keep-last/--keep-within, and mapping a duration onto those is out of scope for
// this task. An all-zero/empty policy yields an empty slice, which Send treats as
// "no pruning configured" and skips forget.
func (r *resticShipper) retentionFlags() []string {
	var flags []string
	add := func(flag string, n int) {
		if n > 0 {
			flags = append(flags, flag, strconv.Itoa(n))
		}
	}
	add("--keep-hourly", r.retention.Hourly)
	add("--keep-daily", r.retention.Daily)
	add("--keep-weekly", r.retention.Weekly)
	add("--keep-monthly", r.retention.Monthly)
	if r.retention.PreserveMin == "latest" {
		flags = append(flags, "--keep-last", "1")
	}
	return flags
}

// runWithMount mounts snap read-only, invokes fn with the mount path, and
// ALWAYS cleans up the mount afterward — including when fn returns an error.
// When both fail, the two errors are joined, so neither masks the
// other and errors.Is matches both.
func (r *resticShipper) runWithMount(ctx context.Context, snap Snapshot, fn func(path string) error) (err error) {
	path, cleanup, err := r.mount.Mount(ctx, snap)
	if err != nil {
		return err
	}
	// Deferred so the unmount runs on every exit path — normal return, fn error,
	// or panic. errors.Join drops nil operands, so a clean run returns fn's error
	// (or nil) unchanged.
	defer func() {
		if cerr := cleanup(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()

	err = fn(path)
	return err
}

// umountTimeout bounds the transient mount's unmount. It is a var
// only so tests can shrink it; it is not configurable.
var umountTimeout = 30 * time.Second

// transientMounter is the production mounter: it mounts a read-only btrfs
// snapshot at a fresh temp dir and returns a cleanup that unmounts it and removes
// the dir. Its own unit tests script mount/umount through a mock Runner; the
// shipper's tests use a fakeMounter.
type transientMounter struct {
	run Runner
}

// Mount creates a temp dir and bind-mounts snap.Path there read-only.
//
// btrfs read-only mount: a btrfs *subvolume* snapshot may itself be read-only,
// but the snapshot's own RO flag does not propagate to an arbitrary mountpoint —
// a plain `mount -o ro` on btrfs can still expose it writable in some kernels.
// The robust, btrfs-correct form is a bind mount followed by a read-only remount:
// `mount --bind src dst` then `mount -o remount,ro,bind dst`. The remount is what
// actually enforces RO on the bind, guaranteeing restic cannot mutate the source.
func (m *transientMounter) Mount(ctx context.Context, snap Snapshot) (string, func() error, error) {
	dir, err := os.MkdirTemp("", "bentoo-snap-ro-")
	if err != nil {
		return "", nil, fmt.Errorf("create mount dir: %w", err)
	}

	// cleanup unmounts on a context that outlives a cancelled Send, bounded by
	// umountTimeout, and removes the directory only once the unmount succeeded
	// and only if it is empty: a directory that may still be a mounted snapshot
	// is never walked.
	cleanup := func() error {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), umountTimeout)
		defer cancel()
		if _, err := m.run.Run(uctx, "umount", []string{dir}, nil); err != nil {
			return fmt.Errorf("unmount %s (left mounted; remove it by hand after unmounting): %w", dir, err)
		}
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("remove mount dir %s: %w", dir, err)
		}
		return nil
	}

	if _, err := m.run.Run(ctx, "mount", []string{"--bind", snap.Path, dir}, nil); err != nil {
		bindErr := fmt.Errorf("bind-mount snapshot: %w", err)
		if rerr := os.Remove(dir); rerr != nil {
			return "", nil, errors.Join(bindErr, fmt.Errorf("remove mount dir %s: %w", dir, rerr))
		}
		return "", nil, bindErr
	}
	if _, err := m.run.Run(ctx, "mount", []string{"-o", "remount,ro,bind", dir}, nil); err != nil {
		return "", nil, errors.Join(fmt.Errorf("remount read-only: %w", err), cleanup())
	}

	return dir, cleanup, nil
}

// Compile-time assertions: transientMounter is a mounter; resticShipper is a
// Shipper and contributes to `list --remote`.
var (
	_ mounter      = (*transientMounter)(nil)
	_ Shipper      = (*resticShipper)(nil)
	_ remoteLister = (*resticShipper)(nil)
)
