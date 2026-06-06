# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.21] - 2026-06-05

### Fixed
- **`BUILD_ID` substitution for version-tracked packages (cursor 403).** Cursor
  embeds a per-release `commitSha` in its `SRC_URI` via `BUILD_ID`. The
  autoupdate bumped only `PV`, leaving `BUILD_ID` stale, so the `.deb` URL mixed
  the old build id with the new version and returned **403 Forbidden**. A
  version-tracked package may now set `commit_sha_path` (requires `parser="json"`);
  the checker resolves the SHA from the same JSON response into the pending
  update, and `substituteCommitHash` rewrites `BUILD_ID="<40hex>"` at apply time.
  Verified end-to-end against the live cursor API (`3.6.31 → 3.7.12`): the
  manifest fetch now succeeds where it previously 403'd.

## [0.3.20] - 2026-06-05

### Added
- **Preserve the `_pre` suffix for commit-tracked snapshot packages.** A new
  `extractSnapshotSuffix` helper detects whether the current ebuild uses `_pre`
  (pre-release) or `_p` (post-release) and reuses it when building the new
  version, so commit-tracked `_pre` packages keep the correct Gentoo ordering
  (`X.Y_pre<date>` < `X.Y` < `X.Y_p<date>`). The `AllSnapshotPackages` table is
  extended with `zed`, `mesa`, `mesa_clc` and `libqmi`.

### Changed
- **Bumped `github.com/chromedp/cdproto`** to `20260427013145`; `go mod tidy`
  promotes it to a direct dependency. Build, vet, tests and `govulncheck` pass.
- Minor lint cleanups in `internal/autoupdate`: write ebuilds with `0o600`
  permissions in `substituteCommitHash` (gosec), and drop an unused `fmt`
  import / needless `Sprintf` in the commit-track tests.

## [0.3.19] - 2026-06-05

### Added
- **`track = "commit"` mode for `_p` snapshot packages in `packages.toml`.**
  Packages versioned as `X.Y.Z_p<date>` (post-release snapshots) can now be
  tracked by commit instead of by tag. Setting `track = "commit"` on a package
  entry makes the checker fetch the latest commit on a branch (GitHub or GitLab
  commits list API), extract the commit date as the new `_p<YYYYMMDD>` suffix,
  and store the commit SHA for substitution at apply time. Cache reads are
  bypassed for commit-tracked packages so the SHA is always current.

