package main

import "log/slog"

// discardLog is the logger unit tests hand to helpers that take the
// invocation's logger (story 062) when the test does not read diagnostics.
func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }
