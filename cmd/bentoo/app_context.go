package main

import (
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// appContext holds shared CLI dependencies loaded once per command invocation.
type appContext struct {
	Config      *config.Config
	OverlayPath string
}

// loadAppContext loads config and validates the overlay path.
// Use for commands that require a valid, existing overlay directory.
func loadAppContext() (*appContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}
	return &appContext{Config: cfg, OverlayPath: overlayPath}, nil
}

// loadAppContextNoValidation loads config and resolves the overlay path
// without validating the overlay directory structure.
// Use for commands like analyze and autoupdate that work with unconfigured overlays.
func loadAppContextNoValidation() (*appContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	overlayPath, err := cfg.GetOverlayPathNoValidation()
	if err != nil {
		return nil, err
	}
	return &appContext{Config: cfg, OverlayPath: overlayPath}, nil
}
