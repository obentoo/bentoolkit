package main

import (
	"context"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// batchBundles reads the two bundles offerFixture and addSecondRepair wrote.
func batchBundles(t *testing.T, f *repairFix, second string) []repair.Bundle {
	t.Helper()
	var bundles []repair.Bundle
	for _, path := range []string{f.bundlePath, second} {
		b, err := repair.ReadBundle(path)
		if err != nil {
			t.Fatal(err)
		}
		bundles = append(bundles, b)
	}
	return bundles
}

// TestRevalidateBatch_EveryBundleGetsItsBlockAndAReason: in the 2026-11-28
// batch of seven, bundles appeared as ✗ in the summary with no block of their
// own. Here the second bundle's re-validation reaches a verdict that is not
// READY: its block is headed by its "[i/N]" label after the live region, and
// its summary line names the state.
func TestRevalidateBatch_EveryBundleGetsItsBlockAndAReason(t *testing.T) {
	f, ar, _ := offerFixture(t, true)
	second := addSecondRepair(t, f)
	bundles := batchBundles(t, f, second)
	pending, err := autoupdate.NewPendingList(f.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Delete(batchPkg2); err != nil {
		t.Fatal(err)
	}

	var ok bool
	out := captureStdout(t, func() {
		ok = ar.revalidateBatch(context.Background(), bundles, f.overlay, f.configDir, config.AutoupdateConfig{})
	})

	if ok {
		t.Fatalf("a batch with an unproven bundle reported success; output:\n%s", out)
	}
	for _, want := range []string{
		"[1/2] " + repairPkg,
		"Re-validation: [1/2] " + repairPkg,
		"[2/2] " + batchPkg2,
		"Re-validation: [2/2] " + batchPkg2,
		"✓ " + repairPkg,
		"✗ " + batchPkg2 + " — HUMAN",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q; output:\n%s", want, out)
		}
	}
}

// TestRevalidateBatch_ARunStoppingStatesItForEachBundle: a run that stops
// before a bundle is reached still prints that bundle's block, saying why it
// was not re-validated, and the summary carries the same reason. It used to
// mark every remaining bundle ✗ without a line about it.
func TestRevalidateBatch_ARunStoppingStatesItForEachBundle(t *testing.T) {
	f, ar, _ := offerFixture(t, true)
	second := addSecondRepair(t, f)
	bundles := batchBundles(t, f, second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var ok bool
	out := captureStdout(t, func() {
		ok = ar.revalidateBatch(ctx, bundles, f.overlay, f.configDir, config.AutoupdateConfig{})
	})

	if ok {
		t.Fatal("a stopped batch reported success")
	}
	for i, b := range bundles {
		if label := "[" + string(rune('1'+i)) + "/2] " + b.Package; !strings.Contains(out, label) {
			t.Errorf("the output lacks the block %q; output:\n%s", label, out)
		}
		if want := "✗ " + b.Package + " — not re-validated: the run is stopping"; !strings.Contains(out, want) {
			t.Errorf("the summary lacks %q; output:\n%s", want, out)
		}
		if !strings.Contains(readFileOrEmpty(b.StagedRoot+"/"+repair.BundleFile), "repaired_at") {
			t.Errorf("the bundle of %s was not marked repaired", b.Package)
		}
	}
}
