# Bentoolkit snapshots

Back to the [README](../README.md).

## Snapshot Management

`bentoo snapshot` manages btrfs snapshots declaratively from a single
`snapshot.toml`. bentoolkit is an **orchestrator**: it renders native config for
mature tools (`btrbk` for snapshots and ssh send/receive, `systemd` for
scheduling) and coordinates them — it never calls `btrfs` directly.

### Dependencies

- `app-backup/btrbk` — the snapshot engine and ssh replication (when `engine.driver = "btrbk"`).
- `app-backup/snapper` — only when `engine.driver = "snapper"` (timeline snapshots + rollback).
- `systemd` — the scheduler backend.
- `app-backup/restic` — only when a `[[ship]]` uses `type = "restic"` (cloud backup).
- `net-misc/rclone` — only when a `[[ship]]` uses `type = "archive"` (cloud backup).

A missing binary is reported at config-validate time with an actionable error
naming the Portage package (e.g. `engine driver "btrbk" requires
app-backup/btrbk on PATH`, or `ship driver "restic" requires app-backup/restic
on PATH`).

When installing through Portage, the `app-portage/bentoolkit` ebuild maps each
backend to a USE flag so you pull in only what your config uses:

| USE flag  | Pulls in            | Enables                                  |
|-----------|---------------------|------------------------------------------|
| `btrbk`   | `app-backup/btrbk`  | btrbk engine (snapshots + ssh ship)      |
| `snapper` | `app-backup/snapper`| snapper engine (timeline + rollback)     |
| `restic`  | `app-backup/restic` | restic cloud ship                        |
| `rclone`  | `net-misc/rclone`   | archive cloud ship                       |
| `systemd` | `sys-apps/systemd`  | systemd timer scheduling                 |

All flags are optional and default-off — the binary degrades gracefully, and
`detect` names the exact missing package at runtime if the active config needs
a backend that is not installed.

### Configuration (`snapshot.toml`)

Resolved in priority order: `/etc/bentoo/snapshot.toml`, then
`$XDG_CONFIG_HOME/bentoo/snapshot.toml`, then `~/.config/bentoo/snapshot.toml`.
System scope (`/etc/bentoo`, system timers) is the primary target.

```toml
[engine]
driver = "btrbk"                 # "btrbk" (backup/replication) | "snapper" (timeline + rollback)
subvolumes = ["/", "/home"]      # btrfs subvolumes to snapshot
snapshot_dir = "/.snapshots"

[engine.retention]               # delegated to btrbk's preserve directives
hourly = 24
daily = 7
weekly = 4
monthly = 6
preserve_min = "latest"

[[ship]]                         # zero or more replication targets
type = "ssh"                     # local/LAN replication via btrbk
target = "user@host:/backup/btrbk"

[[ship]]                         # cloud backup — restic (recommended)
name = "offsite"
type = "restic"
repo = "s3:s3.amazonaws.com/my-bucket"   # or any restic/rclone backend
password_file = "/etc/bentoo/restic.pass" # secret PATH only, never the value
compression = "auto"             # auto | max | off

[[ship]]                         # cloud backup — portable archive object
name = "gdrive"
type = "archive"
remote = "gdrive:bentoo-backups" # an rclone remote:path
mode = "incremental"             # incremental (default) | full
compress = "zstd"                # stream compressor

[schedule]
backend = "systemd"              # only "systemd" in this release
on_calendar = "daily"            # systemd OnCalendar=
persistent = true                # systemd Persistent=
randomized_delay = "5m"          # systemd RandomizedDelaySec=

[notify]                         # best-effort run notifications (every part optional)
on = ["failure"]                 # outcomes that notify: "failure" and/or "success"

[notify.ntfy]
url = "https://ntfy.sh/my-topic" # ntfy topic URL (POST the run summary)
# auth token (optional): set BENTOO_NTFY_TOKEN in the env or the bentoo secrets file

[notify.healthchecks]
ping_url = "https://hc-ping.com/<uuid>"   # base ping on success, /fail on failure
start = true                     # also ping /start before the run

[notify.webhook]
url = "https://example.com/hook" # receives the RunResult as a JSON POST
headers = { Authorization = "Bearer ..." } # optional custom headers, never logged

[notify.email]
to = ["ops@example.com"]         # one or more recipients (activates the driver)
from = "bentoo@myhost"
# transport: local sendmail by default; configure [notify.email.smtp] to use SMTP

[notify.email.smtp]              # optional — omit to send via local `sendmail -t`
host = "smtp.example.com"
port = 587
user = "bentoo"                  # with BENTOO_SMTP_PASSWORD set, enables SMTP AUTH (PLAIN)
# The password is not a config key: put BENTOO_SMTP_PASSWORD in the secrets file
# (~/.config/bentoo/secrets or /etc/bentoo/secrets, chmod 600).
```

### Commands

