# Security Policy

## Supported Versions

`bentoolkit` is pre-1.0 and ships frequent releases. Security fixes land on the
latest released minor only; please upgrade before reporting.

| Version | Supported          |
| ------- | ------------------ |
| 0.11.x  | :white_check_mark: |
| < 0.11  | :x:                |

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
