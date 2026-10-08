# Architecture

How bentoolkit is put together, and why. For what each command does, see the
pages under [docs/](docs/); for how to work on the code, see
[CONTRIBUTING.md](CONTRIBUTING.md) and [docs/development.md](docs/development.md).

## Two binaries, one module

| Binary | Source | What it links |
|---|---|---|
| `bentoo` | `cmd/bentoo` | cobra, Bubble Tea, goquery/htmlquery, gobreaker; chromedp only when built with `-tags chromedp` |
| `bentoo-tray` | `cmd/bentoo-tray` | godbus and the config, secrets and notices packages; no cobra, no Bubble Tea, no chromedp |

The tray is a small session daemon, so it is kept apart from everything the
CLI needs. It also carries its own version (`internal/tray/version`), which
moves only when the tray changes.

## Layers

Dependencies point one way:

```
cmd/bentoo, cmd/bentoo-tray          composition: flags, wiring, rendering
        │
        ▼
internal/autoupdate, overlay,        domain: what bentoo does
snapshot, notice, realign, tray,
gentoo/*, desktop/*
        │
        ▼
internal/common/*                    infrastructure shared by the domain
```

- **`internal/common` never imports a domain package.** If shared code needs
  domain knowledge, it belongs in the domain.
- **`cmd/bentoo` builds services and hands them their dependencies** through
  the `deps` struct, so a test can replace any of them. Older commands still
  keep flags in package-level variables; `.golangci.yml` lists them, and the
  list only shrinks.
- `go list ./...` is the authoritative package list, and every package's
  doc comment (`go doc <package>`) says what it is for.

## Decisions worth knowing before changing the code

### Processes and signals

Long-running children (pkgdev, the unprivileged `ebuild`, the `claude` CLI,
git for providers) run in **their own process group** (`internal/common/procgroup`),
so a cancel stops them and every helper they started. The cost of that move is
that the terminal's Ctrl+C and hang-up no longer reach them, so the parent must
cancel their context. Every command therefore runs under one process-wide
signal context that also handles SIGHUP. Commands marked `cancellable` cancel
that context on a signal; the rest re-raise it and exit.

Anything that must prompt on the terminal (sudo, git credentials, a pager)
stays in the terminal's **foreground** group instead.

### State shared between processes

Two `bentoo` processes may run at once, for example a timer and an operator.
So:

- JSON state files (`pending.json`, caches) are read, merged and saved under
  an inter-process lock (`internal/autoupdate/statefile`), and a write never
  drops another process's changes.
- Every write that must survive a crash goes through one audited sequence:
  temp file, sync, rename (`internal/common/fileutil`).
- The distfiles directory Portage shares is guarded by its own lock
  (`internal/common/distfiles`).

### Secrets

Secrets never live in `config.yaml`. They resolve from a fixed chain
(`internal/common/secrets`): the environment, then the user's
`~/.config/bentoo/secrets`, then `/etc/bentoo/secrets`. The logger
(`internal/common/logging`, on `log/slog`) redacts every resolved secret and
every credential-named key, so a secret that reaches a log line is masked.

### The autoupdate pipeline

```
packages.toml ─► check ─► pending.json ─► apply
 (registry)     fetch upstream,          stage the bump, validate it on the
                parse the version,       depth ladder, regenerate Manifest,
                compare                  commit
```

- **Registry** (`internal/autoupdate/registry`): one record per package, saying
  where the upstream version is read and how.
- **Check**: fetch (`fetch`: rate limits, a circuit breaker, caches) and parse
  (`parse`: JSON, regex, HTML/XPath; a headless browser only with
  `-tags chromedp`).
- **Apply** works on a staged copy and validates it on a ladder of depths
  (`validate`): none, options, patches, configure, compile, install. The depth
  depends on how far the version moved, and per-package overrides must state
  a reason.
- **LLM agents** (`fixer`, `llm`) only repair, and only through the local
  `claude` CLI with a least-privilege permission profile. A failure the machine
  caused (a full disk, an unreadable file) is classified as the host's and
  never handed to an agent. When in doubt, the classifier fails open: it
  wastes a build rather than suppressing a package.

### Notices and the tray

`bentoo notice` writes one notice twice: a GLEP 42 news item in the overlay,
and a YAML file for the website, which publishes a JSON Feed. `bentoo-tray`
reads that feed (and Portage's own news, offline), matches it against the
installed packages, and announces what applies. Its tray icon is a
StatusNotifierItem implemented directly on D-Bus (`internal/desktop/sni`), so
the daemon needs no GUI toolkit.

### Snapshots

`bentoo snapshot` orchestrates existing tools from one declarative TOML file:
btrbk for snapshots and replication, snapper for system rollback, restic and
rclone for cloud copies, and systemd timers for scheduling. It drives them; it
does not reimplement them.

## How it is verified

- Every change runs `go test -race -shuffle=on` with at least 80% total
  coverage, golden files for rendered output, property tests (gopter) for
  parsers, fuzz targets for the inputs bentoo reads from outside, and goleak
  in `autoupdate` and `overlay`.
- golangci-lint runs with a strict configuration whose reasons are written next
  to each rule. Debt that existed when a rule was enabled is listed by name, and
  those lists only shrink.
- The project's CI runs locally: `scripts/ci-vm-gate.sh` reproduces every job
  on a clean checkout in a KVM guest. GitHub runs only what has no local
  substitute (CodeQL). See [docs/development.md](docs/development.md).
