# Bentoolkit overlay commands

Back to the [README](../README.md).

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

#### Pull Upstream Changes

Fetch the configured remote and integrate the current branch's upstream:

```bash
bentoo overlay pull
```

The integration is fast-forward only by default: if the overlay has diverged
from its upstream, the pull refuses rather than writing a merge commit. Pick a
strategy explicitly when it has:

```bash
bentoo overlay pull --rebase   # replay local commits on top of the upstream
bentoo overlay pull --merge    # accept a merge commit
bentoo overlay pull --dry-run  # report what would be integrated, change nothing
```

The upstream comes from the branch that is checked out, not from the remote's
default branch, so pulling on a work branch never drags `master` into it. A
branch with no upstream is an error, not a silent fallback.

`bentoo overlay sync` remains as an alias for this command.

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
   outside `[A-Z0-9]` replaced by `_`). See [Secrets](configuration.md#secrets).

3. **Global token** — the `GITHUB_TOKEN` (or `GH_TOKEN`) environment variable,
   or a matching line in the secrets file:
   ```bash
   export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
   bentoo overlay compare
   ```

`config.yaml` no longer holds a token — the value is resolved once through the
secrets chain (see [Secrets](configuration.md#secrets)).

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
