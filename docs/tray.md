# Bentoolkit desktop notifications

Back to the [README](../README.md).

## Desktop Notifications (bentoo-tray)

`bentoo-tray` is a small session daemon that reads the overlay's notices feed
(`https://obentoo.org/notices.json`) and the unread bentoo news items, keeps
the ones that concern the packages installed from the bentoo repository, and
shows them as desktop notifications and a tray icon with a menu. It is a
separate binary from `bentoo`; one instance runs per session (it owns the
session-bus name `org.obentoo.BentooTray`).

### Install

`make install` installs, under `$(DESTDIR)$(PREFIX)` (`PREFIX` defaults to
`/usr/local`):

| File | Path |
|---|---|
| binary | `$(PREFIX)/bin/bentoo-tray` |
| desktop entry | `$(PREFIX)/share/applications/bentoo-tray.desktop` |
| systemd user unit | `$(PREFIX)/lib/systemd/user/bentoo-tray.service` |
| icons | `$(PREFIX)/share/icons/hicolor/scalable/apps/bentoo-tray{,-unread,-critical}.svg` |

```bash
make build
sudo make install PREFIX=/usr
```

### Versioning

`bentoo-tray` has a version of its own, independent of bentoolkit's release
number. It lives in `internal/tray/version/VERSION` and is embedded into the
binary at build time, so `make build`, a distribution package and a plain
`go build` all report the same number with no build flag.

A change to the tray bumps that file in the same change, following
semantic versioning. The tray still ships inside the bentoolkit release
tarball, so `--version` names both — the tray first, then the release it was
built from:

```text
$ bentoo-tray --version
bentoo-tray version 0.1.0
  bentoolkit: 0.33.1
```

The feed request carries the tray's version too, as
`User-Agent: bentoo-tray/<version>`.

### Autostart

Pick one — both start it with the graphical session:

```bash
# XDG autostart (any desktop)
cp /usr/share/applications/bentoo-tray.desktop ~/.config/autostart/

# or the systemd user unit (restarts it on failure)
systemctl --user enable --now bentoo-tray.service
```

With `PREFIX=/usr/local`, the desktop entry is under
`/usr/local/share/applications/`.

### GNOME

GNOME has no tray of its own. Notifications work without anything else; to see
the icon and its menu, install the AppIndicator extension
(`gnome-shell-extension-appindicator`) and enable it. Without it, `bentoo-tray`
logs one WARN naming the extension and runs with notifications only. KDE Plasma
shows the icon natively.

The extension does not show tooltips, so the unread count in the icon's tooltip
appears on KDE only; on GNOME the orange or red dot on the icon tells you there
are unread notices.

### Opening a notice

Open (on a notification or a menu entry) goes through the desktop portal, and
falls back to `xdg-open` when the portal is missing or fails. When an
administrator has locked down the portal so that it refuses to open links,
`bentoo-tray` respects that: it does not fall back to `xdg-open`, keeps the
notice unread and logs a WARN naming it.

### Configuration

The optional `tray:` section of `~/.config/bentoo/config.yaml`:

```yaml
tray:
  interval: 6h            # between feed checks; minimum 1h
  feed_url: https://obentoo.org/notices.json   # https only
  mute: [release]         # release, news, announcement — security cannot be muted
  downgrade_critical: false
  skip_metered: true      # skip scheduled checks on a metered connection
```

State (read/unread, the feed's ETag and serial, a pause, and when the next
check may run) lives in `$XDG_STATE_HOME/bentoo-notices/state.json`
(`~/.local/state/bentoo-notices/state.json` by default). Because the next
check time is kept there, a restart does not check again sooner than the server
asked (`Retry-After`) or the backoff allows; a wait inherited from a previous
run is capped at the larger of 1.2 × `interval` and 24 hours.

### Logging

`bentoo-tray` logs one key=value event per line to standard error — the
journal when it runs as the user unit (`journalctl --user -u bentoo-tray`).
Set the level with `BENTOO_TRAY_LOG_LEVEL` (`debug`, `info` — the default —,
`warn` or `error`):

```bash
BENTOO_TRAY_LOG_LEVEL=debug bentoo-tray
```

Exit codes: `0` on a clean stop (SIGINT, SIGTERM, SIGHUP or Quit), including a
stop signal received while the tray is still starting, and when an instance is
already running; `1` on a startup failure (session bus unreachable,
unreadable state file); `2` when the session bus is lost while running.

### Running the D-Bus tests

The tray's integration tests start private `dbus-daemon`s, so they need
`dbus-daemon` on `PATH` (`sys-apps/dbus`). Without it they are skipped locally
and fail when `CI=true`:

```bash
go test -race ./internal/desktop/... ./cmd/bentoo-tray/...
CI=true go test -race ./internal/desktop/...   # as CI runs them
```

The manual desktop matrix (KDE Plasma 6, GNOME with and without the extension)
is in `misc/tray/E2E-CHECKLIST.md`.
