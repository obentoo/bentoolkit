# Bentoolkit

CLI tools for Bentoo Linux distribution maintainers and developers.

## Modules

- **overlay**: Bentoo overlay commit management, version comparison, and automated updates
- **snapshot**: declarative btrfs snapshot management orchestrating `btrbk` (snapshot + ssh replication), `snapper` (timeline + system rollback), and `systemd` timers
- **bentoo-tray**: a desktop notifier for the overlay's notices — security advisories, releases and news that concern the packages you have installed (see [Desktop Notifications](docs/tray.md#desktop-notifications-bentoo-tray))

## Installation

### Prerequisites

First, add the Bentoo overlay to your Gentoo/Bentoo system:

**Option 1: Using eselect-repository**
```bash
eselect repository add bentoo git https://github.com/lucascouts/bentoo.git
emerge --sync bentoo
```

**Option 2: Manual configuration**

Create `/etc/portage/repos.conf/bentoo.conf`:
```ini
[bentoo]
location = /var/db/repos/bentoo
sync-type = git
sync-uri = https://github.com/lucascouts/bentoo.git
priority = 99
```

Then sync:
```bash
emerge --sync bentoo
```

### Install bentoolkit

```bash
emerge --ask app-portage/bentoolkit
```

### Manual Build

```bash
git clone https://github.com/obentoo/bentoolkit.git
cd bentoolkit
make build
sudo make install
```

### Build Targets

```bash
make build           # Build bentoo and bentoo-tray
make install         # Install both to /usr/local/bin, plus bentoo-tray's desktop entry, user unit and icons
make install-config  # Copy config.example.yaml to ~/.config/bentoo/ (no overwrite)
make test            # Run tests
make coverage        # Run tests with coverage report
make audit           # Run security audit (go mod verify + govulncheck)
make clean           # Remove build artifacts
make build-all       # Cross-compile for linux amd64 and arm64
make checksums       # Write build/SHA256SUMS over the binaries in build/
make check           # Run lint, test, and audit
make help            # Show all available targets
```

Builds are reproducible: binaries are built with `-trimpath`, and the build
date they report is the time in `SOURCE_DATE_EPOCH`, else the last commit's, so
two builds of one commit from clean checkouts are byte-identical wherever the
tree was cloned.

## Usage

`bentoo --help` lists every command, and `bentoo <command> --help` describes
one. Each command family is documented in its own page under `docs/`; see
[Documentation](#documentation).

## Development

### Running Tests

```bash
# Run all tests, with the race detector, in random order
make test

# Replay the order of a failing run (the seed is printed as -test.shuffle <seed>)
make test SHUFFLE=1790618260127275631

# Run tests with coverage (also -race, random order)
make coverage

# Run every fuzz target for FUZZTIME each (default 30s)
make fuzz
make fuzz FUZZTIME=5m

# Run specific package tests
go test -v ./internal/overlay/...
go test -v ./internal/autoupdate/...
```

The security audit and the project structure are in
[Development](docs/development.md).

## Documentation

- [Configuration](docs/configuration.md): the config file, its options, the secrets file and logging
- [Overlay commands](docs/overlay.md): `bentoo overlay`, from status to prune, and a typical workflow
- [Distfiles](docs/distfiles.md): fetching gated distfiles, and where updates download distfiles
- [Notices](docs/notices.md): `bentoo notice`, one notice written as a news item and a site page
- [Autoupdate](docs/autoupdate.md): `packages.toml`, the record model and the LLM providers
- [Runtime behaviour](docs/behaviour.md): exit codes, live output, concurrency, timeouts, headers, HTTP/2 and filesystem assumptions
- [Snapshots](docs/snapshot.md): `bentoo snapshot`, btrbk, snapper, cloud backup and rollback
- [Desktop notifications](docs/tray.md): installing and configuring `bentoo-tray`
- [Development](docs/development.md): security audit and project structure

## License

MIT. See [LICENSE](LICENSE).
