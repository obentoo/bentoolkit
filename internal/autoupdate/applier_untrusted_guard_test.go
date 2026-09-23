package autoupdate

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// untrustedFixture is one package ready for Apply: an ebuild at oldVersion that
// carries both an aux assignment and a commit variable, a pending update the
// caller fills in, and a packages config declaring the aux variable.
type untrustedFixture struct {
	pkg        string
	oldVersion string
	newVersion string
	overlayDir string
	configDir  string
	pkgDir     string
	pending    *PendingList
}

const (
	untrustedOldAux  = "esr-bb23"
	untrustedOldHash = "503d0d3d3858f463973f2cfce4a3aa0173567500"
)

// untrustedEbuildBody is the source ebuild every fixture starts from.
func untrustedEbuildBody() string {
	return "EAPI=8\n" +
		"DESCRIPTION=\"Test package\"\n" +
		"HOMEPAGE=\"https://example.com\"\n" +
		"MY_BUILD=\"" + untrustedOldAux + "\"\n" +
		"EGIT_COMMIT=\"" + untrustedOldHash + "\"\n" +
		"SRC_URI=\"https://example.com/${PV}${MY_BUILD}/${EGIT_COMMIT}.tar.gz\"\n" +
		"LICENSE=\"MIT\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"
}

// newUntrustedFixture lays out the overlay and queues update (Package,
// CurrentVersion, NewVersion and Status are filled in here).
func newUntrustedFixture(t *testing.T, update PendingUpdate) *untrustedFixture {
	t.Helper()
	tmp := t.TempDir()
	f := &untrustedFixture{
		pkg:        "mail-client/betterbird-bin",
		oldVersion: "128.6.0",
		newVersion: "128.7.0",
		overlayDir: filepath.Join(tmp, "overlay"),
		configDir:  filepath.Join(tmp, "config"),
	}
	f.pkgDir = filepath.Join(f.overlayDir, "mail-client", "betterbird-bin")
	if err := os.MkdirAll(f.pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir package dir: %v", err)
	}
	src := filepath.Join(f.pkgDir, "betterbird-bin-"+f.oldVersion+".ebuild")
	if err := os.WriteFile(src, []byte(untrustedEbuildBody()), 0o644); err != nil {
		t.Fatalf("write source ebuild: %v", err)
	}
	pending, err := NewPendingList(f.configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	update.Package = f.pkg
	update.CurrentVersion = f.oldVersion
	update.NewVersion = f.newVersion
	update.Status = StatusPending
	if err := pending.Add(update); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}
	f.pending = pending
	return f
}

// applier builds an Applier over the fixture; extra options are appended.
func (f *untrustedFixture) applier(t *testing.T, extra ...ApplierOption) *Applier {
	t.Helper()
	cfg := &PackagesConfig{Packages: map[string]PackageConfig{f.pkg: {
		Parser:     "regex",
		URL:        "https://example.invalid/betterbird",
		Pattern:    `Betterbird ([0-9.]+)`,
		AuxVar:     "MY_BUILD",
		AuxPattern: `Betterbird [0-9.]+(esr-bb[0-9]+)`,
	}}}
	opts := append([]ApplierOption{
		WithApplierPendingList(f.pending),
		WithApplierPackagesConfig(cfg),
		WithExecCommand(mockExecCommandSuccess),
	}, extra...)
	a, err := NewApplier(f.overlayDir, f.configDir, opts...)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return a
}

// snapshotTree maps every regular file under root (relative path) to its bytes.
// A missing root yields an empty map.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

// TestApplyAcceptsWellFormedAuxValue is the hostile half of the S050-R1.1/R1.2
// gate: values the allow-lists must NOT refuse. The aux values are the real ones
// (betterbird esr-bb24, nxplayer MY_BUILD 1 and 2), the 128-character boundary
// and every punctuation mark the class admits; the hash is 40 lowercase hex. Each
// must apply and reach the new ebuild byte for byte. Green today, and must stay
// green once the gate lands.
func TestApplyAcceptsWellFormedAuxValue(t *testing.T) {
	const goodHash = "bc99a094bd926ae7b5ab8643947ce1438c950720"
	cases := []struct {
		name   string
		update PendingUpdate
		want   string
	}{
		{"betterbird esr-bb24", PendingUpdate{AuxValue: "esr-bb24"}, `MY_BUILD="esr-bb24"`},
		{"nxplayer build 1", PendingUpdate{AuxValue: "1"}, `MY_BUILD="1"`},
		{"nxplayer build 2", PendingUpdate{AuxValue: "2"}, `MY_BUILD="2"`},
		{"128 characters", PendingUpdate{AuxValue: strings.Repeat("a", 128)}, `MY_BUILD="` + strings.Repeat("a", 128) + `"`},
		{"every admitted punctuation", PendingUpdate{AuxValue: "A-z_0.9+b"}, `MY_BUILD="A-z_0.9+b"`},
		{"lowercase 40-hex commit", PendingUpdate{CommitHash: goodHash}, `EGIT_COMMIT="` + goodHash + `"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUntrustedFixture(t, tc.update)
			a := f.applier(t)
			result, err := a.Apply(f.pkg, false)
			if err != nil {
				t.Fatalf("Apply refused a well-formed value: %v", err)
			}
			if !result.Success {
				t.Fatalf("Apply did not succeed: %+v", result)
			}
			got, readErr := os.ReadFile(a.EbuildPath(f.pkg, f.newVersion))
			if readErr != nil {
				t.Fatalf("new ebuild not written: %v", readErr)
			}
			if !strings.Contains(string(got), tc.want+"\n") {
				t.Errorf("new ebuild lacks %s:\n%s", tc.want, got)
			}
		})
	}
}
