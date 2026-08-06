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

#### Prune Redundant Packages

`overlay compare` recommends; `overlay prune` acts on the recommendation — but
never on the recommendation alone.

```bash
# Plan the whole overlay. Removes nothing.
bentoo overlay prune

# Restrict the plan
bentoo overlay prune app-editors
bentoo overlay prune app-editors/zed

# Carry the plan out
bentoo overlay prune --apply
```

**The verdict does not authorise the removal.** A verdict is a statement about
*versions*: "::gentoo ships the same or more". Whether deleting our copy loses
anything is a statement about *content*. Measured on the live overlay: of the 74
packages `compare` calls `redundant`, **8 carry real local changes nobody
declared** — `kwin`, `plasma-desktop` and `nodejs` among them. A prune driven by
the verdict alone deletes work.

So the verdict only selects the candidates, and a byte comparison decides: every
version the two trees share must match, and the whole `files/` tree with it.
`Manifest` and `metadata.xml` are never compared — the first holds distfile
hashes that differ by revision, the second differs by maintainer on every
package we carry.

The plan prints three groups, and every package in them carries its reason:

| Group | What it is | Removed by |
|---|---|---|
| Identical | our copy holds nothing of ours | `--apply` |
| Diverging | an **undeclared** difference the byte comparison found | `--apply --include-patched` |
| Refused | no flag on this command removes these | nothing |

**A package whose registry entry declares `patched` is Refused, not Diverging.**
The declaration already makes `overlay compare` call it `keep` rather than
`redundant`, and this command never removes a package the verdict refused —
`--include-patched` does not reach it. If such a copy is genuinely obsolete,
clear its declaration first; that is `overlay analyze`'s business, and it leaves
a record of the decision.

**Options:**

| Flag | Description | Default |
|------|-------------|---------|
| `--apply` | Carry out the plan | false (plan only) |
| `--include-patched` | Also remove undeclared divergence, discarding that work | false |
| `--keep-registry` | Leave `.autoupdate/packages.toml` untouched | false |
| `--yes` | Skip the identical batch's confirmation | false |

The provider flags (`--clone`, `--cache-dir`, `--no-cache`, `--timeout`,
`--token`, `--sync`) are inherited from `overlay compare`.

**`--yes` does not cover `--include-patched`.** The two batches are two
decisions, and `--apply` asks about them separately. `--yes` answers for the
identical batch, which loses nothing — every byte is already in ::gentoo, so the
worst case is a re-sync. It does **not** answer for the diverging batch, and a
session with no terminal is refused there outright, `--yes` or not: that flag
exists so a scripted run can proceed unattended, and discarding the only copy of
something is not a decision a script may take on its own.

A removal deletes the whole package directory and then every
`.autoupdate/packages.toml` entry of that atom — all of them, since 90 of the
registry's 321 atoms carry more than one, and a half-deleted atom keeps updating
a package that is gone. The registry edit runs **after** the removals and only
for the packages whose directory actually went, so the file never claims a
removal that did not happen. One failed removal does not stop the rest; the run
reports it and exits non-zero.

**A local ::gentoo tree is required.** An API provider has no content to
authorise anything with, and fetching it would cost one rate-limited request per
package. Such a run refuses everything and says so, rather than comparing ~300
packages to reach a refusal that was certain beforehand:

```yaml
# ~/.config/bentoo/config.yaml
repositories:
  gentoo:
    provider: local
    path: /var/db/repos/gentoo
```

Nothing here commits or pushes. The overlay's own automation publishes, and a
prune that also committed would remove the window in which a wrong removal is
still local.

#### Autoupdate

Check for new upstream versions and apply them automatically:

```bash
# Check all packages configured in packages.toml
bentoo overlay autoupdate

# Check a specific package
bentoo overlay autoupdate app-misc/hello

# Check the registry itself against the record model (read-only)
bentoo overlay autoupdate --lint

# …and repair what has a mechanical fix (prints the diff, then asks)
bentoo overlay autoupdate --lint --fix
```

`--lint` reports every record missing its `# END` marker or its `comments`
field, every comment left floating outside a record, every record whose fields
are semantically invalid, and every deviation from the closed field set — an
unknown or retired key, a redundant `enabled = true`, fields out of the
canonical order, or an entry tracking commits with no `base_from` (whose base
version can freeze unnoticed). It exits non-zero, so it doubles as a pre-commit
gate on the registry.

