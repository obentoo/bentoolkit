---
story: manifest-fixer-fetch-routing
type: bugfix
scale: full
version: 2
created: 2026-06-22
last-refined: 2026-06-22
history:
  - v1: Initial design (feature-style sections + ad-hoc "Review Resolutions")
  - v2: Rewritten to the canonical Epic bugfix-design template
---

# Design - Bugfix: Manifest Fixer Fetch Routing

## Root Cause Analysis

- **Symptom:** `runManifestWithFix` (`internal/autoupdate/applier.go:760`) calls
  `a.fixer.FixManifest(...)` on every manifest failure (`:789`), so a permanently
  404 distfile triggers an agent run that cannot succeed, and a renamed asset gets
  the generic instruction.
- **Root cause:** there is **no classification step** between the first
  `runManifest` failure (`:761`) and the fixer call (`:789`). "Manifest failed" is
  treated as a single, uniformly LLM-repairable condition, so a dead distfile, a
  moved distfile, and a non-fetch error (digest/EAPI) are indistinguishable.
- **Why it wasn't caught:** the capability to probe distfile availability never
  existed — the `Checker` resolves upstream *versions* from a `PackageConfig` and
  returns provider API endpoints (`checker.go`, `discovery.go`) with **no** distfile
  HEAD/availability check anywhere, and the `Applier` holds no `Checker`. Story 009
  improved the fixer's *diagnostics* but explicitly left *when it is invoked* out
  of scope.

## Affected Components

| Component | Role in Bug | Change Needed |
|---|---|---|
| `internal/autoupdate/applier.go` — `runManifestWithFix` | invokes the fixer unconditionally | insert detection → classify → route |
| `internal/autoupdate/applier.go` (additions) | — | `isFetchFailure`, `ErrFetchUnrecoverable` sentinel, `rewriteSrcURI`, `WithApplierFetchClassifier`, `FixMethod` field on `ApplyResult` |
| `internal/autoupdate/fetch_classifier.go` (new) | — | the classifier: manifest-output availability parser + GitHub discovery → `FetchVerdict` |
| `internal/autoupdate/manifest_fixer.go` | one instruction/allowlist for all failures | fetch-mode instruction + args; `FixMode`; `ManifestFixRequest.{Mode,DiscoveredURL}` |
| `internal/common/github/client.go` | no release-asset-by-tag call | `GetReleaseAssets(ctx, owner, repo, tag)` |
| `cmd/bentoo/overlay_autoupdate.go` | no classifier wired | `newConfiguredFetchClassifier` + `WithApplierFetchClassifier` in `runApply`/`runApplyAll` |

## Fix Approach

Insert a classify-and-route stage that runs **only** for fetch failures; every
other failure stays on today's path verbatim.

1. **Detect (raw error).** `isFetchFailure(rawManifestErr)` matches the pkgcore
   markers (`failed fetching required distfiles`, `failed fetching files for
   package`, `failed fetching file:`) as tolerant substrings on the **untruncated**
   error (`truncateManifestError` middle-elides at 16 KiB; the marker lives in the
   `Output:` tail). No marker → today's generic fixer (unchanged).

2. **Classify from the manifest output, not a live re-probe.** The first
   `pkgdev manifest` already expanded `mirror://`, tried every URL, and logged each
   result into the error (`"command failed: %w\nOutput: %s"`, `applier.go:921`).
   The classifier parses `(url, status)` pairs from that output — more faithful and
   cheaper than re-probing, and it avoids re-implementing `mirror://` expansion.
   Conservative rule (favors fail-open): **gone** only when *all* recognized URLs
   are explicit not-found (`404`/`410`/`No such file`) AND none are transient
   (`5xx`/`timed out`/`unable to resolve`/`Connection refused`); any transient or
   unparseable status, or zero parseable URLs → **inconclusive**.

3. **Discover replacement (GitHub-releases only, v1).** When availability is
   `gone`, derive `owner`/`repo`/`tag`/`CurrentURL` by **parsing the gone URL**
   (`github.com/<owner>/<repo>/releases/download/<tag>/<file>`) and list that tag's
   assets via `GetReleaseAssets`. A compatible renamed asset (same extension,
   nearest-name) → **recoverable** with `DiscoveredURL`. Error mapping keyed on the
   existing github sentinels: `errors.Is(err, github.ErrNotFound)` (tag/repo 404)
   or tag-with-no-compatible-asset → **irreparable**; `github.ErrRateLimit`/
   `github.ErrAPIError`/network/`ctx` cancellation → **inconclusive**. A non-GitHub
   gone URL has no v1 discovery → **irreparable**.

4. **Route by verdict.**
   - `irreparable` → **skip the LLM**, return `fmt.Errorf("%w: %s (%s)",
     ErrFetchUnrecoverable, reason, pkg)`; the deferred orphan rollback in `Apply`
     still fires (the skip returns a failed result).
   - `recoverable` → **mechanical** repair: `rewriteSrcURI` then `runManifest`; on
     pass, record `FixMethod="mechanical"` and return success (no LLM); on failure,
     fall through to the fetch-focused LLM.
   - `inconclusive` → fetch-focused LLM (fail-open).

