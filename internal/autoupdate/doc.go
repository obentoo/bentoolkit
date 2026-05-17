// Package autoupdate provides automated version checking and update application
// for ebuilds in the Bentoo overlay.
//
// The package implements:
//   - Package configuration management via TOML files
//   - Version parsing from upstream sources (JSON, regex, LLM)
//   - Cache management for version query results
//   - Pending updates tracking and application
//
// Configuration is read from overlay/.autoupdate/packages.toml which defines
// how to check upstream versions for each package. Local state is maintained
// in ~/.config/bentoo/autoupdate/ for caching and pending updates.
//
// # Response body size cap
//
// Every HTTP response body is bounded so an oversized or malicious response
// cannot exhaust memory. RetryableHTTPClient.GetWithContext wraps the response
// body in an http.MaxBytesReader capped at httputil.MaxBodyBytes (10 MiB); a
// read that exceeds the cap surfaces as an error wrapping ErrResponseTooLarge.
//
// The LLM clients (ClaudeClient, OpenAIClient, OllamaClient) apply the same
// 10 MiB default to their API response bodies, but the limit is per-client and
// can be raised with each client's WithMaxBodyBytes option because legitimate
// LLM responses (notably from a local Ollama instance) can exceed 10 MiB.
//
// Usage:
//
//	checker, err := autoupdate.NewChecker(overlayPath)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	batch := checker.CheckAll(false)
//	// batch.Items holds the successful CheckResults; batch.Failures maps a
//	// package name to the error that occurred; batch.ExitCode() yields the
//	// 0/1/2 process exit code.
package autoupdate

import (
	// Import TOML library for configuration parsing
	_ "github.com/BurntSushi/toml"
)