- **Automatic base-version detection from commit titles
  (`commit_version_pattern` + `commit_message_path`).** When a snapshot
  package declares these two fields, the checker scans all returned commit
  titles and extracts a version from the first match. If the detected version
  is newer than the current base (e.g. a commit titled *"Update for
  Vulkan-Docs 1.4.353"* while the ebuild is at `1.4.352_p…`), the new version
  is built from the detected base (`1.4.353_p<today>`) rather than the stale
  one. The base is never downgraded: a commit mentioning an older version is
  ignored. This covers the common Khronos pattern where a Vulkan SDK or
  Vulkan-Docs version bump appears in the commit stream days or weeks before
  the upstream tag is cut.

- **Commit-hash substitution at apply time.** `PendingUpdate` now carries a
  `CommitHash` field. When `--apply` runs on a commit-tracked package, a new
  `substituteCommitHash` step rewrites the commit variable in the copied ebuild
  before the manifest step. Handles the three variable forms used in the
  overlay: `EGIT_COMMIT="<sha>"`, `GIT_COMMIT="<sha>"`, and `COMMIT=<sha>`
  (bare, no quotes — used by `dev-db/sqlitebrowser`).

- **22 new tests** in `checker_commit_track_test.go` covering:
  `extractSnapshotBase` (8 cases), `scanCommitsForVersion` (6 cases including
  GitHub and GitLab formats, invalid JSON and bad regex), `CheckPackage` commit
  tracking (date-only bump, base-version bump via commit title, no-update,
  SHA persistence in pending, cache-bypass guarantee), and a table-driven suite
  with one sub-test per `_p` package (glslang, spirv-headers, spirv-tools,
  vulkan-headers, vulkan-tools, vulkan-layers, vulkan-loader, sqlitebrowser,
  modemmanager), plus edge-case tests for downgrade protection and the GitLab
  `+00:00` timezone format.

## [0.3.18] - 2026-06-04

### Added
- **`autoupdate` auto-disables orphaned packages instead of erroring forever.**
  When a package's ebuild is removed from the overlay, the checker used to fail
  every run with `failed to get current version: no ebuild file found`. It now
  detects the orphan, sets `enabled = false` for that entry in `packages.toml`,
  and surfaces it as an informational `no ebuild in overlay — disabled` line
  rather than a recurring failure (so it no longer pollutes the exit code).
  Subsequent runs skip the entry silently. The edit is surgical — it inserts a
  single `enabled = false` line into the affected section, preserving every
  comment, ordering, and formatting in the hand-maintained file (unlike a full
  TOML re-encode). Applies to both `--check` (batched, one atomic write) and the
  single-package path.

## [0.3.17] - 2026-06-03

### Fixed
- **`autoupdate --apply` no longer fails on stale pending entries.** The applier
  trusted the `current_version` recorded at check-time to locate the source
  ebuild; when the overlay had since drifted — the package bumped further by
  hand, or removed entirely — that file was gone and Apply died with a cryptic
  `source ebuild file not found`. Apply now re-resolves the current version
  against the live overlay (mirroring the checker's selection): a stale-but-
  present `current_version` self-heals to the real source, and a genuinely
  obsolete entry (package removed, or overlay already at/beyond the target) is
  **pruned from `pending.json`** and reported as `Obsolete (pruned)` rather than
  counted as a failure. `--apply all` gains an `Obsolete: N` summary line and no
  longer exits non-zero solely because of superseded entries.

## [0.3.16] - 2026-06-03

### Added
- **Live test covering the `~/.config/bentoo/secrets` serial path for
  authenticated distfile fetch.** `TestFetchDistfileLiveFileZillaProSecretsFile`
  blanks `FILEZILLA_PRO_KEY` so `resolveSecret` must fall back to the secrets
  file, then runs the real FileZilla Pro POST and asserts a non-trivial binary
  comes back — proving a user-configured serial is accepted end-to-end. Gated
  on `FILEZILLA_SECRETS_E2E=1`, so it never runs in CI. Test-only — no runtime
  or binary behavior changes.

## [0.3.15] - 2026-06-03

### Fixed
- **CI: `TestApply_CancelsOnContextCancellation_Compile` no longer flakes on
  slow runners.** The test slept a fixed 200ms before cancelling, assuming the
  `Apply` goroutine had cleared the instant manifest step and was blocked in
  compile; on a loaded CI runner it had not, so the cancel aborted the manifest
  instead and the assertion failed. It now waits on a deterministic signal (the
  exec factory closes a channel the first time it builds a compile command,
  which only happens after the manifest step returns) before cancelling, so the
  cancellation can only ever hit the compile step. Test-only change — no runtime
  or binary behavior is affected.

## [0.3.14] - 2026-06-03

### Added
- **Per-package `enabled` toggle in `packages.toml`.** Each entry may now set
  `enabled = false` to be skipped silently by `overlay autoupdate --check` — no
  network fetch, and absent from progress output and the run totals — without
  deleting its configuration. An absent/`true` value means enabled (the
  default), so existing configs need no migration. This is the clean way to
  park an orphaned entry (e.g. a package whose ebuild was removed from the
  overlay but whose check config is worth keeping). `CheckAll` filters disabled
  packages up front alongside the `--only` type filter; `CheckPackage` (an
  explicitly named package) is intentionally unfiltered, treating an explicit
  name as a conscious override.
- **Authenticated distfile fetch for serial-gated packages.** Some commercial
  packages (e.g. `net-ftp/filezilla-pro`) gate their distfile behind a
  serial/registration key, so `pkgdev manifest` cannot fetch it from `SRC_URI`.
  A package's free-form `[meta]` block can now drive an authenticated download:
  before the manifest step `--apply` submits the vendor's download form (POST or
  GET) with the serial injected, and drops the file into pkgdev's private
  `--distdir` so it digests the local copy. The serial is **never** stored in
  the overlay — it is resolved at runtime from an env var or
  `~/.config/bentoo/secrets` and scrubbed from every log line and error message.
  HTML/zero-byte responses and unsafe filenames are rejected; the download is
  context-bounded so SIGINT cancels it. Packages without a `[meta]` fetch spec
  (the overwhelming majority) follow the normal pkgdev-from-`SRC_URI` path
  unchanged.

## [0.3.13] - 2026-06-03

### Changed
- **`overlay autoupdate --check` is now dramatically faster on GitHub/GitLab-heavy
  overlays.** The HTTP rate limiter gained per-host policies: GitHub hosts run at
  ~10 req/s (100ms) and GitLab at ~3.3 req/s (300ms) — the two providers that
  dominate `packages.toml` — while every other host keeps the conservative
  1-req/6s default. Previously a single uniform 1-req/6s-per-host limit serialised
  the ~220 GitHub/GitLab packages regardless of `--concurrency`, capping a full
  check at ~13 min; with per-host tuning it completes in well under a minute.
  The default `--concurrency` was raised from 10 to 20 to keep the tuned limiters
  saturated (max remains 100). New `RateLimiter` options `WithHTTPInterval`,
  `WithHostPolicy` and `WithTunedHostPolicies` expose the tuning; the zero-config
  limiter keeps its uniform 6s-per-host behavior.

## [0.3.12] - 2026-06-03

### Added
- **`overlay autoupdate` now classifies each package as binary or source and can
  filter on it.** A new optional `type = "bin" | "source"` field in
  `packages.toml` records a package's kind; when omitted it is auto-detected from
  the current ebuild (`RESTRICT="bindist"`, a `-bin` suffix, or a binary
  `SRC_URI`) via the existing `detectBinaryPackage` heuristic, so existing
  configs need no change and only override/correction cases set it explicitly.
  `--check` now tags every result line (`[bin]`/`[src]`) and prints a
  `Checked N source, M bin` summary, and a new `--only=bin|source` flag restricts
  the batch to one kind — filtered packages are skipped *before* any network
  fetch. An unrecognized `type` value (in `packages.toml`) or `--only` value
  fails fast rather than silently checking everything. `CheckResult` gains a
  `Type` field; classification is metadata only and does **not** change
  apply/compile behavior.

## [0.3.11] - 2026-06-03

### Fixed
- **`overlay autoupdate --apply` now regenerates the Manifest without root or
  Portage write access.** The apply step ran `ebuild <path> manifest`, which
  inherits the system `DISTDIR` (`/var/cache/distfiles`) and tries to *write*
  the fetched `SRC_URI` distfiles there. As an unprivileged user this failed
  with `No write access to '/var/cache/distfiles'`, so every apply aborted
  before updating the Manifest (`ebuild manifest command failed`). `runManifest`
  now mirrors the `overlay manifest` subcommand: it creates a private writable
  distdir (`os.MkdirTemp`, removed when the step returns) and runs
  `pkgdev manifest --distdir <tmpdir>` from the package directory. `pkgdev`
  neither requires root nor touches the system `DISTDIR`, so the manifest step
  works as a regular user. Timeout, context cancellation and orphan-ebuild
  rollback are unchanged.

## [0.3.10] - 2026-06-03

### Fixed
- **`overlay add <pkg>` now reports only what it staged, not the whole working
  tree.** After staging, the command printed `overlay.Status()` — a full
  `git status --porcelain` of the working tree — so every modified package in the
  overlay appeared in the output, making it look like `add <pkg>` had staged
  everything. The index was actually correct (only the chosen paths were staged,
  and `overlay commit` uses `git commit` with no `-a`, so only staged changes are
  committed); the feedback was the only thing misleading. Root cause: the status
  parser collapsed the porcelain `XY` columns with `TrimSpace`, discarding the
  staged-vs-unstaged distinction. Added `GitRunner.StagedStatus()` /
  `ParseStagedStatusOutput` (keyed on the index column `X`; untracked and
  worktree-only entries are dropped and staged renames are split into delete+add,
  matching the existing parser) and a matching `overlay.StagedStatus`; `runAdd`
  now displays that. The no-argument `overlay add` (equivalent to `git add .`)
  still lists everything, since there everything truly is staged.

## [0.3.9] - 2026-06-03

### Added
- **`overlay autoupdate --check` now shows live progress.** The check fans out
  concurrently and previously printed nothing until the final results table,
  leaving the terminal silent through the whole network/LLM phase. `runCheck`
  now wires the Checker's existing `WithProgressCallback` (until now never
  connected) to a self-rewriting `Checking: [pct%] done/total` line, mirroring
  `overlay compare`. The counter is driven by `CheckAll`'s atomic counter, so it
  stays monotonic despite the concurrent workers; the line is cleared before the
  results table and suppressed under `--quiet`.

