package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// Story 079, sub-task 6.1: `--apply all` runs a batch in dependency waves.
//
// applyWaves(updates, pins) returns waves of INDICES into updates. An entry B
// depends on an entry A when A's atom (its key without "@label" / ":slot") is
// a key of B.Requires and A.NewVersion satisfies the pin B's record declares
// for that atom. pins(pkg, atom) answers that pin operator.

// rwPins builds the pins callback from an atom -> operator table. It ignores
// the dependent's key on purpose: every case below declares one operator per
// required atom, so the answer cannot depend on how the planner spells pkg.
func rwPins(byAtom map[string]string) func(pkg, atom string) string {
	return func(_, atom string) string { return byAtom[atom] }
}

// rwCheckPartition asserts waves is a partition of 0..n-1 with no empty wave.
func rwCheckPartition(t *testing.T, waves [][]int, n int) {
	t.Helper()
	seen := make(map[int]int, n)
	for w, wave := range waves {
		if len(wave) == 0 {
			t.Errorf("wave %d is empty: %v", w, waves)
		}
		for _, i := range wave {
			if i < 0 || i >= n {
				t.Errorf("wave %d holds index %d, outside 0..%d: %v", w, i, n-1, waves)
				continue
			}
			seen[i]++
		}
	}
	for i := 0; i < n; i++ {
		if seen[i] != 1 {
			t.Errorf("index %d appears %d times across the waves, want exactly once: %v", i, seen[i], waves)
		}
	}
}

// rwSorted returns a copy of waves with each wave sorted, so a case can state
// wave membership without pinning the order inside a wave.
func rwSorted(waves [][]int) [][]int {
	out := make([][]int, len(waves))
	for i, w := range waves {
		c := append([]int(nil), w...)
		sort.Ints(c)
		out[i] = c
	}
	return out
}

func TestRequiresWavesNoRequirementsIsOneWaveInInputOrder(t *testing.T) {
	updates := []autoupdate.PendingUpdate{
		{Package: "dev-lang/flutter", CurrentVersion: "3.47.6", NewVersion: "3.48.0"},
		{Package: "dev-lang/dart@stable", CurrentVersion: "3.13.5", NewVersion: "3.14.0"},
		{Package: "app-misc/foo:2", CurrentVersion: "2.0", NewVersion: "2.1"},
		{Package: "app-misc/bar", CurrentVersion: "1.0", NewVersion: "1.1"},
	}
	waves := applyWaves(updates, rwPins(nil))

	want := [][]int{{0, 1, 2, 3}}
	if !reflect.DeepEqual(waves, want) {
		t.Fatalf("applyWaves with no Requires = %v, want %v (one wave, input order — R6.3)", waves, want)
	}
}

