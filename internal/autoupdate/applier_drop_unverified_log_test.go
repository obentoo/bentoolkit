package autoupdate

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDropUnverifiedDistfiles_OneWarnPerPackage reproduces the 39 identical
// WARN lines a libreoffice-l10n bump printed on 2026-11-13, one per helppack.
// Three unlisted files must give exactly one WARN that counts them.
func TestDropUnverifiedDistfiles_OneWarnPerPackage(t *testing.T) {
	overlay, distdir := t.TempDir(), t.TempDir()
	pkgDir := filepath.Join(overlay, "app-office", "libreoffice-l10n")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"helppack_am.rpm", "helppack_ar.rpm", "helppack_ast.rpm"} {
		if err := os.WriteFile(filepath.Join(distdir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var logs bytes.Buffer
	a, err := NewApplier(overlay, t.TempDir(),
		WithApplierLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	if got := a.dropUnverifiedDistfiles("app-office/libreoffice-l10n@stable", "26.8.3.1", distdir, false); got != 3 {
		t.Fatalf("dropped %d file(s), want 3 — the fixture does not exercise the drop", got)
	}

	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("logged %d WARN record(s) for one package, want 1:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "count=3") {
		t.Errorf("the WARN does not count the dropped files:\n%s", logs.String())
	}
}
