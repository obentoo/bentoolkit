package overlay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// These tests pin S050-R5.1..R5.4 and R5.6: two or more matched ebuilds that
// map to one target are a collision, recorded in RenameResult.Collisions and
// refused by Rename with a *CollisionError before anything moves, --force or
// not.
//
// The shape of a Collisions element is not part of the contract, so the tests
// read it only through len() and its rendered %v form; *CollisionError is read
// only through errors.As and Error().

// collisionOverlay builds an overlay whose ebuilds each carry DISTINCT bytes
// (their own filename), so an overwrite by a sibling source is visible.
func collisionOverlay(t *testing.T, files map[string][]string) (string, *config.Config) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"profiles", "metadata"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for catPkg, names := range files {
		pkgDir := filepath.Join(root, filepath.FromSlash(catPkg))
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", catPkg, err)
		}
		for _, name := range names {
			body := "# " + catPkg + "/" + name + "\nEAPI=8\n"
			if err := os.WriteFile(filepath.Join(pkgDir, name), []byte(body), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
	}
	return root, &config.Config{Overlay: config.OverlayConfig{Path: root}}
}

// collisionSnapshot records every regular file under root, name -> bytes.
func collisionSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		snap[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snap
}

func assertCollisionSnapshotUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := collisionSnapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("overlay changed although the rename was refused\nbefore: %v\nafter:  %v", before, after)
	}
}

func assertIsCollisionError(t *testing.T, err error) *CollisionError {
	t.Helper()
	var ce *CollisionError
	if !errors.As(err, &ce) {
		t.Fatalf("Rename error = %v (%T), want a *CollisionError", err, err)
	}
	return ce
}

// collisionsText renders result.Collisions so the tests can look for filenames
// without pinning the element type.
func collisionsText(result *RenameResult) string {
	return fmt.Sprintf("%+v", result.Collisions)
}