5. **Mechanical rewrite.** `rewriteSrcURI(ebuildPath, oldURL, newURL)` mirrors the
   anchored `regexp.QuoteMeta` + write-`0o600` + **error-if-unchanged** discipline of
   `substituteAuxVar`/`substituteCommitHash` (`applier.go:693-743`); multi-line
   `SRC_URI` aware. Regeneration reuses the existing `runManifest` (no hand-edited
   Manifest — thin-manifest invariant).

6. **Fetch-mode fixer.** `ManifestFixRequest` gains `Mode (FixModeGeneric|FixModeFetch)`
   and `DiscoveredURL`. In `FixModeFetch`: a fetch-focused instruction (verify
   upstream, correct `SRC_URI`/version only, never edit build logic, never invent
   URLs, give up with a one-line report if no distfile exists; include the
   `DiscoveredURL` hint); an allowlist built as
   `append(append([]string{}, manifestFixAllowedTools...), "Bash(gh *)", "Bash(curl *)")`
   (a copy — the package slice is never mutated); `--max-turns
   manifestFixFetchMaxTurns` (12, vs 30) and `--max-budget-usd` × 0.5 when set.

7. **Verdict zero-value = fail-open.** `Classify` returns a `FetchVerdict` and **no
   error**: every internal failure folds into the zero value `FetchInconclusive`,
   so fail-open is structural, not a branch that can be forgotten.

8. **Recording the fix method.** `ApplyResult` gains `FixMethod string`
   (`""`|`"llm"`|`"mechanical"`); the `Fixed`/`FixSummary` doc comments are updated
   to say a fix may be mechanical OR LLM, so a mechanical fix is distinguishable.

Key interfaces:

```go
// fetch_classifier.go
type FetchVerdictKind int
const ( FetchInconclusive FetchVerdictKind = iota; FetchIrreparable; FetchRecoverable )
type FetchVerdict struct { Kind FetchVerdictKind; CurrentURL, DiscoveredURL, Reason string }
type FetchClassifyRequest struct { Package, Version, EbuildPath, ManifestError string }
type FetchClassifier interface { Classify(ctx context.Context, req FetchClassifyRequest) FetchVerdict }

// applier.go
var ErrFetchUnrecoverable = errors.New("fetch failure: no obtainable distfile upstream")
func WithApplierFetchClassifier(c FetchClassifier) ApplierOption
func isFetchFailure(rawManifestErr string) bool
func (a *Applier) rewriteSrcURI(ebuildPath, oldURL, newURL string) error

// manifest_fixer.go
type FixMode int
const ( FixModeGeneric FixMode = iota; FixModeFetch )
const ( manifestFixFetchMaxTurns = 12; manifestFixFetchBudgetFactor = 0.5 )

// github/client.go
type ReleaseAsset struct { Name, DownloadURL string }
func (c *Client) GetReleaseAssets(ctx context.Context, owner, repo, tag string) ([]ReleaseAsset, error)
```

Reporter stage sequence per branch: irreparable → `fetch-classify`; inconclusive
→ `fetch-classify`→`llm-fix`→`re-check`; recoverable-success →
`fetch-classify`→`mech-fix`; recoverable-fallback →
`fetch-classify`→`mech-fix`→`llm-fix`→`re-check`.

## Regression Test Strategy

- **Bug verification test:** a scripted `pkgdev manifest` that fails with `failed
  fetching required distfiles` + a classifier verdict of `irreparable` → the fixer
  is **not** invoked and the error wraps `ErrFetchUnrecoverable` (fails before the
  fix, passes after).
- **Recoverable + fail-open tests:** `recoverable` → mechanical rewrite + passing
  re-check (no LLM, `FixMethod=="mechanical"`); `recoverable` with failing re-check
  → fetch-focused LLM; `inconclusive`/cancellation → fetch-focused LLM (fail-open).
- **Regression tests (Unchanged Behavior):** no-marker failure → generic fixer
  unchanged; every LLM route still `errors.Is(ErrLLMRequestFailed)`;
  `manifestFixAllowedTools` unmutated; no API key in any error string; orphan
  rollback fires on the irreparable skip; bare-mode key invariant intact.
- **Existing test impact:** existing applier/fixer tests run with a **nil**
  classifier (default), so they behave exactly as today — no updates expected.

## Side Effects Assessment

| Potential Side Effect | Risk Level | Mitigation |
|---|---|---|
| Output-parser coupling to fetcher log text | Medium | Conservative rule: any unrecognized/transient status → `inconclusive` (fail-open); marker set centralized + table-tested; a new fetcher format never yields a false `irreparable`. |
| False `irreparable` for a non-GitHub package whose tarball merely moved | Medium | Requires *all* official URLs to be conclusively 404 first; a live/transient mirror → `inconclusive`→LLM. Accepted v1 limit, documented as a Constraint. |
| GitHub rate-limit/403 on discovery in the hot apply path | Low | Reuse the client's rate-limit/token plumbing; a 403/limit → `inconclusive` (fail-open), never a crash or false verdict. |
| Mechanical rewrite corrupts the ebuild | Low | Anchored `QuoteMeta` + error-if-unchanged; the authoritative `runManifest` re-check gates it; a bad rewrite fails re-check → LLM fallback; orphan rollback removes a half-applied ebuild. |
| Context cancellation mid-classify/rewrite | Low | `Classify` derives from `a.ctx` and maps cancellation to `inconclusive`; `rewriteSrcURI` is a local file op; re-check honors `a.ctx` as today. |
