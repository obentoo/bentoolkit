package logger

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// TestVerboseModeShowsDebugMessages tests that --verbose shows debug messages
// _Requirements: 3.3_
func TestVerboseModeShowsDebugMessages(t *testing.T) {
	// Create a new logger with a buffer to capture output
	buf := new(bytes.Buffer)
	log := &Logger{
		level:  LevelInfo, // Default level
		output: buf,
	}

	// Debug should not appear at Info level
	log.Debug("debug message before verbose")
	if strings.Contains(buf.String(), "debug message before verbose") {
		t.Error("Debug message should not appear at Info level")
	}

	// Enable verbose mode
	log.SetVerbose(true)

	// Debug should now appear
	log.Debug("debug message after verbose")
	if !strings.Contains(buf.String(), "debug message after verbose") {
		t.Error("Debug message should appear when verbose is enabled")
	}
}

// TestQuietModeSuppressesInfoMessages tests that --quiet suppresses info messages
// _Requirements: 3.4_
func TestQuietModeSuppressesInfoMessages(t *testing.T) {
	// Create a new logger with a buffer to capture output
	buf := new(bytes.Buffer)
	log := &Logger{
		level:  LevelInfo, // Default level
		output: buf,
	}

	// Info should appear at Info level
	log.Info("info message before quiet")
	if !strings.Contains(buf.String(), "info message before quiet") {
		t.Error("Info message should appear at Info level")
	}

	// Clear buffer
	buf.Reset()

	// Enable quiet mode
	log.SetQuiet(true)

	// Info should not appear in quiet mode
	log.Info("info message after quiet")
	if strings.Contains(buf.String(), "info message after quiet") {
		t.Error("Info message should not appear when quiet is enabled")
	}

	// Error should still appear in quiet mode
	log.Error("error message in quiet mode")
	if !strings.Contains(buf.String(), "error message in quiet mode") {
		t.Error("Error message should appear even in quiet mode")
	}
}

// TestLogLevelHierarchy tests that log levels work correctly
func TestLogLevelHierarchy(t *testing.T) {
	tests := []struct {
		name        string
		level       Level
		expectDebug bool
		expectInfo  bool
		expectWarn  bool
		expectError bool
	}{
		{
			name:        "Debug level shows all",
			level:       LevelDebug,
			expectDebug: true,
			expectInfo:  true,
			expectWarn:  true,
			expectError: true,
		},
		{
			name:        "Info level hides debug",
			level:       LevelInfo,
			expectDebug: false,
			expectInfo:  true,
			expectWarn:  true,
			expectError: true,
		},
		{
			name:        "Warn level hides debug and info",
			level:       LevelWarn,
			expectDebug: false,
			expectInfo:  false,
			expectWarn:  true,
			expectError: true,
		},
		{
			name:        "Error level shows only errors",
			level:       LevelError,
			expectDebug: false,
			expectInfo:  false,
			expectWarn:  false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := new(bytes.Buffer)
			log := &Logger{
				level:  tt.level,
				output: buf,
			}

			log.Debug("debug")
			log.Info("info")
			log.Warn("warn")
			log.Error("error")

			output := buf.String()

			if tt.expectDebug != strings.Contains(output, "debug") {
				t.Errorf("Debug: expected %v, got %v", tt.expectDebug, strings.Contains(output, "debug"))
			}
			if tt.expectInfo != strings.Contains(output, "info") {
				t.Errorf("Info: expected %v, got %v", tt.expectInfo, strings.Contains(output, "info"))
			}
			if tt.expectWarn != strings.Contains(output, "warn") {
				t.Errorf("Warn: expected %v, got %v", tt.expectWarn, strings.Contains(output, "warn"))
			}
			if tt.expectError != strings.Contains(output, "error") {
				t.Errorf("Error: expected %v, got %v", tt.expectError, strings.Contains(output, "error"))
			}
		})
	}
}

// TestSetVerboseEnablesDebugLevel tests SetVerbose sets level to Debug
func TestSetVerboseEnablesDebugLevel(t *testing.T) {
	log := &Logger{level: LevelInfo}
	log.SetVerbose(true)
	if log.level != LevelDebug {
		t.Errorf("SetVerbose(true) should set level to Debug, got %v", log.level)
	}
}

// TestSetQuietEnablesErrorLevel tests SetQuiet sets level to Error
func TestSetQuietEnablesErrorLevel(t *testing.T) {
	log := &Logger{level: LevelInfo}
	log.SetQuiet(true)
	if log.level != LevelError {
		t.Errorf("SetQuiet(true) should set level to Error, got %v", log.level)
	}
}

// TestPackageLevelFunctions tests the package-level convenience functions
func TestPackageLevelFunctions(t *testing.T) {
	// Reset default logger for testing by resetting the once and defaultLogger
	once = sync.Once{}
	defaultLogger = nil

	// Create a buffer to capture output
	buf := new(bytes.Buffer)

	// Initialize default logger with our buffer
	once.Do(func() {
		defaultLogger = &Logger{
			level:  LevelDebug,
			output: buf,
		}
	})

	Debug("debug test")
	Info("info test")
	Warn("warn test")
	Error("error test")

	output := buf.String()
	if !strings.Contains(output, "debug test") {
		t.Error("Package Debug() should work")
	}
	if !strings.Contains(output, "info test") {
		t.Error("Package Info() should work")
	}
	if !strings.Contains(output, "warn test") {
		t.Error("Package Warn() should work")
	}
	if !strings.Contains(output, "error test") {
		t.Error("Package Error() should work")
	}
}
