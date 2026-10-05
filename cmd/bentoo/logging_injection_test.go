package main

// Authored for story 062, sub-task 4.1 (R5.2, R6.2).
//
// A diagnostic emitted by an internal component that `overlay autoupdate`
// builds reaches the invocation's logger — both sinks — which proves the
// command handed that component the logger rather than letting it discard
// (R5.3 makes a forgotten logger silent, so only an end-to-end run shows it).
//
// The probe is the checker's warning for a package whose llm_prompt is set
// while no LLM is configured. The run is hermetic: the registry points at an
// httptest server answering the version the overlay already has.
//
// This file does not import the logging package, so it compiles against
// today's tree; it is self-contained (no helper from logging_wiring_test.go).
// Isolation: HOME and XDG_CONFIG_HOME come from newTestCLI, XDG_STATE_HOME is
// set here before the run.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const s062InjPackage = "app-misc/s062-llm-probe"

// s062InjSeed builds a harness whose overlay has one up-to-date package with an
// llm_prompt, and returns it with its log file path.
func s062InjSeed(t *testing.T) (*testCLI, string) {
	t.Helper()
	c := newTestCLI(t)
	state := filepath.Join(c.Home(), "state")
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("BENTOO_LOG_LEVEL", "")

	writeExitTestEbuild(t, c.Overlay(), s062InjPackage, "1.0.0")
	cfgDir := filepath.Join(c.Overlay(), ".autoupdate")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := "[\"" + s062InjPackage + "\"]\n" +
		"url = \"" + upstreamServer(t, "1.0.0") + "\"\n" +
		"parser = \"json\"\n" +
		"path = \"version\"\n" +
		"llm_prompt = \"extract the version\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "packages.toml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	return c, filepath.Join(state, "bentoo", "logs", "bentoo.log")
}

// s062InjFileWarns counts the WARN records in the log file that carry the
// package in an attribute.
func s062InjFileWarns(t *testing.T, logFile string) int {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading the log file %s: %v", logFile, err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("a log file line is not one JSON object: %v\n%s", err, line)
		}
		if rec["level"] != "WARN" {
			continue
		}
		for k, v := range rec {
			if k != "msg" && strings.Contains(fmt.Sprint(v), s062InjPackage) {
				n++
				break
			}
		}
	}
	return n
}

// s062InjStderrWarns returns the stderr WARN lines naming the package.
func s062InjStderrWarns(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "level=WARN ") && strings.Contains(line, s062InjPackage) {
			out = append(out, line)
		}
	}
	return out
}

// TestLoggingInjectionComponentWarningReachesBothSinks: at the default level
// the checker's warning is one slog text line on stderr and one JSON record in
// the file; under --quiet it leaves stderr but stays in the file (R2.4).
func TestLoggingInjectionComponentWarningReachesBothSinks(t *testing.T) {
	t.Run("default level", func(t *testing.T) {
		c, logFile := s062InjSeed(t)
		_, stderr, _ := c.Run("overlay", "autoupdate", "--check", "--force")

		if got := s062InjStderrWarns(stderr); len(got) != 1 {
			t.Errorf("stderr holds %d slog WARN lines naming %s, want 1:\n%s", len(got), s062InjPackage, stderr)
		}
		if n := s062InjFileWarns(t, logFile); n != 1 {
			t.Errorf("the log file holds %d WARN records naming %s, want 1 — the checker did not receive the invocation's logger", n, s062InjPackage)
		}
	})

	t.Run("quiet", func(t *testing.T) {
		c, logFile := s062InjSeed(t)
		_, stderr, _ := c.Run("--quiet", "overlay", "autoupdate", "--check", "--force")

		if strings.Contains(stderr, s062InjPackage) {
			t.Errorf("--quiet let the warning reach stderr:\n%s", stderr)
		}
		if n := s062InjFileWarns(t, logFile); n != 1 {
			t.Errorf("under --quiet the log file holds %d WARN records naming %s, want 1", n, s062InjPackage)
		}
	})
}
