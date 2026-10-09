# Bentoolkit development

Back to the [README](../README.md).

### Checks

`make check` runs the whole local suite: `lint`, `test` and `audit`. Run it
while you work; before a pull request, run the gate below.

| Target | What it runs |
|---|---|
| `make lint` | `gofmt`, `go vet`, then golangci-lint for both build-tag sets (default and `chromedp`). `lint-pin-check` fails when the golangci-lint version in the Makefile differs from the one CI installs. |
| `make test` | `go test -race -shuffle=on ./...` |
| `make coverage` | `make test` plus `coverage.out` and an HTML report. CI fails below 80%. |
| `make fuzz` | Every `Fuzz*` target for `FUZZTIME` each (default `30s`). |
| `make audit` | `audit-ctx`, `audit-comments`, `audit-pkgdoc`, `go mod verify` and `govulncheck`. |

### Local CI gate

`make check` runs on your machine, with your git, your `/tmp` and your
caches. Before a pull request, run the gate, which reproduces every job of
`.github/workflows/ci.yml` on a clean checkout. The gate is the project's CI:
`ci.yml` no longer runs on push or pull request, only by hand from the Actions
tab. GitHub keeps only what is a service rather than a check: Dependabot,
secret scanning with push protection, and private vulnerability reports. Say
in the pull request which gate run it rests on; a Dependabot pull request is
gated the same way, by its branch.

```bash
./scripts/ci-vm-gate.sh            # gate HEAD (committed work only)
./scripts/ci-vm-gate.sh <ref>      # gate a branch or commit, e.g. a Dependabot PR
```

- **In a KVM guest** (Ubuntu 24.04, as the hosted runner, as a non-root user):
  Test with `-race -shuffle=on` and the 80% coverage floor, Audit, Lint for both
  tag sets at the pinned golangci-lint, Build, and CodeQL for Go and GitHub
  Actions with the suites GitHub's default setup runs.
- **On the host**, in a clean worktree of the same commit: gitleaks, OSV-Scanner,
  zizmor and the changelog rule.

CodeQL fails on any result not listed in `scripts/codeql-dismissed.tsv`
(rule, path, fingerprint and the reason it is not a defect), and on an entry
that no longer matches a result, so the list only shrinks. To dismiss a false
positive, add its line from the log with the reason; the SARIF files are kept
with the logs.

It prints one PASS/FAIL line per job and keeps every log under
`~/.local/share/bentoolkit-ci/logs/`. `--status` also posts the verdict on the
commit as a `local-gate` status.

Create the guest once from an Ubuntu 24.04 cloud image (no root needed, only
the `libvirt` group):

```bash
ssh-keygen -t ed25519 -f ~/.ssh/ci_runner   # if you have no key yet
./scripts/ci-vm-create.sh noble-server-cloudimg-amd64.img
```

The guest shuts down 30 s after each gate; `touch
~/.local/share/bentoolkit-ci/hold` keeps it running. Two deliberate
differences from the hosted runner, both explained in the scripts:

- Tests write their temporary files to tmpfs (`TMPDIR=/dev/shm/...`), because
  `fsync` on the guest's disk image is about 27 ms.
- git is the distribution's 2.43, older than the runner's. That is how a
  `git reset` incompatibility with 2.43 was found.

### Security Audit

```bash
make audit
```

- `go mod verify` checks the module cache against `go.sum`.
- `govulncheck` comes from the `tool` directive in `go.mod`, so it needs no
  separate install: `make audit` runs it as `go tool govulncheck ./...`.
  `GOTOOLCHAIN` is pinned to the `toolchain` line of `go.mod`, so the scan
  covers the standard library CI builds with, not the host's.
- `audit-ctx` fails on a `context.Background()` in `internal/autoupdate` or
  `internal/overlay` that does not carry a `// SAFE:` justification.
- `audit-comments` fails on planning tracker IDs and on comment blocks of 20
  or more lines in production code.

CI adds gitleaks over the full history, OSV-Scanner and zizmor (workflow lint).

### Release assets

Every release carries a vendor tarball, so the overlay ebuilds build without
network access, plus an SPDX SBOM, a SHA256SUMS file and a cosign signature
bundle for the tarball and the SBOM. They are made locally, after the
annotated tag is pushed and before the release notes are written:

```bash
make release-deps VERSION=X.Y.Z          # writes the five files to dist/
make release-deps-verify VERSION=X.Y.Z   # regenerates the tarball and checks everything
gh release upload vX.Y.Z \
  dist/bentoolkit-X.Y.Z-vendor.tar.xz dist/bentoolkit-X.Y.Z-vendor.tar.xz.sigstore.json \
  dist/bentoolkit-X.Y.Z.spdx.json dist/bentoolkit-X.Y.Z.spdx.json.sigstore.json \
  dist/bentoolkit-X.Y.Z-SHA256SUMS
```