## [0.3.8] - 2026-06-03

### Fixed
- **`make audit-ctx` (CI) no longer fails on the chromedp backend.** The
  `context.Background()` that roots the chromedp browser allocator in
  `script_evaluator_chromedp.go` (added in 0.3.6) lacked the `// SAFE:`
  justification the context-spine audit requires, so the `audit-ctx` job failed
  with *"naked context.Background() found"*. Annotated it like the other
  intentional root contexts (`applier.go`, `analyzer.go`, …); no behavior change.

## [0.3.7] - 2026-06-03

### Added
- **`overlay autoupdate --apply --clean` (`-c`) removes the old ebuild after a
  successful apply.** With the flag, once the new version is created, manifested
  and the pending entry cleared, `Apply` deletes the previous version's ebuild
  (the one it bumped from) and regenerates the Manifest so the now-orphaned
  distfile entries are pruned — leaving only the freshly created version, the way
  a manual version bump ends. It is best-effort: a removal or manifest-prune
  failure is surfaced as a `Clean:` warning on the result but never flips the
  apply to failed, since the update itself is already done. The removed version
  is reported as `Removed: <pkg>-<old>.ebuild`. Works with both
  `--apply <pkg> --clean` and `--apply all --clean`.

## [0.3.6] - 2026-06-03

