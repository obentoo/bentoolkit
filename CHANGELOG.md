# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Releases 0.1.0 to 0.29.1 are in the [changelog archive](docs/changelog/0.1.0-0.29.1.md).

## [Unreleased]

### Added

- **Releases carry a signed, reproducible vendor tarball and an SBOM.**
  `make release-deps VERSION=X.Y.Z` writes `bentoolkit-X.Y.Z-vendor.tar.xz`
  (the tag's `go mod vendor`), an SPDX SBOM, SHA256SUMS and a cosign bundle
  for the tarball and the SBOM; `make release-deps-verify` regenerates the
  tarball byte for byte and checks the rest. The overlay ebuilds fetch the
  tarball, so they no longer need network access to build. See "Release
  assets" in docs/development.md.

### Fixed

- **`overlay autoupdate --check` no longer hangs when a package check
  misbehaves.** A check that returned neither a result nor an error crashed
  the worker while it held the run's lock, and the crash recovery waited on
  that same lock forever. The package is now reported as failed ("check
  returned no result") and the remaining packages are still checked.

- **A malformed JSON path in `packages.toml` is refused when the registry
  loads.** `path`, `commit_sha_path` and `versions_path` used to accept
  `a..b`, `a[0]b`, `a[+1]` or a stray `]` and then read something other than
  what was written (`a..b` read as `a.b`). They now follow one grammar, shared
  by the loader, `overlay autoupdate --lint` and the fetch itself, and the error
  names the package, the field and the quoted path. No record of the bentoo
  overlay is affected.

- **`overlay autoupdate --lint` reads what the registry says.** A
  `track = "commit"` with a trailing comment, or with `base_from = ""`, now gets
  the legacy-base finding; a `type` followed by a comment is read; a
  non-boolean `binary` is named as such instead of "it says nothing"; and a
  mirror that repeats `url` or another mirror up to host case or a trailing `/`
  is refused at load.

## [0.34.0] - 2026-10-09

### Added

- **A `LICENSE` file.** The README has always said MIT, but the repository
  shipped no license text, so GitHub reported the project as unlicensed and
  the code was, strictly, all rights reserved. The MIT text is now at the
  root.

- **A local CI gate, `scripts/ci-vm-gate.sh`.** It reproduces every CI job
  on a clean checkout of a commit: the Go jobs in a KVM guest (Ubuntu 24.04,
  as the hosted runner, non-root) and the scanners on the host. One PASS/FAIL
  line per job, logs kept. `scripts/ci-vm-create.sh` creates the guest from a
  cloud image without root. See docs/development.md.

### Changed

- **Building bentoolkit needs Go 1.27.** `go.mod` declares `go 1.27.0` and
  pins `toolchain go1.27.2`, which carries the standard-library fixes open
  against go1.27.1 (GO-2026-6599 to GO-2026-6617). Go 1.26 is no longer a
  supported build toolchain. The bentoo overlay already builds with
  `>=dev-lang/go-1.27.2`.

- **The lint pin moved from golangci-lint v2.13.2 to v2.14.0.** v2.13.2
  cannot read the export data of the Go 1.27 standard library ("export data
  version 5 is greater than maximum supported version 4"), so the Lint job
  failed on every commit since the move to Go 1.27. v2.14.0 reports no
  issue on the current tree, with and without the `chromedp` tag.

- **CI runs locally; GitHub runs only what cannot.** `.github/workflows/ci.yml`
  no longer starts on push or pull request. Every one of its jobs runs in the
  local gate (`scripts/ci-vm-gate.sh`), whose result is what a pull request
  rests on. The workflow stays as the definition the gate mirrors and as a
  kill switch (manual runs, or restore its triggers). CodeQL runs in the gate
  too, so the repository's CodeQL default setup is switched off.

- **YAML comes from `go.yaml.in/yaml/v3`, not `gopkg.in/yaml.v3`.** The old
  module was archived upstream on 2025-04-01 and will receive no further
  fixes; `go.yaml.in/yaml/v3` is its maintained continuation, with the same
  API. Output is unchanged: the notices feed's golden files pass untouched. A
  `depguard` rule keeps the old import from coming back.

### Fixed

- **The `claude` LLM provider works again with no `model` set.** Its default
  was `claude-3-haiku-20240307`, which Anthropic retired on 2026-04-19, so
  every request that relied on the default has failed since. The default is
  now `claude-haiku-4-5`. A config that names the retired model must change
  it the same way.

- **`overlay autoupdate --apply` no longer makes the package's other versions
  lose their Manifest entries.** Promotion wrote the candidate's Manifest,
  which covers only the new version, over the published one, so every other
  ebuild of the package was left with no DIST entry and could not be
  installed (seen on `dev-util/flutter`). The other versions keep their DIST
  entries now: the published records stay, the candidate's are added, and a
  record for the same file is taken from the candidate.

- **`config.example.yaml` documents every key, in English.** It was the one
  Portuguese file in the repository and it never mentioned
  `autoupdate.distdir`, `autoupdate.distfiles_cache` or any of the
  `autoupdate.validate` block. All three are now there, commented out with
  their defaults. A new test fails when the code reads a key the example
  does not mention, the direction the existing strict-decode test could not
  see.

- **`docs/development.md` describes the project as it is.** Its tree listed
  eight files and a `logger/` package that no longer exist and left out the
  tray, snapshot, notice and desktop packages. It now maps every package and
  every `make` check, and it no longer tells you to `go install`
  govulncheck, which `go.mod` already provides as a tool.

- **`bentoo completion --help` gives Linux instructions.** The macOS/Homebrew
  line is gone, and the bash example writes to the per-user
  bash-completion directory instead of `/etc`, which needs root.

- **Refreshing a cloned repository works with git 2.43.** When `git pull
  --ff-only` fails, the git-clone provider falls back to a fetch and a
  `git reset --hard` to the fetched branch. That reset was written as
  `git reset --hard --end-of-options origin/<branch>`, which git 2.43 (the
  version Ubuntu 24.04 ships) rejects, so on such a system the fallback
  always failed. It is now `git reset --hard origin/<branch> --`: the ref
  still cannot be read as an option, since it always begins with `origin/`,
  and the trailing `--` keeps it from being read as a path.

## [0.33.3] - 2026-10-08

### Security

- **Built with Go 1.26.9 and `golang.org/x/net` v0.60.0** (GO-2026-6617,
  CVE-2026-97032: an HTTP/2 server crash from a race in the HPACK encoder of
  `net/http`). bentoolkit and `bentoo-tray` only act as HTTP clients, so the
  crash path is a server one, but `govulncheck` reaches the affected
  `net/http` code from the HTTP client paths and the toolchain in `go.mod`
  decides what a release links against. Both fixes were taken on the day
  they were published, ahead of the usual seven-day wait for new
  dependencies, because they are the Go team's own security releases.

### Fixed

- **`bentoo-tray` stopped while starting exits 0.** A SIGINT, SIGTERM or
  SIGHUP that reached the tray while it was starting made it log
  `bentoo-tray could not start` and exit `1` — also at the moment the session
  bus had already granted it its name but not yet answered, which made the
  tray's signal test fail intermittently. The name request now runs to its
  answer (bounded by 2 s), and a stop signal during startup stops the tray
  cleanly: the state is saved, the name released, and the exit code is `0`
  with an INFO line. The tray's version is 0.1.1.

## [0.33.2] - 2026-10-08

### Fixed

- **The LLM manifest repair reuses what the first attempt downloaded.** When
  the manifest step of a staged `overlay autoupdate --apply` failed, the
  repair started from an empty distdir and the first attempt's downloads were
  deleted, so every distfile was fetched again: a host that went down in
  between (download.documentfoundation.org on 2026-10-02) failed the repair
  too, and a large package paid the download twice. The first attempt's
  completed files now move into the repair's distdir; partial downloads
  (`.__download__`) and links into the distfiles cache are left behind.

- **A staged apply reuses distfiles already in the distfiles cache.** The
  staged tree holds the candidate ebuild alone, so a staged
  `overlay autoupdate --apply` could not name the distfiles the new version
  needs and downloaded them again even when the distfiles cache or the host
  DISTDIR already had them. The names now come from the published package's
  Manifest and ebuilds: the manifest step links the cached files in, and the
  LLM manifest repair receives copies of them (copies, never links, since
  the repair's agent can write to its directory).

## [0.33.1] - 2026-10-06

### Changed

- **`bentoo-tray` has its own version, starting at 0.1.0.** It printed
  bentoolkit's release under the wrong name (`bentoo version 0.33.0`); its
  version now lives in `internal/tray/version/VERSION`, embedded at build
  time, and moves only when the tray changes. `bentoo-tray --version` reads
  `bentoo-tray version 0.1.0`, with the bentoolkit release it was built from
  on the next line (`bentoolkit: 0.33.1`). The startup log line gains a
  `bentoolkit` attribute beside `version`, and the feed request's User-Agent
  is `bentoo-tray/0.1.0`. `bentoo --version` is unchanged.

## [0.33.0] - 2026-10-06

### Added

- **A JSON log file, and `BENTOO_LOG_LEVEL`.** Every run appends its
  diagnostics to `$XDG_STATE_HOME/bentoo/logs/bentoo.log`
  (`~/.local/state/bentoo/logs/bentoo.log` by default), one JSON object per
  line, at `info` and above whatever `--quiet` says. `BENTOO_LOG_LEVEL`
  (`debug`, `info`, `warn`, `error`) sets the stderr level when neither
  `--verbose` nor `--quiet` is given.

- **`make build` is reproducible, and `make checksums` writes `SHA256SUMS`.**
  The build date stamped into the binaries was the wall clock, and the
  checkout's path was embedded, so two builds of one commit never matched.
  The date now comes from `SOURCE_DATE_EPOCH`, else the last commit, and every
  build uses `-trimpath`. `make checksums` records the SHA-256 of the binaries
  in `build/`.

- **`requires` in `packages.toml`: packages that must move together.** A
  record can declare that its ebuild pins another package at a version
  upstream publishes beside its own — dev-lang/flutter pins
  `~dev-lang/dart-<dart_sdk_version>`, and four flutter bumps shipped with the
  previous pin. `--check` captures the required version from the same release
  object and reports whether it is `present`, `pending` or `missing`.
  `--apply` rewrites the pinned atom, and waits (keeping the pending entry)
  while neither the overlay nor ::gentoo holds that version. `--apply all`
  applies the required bump first. The JSON check report gains a
  `requirements` key on every package (empty when none).

- **A bump warns about `files/` named for the old version.** A patch named
  with the old `${P}` made the new ebuild die in `src_prepare`. When the ebuild
  builds `${FILESDIR}` paths from a version variable and such a file exists,
  `--check` and `--apply` now name it and the name the new ebuild will look
  for. They warn rather than rename, because the previous ebuild may still
  read the old name.

- **`--apply` regenerates the bumped package's md5-cache.** A bump left the
  previous version's `metadata/md5-cache` entry behind and wrote none for the
  new one, and the overlay once held 548 orphaned entries. After a successful
  apply, `egencache` now runs for that package against the checkout, which also
  removes the entries of versions `--clean` deleted. A failure is a warning and
  keeps the bump.

- **`--overlay <path>`, and the current checkout is used.** Every command took
  the overlay from `overlay.path` alone, so `--lint` in a worktree reported
  green on the main checkout it actually read. `--overlay` now chooses the
  overlay for one run. Without it, a run started inside another checkout of
  the same overlay (same `profiles/repo_name`) uses that checkout and logs
  that it did.

- **`aux_url` in `packages.toml`.** `aux_pattern` can read `aux_var`'s value
  from a URL other than the version page: jdtls's build id in `latest.txt`,
  codex's `RUSTY_V8_TAG` in the release's `Cargo.lock`, a TypeScript pin in a
  `package.json`. `{version}` in it is replaced by the detected version and is
  accepted only in the path or query, so an upstream value cannot choose the
  host. Credential headers are not sent there.

- **`mirrors` in `packages.toml`.** A record can list URLs that serve the same
  content as `url`. When `url` fails, each mirror is probed in order with the
  whole record, `script` records included, before `fallback_url`. Credential
  headers stay with `url`. A check fails as a network failure only when every
  source failed in transport; if `url` answered with something the record
  cannot read, the record is still blamed and the registry repair is offered.

- **A bump warns when ::gentoo ships the version it leaves behind, and its
  copy differs.** A bump copies our own ebuild forward and never reads
  ::gentoo, so every fix the distribution made to that revision was dropped in
  silence — a parity audit of the overlay traced 40 findings to it (mesa's
  `RUST_MIN_VER`, modemmanager's gobject-introspection floor). `--check` and
  `--apply` now print a stage line naming both files when ::gentoo ships the
  exact version being left behind and the two differ. It never blocks or
  changes the bump, and stays silent when ::gentoo lacks that version or the
  copies are identical. The tree is `/var/db/repos/gentoo`; `BENTOO_GENTOO_REPO`
  overrides it, and an empty value switches the check off.

- **`bentoo-tray`, a desktop notifier for the overlay's notices.** A new
  binary reads the notices feed (`https://obentoo.org/notices.json`, JSON Feed
  1.1 with ETag revalidation, a 1 MiB cap and backoff) and the unread bentoo
  news items, keeps the notices that concern packages installed from the
  bentoo repository, and shows them as desktop notifications (Open, Mark as
  read) and a StatusNotifierItem tray icon with a menu (Check now, Mark all as
  read, Pause, Quit). Security notices cannot be muted; a first run notifies
  only applicable security notices. It honours metered and offline
  connections through NetworkManager, opens notice pages through the desktop
  portal (never bypassing a portal that an administrator locked down),
  logs key=value lines at `BENTOO_TRAY_LOG_LEVEL`, and keeps its state
  in `$XDG_STATE_HOME/bentoo-notices/state.json` — including when the next
  check may run, so a restart honours the server's `Retry-After` and the
  backoff. Notification titles are sent as plain text, and the body is
  escaped only for a notification server that renders markup. `make install` adds its
  desktop entry, a systemd user unit and its icons; configure it in the new
  `tray:` config section. On GNOME the icon needs
  `gnome-shell-extension-appindicator`.

### Changed

- **Diagnostics on stderr are `key=value` lines, and secrets are redacted
  from them.** bentoo's warnings and errors now go through `log/slog`: each is
  one line such as `level=WARN msg="loading config: failed" err="..."`, with
  the variable data in attributes instead of inside the sentence. A failing
  command's cause reads `level=ERROR msg="command failed" err="..."`. Every
  secret bentoo resolved, and the value of any credential-named attribute, is
  written as `***`. Command results, reports and prompts keep their shape.

- **New dependency: `github.com/godbus/dbus/v5` v5.2.2.** The `bentoo-tray`
  desktop notifier speaks D-Bus (StatusNotifierItem, dbusmenu, Notifications,
  the OpenURI portal and NetworkManager); the standard library has no D-Bus
  client, and godbus is pure Go, so the binaries stay `CGO_ENABLED=0`.
  v5.2.2 was published on 2025-12-29.

- **The README is a front page; the reference moved to `docs/`.** Installation,
  build and test stay in `README.md`; configuration, every command family,
  runtime behaviour (exit codes, timeouts, headers), snapshots, the tray and
  development notes now live in `docs/configuration.md`, `docs/overlay.md`,
  `docs/distfiles.md`, `docs/notices.md`, `docs/autoupdate.md`,
  `docs/behaviour.md`, `docs/snapshot.md`, `docs/tray.md` and
  `docs/development.md`, moved unrewritten. Releases 0.1.0 to 0.29.1 moved to
  `docs/changelog/0.1.0-0.29.1.md`, also unrewritten.

- **Messages and flag help no longer cite internal tracker IDs.** A handful of
  operator-visible strings ended in references such as `(R9.6)` or `(S042-D7)`
  that pointed at planning notes outside the repository; the reference is gone
  and the rest of the sentence is kept. The `overlay validate` export note says
  "A later change" where it named a planning story. The `llm_prompt` warning
  now points at `docs/autoupdate.md` instead of the README. Source comments
  were cleaned the same way and shortened to the contract and its reason, and
  the new `make audit-comments` target, run by `make audit` and the CI lint
  job, fails on a tracker ID or a comment block of 20+ lines in non-test Go
  code.

### Removed

- **The playwright-go backend of the `script` parser, and its `playwright`
  build tag.** Two backends did one job; chromedp stays, because it drives the
  system Chrome with no Node.js driver and no `playwright install` step, and it
  is already the overlay ebuild's default (`USE=browser`). Build with
  `-tags chromedp` and keep a Chrome or Chromium executable on `PATH`. A build
  that still passes `-tags playwright` now fails to compile with a message
  naming `-tags chromedp`, so no binary ships silently without browser
  support; an ebuild using `USE=playwright` must move to `USE=browser`. The
  backend is now chosen by build constraints alone, and a binary built without
  the tag reports that it needs `-tags chromedp` and Chrome or Chromium.
  `github.com/mxschmitt/playwright-go` and its three indirect modules leave
  `go.mod`.

### Fixed

- **`overlay compare` and `overlay prune` stop on the first `Ctrl+C`.** The
  repository registry download ignored the interruption, so while
  api.gentoo.org never answered both commands ran on for 30 s (compare up to
  60 s: a second download for its hint), then reported "repository 'gentoo'
  not found". They now exit `1` at once and print `interrupted while fetching
  the repository registry`. `overlay autoupdate --revive`, `--revive-list` and
  `--check --revivable` share the fix.

- **`--check` caps requests per host, and script records share a
  navigation.** `--concurrency` bounded packages, not connections, so a host
  that stopped answering could hold as many hung requests as the run had
  workers. At most 6 are now in flight per host. Records running the same
  `script` on the same `url` (libreoffice and libreoffice-l10n) each opened
  their own browser; they now share one evaluation per run.

- **The autoupdate state follows `XDG_CONFIG_HOME`.** `config.yaml` honoured
  it, but `pending.json` and the version cache were hard-coded under
  `~/.config`. A run with a temporary `XDG_CONFIG_HOME` therefore read a
  scratch overlay and still wrote into the real pending list.

- **`--check <pkg>` and `--apply` honour `hold` and `enabled = false`.** Only
  the full scan skipped them: an explicit check fetched a held or disabled
  package and queued its update, and the applier refused held packages but
  applied disabled ones. A single check now reports the package as skipped
  without fetching it, and the applier refuses both, keeping the pending entry.

- **`series` is matched against the PV, not the revision.** An exact series
  such as `^1\.8\.3$` rejected the line's own `1.8.3-r1`, so every revbump
  broke the entry. Existing `(?:-r[0-9]+)?$` workarounds keep matching.

- **A bump fails when its Manifest misses a DIST entry.** `pkgdev manifest`
  exiting 0 did not prove every `SRC_URI` file was digested, and a batch bump
  once shipped 76 ebuilds that could not fetch. After the manifest step,
  `pkgcheck scan -k MissingManifest` now checks the new version, and a missing
  distfile fails the apply. When pkgcheck is absent or fails, the apply goes
  on and a warning says the check did not run.

- **`fallback_url` probes like the primary source.** The fallback kept only
  the parser fields, so it lost the record's `timeout`, its custom
  `User-Agent` and its `series`/`suffix`. With `select = max`, that let the
  fallback return a version outside the series and fail the whole check.
  The fallback now keeps `timeout`, `series`, `suffix`, `suffix_when` and
  every header except the credential-bearing ones (`Authorization`,
  `X-Api-Key`, `X-Auth-Token`, `Private-Token`), which stay with the primary
  host.

- **A network failure no longer offers "Fix registry?".** Timeouts, TLS EOF,
  exhausted retries and an open circuit breaker now also wrap the new
  `ErrUpstreamUnreachable`, and the interactive LLM repair skips them, so a
  distracted "y" cannot rewrite a record that was correct. A host that does
  not resolve is still offered, since that is usually a mistyped `url`.

- **Ctrl-C now stops `overlay autoupdate --apply all` and in-flight network
  calls.** A cancelled `--apply all` used to keep dispatching every queued
  package, each one copying and rewriting an ebuild before it failed and rolled
  back; it now starts no package after the cancel and reports each package it
  never began as a failure, so the exit status stays non-zero. The Claude API,
  OpenAI and Ollama requests, the GitHub rate-limit lookup and the host
  `portageq distdir` query now end within seconds of a cancel instead of running
  to their own timeouts. `overlay analyze` now applies its 60 s LLM timeout,
  which caps a slow local Ollama model below Ollama's own 120 s.

## [0.32.0] - 2026-10-01

### Security

- **BREAKING: an authenticated fetch only sends `BENTOO_FETCH_*` variables.**
  A `packages.toml` record's `fetch_serial_env` and `fetch_form_env` could name
  any variable — `GITHUB_TOKEN`, an LLM key, a `BENTOO_REPO_<NAME>_TOKEN` — and
  the value was resolved through the secrets chain and posted to the record's
  own `fetch_url`, which the record's author chooses. A name that does not begin
  with `BENTOO_FETCH_` now refuses the record before anything is resolved, in
  the sweep, in `bentoo distfile` and in `overlay autoupdate --lint`, with the
  same message naming the variable, the key and its replacement; the rest of a
  sweep still runs. `--lint` now runs the authenticated-fetch parser, so it also
  reports the records the fetch would refuse for other reasons. `BENTOO_FETCH_*`
  variables are also never expanded in a header: they stay literal with a
  `Warn`. **Migration:** rename each variable `X` to `BENTOO_FETCH_X` in the
  record and in the environment or secrets file.
- **The `BENTOO_FETCH_` refusals print the record's variable names quoted.**
  A `fetch_form_env` name is URL-decoded, so `%0A` or `%1B` put a raw newline
  or escape sequence into `--lint`, the sweep report and `bentoo distfile`
  output — enough to forge a report line or drive the terminal. The name and
  its suggested `BENTOO_FETCH_` replacement are now quoted and escaped in both
  the `fetch_serial_env` and the `fetch_form_env` refusal.
- **An authenticated-fetch variable name may use only letters, digits and
  underscore.** A name such as `BENTOO_FETCH_X%0Aforged` passed the prefix rule
  and, being unset, reached the missing-secret error with its newline or escape
  sequence raw. `fetch_serial_env` and every `fetch_form_env` variable outside
  `BENTOO_FETCH_[A-Za-z0-9_]+` now refuse the record before anything is
  resolved, in the sweep, `bentoo distfile` and `--lint`; the missing-secret
  error and the unknown `fetch_*` key error print what they name quoted.
  **Migration:** a name such as `BENTOO_FETCH_A-B`, resolvable only from the
  secrets file, must be renamed (e.g. `BENTOO_FETCH_A_B`) in the record and
  in the secrets file.
- **`overlay autoupdate --list` escapes what it prints from the pending
  list.** It reads the pending-updates file without loading `packages.toml`,
  so the key check above never sees it: an apply error carries text from
  outside, and an entry recorded before that check may carry a hostile key.
  Each field — package, versions, status, error — is now printed quoted when
  it holds a non-printable character, so it can no longer drive the terminal
  or forge a line; printable entries print exactly as before.
- **A `packages.toml` key holding a non-printable character refuses the
  load.** A quoted TOML key can hold an escape sequence, a newline or a
  bidirectional format character (`["cat/x\u001b[2J"]`), and the key is printed
  raw by every message that names a record. Such a key now stops the file from
  loading, with an error naming it quoted, so it can no longer drive the
  terminal or forge lines in `--lint`, sweep, check, apply or `bentoo distfile`
  output; `--lint`'s text scan, which runs before the parser, prints it
  quoted too.
- **A `packages.toml` key can no longer name a directory outside its
  category.** A key whose category or package half is `.`, `..`, or holds a
  `/`, `\` or NUL byte (`../x`, `cat/..`) used to be joined under the overlay
  as-is; it is now refused as an invalid package key, naming the half that was
  refused. No real Gentoo atom has that shape.
- **`fetch_url` and `fetch_id_url` must be absolute `http(s)` URLs with a fixed
  host.** A `file:`, `ftp:` or relative template, or one with `{id}` or
  `{version}` in the host, is refused by the download and by `--lint`, so a
  catalogue id or an upstream version can no longer choose which host receives
  the request. Placeholders in the path or query still work.

- **Every `claude` agent bentoo spawns now receives an allow-listed environment,
  not bentoo's whole one.** The text client, the manifest, registry and build
  fixers and the bump reviewer used to inherit every variable bentoo had —
  `GITHUB_TOKEN`, the ntfy token, the SMTP password, every `BENTOO_*` value — in
  a process that reads untrusted upstream pages. The child now gets only `PATH`,
  `HOME`, `TMPDIR`, `LANG`, `TERM`, `CLAUDE_CONFIG_DIR`, the CA and proxy
  variables, and the `LC_*` and `XDG_*` families; the manifest fixer also gets
  `PORTAGE_*` and exactly one `DISTDIR`, the one the applier computed. In bare
  mode the resolved API key is the only `ANTHROPIC_API_KEY` entry, so a key the
  caller's shell exports can no longer sit beside it as a duplicate (this is
  also why `TestChildEnv_InjectsResolvedKey` failed inside a Claude Code
  session). `ANTHROPIC_BASE_URL` and the Bedrock/Vertex switches are not on the
  list, so those setups lose the agents until a later change adds them.

- **One builder now writes every `claude` agent's permission arguments.** Each
  rule is its own argv element; `Read` and `Edit` are scoped to the agent's own
  directory as `Read(//<dir>/**)` and `Edit(//<dir>/**)` (writes are scoped
  through `Edit`, the only rule the CLI consults for them); the secrets files
  are denied by path; WebFetch is granted only as `WebFetch(domain:<host>)` for
  hosts that are lowercase DNS names — a host from `packages.toml` or `pkgdev`
  output that is not one is dropped with a warning, so it cannot widen or forge
  a rule. Every agent runs with `--permission-mode dontAsk`, no user, project or
  local settings, no MCP servers or account connectors, and inline settings that
  block reads outside its working directories and refuse `bypassPermissions`.
  A directory outside `[A-Za-z0-9._+@/-]` (for example an overlay path with a
  space) now stops the fixer before it spawns, with an error naming the path.

- **An IPv4 literal never becomes a WebFetch host.** A host whose last label is
  a number — `127.0.0.1`, `10.0.0.1`, `169.254.169.254`, and the spellings a
  WHATWG URL parser also reads as an address, such as `127.1` and
  `127.0.0.0x1` — is dropped with a warning, like any other host that is not a
  DNS name. The manifest fixer takes hosts from the URLs `pkgdev` prints, which
  upstream controls, and a literal there would grant a fetch straight to
  loopback, the internal network or a cloud metadata endpoint. This judges the
  host's spelling, not its resolution: a DNS name that resolves to such an
  address (for example `169.254.169.254.nip.io`) is still granted, and only
  egress rules for the agent's user close that — see `SECURITY.md`.

- **Every agent now runs under those scoped permissions.** The manifest fixer
  keeps `Read`, `Edit`, `Write`, `Bash(pkgdev *)` and WebFetch — its WebFetch
  reaches the hosts of the package's `url`/`fallback_url` and the http(s) URLs
  `pkgdev` printed, plus GitHub; `Bash(wget *)`, `Bash(cat *)` and
  `Bash(ls *)` are gone. The registry fixer keeps `Read`, `Edit`, `Write` and
  WebFetch to its entry's own hosts; `Bash(curl *)` is gone. The build fixer's
  `Read`/`Edit` reach only the staged package directory. The text client (no
  tools at all) and the bump reviewer (`Read` only) now run in a private 0700
  directory created for each call and removed afterwards, instead of in
  bentoo's working directory, which a read-only tool could otherwise read.
- **BREAKING: a header credential now goes only to the hosts it belongs to.**
  `packages.toml` lives in the overlay repository, so any contributor could
  write a record pairing `url = "https://evil.example"` with
  `X-Api-Key = "${GITHUB_TOKEN}"`, and `bentoo overlay autoupdate --check`
  would send the maintainer's token there. Each expandable variable is now
  bound: `GITHUB_TOKEN` to the GitHub hosts over https, `GITLAB_TOKEN` to
  `https://gitlab.com`, `BENTOO_*` to the host of the package's own `url` or
  `base_url`. A record that breaks its binding fails its own check, before any
  request is sent, with a message naming the header, the variable and the
  host; the rest of the batch runs, and the refused package does not fall back
  to `fallback_url` or the LLM stage. **Migration:** move a credential that must
  reach another host into a `BENTOO_*` variable.
- **BREAKING: `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` are no longer expanded in
  a header.** No upstream a package names has a reason to receive the
  maintainer's LLM keys. A reference is now passed through literally with a
  `Warn`. **Migration:** rename the variable to `BENTOO_*`
  (e.g. `${BENTOO_OPENAI_API_KEY}`).
- **A redirect no longer carries a credential to another host or to plain
  http.** Go forwards custom headers such as `X-Api-Key` and `Private-Token`
  to whatever host a redirect names; every client that can carry a credential
  now drops those headers once a redirect chain leaves the original host, and
  refuses an https-to-http redirect instead of sending the token in
  cleartext. An authenticated fetch whose form is redirected (307/308) to
  another host is refused rather than re-posting the serial there.
- **BREAKING: a GitLab repository must use https, and the automatic GitHub
  token is sent over https only.** An `http://` GitLab repository URL sent
  `PRIVATE-TOKEN` in cleartext; it is now rejected with a message saying https
  is required. A request to `http://api.github.com/` no longer receives the
  token.
- **BREAKING: bentoolkit's own secrets are never expanded in a header.**
  A `BENTOO_*` variable goes to the host of the record's own `url`, and the
  record's author picks that url, so a `packages.toml` PR naming
  `${BENTOO_NTFY_TOKEN}`, `${BENTOO_SMTP_PASSWORD}` or a
  `${BENTOO_REPO_<NAME>_TOKEN}` could have received the maintainer's
  notification, mail or repository token. Those references now stay literal
  with a `Warn`. **Migration:** give a header credential its own `BENTOO_*`
  name. A GitLab URL rejected for not using https is also shown without its
  userinfo or query, so a token written into it no longer reaches the log.

- **Autoupdate no longer writes a malformed upstream value into an ebuild.** An
  `aux_pattern` capture and an upstream commit hash used to reach the bash
  source of the new ebuild unchecked. A captured `x"; touch /tmp/pwned; "`
  closed the quoted assignment and left a shell command for `emerge` to run.
  Now an aux value outside `[A-Za-z0-9._+-]{1,128}` and a commit hash that is
  not 40 lowercase hex are refused before anything is staged:
  - Under `--apply`, the package is marked failed with the value named, its
    directory is left byte-identical, and an `--apply all` batch carries on
    with the others.
  - Under `--check`, the package is reported skipped with the value named, and
    no gate runs on it. The check stages the same ebuild and runs `pkgdev
    manifest` and the configure step on it, so it was a second way in.
  - The function every ebuild writer calls refuses the value as well, so a
    writer added later is covered without having to remember the check.
- **An accepted value is written literally.** The replacement template expanded
  `$1`/`${2}` inside the value itself, so `a${1}b` became `aMY_BUILD="b`. The
  value's `$` is now escaped before substitution.

- **A `snapshot.toml` value can no longer inject a directive into btrbk.conf
  or a systemd unit.** A newline in a value reached `btrbk.conf` or the timer
  unit as an extra directive, and a config path containing a space, `%` or `$`
  was split or expanded by systemd. A control character in any value those
  files use is now refused with an error naming it, and `ExecStart` arguments
  are quoted and escaped as systemd expects. Ordinary values render exactly as
  before.

- **A branch or path beginning with `-` can no longer be read as a git
  option.** `overlay add` stages a path after `--`, and merge, fast-forward and
  rebase pass the branch after `--end-of-options`, so such a name is taken as a
  file or a revision.

### Added

- **`bentoo notice new` and `bentoo notice revise` author a notice in both
  places it lives.** One command writes the overlay's GLEP 42 news item
  (`metadata/news/<id>/<id>.en.txt`) and, when the new `notice.site_path` key
  is set, the site's `src/content/notices/<id>.yaml`, with one shared ID.
  Everything portage or the site build would reject — type, severity, the ID's
  short name, title and summary lengths, `--affects` ranges, control
  characters, a future date — is refused before any file is written, and a
  failed second write removes the first. `revise` opens the current text in
  `$VISUAL`/`$EDITOR`, bumps the news item's `Revision` and the site file's
  `updated` together, and replaces both atomically. Neither command runs git:
  both print the paths written and the commands to review, commit and push.

- **`--format json` for `overlay compare` gains three keys.** Every package now
  has `cause` and `error`. Both are always present, both are `""` on a row that
  did not fail, and `error` holds the full text on one line. The run gains
  `reading_failures`, a list of `{"cause", "count"}` over the same comparisons
  `unread` counts. These keys are only added: `schema` stays `2`, and no
  existing key is renamed or removed.

### Fixed

- **`bentoo notice` no longer loses the text typed above the scissors line.**
  The editor's file ends its instructions with a scissors line, and only what
  was below it used to be kept — so a body typed at the top of the file, where
  most editors open, was discarded, `notice new` failed with an empty body and
  the temporary file went with the text. Text above the line is now kept; only
  the `#` instruction lines there are dropped, and everything below the line is
  still kept as written.

- **A Manifest `DIST` line named `.` or `..` is no longer read as a
  distfile.** Joined onto the distdir, such a name points at the distdir
  itself or its parent. Names that merely contain dots (`...`, `.foo`,
  `foo..tar.gz`) are still read.
- **An ebuild path whose category or package is `.` or `..` is refused.**
  `././x/x-1.ebuild` used to parse with category `.`, and the path it
  rendered back (`./x/x-1.ebuild`) did not parse at all.

- **One failing upstream no longer stalls every check.** The autoupdate HTTP
  client kept a single circuit breaker for all hosts, so two dead hosts made
  every other package fail with "circuit breaker open" for 30 s. Each
  upstream host:port now has its own breaker, and the refusal names the host
  that was refused.
- **Ctrl-C no longer waits out retries and lookups.** A retry wait now ends as
  soon as the check is cancelled or its deadline passes, and the error says
  "cancelled" or "deadline exceeded". `overlay compare`, `--list-revivable` and
  revive stop their in-flight GitHub and GitLab lookups instead of running into
  their 30 s timeouts. A git-clone lookup no longer starts once the command is
  cancelled, and a clone already in flight stops with it (it was, and still
  is, bounded at 2 minutes); updating an existing clone is not yet
  cancellable.
- **Retries are spread out and honour `Retry-After`.** The 1 s / 2 s / 4 s
  backoff is now the ceiling of a random wait, so many packages retrying one
  host no longer retry in lockstep. A 429 or 503 carrying `Retry-After` waits
  exactly that long; one asking for more than 60 s, or for longer than the
  check has left, fails at once with a message naming the host and the wait.
- **Provider and registry responses are bounded.** GitHub and GitLab API
  bodies and the repository-registry download are capped at 10 MiB, and the
  registry download gives up after 30 s and falls back to the eselect cache.
- **Error causes survive wrapping.** A failed check, retry exhaustion, a
  manifest or compile failure and an authenticated distfile fetch keep their
  underlying cause (cancellation, timeout, process exit) reachable, without
  changing their text; the authenticated fetch still never shows a credential.

- **`overlay rename` no longer overwrites one ebuild with another.** The rename
  strips the revision, so `foo-1.0.ebuild` and `foo-1.0-r1.ebuild` both mapped
  to `foo-1.1.ebuild`. Both were moved, the second on top of the first, and the
  run reported `Renamed 2 ebuild(s)`. The newer revision was silently lost, with
  or without `--force`. A shared target is now shown in the preview with every
  source, and the rename exits 1 before prompting and before moving anything,
  including under `--dry-run`. `--force` does not override this.
- **`overlay rename` refuses a new version that is not a version.** A value such
  as `1.2/../../../x` became part of the target path and moved the ebuild out of
  its package directory. It is now refused before the configuration is read, and
  the value is named.
- **A `~name` distdir or distfiles-cache path is refused, not misread.**
  `~alice/distfiles` was expanded to `$HOME/alice/distfiles`, a directory under
  the *current* user's home. Only `~` and `~/…` are expanded now. A distdir in
  the `~name` form fails and names the path; a cache path in that form skips
  prepopulation, as a missing cache already does.
- **A bump whose auxiliary value could not be resolved is held, not shipped
  stale.** When a package declares `aux_pattern` or `commit_sha_path` and that
  value cannot be fetched or captured, the check used to queue the bump anyway,
  and the new ebuild shipped the previous release's `MY_BUILD` or `BUILD_ID`.
  The check now records the cause and leaves the pending list alone. The value
  is fetched again on the next check, so the bump goes through on its own once
  upstream serves it.

- **Versions are now ordered by PMS §3.3, so distinct versions no longer compare
  equal.** The comparison dropped the trailing letter, ranked only the first
  suffix and padded numeric components with zeros: `1.1.1w` and `1.1.1v`,
  `1.0_rc1_p1` and `1.0_rc1`, `1.0.0` and `1.0` all read as one version, and
  `1.01` read as `1.1`. Every number is now compared at any length, so large
  date stamps no longer collapse either.

  `overlay compare`, baseline selection and autoupdate may therefore report a
  different newest version for such pairs — a package once shown as up to date
  can now show as outdated. A string that fails the version grammar now orders
  below every real version instead of being read as a near-zero one.

- **`bentoo snapshot run` ships through `archive` and `restic` again.** Both
  engines returned a snapshot without its path, so the ship ran
  `btrfs send ""` or bound-mounted an empty path and failed on every run. The
  path is now carried from snapper and btrbk, and a snapshot neither engine can
  identify is refused before anything runs.
- **An `ssh` ship under the snapper engine is refused instead of reporting a
  success that sent nothing.** Only btrbk sends to ssh targets; the config is
  now rejected with a message pointing at `archive` or `restic`.
- **A restic snapshot mount is never deleted while it may still be mounted.**
  The cleanup ignored a failed unmount and then removed the mountpoint
  recursively, walking into the snapshot. It now leaves the directory in place
  when the unmount fails, and removes it only after a successful one.
- **The archive ship streams instead of holding each stage in memory.** A
  multi-GB `btrfs send` was held in RAM about twice. The stages are now
  connected by pipes, and a failed upload removes the partial object it left.
  Restore still waits for the download and decompression to succeed before
  `btrfs receive` starts, so a failed download leaves no partial subvolume.
- **A half-written bentoo block no longer eats `/etc/portage/bashrc`.** A begin
  marker with no end marker made install and uninstall drop every line after
  it, or the whole file. Both now stop with an error naming the file and how
  to repair the block.
- **An SMTP server that stops answering no longer hangs the timer-driven run.**
  Sending a notification is bounded at 15 s and stops when the run is
  cancelled; the error names the step and the server, never the password or
  the message.

- **Ctrl+C stops the whole run, not just the process bentoo started.** Only
  the direct child was killed, while `pkgdev manifest`, the build's `ebuild`,
  the `claude` CLI and every `git` call left descendants holding the output
  pipe, so the run kept waiting — for a download, a compile of hours, or
  forever on a stalled `git push`. A cancel now sends SIGTERM to the child and
  everything it started, then SIGKILL 5 s later, and the run returns within
  6 s. SIGHUP, from a closed terminal, now cancels like SIGINT and SIGTERM.
- **A cancelled build says it was interrupted.** A privileged compile stopped
  by a cancel is no longer reported as a compile failure and no longer starts
  the LLM build fixer; its partial log is still kept. The `sudo` password prompt
  keeps working, and if the stop cannot reach the privileged build, the error
  names its process and says it may still be running.
- **The distfile lock wait stops on cancel** instead of waiting up to 2
  minutes, and `overlay analyze` stops starting new packages once cancelled,
  runs at most 3 at a time, and records a package that panics instead of
  crashing.
- **Captured child output keeps its last 64 KiB**, with a line saying how much
  was dropped, so a noisy failure no longer produces an unbounded error
  message. Build verdicts and retained logs still read the whole transcript.

- **A crash no longer leaves a truncated ebuild, registry or state file.**
  `cache.json`, `pending.json`, `analysis_cache.json`, `packages.toml` and the
  commit-hash and aux-variable substitutions in an ebuild are now written to a
  synced temporary file beside the target and renamed into place, and the
  directory is synced after the rename. A killed run leaves the previous file
  intact; its dot-prefixed temporary is removed by the next
  `bentoo overlay autoupdate` run that writes the overlay (every mode but
  `--list` and `--lint` without `--fix`). The file's mode is now set before the
  rename, so on a filesystem that refuses `chmod` (some network, FUSE or
  read-only mounts) saving these files fails with an error naming the file,
  where the cache, pending and analysis-cache saves used to warn and go on.
- **Copying or promoting an ebuild never overwrites one.** The new ebuild is
  published through a hard link that fails if anything already sits at its name,
  including a file created after the existence check or a dangling symlink, so
  the existing file is left untouched and no partial ebuild is left behind. A
  copied ebuild keeps the source ebuild's mode instead of one chosen by the umask.
- **Two runs no longer erase each other's cache and pending entries.** Each save
  of the three state files holds a lock in the config directory, re-reads the
  file and merges it key by key: an entry this run added, changed or deleted wins,
  and every other entry keeps what is on disk.
- **One `bentoo overlay autoupdate` run per overlay.** Every mode that writes the
  overlay or its registry holds `<overlay>/.autoupdate.bentoo-lock`; a second run
  waits up to 2 minutes and then exits 1 naming the lock and the holder's PID.
  The lock file is removed on every exit, whatever the exit code; a lock left by
  a killed run is reclaimed at once. A symlink, FIFO or directory planted at a
  lock's name is refused instead of followed. `--list` and `--lint` without
  `--fix` take no lock.
- **The registry keeps its mode.** `overlay analyze` and the registry fixer's
  restore no longer turn a `0644` `packages.toml` into `0600` under a restrictive
  umask; a new registry is created `0644`.
- **`bentoo overlay add` never stages bentoo's scratch files**, so a leftover
  temporary or lock file cannot be committed.
- **The distdir writability probe no longer follows a symlink** planted at its
  probe name; it reports the distdir as not writable instead of truncating the
  link's target.

- **`overlay compare` now says why an upstream lookup failed.** A package whose
  lookup failed used to read `error` and "the comparison failed, so nothing is
  known about how the two versions relate", whether GitHub rate-limited the
  run, rejected the token or the network dropped. The row's reason now names a
  cause and carries the error text:
  `the upstream lookup failed (rate-limited): API rate limit exceeded: …`. The
  causes are `rate-limited`, `auth`, `network`, `not found upstream` and
  `other`. The status word is still `error`, and no verdict or count changes.
  Before the text is recorded, the token the run resolved is replaced with
  `***`.
- **A failed AI review now says why.** A row whose review failed is marked
  `[reading failed: <cause>]` instead of the bare `[reading failed]`. The cause
  is one of `timed out`, `could not start`, `exited non-zero`,
  `empty or unusable reply`, `cancelled`, `ebuild unreadable` or `other`. The
  note about unread comparisons now ends with the counts per cause, for example
  "3 timed out, 1 could not start". So does the realignment summary, for example
  "(2 timed out, 1 empty or unusable reply)". A row whose review failed with no
  recorded cause still shows the bare marker. `reading` keeps its four values,
  and every `claude` failure sentence stays word for word the same.
- **A GitHub or GitLab HTTP 401 can now be told apart from other API errors.**
  It still matches the generic API error, as before, so nothing that already
  handled it changes. It now also matches a new "authentication rejected"
  error, and that is what `overlay compare` reports as `auth`.
  Known limit: GitHub answers a permission problem with 403, and every 403 is
  still read as a rate limit, so a permission 403 reads `rate-limited`. The
  error text next to it shows an empty reset time.
- **`overlay diff` recognises git's "differences found" exit status even when it
  arrives wrapped.** It is now read with `errors.As` instead of a type
  assertion. Nothing visible changes today, since the error reaches the check
  unwrapped.

- **`overlay manifest` is now cancelled cleanly by SIGHUP.** It stopped
  cleanly on SIGINT and SIGTERM, but SIGHUP (a closed terminal) killed it
  mid-run. SIGHUP now cancels it like the other two, and it exits `1`.

### Changed

- **Failure messages now go to stderr; report rows stay on stdout.** The
  refusals and failures of `overlay prune`, `overlay autoupdate
  --mark-auto-disabled`, `overlay validate`, `overlay autoupdate --lint --fix`,
  the registry update after `overlay autoupdate --check`, `overlay staged
  clean`, `overlay compare --realign` and `overlay analyze --all` — 23
  messages, with the hint lines printed beside them — used to be written to
  stdout. The
  failure messages of these commands now go to stderr, so a script that reads
  stdout gets only the report; a row or count inside a report stays on stdout.
- **A second signal now terminates a cancellable command immediately.** A
  command that stops cleanly on `Ctrl+C` (SIGINT, SIGTERM or SIGHUP) used to
  swallow a second signal while it wound down; the second one now ends the
  process at once. The first signal behaves as before (apart from SIGHUP at
  `overlay manifest`, under Fixed).
- **Exit codes and first-signal behaviour are unchanged.** Every command exits
  with the same code as before in every situation, and the first signal is
  handled as before: one `Ctrl+C` at the `overlay commit` and `overlay analyze`
  confirmation prompts still ends the command. The README's "Exit codes"
  section now documents the whole contract — success, failure, usage error,
  `overlay validate`'s `1`/`2`/`130`, the batch `0`/`1`/`2` of `overlay
  autoupdate --check` and `overlay analyze --all`, and what each cancellable
  command does when interrupted. `notice new` and `notice revise` are
  cancellable too: a first interrupt while the editor is open stops it, writes
  nothing and exits `1`, as before; a second one now terminates them at once.

- **`make lint` runs exactly what the CI Lint job runs.** It builds
  golangci-lint v2.13.2 (the CI pin; `make lint-pin-check` fails if the two
  drift) and lints the default, `chromedp` and `playwright` builds. Every `make`
  target now runs the Go toolchain `go.mod` names, as CI does. The lint set adds
  gofmt/goimports, errorlint, usetesting, tparallel, forbidigo (no `fmt.Print`
  in library packages) and a strict nolintlint, and gosec now checks G304,
  G703 and G704 everywhere. `make clean` also removes `cov.out`, `coverage*.out`
  and the `bentoo` binary. Pre-commit pins gitleaks by commit and adds gofmt and
  `go vet` hooks.

- **Error causes stay reachable.** Library errors that formatted their cause
  with `%v` now wrap it, so `errors.Is` and `errors.As` see the cause (a Claude
  run's context error, an LLM request's `*url.Error`, an `fs` or `json` error)
  under the same sentinel as before. When a failed manifest was followed by a
  failed or skipped LLM fix, the manifest failure is now the error's cause and
  the fix attempt's error is only context in its text; it used to be the other
  way round. The message text is unchanged apart from that order.

- **`make test` and `make coverage` run with the race detector in random
  order**, as the CI test job now does. A failing run prints
  `-test.shuffle <seed>`, and `make test SHUFFLE=<seed>` replays that order.
  `make fuzz` runs every fuzz target for `FUZZTIME` each (default 30s).

- **A tool the agent was refused is now named in the failure.** When a fixer
  or the bump reviewer fails, the error ends with `refused tools:` and the
  refused tools — `WebFetch(<host>)` for a fetch, the bare name otherwise —
  and never the refused call's input. When a manifest or registry fix
  "succeeds" but its re-check fails, the "still failed" / "still failing after
  fix" message adds `(agent was refused: …)`. Nothing retries with wider
  permissions, and no setting widens an agent's tools, hosts or paths.

- **`HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are honoured** by every client
  built on the shared transport, including the GitHub and GitLab providers,
  which used to connect directly.
- **A server that accepts a connection but sends no response headers within
  30 s** now ends the attempt as a timeout; the autoupdate client retries it
  like any other timeout. A larger
  `http_timeout` raises this wait with it, and the Ollama client waits up to
  its full 120 s for a non-streaming reply.

- **git calls are time-bounded:** 5 minutes for network operations (push,
  fetch, clone, the provider's update) and 1 minute for local ones, with an
  error that names the operation and the bound. A provider clone may now take
  5 minutes instead of 2.

### Documentation

- **`SECURITY.md` now states the boundary the code enforces.** It listed 0.11.x
  as the supported version and claimed that only secret paths ever reached a
  subprocess, while every agent inherited the values. It now lists 0.32.x,
  says which environment an agent receives and that other subprocesses
  (`pkgdev`, `git`, `ebuild`) still inherit bentoo's, and gains an "LLM Agents"
  section: each agent's tools and directory scope, the denied secrets paths,
  the WebFetch host rule, the pinned settings, and the residual risk that
  permission rules do not confine a program such as `pkgdev` run by the agent.

## [0.31.1] - 2026-09-22

A maintenance release: dependency updates, one piece of source hygiene, and the
CI lint pin caught up. Nothing a user drives changed — see the rendering note
below for why that is a measurement rather than a hope.

### Security

- **A literal U+202E RIGHT-TO-LEFT OVERRIDE lived in the source, as the value of
  the constant that exists to reject it.**
  `internal/common/provider/gitclone_validators.go` defined
  `const rtlOverride = '<the character itself>'`. It is invisible, so review
  never showed it, and it reorders the rendering of any diff that touches the
  line — the [Trojan Source](https://trojansource.codes/) trick the constant
  defends against. Now spelled `'\u202e'`: identical rune, no behaviour change,
  and `gitclone_test.go` had always used the escape form, so the two halves of
  the same defence now agree.

  **This was never a vulnerability.** The validator has always rejected U+202E
  at `gitclone_validators.go:174` and tests cover it. What shipped was an
  invisible control character in a file humans are expected to read. A repo-wide
  sweep for U+202A–202E, U+2066–2069, U+200E, U+200F and U+061C across every
  `*.go` now comes back empty.

  No CI check could have caught it at the time: gosec's G116 arrived after the
  v2.1.6 the lint job pinned. That gap is closed in this release, so a
  recurrence would now fail CI.

- **Three modules were bumped inside the release quarantine, as a deliberate
  override.** `charmbracelet/x/exp/teatest` and `.../golden` at 63 hours old,
  `golang.org/x/telemetry` at 23 — against the 7-day `cooldown` in
  `.github/dependabot.yml`. Ages read from the proxy's own timestamps, not from
  the date inside the pseudo-version, which looks older than the quarantine
  clock actually is.

  The quarantine's value is that someone else finds the hijack first, so waiving
  it means reading the upstream diffs yourself. `charmbracelet/x`
  `3986e9119cf9..53e2afe73ae5` is two commits touching **one** file,
  `powernap/pkg/config/lsps.json`, with nothing under `exp/` at all — making the
  teatest/golden bump a no-op, byte-identical content under a new
  pseudo-version. `golang/telemetry` `4bcc4b2ee518..bdcd072333a6` is three files
  of regenerated config by `gobot@golang.org` plus a pkgsite counter.

  None of the three reaches the shipped binary: `x/telemetry` arrives only
  through `golang.org/x/vuln/cmd/govulncheck` (the `tool` directive), and
  teatest and golden are test-only. Precedent for the override and for cutting
  it as a patch is 0.30.2.

- **A direct dependency had never been proposed by Dependabot, and only the
  absence of a PR recorded it.** `github.com/chromedp/cdproto` sat five weeks
  behind — pinned 2026-08-04 against a 2026-09-12 upstream — with
  `gh pr list --state all --search cdproto` empty across the entire repo
  history. It carries no semver tag (`@v/list` answers empty), which looks like
  the same resolver trap already documented for the `charmbracelet/x/exp` pair,
  but that is inferred from the missing PR rather than read from an update-job
  log, and those logs are UI-only. The cooldown (the candidate was 10 days old)
  and `open-pull-requests-limit` (5 of 10 in use) were both ruled out by
  measurement.

  Bumped by hand and documented in `.github/dependabot.yml`, deliberately **not**
  added to `ignore:` — ignoring it would cause the silence rather than record it.
  It is compiled only into the chromedp-tagged build, where
  `go list -deps -tags chromedp ./cmd/bentoo` links 58 of its packages; the
  default build that `make build` produces links none.

- **The chain carries no advisory.** govulncheck is clean under the pinned
  `go1.26.8` rather than merely under the newer local toolchain — different
  questions, because govulncheck judges the stdlib of whichever toolchain runs
  it. osv-scanner at v2.6.0 (the exact pin the workflow now uses), trivy,
  gitleaks over 450 commits and zizmor are clean too; `go mod verify` passes and
  `go mod tidy -diff` is empty. GitHub reports no open Dependabot or code
  scanning alert.

### Fixed

- **Two comments in `internal/autoupdate/validate/stage.go` pointed at the wrong
  code, and had for some time.** They anchored to `applier.go:471` for the mode
  the applier gives its `logs/` directory and to `applier.go:953` for
  `copyEbuild` refusing an existing destination. Neither line held that code any
  more: 471 is `WithApplierContext` and 953 is a `ValidationSourceStaged`
  assignment. Both now name identifiers instead — `NewApplier`'s
  `os.MkdirAll(applier.logsDir, 0o750)` and `(*Applier).copyEbuild` — which
  survive every edit above them.

  Worth knowing how this surfaced, because the mechanism will repeat.
  `TestAnchorCitationsResolve` sweeps only the files the current story touched,
  derived from `git diff <storyBaseCommit>..HEAD`. `stage.go` entered that set
  only when the lint work above edited it, which pulled two long-standing
  anchors into scope. The guard also **skips itself** when the story base commit
  does not resolve — which is the case on CI's checkout — so it is a local-only
  gate, and a green CI never said anything about it either way.

### Changed

- **The CI lint pin moved from golangci-lint v2.1.6 to v2.13.2 — twelve minors —
  and ten findings had to be cleared before it could.** The old pin made a local
  run with a current binary report findings the job could not see, and it lives
  inside a `run:` script, where neither Dependabot ecosystem reads it, so nothing
  would ever have said it was stale. Moving such a pin is never a version bump:
  every check added in between becomes a CI failure first.

  Four were mechanical (`QF1012` → `fmt.Fprintf`, `reflect.Ptr` →
  `reflect.Pointer`). One was a false positive — `min()` already bounds both
  indexes, which gosec cannot see. Two were a deprecation that does not apply:
  `go/parser.ParseDir` is deprecated *because* it ignores build tags, which is
  exactly what a source-level assertion over every non-test file needs.

  Three were `G122` symlink-TOCTOU warnings on `filepath.WalkDir` callbacks,
  silenced on a stated threat model rather than waved away: all three already
  handle symlinks deliberately, and what remains is the residual race, winnable
  only by someone who can already write into the host's Portage repo or its
  fetched DISTDIR. The comment records that `os.Root` **is** a viable migration
  if that stops holding — each root is walked separately and `os.Root` carries
  `Lchown`, `Chmod`, `Lstat`, `Readlink` and `Symlink` as of Go 1.26 — so it is
  a decision to revisit, not a dead end.

  One detail worth carrying forward: `QF1012` had **seven** sites, not the three
  reported. `max-same-issues` defaults to 3, so a finding count is not a work
  estimate.

- **Rendering is provably unchanged, despite two of the bumps landing in
  rendering libraries.** `mattn/go-runewidth` v0.0.30 rewrote `Wrap` to measure
  grapheme clusters instead of runes, and `xo/terminfo` v1.2.0 carries parser and
  escaping fixes for older ncurses; both are linked into the default build. Every
  `*.golden` file in the repository is byte-identical to v0.31.0, so for
  everything the golden tests cover, output did not move.

- **Dependency updates.** `golang.org/x` group (`net` v0.59.0, `text` v0.42.0,
  `tools` v0.50.0, `vuln` v1.8.0, plus `telemetry`) — the first group PR since
  the `go` directive fix shipped in 0.30.1, which confirms that diagnosis on the
  first Monday that could test it. Also `mongo-driver` v1.17.10,
  `go-runewidth` v0.0.30, `xo/terminfo` v1.2.0 (which drops `golang.org/x/exp`
  from the module graph) and the `osv-scanner-action` at v2.6.0.

- **The `dependencies`, `go` and `ci` labels now exist.** `dependabot.yml` had
  declared them for some time; GitHub drops labels that do not exist, silently,
  so every Dependabot PR had arrived unlabelled.

## [0.31.0] - 2026-09-19

### Added
- **`bentoo distfile fetch <category/package>` — the gated download, for the
  person installing the package.** The authenticated fetch has existed since
  0.28, but only inside the sweep: `prefetchAuthDistfile` runs minutes before
  `pkgdev manifest` and nothing else could reach it. A user whose `emerge`
  stopped on a distfile no mirror may carry was therefore left with the manual
  route the ebuild's `pkg_nofetch` prints — open the vendor page, log in, save
  the file under exactly the right name, move it into `DISTDIR` — every step of
  which can land a file the Manifest then rejects.

  The new command performs the download the overlay already records, writing it
  into the host's own `DISTDIR` (asked of `portageq distdir`, never assumed)
  under exactly the name `fetch_filename` resolves to. It runs the same request,
  the same guards and the same writer as the sweep, so what a user fetches is
  what the Manifest was computed against. `--version` selects the version an
  older ebuild needs; `--distdir` overrides the destination. An atom matching
  two records (release lines, slots) is refused and both keys are named, rather
  than resolved to whichever one the map yielded first.

  `internal/autoupdate` gained one exported door for it — `FetchAuthDistfile`,
  with `ErrPackageNotInRegistry`, `ErrAmbiguousPackageKey` and `ErrNoAuthFetch`
  — deliberately not the spec type: the `[meta]` sub-schema is this package's
  own business and has changed twice, while "fetch the distfile this record
  describes" is the stable question a command can be built on.

- **A gated download can now be a two-step one, and the config says which.**
  Four `[meta]` keys describe what a vendor endpoint actually does:
  `fetch_body = "json"` sends the form as a JSON object instead of urlencoded
  (exactly the literals `true`/`false` become booleans; everything else stays a
  string, so a postcode is not silently turned into a number);
  `fetch_response = "url"` says the reply NAMES the file rather than being it,
  and the download is fetched from the address it returns; `fetch_content_type`
  and `fetch_min_bytes` state what the finished file must be.

  This is not speculative. Measured against Blackmagic's DaVinci Resolve
  endpoint on 2026-09-19, the reply to the registration form is **492 bytes of
  `text/plain`** holding a CloudFront URL whose signature expires in three
  hours; the file itself is 3.81 GiB of `application/zip` behind it. The old
  guard rejected only `text/html`, and the old writer rejected only zero bytes
  — so that 492-byte sentence would have been written as
  `DaVinci_Resolve_21.1_Linux.zip` and digested into a green Manifest. The
  guard is now stated as "a distfile is not text", which covers `text/plain`,
  JSON and XML, and names `fetch_response` as the fix.

- **An endpoint whose id changes every release is resolved at fetch time.**
  `fetch_id_url` + `fetch_id_pattern` (a regex with one capture group) look the
  per-release download id up in whatever catalogue the vendor publishes and
  substitute it into `{id}` in `fetch_url`. Baking that id into the record is
  correct exactly until the next bump, after which it serves the PREVIOUS
  version's installer under the new version's file name.

  `{version}` is substituted into the pattern **quoted** (`regexp.QuoteMeta`),
  because an unquoted `21.1` is a regex that matches `2101` just as happily,
  and a catalogue holding both answers with whichever comes first.

- **A redirect on the form leg is refused, and the error no longer lies.**
  A `301`/`302`/`303` makes every HTTP client reissue the `POST` as a `GET` and
  DROP the body, so the vendor receives a request carrying none of the form and
  answers with its login page. The failure used to be reported as a rejected
  serial — wrong in both fact and remedy. It is now reported as what it is,
  naming the address to point `fetch_url` at. `307`/`308` preserve the body and
  are still followed.

- **`fetch_form_env` keeps a registration form's personal fields out of the
  overlay.** A vendor that gates a download behind a *registration* form asks
  for a name, an e-mail, a telephone and an address, and `packages.toml` is
  committed to a public repository. The new key states the field names and, for
  each, the NAME of the variable holding the value, resolved at fetch time
  through the same chain as the serial. It is that pair generalised, and every
  value is resolved before the first request goes out, so a missing variable is
  named together with the field it belongs to.

  Resolved values are scrubbed from error text — the serial always, the rest
  once they are four characters or longer, because substituting a
  two-character value would blank out fragments of the message somebody needs
  and identifies nobody on its own. Combining the key with
  `fetch_method = "get"` is refused: a GET puts every field in the query
  string, where the vendor's log, every proxy and the operator's own shell
  history keep it.

- **`fetch_timeout` for records that download something large.** The budget
  covers the whole download and was a fixed five minutes, sized for a distfile
  of tens of megabytes. A 3.81 GiB archive needs better than 13 MB/s sustained
  to finish inside that, so a record that knows what it fetches can say so. The
  default is unchanged.

### Fixed
- **A fetched distfile is no longer written 0600.** `os.CreateTemp` makes the
  temporary file private and the atomic rename preserved it, which was
  invisible while the only caller was the sweep writing into a private distdir
  it owns. `bentoo distfile fetch` writes into the HOST's `DISTDIR`, and the
  process that reads a distfile at merge time is not the one that fetched it:
  under `FEATURES="userfetch userpriv"` Portage reads as uid `portage`, which
  cannot open a 0600 file. The merge then failed on a file that was present,
  complete and digest-correct. The mode is set on the temp file before the
  rename, so the final name never exists with the wrong bits.

### Changed
- **A `[meta]` authenticated fetch no longer has to carry a serial — but may not
  carry half of one.** `fetch_serial_env` and `fetch_serial_field` were both
  unconditionally required, which kept the authenticated path out of reach of
  every download gated by a form alone rather than by a credential. They are now
  optional AS A PAIR: neither is fine, one without the other is an error naming
  the missing half.

  The pair rule is not pedantry. A half-declared serial describes a request that
  cannot be built — an env var with no field to carry it, or a field with no
  value to put in it — and accepting it would submit the form WITHOUT the
  credential it was configured to carry. A gated endpoint answers that with its
  login page: a 200 response with a body, which reaches the writer rather than
  the error path. The existing HTML guard catches the common shape of that, but
  the guard is the second line of defence; refusing the impossible config is the
  first.

## [0.30.3] - 2026-09-15

### Fixed
- **A `--revivable` scan no longer ends in soft errors over entries that are
  exactly where they should be.** Every run closed with "revive scan completed
  with soft errors" naming three packages: `app-office/libreoffice` and
  `app-office/libreoffice-l10n` on the `testing` line upstream has not published
  yet, and `media-plugins/gst-plugins-mpeg2dec` on the odd line whose plugin
  upstream dropped in 1.29. All three records were correct; the scan was reading
  them wrong.

  The lookup has four outcomes and the scan handled two. Besides "ebuild
  present" and `ErrNoEbuildFound`, a `":slot"` or `series` filter that matches
  nothing yields `ErrSlotNotFound`/`ErrSeriesNotFound` — the package DIRECTORY
  is there, holding the ebuilds of another release line. Those landed in the
  catch-all meant for an unreadable directory.

  Such an entry is now skipped in silence, like the other two, because it is
  neither an orphan nor a fault. Reviving it would be the mistake the existing
  "present" skip exists to prevent: mpeg2dec on the odd line would have
  qualified — upstream 1.29.2 is above the 1.28.x ::gentoo carries — and the
  revive would have seeded an ebuild for a plugin that does not exist at that
  version, next to a sibling line that is already newer.

  Nothing stops being reported. A filter that genuinely is wrong is caught on
  the enabled path, where `CheckAll` surfaces the same sentinel and the
  reconciliation records it as `NoEbuild`; the revive scan only ever sees
  disabled entries, where a filter matching nothing is the decision already
  written in `disabled_by`. The skip also lands before the upstream fetch, so a
  scan spends three fewer requests saying nothing.

## [0.30.2] - 2026-09-11

### Security

- **The two `charmbracelet/x/exp` modules were bumped by hand, inside the
  release quarantine, as a deliberate override.** `teatest` and `golden` sit in
  the Dependabot `ignore` list — upstream publishes no semver tag, so the
  resolver walks up to `charmbracelet/x` v0.1.0 and answers
  `dependency_file_not_resolvable`. That exclusion also drops them out of the
  7-day `cooldown`, which is a Dependabot feature and nothing else: the two
  dependencies nobody automates are the two whose quarantine nothing enforces.
  The `v0.0.0-20260906004030-3986e9119cf9` snapshot was published
  `2026-09-06T00:40:30Z` and would have aged out `2026-09-13T00:40:30Z`; it was
  taken on 2026-09-11, roughly 25 hours early. Read the publish time with
  `go list -m -json <module>@<version> | jq -r .Time` — the date inside the
  pseudo-version looks older than the quarantine clock actually is.

  Both are test-only — `teatest` drives the TUI test, `golden` arrives through
  it, neither is linked into the binary — so the exposure is the test host, not
  anything that ships. Neither golden file needed regenerating:
  `TestModelGoldenFrame.golden` and `TestFullscreenSectionsGoldenFrame.golden`
  are byte-identical after the bump, which makes the standing note to regenerate
  them a check rather than a certainty.

- **The chain carries no advisory.** govulncheck is clean under the pinned
  `go1.26.8` and not merely under the newer local toolchain — the two are
  different questions, because govulncheck judges the stdlib of whichever
  toolchain runs it. osv-scanner 2.5.1 (the exact pin the workflow uses),
  gitleaks over 431 commits and zizmor are clean too; `go mod verify` passes and
  `go mod tidy -diff` is empty. The other pending updates — `cascadia` v1.3.5
  (quarantine cleared 2026-09-11), the 2026-09-08 `x/` releases (2026-09-15),
  `go-runewidth` v0.0.30 and `mongo-driver` v1.17.10 (2026-09-17) — are left for
  Dependabot.

- **The Monday of 2026-09-14 does not test the `golang-x` fix shipped in
  0.30.1, and its silence should not be read as a relapse.** The candidate `x/`
  releases were published 2026-09-08, so the cooldown clears them only on
  09-15, and no intermediate version exists for Dependabot to fall back to —
  each pinned version is the immediately preceding release. The first run that
  can confirm or refute the diagnosis is 2026-09-21.

## [0.30.1] - 2026-09-09

### Security

- **The `golang-x` Dependabot group had been silently dropping every update
  since 2026-08-24, and nothing reported it.** Dependabot kept running and kept
  opening PRs for everything else, so only the *absent* group PR recorded the
  stall — `golang.org/x/time` sat 21 days behind.

  The cause is the `go` directive. Every `golang.org/x` release from late August
  on declares `go 1.26.0` in its own go.mod, and Go ranks a bare `1.26` BELOW
  `1.26.0`; `go get` states it outright: `upgraded go 1.26 => 1.26.0`. Taking
  any of them required raising this module's directive, which Dependabot will
  not do, so it discarded the whole group without a word.

  Two likelier-looking explanations were wrong, and the note in
  `dependabot.yml` records both so they are not re-investigated. Cooldown was
  not blocking the group: #128 shipped `x/mod` v0.40.0 while v0.41.0 was still
  in quarantine, so per-dependency cooldown picks an older eligible version
  rather than holding the group. Nor was it the missing-semver-tag failure the
  two `charmbracelet/x/exp` entries already document: the same #128 bumped
  `x/telemetry`, which has no semver tag at all, without trouble.

- **The fix carried a security regression, and the `toolchain` line is what
  stops it.** `actions/setup-go` reads a patch-qualified `go` directive as an
  *exact* version, so `go 1.26.0` alone would have frozen CI on the unpatched
  1.26.0 while 1.26.8 is current — a downgrade arriving disguised as a
  dependency update. setup-go prefers `toolchain` when present; `act -j audit`
  confirms the runner installs Go 1.26.8. The pin needs a manual bump on each Go
  patch release, which is the cost of a `go` directive that now carries a patch
  component.

- **A reachable stdlib CVE no longer passes CI.** Excluding stdlib findings was
  sound while the directive read `go 1.26` and setup-go fetched the newest patch
  every run — the runner healed itself and there was nothing here to do. Pinning
  the toolchain ends that, so the Go version became this repo's to bump and the
  finding became actionable. Both counts now come from one jq helper
  parameterised on which side of the split to keep, so the two paths cannot
  drift, and each message names the file to change.

- **Six dependency bumps, none carrying an advisory.** `x/time` v0.16.0 (the
  only direct one), `x/sys` v0.48.0, `x/mod` v0.41.0, `x/sync` v0.23.0,
  `mattn/go-runewidth` v0.0.29 and `go-json-experiment/json` to its 2026-08-20
  snapshot. They were the updates outside the 7-day release quarantine when the
  chain was audited; `andybalholm/cascadia` v1.3.5 and the 2026-09-08 `x/`
  releases were published inside that window and are left for Dependabot, which
  can now see them again.

### Fixed

- **`make audit` never ran govulncheck, and said so in a line nobody read.** It
  probed `command -v govulncheck`, printed "govulncheck not installed, skipping
  vulnerability check" and exited 0 — so `make audit`, and `make check` through
  it, reported success while scanning nothing. The tool comes from the `tool`
  directive in go.mod, which places it inside the module rather than on PATH; it
  is reachable only as `go tool govulncheck`, exactly how `ci.yml` has always
  invoked it. The local target was the one lying, and the install hint it
  offered was for a tool the module already carries. The fallback is gone on
  purpose: the tool is a module dependency, so its absence is a broken checkout
  and should fail loudly rather than degrade to a green.

## [0.30.0] - 2026-09-07

### Changed
- **The review budget is a measured number now, not an inherited one.**
  `DefaultReviewTimeout` goes from 120 to 300 seconds. The old value was chosen
  by another story for a tool-free extraction path and never checked against the
  agentic round trip it ended up bounding.

  Measured 2026-09-06/07 over the maintainer's own overlay — 268 packages, 126
  invocations, budget lifted to 1800s so nothing was cut short. 122 succeeded, 4
  exited non-zero, none reached the deadline. The successful reviews spread from
  7.4s to 124.6s, with a median of 14.3s and a p99 of 112.0s.

  So 120s was wrong by 4.6 seconds on one review out of 122 — which is the worst
  kind of wrong, because a ceiling sitting just inside the distribution looks
  adequate and still takes the tail off. 300s is 2.4x the largest measured
  success and 2.7x the p99, and it stays finite on purpose: a review that
  genuinely hangs must still end.

  The constant's documentation carries the readings, the date and the machine,
  because nothing automated re-derives them. It also records what the
  measurement does NOT establish — the original five-of-five failure this story
  was opened for is not reproduced by it, and the duration log does not record
  which package an invocation belonged to, so the expensive population cannot be
  separated back out. Raising the number further to cover an unmeasured case
  would reintroduce exactly the unmeasured constant this story removed.

- **Comments no longer state a budget the configuration can change.** Four
  in-tree comments restated `120s` away from the constant that defines it, and
  one of them had already become false when the budget started reaching the
  client. Each now names where the budget comes from instead of what it is.

### Added
- **The review budget is configurable, and for the first time it is connected.**
  `autoupdate.review.timeout` (an integer of seconds, default 300) sets the
  deadline one `overlay compare` divergence review runs under, documented in
  `config.example.yaml` alongside `cache_ttl` and `http_timeout`.

  The option it feeds was never the missing piece: `WithClaudeCodeTimeout` and
  the client's `timeout` field both already existed. What was missing is that
  `newClaudeAsker` never passed the option — so every review this tool has ever
  run used the package default, and no configuration could have changed it. The
  budget now travels through the one construction seam the divergence and
  realignment reviews already share.

  The key is nested under `autoupdate` rather than given a top-level block of
  its own, which is semantically off by one command and deliberate: the strict
  config probe mirrors top-level keys by hand and nothing tests that mirror, so
  a new top-level key would print `field <key> not found in type
  config.probeConfig` to stderr on every command. Nesting inherits the mirror.
  An absent block, a half-written block and a nil pointer all resolve to the
  documented default.

- **Every `claude` invocation records what it cost.** `run` emits one line per
  invocation carrying the outcome and the wall-clock time it took, for every
  outcome including success. A budget cannot be set from the failures alone —
  those are precisely the runs that hit the ceiling — so the successful
  durations are recorded too, and the cost of a review is now recoverable from
  a run's own output instead of by instrumenting again.

### Fixed
- **The auxiliary variable substitution can no longer bleed past the assignment
  it means.** `regexp.QuoteMeta` pins the variable's name but not its position,
  so the unanchored pattern also matched a longer name merely ending in the
  target one, and a commented-out assignment — and `ReplaceAllString` rewrote
  every match. A single bump could corrupt three lines it had no business
  touching.

  This is the `aux_var` half of the lesson `substituteCommitHash` already
  learned when an unanchored match clobbered a vendored revision; the two were
  asymmetric, and only one of them was anchored. Latent rather than live: all
  six ebuilds declaring `aux_var` put the assignment at column zero with no
  colliding name in the file, so this is a guard against the next ebuild.

- **An auxiliary variable that already holds the right value no longer fails the
  bump.** `substituteAuxVar` decided on difference: when the rewritten ebuild
  came out equal to the original it reported the variable as absent. But
  "nothing changed" has two causes and only one of them is a fault, so a bump
  died naming a variable that was sitting right there in the file, leaving the
  staged tree behind.

  Reachable whenever an upstream keeps the auxiliary value across two releases.
  `net-misc/nxplayer` shipped 10.0.59 and 10.0.60 both as build `_1`, so there
  was nothing to substitute and the apply aborted. Every package declaring
  `aux_var` was exposed to the same stall. Presence decides now, which is what
  the sibling `substituteCommitHash` has done since it met the same conflation
  on a pure base correction: absence is the error, a no-op is a successful
  nothing.

- **Neither review wrapper blames the ebuilds any more.** `overlay compare`'s
  divergence and realignment reviewers both wrapped every failure as `the claude
  CLI could not read the two ebuilds`. Neither seam opens a file: both ebuilds
  arrive as bytes, read upstream, and all the wrapper does is put them on the
  CLI's stdin — so the sentence was false for every failure it could ever
  report, on both paths. Each now names its own review and lets the classified
  cause through unaltered. The one place that still says the ebuilds could not
  be read is the one where that is what happened.

- **A `claude` invocation killed by its own deadline now says so.** Every failed
  review of `overlay compare --realign` was reported as `the claude CLI could
  not read the two ebuilds: ... claude CLI failed: signal: killed`. Reading the
  ebuilds was never what failed. `run` in `claude_code.go` never read its own
  context, so a SIGKILL from this program's 120-second budget arrived
  indistinguishable from any other spawn failure, and the message sent whoever
  debugged it to the filesystem.

  The context is now read before anything frames the failure, and the three
  outcomes get three sentences: an elapsed deadline names the budget actually in
  force, a process that never started says that, and a process that ran and
  exited keeps the exit-code framing it always had. A run ended by a parent
  rather than by this client's own budget claims no number — quoting one would
  assert that a value ran out when it had not.

- **One precedence, consulted once, instead of two that could drift.** The
  ordering that decides which of the three failures happened — a context error
  outranks any exit-code framing — was written only inside `formatFixerError` in
  `manifest_fixer.go`. It is now a classifier that both the fixers and the
  review path consume. What is shared is the order and nothing else: the four
  existing fixer messages are byte for byte what they were, because a review
  told "claude fixer aborted" would be told about an operation it never ran.

[Unreleased]: https://github.com/obentoo/bentoolkit/compare/v0.34.0...HEAD
[0.34.0]: https://github.com/obentoo/bentoolkit/compare/v0.33.3...v0.34.0
[0.33.3]: https://github.com/obentoo/bentoolkit/compare/v0.33.2...v0.33.3
[0.33.2]: https://github.com/obentoo/bentoolkit/compare/v0.33.1...v0.33.2
[0.33.1]: https://github.com/obentoo/bentoolkit/compare/v0.33.0...v0.33.1
[0.33.0]: https://github.com/obentoo/bentoolkit/compare/v0.32.0...v0.33.0
[0.32.0]: https://github.com/obentoo/bentoolkit/compare/v0.31.1...v0.32.0
[0.31.1]: https://github.com/obentoo/bentoolkit/compare/v0.31.0...v0.31.1
[0.31.0]: https://github.com/obentoo/bentoolkit/compare/v0.30.3...v0.31.0
[0.30.3]: https://github.com/obentoo/bentoolkit/compare/v0.30.2...v0.30.3
[0.30.2]: https://github.com/obentoo/bentoolkit/compare/v0.30.1...v0.30.2
[0.30.1]: https://github.com/obentoo/bentoolkit/compare/v0.30.0...v0.30.1
[0.30.0]: https://github.com/obentoo/bentoolkit/compare/v0.29.1...v0.30.0
