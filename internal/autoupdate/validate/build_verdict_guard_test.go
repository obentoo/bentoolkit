package validate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Story 054, R7.3 — regression guards, GREEN today. Captured output shown to a
// human is capped at 64 KiB (R7.1); the build verdict and the retained log must
// never see that cap. Every phase marker below sits more than 64 KiB before the
// end of the transcript, so a verdict computed from a tail would lose them.

// beyondTheTail is ~76 KB of build noise carrying no phase marker.
var beyondTheTail = strings.Repeat("  CC       gst/qt6/gstqt6element-very-long-object-name.lo  ...\n", 1200)

// withNoiseAfterConfigureStarts buries every marker up to "configure started"
// under more than 64 KiB of output.
func withNoiseAfterConfigureStarts(t *testing.T, log string) string {
	t.Helper()
	if !strings.Contains(log, markerConfiguring) {
		t.Fatalf("fixture lost its configure-start marker")
	}
	out := strings.Replace(log, markerConfiguring, markerConfiguring+beyondTheTail, 1)
	if tail := out[len(out)-65536:]; strings.Contains(tail, markerConfiguring) || strings.Contains(tail, markerPreparing) {
		t.Fatalf("fixture broken: the start markers must lie beyond the last 64 KiB")
	}
	return out
}

func TestBuildVerdictSeesMarkersBeyondTheTail(t *testing.T) {
	// Both directions: a verdict from the tail would turn this PASS into "never
	// started", and this FAILED into "the machine, not the ebuild".
	t.Run("a pass stays a pass", func(t *testing.T) {
		gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure),
			buildSeam(&buildSpy{}, withNoiseAfterConfigureStarts(t, configureOKLog), nil))
		if err != nil {
			t.Fatalf("RunBuildGates: %v", err)
		}
		if cfg := gateNamed(t, gates, GateConfigure); cfg.Outcome != OutcomePass {
			t.Errorf("configure gate = %q (%q), want PASS: its start markers lie beyond the last 64 KiB (R7.3)", cfg.Outcome, cfg.Reason)
		}
	})
	t.Run("a failure stays attributed to configure", func(t *testing.T) {
		gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure),
			buildSeam(&buildSpy{}, withNoiseAfterConfigureStarts(t, configureFailLog), errExit1{}))
		if err != nil {
			t.Fatalf("RunBuildGates: %v", err)
		}
		if cfg := gateNamed(t, gates, GateConfigure); cfg.Outcome != OutcomeFailed {
			t.Errorf("configure gate = %q (%q), want FAILED: prepare and configure started beyond the last 64 KiB (R7.3)", cfg.Outcome, cfg.Reason)
		}
	})
}

func TestCompileLogKeepsTheWholeTranscript(t *testing.T) {
	req := buildRequestFor(t, DepthConfigure)
	output := withNoiseAfterConfigureStarts(t, configureFailLog)
	if _, err := RunBuildGates(context.Background(), req, buildSeam(&buildSpy{}, output, errExit1{})); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}
	entries, err := os.ReadDir(req.LogDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("log dir holds %d file(s) (%v), want the one retained log", len(entries), err)
	}
	got, err := os.ReadFile(filepath.Join(req.LogDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(output)) {
		t.Errorf("retained log holds %d bytes and not the whole %d-byte transcript (R7.3: the log is never tail-bounded)", len(got), len(output))
	}
}

type errExit1 struct{}

func (errExit1) Error() string { return "exit status 1" }
