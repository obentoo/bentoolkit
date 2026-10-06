package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// Story 079, sub-task 7.1: the producer -> consumer shapes agree end to end.
//
// The pending entries are written to pending.json and READ BACK from disk the
// way runApplyAll reads them, so the captured requirement must survive the
// JSON round trip before the wave planner, the gate and the pin rewrite can
// see it. One `--apply all` pass must then publish dart first and flutter with
// its pin pointed at the dart it was released with.

const rpFlutterEbuild = "EAPI=8\nDESCRIPTION=\"flutter\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\nLICENSE=\"BSD\"\n" +
	"RDEPEND=\"~dev-lang/dart-3.13.5\"\n"

const rpDartEbuild = "EAPI=8\nDESCRIPTION=\"dart\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\nLICENSE=\"BSD\"\n"

func rpWrite(t *testing.T, overlay, rel, content string) {
	t.Helper()
	p := filepath.Join(overlay, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func TestRequiresPairAppliesBothInOneRun(t *testing.T) {
	// Both apply paths: in place, and staged — where the second wave's gate
	// must see what the first wave's promotion published into the overlay.
	for _, staged := range []bool{false, true} {
		name := "in place"
		if staged {
			name = "staged"
		}
		t.Run(name, func(t *testing.T) { requiresPairRun(t, staged) })
	}
}

func requiresPairRun(t *testing.T, staged bool) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gentoo := t.TempDir()
	t.Setenv("BENTOO_GENTOO_REPO", gentoo)

	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	rpWrite(t, overlay, "dev-lang/dart/dart-3.13.5.ebuild", rpDartEbuild)
	rpWrite(t, overlay, "dev-lang/flutter/flutter-3.47.6.ebuild", rpFlutterEbuild)

	// Producer: what --check records.
	writer, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	for _, u := range []autoupdate.PendingUpdate{
		{Package: "dev-lang/dart@stable", CurrentVersion: "3.13.5", NewVersion: "3.14.0"},
		{Package: "dev-lang/flutter", CurrentVersion: "3.47.6", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
	} {
		if err := writer.Add(u); err != nil {
			t.Fatalf("pending.Add %s: %v", u.Package, err)
		}
	}

	// Consumer: a fresh list loaded from pending.json, as runApplyAll does.
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("reload pending.json: %v", err)
	}
	updates := pending.List()
	// Dependent first: a run that kept input order would apply flutter
	// before its dart and the pair would not land in one run.
	sort.Slice(updates, func(i, j int) bool { return updates[i].Package > updates[j].Package })
	if len(updates) != 2 || updates[0].Package != "dev-lang/flutter" {
		t.Fatalf("reloaded updates = %+v, want flutter then dart@stable", updates)
	}
	if got := updates[0].Requires["dev-lang/dart"]; got != "3.14.0" {
		t.Fatalf("flutter Requires after the pending.json round trip = %v, want dev-lang/dart -> 3.14.0", updates[0].Requires)
	}

	records := map[string]registry.PackageConfig{
		"dev-lang/dart@stable": {URL: "https://example.invalid/dart.json", Parser: "regex", Pattern: `"version":\s*"([^"]+)"`},
		"dev-lang/flutter": {
			URL: "https://example.invalid/releases_linux.json", Parser: "regex", Pattern: `"version":\s*"([^"]+)"`,
			Requires: map[string]registry.RequireSpec{
				"dev-lang/dart": {Pattern: `"dart_sdk_version":\s*"([^"]+)"`, Pin: "~"},
			},
		},
	}
	opts := []autoupdate.ApplierOption{
		autoupdate.WithApplierPendingList(pending),
		autoupdate.WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		}),
		autoupdate.WithApplierDistdir(t.TempDir(), ""),
		autoupdate.WithApplierGentooPath(gentoo),
		autoupdate.WithApplierPackagesConfig(&registry.PackagesConfig{Packages: records}),
	}
	if staged {
		opts = append(opts,
			autoupdate.WithApplierStagingRoot(filepath.Join(tmp, "staging")),
			autoupdate.WithApplierDepth(validate.DepthOptions),
			autoupdate.WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
		)
	}
	applier, err := autoupdate.NewApplier(overlay, configDir, opts...)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	// Concurrency 1: one worker, so only the wave order can put dart first.
	results, failures := applier.ApplyAll(t.Context(), updates, false, 1)

	if failures != 0 {
		t.Errorf("failures = %d, want 0", failures)
	}
	for i, u := range updates {
		if i >= len(results) || results[i] == nil {
			t.Fatalf("no result for %s at index %d", u.Package, i)
		}
		r := results[i]
		if r.Package != u.Package || !r.Success || len(r.Waiting) != 0 {
			t.Errorf("results[%d] = {Package:%q Success:%v Waiting:%v Error:%v}, want %s applied",
				i, r.Package, r.Success, r.Waiting, r.Error, u.Package)
		}
	}

	if _, err := os.Stat(filepath.Join(overlay, "dev-lang/dart/dart-3.14.0.ebuild")); err != nil {
		t.Errorf("dart-3.14.0.ebuild not published: %v", err)
	}
	newFlutter, err := os.ReadFile(filepath.Join(overlay, "dev-lang/flutter/flutter-3.48.0.ebuild"))
	if err != nil {
		t.Fatalf("flutter-3.48.0.ebuild not published: %v", err)
	}
	if !strings.Contains(string(newFlutter), "~dev-lang/dart-3.14.0") {
		t.Errorf("flutter-3.48.0.ebuild does not pin ~dev-lang/dart-3.14.0:\n%s", newFlutter)
	}
	if strings.Contains(string(newFlutter), "dev-lang/dart-3.13.5") {
		t.Errorf("flutter-3.48.0.ebuild still pins the previous dart 3.13.5:\n%s", newFlutter)
	}

	reloaded, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("reload pending.json after apply: %v", err)
	}
	for _, key := range []string{"dev-lang/dart@stable", "dev-lang/flutter"} {
		if reloaded.Has(key) {
			t.Errorf("pending.json still holds %s after it was applied", key)
		}
	}
}
