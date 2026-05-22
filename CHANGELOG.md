# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.1] - 2026-05-22

### Changed
- Bumped indirect dependencies to their latest patch/minor releases:
  `golang.org/x/net` v0.54.0 → v0.55.0,
  `golang.org/x/sys` v0.44.0 → v0.45.0, and
  `golang.org/x/crypto` v0.51.0 → v0.52.0. No API changes; routine
  upstream fixes. `govulncheck` reports zero known vulnerabilities
  against the resulting module graph.
- `.gitignore` now ignores the entire `.epic/` directory; previously only
  `.epic/**/.draft/` and `.epic/archive/` were excluded. Epic plugin state
  is no longer versioned.
- **`llm_prompt` is documented as `analyze`-only; `--check` emits a Warn when
  the field is set.** The README previously implied `llm_prompt` worked under
  `--check`, but the live LLM branch in `Checker.fetchUpstreamVersion` is gated
  on a non-nil `llmClient` that the CLI has never wired. `NewChecker` now logs
  one Warn per package whose `llm_prompt` is set, identifying the package and
  pointing the user at `bentoo overlay analyze`. The struct field is retained
  so existing `packages.toml` files load unchanged.

### Fixed
- **`overlay autoupdate --apply` now honours SIGINT/SIGTERM.** The signal-derived
  context built by `runAutoupdate` is now threaded into `NewApplier` via
  `WithApplierContext`, so a Ctrl-C during `ebuild manifest` or the elevated
  compile step terminates the spawned child within ~2 s and triggers the
  existing orphan-rollback path. This closes the gap left by 0.2.0, whose
  CHANGELOG claimed SIGINT/SIGTERM cancelled in-flight HTTP requests and child
  processes — the claim now holds for both `--check` and `--apply`.
- **`autoupdate.cache_ttl` from `~/.config/bentoo/config.yaml` is now applied.**
  A new `WithCacheTTL` checker option carries the user-configured TTL through
  to `Cache.TTL`; previously the value was loaded into config but ignored, so
  cache entries always expired at the hardcoded 1-hour default.
- **`pending.json` clears after a successful `--apply`.** A package that
  completes the full apply path (copy + manifest, plus compile when
  `--compile`) is removed from `pending.json`, so `bentoo overlay autoupdate
  --list` no longer surfaces already-applied entries. Failures keep the entry
  for retry. A delete-after-success bookkeeping failure emits a Warn but does
  not flip `result.Success`.
- CI: silenced a `contextcheck` false positive on the `applier.Apply` call in
  `runApply`. The signal-derived context is propagated into the applier's
  spawned processes via `WithApplierContext` (`a.ctx`), not a `ctx` parameter,
  so the lint warning is annotated with an inline `//nolint:contextcheck`
  justification rather than altering `Apply`'s signature.

Validated with `go build`, `go vet`, `go test ./...`, and `govulncheck`
(0 vulnerabilities).

## [0.2.0] - 2026-05-17

### Added
- `--concurrency=N` flag on `overlay autoupdate` and `overlay compare` bounds
  the number of packages processed in parallel. Default `10`, valid range
  `[1, 100]`; a value outside the range fails fast with a clear error before
  any package work begins.
- Shared, tuned HTTP transport (`httputil.BuildTransport`) with connection
  pooling, replacing per-request ad-hoc transports across the autoupdate and
  provider HTTP paths.
- `BENTOO_DISABLE_HTTP2=1` environment variable opts the shared transport out
  of HTTP/2 (HTTP/1.1 only) for environments where an HTTP/2 proxy misbehaves.
- Git clone URL and branch validators, and LLM regex/XPath validation, run
  before the corresponding external invocation.
- Documented process exit codes for `overlay autoupdate`: `0` success, `1`
  partial failure, `2` total failure / invalid configuration.
- `goleak`-based goroutine-leak detection in the test suite.

### Changed
- **BREAKING:** `${VAR}` expansion in `packages.toml` header values is now
  allow-listed. It applies only when the header name (case-insensitive) is one
  of `Authorization`, `X-Api-Key`, `X-Auth-Token`, `Private-Token` **and** the
  variable is prefixed `BENTOO_` or is one of `GITHUB_TOKEN`, `GITLAB_TOKEN`,
  `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`. A non-allow-listed `${VAR}` is now
  passed through literally with a `Warn` instead of being expanded — rename
  such variables to add the `BENTOO_` prefix.
- **BREAKING:** `overlay autoupdate` now exits `1` on partial failure (at least
  one package failed and at least one succeeded); previously it exited `0`.
- **BREAKING:** the `ProgressCallback` signature is now
  `func(done, total uint64)`.
- **BREAKING:** `CheckAll` / `AnalyzeAll` now return a `BatchResult`, separating
  successful items from per-package failures.
- Cache files and the apply-log are now written with mode `0600` (was `0644`).
- HTTP/2 is now enabled by default on the shared transport.

### Security
- Env-var header-expansion allow-list prevents a malicious or mistaken
  `packages.toml` from exfiltrating arbitrary process secrets through a
  non-auth header or an arbitrary variable name.
