package autoupdate

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// fakeVMBackend runs every command through "fakevm run --", as a VM backend
// will through `shidashi vm run --`.
type fakeVMBackend struct{}

func (fakeVMBackend) Name() string { return "fakevm" }
func (fakeVMBackend) Wrap(name string, arg []string) (string, []string) {
	return "fakevm", append([]string{"run", "--", name}, arg...)
}

// TestBuildBackend_TheBuildChildrenRunInTheBackend: the build gates' argv and
// the dependency probe reach the backend wrapped, the manifest step stays on
// the host, and the stage record names the backend that proved the bump.
func TestBuildBackend_TheBuildChildrenRunInTheBackend(t *testing.T) {
	builds := 0
	a, pkg := scriptedBuildApplier(t, buildRun{output: cleanCompile}, &builds)
	var (
		mu       sync.Mutex
		commands [][]string
	)
	a.execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		mu.Lock()
		commands = append(commands, append([]string{name}, arg...))
		mu.Unlock()
		return exec.CommandContext(ctx, "true")
	}
	WithApplierBuildBackend(fakeVMBackend{})(a)

	a.Validate(t.Context(), pkg, validate.DepthCompile)

	var wrappedEbuild, wrappedProbe, hostManifest bool
	for _, c := range commands {
		line := strings.Join(c, " ")
		switch {
		case c[0] == "fakevm" && len(c) > 3 && c[3] == "ebuild":
			wrappedEbuild = true
		case c[0] == "fakevm" && len(c) > 3 && c[3] == "emerge":
			wrappedProbe = true
		case c[0] == "pkgdev":
			hostManifest = true
		case c[0] == "ebuild" || c[0] == "emerge":
			t.Errorf("a build child ran on the host, outside the backend: %s", line)
		}
	}
	if !wrappedEbuild {
		t.Errorf("no ebuild child reached the backend; commands: %v", commands)
	}
	if !wrappedProbe {
		t.Errorf("the dependency probe did not reach the backend; commands: %v", commands)
	}
	if !hostManifest {
		t.Errorf("the manifest step did not run on the host; commands: %v", commands)
	}

	records, err := filepath.Glob(filepath.Join(a.StagingRoot(), "*", "*", "*", ".bentoo-stage-record.json"))
	if err != nil || len(records) != 1 {
		t.Fatalf("want one stage record, found %v (err %v)", records, err)
	}
	rec, err := validate.ReadStageRecord(filepath.Dir(records[0]))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Backend != "fakevm" {
		t.Errorf("the record names backend %q, want the one that ran the gates", rec.Backend)
	}
}