### Added
- **`chromedp` backend for the `parser="script"` headless-browser path.** A new
  `liveEvaluator` implementation in `script_evaluator_chromedp.go` (built with
  `-tags chromedp`, mutually exclusive with `playwright` via
  `//go:build chromedp && !playwright`) drives the system Chrome/Chromium
  directly over the DevTools Protocol — no Node.js driver and no
  `playwright install` step. `chromedp.Evaluate(..., WithAwaitPromise(true))`
  reaches parity with Playwright's `page.Evaluate`, including resolving an
  `(async () => {...})()` IIFE to its string result. The integration test
  (`-tags chromedp -run Integration`) mirrors the Playwright one. `go mod tidy`
  without a tag still prunes both browser deps, so `chromedp` and `playwright-go`
  are pinned as direct requires.

### Fixed
- **`overlay autoupdate --apply` no longer produces invalid ebuild filenames for
  upstreams whose version carries a tag prefix.** When the detected `NewVersion`
  came from a git tag like `v9.2.0588`, `Apply` used it verbatim to build the
  destination ebuild name (`vim-v9.2.0588.ebuild`), which Portage rejects with
  *"does not follow correct package syntax"* — failing the manifest step for vim,
  vim-core, bind-tools, nodejs, ollama, ollama-bin, bisq-bin, etc. `Apply` now
  strips the prefix (`stripVersionPrefix` + trim) and validates the result with
  `ebuild.IsValidVersion` before touching the filename, surfacing a clear
  `ErrInvalidNewVersion` for non-versions (e.g. `latest`) instead of a cryptic
  Portage error.

