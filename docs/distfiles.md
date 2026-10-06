# Bentoolkit distfiles

Back to the [README](../README.md).

### Distfile Commands

#### Fetch a gated distfile

Some vendors do not let a mirror carry their archive: the download is behind a
registration form, or behind a POST that cannot be replayed from a URL. Portage
cannot follow such a `SRC_URI`, so `emerge` stops and runs the ebuild's
`pkg_nofetch`, which tells you to fetch the file by hand.

When the overlay's `packages.toml` records how that download is performed (the
`meta.fetch_*` keys below), one command performs it for you:

```bash
# Fetch the distfile for the version the overlay currently carries
bentoo distfile fetch app-misc/example

# Fetch the one a specific ebuild needs
bentoo distfile fetch app-misc/example --version 3.70.5

# Two records for one atom (release lines, slots): name the record, not the atom
bentoo distfile fetch app-office/libreoffice@testing
```

- **Where it lands.** The host's own `DISTDIR`, as reported by
  `portageq distdir` — never a path assumed by bentoo — so `emerge` finds the
  file without being told. `--distdir` overrides it. The directory must be
  writable by you: on most systems `DISTDIR` is group-writable by the `portage`
  group, and the run stops with the directory named when it is not.
- **Under what name.** Exactly the name the record's `fetch_filename` resolves
  to, which is the basename the ebuild's `SRC_URI` expects — the point of the
  command is that the file passes the Manifest check.
- **Which version.** The highest ebuild the overlay carries for that record
  (honouring its `series`/slot filter), unless `--version` names another. Pass
  the version `emerge` asked for whenever you are installing anything but the
  newest ebuild.
- **The serial, when there is one.** Read at runtime from the environment
  variable the record names, through the [secrets](configuration.md#secrets) chain. It is never
  written to the overlay, the logs, or the command's output — the variable's
  NAME is printed, its value never is.

It is the same download `bentoo overlay autoupdate` performs before it
regenerates a Manifest — same request, same guards, same file name — so what
this writes is what the Manifest was computed against.

### Distfiles

Applying an update regenerates the package's `Manifest`, which means `pkgdev`
downloads and digests the new distfile. Two flags choose where that happens —
the same two names, with the same meaning, that `bentoo overlay manifest`
already uses:

| Flag | Config key | Default |
|---|---|---|
| `--distdir` | `autoupdate.distdir` | the DISTDIR this machine's own package manager reports (`portageq distdir`, normally `/var/cache/distfiles`) |
| `--distfiles-cache` | `autoupdate.distfiles_cache` | `/var/cache/distfiles` |

The flag wins over the config key. Note the default is **not** a temporary
directory: a distfile fetched by one run is a distfile the next run and every
`emerge` can reuse. Pass `--distdir` to work somewhere else, and
`--distfiles-cache ""` to disable the cache lookup (only the flag can disable
it — an empty config key is indistinguishable from an absent one).

A relative path in either **config key** is refused, because it would resolve
against whatever directory the process happened to start in. Flags stay
permissive.

Because that directory is shared with the host rather than private to the run:

- A distfile already present under a name the package's current `Manifest`
  lists is reused, not downloaded again.
- A distfile present under a name the `Manifest` does **not** list cannot be
  verified, so it is **moved aside** — not deleted — and reported. Portage's
  default `FETCHCOMMAND` writes straight to the final filename, so a download
  killed midway leaves a truncated file under the name a digest would otherwise
  bless.
- A failed fetch removes only what that run created. A file that was already
  there is never touched, whoever wrote it.
- Each distfile is locked for the duration of the `pkgdev` run that needs it, so
  the sweep's concurrent workers — and two `bentoo` runs sharing a distdir —
  cannot fetch the same file at once.
- A directory bentoo did not create is never removed.

#### Running a sweep at the same time as `emerge`

The locking above covers bentoo against itself. It does **not** extend to
portage.

Portage serialises its own downloads with `FEATURES=distlocks`, which is enabled
by default — it takes an advisory lock on a sidecar named
`.<distfile>.portage_lockfile`. But `pkgdev`, the tool bentoo calls to generate
manifests, does not take that lock. It fetches through pkgcore, which implements
no distfile locking at all and does not recognise `distlocks` (the setting is
not disobeyed so much as unheard of). A sweep and an `emerge` that want the same
distfile at the same moment are therefore **not** serialised against each other,
and whichever finishes second can be left with a corrupt or truncated download.

This is a limitation of `pkgdev`, not something bentoo can fix from the outside;
coordinating with another package manager's internal locking is not a promise
this tool makes. If it matters to you, avoid running an autoupdate sweep while an
`emerge` is fetching, or give the sweep a directory of its own with `--distdir`.
