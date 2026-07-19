# Bentoolkit

CLI tools for Bentoo Linux distribution maintainers and developers.

## Modules

- **overlay**: Bentoo overlay commit management, version comparison, and automated updates
- **snapshot**: declarative btrfs snapshot management orchestrating `btrbk` (snapshot + ssh replication), `snapper` (timeline + system rollback), and `systemd` timers

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
make build           # Build the binary
make install         # Install to /usr/local/bin
make install-config  # Copy config.example.yaml to ~/.config/bentoo/ (no overwrite)
make test            # Run tests
make coverage        # Run tests with coverage report
make audit           # Run security audit (go mod verify + govulncheck)
make clean           # Remove build artifacts
make build-all       # Cross-compile for linux amd64 and arm64
make check           # Run lint, test, and audit
make help            # Show all available targets
```

## Configuration

Bentoo reads `~/.config/bentoo/config.yaml` (or `$XDG_CONFIG_HOME/bentoo/config.yaml`).
The repository ships a fully commented [`config.example.yaml`](config.example.yaml) —
copy it with `make install-config` (which never overwrites an existing config), or
create the file by hand:

```yaml
overlay:
  path: /var/db/repos/bentoo

git:
  user: your_username
  email: your_email@example.com

# A GitHub token (optional — it raises the API rate limits) is NOT stored here.
# Export GITHUB_TOKEN or add it to the secrets file; see "Secrets" below.

# Optional: custom repositories for compare command
repositories:
  my-overlay:
    provider: github  # github, gitlab, git, or local
    url: myuser/my-overlay
    branch: main

# Optional: autoupdate settings — the LLM provider lives under autoupdate.llm
autoupdate:
  llm:
    provider: claude        # claude, claude-code, openai, or ollama
    api_key_env: ANTHROPIC_API_KEY
    model: claude-3-haiku-20240307
    # claude-code only (drives the local `claude` CLI):
    bare: auto              # auto (default) | true | false
    max_budget_usd: 0.50    # optional per-call spend cap
```

### Configuration Options

| Option | Description | Required |
|--------|-------------|----------|
| `overlay.path` | Path to your local Bentoo overlay repository | Yes |
| `git.user` | Git username for commits (fallback if not in ~/.gitconfig) | No |
| `git.email` | Git email for commits (fallback if not in ~/.gitconfig) | No |
| `repositories.<name>` | Custom repository definitions for the compare command | No |
| `llm.provider` | LLM provider for autoupdate: `claude`, `claude-code`, `openai`, or `ollama` | No |
| `llm.api_key_env` | Name of the variable holding the LLM API key, resolved via env or the secrets file | No |
| `llm.model` | Model name (e.g. `claude-3-haiku-20240307`, `gpt-4o-mini`; `claude-code` defaults to the `sonnet` alias) | No |
| `llm.bare` | `claude-code` only: `auto` (default — `--bare`+API key when `api_key_env` resolves to a non-empty key via env or the secrets file, else the CLI login), `true` (force `--bare`+key), or `false` (force login/subscription) | No |
| `llm.max_budget_usd` | `claude-code` only: optional per-call spend cap passed to `claude --max-budget-usd` (unset = no cap) | No |

The tool will automatically use your `~/.gitconfig` settings for user name and email if available.

### Secrets

bentoo never stores secrets in `config.yaml` or `snapshot.toml`. Every secret it
consumes is resolved at runtime through a single chain:

1. an **environment variable**, then
2. the **user secrets file** `$XDG_CONFIG_HOME/bentoo/secrets` (else
   `~/.config/bentoo/secrets`), then
3. the **system secrets file** `/etc/bentoo/secrets`.

The secrets file is `.env` style — `NAME=value`, `#` comments, an optional
`export ` prefix — one entry per line. Keep it private with
`chmod 600 ~/.config/bentoo/secrets` (bentoo warns once if the file is group- or
world-readable).

```bash
# ~/.config/bentoo/secrets
GITHUB_TOKEN=ghp_xxxxxxxxxxxx
BENTOO_REPO_MY_OVERLAY_TOKEN=ghp_xxxxxxxxxxxx
ANTHROPIC_API_KEY=sk-ant-xxxxxxxx
BENTOO_NTFY_TOKEN=tk_xxxxxxxxxxxx
BENTOO_SMTP_PASSWORD=your-smtp-password
```

| Secret | Name(s) looked up |
|--------|-------------------|
| GitHub API token | `GITHUB_TOKEN`, then `GH_TOKEN` |
| Per-repository token | `BENTOO_REPO_<NAME>_TOKEN` — `<NAME>` is the repository's config key uppercased, every character outside `[A-Z0-9]` replaced by `_` (e.g. `my-overlay` → `BENTOO_REPO_MY_OVERLAY_TOKEN`) |
| LLM API key | the value of `llm.api_key_env` (e.g. `ANTHROPIC_API_KEY`), itself resolved through this chain |
| Authenticated-fetch serial | the value of `fetch_serial_env` (e.g. `FILEZILLA_PRO_KEY`) |
| ntfy auth token | `BENTOO_NTFY_TOKEN` |
| SMTP password | `BENTOO_SMTP_PASSWORD` — enables PLAIN auth together with `[notify.email.smtp] user`; unresolvable means the mail is sent unauthenticated |

For `overlay compare` the GitHub token precedence is **`--token` flag >
per-repo `BENTOO_REPO_<NAME>_TOKEN` > global `GITHUB_TOKEN`/`GH_TOKEN`**.

