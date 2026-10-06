package llm

import (
	"log/slog"
	"testing"
)

// captureInfoLogs returns a capture of Info-level records (the logCapture type
// is defined in httpclient_test.go); the test injects lc.logger() into the
// component it exercises.
func captureInfoLogs(t *testing.T) *logCapture {
	t.Helper()
	return &logCapture{level: slog.LevelInfo}
}