```bash
# Render the native btrbk.conf and install + enable the systemd timer
bentoo snapshot apply

# Run the engine → prune → ship pipeline now (the timer target)
bentoo snapshot run

# List local snapshots per subvolume; --remote also queries btrbk targets
# and restic repositories
bentoo snapshot list
bentoo snapshot list --remote

# Show the last run (per stage), timer state + next scheduled run, free space
bentoo snapshot status

# Apply [engine.retention] on demand: engine-native prune + archive GFS
bentoo snapshot prune
bentoo snapshot prune --ship gdrive       # scope to one destination only

# Restore a snapshot from a cloud ship (destructive — requires confirmation)
# --subvolume is required only when two or more subvolumes are configured
bentoo snapshot restore <id> --target /mnt/restore --ship offsite --yes
bentoo snapshot restore <id> --target /mnt/restore --ship offsite --subvolume /home --yes

# Roll the system back to a snapshot (snapper engine only; destructive)
bentoo snapshot rollback <id> --yes

# Install / remove the opt-in pre/post-emerge snapshot hook (snapper engine)
bentoo snapshot hook --install
bentoo snapshot hook --uninstall
```

`apply` is idempotent — re-running reconciles the units without duplicates.
`--config <path>` overrides the search path on any verb. `run` persists a
`RunResult` under `/var/lib/bentoo/snapshot/last-run.json`, which `status` reads
back.

**Dry-run everywhere.** `apply`, `run`, `restore`, `rollback`, and `prune` all
accept `--dry-run`: the verb prints exactly what it would do (configs and
systemd units it would write, the engine → prune → ship pipeline it would
execute, or the destructive actions it would perform) and **guarantees zero side
effects** — no subprocess is spawned, nothing is written, no confirmation is
prompted. Preview any change safely before committing to it.

### Notifications

The optional `[notify]` section reports the outcome of a `bentoo snapshot run` so a
scheduled backup surfaces failures without scraping logs. Four backends fan out
from one config — configure any subset:

- **ntfy** (`[notify.ntfy]`) — POSTs a run summary to a topic URL. Failures use an
  elevated priority and an alert tag; successes use normal priority. An optional auth
  token, resolved from `BENTOO_NTFY_TOKEN` via the secrets chain, is sent as a Bearer
  header.
