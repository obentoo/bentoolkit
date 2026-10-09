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
| `bentoo-tray` | A desktop notifier that announces the notices that concern the packages you have installed |

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
