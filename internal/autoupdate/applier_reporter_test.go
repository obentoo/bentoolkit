package autoupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// recordingReporter records every Reporter event as an ordered "kind:id:detail"
// string so the applier's emitted lifecycle can be asserted. Mutex-guarded so it
// is safe under the StreamCapture writer goroutine (R7.4).
type recordingReporter struct {
	mu     sync.Mutex
	events []string
}

func (r *recordingReporter) add(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recordingReporter) BatchStart(total int)                 { r.add(fmt.Sprintf("BatchStart:%d", total)) }
func (r *recordingReporter) TaskStart(id, label string)           { r.add("TaskStart:" + id) }
func (r *recordingReporter) TaskStage(id, stage string)           { r.add("TaskStage:" + id + ":" + stage) }
func (r *recordingReporter) TaskProgress(id string, frac float64) { r.add("TaskProgress:" + id) }
func (r *recordingReporter) TaskLine(id string, stream tui.Stream, text string, eol bool) {
	r.add("TaskLine:" + id + ":" + text)
}
func (r *recordingReporter) TaskDone(id string, ok bool, summary, captured string) {
	r.add(fmt.Sprintf("TaskDone:%s:%v", id, ok))
}
func (r *recordingReporter) Log(level, text string)   { r.add("Log:" + text) }
func (r *recordingReporter) BatchDone(summary string) { r.add("BatchDone") }

func (r *recordingReporter) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

var _ tui.Reporter = (*recordingReporter)(nil)

// assertOrder checks that want appears as an ordered subsequence of events
// (other events may be interleaved — e.g. TaskLine from the stream tail).
func assertOrder(t *testing.T, events []string, want ...string) {
	t.Helper()
	i := 0
	for _, e := range events {
		if i < len(want) && e == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Errorf("event order: matched %d/%d of\n  want %v\n  got  %v", i, len(want), want, events)
	}
}

// R6.1/R3.1: a successful apply emits TaskStart -> TaskStage("manifest") ->
// TaskDone(ok=true) for the package, in order.
func TestApplierReporterSuccessEventOrder(t *testing.T) {
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	cfg := filepath.Join(tmp, "config")
	pkg := "dev-libs/foo"

	createTestEbuildFile(t, overlay, pkg, "1.0")
	pending, _ := NewPendingList(cfg)
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.0", NewVersion: "1.1", Status: StatusPending})

	rec := &recordingReporter{}
	applier, err := NewApplier(overlay, cfg,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
		WithApplierReporter(rec),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	res, err := applier.Apply(t.Context(), pkg)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("apply failed: err=%v result=%+v", err, res)
	}

	assertOrder(t, rec.snapshot(),
		"TaskStart:"+pkg,
		"TaskStage:"+pkg+":manifest",
		"TaskDone:"+pkg+":true",
	)
}

// R6.1/R4.3: a manifest-fail-then-fix apply emits the manifest -> llm-fix ->
// re-check stages before a successful TaskDone.
func TestApplierReporterFailThenFixEventOrder(t *testing.T) {
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	cfg := filepath.Join(tmp, "config")
	pkg := "dev-games/godot"

	createTestEbuildFile(t, overlay, pkg, "4.7_rc3")
	pending, _ := NewPendingList(cfg)
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "4.7_rc3", NewVersion: "4.7", Status: StatusPending})

	fixer := &fakeFixer{summary: "rewrote SRC_URI"}
	rec := &recordingReporter{}
	applier, err := NewApplier(overlay, cfg,
		WithApplierPendingList(pending),
		WithExecCommand(pkgdevFlakySeam()),
		WithApplierFixer(fixer),
		WithApplierReporter(rec),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	res, err := applier.Apply(t.Context(), pkg)
	if err != nil || res == nil || !res.Success || !res.Fixed {
		t.Fatalf("apply (fix) failed: err=%v result=%+v", err, res)
	}

	assertOrder(t, rec.snapshot(),
		"TaskStart:"+pkg,
		"TaskStage:"+pkg+":manifest",
		"TaskStage:"+pkg+":llm-fix",
		"TaskStage:"+pkg+":re-check",
		"TaskDone:"+pkg+":true",
	)
}

