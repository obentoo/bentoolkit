package autoupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

const repairBundlePkg = "media-plugins/gst-plugins-qt6"

// TestApplyFailureWritesRepairBundleBesideTheKeptTree covers R1.1, R1.2, R1.5
// and R1.8's data: every field is filled from the failed run.
func TestApplyFailureWritesRepairBundleBesideTheKeptTree(t *testing.T) {
	start := time.Now().Add(-time.Second)
	cfg := &registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
		repairBundlePkg: {URL: "https://api.example.org/repos/gst/tags", FallbackURL: "https://mirror.example.org/gst"},
	}}
	f := &stageFixer{res: fixer.ManifestFixResult{Summary: "tried", DeniedTools: []string{"Bash", "WebFetch(raw.example.org)"}}}
	result := failedStagedApply(t, WithApplierFixer(f), WithApplierPackagesConfig(cfg))

	path := filepath.Join(result.StagedPath, repair.BundleFile)
	if result.RepairBundle != path {
		t.Fatalf("RepairBundle = %q, want %q", result.RepairBundle, path)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("no bundle beside the kept tree: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("bundle mode %o, want 600", st.Mode().Perm())
	}
	b, err := repair.ReadBundle(path)
	if err != nil {
		t.Fatalf("ReadBundle: %v", err)
	}
	if b.Schema != repair.SchemaVersion || b.Package != repairBundlePkg || b.CurrentVersion != "1.28.6" || b.Candidate != "1.29.2" {
		t.Errorf("identity fields = %d %q %q %q", b.Schema, b.Package, b.CurrentVersion, b.Candidate)
	}
	if b.Stage == "" || b.Stage != result.FailedStage {
		t.Errorf("Stage = %q, want the result's FailedStage %q", b.Stage, result.FailedStage)
	}
	if b.Failure == "" || !strings.Contains(result.Error.Error(), firstLine(b.Failure)) {
		t.Errorf("Failure %q is not the apply's failure text %q", b.Failure, result.Error)
	}
	if !slices.Equal(b.RefusedTools, []string{"Bash", "WebFetch(raw.example.org)"}) {
		t.Errorf("RefusedTools = %q", b.RefusedTools)
	}
	if b.StagedRoot != result.StagedPath {
		t.Errorf("StagedRoot = %q, want %q", b.StagedRoot, result.StagedPath)
	}
	wantEbuild := filepath.Join(result.StagedPath, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	if b.EbuildPath != wantEbuild {
		t.Errorf("EbuildPath = %q, want %q", b.EbuildPath, wantEbuild)
	}
	if !slices.Equal(b.UpstreamURLs, cfg.Packages[repairBundlePkg].UpstreamURLs()) {
		t.Errorf("UpstreamURLs = %q, want %q", b.UpstreamURLs, cfg.Packages[repairBundlePkg].UpstreamURLs())
	}
	if b.Created.Before(start) || b.Created.After(time.Now().Add(time.Second)) {
		t.Errorf("Created = %v, not the time of this run", b.Created)
	}
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// TestApplyFailureRepairBundleRedactsResolvedSecrets covers R1.3: a resolved
// token echoed by the failing child never reaches the bundle.
func TestApplyFailureRepairBundleRedactsResolvedSecrets(t *testing.T) {
	const name = "BENTOO_TEST_REPAIR_BUNDLE_TOKEN"
	const token = "zq-repair-bundle-token-7f3c9a"
	t.Setenv(name, token)
	if got, found, err := secrets.Lookup(name); err != nil || !found || got != token {
		t.Fatalf("fixture: secret lookup = %q %v %v", got, found, err)
	}
	echoing := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo 'GET https://x.example.org/a?token="+token+" 404' >&2; exit 1")
	}
	result := failedStagedApply(t, WithExecCommand(echoing))
	if !strings.Contains(result.Error.Error(), token) {
		t.Skipf("fixture: the failure text does not carry the child's output (%v); redaction cannot be observed here", result.Error)
	}
	data, err := os.ReadFile(filepath.Join(result.StagedPath, repair.BundleFile))
	if err != nil {
		t.Fatalf("no bundle written: %v", err)
	}
	if bytes.Contains(data, []byte(token)) {
		t.Fatalf("the bundle carries the resolved secret in clear:\n%s", data)
	}
	if !bytes.Contains(data, []byte("***")) {
		t.Fatalf("the bundle does not carry the logger's mask where the secret was:\n%s", data)
	}
}

// TestApplyFailureWithoutStagedTreeWritesNoBundle: no staged tree, no bundle —
// and never one in the published overlay or the config dir.
func TestApplyFailureWithoutStagedTreeWritesNoBundle(t *testing.T) {
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	createTestEbuildFile(t, overlayDir, repairBundlePkg, "1.28.6")
	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Add(PendingUpdate{Package: repairBundlePkg, CurrentVersion: "1.28.6", NewVersion: "1.29.2", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	a, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithApplierDistdir(t.TempDir(), ""),
		WithConfirmFunc(func(string) bool { return true }),
		WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return exec.CommandContext(ctx, "false") }),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Apply(t.Context(), repairBundlePkg, false)
	if err == nil || result.Success {
		t.Fatalf("fixture: the bump did not fail")
	}
	if result.StagedPath != "" {
		t.Fatalf("fixture: an applier without a staging root kept a staged tree %q", result.StagedPath)
	}
	if result.RepairBundle != "" {
		t.Fatalf("RepairBundle = %q with no staged tree", result.RepairBundle)
	}
	walkErr := filepath.WalkDir(tmp, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if filepath.Base(p) == repair.BundleFile {
			return fmt.Errorf("bundle written at %s", p)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
}

// failApplyWith calls failApply on a fresh applier whose log goes to buf.
func failApplyWith(t *testing.T, stagedPath string, buf *bytes.Buffer) (*ApplyResult, error) {
	t.Helper()
	configDir := t.TempDir()
	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Add(PendingUpdate{Package: repairBundlePkg, CurrentVersion: "1.28.6", NewVersion: "1.29.2", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	a, err := NewApplier(t.TempDir(), configDir,
		WithApplierPendingList(pending),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierLogger(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	)
	if err != nil {
		t.Fatal(err)
	}
	res := &ApplyResult{Package: repairBundlePkg, OldVersion: "1.28.6", NewVersion: "1.29.2", StagedPath: stagedPath, FailedStage: "manifest"}
	return a.failApply(repairBundlePkg, res, fmt.Errorf("%w: digest mismatch", ErrManifestFailed))
}

// TestFailApplyBundleWriteFailureWarnsAndKeepsTheOutcome covers R1.6 and R1.7:
// a bundle that cannot be written is a WARN naming package and path, and the
// returned error is exactly what it would have been with no bundle at all.
func TestFailApplyBundleWriteFailureWarnsAndKeepsTheOutcome(t *testing.T) {
	var noBundleLog bytes.Buffer
	_, baseline := failApplyWith(t, "", &noBundleLog)

	notADir := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, staged := range []string{filepath.Join(t.TempDir(), "vanished", "1.29.2"), notADir} {
		var buf bytes.Buffer
		res, err := failApplyWith(t, staged, &buf)
		if err == nil || !errors.Is(err, ErrManifestFailed) {
			t.Fatalf("staged %s: failApply error %v lost the original", staged, err)
		}
		if err.Error() != baseline.Error() || res.Error.Error() != baseline.Error() {
			t.Fatalf("staged %s: outcome changed by the bundle failure:\n got %q\nwant %q", staged, err, baseline)
		}
		if res.Success {
			t.Fatalf("staged %s: a failure turned into a success", staged)
		}
		if res.RepairBundle != "" {
			t.Fatalf("staged %s: RepairBundle = %q although nothing was written", staged, res.RepairBundle)
		}
		warn := regexp.MustCompile(`(?m)^.*level=WARN.*$`).FindAllString(buf.String(), -1)
		found := false
		for _, l := range warn {
			if strings.Contains(l, repairBundlePkg) && strings.Contains(l, staged) {
				found = true
			}
		}
		if !found {
			t.Fatalf("staged %s: no WARN naming the package and the path; log:\n%s", staged, buf.String())
		}
	}
}

// TestFailApplyWritesTheBundleAfterTheStatus: the hook runs on the single
// failure exit and does not alter the recorded status or error.
func TestFailApplyWritesTheBundleAfterTheStatus(t *testing.T) {
	var buf bytes.Buffer
	_, baseline := failApplyWith(t, "", &buf)
	staged := t.TempDir()
	res, err := failApplyWith(t, staged, &buf)
	if err == nil || err.Error() != baseline.Error() {
		t.Fatalf("failApply error %v, want %v", err, baseline)
	}
	path := filepath.Join(staged, repair.BundleFile)
	if res.RepairBundle != path {
		t.Fatalf("RepairBundle = %q, want %q", res.RepairBundle, path)
	}
	b, rerr := repair.ReadBundle(path)
	if rerr != nil {
		t.Fatalf("ReadBundle: %v", rerr)
	}
	if b.Stage != "manifest" || b.Package != repairBundlePkg || !strings.Contains(b.Failure, "digest mismatch") {
		t.Fatalf("bundle = %+v", b)
	}
}
