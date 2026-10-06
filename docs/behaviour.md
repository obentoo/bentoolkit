# Bentoolkit runtime behaviour

Back to the [README](../README.md).

### Exit codes

Every `bentoo` command reports its outcome through the process exit code, so it
can be wired into scripts and CI:

| Situation | Code |
|-----------|------|
| Success, including "nothing to do" (`overlay commit` or `overlay staged clean` with nothing staged, `overlay compare` on an overlay with no packages, `overlay autoupdate --list` with nothing pending, `version`) | `0` |
| Any failure a command reports (configuration, git, scan, lock held, write refused, `snapshot hook` without `--install`/`--uninstall`, `overlay manifest` with nothing to update, `overlay autoupdate --lint` with findings) | `1` |
| Usage error: unknown command, unknown flag, wrong argument count, unusable `--ui` | `1` |
| `overlay validate`: an error finding from a deciding gate, or a `--depth` that does not parse | `1` |
| `overlay validate`: the selector matches nothing, or the run fails for a reason other than an interruption | `2` |
| `overlay validate` interrupted | `130` |
| `overlay autoupdate --check` and `overlay analyze --all` batches: every package succeeded | `0` |
| … partial failure — at least one package failed **and** at least one succeeded | `1` |
| … total failure — no package was processed (or the configuration is invalid) | `2` |
| First interrupt (SIGINT/`Ctrl+C`, SIGTERM or SIGHUP) while a cancellable command runs | the code its interrupted run returns — see the list below |
| First interrupt while any other command runs, or before a command is selected | terminated by the signal (a shell reports 128+n) |
| Second interrupt while a cancellable command is handling the first | terminated by the signal |

For a batch, a non-zero exit code is therefore distinguishable: `1` means "some
work landed", `2` means "nothing landed". The per-package errors that caused a
`1` or `2` are also printed so the failing packages can be retried individually.

A failure prints its message on stderr; report rows stay on stdout. The usage
text is printed only for a usage error such as an unknown flag or a wrong
argument count, not when a command fails.

**Cancellable commands.** These commands stop cleanly on their first interrupt
(SIGINT, SIGTERM or SIGHUP alike) and return the code below:

| Command | Interrupted by its first signal |
|---------|---------------------------------|
| `overlay validate` | exit `130` |
| `overlay autoupdate` (`--check --force`) | exit `2` |
| `snapshot status` | exit `0` — the interrupted run renders and returns normally |
| `distfile fetch`, `overlay add`, `overlay analyze`, `overlay commit`, `overlay manifest`, `overlay pull`, `overlay push`, `overlay status`, `snapshot apply`, `snapshot list`, `snapshot prune`, `snapshot rollback`, `snapshot run` | exit `1` |
| `notice new`, `notice revise` | exit `1` — measured while the editor is open: the editor is stopped and nothing is written |
| `overlay compare`, `overlay prune` | exit `1` — an interrupted repository registry fetch prints `interrupted while fetching the repository registry` |
| `overlay staged clean` | waits on no external program; with nothing staged it exits `0` before an interrupt can land |
| `snapshot restore` | refuses before any wait unless a ship entry is configured; its interrupted exit code has not been measured |

At the confirmation prompts of `overlay commit` and `overlay analyze`, a single
`Ctrl+C` ends the command. Every command not listed above — including
`overlay log` and `overlay diff` — is terminated by the first signal. A second
signal always terminates, even while a cancellable command is still winding
down.

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

`--concurrency` counts packages, not connections. During `--check`, at most
**6 requests are in flight to any one host**, whatever `--concurrency` says,
so a host that stops answering is not buried under a pile of hung requests.
Waiting for a slot does not count against a request's timeout. Records that
run the same `script` on the same `url` (fragment included) share one browser
navigation per run, the way identical HTTP reads already share one fetch.

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
   of: `GITHUB_TOKEN`, `GITLAB_TOKEN`.
   `OPENAI_API_KEY` is no longer expandable in a header (it used to be),
   and `ANTHROPIC_API_KEY` is no longer expandable either — see the
   migration note below.

This prevents a malicious or mistaken `packages.toml` from exfiltrating
arbitrary process secrets (e.g. a cloud credential) through a non-auth header
or an arbitrary variable name. A `${VAR}` that does not satisfy both rules is
**passed through literally** (the header value keeps the raw `${VAR}` text) and
a `Warn` is logged.

#### Where each credential may go

`packages.toml` lives in the overlay repository, so a record is written by
whoever contributed it. Each expandable variable is therefore **bound** to the
hosts it belongs to:

| Variable | May be sent to |
|----------|----------------|
| `GITHUB_TOKEN` | `https` only, and only `api.github.com`, `github.com`, `codeload.github.com`, `objects.githubusercontent.com`, `raw.githubusercontent.com` |
| `GITLAB_TOKEN` | `https` only, and only `gitlab.com` (a self-hosted GitLab uses a `BENTOO_*` variable) |
| `BENTOO_*` | the host of the package's own `url` or `base_url` (plain `http` allowed) |

The GitHub and GitLab tokens are pinned to their vendors' hosts, but a
`BENTOO_*` variable follows the record: it goes to whatever host the record's
own `url` or `base_url` names, and whoever wrote the record chose that url.
When you review a `packages.toml` change that references a `BENTOO_*`
variable, check that the record's url is a host you trust with it.

bentoolkit's own secrets are never expanded, whatever host the record names:
`BENTOO_REPO_<NAME>_TOKEN`, `BENTOO_NTFY_TOKEN`, `BENTOO_SMTP_PASSWORD` and
every `BENTOO_FETCH_*` authenticated-fetch secret stay literal in a header and log a `Warn`. Give a header credential its own
`BENTOO_*` name instead.

Hosts are compared exactly, ignoring case and port: a subdomain or a look-alike
is a different host. A record that pairs a variable with any other host is
**refused**: that package's check fails, before any request is sent, with a
message naming the header, the variable and the host — and the rest of the
batch runs normally. The refusal is decided from the variable's *name*, so it
happens whether or not the variable is set on the machine running the check.
A refused package does not try its `mirrors`, its `fallback_url` or the LLM stage.

**Redirects.** When an upstream redirects to another host, the credential
headers (`Authorization`, `X-Api-Key`, `X-Auth-Token`, `Private-Token`) are
dropped for the rest of the redirect chain and the redirect is followed. A
redirect from `https` to plain `http` is refused when the request carried one
of them, so a token is never sent in cleartext.

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
# Expanded: allow-listed header + BENTOO_-prefixed variable, sent to the
# package's own host (api.example.com).
Authorization = "Bearer ${BENTOO_MY_TOKEN}"
X-Api-Key = "${BENTOO_HELLO_KEY}"
```

```toml
[app-misc/world]
url = "https://api.github.com/repos/example/world/releases/latest"
parser = "json"
path = "tag_name"

[app-misc/world.headers]
# Expanded: GITHUB_TOKEN is bound to the GitHub hosts, and this is one of them.
Authorization = "Bearer ${GITHUB_TOKEN}"
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

**Migration (BREAKING, credential binding):**
- `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` are no longer expandable in a header — a reference to either is now passed through literally with a `Warn`. Rename it to a `BENTOO_*` variable (e.g. `${BENTOO_OPENAI_API_KEY}`), which then goes only to the package's own host.
- A record that sent `${GITHUB_TOKEN}` or `${GITLAB_TOKEN}` to a host outside its binding is now refused: move that credential to a `BENTOO_*` variable as well.
- A GitLab repository configured with an `http://` URL is now rejected; use `https://`.

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
