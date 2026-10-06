// Package llm holds autoupdate's LLM providers: the OpenAI and Ollama HTTP
// clients and the Claude Code client that drives the local `claude` CLI, the
// least-privilege permission profile every agent run is given (story 051), and
// the classification of how a `claude` run ended.
//
// It imports ebuilds and the common packages only; the agent fixers build on it
// (story 061).
package llm
