---
story: manifest-fixer-fetch-routing
type: bugfix
scale: full
version: 2
created: 2026-06-22
last-refined: 2026-06-22
history:
  - v1: Initial story (non-canonical format — Requirements/Decisions/Scope sections)
  - v2: Rewritten to the canonical Epic bugfix templates (story + design + tasks)
---

# Bugfix - Manifest Fixer Invoked Unconditionally on Fetch Failures

## Summary

`bentoo overlay autoupdate --apply` invokes the agentic LLM fixer
(`ClaudeCodeFixer.FixManifest`) for **every** failed `pkgdev manifest`, including
the large class where the distfile is dead or moved upstream (HTTP 404 / missing
FTP path) — which no ebuild-editing agent can repair. This burns time and the
Claude subscription quota on unrepairable cases, and gives genuinely recoverable
cases (a renamed release asset) only the generic "repair the ebuild" instruction
instead of a fetch-focused one.

## Reproduction Steps

1. Point an ebuild at a distfile version that upstream never published on any
   mirror (observed: `media-gfx/imagemagick-7.1.2.26`).
2. Run `bentoo overlay autoupdate --apply media-gfx/imagemagick`.
3. Observe: `pkgdev manifest` fails with `failed fetching required distfiles`
   (404 on every mirror), and the applier invokes the LLM fixer anyway — which
   cannot succeed, yet consumes a full (budget-capped) agent run.
4. Contrast: `net-misc/rustdesk` fails the same way but because a GitHub release
   asset was *renamed* — a fetch-focused attempt could fix it, yet it receives the
   same generic instruction.

## Current Behavior (Defect)

WHEN `pkgdev manifest` fails for any reason THEN the system invokes the LLM fixer
unconditionally, with no inspection of why the manifest failed.

WHEN the manifest failure is a dead upstream distfile THEN the system spends a
full agent run (time + subscription quota) on a repair that cannot succeed.

WHEN the manifest failure is a moved/renamed distfile THEN the system gives the
agent the generic "repair the ebuild" instruction with no network-discovery
tools, rather than a fetch-focused one.

## Expected Behavior (Correct)

WHEN `pkgdev manifest` fails AND the raw error carries a pkgcore fetch marker THE
SYSTEM SHALL route the failure by upstream recoverability instead of invoking the
LLM unconditionally.

WHEN the distfile is unobtainable upstream THE SYSTEM SHALL skip the LLM fixer and
fail the apply with an error that names the distfile and wraps a distinct
`ErrFetchUnrecoverable` sentinel.

WHEN an obtainable replacement URL is discovered upstream THE SYSTEM SHALL repair
mechanically — rewrite `SRC_URI` and regenerate the manifest without the LLM —
and SHALL fall back to the fetch-focused LLM fixer only if that mechanical fix
does not pass the authoritative re-check.

WHEN classification cannot reach a verdict (network error, timeout, ambiguous) THE
SYSTEM SHALL invoke the fetch-focused LLM fixer (fail-open).

WHEN the LLM fixer is invoked in fetch mode THE SYSTEM SHALL use a fetch-focused
instruction, a `gh`/`curl`-augmented allowlist built as a copy of the default
(never mutating it), and reduced `--max-turns`/`--max-budget-usd`.

## Unchanged Behavior (Regression Prevention)

WHEN a manifest failure carries no fetch marker THE SYSTEM SHALL CONTINUE TO
invoke the generic LLM fixer (when one is wired) with its current instruction,
allowlist, turns, and budget — exactly as today.

WHEN any fix is attempted THE SYSTEM SHALL CONTINUE TO use the second `pkgdev
manifest` run as the sole authoritative arbiter of recovery, for both the
mechanical and the LLM routes.

WHEN a failure is returned on any LLM path THE SYSTEM SHALL CONTINUE TO wrap
`ErrLLMRequestFailed`, so `errors.Is` and the applier's `(LLM fix attempt failed:
%w)` combination keep working.

WHEN the fixer runs in bare mode THE SYSTEM SHALL CONTINUE TO inject the API key
only via the child environment, so it never appears in argv, logs, or any error
string.

WHEN an apply fails after the ebuild copy THE SYSTEM SHALL CONTINUE TO run the
deferred orphan rollback in `Apply`, including on the new irreparable-skip path.

WHEN a fixer invocation fails THE SYSTEM SHALL CONTINUE TO format the error via
story 009's `formatFixerError` and its truncation discipline, unchanged.

## Constraints

- v1 upstream discovery is **GitHub-releases only**; a non-GitHub `SRC_URI` (e.g.
  `mirror://`) whose URLs are all conclusively gone classifies as irreparable, and
  other providers degrade to inconclusive→LLM. Broader provider discovery is a
  follow-up story.
- No new third-party dependency — the classifier reuses the existing GitHub client
  and HTTP plumbing; only a `ctx`-aware `GetReleaseAssets` is added.
- A `context.Context` with a timeout threads into the discovery HTTP call (Go
  net-I/O rule); tests run `-race`.
- Tooling: no browser E2E tooling applies — Go CLI/library change; coverage is Go
  unit/integration tests via the existing `exec`/HTTP seams.
