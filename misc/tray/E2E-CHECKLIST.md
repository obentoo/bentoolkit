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

## Health monitors

The disk, memory, CPU and network monitors. In E3 (no icon) check only the
notification part of each expected result. The memory case can trigger the
OOM killer: save your work, or run it in the VM.

### Setup (each environment)

- [ ] Install `app-benchmarks/stress-ng`
- [ ] Create a 30 GiB test filesystem owned by you, attach it as a loop
      device and mount it (sparse: it uses almost no real disk):

      ```bash
      truncate -s 30G /var/tmp/bt-health.img
      mkfs.ext4 -q -m 0 -L bthealth -E root_owner="$(id -u):$(id -g)" /var/tmp/bt-health.img
      udisksctl loop-setup -f /var/tmp/bt-health.img   # prints /dev/loopN
      udisksctl mount -b /dev/loopN                    # mounts /run/media/$USER/bthealth
      export M=/run/media/$USER/bthealth
      avail() { df -BM --output=avail "$M" | tail -1 | tr -dc 0-9; }   # MiB free
      ```

- [ ] Watch only that filesystem and shorten the reminder, then restart with
      `systemctl --user restart bentoo-tray.service`:

      ```yaml
      tray:
        monitors:
          reminder: 15m
          disk:
            paths: [/run/media/<user>/bthealth]
      ```

- [ ] Follow the readings beside the journal:
      `watch -n 5 'df -h "$M"; grep -E "MemTotal|MemAvailable|SwapFree" /proc/meminfo; cat /proc/pressure/cpu /proc/pressure/memory'`
- [ ] Network cut: E1 unplugs the cable (or `nmcli networking off`, then
      `nmcli networking on`); E2/E3 run `virsh domif-setlink <vm> <iface> down`
      / `up` on the host. While cut, `nmcli networking connectivity` must read
      `none`, `portal` or `limited`
- [ ] Afterwards: `rm -f "$M"/fill*`, `udisksctl unmount -b /dev/loopN`,
      `udisksctl loop-delete -b /dev/loopN`, `rm /var/tmp/bt-health.img`, and
      restore the config

### Cases

| Case | Expected | E1 | E2 | E3 |
|---|---|---|---|---|
| Disk warning: leave 2.5 GiB free with `fallocate -l $(( $(avail) - 2560 ))M "$M/fill1"` | within 90 s one normal-urgency "Low disk space on /run/media/…/bthealth", body with the free GiB and percentage; icon asks for attention; tooltip line `Disk …` beside the unread count | | | |
| Disk critical: leave 1 GiB free with `fallocate -l $(( $(avail) - 1024 ))M "$M/fill2"` | within 90 s one critical notification that replaces the warning (one notification on screen and in the history, not two); critical icon variant; shown over Do Not Disturb | | | |
| Disk reminder: keep it full 15 min | the critical notification is sent once more, still replacing the previous one | | | |
| Disk recovery: `rm "$M"/fill*` | no new notification; the notification is withdrawn within 2 min; icon back to plain (or to the unread dot when notices are unread) | | | |
| Disk paused: Pause for 1 hour, repeat the warning, then the critical step | the warning is not notified but shows in the icon and tooltip; the critical one is notified; Resume sends nothing for a warning still active | | | n/a |
| Restart during an alert: `systemctl --user restart bentoo-tray.service` with the disk at 1 GiB free | the alert is announced again about 90 s after the start, not at once | | | |
| Memory: `stress-ng --vm 1 --vm-bytes 95% --vm-keep --timeout 20m` (raise the percentage until `MemAvailable` stays under 5% of `MemTotal` and swap is almost full) | within 3 min a critical "Memory almost exhausted" with available, total and free swap; a warning sent before it is replaced, not stacked; one more after 15 min; after Ctrl+C the notification is withdrawn within 5 min | | | |
| CPU outside emerge: `stress-ng --cpu $(( $(nproc) * 2 )) --timeout 8m` | `some avg60` in `/proc/pressure/cpu` reaches 80; about 5 min later one normal "CPU saturated", body "Pressure N% for M min"; never critical; after it stops the notification is withdrawn once the pressure stays under 70 for 5 min | | | |
| CPU during emerge: a real build (`emerge -1 sys-devel/gcc`) or `sudo emerge --ask -1 app-misc/jq` left at its prompt, plus the same `stress-ng` run | `some avg60` reaches 80, no CPU notification during the 8 min; disk and memory still notify | | | |
| Network cut for 10 s | no notification | | | |
| Network cut for 40 s | one "Network down" at about 30 s; icon asks for attention, tooltip `Network: down`; 10 s after reconnecting one low-urgency "Network back" that replaces it; icon back to plain | | | |
| Network: tray started offline | no "Network down" before 60 s after the start | | | |
| Network cut 40 s while paused | neither "Network down" nor "Network back", also after Resume | | | n/a |
| System notification click | opens nothing; no WARN in the journal | | | |
| Mark all as read with an alert active | unread notices clear; the alert stays in the icon and tooltip | | | n/a |

## Error monitors