func TestRenameRefusesSharedTarget(t *testing.T) {
	t.Run("revision pair onto one target", func(t *testing.T) {
		root, cfg := collisionOverlay(t, map[string][]string{
			"app-misc/lib-foo": {"lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild"},
		})
		before := collisionSnapshot(t, root)

		result, err := Rename(cfg, &RenameSpec{Category: "app-misc", PackagePattern: "lib-foo", OldVersion: "1.0", NewVersion: "1.1"},
			&RenameOptions{NoManifest: true, SkipPrompt: true})
		assertIsCollisionError(t, err)
		if result == nil {
			t.Fatal("Rename returned a nil result with the collision; want the result carrying Collisions")
		}
		if n := len(result.Collisions); n != 1 {
			t.Errorf("len(Collisions) = %d, want 1 (one target shared by two sources): %s", n, collisionsText(result))
		}
		txt := collisionsText(result)
		for _, want := range []string{"lib-foo-1.1.ebuild", "lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild"} {
			if !strings.Contains(txt, want) {
				t.Errorf("Collisions %s does not name %q", txt, want)
			}
		}
		if len(result.Renamed) != 0 {
			t.Errorf("Renamed = %v, want nothing moved", result.Renamed)
		}
		if _, statErr := os.Stat(filepath.Join(root, "app-misc", "lib-foo", "lib-foo-1.1.ebuild")); statErr == nil {
			t.Error("lib-foo-1.1.ebuild exists: a source was moved onto the shared target")
		}
		assertCollisionSnapshotUnchanged(t, root, before)
	})

	// Third element: -r2 joins the SAME collision; it must not become a second
	// collision or slip through as a benign single mapping.
	t.Run("third revision joins the same collision", func(t *testing.T) {
		root, cfg := collisionOverlay(t, map[string][]string{
			"app-misc/lib-foo": {"lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild", "lib-foo-1.0-r2.ebuild"},
		})
		before := collisionSnapshot(t, root)

		result, err := Rename(cfg, &RenameSpec{Category: "app-misc", PackagePattern: "lib-foo", OldVersion: "1.0", NewVersion: "1.1"},
			&RenameOptions{NoManifest: true, SkipPrompt: true})
		assertIsCollisionError(t, err)
		if result == nil {
			t.Fatal("nil result")
		}
		if n := len(result.Collisions); n != 1 {
			t.Errorf("len(Collisions) = %d, want 1 target with three sources: %s", n, collisionsText(result))
		}
		txt := collisionsText(result)
		for _, want := range []string{"lib-foo-1.1.ebuild", "lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild", "lib-foo-1.0-r2.ebuild"} {
			if !strings.Contains(txt, want) {
				t.Errorf("Collisions %s does not name %q", txt, want)
			}
		}
		assertCollisionSnapshotUnchanged(t, root, before)
	})

	// R5.2 "no file in ANY package moves": the non-colliding package in the
	// same rename stays put too.
	t.Run("non-colliding package in the same rename does not move", func(t *testing.T) {
		root, cfg := collisionOverlay(t, map[string][]string{
			"app-misc/lib-foo": {"lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild"},
			"app-misc/lib-bar": {"lib-bar-1.0.ebuild"},
		})
		before := collisionSnapshot(t, root)

		result, err := Rename(cfg, &RenameSpec{Category: "app-misc", PackagePattern: "lib-*", OldVersion: "1.0", NewVersion: "1.1"},
			&RenameOptions{NoManifest: true, SkipPrompt: true})
		assertIsCollisionError(t, err)
		if result == nil {
			t.Fatal("nil result")
		}
		if n := len(result.Collisions); n != 1 {
			t.Errorf("len(Collisions) = %d, want 1 (bar has a single source and is not a collision): %s", n, collisionsText(result))
		}
		if strings.Contains(collisionsText(result), "lib-bar-1.1.ebuild") {
			t.Errorf("Collisions names lib-bar-1.1.ebuild, which has one source: %s", collisionsText(result))
		}
		if _, statErr := os.Stat(filepath.Join(root, "app-misc", "lib-bar", "lib-bar-1.1.ebuild")); statErr == nil {
			t.Error("lib-bar-1.1.ebuild exists: the non-colliding package moved although the rename was refused")
		}
		assertCollisionSnapshotUnchanged(t, root, before)
	})

	// Hostile collapse: equal versions in two DIFFERENT packages map to two
	// different targets and must not be read as a collision.
	t.Run("same version in two packages is not a collision", func(t *testing.T) {
		root, cfg := collisionOverlay(t, map[string][]string{
			"app-misc/lib-foo": {"lib-foo-1.0.ebuild"},
			"app-misc/lib-bar": {"lib-bar-1.0.ebuild"},
		})
		result, err := Rename(cfg, &RenameSpec{Category: "app-misc", PackagePattern: "lib-*", OldVersion: "1.0", NewVersion: "1.1"},
			&RenameOptions{NoManifest: true, SkipPrompt: true})
		if err != nil {
			t.Fatalf("Rename error = %v, want success: lib-foo-1.1 and lib-bar-1.1 are different targets", err)
		}
		if n := len(result.Collisions); n != 0 {
			t.Errorf("len(Collisions) = %d, want 0: %s", n, collisionsText(result))
		}
		for _, p := range []string{"app-misc/lib-foo/lib-foo-1.1.ebuild", "app-misc/lib-bar/lib-bar-1.1.ebuild"} {
			if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(p))); statErr != nil {
				t.Errorf("%s missing after a rename with no collision: %v", p, statErr)
			}
		}
	})

	// Same filename, two categories: the target PATHS differ, so this is not
	// a collision either (a key built from the filename alone would collapse).
	t.Run("same package name in two categories is not a collision", func(t *testing.T) {
		root, cfg := collisionOverlay(t, map[string][]string{
			"app-misc/lib-foo": {"lib-foo-1.0.ebuild"},
			"dev-libs/lib-foo": {"lib-foo-1.0.ebuild"},
		})
		result, err := Rename(cfg, &RenameSpec{Category: "*", PackagePattern: "lib-foo", OldVersion: "1.0", NewVersion: "1.1"},
			&RenameOptions{NoManifest: true, SkipPrompt: true})
		if err != nil {
			t.Fatalf("Rename error = %v, want success: app-misc/foo and dev-libs/foo have different targets", err)
		}
		if n := len(result.Collisions); n != 0 {
			t.Errorf("len(Collisions) = %d, want 0: %s", n, collisionsText(result))
		}
		for _, p := range []string{"app-misc/lib-foo/lib-foo-1.1.ebuild", "dev-libs/lib-foo/lib-foo-1.1.ebuild"} {
			if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(p))); statErr != nil {
				t.Errorf("%s missing: %v", p, statErr)
			}
		}
	})
}

