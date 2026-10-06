# Bentoolkit configuration

Back to the [README](../README.md).

## Configuration

Bentoo reads `~/.config/bentoo/config.yaml` (or `$XDG_CONFIG_HOME/bentoo/config.yaml`).
The repository ships a fully commented [`config.example.yaml`](../config.example.yaml) —
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
| `notice.site_path` | Root of the site repository `bentoo notice` writes `src/content/notices/<id>.yaml` into. A leading `~/` is expanded. Unset: only the news item is written and the site YAML is printed | No |
| `ui.mode` | How long-running commands render themselves: `auto`, `plain`, `inline` or `fullscreen`. Unset falls through to `auto` (inline on a terminal, plain in a pipe), which is exactly today's behaviour. Overridden by `--ui <mode>` and `BENTOO_UI` | No |

The tool will automatically use your `~/.gitconfig` settings for user name and email if available.

**Which overlay a run uses.** `--overlay <path>` wins. Without it, a run
started inside another checkout of the configured overlay — a git worktree or
a second clone, recognised by the same `profiles/repo_name` — works on that
checkout and says so in an INFO line. Otherwise `overlay.path` applies. The
autoupdate state (`pending.json`, the version cache, logs, staged trees) lives
in `$XDG_CONFIG_HOME/bentoo/autoupdate` (else `~/.config/bentoo/autoupdate`),
beside `config.yaml`, and is shared by every checkout.

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
| Authenticated-fetch serial | the value of `fetch_serial_env` (e.g. `BENTOO_FETCH_FILEZILLA_PRO_KEY`), which must be a `BENTOO_FETCH_*` name spelled with letters, digits and underscore only; absent for a record that configures no serial |
| Authenticated-fetch form values | each variable `fetch_form_env` names (e.g. `BENTOO_FETCH_BMD_EMAIL`), each a `BENTOO_FETCH_*` name spelled with letters, digits and underscore only; absent for a record without `fetch_form_env` |
| ntfy auth token | `BENTOO_NTFY_TOKEN` |
| SMTP password | `BENTOO_SMTP_PASSWORD` — enables PLAIN auth together with `[notify.email.smtp] user`; unresolvable means the mail is sent unauthenticated |

For `overlay compare` the GitHub token precedence is **`--token` flag >
per-repo `BENTOO_REPO_<NAME>_TOKEN` > global `GITHUB_TOKEN`/`GH_TOKEN`**.

> **One deliberate exception:** `${VAR}` expansion in `packages.toml` request
> `headers` reads the **process environment only** (never the secrets file) —
> see [Headers and environment variables](behaviour.md#headers-and-environment-variables).

### Logging

bentoo writes its diagnostics — warnings about degraded paths, progress
detail, a failing command's cause — in two places on every run:

- **stderr**, one `key=value` line per diagnostic (`log/slog` text format,
  without a timestamp), for example
  `level=WARN msg="loading config: failed" err="..."`;
- **`$XDG_STATE_HOME/bentoo/logs/bentoo.log`** (`~/.local/state/bentoo/logs/bentoo.log`
  when `XDG_STATE_HOME` is unset), one JSON object per line with `time`,
  `level`, `msg` and the diagnostic's attributes, so a cron run can be read
  back with `jq`. The directory is created `0750` and the file `0600`; the
  file is appended to and never rotated (use `logrotate`).

The stderr level is set, in order of precedence, by `--quiet` (errors only),
`--verbose` (debug), then the `BENTOO_LOG_LEVEL` environment variable
(`debug`, `info`, `warn` or `error`, any case), and is `info` otherwise. The
file always records `info` and above — `debug` too when the stderr level is
`debug` — so `--quiet` never empties it.

Every secret bentoo resolved (see [Secrets](#secrets)) is replaced with `***`
in both places, as is the value of any attribute whose key names a credential
(`*_token`, `*_password`, `*_secret`, `*_api_key`, `*_authorization`). A
command's own output — results, reports, prompts — is not a diagnostic: it is
not written to `bentoo.log`.
