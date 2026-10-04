package autoupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// writeRepoSkeleton gives dir the files egencache needs to see a repository.
func writeRepoSkeleton(t *testing.T, dir, masters string) {
	t.Helper()
	for _, d := range []string{"profiles", "metadata/md5-cache"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles", "repo_name"), []byte("bentoo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles", "categories"), []byte("test-cat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	layout := "masters = " + masters + "\nthin-manifests = true\n"
	if err := os.WriteFile(filepath.Join(dir, "metadata", "layout.conf"), []byte(layout), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoriesConfiguration(t *testing.T) {
	dir := t.TempDir()
	writeRepoSkeleton(t, dir, "gentoo")
	conf, name, err := repositoriesConfiguration(dir, "/srv/gentoo")
	if err != nil {
		t.Fatalf("repositoriesConfiguration: %v", err)
	}
	if name != "bentoo" {
		t.Errorf("name = %q", name)
	}
	for _, want := range []string{"location = /srv/gentoo", "[bentoo]\nlocation = " + dir, "masters = gentoo"} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf lacks %q:\n%s", want, conf)
		}
	}

	other := t.TempDir()
	writeRepoSkeleton(t, other, "gentoo guru")
	if _, _, err := repositoriesConfiguration(other, ""); err == nil {
		t.Error("a master with no known location was accepted")
	}
}

// TestApplyRegeneratesMetadataCache pins that a successful apply runs egencache
// for the bumped package against the checkout, not the synced repository.
func TestApplyRegeneratesMetadataCache(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")
	writeRepoSkeleton(t, overlayDir, "gentoo")

	pending, _ := NewPendingList(configDir)
	if err := pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.0.0", NewVersion: "2.0.0", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}

	var (
		mu  sync.Mutex
		got [][]string
	)
	execFn := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "egencache" {
			mu.Lock()
			got = append(got, append([]string{name}, arg...))
			mu.Unlock()
			// Stand in for egencache: write the entry it would have written.
			entry := filepath.Join(overlayDir, "metadata", "md5-cache", "test-cat", "test-pkg-2.0.0")
			return exec.CommandContext(ctx, "sh", "-c", `mkdir -p "$(dirname "$1")" && : > "$1"`, "sh", entry)
		}
		return exec.CommandContext(ctx, "true")
	}
	applier, err := NewApplier(overlayDir, configDir, WithApplierPendingList(pending), WithExecCommand(execFn))
	if err != nil {
		t.Fatal(err)
	}
	result, err := applier.Apply(t.Context(), pkg, false)
	if err != nil || !result.Success {
		t.Fatalf("Apply: %v (success=%v)", err, result.Success)
	}
	if result.MetadataCacheWarning != "" {
		t.Errorf("MetadataCacheWarning = %q", result.MetadataCacheWarning)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("egencache ran %d times, want 1", len(got))
	}
	args := got[0]
	if args[len(args)-1] != pkg {
		t.Errorf("egencache target = %q, want %q", args[len(args)-1], pkg)
	}
	i := slices.Index(args, "--repositories-configuration")
	if i < 0 || !strings.Contains(args[i+1], "location = "+overlayDir) {
		t.Errorf("egencache not pointed at the checkout: %v", args)
	}
}

// TestRegenMetadataCacheWithRealEgencache runs the real tool against a scratch
// repository: the new version gets an entry, the removed one loses its own, and
// another package's entry is left alone. It needs Portage and ::gentoo, so it
// skips anywhere else (the CI runner included).
func TestRegenMetadataCacheWithRealEgencache(t *testing.T) {
	if _, err := exec.LookPath("egencache"); err != nil {
		t.Skip("egencache not installed")
	}
	if _, err := os.Stat(filepath.Join(defaultGentooRepo, "profiles", "repo_name")); err != nil {
		t.Skip("no ::gentoo at " + defaultGentooRepo)
	}
	overlay := t.TempDir()
	writeRepoSkeleton(t, overlay, "gentoo")
	createTestEbuildFile(t, overlay, "test-cat/test-pkg", "2.0.0")
	cache := filepath.Join(overlay, "metadata", "md5-cache", "test-cat")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{"test-pkg-1.0.0", "other-pkg-1.0"} {
		if err := os.WriteFile(filepath.Join(cache, stale), []byte("EAPI=8\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	a, err := NewApplier(overlay, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.regenMetadataCache(t.Context(), "test-cat/test-pkg", "2.0.0"); err != nil {
		t.Fatalf("regenMetadataCache: %v", err)
	}
	entries, _ := os.ReadDir(cache)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"other-pkg-1.0", "test-pkg-2.0.0"}; !slices.Equal(names, want) {
		t.Errorf("md5-cache = %v, want %v", names, want)
	}
}

// TestRegenMetadataCacheRefusesASilentEgencacheFailure pins that egencache's
// exit status is not trusted: it exits 0 after "Error processing …", so a
// missing entry for the new version is reported.
func TestRegenMetadataCacheRefusesASilentEgencacheFailure(t *testing.T) {
	overlay := t.TempDir()
	writeRepoSkeleton(t, overlay, "gentoo")
	a, err := NewApplier(overlay, t.TempDir(), WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", "Error processing test-cat/test-pkg-2.0.0 with returncode 1, continuing...")
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = a.regenMetadataCache(t.Context(), "test-cat/test-pkg", "2.0.0")
	if err == nil || !strings.Contains(err.Error(), "Error processing") {
		t.Errorf("err = %v, want a failure carrying egencache's output", err)
	}
}
