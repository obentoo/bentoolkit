package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// TestCheck_NoLogRecordLandsOnTheProgressLine reproduces the 2026-11-13 run,
// where "update held by min_age_days" was printed glued to the `\r  Checking:`
// counter. Stdout and the logger share ONE file here, so the file holds the
// order the terminal would show: every log record must come after the line
// that clears the counter.
func TestCheck_NoLogRecordLandsOnTheProgressLine(t *testing.T) {
	td := defaultDeps()
	auOpts := testAutoupdateOptions()

	published := time.Now().UTC().Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"version":"1.2.3","published":%q}`, published)
	}))
	t.Cleanup(server.Close)

	overlayDir, configDir := t.TempDir(), t.TempDir()
	t.Setenv("HOME", t.TempDir())
	pkg := "dev-util/young"
	writeExitTestPackagesConfig(t, overlayDir, server.URL, []string{pkg})
	registry := filepath.Join(overlayDir, ".autoupdate", "packages.toml")
	body, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("min_age_days = 7\npublished_path = \"published\"\n")...)
	if err := os.WriteFile(registry, body, 0o644); err != nil {
		t.Fatal(err)
	}
	writeExitTestEbuild(t, overlayDir, pkg, "0.9.0")

	pinCheckFlags(t, auOpts)
	quiet = false // the counter is the subject
	setReconcileYes(t, auOpts, false)
	setReconcileInteractive(td, func() bool { return false })

	terminal, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = terminal
	t.Cleanup(func() { os.Stdout = origStdout })

	run := testAutoupdateRunWith(auOpts, td)
	run.lg = slog.New(slog.NewTextHandler(terminal, &slog.HandlerOptions{Level: slog.LevelInfo}))
	_ = run.runCheck(context.Background(), overlayDir, configDir, nil, 0, &config.Config{}, config.LLMConfig{})
	os.Stdout = origStdout

	out, err := os.ReadFile(terminal.Name())
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	held := strings.Index(text, "held by min_age_days")
	if held < 0 {
		t.Fatalf("the fixture logged no min_age_days record, so it proves nothing:\n%q", text)
	}
	cleared := strings.LastIndex(text, "\r                                        \r")
	if cleared < 0 || held < cleared {
		t.Errorf("a log record was written while the progress counter was on screen:\n%q", text)
	}
}
