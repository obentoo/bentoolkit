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
// Usage:
//
//	checker, err := autoupdate.NewChecker(overlayPath)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	results, err := checker.CheckAll(false)
package autoupdate

import (
	// Import TOML library for configuration parsing
	_ "github.com/BurntSushi/toml"
)