func TestRequiresWavesOrdering(t *testing.T) {
	tilde := map[string]string{"dev-lang/dart": "~"}

	cases := []struct {
		name    string
		updates []autoupdate.PendingUpdate
		pins    map[string]string
		want    [][]int // compared with each wave sorted
	}{
		// --- Hostile: inputs that look like the required atom but are other
		// packages. An edge here would delay flutter for nothing.
		{
			name: "hostile collapse: dart-sass, dartx and dev-util/dart are not dev-lang/dart",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
				{Package: "dev-lang/dart-sass", NewVersion: "3.14.0"},
				{Package: "dev-lang/dartx@stable", NewVersion: "3.14.0"},
				{Package: "dev-util/dart:0", NewVersion: "3.14.0"},
			},
			pins: tilde,
			want: [][]int{{0, 1, 2, 3}},
		},
		{
			// A third entry for the SAME atom whose version does not satisfy
			// the pin must not become an edge. dart@beta is pushed to wave 1 by
			// its own requirement, so a wrong edge would push flutter to wave 2.
			name: "hostile collapse: a second dart key with a non-matching version adds no edge",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
				{Package: "dev-lang/dart@beta", NewVersion: "3.15.0_beta1", Requires: map[string]string{"sys-devel/llvm": "19.1.0"}},
				{Package: "dev-lang/dart@stable", NewVersion: "3.14.0"},
				{Package: "sys-devel/llvm", NewVersion: "19.1.0"},
			},
			pins: map[string]string{"dev-lang/dart": "~", "sys-devel/llvm": "~"},
			want: [][]int{{2, 3}, {0, 1}},
		},
		// --- Hostile: differently spelled keys that ARE the required atom.
		// Missing the edge here would apply flutter before its dart.
		{
			name: "hostile split: an @label key is the required atom",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
				{Package: "dev-lang/dart@stable", NewVersion: "3.14.0"},
			},
			pins: tilde,
			want: [][]int{{1}, {0}},
		},
		{
			name: "hostile split: a :slot key is the required atom",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
				{Package: "dev-lang/dart:0", NewVersion: "3.14.0"},
			},
			pins: tilde,
			want: [][]int{{1}, {0}},
		},
		{
			name: "hostile split: a :slot@label key is the required atom",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
				{Package: "dev-lang/dart:0@stable", NewVersion: "3.14.0"},
			},
			pins: tilde,
			want: [][]int{{1}, {0}},
		},
		// --- The pin decides whether the pending version satisfies the edge.
		{
			name: "pin ~ not satisfied by another version: no edge",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
				{Package: "dev-lang/dart@stable", NewVersion: "3.15.0"},
			},
			pins: tilde,
			want: [][]int{{0, 1}},
		},
		{
			name: "pin >= satisfied by a greater version: edge",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
				{Package: "dev-lang/dart@stable", NewVersion: "3.15.0"},
			},
			pins: map[string]string{"dev-lang/dart": ">="},
			want: [][]int{{1}, {0}},
		},
		{
			name: "pin >= not satisfied by a lower version: no edge",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
				{Package: "dev-lang/dart@stable", NewVersion: "3.13.9"},
			},
			pins: map[string]string{"dev-lang/dart": ">="},
			want: [][]int{{0, 1}},
		},
		{
			name: "pin = satisfied by the exact version: edge",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
				{Package: "dev-lang/dart@stable", NewVersion: "3.14.0"},
			},
			pins: map[string]string{"dev-lang/dart": "="},
			want: [][]int{{1}, {0}},
		},
		// --- Benign shapes.
		{
			name: "chain dart@stable then flutter, required entry first in input",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/dart@stable", NewVersion: "3.14.0"},
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
			},
			pins: tilde,
			want: [][]int{{0}, {1}},
		},
		{
			name: "three-link chain gives three waves",
			updates: []autoupdate.PendingUpdate{
				{Package: "app-misc/c", NewVersion: "3.0", Requires: map[string]string{"app-misc/b": "2.0"}},
				{Package: "app-misc/b", NewVersion: "2.0", Requires: map[string]string{"app-misc/a": "1.0"}},
				{Package: "app-misc/a", NewVersion: "1.0"},
			},
			pins: map[string]string{"app-misc/a": "~", "app-misc/b": "~"},
			want: [][]int{{2}, {1}, {0}},
		},
		{
			name: "requirement with no pending entry adds no edge",
			updates: []autoupdate.PendingUpdate{
				{Package: "dev-lang/flutter", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
				{Package: "app-misc/bar", NewVersion: "1.1"},
			},
			pins: tilde,
			want: [][]int{{0, 1}},
		},
		{
			name: "two-entry cycle lands in the final wave, after the free entry",
			updates: []autoupdate.PendingUpdate{
				{Package: "app-misc/aaa", NewVersion: "1.0", Requires: map[string]string{"app-misc/bbb": "1.0"}},
				{Package: "app-misc/bbb", NewVersion: "1.0", Requires: map[string]string{"app-misc/aaa": "1.0"}},
				{Package: "app-misc/free", NewVersion: "5.0"},
			},
			pins: map[string]string{"app-misc/aaa": "~", "app-misc/bbb": "~"},
			want: [][]int{{2}, {0, 1}},
		},
		{
			name: "two-entry cycle alone is one final wave",
			updates: []autoupdate.PendingUpdate{
				{Package: "app-misc/aaa", NewVersion: "1.0", Requires: map[string]string{"app-misc/bbb": "1.0"}},
				{Package: "app-misc/bbb", NewVersion: "1.0", Requires: map[string]string{"app-misc/aaa": "1.0"}},
			},
			pins: map[string]string{"app-misc/aaa": "~", "app-misc/bbb": "~"},
			want: [][]int{{0, 1}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			waves := applyWaves(tc.updates, rwPins(tc.pins))
			rwCheckPartition(t, waves, len(tc.updates))
			if got := rwSorted(waves); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("applyWaves = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- applyAllPackages over a real Applier -----------------------------------

const rwDartEbuild = "EAPI=8\nDESCRIPTION=\"dart\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\nLICENSE=\"BSD\"\n"

const rwFlutterEbuild = "EAPI=8\nDESCRIPTION=\"flutter\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\nLICENSE=\"BSD\"\n" +
	"RDEPEND=\"~dev-lang/dart-3.13.5\"\n"

const rwPlainEbuild = "EAPI=8\nDESCRIPTION=\"plain\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\nLICENSE=\"MIT\"\n"

func rwWriteEbuild(t *testing.T, overlay, rel, content string) {
	t.Helper()
	p := filepath.Join(overlay, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func rwExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// rwHermetic keeps the run off the host's config, home and ::gentoo tree.
func rwHermetic(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gentoo := t.TempDir()
	t.Setenv("BENTOO_GENTOO_REPO", gentoo)
	return gentoo
}

// rwRecord is a registry record; only Requires matters to the apply path.
func rwRecord(requires map[string]autoupdate.RequireSpec) autoupdate.PackageConfig {
	return autoupdate.PackageConfig{
		URL:      "https://example.invalid/releases.json",
		Parser:   "regex",
		Pattern:  `"version":\s*"([^"]+)"`,
		Requires: requires,
	}
}

var rwDartRequire = map[string]autoupdate.RequireSpec{
	"dev-lang/dart": {Pattern: `"dart_sdk_version":\s*"([^"]+)"`, Pin: "~"},
}

// rwApplier builds an Applier over overlay whose external commands run through
// factory, with an empty ::gentoo and a private distdir.
func rwApplier(t *testing.T, overlay, configDir, gentoo string, pending *autoupdate.PendingList,
	records map[string]autoupdate.PackageConfig, factory func(context.Context, string, ...string) *exec.Cmd,
) *autoupdate.Applier {
	t.Helper()
	applier, err := autoupdate.NewApplier(overlay, configDir,
		autoupdate.WithApplierPendingList(pending),
		autoupdate.WithExecCommand(factory),
		autoupdate.WithApplierDistdir(t.TempDir(), ""),
		autoupdate.WithApplierGentooPath(gentoo),
		autoupdate.WithApplierPackagesConfig(&autoupdate.PackagesConfig{Packages: records}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return applier
}

func rwTrue(ctx context.Context, _ string, _ ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
}

// rwFailIn returns a factory whose commands fail when run inside a package
// directory ending in dir (e.g. "/dev-lang/dart") and succeed elsewhere.
func rwFailIn(dir string) func(context.Context, string, ...string) *exec.Cmd {
	script := `case "$PWD" in *` + dir + `) exit 1;; esac; exit 0`
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
}

func rwAddPending(t *testing.T, pending *autoupdate.PendingList, updates []autoupdate.PendingUpdate) {
	t.Helper()
	for _, u := range updates {
		if err := pending.Add(u); err != nil {
			t.Fatalf("pending.Add %s: %v", u.Package, err)
		}
	}
}

func rwFindWaiting(r *autoupdate.ApplyResult, want string) bool {
	for _, w := range r.Waiting {
		if strings.Contains(w, want) {
			return true
		}
	}
	return false
}

// R6.1 + R6.5: the dependent is listed FIRST, so a planner that ran the input
// order would apply flutter before its dart. Results must still sit at the
// caller's indices.
//
// Concurrency 1 makes the input order observable: one worker takes flutter
// first unless the planner put dart in an earlier wave. Concurrency 4 covers
// the worker-pool path.
func TestRequiresWavesApplyAllKeepsResultsAtInputIndices(t *testing.T) {
	for _, concurrency := range []int{1, 4} {
		t.Run(fmt.Sprintf("concurrency=%d", concurrency), func(t *testing.T) {
			rwApplyAllKeepsResultsAtInputIndices(t, concurrency)
		})
	}
}

func rwApplyAllKeepsResultsAtInputIndices(t *testing.T, concurrency int) {
	t.Helper()
	gentoo := rwHermetic(t)
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")

	rwWriteEbuild(t, overlay, "dev-lang/flutter/flutter-3.47.6.ebuild", rwFlutterEbuild)
	rwWriteEbuild(t, overlay, "app-misc/indep/indep-1.0.ebuild", rwPlainEbuild)
	rwWriteEbuild(t, overlay, "dev-lang/dart/dart-3.13.5.ebuild", rwDartEbuild)

	updates := []autoupdate.PendingUpdate{
		{Package: "dev-lang/flutter", CurrentVersion: "3.47.6", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
		{Package: "app-misc/indep", CurrentVersion: "1.0", NewVersion: "1.1"},
		{Package: "dev-lang/dart@stable", CurrentVersion: "3.13.5", NewVersion: "3.14.0"},
	}
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	rwAddPending(t, pending, updates)

	records := map[string]autoupdate.PackageConfig{
		"dev-lang/flutter":     rwRecord(rwDartRequire),
		"app-misc/indep":       rwRecord(nil),
		"dev-lang/dart@stable": rwRecord(nil),
	}
	applier := rwApplier(t, overlay, configDir, gentoo, pending, records, rwTrue)

	results, failures := applyAllPackages(applier, updates, false, concurrency)

	if failures != 0 {
		t.Errorf("failures = %d, want 0", failures)
	}
	if len(results) != len(updates) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(updates))
	}
	wantNew := []string{"3.48.0", "1.1", "3.14.0"}
	for i, u := range updates {
		r := results[i]
		if r == nil {
			t.Errorf("results[%d] is nil (%s)", i, u.Package)
			continue
		}
		if r.Package != u.Package {
			t.Errorf("results[%d].Package = %q, want %q (result attributed to the wrong package)", i, r.Package, u.Package)
		}
		if r.NewVersion != wantNew[i] {
			t.Errorf("results[%d].NewVersion = %q, want %q", i, r.NewVersion, wantNew[i])
		}
		if !r.Success || len(r.Waiting) != 0 {
			t.Errorf("results[%d] (%s) Success=%v Waiting=%v Error=%v, want applied", i, u.Package, r.Success, r.Waiting, r.Error)
		}
	}
	if !rwExists(filepath.Join(overlay, "dev-lang/dart/dart-3.14.0.ebuild")) {
		t.Error("dart-3.14.0.ebuild missing: the required bump was not applied")
	}
	if !rwExists(filepath.Join(overlay, "dev-lang/flutter/flutter-3.48.0.ebuild")) {
		t.Error("flutter-3.48.0.ebuild missing: the dependent bump did not run after its requirement")
	}
}

// R6.2: the required bump FAILS during the run; the dependent is reported
// waiting, writes nothing and keeps its pending entry.
func TestRequiresWavesDependentWaitsWhenRequiredFails(t *testing.T) {
	gentoo := rwHermetic(t)
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")

	rwWriteEbuild(t, overlay, "dev-lang/flutter/flutter-3.47.6.ebuild", rwFlutterEbuild)
	rwWriteEbuild(t, overlay, "dev-lang/dart/dart-3.13.5.ebuild", rwDartEbuild)

	updates := []autoupdate.PendingUpdate{
		{Package: "dev-lang/flutter", CurrentVersion: "3.47.6", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
		{Package: "dev-lang/dart@stable", CurrentVersion: "3.13.5", NewVersion: "3.14.0"},
	}
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	rwAddPending(t, pending, updates)
	records := map[string]autoupdate.PackageConfig{
		"dev-lang/flutter":     rwRecord(rwDartRequire),
		"dev-lang/dart@stable": rwRecord(nil),
	}
	applier := rwApplier(t, overlay, configDir, gentoo, pending, records, rwFailIn("/dev-lang/dart"))

	results, failures := applyAllPackages(applier, updates, false, 4)

	if failures != 1 {
		t.Errorf("failures = %d, want 1 (dart failed; a waiting flutter is not a failure)", failures)
	}
	if len(results) != 2 || results[0] == nil || results[1] == nil {
		t.Fatalf("results = %v, want two non-nil results", results)
	}
	dart, flutter := results[1], results[0]
	if dart.Package != "dev-lang/dart@stable" || dart.Success || dart.Error == nil {
		t.Errorf("dart result = {Package:%q Success:%v Error:%v}, want a failure at index 1", dart.Package, dart.Success, dart.Error)
	}
	if flutter.Package != "dev-lang/flutter" {
		t.Errorf("results[0].Package = %q, want dev-lang/flutter", flutter.Package)
	}
	if flutter.Success || flutter.Error != nil || !rwFindWaiting(flutter, "~dev-lang/dart-3.14.0") {
		t.Errorf("flutter result Success=%v Error=%v Waiting=%v, want waiting on ~dev-lang/dart-3.14.0", flutter.Success, flutter.Error, flutter.Waiting)
	}
	if rwExists(filepath.Join(overlay, "dev-lang/flutter/flutter-3.48.0.ebuild")) {
		t.Error("flutter-3.48.0.ebuild was written although its requirement failed")
	}
	reloaded, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("reload pending: %v", err)
	}
	if !reloaded.Has("dev-lang/flutter") {
		t.Error("flutter pending entry dropped; a waiting bump must stay pending")
	}
}

// R6.2: the required bump WAITS on its own unmet requirement; the dependent
// waits too, and neither counts as a failure.
func TestRequiresWavesDependentWaitsWhenRequiredWaits(t *testing.T) {
	gentoo := rwHermetic(t)
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")

	rwWriteEbuild(t, overlay, "dev-lang/flutter/flutter-3.47.6.ebuild", rwFlutterEbuild)
	rwWriteEbuild(t, overlay, "dev-lang/dart/dart-3.13.5.ebuild", rwDartEbuild+"BDEPEND=\"~dev-util/dartgen-0.9\"\n")

	updates := []autoupdate.PendingUpdate{
		{Package: "dev-lang/flutter", CurrentVersion: "3.47.6", NewVersion: "3.48.0", Requires: map[string]string{"dev-lang/dart": "3.14.0"}},
		{Package: "dev-lang/dart@stable", CurrentVersion: "3.13.5", NewVersion: "3.14.0", Requires: map[string]string{"dev-util/dartgen": "1.0"}},
	}
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	rwAddPending(t, pending, updates)
	records := map[string]autoupdate.PackageConfig{
		"dev-lang/flutter": rwRecord(rwDartRequire),
		"dev-lang/dart@stable": rwRecord(map[string]autoupdate.RequireSpec{
			"dev-util/dartgen": {Pattern: `"dartgen":\s*"([^"]+)"`, Pin: "~"},
		}),
	}
	applier := rwApplier(t, overlay, configDir, gentoo, pending, records, rwTrue)

	results, failures := applyAllPackages(applier, updates, false, 4)

	if failures != 0 {
		t.Errorf("failures = %d, want 0 (waiting is not failing)", failures)
	}
	if len(results) != 2 || results[0] == nil || results[1] == nil {
		t.Fatalf("results = %v, want two non-nil results", results)
	}
	if r := results[1]; r.Package != "dev-lang/dart@stable" || r.Success || !rwFindWaiting(r, "~dev-util/dartgen-1.0") {
		t.Errorf("dart result {Package:%q Success:%v Waiting:%v}, want waiting on ~dev-util/dartgen-1.0", r.Package, r.Success, r.Waiting)
	}
	if r := results[0]; r.Package != "dev-lang/flutter" || r.Success || !rwFindWaiting(r, "~dev-lang/dart-3.14.0") {
		t.Errorf("flutter result {Package:%q Success:%v Waiting:%v}, want waiting on ~dev-lang/dart-3.14.0", r.Package, r.Success, r.Waiting)
	}
	for _, f := range []string{"dev-lang/dart/dart-3.14.0.ebuild", "dev-lang/flutter/flutter-3.48.0.ebuild"} {
		if rwExists(filepath.Join(overlay, f)) {
			t.Errorf("%s was written although its bump waits", f)
		}
	}
}

// R6.4: a cycle is attempted, not skipped; whatever is still unmet waits.
func TestRequiresWavesCycleAttemptsEveryEntry(t *testing.T) {
	gentoo := rwHermetic(t)
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")

	rwWriteEbuild(t, overlay, "app-misc/aaa/aaa-0.9.ebuild", rwPlainEbuild+"RDEPEND=\"~app-misc/bbb-0.9\"\n")
	rwWriteEbuild(t, overlay, "app-misc/bbb/bbb-0.9.ebuild", rwPlainEbuild+"RDEPEND=\"~app-misc/aaa-0.9\"\n")

	updates := []autoupdate.PendingUpdate{
		{Package: "app-misc/aaa", CurrentVersion: "0.9", NewVersion: "1.0", Requires: map[string]string{"app-misc/bbb": "1.0"}},
		{Package: "app-misc/bbb", CurrentVersion: "0.9", NewVersion: "1.0", Requires: map[string]string{"app-misc/aaa": "1.0"}},
	}
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	rwAddPending(t, pending, updates)
	records := map[string]autoupdate.PackageConfig{
		"app-misc/aaa": rwRecord(map[string]autoupdate.RequireSpec{"app-misc/bbb": {Pattern: `"b":\s*"([^"]+)"`, Pin: "~"}}),
		"app-misc/bbb": rwRecord(map[string]autoupdate.RequireSpec{"app-misc/aaa": {Pattern: `"a":\s*"([^"]+)"`, Pin: "~"}}),
	}
	applier := rwApplier(t, overlay, configDir, gentoo, pending, records, rwTrue)

	results, failures := applyAllPackages(applier, updates, false, 4)

	if failures != 0 {
		t.Errorf("failures = %d, want 0", failures)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	waiting := 0
	for i, u := range updates {
		r := results[i]
		if r == nil {
			t.Errorf("results[%d] is nil: cycle entry %s was never attempted", i, u.Package)
			continue
		}
		if r.Package != u.Package {
			t.Errorf("results[%d].Package = %q, want %q", i, r.Package, u.Package)
		}
		if r.Success == (len(r.Waiting) > 0) {
			t.Errorf("%s: Success=%v Waiting=%v, want exactly one of applied or waiting", u.Package, r.Success, r.Waiting)
		}
		if len(r.Waiting) > 0 {
			waiting++
		}
	}
	if waiting == 0 {
		t.Error("no cycle entry reported waiting; neither requirement was present when the cycle started")
	}
}
