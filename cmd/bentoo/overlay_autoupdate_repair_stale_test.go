package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
)

// rewriteRepairBundle rewrites the fixture's bundle in place through edit,
// leaving it where FindBundle looks whatever staged root it then names.
func rewriteRepairBundle(t *testing.T, f *repairFix, edit func(b *repair.Bundle)) {
	t.Helper()
	b, err := repair.ReadBundle(f.bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	edit(&b)
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.bundlePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// setRepairPending replaces the fixture's pending entry; an empty candidate
// removes it.
func setRepairPending(t *testing.T, f *repairFix, candidate string) {
	t.Helper()
	pending, err := autoupdate.NewPendingList(f.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if candidate == "" {
		err = pending.Delete(repairPkg)
	} else {
		err = pending.Add(autoupdate.PendingUpdate{Package: repairPkg, CurrentVersion: repairCurrent, NewVersion: candidate, Status: autoupdate.StatusFailed})
	}
	if err != nil {
		t.Fatal(err)
	}
}

// TestRepairRefusesAStaleBundleVersion covers R2.7, and a bundle that names a
// staged tree other than the one it lies in: both are refused with exit 1
// before any question, naming what disagrees.
func TestRepairRefusesAStaleBundleVersion(t *testing.T) {
	const otherCand = "1.2"
	cases := []struct {
		name  string
		setup func(t *testing.T, f *repairFix) (want []string)
	}{
		{"no pending update", func(t *testing.T, f *repairFix) []string {
			setRepairPending(t, f, "")
			// The phrase, not the word: the temp paths carry the subtest name.
			return []string{"no pending update", repairCand}
		}},
		{"pending candidate differs", func(t *testing.T, f *repairFix) []string {
			setRepairPending(t, f, otherCand)
			return []string{otherCand, repairCand}
		}},
		{"staged root elsewhere", func(t *testing.T, f *repairFix) []string {
			elsewhere := t.TempDir()
			rewriteRepairBundle(t, f, func(b *repair.Bundle) { b.StagedRoot = elsewhere })
			return []string{elsewhere, f.staged}
		}},
		{"bundle candidate is not its directory's", func(t *testing.T, f *repairFix) []string {
			setRepairPending(t, f, otherCand)
			rewriteRepairBundle(t, f, func(b *repair.Bundle) { b.Candidate = otherCand })
			return []string{otherCand, f.staged}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auOpts := testAutoupdateOptions()
			f := repairFixture(t, auOpts, 0)
			want := tc.setup(t, f)
			before := readFileOrEmpty(f.bundlePath)
			code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
			if code != 1 {
				t.Fatalf("exit %d, want 1:\n%s", code, out)
			}
			for _, w := range want {
				if !strings.Contains(out, w) {
					t.Errorf("refusal does not name %q:\n%s", w, out)
				}
			}
			if strings.Contains(out, "[y/N]") {
				t.Errorf("the refusal came after the confirmation question:\n%s", out)
			}
			assertNoSession(t, f)
			if readFileOrEmpty(f.bundlePath) != before {
				t.Fatalf("a refused --repair changed the bundle")
			}
		})
	}
}
