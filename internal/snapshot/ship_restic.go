package snapshot

import (
	"context"
	"fmt"
	"os"
	"strconv"
)

// mounter mounts a snapshot read-only and returns the mount path plus a
// cleanup that unmounts it. Faked in tests so no real mount/umount runs.
type mounter interface {
	Mount(ctx context.Context, snap Snapshot) (path string, cleanup func() error, err error)
}

// resticShipper backs up a snapshot into a restic repository (design §4.1). It
// never moves bytes from a live subvolume directly: a transient read-only mount
// (via mount) exposes the snapshot, restic reads from there, and the mount is
// always torn down afterward (R7.3). All subprocesses go through run (R7). The
// actual `restic backup`/`forget` lives in Send (T2.2); this file is the mount
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

// Send backs snap up into the restic repository (R1.1): it exposes the snapshot
// through a transient read-only mount and runs `restic backup <mountPath>`, then
// — when a retention policy is configured — prunes the repo with
// `restic forget --prune` (R1.4). Both subprocesses go through r.run (R7.2); the
// mount is always torn down afterward by runWithMount (R7.3).
//
// Secrets (G4/R6): the repo URL and the password-FILE PATH are passed as argv
// flags (--repo / --password-file). Those are non-secret locators; the password
// VALUE itself lives only inside the file and is never read here, never placed in
// argv/stdin, and never logged (R6.1, R6.2).
func (r *resticShipper) Send(ctx context.Context, snap Snapshot) (ShipReport, error) {
	err := r.runWithMount(ctx, snap, func(path string) error {
		args := []string{"backup", path, "--tag", "bentoo," + snap.Subvolume}
		if r.compression != "" {
			args = append(args, "--compression", r.compression)
		}
		args = append(args, r.repoFlags()...)
		if _, err := r.run.Run(ctx, "restic", args, nil); err != nil {
			return err
		}

		// Prune is skipped entirely when no retention is configured (R1.4): an
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

// repoFlags returns the repository locator flags shared by backup and forget.
// Both values are non-secret paths/URLs (R6.1): --repo is the repository URL and
// --password-file is the PATH to the password file, not the password itself.
func (r *resticShipper) repoFlags() []string {
	return []string{"--repo", r.repo, "--password-file", r.passwordFile}
}

// retentionFlags maps the retention policy to restic's --keep-* flags (R1.4). Each
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
// ALWAYS cleans up the mount afterward — including when fn returns an error
// (R7.3). The cleanup error never masks fn's error.
func (r *resticShipper) runWithMount(ctx context.Context, snap Snapshot, fn func(path string) error) error {
	path, cleanup, err := r.mount.Mount(ctx, snap)
	if err != nil {
		return err
	}
	// Deferred so the unmount runs on every exit path — normal return, fn error,
	// or panic. fn's error is the return value; a cleanup failure is reported only
	// when fn itself succeeded, so it can never mask the more important fn error.
	defer func() {
		if cerr := cleanup(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	err = fn(path)
	return err
}

// transientMounter is the production mounter (R7): it mounts a read-only btrfs
// snapshot at a fresh temp dir and returns a cleanup that unmounts it and removes
// the dir. It is exercised by live tests only — unit tests use a fakeMounter — so
// it is kept deliberately simple.
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

	// cleanup is best-effort and idempotent-safe: a failed/absent umount must not
	// stop us from removing the temp dir, and double-invocation is harmless.
	cleanup := func() error {
		_, _ = m.run.Run(ctx, "umount", []string{dir}, nil)
		return os.RemoveAll(dir)
	}

	if _, err := m.run.Run(ctx, "mount", []string{"--bind", snap.Path, dir}, nil); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("bind-mount snapshot: %w", err)
	}
	if _, err := m.run.Run(ctx, "mount", []string{"-o", "remount,ro,bind", dir}, nil); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("remount read-only: %w", err)
	}

	return dir, cleanup, nil
}

// Compile-time assertions: transientMounter is a mounter, resticShipper a Shipper.
var (
	_ mounter = (*transientMounter)(nil)
	_ Shipper = (*resticShipper)(nil)
)
