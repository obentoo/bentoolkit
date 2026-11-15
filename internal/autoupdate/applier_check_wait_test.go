package autoupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestValidate_AnUpstream5xxDeclinesOverTheHost: a manifest step that failed
// because upstream answered 502 was the network's fault, so every declined
// gate names the host, and the report then reads the bump as WAIT rather than
// sending the maintainer to an ebuild with nothing wrong in it.
func TestValidate_AnUpstream5xxDeclinesOverTheHost(t *testing.T) {
	staging := filepath.Join(t.TempDir(), "staging")
	applier, pkg := checkFixture(t, staging, false)
	applier.execCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `printf 'ERROR 502: Bad Gateway.\n'; exit 1`)
	}

	result := applier.Validate(t.Context(), pkg, validate.DepthNone)

	skipped := 0
	for _, gate := range result.Gates {
		if gate.Outcome != validate.OutcomeSkipped {
			continue
		}
		skipped++
		if gate.Declined != validate.DeclineHost {
			t.Errorf("the %s gate declined over %q, want %q; reason: %s", gate.Gate, gate.Declined, validate.DeclineHost, gate.Reason)
		}
	}
	if skipped == 0 {
		t.Fatalf("no gate was declined, so the fixture never failed the manifest step: %+v", result.Gates)
	}

	// The record beside the retained tree says the same, by the same rule.
	records, err := filepath.Glob(filepath.Join(staging, "*", "*", "*", ".bentoo-stage-record.json"))
	if err != nil || len(records) != 1 {
		t.Fatalf("want exactly one stage record under %s, found %v (err %v)", staging, records, err)
	}
	rec, err := validate.ReadStageRecord(filepath.Dir(records[0]))
	if err != nil {
		t.Fatalf("ReadStageRecord: %v", err)
	}
	if rec.State != "WAIT" || rec.Backend != validate.BackendHost || rec.RecordedAt.IsZero() {
		t.Errorf("record state=%q backend=%q recorded_at=%v, want WAIT, %q and a time", rec.State, rec.Backend, rec.RecordedAt, validate.BackendHost)
	}
}

// TestHostEnvironment_NamesTheGentooSync: the record identifies the host by
// when its ::gentoo tree was synced, and says nothing when it cannot tell.
func TestHostEnvironment_NamesTheGentooSync(t *testing.T) {
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, "metadata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "metadata", "timestamp.chk"), []byte("Fri, 13 Nov 2026 18:31:03 +0000\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, want := hostEnvironment(tree), "::gentoo synced Fri, 13 Nov 2026 18:31:03 +0000"; got != want {
		t.Errorf("hostEnvironment = %q, want %q", got, want)
	}
	if got := hostEnvironment(t.TempDir()); got != "" {
		t.Errorf("a tree without timestamp.chk gave %q, want empty", got)
	}
}
