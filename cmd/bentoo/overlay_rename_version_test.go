package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// invalidRenameVersions are new versions ebuild.IsValidVersion rejects. The
// first is the audit's A10 path; the others are near misses of a version.
var invalidRenameVersions = []string{
	"1.2/../../../x",
	"latest",
	"1.2 beta",
	"../1.2",
	"1.2/x",
}

// TestParseRenameArgsRejectsInvalidNewVersion pins S050-R4.1, both directions.
// Hostile half first: a path or a word must be refused with ErrInvalidNewVersion
// naming the %q-quoted value. Converse: well-formed versions, including suffixed
// ones a too-tight check would wrongly refuse, still parse to the same value.
func TestParseRenameArgsRejectsInvalidNewVersion(t *testing.T) {
	for _, v := range invalidRenameVersions {
		t.Run("refuses "+v, func(t *testing.T) {
			spec, err := ParseRenameArgs([]string{"app-misc:foo:1.1", "=>", v})
			if err == nil {
				t.Fatalf("ParseRenameArgs accepted %q as a new version: %+v", v, spec)
			}
			if !errors.Is(err, ErrInvalidNewVersion) {
				t.Errorf("error = %v, want it to wrap ErrInvalidNewVersion", err)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", v)) {
				t.Errorf("error %q does not name the value quoted as %q", err, v)
			}
		})
	}

	for _, v := range []string{"1.1", "2.0.1", "1.2_rc1", "1.2_p20260923", "1.2b"} {
		t.Run("accepts "+v, func(t *testing.T) {
			spec, err := ParseRenameArgs([]string{"app-misc:foo:1.0", "=>", v})
			if err != nil {
				t.Fatalf("ParseRenameArgs refused the well-formed version %q: %v", v, err)
			}
			if spec.NewVersion != v {
				t.Errorf("NewVersion = %q, want %q", spec.NewVersion, v)
			}
		})
	}
}

// TestRunRenameRejectsInvalidNewVersion pins S050-R4.2: the command exits 1 on
// such an argument, before the overlay is scanned. The overlay holds a
// foo-1.1.ebuild the spec WOULD match, so a run that got as far as the preview
// returns normally in --dry-run mode (exit code -1 here) and, without it, moves
// the file; both modes must exit 1 and leave the directory byte-identical.
func TestRunRenameRejectsInvalidNewVersion(t *testing.T) {
	for _, v := range invalidRenameVersions {
		for _, dryRun := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/dry-run=%v", v, dryRun), func(t *testing.T) {
				pkgDir := renameTestHome(t, map[string]string{"foo-1.1.ebuild": "V11\n"})
				before := renameDirState(t, pkgDir)

				code, _, _ := runRenameObserved(t, renameFlagsSnapshot{dryRun: dryRun, yes: true, force: true}, "",
					[]string{"app-misc:foo:1.1", "=>", v})

				if code != 1 {
					t.Errorf("exit code = %d, want 1 for new version %q", code, v)
				}
				if after := renameDirState(t, pkgDir); !reflect.DeepEqual(before, after) {
					t.Errorf("the package directory changed\nbefore: %q\n after: %q", before, after)
				}
			})
		}
	}
}
