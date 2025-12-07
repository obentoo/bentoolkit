package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level represents the logging level
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelQuiet // No output
)

var levelNames = map[Level]string{
	LevelDebug: "DEBUG",
	LevelInfo:  "INFO",
	LevelWarn:  "WARN",
	LevelError: "ERROR",
}

// Logger handles application logging
type Logger struct {
	level      Level
	output     io.Writer
	fileOutput *os.File
	mu         sync.Mutex
}

var (
	defaultLogger *Logger
	once          sync.Once
)

// Default returns the default logger instance
func Default() *Logger {
	once.Do(func() {
		defaultLogger = &Logger{
			level:  LevelInfo,
			output: os.Stderr,
		}
	})
	return defaultLogger
}

// SetLevel sets the logging level
func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// SetVerbose enables debug output
func (l *Logger) SetVerbose(verbose bool) {
	if verbose {
		l.SetLevel(LevelDebug)
	}
}

// SetQuiet disables all output except errors
func (l *Logger) SetQuiet(quiet bool) {
	if quiet {
		l.SetLevel(LevelError)
	}
}

// EnableFileLogging enables logging to a file
func (l *Logger) EnableFileLogging() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	logDir, err := LogDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	logFile := filepath.Join(logDir, "bentoo.log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	l.fileOutput = f
	return nil
}

// Close closes the log file if open
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fileOutput != nil {
		l.fileOutput.Close()
		l.fileOutput = nil
	}
}

// LogDir returns the log directory path
func LogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	// Use XDG_STATE_HOME for logs (standard for runtime data)
	xdgState := os.Getenv("XDG_STATE_HOME")
	if xdgState == "" {
		xdgState = filepath.Join(home, ".local", "state")
	}

	return filepath.Join(xdgState, "bentoo", "logs"), nil
}

func (l *Logger) log(level Level, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if level < l.level {
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	levelName := levelNames[level]
	msg := fmt.Sprintf(format, args...)
	logLine := fmt.Sprintf("[%s] %s: %s\n", timestamp, levelName, msg)

	// Write to stderr for terminal output
	if l.level <= level {
		fmt.Fprint(l.output, msg+"\n")
	}

	// Write to file if enabled
	if l.fileOutput != nil {
		l.fileOutput.WriteString(logLine)
	}
}

// Debug logs a debug message
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(LevelDebug, format, args...)
}

// Info logs an info message
func (l *Logger) Info(format string, args ...interface{}) {
	l.log(LevelInfo, format, args...)
}

// Warn logs a warning message
func (l *Logger) Warn(format string, args ...interface{}) {
	l.log(LevelWarn, format, args...)
}

// Error logs an error message
func (l *Logger) Error(format string, args ...interface{}) {
	l.log(LevelError, format, args...)
}

// Package-level convenience functions
func Debug(format string, args ...interface{}) { Default().Debug(format, args...) }
func Info(format string, args ...interface{})  { Default().Info(format, args...) }
func Warn(format string, args ...interface{})  { Default().Warn(format, args...) }
func Error(format string, args ...interface{}) { Default().Error(format, args...) }
func SetVerbose(v bool)                        { Default().SetVerbose(v) }
func SetQuiet(q bool)                          { Default().SetQuiet(q) }
