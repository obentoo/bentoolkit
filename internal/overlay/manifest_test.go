package overlay

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

func TestParseManifestScope(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		want    ManifestScope
		wantErr bool
	}{
		{"empty", "", ManifestScope{}, false},
		{"whitespace", "   ", ManifestScope{}, false},
		{"category only", "app-editors", ManifestScope{Category: "app-editors"}, false},
		{"category trims", "  app-editors  ", ManifestScope{Category: "app-editors"}, false},
		{"category and package", "app-editors/zed", ManifestScope{Category: "app-editors", Package: "zed"}, false},
		{"package trims", " app-editors / zed ", ManifestScope{Category: "app-editors", Package: "zed"}, false},
		{"empty category in slash form", "/zed", ManifestScope{}, true},
		{"empty package in slash form", "app-editors/", ManifestScope{}, true},
		{"too many separators", "a/b/c", ManifestScope{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseManifestScope(tt.arg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseManifestScope(%q) want error, got nil", tt.arg)
				}
				if !errors.Is(err, ErrManifestInvalidScope) {
					t.Errorf("ParseManifestScope(%q) error = %v, want ErrManifestInvalidScope", tt.arg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseManifestScope(%q) unexpected error: %v", tt.arg, err)
			}
			if got != tt.want {
				t.Errorf("ParseManifestScope(%q) = %+v, want %+v", tt.arg, got, tt.want)
			}
		})
	}
}

func TestResolveManifestTargets_WholeOverlay(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")
	createRenameTestEbuild(t, overlayPath, "app-misc", "world", "2.0.0")
	createRenameTestEbuild(t, overlayPath, "dev-libs", "foo", "0.1.0")

	got, err := ResolveManifestTargets(overlayPath, ManifestScope{})
	if err != nil {
		t.Fatalf("ResolveManifestTargets() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ResolveManifestTargets() got %d, want 3 — %+v", len(got), got)
	}
	// Sorted: app-misc/hello, app-misc/world, dev-libs/foo.
	if got[0].Category != "app-misc" || got[0].Package != "hello" {
		t.Errorf("got[0] = %+v, want app-misc/hello", got[0])
	}
	if got[2].Category != "dev-libs" || got[2].Package != "foo" {
		t.Errorf("got[2] = %+v, want dev-libs/foo", got[2])
	}
}

func TestResolveManifestTargets_Category(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")
	createRenameTestEbuild(t, overlayPath, "dev-libs", "foo", "0.1.0")

	got, err := ResolveManifestTargets(overlayPath, ManifestScope{Category: "app-misc"})
	if err != nil {
		t.Fatalf("ResolveManifestTargets() error = %v", err)
	}
	if len(got) != 1 || got[0].Category != "app-misc" || got[0].Package != "hello" {
		t.Errorf("ResolveManifestTargets(category=app-misc) = %+v, want [app-misc/hello]", got)
	}
}

func TestResolveManifestTargets_Package(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	got, err := ResolveManifestTargets(overlayPath, ManifestScope{Category: "app-misc", Package: "hello"})
	if err != nil {
		t.Fatalf("ResolveManifestTargets() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ResolveManifestTargets() got %d, want 1", len(got))
	}
}

func TestResolveManifestTargets_PackageMissing(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	_, err := ResolveManifestTargets(overlayPath, ManifestScope{Category: "app-misc", Package: "ghost"})
	if err == nil {
		t.Fatal("ResolveManifestTargets() expected error for missing package, got nil")
	}
}

func TestResolveManifestTargets_EmptyOverlay(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	_, err := ResolveManifestTargets(overlayPath, ManifestScope{})
	if !errors.Is(err, ErrManifestNoTargets) {
		t.Errorf("ResolveManifestTargets() empty overlay error = %v, want ErrManifestNoTargets", err)
	}
}

func TestResolveManifestTargets_OverlayPathNotSet(t *testing.T) {
	_, err := ResolveManifestTargets("", ManifestScope{})
	if !errors.Is(err, ErrOverlayPathNotSet) {
		t.Errorf("ResolveManifestTargets(\"\") error = %v, want ErrOverlayPathNotSet", err)
	}
}

func TestRegenerateManifests_DryRun(t *testing.T) {
	targets := []ManifestUpdate{
		{Category: "app-misc", Package: "hello"},
		{Category: "dev-libs", Package: "foo"},
	}
	got := RegenerateManifests("/nonexistent", targets, &ManifestOptions{DryRun: true})
	if len(got) != 2 {
		t.Fatalf("RegenerateManifests() dry-run got %d, want 2", len(got))
	}
	for _, u := range got {
		if u.Success {
			t.Errorf("DryRun must not mark Success=true (got %+v)", u)
		}
		if u.Error != "" {
			t.Errorf("DryRun must not set Error (got %+v)", u)
		}
	}
}

func TestRegenerateManifests_PkgdevMissing(t *testing.T) {
	if _, err := exec.LookPath("pkgdev"); err == nil {
		t.Skip("pkgdev installed; cannot exercise the not-found path")
	}

	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)
	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	targets := []ManifestUpdate{{Category: "app-misc", Package: "hello"}}
	got := RegenerateManifests(overlayPath, targets, &ManifestOptions{Keep: true})
	if len(got) != 1 || got[0].Success {
		t.Fatalf("expected single failed update, got %+v", got)
	}
	if !strings.Contains(got[0].Error, "pkgdev not found") {
		t.Errorf("error = %q, want substring 'pkgdev not found'", got[0].Error)
	}
}