- Git clone URL and branch validation rejects unsafe inputs such as `file://`
  URLs and argument/flag injection.
- HTTP response bodies are capped at 10 MiB; an oversized body now fails with
  `ErrResponseTooLarge` instead of being read unbounded.

### Fixed
- An orphan `.ebuild` left behind when `ebuild manifest` fails is now rolled
  back.
- Per-package errors in batch operations are no longer silently swallowed; the
  `//nolint:errcheck` directive that hid them was removed.
- The rate limiter is now actually invoked on the HTTP hot path.
- `git clone` and `ebuild manifest` invocations now run under a timeout.
- `SIGINT`/`SIGTERM` now cancels in-flight HTTP requests and child processes.

Validated with `go test -race ./...`, `golangci-lint run`,
`govulncheck ./...`, and `make audit-ctx`.

## [0.1.11] - 2026-05-15

### Changed
- CI: bumped `actions/checkout` v4 → v6.0.2 and `actions/setup-go` v5 →
  v6.4.0 to run on Node 24 ahead of GitHub's Node 20 removal
  (scheduled 2026-09-16); both actions are now pinned to their commit
  SHAs (with the version tag in a trailing comment) for supply-chain
  hardening. `google/osv-scanner-action` was likewise bumped v2.0.2 →
  v2.3.8 and SHA-pinned, and the removed `--skip-git` flag was dropped
  (not scanning the git root is the v2.x default).
- CI: Go toolchain version is now sourced from `go.mod`
  (`go-version-file: go.mod`) instead of the fuzzy `'1.25'` input, so
  the runner always matches the module's stated `go` directive.

### Fixed
- CI: green again after the `actions/setup-go@v6` upgrade flipped
  `GOTOOLCHAIN`'s default from `auto` to `local`, which made the fuzzy
  `'1.25'` input resolve to 1.25.9 on the runner while `go.mod`
  requires `>= 1.25.10`. Sourcing from `go.mod` keeps the two in
  lockstep and removes the manual bump treadmill.

## [0.1.10] - 2026-05-10

### Changed
- Bumped indirect dependencies to their latest patch/minor releases:
  `golang.org/x/net` v0.53.0 → v0.54.0,
  `golang.org/x/sys` v0.43.0 → v0.44.0, and
  `golang.org/x/text` v0.36.0 → v0.37.0. The `go` directive in
  `go.mod` was also bumped from `1.25.9` to `1.25.10` to track the
  latest 1.25.x toolchain. No API changes; routine upstream fixes.
  Validated with `go build`, `go vet`, `gofmt`, `go mod verify`,
  `go test -race`, `govulncheck` (0 vulnerabilities) and
  `golangci-lint` (0 issues) against the project's 10-linter config.

## [0.1.9] - 2026-05-03

### Added
- `overlay manifest` now reuses distfiles already present in the system
  Portage cache instead of re-downloading them. Before each `pkgdev`
  invocation, every `DIST` entry listed in the package's existing
  `Manifest` is looked up in `--distfiles-cache` (default
  `/var/cache/distfiles`) and, when found, symlinked into the working
  distdir. The cache is opened read-only — nothing is ever written
  back. Pass `--distfiles-cache ""` to disable, or point the flag to
  a custom directory. Cache misses fall through to pkgdev's normal
  download path, so the optimization is transparent.
- `LogReporter` now appends `[reused N]` to the per-package OK line
  when at least one distfile was satisfied from the cache, and
  `ManifestUpdate` exposes a new `Reused` field for downstream callers.

## [0.1.8] - 2026-04-29

### Fixed
- CI lint pipeline (`golangci-lint`) is green again. Two `fmt.Fprintln`
  calls in the manifest reporters were swapped for `fmt.Fprintf` with
  an explicit `\n`, since the project's `errcheck` exclusion list
  covers `Fprint`/`Fprintf` but not `Fprintln`. The pkgdev distfiles
  cache directory is now created with mode `0o750` instead of `0o755`
  to satisfy `gosec` G301 (per-user cache; group-only access is
  sufficient). No behaviour change.

## [0.1.7] - 2026-04-29

### Added
- `overlay manifest` now regenerates packages in parallel with a worker
  pool (default 10 simultaneous `pkgdev` invocations, configurable via
  `--jobs`/`-j`). Dramatically faster on overlays with many packages.
  Per-target ordering of the returned results is preserved regardless
  of completion order, and `pkgdev` sub-processes are wired through
  `exec.CommandContext` so SIGINT/SIGTERM cancels an in-flight run
  cleanly.
- Live terminal UI for `overlay manifest`: when stdout is a TTY, a
  fixed block at the bottom shows one slot per active worker plus a
  `[done/total] ████░░░░ NN%` global progress bar; finished packages
  scroll above the block as `✓` / `✗` history lines. Outside a TTY
  (CI logs, pipes), output falls back to plain `START / OK / FAIL`
  log lines — concurrent-safe via an internal mutex. No new
  dependencies; the TUI is built on raw ANSI escapes.

