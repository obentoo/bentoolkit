package main

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
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
	for _, stage := range []string{"gate:compile", "re-check", "gate:build"} {
		t.Run(stage, func(t *testing.T) {
			auOpts := testAutoupdateOptions()
			auOpts.noTUI = true
			f := repairFixture(t, auOpts, 0)
			b, err := repair.ReadBundle(f.bundlePath)
			if err != nil {
				t.Fatal(err)
			}
			b.Stage = stage
			if err := repair.WriteBundle(b); err != nil {
				t.Fatal(err)
			}
			code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
			if strings.Contains(out, compilePrompt) {
				t.Fatalf("stage %q asked for the privileged compile gate, which is gone (exit %d):\n%s", stage, code, out)
			}
		})
	}

	t.Run("--compile is refused", func(t *testing.T) {
		auOpts := testAutoupdateOptions()
		auOpts.noTUI = true
		auOpts.compile = true
		repairFixture(t, auOpts, 0)
		code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
		if code != 1 || !strings.Contains(out, "--compile was removed") {
			t.Fatalf("--repair --compile exited %d without the removal notice:\n%s", code, out)
		}
	})
}

// TestRepairDepth_RaisesACompileFailureToTheCompileDepth: a bundle that failed
// at the compile gate re-validates at --depth=compile on the unprivileged
// ladder, unless the operator already asked for as much or more.
func TestRepairDepth_RaisesACompileFailureToTheCompileDepth(t *testing.T) {
	install, configure := validate.DepthInstall, validate.DepthConfigure
	for _, tc := range []struct {
		stage string
		asked *validate.Depth
		raise bool
	}{
		{"gate:compile", nil, true},
		{"gate:compile", &configure, true},
		{"gate:compile", &install, false},
		{"re-check", nil, false},
	} {
		depth, raise := repairDepth(repair.Bundle{Stage: tc.stage}, tc.asked)
		if raise != tc.raise || (raise && depth != validate.DepthCompile) {
			t.Errorf("stage %q asked %v: repairDepth = %v, %v; want raise=%v to compile", tc.stage, tc.asked, depth, raise, tc.raise)
		}
	}
}
