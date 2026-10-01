# bentoo-tray — manual E2E checklist

Run before a release that changes `cmd/bentoo-tray`, `internal/tray` or
`internal/desktop`. The automated tests prove the D-Bus contracts against a
private `dbus-daemon` with fake peers; only a real desktop proves what a user
sees. Record the date, the commit and the result of every case.

## Environments

| # | Environment | How |
|---|---|---|
| E1 | KDE Plasma 6, Wayland | the maintainer's session |
| E2 | GNOME 51, Wayland, with `gnome-shell-extension-appindicator` enabled | KVM/libvirt VM |
| E3 | GNOME 51, Wayland, without the extension | same VM, extension disabled |

## Setup (each environment)

- [ ] Install with `sudo make install PREFIX=/usr` and start with
      `systemctl --user enable --now bentoo-tray.service`
- [ ] Point `tray.feed_url` at a test feed you control (https, same host as its
      notice URLs) holding: one `security`/`critical` notice that applies to an
      installed bentoo package, two `info` notices, and one notice for a
      package that is not installed
- [ ] Remove `~/.local/state/bentoo-notices/state.json` for a first run, then
      repeat the cases below with a saved state
- [ ] `BENTOO_TRAY_LOG_LEVEL=debug`; follow `journalctl --user -u bentoo-tray -f`

## Cases

| Case | Expected | E1 | E2 | E3 |
|---|---|---|---|---|
| Icon state, nothing unread | plain icon, status Active | | | n/a |
| Icon state, unread `info` | orange-dot icon | | | n/a |
| Icon state, unread `critical` | red-dot icon | | | n/a |
| Tooltip | states the unread count (GNOME's extension shows no tooltip) | | n/a | n/a |
| First run | only the applicable security notice is notified; the rest are marked read | | | |
| Critical notification over Do Not Disturb | the critical notice is shown although DND is on | | | |
| Open (notification default action) | the browser opens the notice page and comes to the front; the notice becomes read | | | |
| Mark as read (notification action) | the notice becomes read; the icon updates | | | |
| Menu | 10 newest unread titles, "N more…" when more exist, Check now, Mark all as read, Pause for 1 hour, Pause until tomorrow, Quit | | | n/a |
| Menu entry | opens that notice; it becomes read; the menu label is the notice's title after a later check changes the list | | | n/a |
| Pause for 1 hour / Pause until tomorrow | Resume replaces the pause entries; a new non-critical notice is not notified; a critical security notice still is | | | n/a |
| Resume | held notifications are sent, grouped when more than 3 | | | n/a |
| Screen lock / unlock | the icon comes back within 5 s after unlock | | | n/a |
| No tray (E3) | one WARN naming `gnome-shell-extension-appindicator`; notifications still work | n/a | n/a | |
| Quit | the process exits 0 within 5 s; the unit does not restart it | | | n/a |
| Logout | SIGHUP / SIGTERM; exit 0; `state.json` saved with mode 0600 | | | |
| Second instance | `bentoo-tray` started again exits 0 with an INFO "already running" | | | |
