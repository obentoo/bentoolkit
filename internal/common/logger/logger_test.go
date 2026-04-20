package logger

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
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

// --- Task 1.1: File logging, Close, LogDir tests ---

// TestEnableFileLogging verifies that EnableFileLogging creates bentoo.log in the correct directory.
// _Requirements: 5.1_
func TestEnableFileLogging(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)

	buf := new(bytes.Buffer)
	l := &Logger{level: LevelInfo, output: buf}

	if err := l.EnableFileLogging(); err != nil {
		t.Fatalf("EnableFileLogging() error: %v", err)
	}
	defer l.Close()

	logFile := filepath.Join(stateDir, "bentoo", "logs", "bentoo.log")
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Errorf("expected log file to exist at %s", logFile)
	}
}

// TestFileLoggingWritesMessages verifies that messages are written to the log file.
// _Requirements: 5.2_
func TestFileLoggingWritesMessages(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)

	buf := new(bytes.Buffer)
	l := &Logger{level: LevelInfo, output: buf}

	if err := l.EnableFileLogging(); err != nil {
		t.Fatalf("EnableFileLogging() error: %v", err)
	}
	defer l.Close()

	l.Info("test message")

	logFile := filepath.Join(stateDir, "bentoo", "logs", "bentoo.log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	if !strings.Contains(string(content), "test message") {
		t.Errorf("expected log file to contain 'test message', got: %s", string(content))
	}
}

// TestClose verifies that Close sets fileOutput to nil.
// _Requirements: 5.3_
func TestClose(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)

	buf := new(bytes.Buffer)
	l := &Logger{level: LevelInfo, output: buf}

	if err := l.EnableFileLogging(); err != nil {
		t.Fatalf("EnableFileLogging() error: %v", err)
	}

	l.Close()

	if l.fileOutput != nil {
		t.Error("expected fileOutput to be nil after Close()")
	}
}

// TestLogDir verifies that LogDir returns the XDG-based path.
// _Requirements: 5.4, 5.5_
func TestLogDir(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)

	got, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir() error: %v", err)
	}

	expected := filepath.Join(stateDir, "bentoo", "logs")
	if got != expected {
		t.Errorf("LogDir() = %q, want %q", got, expected)
	}
}

// TestLogDirDefault verifies that LogDir falls back to ~/.local/state/bentoo/logs.
// _Requirements: 5.4_
func TestLogDirDefault(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")

	got, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir() error: %v", err)
	}

	if !strings.HasSuffix(got, filepath.Join(".local", "state", "bentoo", "logs")) {
		t.Errorf("LogDir() = %q, expected suffix .local/state/bentoo/logs", got)
	}
}

// --- Task 1.2: Singleton and no-op flag tests ---

// TestDefaultSingleton verifies that Default() returns the same instance each time.
// _Requirements: 5.6_
func TestDefaultSingleton(t *testing.T) {
	once = sync.Once{}
	defaultLogger = nil

	l1 := Default()
	l2 := Default()

	if l1 != l2 {
		t.Error("Default() should return the same logger instance (singleton)")
	}
}

// TestSetVerboseFalseIsNoop verifies that SetVerbose(false) does not change the log level.
// _Requirements: 5.7_
func TestSetVerboseFalseIsNoop(t *testing.T) {
	l := &Logger{level: LevelInfo}
	l.SetVerbose(false)
	if l.level != LevelInfo {
		t.Errorf("SetVerbose(false) should be a no-op, got level %v", l.level)
	}
}

// TestSetQuietFalseIsNoop verifies that SetQuiet(false) does not change the log level.
// _Requirements: 5.8_
func TestSetQuietFalseIsNoop(t *testing.T) {
	l := &Logger{level: LevelInfo}
	l.SetQuiet(false)
	if l.level != LevelInfo {
		t.Errorf("SetQuiet(false) should be a no-op, got level %v", l.level)
	}
}

// --- Task 1.3: PBT — Logger dual output consistency ---

// TestLoggerDualOutputConsistency tests Property 3: Logger dual output consistency.
// **Feature: test-coverage-improvement, Property 3: Logger dual output consistency**
// **Validates: Requirements 5.2**
func TestLoggerDualOutputConsistency(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)
	properties.Property("message appears in both writer and log file", prop.ForAll(
		func(msg string) bool {
			buf := new(bytes.Buffer)
			l := &Logger{level: LevelInfo, output: buf}
			if err := l.EnableFileLogging(); err != nil {
				return false
			}
			l.Info("%s", msg)
			l.Close()

			// Check writer output
			if !strings.Contains(buf.String(), msg) {
				return false
			}

			// Check file output
			logFile := filepath.Join(stateDir, "bentoo", "logs", "bentoo.log")
			content, err := os.ReadFile(logFile)
			if err != nil {
				return false
			}
			return strings.Contains(string(content), msg)
		},
		gen.AlphaString().SuchThat(func(s interface{}) bool { return len(s.(string)) > 0 }),
	))
	properties.TestingRun(t)
}
