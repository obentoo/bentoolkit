# Bentoolkit

CLI tools for Bentoo Linux distribution maintainers and developers.

## Modules

- **overlay**: Bentoo overlay commit management with automatic message generation

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
```

### Configuration Options

| Option | Description | Required |
|--------|-------------|----------|
| `overlay.path` | Path to your local Bentoo overlay repository | Yes |
| `git.user` | Git username for commits (fallback if not in ~/.gitconfig) | No |
| `git.email` | Git email for commits (fallback if not in ~/.gitconfig) | No |
| `github.token` | GitHub personal access token for higher API rate limits | No |
| `repositories.<name>` | Custom repository definitions for the compare command | No |

The tool will automatically use your `~/.gitconfig` settings for user name and email if available.

## Usage

### Overlay Commands

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

To create a token: Go to [GitHub Settings → Developer settings → Personal access tokens](https://github.com/settings/tokens) and generate a new token. No scopes are required (public repository access only).

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

### Workflow Example

Typical workflow for adding a new package version:

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
├── cmd/bentoo/            # CLI commands
│   ├── main.go            # Entry point
│   ├── overlay_add.go     # overlay add command
│   ├── overlay_commit.go  # overlay commit command
│   ├── overlay_compare.go # overlay compare command
│   ├── overlay_push.go    # overlay push command
│   └── overlay_status.go  # overlay status command
├── internal/
│   ├── common/
│   │   ├── config/        # Configuration loading
│   │   ├── ebuild/        # Ebuild parsing and version comparison
│   │   ├── git/           # Git operations wrapper
│   │   ├── github/        # GitHub API client (legacy)
│   │   └── provider/      # Repository providers
│   │       ├── interface.go   # Provider interface
│   │       ├── factory.go     # Provider factory
│   │       ├── github.go      # GitHub API provider
│   │       ├── gitlab.go      # GitLab API provider
│   │       └── gitclone.go    # Git clone provider
│   └── overlay/           # Overlay business logic
│       ├── compare.go     # Package comparison logic
│       └── scanner.go     # Overlay scanning
├── Makefile               # Build targets
└── README.md
```

## License

MIT