## [0.3.5] - 2026-06-02

### Added
- **`overlay autoupdate --apply all` applies every pending update in one run.**
  Previously `--apply` accepted only a single exact `category/package`; any other
  value failed with `ErrPackageNotInPending`. The new `all` sentinel (safe because
  real package names always contain a `/`) reuses a single `Applier` and iterates
  over a snapshot of the pending list, applying each package independently. Each
  outcome is printed, followed by an `Apply All Summary` (`Applied: N` / `Failed:
  M`). Successfully applied packages leave `pending.json`; failures remain marked
  `failed`. The process exits non-zero when any package fails, matching the
  single-package contract. `--apply all --compile` still prompts per package.

## [0.3.4] - 2026-06-02

### Security
- **Bump the `go` directive to 1.25.11.** The Go 1.25.10 standard library is
  affected by [GO-2026-5037](https://osv.dev/GO-2026-5037) and
  [GO-2026-5039](https://osv.dev/GO-2026-5039), both fixed in 1.25.11. The
  `go.mod` directive drives the toolchain CI installs (via setup-go's
  `go-version-file`), so the bump clears the osv-scanner findings. No source
  changes; build and vet stay green.

## [0.3.3] - 2026-06-02

### Fixed
- **`overlay autoupdate --check` no longer fails en masse with HTTP 403.** The
  version-check hot path (`Checker.fetchContent`) issued requests via
  `GetWithContext`, which bypasses `applyHeaders` — so checks never sent a
  `User-Agent`, the configured GitHub token, or the per-package `headers` from
  `packages.toml`. Every request went out as an anonymous `Go-http-client/1.1`,
  and `api.github.com` (60 req/h per IP) answered `403` for the bulk of
  GitHub-backed packages. `fetchContent` now routes through
  `GetWithHeadersContext`, putting the User-Agent, `Authorization` token, and
  TOML-declared headers on the wire. The full batch check dropped from 68
  spurious 403s to 0.

### Added
- **GitHub API authentication for autoupdate.** New `WithGitHubToken` checker
  option, wired by `overlay autoupdate` from `~/.config/bentoo/config.yaml`'s
  `github.token`, with `GITHUB_TOKEN`/`GH_TOKEN` env taking precedence — the
  same resolution order `overlay compare` uses. Raises the GitHub API limit from
  60 to 5000 req/h, eliminating rate-limit 403s in the full batch check.
- **Default `User-Agent` (`bentoolkit/<version>`)** on the autoupdate HTTP
  client, avoiding the `Go-http-client/1.1` string that WAF-fronted upstreams
  reject outright.

## [0.3.2] - 2026-06-02

### Changed
- **Bump `github.com/mattn/go-colorable` from 0.1.14 to 0.1.15.** Routine
  maintenance update of an indirect dependency; no functional or security
  impact. A dependency audit (`govulncheck ./...`) reported no vulnerabilities
  in the reachable code, and all direct dependencies are already current.

### Notes
- The proposed `github.com/deckarep/golang-set/v2` 2.8.0 → 2.9.0 bump was
  intentionally **not** applied: 2.9.0 pulls in `go.mongodb.org/mongo-driver`
  (unused, not reachable) with no security benefit. The package stays pinned at
  2.8.0 (an indirect dependency of `playwright-go`).

### Internal
- Minor `gofmt` comment normalization in `internal/autoupdate/version_history.go`.

## [0.3.1] - 2026-06-02

### Security
- **Bump `github.com/go-jose/go-jose/v3` from 3.0.4 to 3.0.5**
  ([CVE-2026-34986](https://github.com/go-jose/go-jose/security/advisories/GHSA-78h2-9frx-2jm8),
  High / CVSS 7.5). Decrypting a JWE that uses a key-wrapping algorithm with an
  empty `encrypted_key` could panic, allowing a denial of service. The
  dependency is indirect and the upgrade is a drop-in patch (the module's
  dependency graph is unchanged). (#7)

### Fixed
- **CI `Lint` job restored to green.** Three pre-existing `staticcheck` findings
  were failing `golangci-lint run ./...`; because the `Build` job depends on
  `Lint`, this had been blocking release builds:
  - `newSelectExtractor` carried no-op `switch` cases whose pure
    `strings.HasPrefix` return values were discarded (SA4017). The redundant
    cases were removed; `"[*]"`-prefixed and non-indexed paths still pass
    through unchanged.
  - the non-bare `claude-code` test's empty guard now actually asserts that
    `ANTHROPIC_API_KEY` is not injected into the child process (SA9003): the
    child exits non-zero with a stderr marker so any leak surfaces as an error.
  - the deliberately nil context test case is annotated `//nolint:staticcheck`
    (SA1012).

## [0.3.0] - 2026-06-02

### Added
- **`transform`, `select`, and a `script` parser for `overlay autoupdate`.**
  Three extensions that let packages previously skipped for parsing limitations
  be tracked again:
  - **`transform`** applies ordered regex substitutions to the extracted version
    (e.g. imagemagick `7.1.2-24` → `7.1.2.24`, godot `-beta` → `_beta`),
    per-candidate and before the Gentoo comparison, so a raw upstream string that
    is not yet a valid version can be normalized.
  - **`select = "max" | "last"`** chooses among multiple matches instead of the
    first, reusing the version-history list extractors (JSON `[*]`, CSS, XPath)
    plus a new regex list extractor. The 10-item history cap is now parameterized
    (`-1` = unlimited) so `select="max"` is not defeated by truncation of an
    ascending list (gn).
  - **`parser = "script"`** evaluates JS against a live DOM for multi-step / SPA
    cases (LibreOffice's 3-segment dir → 4-segment tarball). It is backed by
    `playwright-go` behind the `playwright` build tag (`page.Evaluate` auto-awaits
    Promises); the default build returns `ErrScriptSupportNotBuilt`, keeping the
    browser dependency opt-in. `@file.js` scripts load from `.autoupdate/scripts/`
    with path-traversal protection.

  `ValidatePackageConfig` now accepts `parser="script"`, validates `select`, warns
  and ignores malformed `transform` rules, and warns that `transform`/`select` are
  ignored on the script path.
- **`claude-code` LLM provider + LLM wiring for `analyze` and `--check`.** A new
  `llm.provider: claude-code` drives the locally-installed `claude` CLI (Claude
  Code) headlessly (`claude -p … --output-format json`, page content on stdin)
  instead of the HTTP API, reusing your existing Claude Code login or an API key.
  Authentication is hybrid via the new `llm.bare` config (`auto`/`true`/`false`):
  `--bare` + `ANTHROPIC_API_KEY` (the cheap path) or the CLI's login/subscription
  session. The new `llm.max_budget_usd` caps per-call spend (`claude
  --max-budget-usd`). The provider defaults to the `sonnet` model alias and runs
  via `exec.CommandContext`, so SIGINT or the timeout kills the child process;
  page content is passed on stdin (never argv) and the API key never reaches argv,
  logs, or errors. `bentoo overlay analyze` now builds the configured provider for
  schema proposal, and `bentoo overlay autoupdate --check` now uses it to extract a
  version for packages that set `llm_prompt` (tried after the primary/fallback
  parsers). Both commands degrade gracefully: when the `claude` CLI is missing or
  unauthenticated they log a Warn and fall back (heuristic schema / skip
  extraction) rather than failing. Internally, the `Checker`'s LLM hook was
  refactored to accept the `LLMProvider` interface (previously Claude-HTTP-only),
  so any provider can be injected; the existing `claude`/`openai`/`ollama`
  providers are unchanged.
- **Robust LLM schema parsing via `flexString`.** A field the schema types as a
  string but a model emits in another shape no longer fails the whole parse:
  scalars (number/bool) are kept as text, `null` becomes `""`, and an object or
  array (returned by some models for e.g. `fallback_config`) is dropped to `""` —
  so one malformed secondary field can't discard an otherwise-valid schema
  proposal.

### Fixed
- **`overlay autoupdate --check` no longer fails packages that queue behind
  others on the same host.** `fetchContent` derived a single
  `opTimeout`-bounded context and used it for *both* the per-host rate-limiter
  wait *and* the HTTP request. A package queued behind several others on the
  same host could therefore burn the entire per-operation deadline while still
  waiting for a rate-limit token and fail with `context deadline exceeded`
  before any request was issued (observed with 13 packages sharing
  `gitlab.freedesktop.org`). The limiter wait now uses the parent
  (signal-aware) context; the `opTimeout` starts only after a token is acquired
  and bounds just the HTTP round-trip. SIGINT/SIGTERM still cancels the wait.

## [0.2.2] - 2026-06-01

### Fixed
- **`overlay autoupdate --check` now actually supports the `html` parser.**
  `Checker.fetchAndParse` built its parser with `NewParser`, which rejects
  `html` outright (`use NewParserFromConfig for html parser`) and has no way to
  carry the `selector`/`xpath` fields — so every package configured with
  `parser = "html"` failed at fetch time even though the parser, the config
  fields, and the README all advertised it. `fetchAndParse` now builds the
  parser via `NewParserFromConfig` and threads `selector`/`xpath` (plus the
  optional regex post-processing in `pattern`) through for both the primary and
  fallback URLs. This makes HTML scraping work end to end, including extracting
  a version from an element attribute via an XPath such as
  `(//a[contains(@href, '/linux-x64/cursor/')]/@href)[1]` with
  `pattern = "cursor/([0-9.]+)"`.
- **`overlay autoupdate --check` no longer silently reports "up to date" for a
  non-comparable upstream version.** `Checker.compareVersions` previously passed
  the raw upstream value straight to `ebuild.CompareVersions`, whose lenient
  `parseVersion` coerces any unparseable component to `0` — so an upstream tag
  like `INKSCAPE_1_4_4`, or even a `v`-prefixed `v7.0.0`, parsed to a near-zero
  version and compared as *older* than the current ebuild, masking real updates.
  `compareVersions` now normalizes both sides (trims whitespace, strips a
  leading `v`/`version-`/etc. prefix) and validates them with the new
  `ebuild.IsValidVersion`. When either side is not a well-formed Gentoo-style
  version, the result is flagged `CheckResult.NotComparable`: it is surfaced as a
  warning, excluded from the pending list, and never counted as "up to date".

### Added
- **`ebuild.IsValidVersion`** reports whether a string is a well-formed
  Gentoo-style version that `CompareVersions` can order meaningfully, so callers
  can reject junk (`latest`, upstream tag names) instead of comparing against a
  silently-zeroed version.

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

[Unreleased]: https://github.com/obentoo/bentoolkit/compare/v0.3.19...HEAD
[0.3.19]: https://github.com/obentoo/bentoolkit/compare/v0.3.18...v0.3.19
[0.3.18]: https://github.com/obentoo/bentoolkit/compare/v0.3.17...v0.3.18
[0.3.0]: https://github.com/obentoo/bentoolkit/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/obentoo/bentoolkit/compare/v0.2.1...v0.2.2
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
