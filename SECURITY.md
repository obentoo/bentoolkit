# Security Policy

## Supported Versions

`bentoolkit` is pre-1.0 and ships frequent releases. Security fixes land on the
latest released minor only; please upgrade before reporting.

| Version | Supported          |
| ------- | ------------------ |
| 0.34.x  | :white_check_mark: |
| < 0.34  | :x:                |

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
pull requests, or discussions.**

Report privately through GitHub's **Private Vulnerability Reporting**:

1. Open the [new advisory form](https://github.com/obentoo/bentoolkit/security/advisories/new).
2. Click **Report a vulnerability** and fill in the details.

This keeps the report confidential, lets us collaborate on a fix in a private
fork, and supports CVE issuance when warranted.

Please include:

- A description of the vulnerability and its impact.
- Steps to reproduce (a minimal proof of concept is ideal).
- Affected version(s), operating system, and Go toolchain.
- Any suggested remediation, if you have one.

### What to expect

- **Acknowledgment** within 3 business days.
- **Initial assessment** within 10 business days, with a severity estimate and
  an expected timeline.
- **Progress updates** at least every 10 business days until resolution.

This is a community-maintained project, so timelines are best-effort.

## Coordinated Disclosure

We follow coordinated disclosure. Please give us a reasonable window to ship a
fix before any public disclosure — by default up to **90 days** from
acknowledgment, or sooner once a fix is released. We are glad to credit
reporters in the advisory unless you prefer to remain anonymous.

## Scope

**In scope** — the `bentoolkit` Go codebase (the `overlay` and `snapshot`
modules), its build chain, and the CI/release workflows in this repository.

**Out of scope** — vulnerabilities in upstream software that bentoolkit merely
orchestrates or depends on (Gentoo/Portage, third-party overlays, `btrbk`,
`snapper`, `systemd`, `btrfs`); please report those to their respective
projects. Because `snapshot` and `overlay` operations run with elevated
privileges by design, issues that require pre-existing root/privileged access
are out of scope unless they cross a clear privilege boundary.

## Security Measures

Every change to this repository is gated in CI by:

- **`govulncheck`** — reachability-based scanning against the Go vulnerability
  database (pinned via a `go.mod` tool directive).
- **OSV-Scanner** — dependency CVE scanning.
- **gitleaks** — secret scanning across the full git history.
- **zizmor** — GitHub Actions workflow hardening; every action is pinned by
  commit SHA.
- **Dependabot** — weekly dependency updates with a 7-day release cooldown to
  dodge the window when most hijacked or yanked packages are caught.

You can reproduce the dependency audit locally with `make audit`.

## Secret Handling

bentoolkit resolves secrets at runtime instead of storing them in its
configuration files. Neither `config.yaml` nor `snapshot.toml` holds a token,
API key, or password at all. Every secret bentoo consumes — the GitHub token,
per-repository tokens, the LLM API key, the authenticated-fetch serial, the ntfy
token, and the SMTP password — is resolved through a single chain:

1. an environment variable, then
2. the user secrets file `$XDG_CONFIG_HOME/bentoo/secrets` (else
   `~/.config/bentoo/secrets`), then
3. the system secrets file `/etc/bentoo/secrets`.

The secrets file is `.env` style (`NAME=value`, `#` comments, an optional
`export ` prefix) and should be `chmod 600`; bentoo warns once if it is group-
or world-readable.

What a secret value can and cannot reach:

- Secret **values** never appear in logs, in argv, or in error messages.
- An **LLM agent** — every `claude` process bentoo spawns — receives an
  allow-listed environment and nothing else: `PATH`, `HOME`, `TMPDIR`, `LANG`,
  `TERM`, `CLAUDE_CONFIG_DIR`, `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`,
  `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` and their lowercase forms, and every
  `LC_*` and `XDG_*` variable. In bare mode it also receives exactly one
  `ANTHROPIC_API_KEY`, holding the key bentoo resolved; otherwise it receives
  no API key at all and uses the CLI's logged-in session. The manifest fixer
  additionally receives `PORTAGE_*` and one `DISTDIR`, the scratch directory
  bentoo computed for it. `GITHUB_TOKEN`, the ntfy token, the SMTP password and
  every `BENTOO_*` variable stay out.
- **Other subprocesses** — `pkgdev`, `git` and `ebuild` run by bentoo itself —
  still inherit bentoo's environment, so a secret exported in the shell that
  starts bentoo reaches them. The build-validation child (`ebuild` for the
  compile gate) is the exception: it gets its own allow-list. Keep secrets in
  the secrets file rather than in the environment where you can.

A config file that still carries a secret key removed in an earlier release gets
one actionable warning naming the key and where the value belongs, emitted when
the file is loaded — a stale key is never ignored in silence.

## LLM Agents

Some autoupdate operations hand a task to an LLM agent (the `claude` CLI) that
reads untrusted upstream pages. Each agent is started with a fixed permission
set; no configuration key widens an agent's tools, hosts or paths, and a
refused tool ends the run as a normal failure that names the tool (never the
refused call's input) instead of being retried with more.

| Agent | Tools | Files reachable | Network |
| ----- | ----- | --------------- | ------- |
| Text client (version extraction, reviews) | none | none; runs in a private 0700 directory created per call and removed after | none |
| Manifest fixer | `Read`, `Edit`, `Write`, `Bash(pkgdev *)`, `WebFetch` | the package directory | WebFetch to the package's upstream hosts |
| Registry fixer | `Read`, `Edit`, `Write`, `WebFetch` | the `.autoupdate` config directory | WebFetch to the registry entry's hosts |
| Build fixer | `Read`, `Edit` | the staged package directory | none |
| Bump reviewer | `Read` | a private 0700 directory created per review and removed after | none |

- **Directory scope.** `Read` is granted as `Read(//<dir>/**)` and writes as
  `Edit(//<dir>/**)` (the CLI applies Edit rules to every file-writing tool). A
  directory containing anything outside `[A-Za-z0-9._+@/-]` — a space, for
  example — is refused before the agent is spawned.
- **Secrets files are denied by path.** Every agent is denied `Read` of
  `$XDG_CONFIG_HOME/bentoo/secrets` (else `~/.config/bentoo/secrets`) and of
  `/etc/bentoo/secrets`; an agent that can edit is also denied `Edit` of both.
- **WebFetch host rule.** WebFetch is granted only as `WebFetch(domain:<host>)`
  for the package's upstream hosts — the hosts of its registry `url`,
  `mirrors` and `fallback_url`, and, for the manifest fixer, of the http(s) URLs `pkgdev`
  printed — plus `github.com`, `codeload.github.com` and
  `objects.githubusercontent.com`. A host that is not a lowercase DNS name is
  dropped with a warning, so a value in `packages.toml` or in `pkgdev` output
  cannot widen a rule or forge a second one. So is an IPv4 literal — any host
  whose last label is a number (`127.0.0.1`, `169.254.169.254`, `127.1`,
  `127.0.0.0x1`). The check reads how a host is **spelled**, not where it
  **resolves**: a DNS name that points at loopback, a private network or a
  metadata endpoint (for example `169.254.169.254.nip.io`) is still granted if
  it appears in a URL upstream printed. No agent holds `curl`, `wget`, `cat` or
  `ls`.
- **Pinned settings.** Every agent runs with `--permission-mode dontAsk` (a
  call that is not pre-approved is refused, never prompted), loads no user,
  project or local settings — so neither `~/.claude/settings.json` nor an
  overlay's `.claude/settings.json` can widen it — loads no MCP servers or
  account connectors (`--strict-mcp-config`), and runs with inline settings
  that set `permissions.blockReadsOutsideWorkingDirectories` to `true` and
  `permissions.disableBypassPermissionsMode` to `"disable"`.
- **Residual risk.** Permission rules govern the agent's own tools, not the
  programs those tools run. `pkgdev`, which the manifest fixer may run, reads
  files and fetches distfiles by itself, outside these rules. bentoo applies no
  OS-level sandbox to the agent; run it under one (a dedicated user, firejail,
  a container) if that residual risk matters for your host. The same control is
  the only one that closes the DNS-alias case above: a name such as
  `169.254.169.254.nip.io` passes the host rule, and resolving it when the rule
  is built would not help, because the name can resolve differently by the time
  the agent fetches. Egress rules for the user the agent runs as (refusing
  loopback, RFC 1918 and link-local destinations) are what stop it.