### Changed
- `RegenerateManifests` (internal API) gained `Jobs`, `Reporter` and
  `Ctx` fields on `ManifestOptions`, plus a new `ProgressReporter`
  interface (`Total`/`Start`/`Done`/`Finish`) for lifecycle events.
  `pkgdev` output is now captured per-job into a buffer and surfaced
  to the reporter on failure rather than streamed straight to the
  shared stdout, so parallel runs no longer interleave their logs.

## [0.1.6] - 2026-04-28

### Added
- `overlay manifest` now accepts `--distdir <path>` to choose where
  `pkgdev` writes the distfiles it fetches. When set, the directory is
  expanded (`~` and relative paths), created if missing, and preserved
  between runs as a persistent download cache. Default behaviour is
  unchanged: a temporary directory under `os.TempDir()` is created and
  removed at the end of the run. The pkgdev progress line now logs the
  resolved `distdir` so it is visible at a glance.

## [0.1.5] - 2026-04-28

### Changed
- Bumped indirect dependencies to their latest patch/minor releases:
  `golang.org/x/net` v0.52.0 → v0.53.0,
  `golang.org/x/sys` v0.42.0 → v0.43.0,
  `golang.org/x/text` v0.35.0 → v0.36.0,
  `github.com/mattn/go-isatty` v0.0.20 → v0.0.22, and
  `github.com/golang/groupcache` to the 2024-11-29 snapshot. Pulls in
  routine upstream fixes (no API changes); `govulncheck` reports zero
  known vulnerabilities against the resulting module graph.

## [0.1.4] - 2026-04-28

### Changed
- `overlay commit` no longer renders package-internal support files
  (`Manifest`, `metadata.xml`, `files/*`, patches) in the generated
  commit message. They are implementation details of the surrounding
  ebuild changes and were producing noisy lines such as
  `del({dev-util/claude-code/Manifest, .../metadata.xml, .../files/*})`
  on every commit. Eclasses, profiles, licenses, top-level metadata and
  files at the overlay root continue to be listed. When a commit only
  touches package-internal files, the message falls back to the generic
  `update: package files`.

## [0.1.3] - 2026-04-28

### Added
- `overlay manifest` subcommand: regenerate `Manifest` files for the
  whole overlay, a single category, or a single package
  (`bentoo overlay manifest [<category> | <category>/<package>]`).
  Default behaviour does a clean regeneration — the existing `Manifest`
  is moved aside, `pkgdev manifest` runs against a per-invocation
  `--distdir` under `os.TempDir()`, and the backup is restored on
  failure. Use `--keep` to preserve the existing `Manifest` (soft
  reconcile) or `--dry-run` (`-n`) to list targets without invoking
  pkgdev. Runs unprivileged; no sudo required.

### Changed
- Rename flow (`overlay rename`) now reuses the shared
  `RegenerateManifests` helper in `internal/overlay/manifest.go`
  instead of carrying its own pkgdev wrapper. Behaviour is preserved
  (`Keep: true` mode), eliminating duplicated logic.

## [0.1.2] - 2026-04-24

### Fixed
- `autoupdate` applier now rejects same-version ebuild copies instead of
  silently truncating the source file. `os.Create` truncates before
  `io.Copy` reads, so a self-copy produced a zero-byte ebuild. Adds a
  guard in `copyEbuild` and a degenerate-case skip in the
  `TestEbuildCopyVersioning` property tests that intermittently broke CI.

### Changed
- `.gitignore` now excludes `.tab/` and `.epic/` local plugin state so
  TAB (tech-advisory-board) and Epic plugin data never gets committed.

## [0.1.1] - 2026-04-24

### Fixed
- `overlay commit` now renders non-ebuild files (eclasses, profiles,
  licenses, metadata and arbitrary repo files) in the generated commit
  message instead of falling back to the generic `update: package files`.
  Examples: `add(eclass/rpm.eclass)`, `mod(profiles/package.mask)`,
  `add(app-misc/hello-1.0), add(eclass/rpm.eclass)`.

## [0.1.0] - 2026-04-20

### Added
- Initial release after versioning restructure. Prior history archived;
  project restarts at 0.1.0 following SemVer from this milestone forward.

[Unreleased]: https://github.com/obentoo/bentoolkit/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/obentoo/bentoolkit/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/obentoo/bentoolkit/compare/v0.1.11...v0.2.0
[0.1.11]: https://github.com/obentoo/bentoolkit/compare/v0.1.10...v0.1.11
[0.1.10]: https://github.com/obentoo/bentoolkit/compare/v0.1.9...v0.1.10
[0.1.9]: https://github.com/obentoo/bentoolkit/compare/v0.1.8...v0.1.9
[0.1.8]: https://github.com/obentoo/bentoolkit/compare/v0.1.7...v0.1.8
[0.1.7]: https://github.com/obentoo/bentoolkit/compare/v0.1.6...v0.1.7
[0.1.6]: https://github.com/obentoo/bentoolkit/compare/v0.1.5...v0.1.6
[0.1.5]: https://github.com/obentoo/bentoolkit/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/obentoo/bentoolkit/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/obentoo/bentoolkit/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/obentoo/bentoolkit/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/obentoo/bentoolkit/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/obentoo/bentoolkit/releases/tag/v0.1.0