// R3.3: a nil reporter (default) and an explicit Noop reporter both behave
// exactly as before this story — the apply succeeds with no panic.
func TestApplierReporterNoopUnchanged(t *testing.T) {
	run := func(t *testing.T, opts ...ApplierOption) {
		t.Helper()
		tmp := t.TempDir()
		overlay := filepath.Join(tmp, "overlay")
		cfg := filepath.Join(tmp, "config")
		pkg := "dev-libs/bar"
		createTestEbuildFile(t, overlay, pkg, "2.0")
		pending, _ := NewPendingList(cfg)
		pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "2.0", NewVersion: "2.1", Status: StatusPending})

		base := []ApplierOption{
			WithApplierPendingList(pending),
			WithExecCommand(mockExecCommandSuccess),
		}
		applier, err := NewApplier(overlay, cfg, append(base, opts...)...)
		if err != nil {
			t.Fatalf("NewApplier: %v", err)
		}
		res, err := applier.Apply(t.Context(), pkg)
		if err != nil || res == nil || !res.Success {
			t.Fatalf("apply failed: err=%v result=%+v", err, res)
		}
		if _, statErr := os.Stat(applier.EbuildPath(pkg, "2.1")); statErr != nil {
			t.Errorf("new ebuild missing: %v", statErr)
		}
	}

	t.Run("no reporter (default nil)", func(t *testing.T) { run(t) })
	t.Run("explicit Noop", func(t *testing.T) { run(t, WithApplierReporter(tui.Noop())) })
}

// R1.1/R1.2: runManifest streams the pkgdev output live as TaskLine events
// (rather than buffering it silently with CombinedOutput). This is the Red driver
// for the StreamCapture seam: before it is wired, no TaskLine is emitted.
func TestApplierManifestStreamsTaskLines(t *testing.T) {
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	cfg := filepath.Join(tmp, "config")
	pkg := "dev-libs/streamer"

	createTestEbuildFile(t, overlay, pkg, "1.0")
	pending, _ := NewPendingList(cfg)
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.0", NewVersion: "1.1", Status: StatusPending})

	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			return exec.CommandContext(ctx, "sh", "-c", "printf 'LINE-A\\nLINE-B\\nLINE-C\\n'")
		}
		return exec.CommandContext(ctx, "true")
	}

	rec := &recordingReporter{}
	applier, err := NewApplier(overlay, cfg,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithApplierReporter(rec),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	res, err := applier.Apply(t.Context(), pkg)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("apply failed: err=%v result=%+v", err, res)
	}

	ev := rec.snapshot()
	for _, want := range []string{
		"TaskLine:" + pkg + ":LINE-A",
		"TaskLine:" + pkg + ":LINE-B",
		"TaskLine:" + pkg + ":LINE-C",
	} {
		if !slices.Contains(ev, want) {
			t.Errorf("missing streamed tail event %q in:\n%v", want, ev)
		}
	}
}

// R7.1: a manifest failure preserves the FULL captured subprocess output in the
// error string, byte-identical to the pre-story CombinedOutput contract. The
// scripted pkgdev writes to stderr too, exercising the combined capture.
func TestApplierManifestErrorStringPreserved(t *testing.T) {
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	cfg := filepath.Join(tmp, "config")
	pkg := "dev-libs/failer"

	createTestEbuildFile(t, overlay, pkg, "1.0")
	pending, _ := NewPendingList(cfg)
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.0", NewVersion: "1.1", Status: StatusPending})

	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			return exec.CommandContext(ctx, "sh", "-c", "printf 'BOOM-OUTPUT-7F3A\\n' 1>&2; exit 1")
		}
		return exec.CommandContext(ctx, "true")
	}

	applier, err := NewApplier(overlay, cfg,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	res, _ := applier.Apply(t.Context(), pkg)
	if res == nil || res.Success || res.Error == nil {
		t.Fatalf("expected a manifest failure, got %+v", res)
	}
	if !strings.Contains(res.Error.Error(), "BOOM-OUTPUT-7F3A") {
		t.Errorf("manifest error must preserve captured output (R7.1):\n%v", res.Error)
	}
}
