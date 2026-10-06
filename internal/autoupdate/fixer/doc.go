// Package fixer holds autoupdate's agent fixers: LLM agents, run through the
// Claude Code CLI with the least-privilege profile from llm, that repair a
// failed bump — the manifest fixer, the build fixer, the bump reviewer and the
// registry fixer. It imports llm, registry and validate, never the update core;
// the registry-fix transaction that re-checks a package stays in the core
// because it drives the Checker (story 061).
package fixer
