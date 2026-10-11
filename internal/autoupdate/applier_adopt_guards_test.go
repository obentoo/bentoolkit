package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// writeTestFile writes body at path, creating its directory.
func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertAdoptRefused checks a refused adopted apply: it failed naming the
// root and want, published nothing, and rewrote the bundle in stage "adopt".
func assertAdoptRefused(t *testing.T, a *Applier, overlayDir, pkg, staged, want string) {
	t.Helper()
	before := hashOverlayTree(t, overlayDir)
	result, err := a.Apply(t.Context(), pkg, false)
	if err == nil || result.Success {
		t.Fatalf("Apply promoted an adopted tree whose package files differ from the overlay")
	}
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), staged) {
		t.Fatalf("error does not name %q and the root %s: %v", want, staged, err)
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Fatalf("a refused adopted apply changed the published overlay")
	}
	b, berr := repair.ReadBundle(filepath.Join(staged, repair.BundleFile))
	if berr != nil {
		t.Fatalf("no bundle after the refusal: %v", berr)
	}
	if b.Stage != "adopt" || result.FailedStage != "adopt" {
		t.Fatalf("bundle stage %q, result stage %q, want adopt", b.Stage, result.FailedStage)
	}
}

// TestApplyAdoptedTreeRefusesPackageFilesTheOverlayDoesNotCarry: promotion
// publishes the ebuild and its Manifest only, so an adopted tree whose files/
// or metadata.xml differ from the overlay's is refused before any gate.
func TestApplyAdoptedTreeRefusesPackageFilesTheOverlayDoesNotCarry(t *testing.T) {
	const patch = "files/a.patch"
	tests := []struct {
		name      string
		published map[string]string
		staged    map[string]string
		want      string // "" means the apply must promote
	}{
		{name: "identical files promote", published: map[string]string{patch: "same\n"}, staged: map[string]string{patch: "same\n"}},
		{name: "published metadata.xml alone promotes", published: map[string]string{"metadata.xml": "<pkgmetadata/>\n"}},
		{name: "patch only in the staged tree", staged: map[string]string{"files/sub/fix.patch": "new\n"}, want: "files/sub"},
		{name: "patch edited in the staged tree", published: map[string]string{patch: "old\n"}, staged: map[string]string{patch: "new\n"}, want: patch},
		{name: "patch removed from the staged tree", published: map[string]string{patch: "old\n"}, want: patch},
		{name: "metadata.xml edited in the staged tree", published: map[string]string{"metadata.xml": "<a/>\n"}, staged: map[string]string{"metadata.xml": "<b/>\n"}, want: "metadata.xml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, overlayDir, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
			for rel, body := range tt.published {
				writeTestFile(t, filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", rel), body)
			}
			for rel, body := range tt.staged {
				writeTestFile(t, filepath.Join(staged, "media-plugins", "gst-plugins-qt6", rel), body)
			}
			if tt.want != "" {
				assertAdoptRefused(t, a, overlayDir, pkg, staged, tt.want)
				return
			}
			result, err := a.Apply(t.Context(), pkg, false)
			if err != nil || !result.Success {
				t.Fatalf("Apply refused an adopted tree whose package files match the overlay: %v", err)
			}
		})
	}
}

// TestApplyAdoptedTreeValidatesAtLeastTheRecordedDepth: with no reviewer to
// escalate, the depth the kept tree's record carries is a floor, never a cap.
func TestApplyAdoptedTreeValidatesAtLeastTheRecordedDepth(t *testing.T) {
	policyDepth := func(t *testing.T) string {
		t.Helper()
		a, _, pkg, _, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
		result, _ := a.Apply(t.Context(), pkg, false)
		return result.DepthRequested
	}(t)
	if policyDepth == validate.DepthInstall.String() {
		t.Fatalf("fixture: the policy already asks for install, so a floor cannot be observed")
	}

	for _, tc := range []struct {
		name     string
		recorded validate.Depth
		want     string
	}{
		{name: "a deeper record raises the depth", recorded: validate.DepthInstall, want: validate.DepthInstall.String()},
		{name: "a shallower record does not lower it", recorded: validate.DepthNone, want: policyDepth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
			if err := validate.WriteStageRecord(staged, validate.StageRecord{
				Package: pkg, Version: "1.29.2", ProducedBy: validate.ProducedByApplier, Depth: tc.recorded,
				Gates: []validate.GateResult{{Gate: validate.GateConfigure, Outcome: validate.OutcomeFailed, Reason: "recorded by the fixture"}},
			}); err != nil {
				t.Fatal(err)
			}
			result, _ := a.Apply(t.Context(), pkg, false)
			if result.DepthRequested != tc.want {
				t.Fatalf("DepthRequested = %q, want %q (reason: %s)", result.DepthRequested, tc.want, result.DepthReason)
			}
		})
	}

	t.Run("an unreadable record is an error naming the root", func(t *testing.T) {
		a, _, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
		// The record's file name, as validate/record.go spells it.
		writeTestFile(t, filepath.Join(staged, ".bentoo-stage-record.json"), "{not json")
		result, err := a.Apply(t.Context(), pkg, false)
		if err == nil || result.Success {
			t.Fatalf("Apply went on past an unreadable record")
		}
		if !strings.Contains(err.Error(), staged) {
			t.Fatalf("error does not name the root %s: %v", staged, err)
		}
	})
}

// TestApplyAdoptedTreeRefusesARootOutsideItsStagedPath: only this bump's own
// staged tree path may be adopted.
func TestApplyAdoptedTreeRefusesARootOutsideItsStagedPath(t *testing.T) {
	t.Run("the same path spelled with a trailing slash is adopted", func(t *testing.T) {
		a, _, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
		WithAdoptedStagedTree(staged + string(filepath.Separator))(a)
		result, err := a.Apply(t.Context(), pkg, false)
		if err != nil || !result.Success {
			t.Fatalf("Apply refused the bump's own staged tree: %v", err)
		}
	})

	t.Run("a copy elsewhere is refused naming both paths", func(t *testing.T) {
		a, overlayDir, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
		other := filepath.Join(a.StagingRoot(), "elsewhere", "1.29.2")
		if err := os.CopyFS(other, os.DirFS(staged)); err != nil {
			t.Fatal(err)
		}
		WithAdoptedStagedTree(other)(a)
		before := hashOverlayTree(t, overlayDir)
		result, err := a.Apply(t.Context(), pkg, false)
		if err == nil || result.Success {
			t.Fatalf("Apply adopted a tree outside the bump's staged path")
		}
		if !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), staged) {
			t.Fatalf("error does not name %s and %s: %v", other, staged, err)
		}
		if after := hashOverlayTree(t, overlayDir); after != before {
			t.Fatalf("a refused adoption changed the published overlay")
		}
	})

	t.Run("no staging root is a refusal", func(t *testing.T) {
		a, _, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
		a.stagingRoot = ""
		result, err := a.Apply(t.Context(), pkg, false)
		if err == nil || result.Success {
			t.Fatalf("Apply adopted a tree with no staging root configured")
		}
		if !strings.Contains(err.Error(), staged) {
			t.Fatalf("error does not name the root %s: %v", staged, err)
		}
	})
}