`--fix` repairs the deviations that have one right answer: the retired `binary`
key becomes `type = "bin"` (or is dropped where `type` is already there), a
redundant `enabled = true` goes, and fields are reordered. It never guesses —
an unknown key and a missing `base_from` are reported and left to a human,
because a wrong name may be a misspelling or a concept that does not exist, and
choosing between `base_from = "file"`, `"tag"`, `"commit_message"` and `"none"`
depends on where upstream versions itself — or whether it versions itself at
all.

The repair is textual: your quoting, spacing and every `comments` block come
through byte for byte. Before writing it reparses the result and compares it
record by record against the original, aborting without writing on any
difference outside those transformations. **The write is gated behind the diff
and a confirmation** — this overlay auto-commits and pushes, so a repair written
unattended is a repair published unattended. Use `--yes` only when you mean
that; a piped or scripted run without it prints the diff and writes nothing.

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

Place a `packages.toml` file in the root of your overlay. Each entry — a
*record* — defines how to extract the version for a package:

```toml
["app-misc/hello"]
url = "https://api.github.com/repos/owner/hello/releases/latest"
parser = "json"
path = "tag_name"
comments = """
hello — GitHub release tag "vX.Y.Z" (the "v" is stripped before comparison).
"""
# END

["dev-libs/mylib"]
url = "https://example.com/releases"
parser = "regex"
pattern = 'mylib-([0-9.]+)\.tar\.gz'
comments = """
mylib — the download index; the pattern is anchored on the tarball name so a
changelog mention of an older version cannot win the first match.
"""
# END

["app-text/myapp"]
url = "https://example.com/downloads"
parser = "html"
selector = "a.release-tag"
comments = """
myapp — the download page's release badge; there is no JSON endpoint.
"""
# END
```

#### The record model

Two conventions hold the file together as it grows past a few hundred entries.
Both are checked by `bentoo overlay autoupdate --lint`.

**Every record ends with a `# END` line.** TOML has no block delimiter, and a
bare `[END]` table would not be one — it would parse as a package named `END`,
and repeated once per record, as a duplicate-table error that stops the whole
file from loading. A comment on the record's last line is the closest valid
equivalent, and it makes the boundary between two records explicit.

**Documentation lives in the `comments` field, never in a floating `#` line.**
This is not only tidiness. A comment sitting between two records belongs to
neither, so nothing says which one it describes; and comments do not survive a
rewrite — `bentoo overlay analyze --save` re-encodes the whole registry, which
used to erase every doc comment in it. As a field the text is data: it has an
owner, and it comes back out.

Write it as a TOML multi-line string starting with the package name, as the last
field of the record, and keep `[` off the start of any line inside it (the
raw-text editors that flip `enabled` scan for `[section]` headers and would read
such a line as one).

The one exception is the **file header**: the comment block before the first
record. It documents the model itself rather than any single package, so it can
live inside no record, and `--lint` leaves it alone. The exemption ends at the
first `[section]` header — a comment after that, between records or trailing the
last one, is reported as before.

##### Field order

Fields run bookkeeping → source → extraction → post-processing → transport →
classification → auxiliary substitution → doc. Omit what you do not need; never
invent a key — `PackageConfig` in
[`internal/autoupdate/config.go`](internal/autoupdate/config.go) is the sole
authority on what parses, and a key it does not declare **fails the load**,
naming the record and the key. That is deliberate: `serie` instead of `series`
used to disable the release-line filter silently, which is exactly the failure
`series` exists to prevent.

The order below is not a style preference — it is the practice measured across
the overlay's records, encoded as `CanonicalFieldOrder` in
[`internal/autoupdate/lint.go`](internal/autoupdate/lint.go). `--lint` reports a
record that deviates and `--lint --fix` reorders it, so this block and the
linter cannot disagree.

