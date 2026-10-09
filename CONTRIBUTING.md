# Contributing to bentoolkit

Thank you for helping. This page is the short version of how a change gets
from your machine into a release. [docs/development.md](docs/development.md)
has the details behind each step.

- **A security problem?** Do not open an issue. Follow [SECURITY.md](SECURITY.md).
- **A bug or an idea?** Open an issue with the matching template first, so the
  fix is agreed on before anyone writes it.

## Set up

You need Go and git. The exact Go version comes from the `toolchain` line in
`go.mod`, and `go` downloads it on first use; the Makefile pins it through
`GOTOOLCHAIN`, so every command below uses the same compiler CI does.

```bash
git clone https://github.com/obentoo/bentoolkit.git
cd bentoolkit
make build          # build/bentoo and build/bentoo-tray
pre-commit install  # optional: gitleaks, gofmt and go vet on every commit
```

## Make the change

- **One concern per pull request.** A bug fix, a refactor and a dependency
  bump are three pull requests, so each can be reviewed and reverted alone.
- **Branch names** start with the kind of change: `feat/`, `fix/`, `docs/`,
  `refactor/`, `test/`, `ci/`, `lint/`, `deps/`.
- **Commit messages** follow `type(scope): summary`, for example
  `fix(provider): reset to the fetched branch without --end-of-options`. The
  body says why, and what was measured to show it works.
- **English everywhere**: code, comments, commit messages, documentation and
  error messages.

### Code rules the tooling enforces

`make lint` checks all of these; the reasons live next to each rule in
[.golangci.yml](.golangci.yml).

- **Errors are wrapped with `%w`** and carry what was being attempted.
- **Logging goes through `log/slog`**: a constant message plus snake_case
  key/value pairs, never a package-level logger and never `fmt.Print*` in
  library code. Secrets are redacted by the logger, so never format one into
  a message.
- **Network and subprocess calls take a `context.Context`.**
- **A `//nolint` names its linter and says why.**
- **Comments stand on their own**: no planning IDs (`S058`, `R4.4`, "story
  060"), and no comment block of 20 lines or more outside a package doc.
- **Baselines only shrink.** Some rules in `.golangci.yml` carry a list of
  names, such as the package-level globals of `cmd/bentoo`, recording debt
  that existed when the rule was enabled. Remove a name when you fix it; never
  add one.

### Tests

- `make test` runs everything with `-race` and in shuffled order; a failure
  prints the seed to replay it with `make test SHUFFLE=<seed>`.
- Total coverage must stay at or above 80%.
- Tests must not depend on the host: no reads from `/etc` or the real
  `$HOME`, and nothing that only passes as root. The gate runs them as an
  unprivileged user on a clean system.

### Dependencies

Justify a new dependency in the pull request: say what the standard library
or the existing code cannot do. A version must be at least 7 days old before
it is taken; Dependabot enforces that with its cooldown, and a manual bump
must check the release date by hand.

## Before you open the pull request

1. Run `make check` while you work.
2. Commit, then run the full gate on the commit:

   ```bash
   ./scripts/ci-vm-gate.sh
   ```

   It reproduces every CI job on a clean checkout. It is the project's CI:
   GitHub does not rerun these jobs, so the pull request must say which gate
   run it rests on (the commit and the PASS lines).
3. Update `CHANGELOG.md` under `## [Unreleased]` when the change ships
   differently, which is any change to a non-test `.go` file, `go.mod` or
   `go.sum`. If it does not (a test-only fix, a typo in a comment), add the
   `no-changelog` label instead. The format is
   [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), written for the
   person running `bentoo`, not for the code reviewer.
4. Update the page under `docs/` that describes what you changed.

## Releases

Maintainers cut releases: a `docs(changelog): cut X.Y.Z` pull request, an
annotated `vX.Y.Z` tag on its merge commit, and the overlay ebuild bump.
Versioning follows [Semantic Versioning](https://semver.org/).

## Conduct

Be kind, assume good faith, and keep discussion about the work.