> **One deliberate exception:** `${VAR}` expansion in `packages.toml` request
> `headers` reads the **process environment only** (never the secrets file) —
> see [Headers and environment variables](#headers-and-environment-variables).

## Usage

### Overlay Commands

#### Initialize Configuration

Initialize the bentoo configuration:

```bash
bentoo overlay init
```

#### Check Status

View pending changes in your overlay, grouped by category and package:

```bash
bentoo overlay status
```

Example output:
```
www-client/firefox:
  [M] firefox-128.0.ebuild
  [A] firefox-129.0.ebuild

app-misc/hello:
  [A] hello-1.0.ebuild
  [A] Manifest
```

Status codes:
- `[A]` - Added (new file)
- `[M]` - Modified
- `[D]` - Deleted
- `[R]` - Renamed
- `[?]` - Untracked

#### Stage Changes

Add files to the staging area:

```bash
# Add current directory (default)
bentoo overlay add

# Add specific files
bentoo overlay add app-misc/hello/hello-1.0.ebuild

# Add multiple paths
bentoo overlay add app-misc/hello/ www-client/firefox/
```

#### Commit Changes

Commit staged changes with automatic message generation:

```bash
# Interactive commit with auto-generated message
bentoo overlay commit

# Provide custom message (skips auto-generation)
bentoo overlay commit -m "Custom commit message"
```

The tool automatically generates commit messages based on changes:

| Change Type | Message Format |
|-------------|----------------|
| New package | `add(category/package-version)` |
| Remove package | `del(category/package-version)` |
| Modify package | `mod(category/package-version)` |
| Version bump | `up(category/package-oldver -> newver)` |
| Version downgrade | `down(category/package-newver -> oldver)` |

Multiple changes are grouped:
```
add(www-client/{firefox-129.0, chrome-120.0}), up(app-misc/hello-1.0 -> 2.0)
```

Package variants (like `-bin` packages) are grouped with nested braces:
```
up(app-misc/{hello{,-bin}-1.0 -> 2.0})
```

#### Push Changes

Push committed changes to the remote repository:

```bash
bentoo overlay push
```

#### Rename Ebuilds

Bulk rename ebuilds from an old version to a new version across a package:

```bash
bentoo overlay rename <category>:<package-pattern>:<old-version> => <new-version>
```

Example:
```bash
bentoo overlay rename app-misc:hello:1.0 => 2.0
```

#### Regenerate Manifests

Regenerate `Manifest` files for one or more packages. By default the
existing `Manifest` is moved aside before `pkgdev` runs (clean regen),
and restored automatically if `pkgdev` fails. Runs as the current user —
no `sudo` required.

```bash
# Whole overlay
bentoo overlay manifest

# All packages in a category
bentoo overlay manifest app-editors

# Single package
bentoo overlay manifest app-editors/zed

# Preview only
bentoo overlay manifest --dry-run app-editors

# Skip the clean step (let pkgdev reconcile in place)
bentoo overlay manifest --keep app-editors/zed
```

Requires `dev-util/pkgdev`.

#### Show Diff

Show the diff of uncommitted or staged changes:

```bash
bentoo overlay diff

# Show diff for a specific path
bentoo overlay diff app-misc/hello/
```

#### Show Commit Log

Display the overlay's commit history:

```bash
bentoo overlay log
```

#### Sync Overlay

Sync the overlay with its upstream remote:

```bash
bentoo overlay sync
```

#### Compare with Upstream

Compare your overlay packages with upstream repositories to find outdated packages:

```bash
# Compare with official Gentoo (default)
bentoo overlay compare
bentoo overlay compare gentoo

# Compare with GURU (Gentoo User Repository)
bentoo overlay compare guru

# Use git clone instead of API (avoids rate limits)
bentoo overlay compare --clone
bentoo overlay compare guru --clone
```

This command will:
- Scan your local Bentoo overlay for all packages
- Query the specified upstream repository (via API or git clone)
- Compare versions using Gentoo's version comparison rules
- **Automatically ignore live ebuilds** (versions with `9999`)
- Display a table of outdated packages

**Built-in Repositories:**

| Name | Description | Provider |
|------|-------------|----------|
| `gentoo` | Official Gentoo repository (default) | GitHub API |
| `guru` | Gentoo User Repository | GitHub API |

Example output:
```
Scanning Bentoo overlay at /var/db/repos/bentoo...
Found 142 packages in Bentoo overlay
Comparing with gentoo using GitHub API (gentoo/gentoo)...

Outdated Packages (Bentoo < Gentoo):
┌─────────────────────────┬──────────────┬────────────────┬────────────────┐
│ Package                 │ Category     │ Bentoo Version │ Gentoo Version │
├─────────────────────────┼──────────────┼────────────────┼────────────────┤
│ vscode                  │ app-editors  │ 1.107.1        │ 1.108.0        │
│ firefox                 │ www-client   │ 128.0          │ 129.0          │
└─────────────────────────┴──────────────┴────────────────┴────────────────┘

Total: 2 outdated packages
```

**Note:** Live ebuilds (versions containing `9999`) are automatically ignored, as they represent bleeding-edge/git versions and not stable releases.

**Options:**

| Flag | Description | Default |
|------|-------------|---------|
| `--clone` | Use git clone instead of API | false |
| `--cache-dir` | Directory to cache data | `~/.cache/bentoo/compare` |
| `--no-cache` | Disable caching | false |
| `--timeout` | HTTP request timeout (seconds) | 30 |
| `--token` | Auth token for API provider | - |

**API vs Git Clone:**

| Mode | Pros | Cons |
|------|------|------|
| API (default) | Fast, no disk space | Rate limited (60/hour or 5000/hour with token) |
| Clone (`--clone`) | No rate limits, always fresh | Slower first run, uses disk space |

**Rate Limits (API mode):**
- Without token: 60 requests/hour
- With token: 5,000 requests/hour

**Using a GitHub Token:**

You can provide a token three ways, in **priority order** (`--token` >
per-repo > global):

1. **Command line flag** (highest priority):
   ```bash
   bentoo overlay compare --token ghp_xxxxxxxxxxxx
   ```

2. **Per-repository secret** — `BENTOO_REPO_<NAME>_TOKEN` for a custom
   repository (`<NAME>` = the repo's config key uppercased, every character
   outside `[A-Z0-9]` replaced by `_`). See [Secrets](#secrets).

3. **Global token** — the `GITHUB_TOKEN` (or `GH_TOKEN`) environment variable,
   or a matching line in the secrets file:
   ```bash
   export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
   bentoo overlay compare
   ```

`config.yaml` no longer holds a token — the value is resolved once through the
secrets chain (see [Secrets](#secrets)).

To create a token: Go to GitHub Settings → Developer settings → Personal access tokens and generate a new token. No scopes are required (public repository access only).

**Custom Repositories:**

You can define custom repositories in your configuration file:

```yaml
# ~/.config/bentoo/config.yaml
repositories:
  # GitLab repository
  gentoo-gitlab:
    provider: gitlab
    url: https://gitlab.gentoo.org/repo/gentoo
    branch: master

  # Custom GitHub overlay. For a private repo, put the token in the secrets
  # file as BENTOO_REPO_MY_OVERLAY_TOKEN — it is never stored in config.
  my-overlay:
    provider: github
    url: myuser/my-overlay

  # Generic git repository
  local-mirror:
    provider: git
    url: https://git.example.com/overlay.git
    branch: main

  # Local on-disk tree (read in place, no clone) — required by
  # `overlay autoupdate --revive`, which seeds a base ebuild off ::gentoo
  gentoo:
    provider: local
    path: /var/db/repos/gentoo
```

Then use them:
```bash
bentoo overlay compare my-overlay
bentoo overlay compare gentoo-gitlab --clone
```

#### Autoupdate

Check for new upstream versions and apply them automatically:

```bash
# Check all packages configured in packages.toml
bentoo overlay autoupdate

# Check a specific package
bentoo overlay autoupdate app-misc/hello
```

The autoupdate system reads version schemas from `packages.toml` in your overlay root, fetches upstream sources, and updates ebuilds when a new version is found.

#### Analyze Package

Use an LLM to analyze a package's upstream source and generate an autoupdate schema:

```bash
# Analyze a package and suggest a schema
bentoo overlay analyze app-misc/hello

# Provide a hint to guide the analysis
bentoo overlay analyze app-misc/hello --hint "version is in the releases page JSON"
```

The analysis output can be pasted into `packages.toml` as a starting schema for `autoupdate`.

### Autoupdate System

The autoupdate system automates version tracking by fetching upstream sources and comparing them against the overlay's current versions.

#### Schema Configuration (`packages.toml`)

Place a `packages.toml` file in the root of your overlay. Each entry defines how to extract the version for a package:

```toml
[app-misc/hello]
url = "https://api.github.com/repos/owner/hello/releases/latest"
parser = "json"
path = "tag_name"

[www-client/firefox]
url = "https://product-details.mozilla.org/1.0/firefox_versions.json"
parser = "json"
path = "LATEST_FIREFOX_VERSION"

[dev-libs/mylib]
url = "https://example.com/releases"
parser = "regex"
pattern = "mylib-([0-9.]+)\\.tar\\.gz"

[app-text/myapp]
url = "https://example.com/downloads"
parser = "html"
selector = "a.release-tag"
```

**Supported parsers:**

| Parser | Required fields | Description |
|--------|----------------|-------------|
| `json` | `path` | JSON path to the version field (e.g. `tag_name`, `data.version`) |
| `regex` | `pattern` | Regex with one capture group matching the version |
| `html` | `selector` or `xpath` | CSS selector or XPath to the element containing the version |

> **Regex parser caveat:** `regex` returns the **first** match in the response
> body, not the highest version. On a page that lists several releases (e.g. a
> directory listing), an unanchored pattern can capture an *older* version and
> cause the check to silently report "up to date". Prefer a JSON API endpoint
> that exposes the latest version directly, or anchor the pattern tightly to the
> single element that always holds the newest release.

> **Non-comparable versions:** before comparing, the extracted value is
> normalized (whitespace trimmed, a leading `v`/`version-`/etc. prefix
> stripped). If the result is still not a well-formed Gentoo-style version
> (e.g. an upstream tag like `INKSCAPE_1_4_4`, or `latest`), the check reports a
> **warning** and skips the package instead of treating it as "up to date" —
> this prevents a bad parser config from silently masking a real update. Fix the
> schema so it extracts a bare version string (e.g. add a `regex` that captures
> the digits, or point `path` at a cleaner field).

**Optional fields:**

| Field | Description |
|-------|-------------|
| `fallback_url` | Secondary URL to try if the primary fails |
| `fallback_parser` | Parser type for the fallback URL |
| `fallback_pattern` | Pattern/path for the fallback parser |
| `llm_prompt` | Instruction used to extract the version via an LLM. Consumed by `bentoo overlay analyze`, and by `bentoo overlay autoupdate --check` when an `llm.provider` is configured (the LLM is tried after the primary/fallback parsers). When no provider is configured, `--check` logs a Warn and skips LLM extraction. |
| `headers` | Custom HTTP headers. `${VAR}` is expanded only for allow-listed auth headers and allow-listed variables — see [Headers and environment variables](#headers-and-environment-variables). Example: `Authorization = "Bearer ${BENTOO_MY_TOKEN}"` |
| `timeout` | Per-operation budget (seconds) for **this** package — the total time spent fetching its version across all retry attempts. Use it for a reliably slow host so it gets extra retry headroom without slowing the whole batch. Absent/`0` uses the global budget derived from `autoupdate.http_timeout`. See [Timeouts](#timeouts). |
| `binary` | Set to `true` for binary packages (manifest-only testing) |

#### Supported LLM Providers

The `analyze` command uses an LLM for schema generation. `bentoo overlay autoupdate --check` also uses the LLM to extract a version when an `llm.provider` is configured and a package sets `llm_prompt` (tried after the primary and fallback parsers); with no provider configured it logs a Warn and skips LLM extraction.

| Provider | Config value | API key env var | Notes |
|----------|-------------|-----------------|-------|
| Anthropic Claude (HTTP API) | `claude` | `ANTHROPIC_API_KEY` | Default model: `claude-3-haiku-20240307` |
| Claude Code (local CLI) | `claude-code` | `ANTHROPIC_API_KEY` (bare mode) | Drives the local `claude` CLI headlessly. Default model: `sonnet` alias. Hybrid auth via `llm.bare`; honors `llm.max_budget_usd`. Degrades to a Warn + fallback when the CLI is missing or unauthenticated. |
| OpenAI | `openai` | `OPENAI_API_KEY` | Default model: `gpt-4o-mini` |
| Ollama (local) | `ollama` | *(none)* | Default model: `llama3`, runs locally |

Configure in `~/.config/bentoo/config.yaml`:

```yaml
llm:
  provider: claude
  api_key_env: ANTHROPIC_API_KEY
  model: claude-3-haiku-20240307
```

The Claude endpoint can be overridden via `CLAUDE_API_ENDPOINT` environment variable (useful for testing or proxies).

##### `claude-code` provider (local CLI)

The `claude-code` provider drives your locally-installed `claude` CLI (Claude Code) headlessly instead of calling the HTTP API, reusing your existing Claude Code login or an API key:

```yaml
llm:
  provider: claude-code
  api_key_env: ANTHROPIC_API_KEY   # used in bare mode
  model: sonnet                    # optional; defaults to the `sonnet` alias (latest Sonnet)
  bare: auto                       # auto | true | false
  max_budget_usd: 0.50             # optional per-call spend cap
```

Authentication is hybrid, selected by `llm.bare`:

- `auto` (default): resolve `api_key_env` **once** through the secrets chain (env → user file → system file); if that yields a non-empty key, run `claude --bare` with it, otherwise use the CLI's logged-in session (subscription). The single resolved value drives both the bare-mode choice and the credential handed to the child `claude`.
- `true` / `false`: force bare (`--bare` + key) or login/subscription mode respectively, regardless of key presence.

> **Cost note.** `sonnet` in login/subscription mode is billed per call (a large page context of ~74k tokens is roughly $0.09+/call). The cheap path is `--bare` + an API key. Set a conservative `max_budget_usd` when running `--check` across many packages. If the `claude` CLI is missing or not authenticated, both `analyze` and `--check` log a Warn and fall back (heuristic schema / skip extraction) — they never fail because of the LLM.

#### Example Autoupdate Workflow

```bash
# 1. Analyze a new package to generate its schema
bentoo overlay analyze www-client/myapp
# → Outputs suggested packages.toml entry

# 2. Add the schema to packages.toml
# ... edit packages.toml ...

# 3. Run autoupdate to check for new versions
bentoo overlay autoupdate www-client/myapp
# → Fetches upstream, applies version bump if found

# 4. Review and commit
bentoo overlay status
bentoo overlay add www-client/myapp/
bentoo overlay commit
# → "up(www-client/myapp-1.0 -> 1.1)"
```

### Exit codes

`bentoo overlay autoupdate` reports its outcome through the process exit code so
it can be wired into scripts and CI:

| Code | Meaning |
|------|---------|
| `0` | Every package was processed successfully. |
| `1` | Partial failure — at least one package failed **and** at least one succeeded. |
| `2` | Total failure — no package was processed (or the configuration is invalid). |

A non-zero exit code is therefore distinguishable: `1` means "some work
landed", `2` means "nothing landed". The per-package errors that caused a `1`
or `2` are also printed so the failing packages can be retried individually.

### Live output

`bentoo overlay autoupdate --apply` (and `--apply all`) and `bentoo overlay
manifest` render a live terminal UI while long subprocesses run: a per-package
status, an overall progress indicator, and a bounded tail of the running
`pkgdev`/`wget` fetch — so you can see what is downloading instead of a frozen
line. When the work finishes, each package leaves a `✓`/`✗` history line in the
scrollback.

The live UI activates only on an interactive terminal. It falls back
automatically to plain, ANSI-free streaming output (still showing the fetch tail
on stderr) when any of the following holds:

- stdout is not a TTY (e.g. piped into a file or `tee`, or run under CI);
- the `--no-tui` flag is passed;
- the `NO_COLOR` environment variable is set;
- the `BENTOO_NO_TUI` environment variable is set.

Pressing `Ctrl-C` during a run cancels the in-flight operation (terminating the
child process) and restores the terminal; a half-applied ebuild is rolled back. A
compile step that needs `sudo`/`doas` releases the terminal so the password prompt
is shown and answered on the real terminal.

### Concurrency

`overlay autoupdate` and `overlay compare` process packages in parallel. The
`--concurrency=N` flag bounds the number of packages worked on at once:

```bash
bentoo overlay autoupdate --concurrency=4
bentoo overlay compare --concurrency=20
```

| Property | Value |
|----------|-------|
| Default | `10` |
| Valid range | `[1, 100]` (inclusive) |

A value outside the valid range **fails fast** with a clear error *before any
package work begins* — so a typo in the flag never starts a partial run.

### Timeouts

Each upstream fetch is bounded by a **per-request** timeout (the cap on a single
HTTP attempt) and an automatically derived **per-operation** budget large enough
for the retry attempts to run within it. Sizing the budget above the per-request
timeout is what lets the built-in retries recover from an occasionally slow or
hung host — otherwise the first slow request would consume the whole budget and
fail with `context deadline exceeded` before any retry.

Resolution order for the per-request timeout (in seconds):

```bash
# 1. --timeout flag (highest priority), e.g. give every request up to 60s:
bentoo overlay autoupdate --check --timeout 60
```

```yaml
# 2. config (~/.config/bentoo/config.yaml): applies to every --check run
autoupdate:
  http_timeout: 45        # default: 30
```

| Setting | Scope | Default |
|---------|-------|---------|
| `--timeout N` | This `--check` run | `0` (use config) |
| `autoupdate.http_timeout` | Every run | `30` |
| `timeout = N` (in `packages.toml`) | One package's per-operation budget | derived from the per-request timeout |

A per-package `timeout` (see the schema fields below) overrides the per-operation
budget for a single package — useful for a host that is reliably slow (e.g.
`salsa.debian.org`, `sources.debian.org`) so it gets extra retry headroom without
slowing the whole batch. If a *single response* itself needs longer than the
per-request cap, raise `autoupdate.http_timeout` (or pass `--timeout`) instead.
On a timeout the error names the host and the per-request cap so it is clear
which endpoint was slow and which knob to raise.

### Headers and environment variables

A `packages.toml` entry can declare custom HTTP `headers`. A `${VAR}` reference
in a header *value* is expanded from the process environment **only** when both
of the following hold (this is an allow-list — there is intentionally no escape
hatch):

1. The header **name** (matched case-insensitively) is one of:
   `Authorization`, `X-Api-Key`, `X-Auth-Token`, `Private-Token`.
2. The environment **variable** is either prefixed with `BENTOO_` **or** is one
   of: `GITHUB_TOKEN`, `GITLAB_TOKEN`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`.

This prevents a malicious or mistaken `packages.toml` from exfiltrating
arbitrary process secrets (e.g. a cloud credential) through a non-auth header
or an arbitrary variable name. A `${VAR}` that does not satisfy both rules is
**passed through literally** (the header value keeps the raw `${VAR}` text) and
a `Warn` is logged.

> **Env-only by design.** This `${VAR}` expansion reads the **process
> environment only** (`os.Getenv`); it deliberately does **not** consult the
> bentoo secrets file. It is the single intentional exception to the unified
> secrets chain — export the variable in the environment to use it here.

```toml
[app-misc/hello]
url = "https://api.example.com/releases/latest"
parser = "json"
path = "tag_name"

[app-misc/hello.headers]
# Expanded: allow-listed header + BENTOO_-prefixed variable.
Authorization = "Bearer ${BENTOO_MY_TOKEN}"
# Expanded: allow-listed header + allow-listed variable.
X-Api-Key = "${GITHUB_TOKEN}"
```

**Migration (BREAKING):** before this release any `${VAR}` in any header was
expanded. A previously-working header such as `Authorization = "Bearer
${MY_TOKEN}"` now has a non-allow-listed variable and will be passed through
literally with a `Warn`. Rename the variable to add the `BENTOO_` prefix:

```diff
-Authorization = "Bearer ${MY_TOKEN}"
+Authorization = "Bearer ${BENTOO_MY_TOKEN}"
```

and export it under the new name (`export BENTOO_MY_TOKEN=...`).

### HTTP/2

The shared HTTP transport negotiates **HTTP/2 by default**. If an HTTP/2-aware
proxy or middlebox in your environment misbehaves, opt out by setting:

```bash
export BENTOO_DISABLE_HTTP2=1
```

With `BENTOO_DISABLE_HTTP2=1` the transport falls back to HTTP/1.1 only.

### Filesystem assumptions

Cache files and the apply-log are written with mode `0600` (owner read/write
only), since they may contain tokens echoed from request headers or upstream
responses.

On filesystems that cannot represent Unix permission bits — notably FAT32 and
exFAT — the `chmod` to `0600` fails. In that case the tool emits a `Warn` and
**continues**; the file is still written, just without the restrictive mode.
Keep caches on a permission-capable filesystem when storing sensitive data.

### Typical Overlay Workflow

```bash
# Navigate to overlay
cd /var/db/repos/bentoo

# Create new ebuild
cp app-misc/hello/hello-1.0.ebuild app-misc/hello/hello-2.0.ebuild
# Edit the ebuild...

# Update manifest
ebuild app-misc/hello/hello-2.0.ebuild manifest

# Check status
bentoo overlay status

# Stage changes
bentoo overlay add app-misc/hello/

# Commit with auto-generated message
bentoo overlay commit
# Shows: "up(app-misc/hello-1.0 -> 2.0)"
# Press 'y' to confirm, 'e' to edit, 'c' to cancel

# Push to remote
bentoo overlay push
```

## Development

### Running Tests

```bash
# Run all tests
make test

# Run tests with coverage
make coverage

# Run specific package tests
go test -v ./internal/overlay/...
go test -v ./internal/autoupdate/...
```

### Security Audit

```bash
# Run module verification and vulnerability check
make audit

# Install govulncheck if not available
go install golang.org/x/vuln/cmd/govulncheck@latest
```

### Project Structure

```
bentoolkit/
├── cmd/bentoo/                 # CLI commands
│   ├── main.go                 # Entry point
│   ├── overlay_add.go          # overlay add command
│   ├── overlay_analyze.go      # overlay analyze command (LLM schema generation)
│   ├── overlay_autoupdate.go   # overlay autoupdate command
│   ├── overlay_commit.go       # overlay commit command
│   ├── overlay_compare.go      # overlay compare command
│   ├── overlay_diff.go         # overlay diff command
│   ├── overlay_init.go         # overlay init command
│   ├── overlay_log.go          # overlay log command
│   ├── overlay_manifest.go     # overlay manifest command
│   ├── overlay_push.go         # overlay push command
│   ├── overlay_rename.go       # overlay rename command
│   ├── overlay_status.go       # overlay status command
│   └── overlay_sync.go         # overlay sync command
├── internal/
│   ├── autoupdate/             # Autoupdate subsystem
│   │   ├── llm.go              # LLM provider interface and Claude client
│   │   ├── openai.go           # OpenAI client
│   │   ├── ollama.go           # Ollama (local) client
│   │   ├── httpclient.go       # HTTP client with retry and circuit breaker
│   │   ├── rate_limiter.go     # Rate limiter (LLM + HTTP, LRU eviction)
│   │   ├── config.go           # packages.toml schema configuration
│   │   ├── checker.go          # Version checking orchestration
│   │   ├── analyzer.go         # Schema analysis
│   │   ├── applier.go          # Version update applicator
│   │   ├── parser.go           # Parser implementations (json/regex/html)
│   │   └── cache.go            # Analysis result caching
│   └── common/
│       ├── config/             # Configuration loading (~/.config/bentoo/config.yaml)
│       ├── ebuild/             # Ebuild parsing and version comparison
│       ├── git/                # Git operations wrapper
│       ├── github/             # GitHub API client (legacy)
│       ├── logger/             # Structured logging
│       ├── output/             # Terminal output helpers
│       ├── version/            # Version utilities
│       └── provider/           # Repository providers
│           ├── interface.go    # Provider interface
│           ├── factory.go      # Provider factory
│           ├── github.go       # GitHub API provider
│           ├── gitlab.go       # GitLab API provider
│           └── gitclone.go     # Git clone provider
├── Makefile                    # Build targets
└── README.md
```

## Snapshot Management

`bentoo snapshot` manages btrfs snapshots declaratively from a single
`snapshot.toml`. bentoolkit is an **orchestrator**: it renders native config for
mature tools (`btrbk` for snapshots and ssh send/receive, `systemd` for
scheduling) and coordinates them — it never calls `btrfs` directly.

### Dependencies

- `app-backup/btrbk` — the snapshot engine and ssh replication (when `engine.driver = "btrbk"`).
- `app-backup/snapper` — only when `engine.driver = "snapper"` (timeline snapshots + rollback).
- `systemd` — the scheduler backend.
- `app-backup/restic` — only when a `[[ship]]` uses `type = "restic"` (cloud backup).
- `net-misc/rclone` — only when a `[[ship]]` uses `type = "archive"` (cloud backup).

A missing binary is reported at config-validate time with an actionable error
naming the Portage package (e.g. `engine driver "btrbk" requires
app-backup/btrbk on PATH`, or `ship driver "restic" requires app-backup/restic
on PATH`).

When installing through Portage, the `app-portage/bentoolkit` ebuild maps each
backend to a USE flag so you pull in only what your config uses:

| USE flag  | Pulls in            | Enables                                  |
|-----------|---------------------|------------------------------------------|
| `btrbk`   | `app-backup/btrbk`  | btrbk engine (snapshots + ssh ship)      |
| `snapper` | `app-backup/snapper`| snapper engine (timeline + rollback)     |
| `restic`  | `app-backup/restic` | restic cloud ship                        |
| `rclone`  | `net-misc/rclone`   | archive cloud ship                       |
| `systemd` | `sys-apps/systemd`  | systemd timer scheduling                 |

All flags are optional and default-off — the binary degrades gracefully, and
`detect` names the exact missing package at runtime if the active config needs
a backend that is not installed.

### Configuration (`snapshot.toml`)

Resolved in priority order: `/etc/bentoo/snapshot.toml`, then
`$XDG_CONFIG_HOME/bentoo/snapshot.toml`, then `~/.config/bentoo/snapshot.toml`.
System scope (`/etc/bentoo`, system timers) is the primary target.

```toml
[engine]
driver = "btrbk"                 # "btrbk" (backup/replication) | "snapper" (timeline + rollback)
subvolumes = ["/", "/home"]      # btrfs subvolumes to snapshot
snapshot_dir = "/.snapshots"

[engine.retention]               # delegated to btrbk's preserve directives
hourly = 24
daily = 7
weekly = 4
monthly = 6
preserve_min = "latest"

[[ship]]                         # zero or more replication targets
type = "ssh"                     # local/LAN replication via btrbk
target = "user@host:/backup/btrbk"

[[ship]]                         # cloud backup — restic (recommended)
name = "offsite"
type = "restic"
repo = "s3:s3.amazonaws.com/my-bucket"   # or any restic/rclone backend
password_file = "/etc/bentoo/restic.pass" # secret PATH only, never the value
compression = "auto"             # auto | max | off

[[ship]]                         # cloud backup — portable archive object
name = "gdrive"
type = "archive"
remote = "gdrive:bentoo-backups" # an rclone remote:path
mode = "incremental"             # incremental (default) | full
compress = "zstd"                # stream compressor

[schedule]
backend = "systemd"              # only "systemd" in this release
on_calendar = "daily"            # systemd OnCalendar=
persistent = true                # systemd Persistent=
randomized_delay = "5m"          # systemd RandomizedDelaySec=

[notify]                         # best-effort run notifications (every part optional)
on = ["failure"]                 # outcomes that notify: "failure" and/or "success"

[notify.ntfy]
url = "https://ntfy.sh/my-topic" # ntfy topic URL (POST the run summary)
# auth token (optional): set BENTOO_NTFY_TOKEN in the env or the bentoo secrets file

[notify.healthchecks]
ping_url = "https://hc-ping.com/<uuid>"   # base ping on success, /fail on failure
start = true                     # also ping /start before the run

[notify.webhook]
url = "https://example.com/hook" # receives the RunResult as a JSON POST
headers = { Authorization = "Bearer ..." } # optional custom headers, never logged

[notify.email]
to = ["ops@example.com"]         # one or more recipients (activates the driver)
from = "bentoo@myhost"
# transport: local sendmail by default; configure [notify.email.smtp] to use SMTP

[notify.email.smtp]              # optional — omit to send via local `sendmail -t`
host = "smtp.example.com"
port = 587
user = "bentoo"                  # with BENTOO_SMTP_PASSWORD set, enables SMTP AUTH (PLAIN)
# The password is not a config key: put BENTOO_SMTP_PASSWORD in the secrets file
# (~/.config/bentoo/secrets or /etc/bentoo/secrets, chmod 600).
```

### Commands

```bash
# Render the native btrbk.conf and install + enable the systemd timer
bentoo snapshot apply

# Run the engine → prune → ship pipeline now (the timer target)
bentoo snapshot run

# List local snapshots per subvolume; --remote also queries btrbk targets
# and restic repositories
bentoo snapshot list
bentoo snapshot list --remote

# Show the last run (per stage), timer state + next scheduled run, free space
bentoo snapshot status

# Apply [engine.retention] on demand: engine-native prune + archive GFS
bentoo snapshot prune
bentoo snapshot prune --ship gdrive       # scope to one destination only

# Restore a snapshot from a cloud ship (destructive — requires confirmation)
bentoo snapshot restore <id> --target /mnt/restore --ship offsite --yes

# Roll the system back to a snapshot (snapper engine only; destructive)
bentoo snapshot rollback <id> --yes

# Install / remove the opt-in pre/post-emerge snapshot hook (snapper engine)
bentoo snapshot hook --install
bentoo snapshot hook --uninstall
```

`apply` is idempotent — re-running reconciles the units without duplicates.
`--config <path>` overrides the search path on any verb. `run` persists a
`RunResult` under `/var/lib/bentoo/snapshot/last-run.json`, which `status` reads
back.

**Dry-run everywhere.** `apply`, `run`, `restore`, `rollback`, and `prune` all
accept `--dry-run`: the verb prints exactly what it would do (configs and
systemd units it would write, the engine → prune → ship pipeline it would
execute, or the destructive actions it would perform) and **guarantees zero side
effects** — no subprocess is spawned, nothing is written, no confirmation is
prompted. Preview any change safely before committing to it.

### Notifications

The optional `[notify]` section reports the outcome of a `bentoo snapshot run` so a
scheduled backup surfaces failures without scraping logs. Four backends fan out
from one config — configure any subset:

- **ntfy** (`[notify.ntfy]`) — POSTs a run summary to a topic URL. Failures use an
  elevated priority and an alert tag; successes use normal priority. An optional auth
  token, resolved from `BENTOO_NTFY_TOKEN` via the secrets chain, is sent as a Bearer
  header.
- **healthchecks.io** (`[notify.healthchecks]`) — pings the base `ping_url` on
  success and `ping_url/fail` on failure (a dead-man's switch). With `start = true`
  it also pings `ping_url/start` before the run so the dashboard can time it.
- **webhook** (`[notify.webhook]`) — POSTs the `RunResult` as JSON to your own
  endpoint, with any custom `headers` applied — for arbitrary automation.
- **email** (`[notify.email]`) — sends the run summary to the configured
  recipients. Transport is local `sendmail -t` by default; configuring
  `[notify.email.smtp]` switches to direct SMTP (stdlib `net/smtp`, with PLAIN
  auth when `user` is set and `BENTOO_SMTP_PASSWORD` resolves through the secrets
  chain). An unresolvable password sends unauthenticated rather than failing the
  notification. The subject reflects the outcome.

`on` filters which outcomes notify (`["failure"]`, `["success"]`, or both); an empty
or omitted `on` notifies on **failure only**. Notification is **best-effort**: a
backend that errors is logged as a warning and never changes the run's exit code,
and the remaining backends are still attempted. **Secrets** (the ntfy token, webhook
header values, the SMTP password) are sent only in request headers / the SMTP
session and are **never written to logs, argv, or error messages**.

### Cloud backup & restore

Two `[[ship]]` drivers push snapshots off-site, on the same schedule and config as
local snapshots, plus a `restore` verb to bring either back.

- **`restic`** (recommended) — backs up a **read-only snapshot mount** with
  `restic backup` to S3/B2/GCS or any rclone backend: dedup, encryption,
  compression (`auto|max|off`), and granular restore. Retention maps
  `[engine.retention]` to `restic forget --prune`. The transient RO mount is always
  unmounted afterward, **including on error**.
- **`archive`** — streams `btrfs send [-p parent] | zstd | rclone rcat` into a single
  portable object on any rclone remote (e.g. Google Drive); restore is a bit-exact
  `rclone cat | zstd -d | btrfs receive`.
  - **Incremental vs full:** `mode = "incremental"` (default) sends `-p <parent>` when
    a recorded parent exists; otherwise it **warns** and falls back to a full send
    (never silent). The parent for a `(subvolume, ship)` is recorded **only after a
    successful ship** under `/var/lib/bentoo/snapshot/parents/`, so a failed ship
    never breaks the chain.
  - **Archive retention (GFS):** rclone has no retention of its own, so after a
    successful ship bentoolkit lists the remote (`rclone lsjson`), applies a
    grandfather-father-son policy from `[engine.retention]`, and deletes out-of-policy
    objects — but **never the active parent**.

**Restore.** `bentoo snapshot restore <id> --target <path> --ship <name>` dispatches
by the ship's driver. An `archive` restore **validates the full + delta chain before
applying** and refuses a broken chain *before* any `btrfs receive`. Restore is
destructive: it requires `--yes` or an interactive `[y/N]` confirmation.

**Secrets.** Only secret **paths** (`password_file`) and rclone's own config/env are
passed — never secret **values** in argv or TOML — and passwords/tokens are never
written to logs or error messages.

**Notes.** restic re-scans the subvolume locally each run (dedup avoids re-upload but
the scan still happens — fine for typical subvolumes). For `archive` incremental
chains, deleting a mid-chain delta would break restorability of later snapshots;
GFS is fully safe for `mode = "full"`, and restore-time chain validation is the
backstop for incremental.

### Rollback (snapper engine)

With `engine.driver = "snapper"` the same config drives **local timeline
snapshots and system rollback** — the "undo a broken update" path. btrbk is
built for backup/replication; snapper is the rollback engine. The driver is
additive: switching back to btrbk changes nothing in existing behavior.

- **Configs.** `apply` renders `/etc/snapper/configs/<name>` per subvolume
  (`/` → `root`, `/home` → `home`) idempotently: bentoo-managed keys
  (`SUBVOLUME`, `TIMELINE_*` limits from `[engine.retention]`,
  `NUMBER_CLEANUP`) are kept in sync while user-added settings and comments are
  preserved.
- **Pipeline.** `run` creates tagged timeline snapshots
  (`snapper create --description "bentoo snapshot"`); prune delegates to
  `snapper cleanup timeline` (native retention, as with btrbk).
- **Rollback.** `bentoo snapshot rollback <id>` runs `snapper -c root rollback`.
  It is destructive, so it requires `--yes` or an interactive `[y/N]` confirm —
  and it is **refused with a clear error when the active engine is not
  snapper** (rollback is snapper-specific; declining is a clean abort).
- **Emerge hook (opt-in).** `bentoo snapshot hook --install` installs a Portage
  hook (`/etc/portage/bashrc.d/50-bentoo-snapshot.sh`, sourced through a
  managed block in `/etc/portage/bashrc`) that creates snapper **pre/post
  snapshot pairs around each package emerge builds** — so a broken update has
  a known-good "pre" to roll back to. `--uninstall` removes it cleanly,
  preserving your own bashrc content. The hook is **never** installed by
  `apply`, and a snapper failure never breaks an emerge.
- **Boot integration.** grub-btrfs / boot-into-snapshot integration is a
  documented follow-up, not part of this release.

### Scope

This release covers the config model, the `btrbk` engine + `ssh`/`restic`/`archive`
shippers, systemd timer generation, dependency detection, run notifications
(ntfy / healthchecks / webhook / email), cloud backup + restore, the `snapper`
engine with system rollback + the opt-in emerge hook, full `--dry-run` coverage,
the on-demand `prune` verb, remote listing (`list --remote`), per-stage `status`
with the next scheduled run, and the Portage USE-flag mapping for every optional
backend. grub-btrfs / boot-into-snapshot integration remains a documented
follow-up.

## License

MIT
