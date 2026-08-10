package main

import (
	"fmt"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// llmConfigToAutoupdate converts the CLI-facing LLM config (config.LLMConfig)
// into the autoupdate provider config (autoupdate.LLMConfig).
//
// It lives in package main because cmd/bentoo already imports both packages,
// which keeps the config and autoupdate packages free of a mutual import
// dependency.
//
// Every CLI-reachable field is carried across. BaseURL is intentionally NOT
// mapped: it exists only on autoupdate.LLMConfig and is populated internally
// for HTTP providers (e.g. the Claude endpoint), with no config-side source.
// A field-parity test guards against future config drift (R-config-drift).
func llmConfigToAutoupdate(c config.LLMConfig) autoupdate.LLMConfig {
	return autoupdate.LLMConfig{
		Provider:     c.Provider,
		APIKeyEnv:    c.APIKeyEnv,
		Model:        c.Model,
		Bare:         c.Bare,
		MaxBudgetUSD: c.MaxBudgetUSD,
	}
}

// newConfiguredLLMProvider builds an autoupdate LLM provider from the CLI config.
// Returns (nil, nil) when no provider is configured (Provider == "") — the caller
// proceeds without an LLM. Returns (nil, err) when a provider IS configured but
// construction fails (e.g. claude CLI absent → ErrClaudeCodeUnavailable, unknown
// provider, missing API key) — the caller logs a Warn and falls back. Returns
// (provider, nil) on success.
//
// This helper is shared by the analyze wiring (T4) and the --check wiring (T5),
// so it stays general: the only policy it encodes is the empty-provider
// short-circuit; every other decision (which provider, defaults) lives in
// autoupdate.NewLLMProvider via the existing llmConfigToAutoupdate mapper.
func newConfiguredLLMProvider(c config.LLMConfig) (autoupdate.LLMProvider, error) {
	if c.Provider == "" {
		return nil, nil
	}
	return autoupdate.NewLLMProvider(llmConfigToAutoupdate(c))
}

// newConfiguredManifestFixer builds an LLM manifest fixer from the CLI config for
// the --apply path. The agentic fixer edits ebuild files and runs pkgdev, which
// only the local claude-code CLI agent can do — so it is wired ONLY for
// provider == "claude-code". Every other case returns (nil, nil): no provider
// (Provider == "") or a non-agentic provider simply leaves --apply with its
// original fail-fast manifest behaviour. A configured-but-unconstructable
// claude-code fixer (e.g. the `claude` CLI is absent) returns (nil, err) so the
// caller can Warn and continue.
func newConfiguredManifestFixer(c config.LLMConfig) (autoupdate.ManifestFixer, error) {
	if c.Provider != "claude-code" {
		return nil, nil
	}
	return autoupdate.NewClaudeCodeFixer(llmConfigToAutoupdate(c))
}

// newConfiguredRegistryFixer builds an LLM registry fixer from the CLI config for
// the --check repair prompt. Like newConfiguredManifestFixer it is wired ONLY for
// provider == "claude-code": only the agentic CLI can investigate upstream and edit
// the failed package's packages.toml entry. Every other provider — including the
// empty/unset one and the non-agentic HTTP "claude" — returns a TRUE nil interface
// (nil, nil), never a boxed (*ClaudeCodeRegistryFixer)(nil), so runCheck's
// `fixer != nil` gate stays honest and the repair prompt never appears (AD9 / R7.1).
// A configured-but-unconstructable claude-code fixer (e.g. the `claude` CLI is
// absent) returns (nil, err) so the caller can Warn and continue with no prompt.
func newConfiguredRegistryFixer(c config.LLMConfig) (autoupdate.RegistryFixer, error) {
	if c.Provider != "claude-code" {
		return nil, nil
	}
	return autoupdate.NewClaudeCodeRegistryFixer(llmConfigToAutoupdate(c))
}

// newConfiguredBuildFixer builds the LLM build fixer for the staged compile gate
// (S033-R8.1, R8.2). Repairing a failed build means EDITING the staged ebuild and
// re-running the gate, which only the local claude-code CLI agent can do — so, like
// newConfiguredManifestFixer and newConfiguredRegistryFixer, it is wired ONLY for
// provider == "claude-code". Every other provider — the empty/unset one and the
// non-agentic HTTP "claude" alike — returns a TRUE nil interface (nil, nil) and the
// bump keeps the original fail-fast behaviour.
//
// # Why a true nil and not the pointer the constructor returns
//
// autoupdate.WithApplierBuildFixer gates on `fixer != nil`. A nil
// *ClaudeCodeBuildFixer assigned to the BuildFixer interface is a NON-nil interface
// holding a nil pointer, so it walks straight through that gate and the applier
// calls a nil receiver on the first failed build — the capability looks enabled and
// is not. Returning a bare `nil` on both the short-circuit and the error path is
// what keeps the caller's gate honest (see the same note at the registry fixer
// above, llm_wiring.go:66-68).
//
// A configured-but-unconstructable claude-code fixer (typically: no `claude` on
// PATH) returns (nil, err) so the caller can Warn and continue — the precedent set
// by applierFixerOption (overlay_autoupdate.go:1251). The variadic options exist so
// the caller can pass the operator's `autoupdate.validate.timeout` through
// WithBuildFixerTimeout, which is that key's only consumer on the fixer side.
func newConfiguredBuildFixer(c config.LLMConfig, opts ...autoupdate.BuildFixerOption) (autoupdate.BuildFixer, error) {
	if c.Provider != "claude-code" {
		return nil, nil
	}
	fixer, err := autoupdate.NewClaudeCodeBuildFixer(llmConfigToAutoupdate(c), opts...)
	if err != nil {
		// Discard the returned pointer deliberately: it is nil, and boxing it into
		// the interface is the exact bug documented above.
		return nil, fmt.Errorf("build fixer: %w", err)
	}
	return fixer, nil
}

// newConfiguredBumpReviewer builds the LLM bump reviewer that reads the difference
// between two versions' upstream build declarations and may ask for MORE validation
// than the depth policy chose (S033-R7, R7.5).
//
// It repeats newConfiguredBuildFixer's discipline to the letter — claude-code only,
// a true nil for everything else, a returned error rather than a fatal one — and is
// deliberately a SEPARATE function rather than a generic helper: the typed-nil trap
// is per-constructor (each has its own concrete pointer type to box), so the two
// constructors are tested separately and read separately.
//
// The variadic options carry `autoupdate.validate.timeout` in through
// WithBumpReviewerTimeout, the key's only consumer on the review side.
func newConfiguredBumpReviewer(c config.LLMConfig, opts ...autoupdate.BumpReviewerOption) (autoupdate.BumpReviewer, error) {
	if c.Provider != "claude-code" {
		return nil, nil
	}
	reviewer, err := autoupdate.NewClaudeCodeBumpReviewer(llmConfigToAutoupdate(c), opts...)
	if err != nil {
		return nil, fmt.Errorf("bump reviewer: %w", err)
	}
	return reviewer, nil
}

// llmCapabilities answers which of the two LLM capabilities a run has asked for:
// the bump reviewer and the build fixer, in that order.
//
// The rule is one flag and two subtractions (S033-R7.1, R7.2):
//
//   - --llm enables BOTH. The operator should not have to learn two names to turn
//     the feature on, and the flag is what records their consent to the cost.
//   - `autoupdate.validate.review: false` or `fix_on_failure: false` switches that
//     ONE back off, independently of the other. This is how a host that wants the
//     reading but not the edits (or the reverse) says so.
//   - Configuration NEVER adds. `review: true` in a file does not spend money on a
//     run where nobody typed --llm, which is why the effective answer reads
//     ReviewDisabled/FixOnFailureDisabled — the subtract signals — and not
//     GetReview/GetFixOnFailure, which report the configured value only.
//
// The two keys are tri-state (*bool) for exactly this: with a plain bool "unset"
// and "false" decode identically, --llm could never be subtracted from, and the
// keys would be inert.
func llmCapabilities(llm bool, v config.ValidateConfig) (review, fix bool) {
	if !llm {
		return false, false
	}
	return !v.ReviewDisabled(), !v.FixOnFailureDisabled()
}

// applierLLMOptions turns the --llm flag and the two config switches into the
// Applier options that actually enable the capabilities (S033-R7.1, R7.2). It is
// the only place the flag is read into an Applier, so --apply and --apply all
// cannot drift apart on what --llm meant.
//
// A construction failure is a WARNING, never fatal — applierFixerOption's
// precedent (overlay_autoupdate.go:1251) and the only defensible reading of the
// feature: on a host with no `claude` CLI, --llm degrades the run to the same apply
// it would have done anyway rather than refusing to publish a bump that is fine.
// The failed capability is simply not appended, which leaves the Applier in the
// state it would have had if the flag were absent — "no LLM was asked for" and "the
// LLM could not be built" produce the same, predictable run.
//
// Both agents get their per-invocation budget from `autoupdate.validate.timeout`
// via GetTimeout, which is that key's entire reason to exist.
func applierLLMOptions(llm bool, llmCfg config.LLMConfig, v config.ValidateConfig) []autoupdate.ApplierOption {
	review, fix := llmCapabilities(llm, v)
	opts := make([]autoupdate.ApplierOption, 0, 2)

	// One key, one budget, both agents — read once so the two option calls below
	// cannot drift onto different sources.
	budget := v.GetTimeout()

	if review {
		reviewer, err := newConfiguredBumpReviewer(llmCfg, autoupdate.WithBumpReviewerTimeout(budget))
		switch {
		case err != nil:
			logger.Warn("LLM bump reviewer unavailable; this run validates to the depth the policy chose: %v", err)
		case reviewer != nil:
			opts = append(opts, autoupdate.WithApplierBumpReviewer(reviewer))
		default:
			logger.Debug("--llm: provider %q cannot review bumps; only claude-code can", llmCfg.Provider)
		}
	}

	if fix {
		fixer, err := newConfiguredBuildFixer(llmCfg, autoupdate.WithBuildFixerTimeout(budget))
		switch {
		case err != nil:
			logger.Warn("LLM build fixer unavailable; a failed build stays failed: %v", err)
		case fixer != nil:
			opts = append(opts, autoupdate.WithApplierBuildFixer(fixer))
		default:
			logger.Debug("--llm: provider %q cannot fix builds; only claude-code can", llmCfg.Provider)
		}
	}

	return opts
}