The emerge, units, read-only and SMART monitors. Run the read-only case in the
VM (E2/E3) or on the virtual Plasma harness: it needs root for `mount`.

| Case | Expected | E1 | E2 | E3 |
|---|---|---|---|---|
| Failed build: `sudo emerge -1 <a package whose src_compile fails>` (a local ebuild with `die` in `src_compile`) | within 2 sample intervals of emerge exiting, one normal "Package build failed" naming the atom and `Log: /var/tmp/portage/…/temp/build.log`; icon asks for attention | | | |
| Ctrl+C during a build | no notification | | | |
| Failed build then a successful `emerge -1 app-misc/jq` | the notification is withdrawn; icon back to plain | | | |
| Failed unit: `systemd-run --user --unit=s098-fail false` | one normal "systemd unit failed" naming `s098-fail.service` and `user`; a second failing unit replaces it with the count 2 | | | |
| Unit recovery: `systemctl --user reset-failed` | nothing is sent; the shared notification is withdrawn once no unit is failed | | | |
| Read-only remount: as root, `truncate -s 64M /var/tmp/ro.img; mkfs.ext4 -q /var/tmp/ro.img; mount -o loop /var/tmp/ro.img /mnt/ro; mount -o remount,ro /mnt/ro` | within 2 sample intervals one critical "Filesystem read-only: /mnt/ro" with device and type, shown over Do Not Disturb; `mount -o remount,rw /mnt/ro` withdraws it | | | |
| Read-only at first sight: mount the image `-o ro` before starting the tray | no notification | | | |
| SMART (fake): on the virtual harness, export a fake `org.freedesktop.UDisks2` drive whose `SmartFailing` turns true | one critical notification at once naming model, serial and "SMART reports failing"; one more per reminder while it lasts | | | |
| Pause with a failed unit | no unit notification while paused; a read-only or SMART alert still notifies | | | n/a |

## Update monitors

The updates, GLSA and sync monitors. The update check runs `emerge
--pretend` and `glsa-check` at niceness 19; in the VM use a small tree or
expect each check to take about a minute. `BENTOO_TRAY_LOG_LEVEL=debug` shows
`update check started` and one INFO per command with `duration_s` and `found`.

### Setup (each environment)

- [ ] `app-portage/gentoolkit` installed (`command -v glsa-check`), the user in
      the `portage` group (`emerge.log` and the sync trigger)
- [ ] For the fake GLSA case: a script `/usr/local/bin/glsa-check` (first on
      `PATH` before the tray starts) printing one line
      `202610-01 [N] Test advisory ( app-misc/jq )` and exiting 0
- [ ] Afterwards: remove the fake `glsa-check`, `touch` any `FETCH_HEAD` you
      aged, and restore the config

### Cases

| Case | Expected | E1 | E2 | E3 |
|---|---|---|---|---|
| Sync with updates pending: `sudo emaint sync -a` on a tree that has updates | within 2 sample intervals of the sync ending plus the check's duration, one low-urgency notification with the count and the first two packages; the tooltip gains an `Updates:` line; the icon stays plain | | n/a | |
| Start-up check: restart the tray with updates pending | no update check before 5 min; one check about 5 min after the start | | | |
| Same set again: sync again without new updates | no new notification | | | |
| Update applied: `sudo emerge -uDN @world` | no check starts while emerge runs; after it ends successfully the updates notification is withdrawn within the next check and the tooltip line goes away | | n/a | |
| Check held by emerge: start `emerge -e @world` (or leave `sudo emerge --ask -1 app-misc/jq` at its prompt), then sync | no update check starts while emerge runs (journal); one starts within 2 sample intervals of emerge ending | | | |
| @world does not resolve: mask a package @world needs (`echo app-misc/jq >> /etc/portage/package.mask/s099` with jq in @world), then sync | one low-urgency "@world does not resolve" telling to run `emerge -puDN @world`; removing the mask and syncing brings the count back | | | |
| Fake GLSA: the fake `glsa-check` above, restart the tray, wait 5 min | one normal-urgency warning naming `202610-01`, its title and `app-misc/jq`; the icon asks for attention; tooltip line | | | |
| GLSA cleared: remove the fake script, sync | the GLSA notification is withdrawn; icon back to plain | | | |
| Shared GLSA note: fake script with two `[N]` lines, then drop the first | one notification on screen (the second replaced the first, count 2); after the first clears nothing is sent and the note keeps its text until the last clears | | | |
| Stale repository: `touch -d '8 days ago' <overlay>/.git/FETCH_HEAD` (or `metadata/timestamp.chk` of a rsync tree) | within 2 sample intervals one warning naming the repository and "8 days"; a near-identical repository name is not affected | | | |
| Repository synced again: `touch` the same file | the sync notification is withdrawn | | | |
| Pause with updates pending and a stale repository | no notification while paused; tooltip lines stay; Resume sends nothing for them | | | n/a |
| Quit during a check: Quit right after `update check started` | the tray exits within 2 s; `pgrep -f 'emerge --pretend'` finds nothing | | | n/a |
| Monitors off: `tray.monitors.updates.enabled: false` and `glsa.enabled: false` | no update check ever starts (journal); the sync monitor still works | | | |
