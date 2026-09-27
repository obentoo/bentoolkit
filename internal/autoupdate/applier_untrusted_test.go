package autoupdate

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// assertRefusedBeforeWriting runs Apply over f in both write modes (straight
// into the overlay, and through a staging root) and checks the S050-R1.3/R1.4
// contract: the named sentinel, the package and the %q-quoted value in the
// message, StatusFailed with the error text in pending.json as read back from
// disk, a byte-identical package directory, and an empty staging root.
func assertRefusedBeforeWriting(t *testing.T, update PendingUpdate, value string, sentinel error) {
	t.Helper()
	for _, staged := range []bool{false, true} {
		t.Run(fmt.Sprintf("staged=%v", staged), func(t *testing.T) {
			f := newUntrustedFixture(t, update)
			stagingRoot := filepath.Join(f.configDir, "staging")
			var extra []ApplierOption
			if staged {
				extra = append(extra, WithApplierStagingRoot(stagingRoot))
			}
			a := f.applier(t, extra...)
			before := snapshotTree(t, f.overlayDir)

			result, err := a.Apply(f.pkg, false)
			if err == nil {
				t.Fatalf("Apply accepted %q", value)
			}
			if !errors.Is(err, sentinel) {
				t.Errorf("error = %v, want it to wrap %v", err, sentinel)
			}
			if result == nil || result.Success {
				t.Errorf("result = %+v, want a failed result", result)
			}
			if !strings.Contains(err.Error(), f.pkg) {
				t.Errorf("error %q does not name the package %s", err, f.pkg)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", value)) {
				t.Errorf("error %q does not name the value quoted as %q", err, value)
			}

			if after := snapshotTree(t, f.overlayDir); !reflect.DeepEqual(before, after) {
				t.Errorf("overlay changed on a refused value\nbefore: %v\n after: %v", snapshotPaths(before), snapshotPaths(after))
			}
			if staged {
				if left := snapshotTree(t, stagingRoot); len(left) != 0 {
					t.Errorf("a refused value left files in the staging tree: %v", snapshotPaths(left))
				}
			}

			reloaded, loadErr := NewPendingList(f.configDir)
			if loadErr != nil {
				t.Fatalf("reload pending: %v", loadErr)
			}
			got, ok := reloaded.Get(f.pkg)
			if !ok {
				t.Fatal("the refused package vanished from pending.json")
			}
			if got.Status != StatusFailed {
				t.Errorf("pending status = %q, want %q", got.Status, StatusFailed)
			}
			if got.Error == "" || !strings.Contains(got.Error, fmt.Sprintf("%q", value)) {
				t.Errorf("pending error = %q, want the error text naming %q", got.Error, value)
			}
		})
	}
}

// snapshotPaths lists a snapshot's paths for a readable failure message.
func snapshotPaths(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestApplyRejectsInvalidAuxValue pins S050-R1.1, R1.3 and R1.4. The first case
// is the audit's quote escape: written today, it closes MY_BUILD's quotes and
// leaves a shell command in an ebuild that is sourced as root. The 129-character
// value is the upper boundary; its 128-character neighbour is accepted in
// TestApplyAcceptsWellFormedAuxValue.
func TestApplyRejectsInvalidAuxValue(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"quote escape", `x"; touch /tmp/pwned; "`},
		{"template reference", "a${1}b"},
		{"129 characters", strings.Repeat("a", 129)},
		{"space", "esr bb24"},
		{"slash", "esr/bb24"},
		{"semicolon", "esr;bb24"},
		{"newline", "esr-bb24\nrm -rf /"},
		{"dollar", "$HOME"},
		{"backtick", "`id`"},
		{"non-ASCII letter", "esr-bb2é"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusedBeforeWriting(t, PendingUpdate{AuxValue: tc.value}, tc.value, ErrInvalidAuxValue)
		})
	}
}

// TestApplyRejectsInvalidCommitHash pins S050-R1.2, R1.3 and R1.4. Uppercase hex
// is refused rather than lowered (story assumption): the near-miss that a
// case-insensitive check would wrongly let through.
func TestApplyRejectsInvalidCommitHash(t *testing.T) {
	const good = "bc99a094bd926ae7b5ab8643947ce1438c950720"
	cases := []struct {
		name  string
		value string
	}{
		{"uppercase hex", strings.ToUpper(good)},
		{"39 characters", good[:39]},
		{"41 characters", good + "0"},
		{"non-hex letter", "g" + good[1:]},
		{"quote escape", good[:20] + `"; id; "`},
		{"template reference", good[:36] + "${1}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusedBeforeWriting(t, PendingUpdate{CommitHash: tc.value}, tc.value, ErrInvalidCommitHash)
		})
	}
}