func TestRegenerateManifests_RestoresBackupOnFailure(t *testing.T) {
	if _, err := exec.LookPath("pkgdev"); err == nil {
		t.Skip("pkgdev installed; this test asserts rollback on failure path")
	}

	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")
	manifestPath := filepath.Join(overlayPath, "app-misc", "hello", "Manifest")
	original := []byte("DIST hello-1.0.0.tar.gz 0 BLAKE2B 0 SHA512 0\n")
	if err := os.WriteFile(manifestPath, original, 0644); err != nil {
		t.Fatalf("seed Manifest: %v", err)
	}

	targets := []ManifestUpdate{{Category: "app-misc", Package: "hello"}}
	// pkgdev missing -> failure occurs before backup is even taken (error
	// short-circuits at LookPath). Manifest must remain intact regardless.
	got := RegenerateManifests(overlayPath, targets, &ManifestOptions{})
	if len(got) != 1 || got[0].Success {
		t.Fatalf("expected single failed update, got %+v", got)
	}

	current, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read Manifest after failed regen: %v", err)
	}
	if string(current) != string(original) {
		t.Errorf("Manifest content modified after failed regen:\n got: %q\nwant: %q", current, original)
	}
	// .bak must not be left lying around.
	if _, err := os.Stat(manifestPath + ".bak"); !os.IsNotExist(err) {
		t.Errorf("Manifest.bak should not exist after failed regen, stat err = %v", err)
	}
}

func TestResolveDistdir_EmptyCreatesTempAndCleansUp(t *testing.T) {
	dir, cleanup, err := resolveDistdir("")
	if err != nil {
		t.Fatalf("resolveDistdir(\"\") error = %v", err)
	}
	if dir == "" {
		t.Fatal("resolveDistdir(\"\") returned empty path")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("temp distdir not created: %v", err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp distdir should be removed after cleanup, stat err = %v", err)
	}
}

func TestResolveDistdir_PersistentCreatesAndPreserves(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "nested", "distfiles")

	dir, cleanup, err := resolveDistdir(target)
	if err != nil {
		t.Fatalf("resolveDistdir(%q) error = %v", target, err)
	}
	if dir != target {
		t.Errorf("resolveDistdir() dir = %q, want %q", dir, target)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("persistent distdir not created: stat err = %v", err)
	}
	cleanup()
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("persistent distdir must survive cleanup, stat err = %v", err)
	}
}

func TestResolveDistdir_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}

	rel := filepath.Join(".cache", "bentoo-test-distdir-resolve")
	input := "~/" + rel
	expected := filepath.Join(home, rel)
	t.Cleanup(func() { _ = os.RemoveAll(expected) })

	dir, cleanup, err := resolveDistdir(input)
	if err != nil {
		t.Fatalf("resolveDistdir(%q) error = %v", input, err)
	}
	defer cleanup()
	if dir != expected {
		t.Errorf("resolveDistdir(%q) = %q, want %q", input, dir, expected)
	}
}

func TestRegenerateManifests_NoTargets(t *testing.T) {
	got := RegenerateManifests("/anywhere", nil, nil)
	if len(got) != 0 {
		t.Errorf("RegenerateManifests(nil) = %+v, want empty", got)
	}
}

func TestFormatManifestResult_Empty(t *testing.T) {
	out := FormatManifestResult(nil, false)
	if !strings.Contains(out, "No packages") {
		t.Errorf("FormatManifestResult(nil) = %q, want 'No packages...'", out)
	}
}

func TestFormatManifestResult_DryRun(t *testing.T) {
	r := &ManifestResult{Updates: []ManifestUpdate{
		{Category: "app-misc", Package: "hello"},
	}}
	out := FormatManifestResult(r, true)
	if !strings.Contains(out, "Dry run") || !strings.Contains(out, "app-misc/hello") {
		t.Errorf("FormatManifestResult dry-run output = %q", out)
	}
}

func TestFormatManifestResult_MixedResults(t *testing.T) {
	r := &ManifestResult{Updates: []ManifestUpdate{
		{Category: "app-misc", Package: "hello", Success: true},
		{Category: "dev-libs", Package: "foo", Success: false, Error: "boom"},
	}}
	out := FormatManifestResult(r, false)
	if !strings.Contains(out, "1 succeeded") || !strings.Contains(out, "1 failed") {
		t.Errorf("FormatManifestResult counts wrong: %q", out)
	}
	if !strings.Contains(out, "dev-libs/foo: boom") {
		t.Errorf("FormatManifestResult should list failure detail: %q", out)
	}
}

func TestRegenerateManifestsForScope_NoConfig(t *testing.T) {
	_, err := RegenerateManifestsForScope(nil, ManifestScope{}, nil)
	if !errors.Is(err, ErrOverlayPathNotSet) {
		t.Errorf("RegenerateManifestsForScope(nil) error = %v, want ErrOverlayPathNotSet", err)
	}
}

func TestRegenerateManifestsForScope_DryRun(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)
	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	cfg := &config.Config{Overlay: config.OverlayConfig{Path: overlayPath}}
	res, err := RegenerateManifestsForScope(cfg, ManifestScope{}, &ManifestOptions{DryRun: true})
	if err != nil {
		t.Fatalf("RegenerateManifestsForScope() error = %v", err)
	}
	if len(res.Updates) != 1 {
		t.Errorf("RegenerateManifestsForScope() got %d updates, want 1", len(res.Updates))
	}
}
