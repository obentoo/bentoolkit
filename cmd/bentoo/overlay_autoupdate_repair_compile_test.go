package main

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
)

// TestRepairRevalidatesACompileFailureWithTheCompileGate: a bundle left by a
// failed compile gate is re-validated with the compile gate, which the adopted
// tree's depth floor alone does not reach. The gate's own confirmation prompt
// is the observable; the fixture's stdin is spent on the session question, so
// the gate is declined and the run still exits 1 with the bundle kept. The
// plain renderer is selected because the live one asks inside the TUI. An
// explicit --compile asks for the gate whatever the bundle's stage.
func TestRepairRevalidatesACompileFailureWithTheCompileGate(t *testing.T) {
	const compilePrompt = "Run compile test for " + repairPkg + "-" + repairCand
	for _, tc := range []struct {
		name        string
		stage       string
		flag        bool
		wantCompile bool
	}{
		{"gate:compile", "gate:compile", false, true},
		{"re-check", "re-check", false, false},
		{"gate:build", "gate:build", false, false},
		{"re-check with --compile", "re-check", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auOpts := testAutoupdateOptions()
			auOpts.noTUI = true
			auOpts.compile = tc.flag
			f := repairFixture(t, auOpts, 0)
			b, err := repair.ReadBundle(f.bundlePath)
			if err != nil {
				t.Fatal(err)
			}
			b.Stage = tc.stage
			if err := repair.WriteBundle(b); err != nil {
				t.Fatal(err)
			}
			code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
			if got := strings.Contains(out, compilePrompt); got != tc.wantCompile {
				t.Fatalf("stage %q: compile gate asked = %v, want %v (exit %d):\n%s", tc.stage, got, tc.wantCompile, code, out)
			}
			if tc.wantCompile {
				if code != 1 {
					t.Fatalf("a declined compile gate exited %d, want 1:\n%s", code, out)
				}
				if _, err := repair.ReadBundle(f.bundlePath); err != nil {
					t.Fatalf("bundle gone after a declined compile gate: %v", err)
				}
			}
		})
	}
}
