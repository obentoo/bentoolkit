package autoupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

const staleMirrorPkg = "mail-client/thunderbird"

// thunderbirdGentoo is ::gentoo's ebuild of a release: its patchset and
// dependency floors move with every version, as mail-client/thunderbird's do.
func thunderbirdGentoo(series, rust, nss string) string {
	return "# Copyright 1999-2026 Gentoo Authors\n# Distributed under the terms of the GNU General Public License v2\n\nEAPI=8\n\n" +
		"FIREFOX_PATCHSET=\"firefox-" + series + "-patches-01.tar.xz\"\n" +
		"PYTHON_COMPAT=( python3_{11..14} )\nRUST_MIN_VER=\"" + rust + "\"\n" +
		"inherit python-any-r1\n\nSLOT=\"0\"\nKEYWORDS=\"amd64 arm64 ~ppc64 x86\"\n" +
		"RDEPEND=\">=dev-libs/nss-" + nss + "\"\n"
}

// thunderbirdMirror is our mirror of ::gentoo's 160.0: the header, a
// divergence tag and a wider PYTHON_COMPAT, nothing else.
var thunderbirdMirror = strings.Replace(
	strings.Replace(thunderbirdGentoo("160", "1.88.0", "3.115"), "\nEAPI=8",
		"\n# Mirrored from ::gentoo at the same version only to widen PYTHON_COMPAT.\n# BENTOO-DIVERGENCE: PYTHON_COMPAT - adds python3_15.\nEAPI=8", 1),
	"python3_{11..14}", "python3_{11..15}", 1)

// staleMirrorApplier is the 2026-11-28 shape: our 160.0 mirror, ::gentoo at
// 160.0 and 161.0, and a pending 160.0 -> 161.0 bump, staged as --check
// stages it.
func staleMirrorApplier(t *testing.T, ours string, cfgs map[string]registry.PackageConfig) (*Applier, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	overlay, gentoo, config := filepath.Join(tmp, "overlay"), filepath.Join(tmp, "gentoo"), filepath.Join(tmp, "config")
	dir := filepath.Join(gentoo, "mail-client", "thunderbird")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for v, body := range map[string]string{
		"160.0": thunderbirdGentoo("160", "1.88.0", "3.115"),
		"161.0": thunderbirdGentoo("161", "1.89.0", "3.117"),
	} {
		if err := os.WriteFile(filepath.Join(dir, "thunderbird-"+v+".ebuild"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	createTestEbuildFileWithContent(t, overlay, staleMirrorPkg, "160.0", ours)
	pending, err := NewPendingList(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Add(PendingUpdate{Package: staleMirrorPkg, CurrentVersion: "160.0", NewVersion: "161.0", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	applier, err := NewApplier(overlay, config,
		WithApplierPendingList(pending),
		WithApplierGentooPath(gentoo),
		WithApplierPackagesConfig(&registry.PackagesConfig{Packages: cfgs}),
		WithExecCommand((&execRecorder{}).command),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierRunCaptured(capturedFrom(func(*exec.Cmd) ([]byte, error) { return []byte(cleanCompile), nil })),
	)
	if err != nil {
		t.Fatal(err)
	}
	return applier, filepath.Join(dir, "thunderbird-161.0.ebuild")
}

// TestValidate_AMirrorBumpedFromOurOldCopyIsRefusedWhenGentooShipsIt: the
// bump copied our 160.0 to 161.0 while ::gentoo shipped 161.0, so the staged
// file carried firefox-160's patchset and 160's floors. It must not be proven
// (and therefore never published), and the reason must point at ::gentoo's
// copy to start from.
func TestValidate_AMirrorBumpedFromOurOldCopyIsRefusedWhenGentooShipsIt(t *testing.T) {
	for name, tc := range map[string]struct {
		ours string
		cfgs map[string]registry.PackageConfig
	}{
		"declared by the ebuild header": {ours: thunderbirdMirror},
		"declared only by the registry's patched text": {
			ours: strings.Replace(thunderbirdMirror, "# Mirrored from ::gentoo", "# Copied", 1),
			cfgs: map[string]registry.PackageConfig{staleMirrorPkg: {Patched: "Mirrored from ::gentoo at the same version only to widen PYTHON_COMPAT"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			applier, theirs := staleMirrorApplier(t, tc.ours, tc.cfgs)

			res := applier.Validate(t.Context(), staleMirrorPkg, validate.DepthCompile)

			if res.WorstOutcome() == validate.OutcomePass {
				t.Fatalf("a stale mirror was proven: %+v", res.Gates)
			}
			var declined bool
			for _, g := range res.Gates {
				if g.Declined == validate.DeclineCandidate && strings.Contains(g.Reason, theirs) && strings.Contains(g.Reason, "same-version mirror") {
					declined = true
				}
			}
			if !declined {
				t.Errorf("no gate declined over the candidate naming %s: %+v", theirs, res.Gates)
			}
		})
	}
}

// TestValidate_AMirrorRederivedFromGentooIsNotRefused: the same bump is fine
// when our copy is version-agnostic, so carrying it forward IS ::gentoo's
// 161.0 plus the declared divergence.
func TestValidate_AMirrorRederivedFromGentooIsNotRefused(t *testing.T) {
	applier, theirs := staleMirrorApplier(t, thunderbirdMirror, nil)
	if err := os.WriteFile(theirs, []byte(thunderbirdGentoo("160", "1.88.0", "3.115")), 0o644); err != nil {
		t.Fatal(err)
	}

	res := applier.Validate(t.Context(), staleMirrorPkg, validate.DepthCompile)

	for _, g := range res.Gates {
		if strings.Contains(g.Reason, "same-version mirror") {
			t.Fatalf("a mirror derived from ::gentoo was refused: %+v", g)
		}
	}
}

func TestDerivedFromGentoo(t *testing.T) {
	gentoo := thunderbirdGentoo("161", "1.89.0", "3.117")
	rederived := strings.Replace(gentoo, "python3_{11..14}", "python3_{11..15}", 1) + "# BENTOO-DIVERGENCE: PYTHON_COMPAT\n"
	if !derivedFromGentoo([]byte(rederived), []byte(gentoo)) {
		t.Error("::gentoo's copy with a wider PYTHON_COMPAT and a comment is derived from it")
	}
	if derivedFromGentoo([]byte(thunderbirdMirror), []byte(gentoo)) {
		t.Error("our 160.0 mirror is not derived from ::gentoo's 161.0")
	}
}

func TestGentooEbuildFor(t *testing.T) {
	g := t.TempDir()
	dir := filepath.Join(g, "app-misc", "p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"p-1.0.ebuild", "p-1.1-r2.ebuild", "p-1.1-r10.ebuild", "p-1.10.ebuild"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for version, want := range map[string]string{
		"1.0":    "p-1.0.ebuild",
		"1.1":    "p-1.1-r10.ebuild",
		"1.1-r2": "p-1.1-r2.ebuild",
		"1.2":    "",
	} {
		got := gentooEbuildFor(g, "app-misc", "p", version)
		if want != "" {
			want = filepath.Join(dir, want)
		}
		if got != want {
			t.Errorf("gentooEbuildFor(%s) = %q, want %q", version, got, want)
		}
	}
	if gentooEbuildFor("", "app-misc", "p", "1.0") != "" {
		t.Error("no tree must answer no ebuild")
	}
}