```toml
["category/package"]                # header: quoted, exactly as in the overlay
enabled = false                     # ONLY when false. Absent = enabled.
hold = true                         # ONLY when true. See "enabled vs hold".
track = "commit"                    # omit for tag/version tracking
url = "https://…"                   # REQUIRED — the endpoint being probed
parser = "json"                     # REQUIRED — json | regex | html | script
path = "tag_name"                   # REQUIRED for parser=json
pattern = 'name-([0-9.]+)\.tar\.xz' # REQUIRED for parser=regex (1 capture group)
selector = "a.release-tag"          # REQUIRED for parser=html (or xpath)
script = "@vendor.js"               # REQUIRED for parser=script (scripts/vendor.js)
transform = [['^v', ""]]            # ordered regex substitutions on the result
select = "max"                      # first (default) | max | last
suffix = "_pre"                     # pre-release channel marker
suffix_when = '^26\.8\.'            # …applied only to a matching version
commit_sha_path = "[0].sha"         # REQUIRED with track="commit"
commit_message_path = "commit.message"
commit_version_pattern = 'sdk-([0-9.]+)'
base_from = "file"                  # where the base lives: file | tag | commit_message | none
base_url = "https://raw.…/VERSION"  # REQUIRED with base_from="file"
base_pattern = '^([0-9][0-9.]*)-devel'  # …1 capture group, the base version
base_tag_pattern = 'vulkan-sdk-([0-9.]+)'  # REQUIRED with base_from="tag"
headers = { "User-Agent" = "bentoo-autoupdate" }
timeout = 60                        # seconds, only for reliably slow hosts
meta = { fetch_url = "https://…" }  # authenticated fetch; NEVER a secret
type = "bin"                        # ONLY to override the -bin/RESTRICT heuristic
series = '^1\.28\.'                 # REQUIRED when the dir holds two release lines
aux_var = "MY_BUILD"                # free-text ebuild var kept in sync…
aux_pattern = 'esr-bb([0-9]+)'      # …always paired with aux_var
comments = """…"""                  # REQUIRED — the doc, always last
# END
```

There is no `binary` key. It was retired: nothing ever read it, and `type`
classifies. `--lint --fix` migrates a record still carrying it.

`meta` is **not** documentation-only, whatever an older comment may have said.
The applier reads six typed keys out of it for authenticated downloads —
`fetch_url`, `fetch_method`, `fetch_serial_env`, `fetch_serial_field`,
`fetch_form`, `fetch_filename` — and `--lint` validates them, because a typo in
`fetch_serial_env` used to disable the download without a word. The rule about
secrets stands: reference an env var, never the value.

##### Rules that are not obvious from the field list

- **Regex values** (`pattern`, `aux_pattern`, `commit_version_pattern`,
  `base_pattern`, and the left side of every `transform` rule) use TOML
  **literal** strings `'…'`. A basic string `"…"` rejects `\.` and `\d`
  outright. Replacements use basic strings.
- **Where the base version comes from** (`track = "commit"` only). The
  `_p<date>`/`_pre<date>` suffix is derived from the current ebuild, but the
  `X.Y.Z` in front of it needs a source, and `base_from` names it:
  - `"file"` — fetch `base_url`, apply `base_pattern`. **Prefer this.** One
    request, no window, no pagination; use it whenever upstream versions itself
    in-tree (`crates/zed/Cargo.toml`, mesa's `VERSION`, `meson.build`,
    `CMakeLists.txt`). Go anchors `^`/`$` to the whole body, so a version
    declared mid-file needs `(?m)`.
  - `"tag"` — fetch a tag listing (`…/git/refs/tags` on GitHub,
    `…/repository/tags` on GitLab) and take the highest version matching
    `base_tag_pattern`. Use it when the scheme **the ebuild uses** exists only
    as tags: glslang and spirv-\* version themselves `2026.3`/`1.5.5` in-tree
    while the overlay tracks `vulkan-sdk-X.Y.Z.W`. Always anchor the pattern to
    one tag family — these repos carry four or more at once, and an unfiltered
    ranking picks `khronos-master-20141209` for vulkan-loader.
  - `"commit_message"` — the older scan via `commit_version_pattern`. Correct
    only when upstream announces releases in commit titles **and** commits
    slowly enough that the bump stays inside the fetch window. That window is
    measured in commits, not days: `per_page=50` covers ten months of
    Vulkan-Headers but 1.3 days of zed.
  - `"none"` — the upstream publishes no version at all: no usable tag, nothing
    in-tree, nothing in the commit titles. The base is a constant you chose
    (conventionally `0`) and only the snapshot suffix moves. It resolves nothing
    at check time, so `base_url`, `base_pattern`, `base_tag_pattern` and
    `commit_version_pattern` must all be absent — declaring one alongside it is
    a contradiction, not dead weight.

    Say it out loud rather than leaving `base_from` off. The two read
    identically to the checker but not to a human, and `--lint` cannot tell
    "nobody declared the source" from "there is no source to declare" unless the
    second says so: `sci-ml/ik_llama-cpp` (one tag, `t0002`, a prerelease a year
    behind an active HEAD) and `sys-apps/asus-ec-sensors` (one stale `v0.1.0`,
    board support landing as plain commits) were the only two records the rule
    reported across 411, and both were right all along.
  - Absent — the legacy form of `"none"`, kept working for registries written
    before the field existed. It behaves identically; it just cannot say whether
    that was the intent, which is why `--lint` reports it.

  A declared source that resolves nothing is now a **check failure**, not a
  fallback. Six of the seven entries that carried a `commit_version_pattern`
  matched nothing — the pattern had been copied to sibling repos that never
  write it — and their bases froze up to seven releases behind while the
  `_p<date>` kept advancing, so the versions looked alive and were not.
  Pick the source that matches the scheme **the ebuild** uses: spirv-tools
  publishes `v2026.3` in `CHANGES`, but the overlay versions it on the
  `vulkan-sdk` scheme, so that file is the wrong source even though it parses.
