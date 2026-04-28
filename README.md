# Bentoolkit

CLI tools for Bentoo Linux distribution maintainers and developers.

## Modules

- **overlay**: Bentoo overlay commit management, version comparison, and automated updates

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
make test            # Run tests
make coverage        # Run tests with coverage report
make audit           # Run security audit (go mod verify + govulncheck)
make clean           # Remove build artifacts
make build-all       # Cross-compile for linux amd64 and arm64
make check           # Run lint, test, and audit
make help            # Show all available targets
```

## Configuration

Create the configuration file at `~/.config/bentoo/config.yaml`:

```yaml
overlay:
  path: /var/db/repos/bentoo

git:
  user: your_username
  email: your_email@example.com

github:
  token: ghp_xxxxxxxxxxxx  # Optional: for higher API rate limits

# Optional: custom repositories for compare command
repositories:
  my-overlay:
    provider: github  # github, gitlab, or git
    url: myuser/my-overlay
    branch: main

# Optional: LLM provider configuration for autoupdate
llm:
  provider: claude        # claude, openai, or ollama
  api_key_env: ANTHROPIC_API_KEY
  model: claude-3-haiku-20240307
```

### Configuration Options

| Option | Description | Required |
|--------|-------------|----------|
| `overlay.path` | Path to your local Bentoo overlay repository | Yes |
| `git.user` | Git username for commits (fallback if not in ~/.gitconfig) | No |
| `git.email` | Git email for commits (fallback if not in ~/.gitconfig) | No |
| `github.token` | GitHub personal access token for higher API rate limits | No |
| `repositories.<name>` | Custom repository definitions for the compare command | No |
| `llm.provider` | LLM provider for autoupdate: `claude`, `openai`, or `ollama` | No |
| `llm.api_key_env` | Environment variable name containing the API key | No |
| `llm.model` | Model name (e.g. `claude-3-haiku-20240307`, `gpt-4o-mini`) | No |

The tool will automatically use your `~/.gitconfig` settings for user name and email if available.

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

You can provide a token in three ways (priority order):

1. **Command line flag:**
   ```bash
   bentoo overlay compare --token ghp_xxxxxxxxxxxx
   ```

2. **Environment variable:**
   ```bash
   export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
   bentoo overlay compare
   ```

3. **Configuration file** (`~/.config/bentoo/config.yaml`):
   ```yaml
   github:
     token: ghp_xxxxxxxxxxxx
   ```

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

  # Custom GitHub overlay
  my-overlay:
    provider: github
    url: myuser/my-overlay
    token: ghp_xxxxxxxxxxxx

  # Generic git repository
  local-mirror:
    provider: git
    url: https://git.example.com/overlay.git
    branch: main
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

**Optional fields:**

| Field | Description |
|-------|-------------|
| `fallback_url` | Secondary URL to try if the primary fails |
| `fallback_parser` | Parser type for the fallback URL |
| `fallback_pattern` | Pattern/path for the fallback parser |
| `llm_prompt` | Custom prompt for LLM-assisted extraction |
| `headers` | Custom HTTP headers (supports `${ENV_VAR}` substitution) |
| `binary` | Set to `true` for binary packages (manifest-only testing) |

#### Supported LLM Providers

The `analyze` and `autoupdate` commands can use an LLM for version extraction when parsers are insufficient.

| Provider | Config value | API key env var | Notes |
|----------|-------------|-----------------|-------|
| Anthropic Claude | `claude` | `ANTHROPIC_API_KEY` | Default model: `claude-3-haiku-20240307` |
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

## License

MIT
