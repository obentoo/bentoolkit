package main

import (
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
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