// R5.3: --force overrides a pre-existing target (a Conflict), never a
// collision.
func TestRenameRefusesSharedTargetEvenWithForce(t *testing.T) {
	cases := map[string][]string{
		"revision pair":                         {"lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild"},
		"revision pair plus an existing target": {"lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild", "lib-foo-1.1.ebuild"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			root, cfg := collisionOverlay(t, map[string][]string{"app-misc/lib-foo": files})
			before := collisionSnapshot(t, root)

			result, err := Rename(cfg, &RenameSpec{Category: "app-misc", PackagePattern: "lib-foo", OldVersion: "1.0", NewVersion: "1.1"},
				&RenameOptions{NoManifest: true, SkipPrompt: true, Force: true})
			assertIsCollisionError(t, err)
			if result != nil && len(result.Renamed) != 0 {
				t.Errorf("Renamed = %v, want nothing moved under --force", result.Renamed)
			}
			assertCollisionSnapshotUnchanged(t, root, before)
		})
	}
}

// R5.1 from RenamePreview, with both hostile directions.
func TestRenamePreviewReportsCollisions(t *testing.T) {
	t.Run("revision pair is recorded", func(t *testing.T) {
		root, cfg := collisionOverlay(t, map[string][]string{
			"app-misc/lib-foo": {"lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild"},
		})
		before := collisionSnapshot(t, root)
		result, err := RenamePreview(cfg, &RenameSpec{Category: "app-misc", PackagePattern: "lib-foo", OldVersion: "1.0", NewVersion: "1.1"})
		if err != nil {
			t.Fatalf("RenamePreview error = %v; the preview reports collisions as data", err)
		}
		if n := len(result.Collisions); n != 1 {
			t.Errorf("len(Collisions) = %d, want 1: %s", n, collisionsText(result))
		}
		txt := collisionsText(result)
		for _, want := range []string{"lib-foo-1.1.ebuild", "lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild"} {
			if !strings.Contains(txt, want) {
				t.Errorf("Collisions %s does not name %q", txt, want)
			}
		}
		if len(result.Conflicts) != 0 {
			t.Errorf("Conflicts = %v, want none: nothing exists at the target yet", result.Conflicts)
		}
		assertCollisionSnapshotUnchanged(t, root, before)
	})

	t.Run("same versions in two packages are not a collision", func(t *testing.T) {
		_, cfg := collisionOverlay(t, map[string][]string{
			"app-misc/lib-foo": {"lib-foo-1.0.ebuild"},
			"app-misc/lib-bar": {"lib-bar-1.0.ebuild"},
		})
		result, err := RenamePreview(cfg, &RenameSpec{Category: "app-misc", PackagePattern: "lib-*", OldVersion: "1.0", NewVersion: "1.1"})
		if err != nil {
			t.Fatalf("RenamePreview error = %v", err)
		}
		if n := len(result.Collisions); n != 0 {
			t.Errorf("len(Collisions) = %d, want 0: %s", n, collisionsText(result))
		}
	})

	// Hostile split: an existing target with ONE source stays a Conflict and
	// must not be promoted to a Collision (which --force could not override).
	t.Run("existing target with one source stays a conflict", func(t *testing.T) {
		_, cfg := collisionOverlay(t, map[string][]string{
			"app-misc/lib-foo": {"lib-foo-1.0.ebuild", "lib-foo-1.1.ebuild"},
		})
		spec := &RenameSpec{Category: "app-misc", PackagePattern: "lib-foo", OldVersion: "1.0", NewVersion: "1.1"}
		result, err := RenamePreview(cfg, spec)
		if err != nil {
			t.Fatalf("RenamePreview error = %v", err)
		}
		if n := len(result.Collisions); n != 0 {
			t.Errorf("len(Collisions) = %d, want 0: %s", n, collisionsText(result))
		}
		if n := len(result.Conflicts); n != 1 {
			t.Errorf("len(Conflicts) = %d, want 1", n)
		}
		_, err = Rename(cfg, spec, &RenameOptions{NoManifest: true, SkipPrompt: true})
		var ce *CollisionError
		if errors.As(err, &ce) {
			t.Errorf("Rename error = %v, a *CollisionError; want the *ConflictError --force can override", err)
		}
		var cfe *ConflictError
		if !errors.As(err, &cfe) {
			t.Errorf("Rename error = %v (%T), want *ConflictError", err, err)
		}
	})
}

