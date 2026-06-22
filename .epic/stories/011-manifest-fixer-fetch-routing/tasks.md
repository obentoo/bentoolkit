---
story: manifest-fixer-fetch-routing
type: bugfix
scale: full
version: 2
created: 2026-06-22
last-refined: 2026-06-22
history:
  - v1: Initial tasks (non-canonical — ## Tn / ### N.N headings, EARS/Covered-by fields)
  - v2: Rewritten to the canonical tasks template (Task List, metadata line, Objective/ToDo/Tests/Validation)
---

# Implementation Plan - Bugfix: Manifest Fixer Fetch Routing

## Overview

Insert a classify-and-route stage into `runManifestWithFix` between the first
`pkgdev manifest` failure and the fixer call: detect fetch failures, classify them
against upstream (irreparable / recoverable / inconclusive), then skip, mechanically
repair, or invoke a fetch-focused LLM. Built bottom-up — detector + sentinel,
GitHub discovery, the classifier, the mechanical rewrite, and the fetch-mode fixer
land independently, then the applier routing wires them together and `cmd/bentoo`
constructs the classifier. Tests drive scripted `exec`/HTTP seams; no real
`claude`, `pkgdev`, `gh`, or network call runs. The authored Red tests already
live under `.draft/authored-tests/`.

## Task List

- [x] 1 - Fetch-failure detection and sentinel
  - _Complexity: Simple | Tests: Unit | Risks: None | Dependencies: None_
  - Objective: Recognize a fetch-failure manifest error and add a distinct sentinel for the skip path

  - [x] 1.1 - `isFetchFailure` detector + `ErrFetchUnrecoverable` sentinel
    - Context:
      - Files: `internal/autoupdate/applier.go` (sentinel block near `ErrManifestFailed`; `runManifestWithFix`)
    - Objective: Detect pkgcore fetch markers on the raw error and declare the skip sentinel
    - ToDo: Add `isFetchFailure(rawManifestErr string) bool` matching `failed fetching required distfiles`, `failed fetching files for package`, `failed fetching file:` as tolerant substrings on the untruncated error. Add `var ErrFetchUnrecoverable = errors.New("fetch failure: no obtainable distfile upstream")` to the sentinel block.
    - Tests: Unit · `internal/autoupdate/applier_fix_test.go` — each marker present → true; non-fetch error (digest/EAPI) → false; marker in a multi-KiB Output dump → true; empty → false
    - Validation: `go test ./internal/autoupdate/ -run FetchFailure` passes
    - Requirements: Expected Behavior (route by recoverability); Current Behavior
    - Commit: "feat(autoupdate): detect fetch-failure manifest errors + sentinel"

- [x] 2 - GitHub release asset discovery
  - _Complexity: Simple | Tests: Integration | Risks: GitHub rate-limit | Dependencies: None_
  - Objective: List a release tag's assets so the classifier can find a renamed distfile

  - [x] 2.1 - `GetReleaseAssets(ctx, owner, repo, tag)`
    - Context:
      - Files: `internal/common/github/client.go` (auth/rate-limit/sentinels to reuse)
    - Objective: Return a tag's assets, threading context, reusing existing error sentinels
    - ToDo: Add `type ReleaseAsset struct { Name, DownloadURL string }` and `func (c *Client) GetReleaseAssets(ctx context.Context, owner, repo, tag string) ([]ReleaseAsset, error)` hitting `GET /repos/{owner}/{repo}/releases/tags/{tag}`, mapping `assets[].{name,browser_download_url}`. Use `http.NewRequestWithContext`; on non-2xx return the existing sentinels (`ErrNotFound` on 404, `ErrRateLimit`/`ErrAPIError` otherwise) — handle the request error, do not ignore it.
    - Tests: Integration · `internal/common/github/client_test.go` — release with assets → names+URLs; tag 404 → `ErrNotFound`; 403 → `ErrRateLimit`; malformed JSON → error
    - Validation: `go test ./internal/common/github/ -run ReleaseAssets` passes
    - Requirements: Expected Behavior (discover obtainable replacement URL)
    - Commit: "feat(github): list release assets by tag (ctx-aware)"

- [x] 3 - Fetch classifier
  - _Complexity: Moderate | Tests: Unit + Integration | Risks: manifest-output format coupling | Dependencies: Task 2_
  - Objective: Turn a fetch failure into an irreparable / recoverable / inconclusive verdict, fail-open by construction

  - [x] 3.1 - Verdict types + availability parser
    - Context:
      - Files: `internal/autoupdate/fetch_classifier.go` (new)
    - Objective: Define the verdict surface and parse upstream availability from the manifest output
    - ToDo: Add `FetchVerdictKind` (`FetchInconclusive`=iota, `FetchIrreparable`, `FetchRecoverable`), `FetchVerdict{Kind,CurrentURL,DiscoveredURL,Reason}`, `FetchClassifyRequest{Package,Version,EbuildPath,ManifestError}`, `FetchClassifier` interface (`Classify(ctx,req) FetchVerdict` — no error). Parse `(url,status)` pairs from the `Output:` section; apply the conservative rule — all recognized URLs not-found AND none transient → `gone`; any transient/unparseable or zero URLs → `inconclusive`.
    - Tests: Unit · `internal/autoupdate/fetch_classifier_test.go` — `FetchVerdict{}.Kind == FetchInconclusive` (fail-open zero-value); all-404 dump → `gone`; `timed out`/`5xx`/`200`/unrecognized → `inconclusive`
    - Validation: `go test ./internal/autoupdate/ -run Classif` passes
    - Requirements: Expected Behavior (classify into 3 states); Unchanged Behavior (fail-open never regresses today's cases)

  - [x] 3.2 - `httpFetchClassifier.Classify` (discovery → verdict)
    - Context:
      - Files: `internal/autoupdate/fetch_classifier.go`
    - Objective: Compose availability with GitHub discovery into the final verdict
    - ToDo: Derive `owner`/`repo`/`tag`/`CurrentURL` by parsing the gone `releases/download/<tag>/<file>` URL. On `gone`: non-GitHub → `Irreparable`; GitHub + compatible renamed asset → `Recoverable`; GitHub + `errors.Is(err, github.ErrNotFound)` or no compatible asset → `Irreparable`; `github.ErrRateLimit`/`ErrAPIError`/network/ctx-cancel → `Inconclusive`. Pass `ctx` into `GetReleaseAssets` and handle its error per the mapping.
    - Tests: Integration · `internal/autoupdate/fetch_classifier_test.go` — non-GitHub gone → Irreparable; renamed asset → Recoverable (carries CurrentURL+DiscoveredURL); no asset / tag-404 → Irreparable; listing error → Inconclusive; cancelled ctx → Inconclusive
    - Validation: `go test ./internal/autoupdate/ -run Classify -race` passes
    - Requirements: Expected Behavior (recoverable/irreparable/inconclusive; cancellable probe)

  - [x] 3.3 - Commit
    - Validation: All tests from 3.1 and 3.2 pass
    - Commit: "feat(autoupdate): fetch classifier (output parse + github discovery)"

- [x] 4 - Mechanical SRC_URI rewrite
  - _Complexity: Simple | Tests: Unit | Risks: None | Dependencies: None_
  - Objective: Repair a moved distfile deterministically, without the LLM

  - [x] 4.1 - `rewriteSrcURI`
    - Context:
      - Files: `internal/autoupdate/applier.go` (`substituteAuxVar`/`substituteCommitHash` as the pattern)
    - Objective: Replace the failing URL with the discovered one in the ebuild's SRC_URI
    - ToDo: Add `func (a *Applier) rewriteSrcURI(ebuildPath, oldURL, newURL string) error` — read the file, anchored `regexp.QuoteMeta(oldURL)` replace (multi-line SRC_URI aware), write `0o600`, return an error if nothing changed (drives the LLM fallback). Handle the read/write errors.
    - Tests: Unit · `internal/autoupdate/applier_fix_test.go` — single-line rewritten; multi-line rewrites the right URL only; URL absent → error; regex-special chars handled; bytes outside the URL untouched
    - Validation: `go test ./internal/autoupdate/ -run RewriteSrcURI` passes
    - Requirements: Expected Behavior (mechanical repair); Expected Behavior (fallback when it cannot apply)
    - Commit: "feat(autoupdate): mechanical SRC_URI rewrite for recoverable fetches"

- [x] 5 - Fetch mode in the fixer
  - _Complexity: Simple | Tests: Unit | Risks: None | Dependencies: None_
  - Objective: Give the LLM a fetch-focused instruction, network tools, and reduced caps

  - [x] 5.1 - Fetch-focused instruction
    - Context:
      - Files: `internal/autoupdate/manifest_fixer.go` (`buildFixInstruction`)
    - Objective: Branch the instruction on a new fetch mode
    - ToDo: Add `FixMode` (`FixModeGeneric`=iota, `FixModeFetch`) and `ManifestFixRequest.{Mode FixMode, DiscoveredURL string}`. In `buildFixInstruction`, `FixModeFetch` → fetch-focused text (verify upstream, correct SRC_URI/version only, never edit build logic, never invent URLs, give up with a one-line report if no distfile; include the `DiscoveredURL` hint when set); `FixModeGeneric` → byte-for-byte the current instruction.
    - Tests: Unit · `internal/autoupdate/manifest_fixer_test.go` — fetch instruction has the fetch-focus directives + the hint when set; generic instruction unchanged
    - Validation: `go test ./internal/autoupdate/ -run FetchInstruction` passes
    - Requirements: Expected Behavior (fetch-focused instruction); Unchanged Behavior (generic path unchanged)

  - [x] 5.2 - Fetch-mode args (allowlist + caps)
    - Context:
      - Files: `internal/autoupdate/manifest_fixer.go` (`buildFixArgs`; `manifestFixAllowedTools`)
    - Objective: Add network tools and reduced limits without mutating shared state
    - ToDo: Add `manifestFixFetchMaxTurns = 12` and `manifestFixFetchBudgetFactor = 0.5`. In `buildFixArgs`, `FixModeFetch` → allowlist `append(append([]string{}, manifestFixAllowedTools...), "Bash(gh *)", "Bash(curl *)")` (a copy — never mutate the package slice); `--max-turns manifestFixFetchMaxTurns`; `--max-budget-usd` = default × factor when `maxBudgetUSD > 0`.
    - Tests: Unit · `internal/autoupdate/manifest_fixer_test.go` — fetch args include `Bash(gh *)`/`Bash(curl *)`; global `manifestFixAllowedTools` unchanged after the call; `--max-turns` is 12; reduced budget when configured; generic args unchanged
    - Validation: `go test ./internal/autoupdate/ -run FetchArgs` passes
    - Requirements: Expected Behavior (gh/curl allowlist, reduced caps); Unchanged Behavior (generic path unchanged; allowlist invariant)

  - [x] 5.3 - Commit
    - Validation: All tests from 5.1 and 5.2 pass
    - Commit: "feat(autoupdate): fetch-mode fixer (focused instruction, gh/curl, reduced caps)"

- [x] 6 - Route manifest fetch-failures in the applier
  - _Complexity: Moderate | Tests: Integration | Risks: orphan rollback on a new pre-fixer early return | Dependencies: Task 1, Task 3, Task 4, Task 5_
  - Objective: Wire detection → classify → skip / mechanical / focused-LLM, preserving every invariant

  - [x] 6.1 - `runManifestWithFix` routing + `FixMethod`
    - Context:
      - Files: `internal/autoupdate/applier.go` (`runManifestWithFix`, `ApplyResult`)
    - Objective: Insert the routing and record how a recovery happened
    - ToDo: Add `a.fetchClassifier FetchClassifier` + `WithApplierFetchClassifier`, and `FixMethod string` on `ApplyResult` (update the `Fixed`/`FixSummary` doc comments to allow mechanical OR LLM). In `runManifestWithFix`: nil classifier OR `!isFetchFailure` → current generic path; else `Classify` → `Irreparable` return `%w(ErrFetchUnrecoverable)` (no fixer); `Recoverable` → `rewriteSrcURI` + `runManifest`, on pass set `FixMethod="mechanical"`, else fall to fetch-mode fixer; `Inconclusive` → fetch-mode fixer. Emit reporter stages `fetch-classify`/`mech-fix`. Keep the authoritative re-check.
    - Tests: Integration · `internal/autoupdate/applier_fix_test.go` — nil classifier & non-fetch → generic fixer; Irreparable → fixer not invoked, `errors.Is(ErrFetchUnrecoverable)`, not `ErrLLMRequestFailed`, ebuild rolled back; Recoverable+pass → success, `FixMethod=="mechanical"`, no fixer; Recoverable+fail & Inconclusive → fetch-mode fixer; every LLM route `errors.Is(ErrLLMRequestFailed)`; no API key in any error
    - Validation: `go test ./internal/autoupdate/ -run Routing -race` passes
    - Requirements: Expected Behavior (route/skip/mechanical/fail-open); Unchanged Behavior (authoritative re-check, ErrLLMRequestFailed, orphan rollback, generic path)
    - Commit: "feat(autoupdate): route manifest fetch-failures by upstream recoverability"

- [x] 7 - Wire the classifier into `--apply`
  - _Complexity: Simple | Tests: Unit | Risks: None | Dependencies: Task 3, Task 6_
  - Objective: Construct and inject the classifier in production, degrading safely when unavailable

  - [x] 7.1 - `newConfiguredFetchClassifier` + wiring
    - Context:
      - Files: `cmd/bentoo/overlay_autoupdate.go` (`applierFixerOption` as the pattern; `runApply`/`runApplyAll`)
    - Objective: Build the classifier from config and pass it into the applier
    - ToDo: Add `newConfiguredFetchClassifier(...)` constructing an `httpFetchClassifier` from the GitHub token/config (mirroring `applierFixerOption`); pass `WithApplierFetchClassifier(...)` in `runApply`/`runApplyAll`. On unavailable config, log a Warn and pass `nil` so apply degrades to today's behavior. Handle the constructor error.
    - Tests: Unit · `cmd/bentoo/overlay_autoupdate_test.go` — valid config → non-nil classifier; unconfigured → nil (logged); the option is in the built option set
    - Validation: `go test ./cmd/bentoo/ -run FetchClassifier` passes
    - Requirements: Expected Behavior (routing active in --apply); Unchanged Behavior (nil classifier = today's behavior)
    - Commit: "feat(autoupdate): wire fetch classifier into --apply"

## Quality Gates

- [x] Bug verification test passes: an `irreparable` fetch failure skips the LLM and returns `ErrFetchUnrecoverable` (fails before the fix, passes after)
- [x] Recoverable path: mechanical rewrite + passing re-check yields success with `FixMethod=="mechanical"` and no LLM call
- [x] Fail-open path: `inconclusive`/cancellation and a failed mechanical re-check both reach the fetch-mode LLM fixer
- [x] Regression: no-marker failure → generic fixer unchanged; every LLM route is `errors.Is(err, ErrLLMRequestFailed)`; `errors.Is(err, ErrFetchUnrecoverable)` is distinct
- [x] Regression: `manifestFixAllowedTools` never mutated; no error string contains the API key; orphan rollback fires on the irreparable skip; bare-mode key invariant intact
- [x] `go test ./internal/autoupdate/ ./internal/common/github/ ./cmd/bentoo/ -race` green; `go vet ./...` clean; `staticcheck ./...` clean (or noted skipped)
- [x] Existing applier/fixer tests pass unchanged with a nil classifier (today's behavior)
