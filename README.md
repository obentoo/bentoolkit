# Bentoolkit

[![Release](https://img.shields.io/github/v/release/obentoo/bentoolkit)](https://github.com/obentoo/bentoolkit/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/obentoo/bentoolkit)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Command-line tools for maintainers of the [Bentoo](https://github.com/obentoo/bentoo)
Gentoo overlay: keep its ebuilds current with upstream, validate every bump
before it ships, publish notices, and protect the machine with btrfs snapshots.

## Modules

| Command | What for |
|---|---|
| `bentoo overlay` | The overlay's git workflow (`status`, `add`, `commit` with a generated message, `push`, `pull`), `compare` against `::gentoo`, `prune` what `::gentoo` already ships identically, `manifest`, `rename` |
| `bentoo overlay autoupdate` | Read each package's upstream version (`--check`), then stage, validate and commit the bump (`--apply`); `packages.toml` says where each version is read |
| `bentoo overlay validate` | Check that an ebuild still matches the source it points at, on a ladder of depths from build options to a full install |
| `bentoo distfile fetch` | Download a distfile Portage cannot fetch by itself (behind a registration form or a POST) into `DISTDIR` |
| `bentoo notice` | Write a notice once, as a GLEP 42 news item in the overlay and an entry in the website's feed |
| `bentoo snapshot` | Declarative btrfs snapshots: btrbk and snapper, cloud copies with restic or rclone, systemd timers, rollback |
| `bentoo-tray` | A desktop notifier that announces the notices that concern the packages you have installed, and warns when disk space, memory, CPU or the network degrade |

## Installation

Add the Bentoo overlay, then install the packages:

```bash
eselect repository add bentoo git https://github.com/obentoo/bentoo.git
emerge --sync bentoo
emerge --ask app-portage/bentoolkit    # bentoo
emerge --ask app-portage/bentoo-tray   # optional: the desktop notifier
```

Or build from source. Go comes from the `toolchain` line in `go.mod`, which
`go` downloads on first use:

```bash
git clone https://github.com/obentoo/bentoolkit.git
cd bentoolkit
make build                # build/bentoo and build/bentoo-tray
sudo make install         # into /usr/local, with the tray's desktop entry and user unit
```

Builds are reproducible: `-trimpath`, and a build date taken from
`SOURCE_DATE_EPOCH` or the last commit.

## Quick start

```bash
make install-config       # or copy config.example.yaml to ~/.config/bentoo/config.yaml
$EDITOR ~/.config/bentoo/config.yaml      # set overlay.path to your checkout

bentoo overlay status                      # what changed in the overlay
bentoo overlay autoupdate --check          # which packages have a newer upstream
bentoo overlay autoupdate --list           # the updates found, waiting to be applied
bentoo overlay autoupdate --apply all      # stage, validate and commit them
```

`bentoo --help` lists every command, and `bentoo <command> --help` explains one.
Secrets (API tokens) never go in the config file; see
[Configuration](docs/configuration.md).

## bentoo-tray health monitors

Besides notices, `bentoo-tray` watches the machine it runs on and raises a
desktop notification when something degrades that would break or stall an
`emerge`. A reading must stay bad for the monitor's sustain window before it
notifies, so a short spike stays silent. Every monitor is on by default.

| Monitor | Watches | Warning | Critical | Default sustain |
|---|---|---|---|---|
| disk | `/`, `PORTAGE_TMPDIR` and `DISTDIR`, as `portageq envvar PORTAGE_TMPDIR` and `portageq distdir` report them at startup | under 10% free AND under 20 GiB free | under 2% free OR under 2 GiB free | 1 min |
| memory | `MemAvailable` in `/proc/meminfo`, and `/proc/pressure/memory` | under 10% of RAM available | under 5% available with no swap or under 10% of swap free, or `full avg60` memory pressure at 10 or more | 2 min |
| cpu | `some avg60` in `/proc/pressure/cpu` | 80 or more | never | 5 min |
| network | NetworkManager's connectivity on the system bus | "Network down" after 30 s offline (60 s when the tray starts offline); "Network back" after 10 s online | never | - |
| emerge | `/var/log/emerge.log`, read from where it was when the tray started | a build that started and did not complete before an unsuccessful exit, naming the package and its `build.log` when it still exists | never | - |
| units | failed systemd units of the system manager and of your session's manager, through D-Bus | a unit entering `failed`, and every unit already failed when the tray starts | never | - |
| readonly | `/proc/self/mountinfo`, every block-device filesystem (`/dev/...`; not tmpfs, squashfs, overlay, network, FUSE or ISO) | never | a filesystem seen read-write that is now read-only (the kernel's reaction to an I/O error) | - |
| smart | udisks2's SMART verdict for every drive, on the system bus | never | an ATA drive reporting itself failing, or an NVMe drive with a critical warning (spare, temperature, degraded, read-only...) | - |
| updates | `emerge --pretend --update --deep --newuse --quiet @world`, run by the update check | never (a low-urgency notice naming the count and the first packages; "@world does not resolve" when emerge exits non-zero) | never | - |
| glsa | `glsa-check --list affected --nocolor` (app-portage/gentoolkit), run by the update check before `@world` | a GLSA that applies to an installed package, naming its id, title and atoms | never | - |
| sync | every repository in `repos.conf` with a `sync-uri`: the newest of `metadata/timestamp.chk` and `.git/FETCH_HEAD` | not synced for more than `sync.max_age` (7 days), naming the repository and the whole days | never | - |

How an alert behaves:

- **Free disk space** is the space an unprivileged user can write (root's
  reserved blocks do not count). Paths on the same filesystem are announced
  once, under the first path. On btrfs the kernel's free figure is an estimate.
- **CPU pressure is ignored while an `emerge` runs**: a build is expected to
  saturate the CPU. Disk and memory are still watched during a build.
- **One notification per problem.** A later notification for the same problem
  replaces the earlier one instead of stacking. A warning is sent once; a
  critical alert is sent at once (also when a warning gets worse) and
  repeated every `reminder` (1 h) while it lasts. Falling back from critical
  to warning sends nothing.
- **Recovery is quiet.** A disk, memory or CPU alert clears only when the
  reading stays 20% past its threshold for the sustain window (CPU: below
  `clear_psi`, 70); its notification is then withdrawn. "Network back" is sent
  only after a "Network down" was shown, and replaces it.
- **Pause** (tray menu) silences warnings, the updates notice and network
  notifications, never critical alerts. A warning or notice raised during a
  pause is not sent when it ends; its tooltip line stays.
- **The icon** asks for attention, and turns to its critical variant for a
  critical alert, while any alert is active; the tooltip lists one line per
  alert (GNOME's AppIndicator extension shows no tooltip). Alerts never enter
  the notices menu, the unread count or `state.json`, and "Mark all as read"
  leaves them alone. Clicking an alert's notification opens nothing.
- **Nothing is kept across restarts**: after a restart a problem still present
  is announced again once its sustain window has passed.
- **Errors notify at once** (emerge, units, readonly, smart have no sustain
  window). A failed emerge clears when a later emerge ends successfully or the
  failed package completes; Ctrl+C and dependency failures, which you already
  saw in the terminal, never notify. Failed units share one notification,
  replaced by each new failure and withdrawn when the last unit recovers. A
  read-only filesystem clears when it is read-write again or unmounted; a
  failing drive when its SMART verdict is healthy again or it is removed.
- **Pending updates are a notice, not an alert**: a low-urgency
  notification and a tooltip line, no attention icon, never repeated. It is
  replaced only when the set of packages changes and withdrawn when nothing is
  left. A GLSA or a stale repository is a warning; all GLSAs share one
  notification, withdrawn when the last one clears.
- **The update check runs** after every sync (`/var/lib/portage/repo_revisions`
  changed), 5 minutes after start, and after each successful `emerge` (needs
  the emerge monitor). Never during an `emerge`: it waits until none runs.
- **It costs** about 50 s of CPU for `@world`, at niceness 19 with the idle
  I/O class, in its own process group. `@world` is killed after 10 min and
  `glsa-check` after 2 min (one WARN, state kept); Quit kills a running check.

Configure them under `tray.monitors` in `~/.config/bentoo/config.yaml`;
[config.example.yaml](config.example.yaml) lists the same keys with their
defaults. Keys under `tray.monitors`:

| Key | Default | Meaning |
|---|---|---|
| `sample_interval` | `30s` | time between two readings; minimum `10s` |
| `reminder` | `1h` | how often a critical alert repeats; minimum `15m` |
| `disk.enabled` | `true` | watch free disk space |
| `disk.paths` | `[]` | the paths to watch; empty means `/`, `PORTAGE_TMPDIR` and `DISTDIR`. A configured list replaces the defaults |
| `disk.warn_percent` | `10` | warning under this percentage free AND under `disk.warn_free` |
| `disk.warn_free` | `20GiB` | see `disk.warn_percent` |
| `disk.crit_percent` | `2` | critical under this percentage free OR under `disk.crit_free` |
| `disk.crit_free` | `2GiB` | see `disk.crit_percent` |
| `disk.sustain` | `1m` | how long a disk reading must last |
| `memory.enabled` | `true` | watch memory |
| `memory.warn_percent` | `10` | warning under this percentage of RAM available |
| `memory.crit_percent` | `5` | critical under this percentage available, with swap absent or under `memory.crit_swap_percent` free |
| `memory.crit_swap_percent` | `10` | see `memory.crit_percent` |
| `memory.psi_full_crit` | `10.0` | critical when memory pressure `full avg60` reaches this |
| `memory.sustain` | `2m` | how long a memory reading must last |
| `cpu.enabled` | `true` | watch CPU pressure |
| `cpu.warn_psi` | `80.0` | warning when CPU pressure `some avg60` reaches this |
| `cpu.clear_psi` | `70.0` | the alert clears below this; it must be below `cpu.warn_psi`. With `cpu.warn_psi` at 70 or less the default is `cpu.warn_psi` x 70/80 |
| `cpu.sustain` | `5m` | how long a CPU reading must last |
| `network.enabled` | `true` | watch connectivity |
| `network.down_after` | `30s` | offline this long sends "Network down" |
| `network.down_after_startup` | `60s` | the same, when the tray starts offline |
| `network.back_after` | `10s` | online this long, after a "Network down", sends "Network back" |
| `emerge.enabled` | `true` | announce failed emerge builds (block `tray.monitors.emerge`) |
| `emerge.log` | `/var/log/emerge.log` | the emerge log to follow; an absolute path |
| `units.enabled` | `true` | announce failed systemd units (block `tray.monitors.units`) |
| `units.system` | `true` | watch the system manager |
| `units.user` | `true` | watch your session's manager (`systemd --user`) |
| `readonly.enabled` | `true` | announce read-only remounts (block `tray.monitors.readonly`) |
| `smart.enabled` | `true` | announce failing drives through udisks2 (block `tray.monitors.smart`) |
| `updates.enabled` | `true` | announce pending `@world` updates (block `tray.monitors.updates`) |
| `glsa.enabled` | `true` | announce applicable GLSAs (block `tray.monitors.glsa`) |
| `sync.enabled` | `true` | announce repositories not synced recently (block `tray.monitors.sync`) |
| `sync.max_age` | `168h` | a repository not synced for longer than this is stale; minimum `24h` |

Percentages are 0-100, sizes whole `KiB`, `MiB`, `GiB` or `TiB`, durations
like `30s`, `2m` or `1h` (0 is accepted, a negative value is not). A bad or
out-of-range value logs a WARN naming the key and uses its default; an unknown
key logs a WARN and the tray still starts. For example, to watch `/home` too
and turn the CPU monitor off:

```yaml
tray:
  monitors:
    disk:
      paths: [/, /var/tmp, /var/cache/distfiles, /home]
    cpu:
      enabled: false
```

Limits worth knowing:

- **`/home` is not watched by default**: KDE Plasma (freespacenotifier) and
  GNOME (gsd-housekeeping) already warn about it. Add it to `disk.paths` if
  you want it here too.
- **With `/proc` mounted `hidepid=2`** (check with `findmnt -no OPTIONS
  /proc`), the tray sees only your own processes. `emerge` runs as root, so it
  cannot see a build, and the CPU monitor is no longer silent during one.
  Disable `cpu.enabled` on such a host if that is noisy.
- **No NetworkManager, no network monitor**: the tray logs one INFO line at
  startup and sends no network notification.
- **No pressure stall information** (a kernel without PSI, so no
  `/proc/pressure/cpu`): the CPU monitor turns itself off with one INFO line;
  the memory monitor uses `/proc/meminfo` alone.
- **The emerge log is read through the `portage` group** (it is
  `portage:portage 0660`): outside that group the tray logs one WARN and keeps
  retrying. emerge.log carries no process id, so with two emerge runs at once,
  or after an emerge killed without its closing line, a failure may name the
  other run's package until that package completes.
- **ATA SMART needs `sys-fs/udisks[smart]` and `sys-libs/libblockdev[smart]`**:
  without them udisks2 reports SMART as unsupported and no ATA failure is
  announced. NVMe health needs udisks2 2.10 or later. Without udisks2 the
  monitor turns itself off with one INFO line.
- **No systemd** (or no `systemd --user`): that manager is not watched, with
  one INFO line at startup.
- **No `glsa-check`** (app-portage/gentoolkit): the GLSA monitor turns off
  with one INFO line; no `emerge`, the updates monitor with one WARN.
- **A repository with neither `metadata/timestamp.chk` nor `.git/FETCH_HEAD`**
  is skipped with one INFO line. `repos.conf` is read once, at startup.
- **The shared GLSA notification names the last GLSA raised**, even after
  that one clears while another still applies.
- Not covered: inode exhaustion, the write state of network, FUSE or image
  filesystems, drive attribute details and self-tests, OpenRC services, which
  process uses the memory, temperature, battery, network quality.

## Documentation

- [Configuration](docs/configuration.md): the config file, the secrets file and logging
- [Overlay commands](docs/overlay.md): from status to prune, with a typical workflow
- [Autoupdate](docs/autoupdate.md): `packages.toml`, the record model and the LLM providers
- [Distfiles](docs/distfiles.md): gated distfiles, and where updates download them
- [Notices](docs/notices.md): one notice as a news item and a site entry
- [Snapshots](docs/snapshot.md): btrbk, snapper, cloud copies and rollback
- [Desktop notifications](docs/tray.md): installing and configuring `bentoo-tray`
- [Runtime behaviour](docs/behaviour.md): exit codes, live output, concurrency and timeouts
- [Development](docs/development.md): the checks, the local CI gate and the project layout
- [Architecture](ARCHITECTURE.md): how the code is put together, and why

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). In short: `make check` while you work
(`make test` for the tests alone), then `./scripts/ci-vm-gate.sh`, which runs
the whole CI (tests, linters, CodeQL, scanners) on a clean checkout in a local
VM.

Report security problems privately, as [SECURITY.md](SECURITY.md) describes.

## License

MIT. See [LICENSE](LICENSE).