// R5.4: the rendered preview lists each colliding target with its sources and
// says --force does not override. The collision section is isolated by
// rendering the same result with Collisions zeroed and diffing the lines, so
// the match list (which already names every filename) cannot satisfy it.
func TestFormatRenamePreviewWithCollisions(t *testing.T) {
	_, cfg := collisionOverlay(t, map[string][]string{
		"app-misc/lib-foo": {"lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild"},
	})
	result, err := RenamePreview(cfg, &RenameSpec{Category: "app-misc", PackagePattern: "lib-foo", OldVersion: "1.0", NewVersion: "1.1"})
	if err != nil {
		t.Fatalf("RenamePreview error = %v", err)
	}
	if len(result.Collisions) == 0 {
		t.Fatalf("fixture: RenamePreview recorded no collision for lib-foo-1.0 + lib-foo-1.0-r1 -> 1.1")
	}
	with := FormatRenamePreview(result, false)

	stripped := *result
	field := reflect.ValueOf(&stripped).Elem().FieldByName("Collisions")
	field.Set(reflect.Zero(field.Type()))
	without := FormatRenamePreview(&stripped, false)

	remaining := map[string]int{}
	for _, line := range strings.Split(without, "\n") {
		remaining[line]++
	}
	var added []string
	for _, line := range strings.Split(with, "\n") {
		if remaining[line] > 0 {
			remaining[line]--
			continue
		}
		added = append(added, line)
	}
	section := strings.Join(added, "\n")
	for _, want := range []string{"lib-foo-1.1.ebuild", "lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild", "--force"} {
		if !strings.Contains(section, want) {
			t.Errorf("collision section of the preview does not contain %q\nsection:\n%s\nfull preview:\n%s", want, section, with)
		}
	}
}

// R5.6: the printed *CollisionError names category/package, the target and
// every source.
func TestCollisionErrorMessage(t *testing.T) {
	_, cfg := collisionOverlay(t, map[string][]string{
		"app-misc/lib-foo": {"lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild"},
	})
	_, err := Rename(cfg, &RenameSpec{Category: "app-misc", PackagePattern: "lib-foo", OldVersion: "1.0", NewVersion: "1.1"},
		&RenameOptions{NoManifest: true, SkipPrompt: true})
	ce := assertIsCollisionError(t, err)
	msg := ce.Error()
	for _, want := range []string{"app-misc/lib-foo", "lib-foo-1.1.ebuild", "lib-foo-1.0.ebuild", "lib-foo-1.0-r1.ebuild"} {
		if !strings.Contains(msg, want) {
			t.Errorf("CollisionError message %q does not name %q", msg, want)
		}
	}
}