- **healthchecks.io** (`[notify.healthchecks]`) — pings the base `ping_url` on
  success and `ping_url/fail` on failure (a dead-man's switch). With `start = true`
  it also pings `ping_url/start` before the run so the dashboard can time it.
- **webhook** (`[notify.webhook]`) — POSTs the `RunResult` as JSON to your own
  endpoint, with any custom `headers` applied — for arbitrary automation.
- **email** (`[notify.email]`) — sends the run summary to the configured
  recipients. Transport is local `sendmail -t` by default; configuring
  `[notify.email.smtp]` switches to direct SMTP (stdlib `net/smtp`, with PLAIN
  auth when `user` is set and `BENTOO_SMTP_PASSWORD` resolves through the secrets
  chain). An unresolvable password sends unauthenticated rather than failing the
  notification. The subject reflects the outcome.

`on` filters which outcomes notify (`["failure"]`, `["success"]`, or both); an empty
or omitted `on` notifies on **failure only**. Notification is **best-effort**: a
backend that errors is logged as a warning and never changes the run's exit code,
and the remaining backends are still attempted. **Secrets** (the ntfy token, webhook
header values, the SMTP password) are sent only in request headers / the SMTP
session and are **never written to logs, argv, or error messages**.

### Cloud backup & restore

Two `[[ship]]` drivers push snapshots off-site, on the same schedule and config as
local snapshots, plus a `restore` verb to bring either back.

- **`restic`** (recommended) — backs up a **read-only snapshot mount** with
  `restic backup` to S3/B2/GCS or any rclone backend: dedup, encryption,
  compression (`auto|max|off`), and granular restore. Retention maps
  `[engine.retention]` to `restic forget --prune`. The transient RO mount is always
  unmounted afterward, **including on error**.
- **`archive`** — streams `btrfs send [-p parent] | zstd | rclone rcat` into a single
  portable object on any rclone remote (e.g. Google Drive); restore is a bit-exact
  `rclone cat | zstd -d | btrfs receive`.
  - **Incremental vs full:** `mode = "incremental"` (default) sends `-p <parent>` when
    a recorded parent exists; otherwise it **warns** and falls back to a full send
    (never silent). The parent for a `(subvolume, ship)` is recorded **only after a
    successful ship** under `/var/lib/bentoo/snapshot/parents/`, so a failed ship
    never breaks the chain.
  - **Object layout:** each object is stored at `<remote>/<subvolume>/<id>.zst`, so
    the subvolume is a **directory** under the remote, not part of the filename. The
    directory name is the subvolume path with every byte outside `[A-Za-z0-9._-]`
    replaced by `-`, with no special case: `/home` becomes `-home`, and the root
    subvolume `/` becomes the directory `-`.
  - **Archive retention (GFS), per subvolume:** rclone has no retention of its own,
    so after a successful ship bentoolkit lists **that subvolume's directory**
    (`rclone lsjson <remote>/<subvolume>`), applies a grandfather-father-son policy
    from `[engine.retention]`, and deletes out-of-policy objects — but **never the
    active parent**. Retention is decided **within one subvolume**: a subvolume's
    snapshots compete only with each other, so adding a second subvolume never
    shortens the first one's history. `bentoo snapshot prune` applies the same
    policy independently to each configured subvolume, and a subvolume nothing has
    been shipped for yet is simply skipped with a warning, not an error.
  - **Nothing outside the layout is ever deleted.** A prune only ever lists a
    configured subvolume's own directory, so any other object in the bucket — put
    there by hand, by another tool, or by an older layout — is never a deletion
    candidate. Directory entries are never passed to `rclone deletefile`.

**Restore.** `bentoo snapshot restore <id> --target <path> --ship <name>` dispatches
by the ship's driver. An `archive` restore **validates the full + delta chain before
applying** and refuses a broken chain *before* any `btrfs receive`. Restore is
destructive: it requires `--yes` or an interactive `[y/N]` confirmation.

**`--subvolume` — which subvolume to read from.** Because each subvolume has its own
directory on the remote, a restore has to know which one to read. With **exactly one**
subvolume configured — the common case — it is inferred and the flag is not needed.
With **two or more**, `--subvolume` becomes **required**: without it the command
exits non-zero, naming the configured subvolumes, **before any subprocess runs**.
Naming a subvolume that is not configured fails the same way. The check is applied
whichever driver the named ship uses, including `restic`, which discards the value —
a gate that depends on the driver is a gate someone has to remember to extend.

```bash
# One subvolume configured: unchanged, no new flag
bentoo snapshot restore 42 --target /mnt/restore --ship offsite --yes

# Two or more: name the one to read from
bentoo snapshot restore 42 --target /mnt/restore --ship offsite --subvolume /home --yes
```

**Secrets.** Only secret **paths** (`password_file`) and rclone's own config/env are
passed — never secret **values** in argv or TOML — and passwords/tokens are never
written to logs or error messages.

**Notes.** restic re-scans the subvolume locally each run (dedup avoids re-upload but
the scan still happens — fine for typical subvolumes). For `archive` incremental
chains, deleting a mid-chain delta would break restorability of later snapshots;
GFS is fully safe for `mode = "full"`, and restore-time chain validation is the
backstop for incremental.

### Rollback (snapper engine)

With `engine.driver = "snapper"` the same config drives **local timeline
snapshots and system rollback** — the "undo a broken update" path. btrbk is
built for backup/replication; snapper is the rollback engine. The driver is
additive: switching back to btrbk changes nothing in existing behavior.

- **Configs.** `apply` renders `/etc/snapper/configs/<name>` per subvolume
  (`/` → `root`, `/home` → `home`) idempotently: bentoo-managed keys
  (`SUBVOLUME`, `TIMELINE_*` limits from `[engine.retention]`,
  `NUMBER_CLEANUP`) are kept in sync while user-added settings and comments are
  preserved.
- **Pipeline.** `run` creates tagged timeline snapshots
  (`snapper create --description "bentoo snapshot"`); prune delegates to
  `snapper cleanup timeline` (native retention, as with btrbk).
- **Rollback.** `bentoo snapshot rollback <id>` runs `snapper -c root rollback`.
  It is destructive, so it requires `--yes` or an interactive `[y/N]` confirm —
  and it is **refused with a clear error when the active engine is not
  snapper** (rollback is snapper-specific; declining is a clean abort).
- **Emerge hook (opt-in).** `bentoo snapshot hook --install` installs a Portage
  hook (`/etc/portage/bashrc.d/50-bentoo-snapshot.sh`, sourced through a
  managed block in `/etc/portage/bashrc`) that creates snapper **pre/post
  snapshot pairs around each package emerge builds** — so a broken update has
  a known-good "pre" to roll back to. `--uninstall` removes it cleanly,
  preserving your own bashrc content. The hook is **never** installed by
  `apply`, and a snapper failure never breaks an emerge.
- **Boot integration.** grub-btrfs / boot-into-snapshot integration is a
  documented follow-up, not part of this release.

### Scope

This release covers the config model, the `btrbk` engine + `ssh`/`restic`/`archive`
shippers, systemd timer generation, dependency detection, run notifications
(ntfy / healthchecks / webhook / email), cloud backup + restore, the `snapper`
engine with system rollback + the opt-in emerge hook, full `--dry-run` coverage,
the on-demand `prune` verb, remote listing (`list --remote`), per-stage `status`
with the next scheduled run, and the Portage USE-flag mapping for every optional
backend. grub-btrfs / boot-into-snapshot integration remains a documented
follow-up.