Name the five files: a `dist/bentoolkit-X.Y.Z*` glob also matches another
version's files, such as `X.Y.Z0`.

- The tarball holds only `bentoolkit-X.Y.Z/vendor/`, built from
  `git archive vX.Y.Z` with the toolchain the tag's `go.mod` names. It is
  byte-reproducible: anyone with the tag can run `make release-deps-verify`.
- The signing key lives outside the repository, in
  `~/.config/bentoolkit-release/cosign.key` (override with `COSIGN_KEY`);
  `cosign.pub` at the repository root is its public half. Create the pair once
  with `cosign generate-key-pair --output-key-prefix
  ~/.config/bentoolkit-release/cosign`.
- Signatures are not uploaded to the public Rekor transparency log, so no
  identity or key fingerprint is published. The cost is that verification has
  to skip the log check.

To verify a downloaded asset:

```bash
cosign verify-blob --key cosign.pub --insecure-ignore-tlog=true \
  --bundle bentoolkit-X.Y.Z-vendor.tar.xz.sigstore.json bentoolkit-X.Y.Z-vendor.tar.xz
```

### Project Structure

Two binaries, one module. Dependencies point downwards: `cmd` composes the
domain packages, which build on `internal/common`; nothing in `internal/common`
imports a domain package.

```
bentoolkit/
├── cmd/
│   ├── bentoo/              # The CLI (cobra): flag parsing, wiring, rendering
│   └── bentoo-tray/         # The session tray daemon that announces notices
├── internal/
│   ├── autoupdate/          # `overlay autoupdate`: check, apply, sweep, analyze
│   │   ├── ebuilds/         # Locate and read an overlay's ebuilds
│   │   ├── fetch/           # HTTP client, rate limits, caches, authenticated distfile fetch
│   │   ├── fixer/           # LLM agents that repair a failed bump
│   │   ├── llm/             # Claude Code, OpenAI and Ollama providers
│   │   ├── parse/           # Version extraction (JSON, regex, HTML/XPath)
│   │   ├── registry/        # packages.toml: load, validate, lint, repair
│   │   ├── statefile/       # Locked read-merge-save of JSON state files
│   │   └── validate/        # Staged-bump validation ladder and build gates
│   ├── overlay/             # Overlay operations: compare, prune, manifest, review
│   ├── realign/             # Proves a realignment towards the ::gentoo baseline
│   ├── snapshot/            # btrbk/snapper snapshots, cloud ship and rollback
│   ├── notice/              # Authors a notice: GLEP 42 news item + site YAML
│   ├── notices/             # The notices feed contract (types and JSON Feed parser)
│   ├── gentoo/              # Readers for Portage data
│   │   ├── news/            # Portage news as notices (the tray's offline source)
│   │   ├── pkgdb/           # /var/db/pkg
│   │   └── repo/            # category/package/*.ebuild layout of a repository
│   ├── tray/                # bentoo-tray's event loop, feed, state, icons, messages
│   ├── desktop/             # D-Bus integration for the tray
│   │   ├── dbusx/           # Private connections and the well-known name
│   │   ├── netmon/          # NetworkManager online/metered state
│   │   ├── notify/          # org.freedesktop.Notifications
│   │   ├── portal/          # Open a URL via the desktop portal or xdg-open
│   │   ├── sni/             # StatusNotifierItem (system tray icon)
│   │   └── dbustest/        # Private buses for integration tests
│   └── common/              # Infrastructure shared by the domain packages
│       ├── config/          # ~/.config/bentoo/config.yaml
│       ├── distfiles/       # The --distdir given to pkgdev
│       ├── ebuild/          # Ebuild parsing and version comparison
│       ├── filelock/        # Inter-process file lock
│       ├── fileutil/        # File modes and crash-safe writes
│       ├── git/             # Git operations
│       ├── github/          # GitHub API client
│       ├── httpx/           # Outbound HTTP transport tuning
│       ├── logging/         # slog logger with secret redaction
│       ├── output/          # Terminal output helpers
│       ├── procgroup/       # Stop a child and its descendants with its context
│       ├── provider/        # Repository providers (GitHub, GitLab, git, local)
│       ├── report/          # View model of an autoupdate run
│       │   └── render/      # Text rendering of that view model
│       ├── secrets/         # Secret resolution chain
│       ├── tui/             # Bubble Tea foundation
│       ├── version/         # Build version stamped by ldflags
│       └── xdg/             # XDG Base Directory paths
├── misc/
│   ├── design/design-system # Catalogue of what the terminal UI draws
│   └── tray/                # .desktop file, systemd user unit, icons, E2E checklist
├── docs/                    # Topic pages (this directory)
├── config.example.yaml      # Every configuration key, documented
└── Makefile                 # Build, install, test, lint and audit targets
```

`go list ./...` is the authoritative package list, and `go doc <package>`
prints each package's own description.
