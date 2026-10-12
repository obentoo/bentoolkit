package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
)

// TestApplyKeepsRepairTreeApplyResultNamesRepairCommand: --apply on a package
// whose staged tree carries a bundle marked by a --repair session leaves the
// tree and its bundle as they were, and both the live failure row and the plain
// Apply Result name the --repair command.
func TestApplyKeepsRepairTreeApplyResultNamesRepairCommand(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 0)
	rewriteRepairBundle(t, f, func(b *repair.Bundle) { b.RepairedAt = time.Date(2020, 1, 1, 1, 0, 0, 0, time.UTC) })
	auOpts.repair = ""
	auOpts.apply = repairPkg
	candidate := filepath.Join(f.pkgDir, "repairdemo-"+repairCand+".ebuild")
	ebuildBefore := readFileOrEmpty(candidate)
	bundleBefore := readFileOrEmpty(f.bundlePath)

	code, out := runRepair(t, auOpts, ttyDeps(false, false), "")

	if code == 0 {
		t.Fatalf("--apply over a repair tree exited 0:\n%s", out)
	}
	repairCmd := "bentoo overlay autoupdate --repair " + repairPkg
	var row string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "["+repairPkg+"] ") && strings.Contains(line, "staged tree kept for repair") {
			row = line
		}
	}
	if !strings.Contains(row, repairCmd) {
		t.Fatalf("no live failure row naming %q:\n%s", repairCmd, out)
	}
	if !strings.Contains(out, "Repair:  "+repairCmd) {
		t.Fatalf("the Apply Result does not name %q:\n%s", repairCmd, out)
	}
	if got := readFileOrEmpty(candidate); got != ebuildBefore || !strings.Contains(got, "# repaired") {
		t.Fatalf("the repaired candidate changed:\n%s", got)
	}
	if got := readFileOrEmpty(f.bundlePath); got != bundleBefore {
		t.Fatalf("the repair bundle changed")
	}
	assertNoSession(t, f)
}

// TestApplyKeepsRepairTreeFailedRepairMarksBundle: a --repair whose
// re-validation fails leaves the bundle marked, so a later --apply keeps the
// tree.
func TestApplyKeepsRepairTreeFailedRepairMarksBundle(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 1)
	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
	if code != 1 {
		t.Fatalf("fixture: exit %d, want 1 from a failing re-validation:\n%s", code, out)
	}
	b, err := repair.ReadBundle(f.bundlePath)
	if err != nil {
		t.Fatalf("no bundle after a failed re-validation: %v", err)
	}
	if b.RepairedAt.IsZero() {
		t.Fatalf("the bundle carries no repair mark after a failed --repair: %+v", b)
	}
}
