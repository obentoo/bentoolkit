package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// TestRepairHintPrintedOnlyForAFailureWithABundle covers R1.8: the hint names
// the failed package, and a failure without a bundle or a success has none.
func TestRepairHintPrintedOnlyForAFailureWithABundle(t *testing.T) {
	const hint = "bentoo overlay autoupdate --repair "
	cases := []struct {
		name   string
		result *autoupdate.ApplyResult
		want   bool
	}{
		{"failure with bundle", &autoupdate.ApplyResult{Package: "dev-go/alpha", OldVersion: "1", NewVersion: "2",
			Error: errors.New("manifest failed"), StagedPath: "/s/a", RepairBundle: "/s/a/.bentoo-repair.json"}, true},
		{"failure without bundle", &autoupdate.ApplyResult{Package: "dev-go/beta", OldVersion: "1", NewVersion: "2",
			Error: errors.New("manifest failed"), StagedPath: "/s/b"}, false},
		{"success", &autoupdate.ApplyResult{Package: "dev-go/gamma", OldVersion: "1", NewVersion: "2", Success: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() { testAutoupdateRun(testAutoupdateOptions()).displayApplyResult(tc.result) })
			got := strings.Contains(out, hint)
			if got != tc.want {
				t.Fatalf("hint present = %v, want %v:\n%s", got, tc.want, out)
			}
			if tc.want && !strings.Contains(out, hint+tc.result.Package) {
				t.Fatalf("hint does not name %s:\n%s", tc.result.Package, out)
			}
		})
	}
}