- **`enabled` vs `hold`.** `enabled` is *bookkeeping the checker flips on its
  own*: it writes `enabled = false` when the ebuild vanishes from the overlay,
  and **deletes that line** when it reappears. The deletion is the point —
  enabled is the default, spelled by the key's absence, so writing
  `enabled = true` would state nothing the file did not already say and `--lint`
  reports it as redundant. `hold` is a *maintainer decision* ("present, but
  never auto-bump") that reconciliation never touches. A package needing manual
  work each release (patchset, pinned SHA, bootstrap compiler) takes `hold` —
  `enabled = false` would be silently reverted. Both skip the fetch entirely.
- **User-Agent** is required by `api.github.com` and `crates.io` — always the
  literal `"bentoo-autoupdate"`. A browser UA is a last resort for a
  Cloudflare-fronted host, and the reason belongs in `comments`.
- **Regex returns capture group 1 of the FIRST match** on the raw body; anchor
  it so a page listing several releases cannot yield an older one, or use
  `select = "max"`.
- **Verify before committing.** Probe the real endpoint with
  `bentoo overlay autoupdate --check <category/package> --force`; never
  hand-write a record from a guessed URL shape.

#### Several release lines of one package (`series`)

An overlay routinely carries more than one ebuild per package, and **one entry
cannot track them all**: the scan takes the directory's highest version, so the
other lines are never bumped.

The `:slot` key suffix already covers the case where the lines are separate
SLOTs — see [Multi-slot packages](#multi-slot-packages). `series` covers the
other half: lines that **share a SLOT** and differ by version. `libreoffice`
keeps the stable 26.2 series beside the testing 26.8 one, both `SLOT=0`;
`zed-bin` keeps 1.13.1 stable beside 1.14.1_pre.

What the absence of it costs is worth stating plainly. With `zed-bin-1.13.1` and
`zed-bin-1.14.1_pre` both present and one entry tracking the stable channel, the
scan returns `1.14.1_pre` as "current" — so every stable release below `1.14.1`
compares *older* and reports "up to date". The stable line stops being updated,
and the silence looks like success.

Give each line its own entry, distinguished by an `@label` in the key, and let
`series` say which versions belong to it:

```toml
["app-office/libreoffice@stable"]
url = "https://downloadarchive.documentfoundation.org/libreoffice/old/"
parser = "regex"
pattern = 'href="([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)/"'
select = "max"
series = '^26\.2\.'
comments = """…"""
# END

["app-office/libreoffice@testing"]
url = "https://downloadarchive.documentfoundation.org/libreoffice/old/"
parser = "regex"
pattern = 'href="([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)/"'
select = "max"
series = '^26\.8\.'
suffix = "_pre"
comments = """…"""
# END
```

`series` narrows **both ends** of the comparison: which ebuild counts as the
entry's current version, and which upstream candidates survive selection. An
extraction path that yields a single value (first match, `script`, fallback,
LLM) *fails* when the version falls outside the series, rather than comparing it
against an ebuild the entry does not track.

The `@label` is identity only — it makes the key unique and never reaches a
filesystem path or a `SLOT=` lookup. `@` rather than `:` because `:` already
means SLOT; both may appear (`net-libs/webkit-gtk:4.1@lts`). Two entries for one
package must differ by slot **or** series: a label alone filters nothing, and
`--lint` rejects it.

Note that with `series` the `suffix_when` below becomes unnecessary — the series
already delimits the line, so a plain `suffix = "_pre"` says the rest.

#### Pre-release channels (`suffix`)

Upstream numbering rarely says a release is a pre-release. LibreOffice publishes
`26.8.0.1` in its **testing** channel with a version string indistinguishable
from a stable one, so the bare value lands in the overlay as if it were a
finished release — and a bump silently drops the `_pre` the ebuild carried.

`suffix` declares the truth, and with it the ordering. Gentoo sorts `_pre` below
the bare version, so `26.8.0.1_pre` stays *older* than the eventual `26.8.0.1`
and the bump fires exactly when upstream promotes the release:

```toml
["app-office/libreoffice"]
url = "https://downloadarchive.documentfoundation.org/libreoffice/old/"
parser = "regex"
pattern = 'href="([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)/"'
select = "max"
suffix = "_pre"
suffix_when = '^26\.8\.'
comments = """
libreoffice — old/ lists the stable 26.2 line and the testing 26.8 one in one
index, and select=max always returns the latter, so suffix_when marks that line
(and only it) as _pre. The ebuild strips it back out for SRC_URI via
MY_PV="${MY_PV/_pre/}". When 26.8 is promoted to stable, drop suffix_when or
point it at the next development line.
"""
# END
```

`suffix_when` is what makes one endpoint serving several release lines
workable. Omit it when the probed URL **is** the pre-release channel: then every
version it yields is a pre-release and the suffix applies unconditionally.

The suffix is applied after `transform` and before comparison, so `select = "max"`
orders the values that will actually become the PV. It is idempotent — a version
upstream already marked (`2.0.0_rc1`) is left alone — and it cannot be combined
with `track = "commit"`, whose `_p<date>` snapshot suffix comes from the current
ebuild instead.

Valid values are the Gentoo suffixes, optionally numbered: `_alpha`, `_beta`,
`_pre`, `_rc`, `_p`. The ebuild must be able to strip the suffix when building
`SRC_URI`, since upstream's filenames do not carry it.

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
| `type` | `"bin"` for a binary package (manifest-only testing), `"source"` for a source-built one. Only to **override** the auto-detection, which already reads the ebuild (`RESTRICT="bindist"`, a `-bin` suffix, a binary `SRC_URI`). Replaces the retired `binary` key; `--lint --fix` migrates a record still carrying it. |
| `series` | Regex restricting the entry to one release line — which ebuild counts as current, and which upstream candidates are eligible. For a package whose parallel ebuilds share a SLOT; see [Several release lines](#several-release-lines-of-one-package-series). |
| `suffix` | Gentoo pre-release suffix (`_alpha`, `_beta`, `_pre`, `_rc`, `_p`, each optionally numbered) appended to the detected version — see [Pre-release channels](#pre-release-channels-suffix). |
| `suffix_when` | Regex gating `suffix`: the suffix is appended only to a version matching it. Omit when the probed URL *is* the pre-release channel. |
| `comments` | The record's documentation, as its last field — see [The record model](#the-record-model). |
| `revision` | The `-rN` suffix to write on a freshly bumped ebuild. Only for multi-slot packages that use the revision to tell their SLOTs apart — see [Multi-slot packages](#multi-slot-packages). Absent/`0` writes a plain PV, which is what an ordinary package wants. |

#### Multi-slot packages

Some packages ship several SLOTs out of one directory, distinguished by the
revision suffix rather than by the version. `net-libs/webkit-gtk` is the
canonical case: `-r410`/`-r411` are SLOT `4.1` and `-r600`/`-r601` are SLOT `6`,
all sharing the same PV series.

A single entry cannot express that. Taking the directory's highest version picks
whichever slot happens to be ahead, so one slot is bumped forever and the other
never is — and naming the destination from the bare upstream PV aims it at the
*other* slot's filename.

Give each slot its own entry by suffixing the key with `:slot`, and declare the
slot's base revision:

```toml
["net-libs/webkit-gtk:4.1"]
url = "https://www.webkitgtk.org/releases/"
parser = "regex"
pattern = 'webkitgtk-(2\.52\.[0-9]+)\.tar\.xz'
select = "max"
revision = 410

["net-libs/webkit-gtk:6"]
url = "https://www.webkitgtk.org/releases/"
parser = "regex"
pattern = 'webkitgtk-(2\.52\.[0-9]+)\.tar\.xz'
select = "max"
revision = 600
```

Each entry then resolves its current version by reading every ebuild's `SLOT=`
and considering only its own slot, keeps its own pending and cache records, and
writes its bump as `<pv>-r<revision>`.

Two things to know:

- **`revision` is the slot's base, not the source ebuild's.** Bumping
  `webkit-gtk-2.52.3-r411` yields `webkit-gtk-2.52.5-r410`, matching ::gentoo:
  `r411` was a revbump *within* the old PV, and a PV change resets it. Set
  `revision` to the value the slot's first ebuild of a new version carries.
- **Omit `revision` for a slot whose ebuilds carry no suffix.** If your overlay's
  SLOT 6 ebuild is `webkit-gtk-2.52.5.ebuild` rather than `-r600`, leave the
  field out for that entry so the plain PV is written.

The `:slot` suffix is part of the entry's identity only — it never appears in a
filesystem path. If a bump would ever land on an ebuild that already exists, the
apply fails with `destination ebuild already exists` and touches nothing.

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
│   ├── overlay_prune.go        # overlay prune command
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
