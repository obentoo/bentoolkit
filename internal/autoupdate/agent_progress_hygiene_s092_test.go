package autoupdate

// Authored for story 092 (autoupdate-apply-observable), sub-task 7.2, test
// first (R4.9): a progress callback that was abandoned adds no line to the
// package's task after the agent run returned. The fixer stops STARTING calls
// once it abandons a callback, but a delivery already past that check still
// lands; only the applier's side can refuse it.
//
// The fake agents below reproduce that delivery deterministically: a goroutine
// holds the request's Progress, blocked, while the agent call returns, and
// calls it once released. Its converse is pinned too: a line delivered while
// the call is running still reaches the task.
//
// Surface: the request's Progress field at the bump-review and build-fix call
// sites, through the applier's existing options; no new symbol.

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// s092v2LateAgent delivers inTime during the call, and late from a goroutine
// that is still blocked when the call returns.
type s092v2LateAgent struct {
	inTime, late string
	release      chan struct{}
	lateDone     chan struct{}
	calls        int
}

func newS092v2LateAgent(stage string) *s092v2LateAgent {
	return &s092v2LateAgent{
		inTime:   stage + ": Read /staged/in-time.ebuild",
		late:     stage + ": Edit /staged/late.ebuild",
		release:  make(chan struct{}),
		lateDone: make(chan struct{}),
	}
}

func (s *s092v2LateAgent) deliver(progress func(string)) {
	s.calls++
	if progress == nil {
		close(s.lateDone)
		return
	}
	progress(s.inTime)
	go func() {
		defer close(s.lateDone)
		<-s.release
		progress(s.late)
	}()
}

func (s *s092v2LateAgent) ReviewBump(_ context.Context, req fixer.BumpReviewRequest) (fixer.BumpReviewReport, error) {
	s.deliver(req.Progress)
	return fixer.BumpReviewReport{Skipped: true, SkipReason: "s092v2"}, nil
}

func (s *s092v2LateAgent) FixBuild(_ context.Context, req fixer.BuildFixRequest) (fixer.BuildFixResult, error) {
	s.deliver(req.Progress)
	return fixer.BuildFixResult{}, errors.New("s092v2: stop after the progress was handed over")
}

// releaseLate lets the blocked delivery run and waits until it has.
func (s *s092v2LateAgent) releaseLate(t *testing.T) {
	t.Helper()
	close(s.release)
	select {
	case <-s.lateDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the late delivery did not return within 5s")
	}
}

func s092v2AssertNoLateLine(t *testing.T, rec *recordingReporter, agent *s092v2LateAgent, pkg string) {
	t.Helper()
	if agent.calls != 1 {
		t.Fatalf("fixture: the agent ran %d time(s), want 1", agent.calls)
	}
	// Hostile half: the agent call has returned; a delivery that resumes now
	// must not add a line to the package's task.
	agent.releaseLate(t)
	lines := taskLines(rec.snapshot(), pkg)
	if slices.Contains(lines, agent.late) {
		t.Errorf("a progress line reached the task after the agent run returned: %q; the task's lines: %q", agent.late, lines)
	}
	// Converse: the line delivered while the run was going reached the task,
	// so the callback was not simply disconnected.
	if !slices.Contains(lines, agent.inTime) {
		t.Errorf("the line delivered during the run is missing from the task: %q; the task's lines: %q", agent.inTime, lines)
	}
}

func TestAgentProgressHygieneAbandonedCallbackAddsNothingLate(t *testing.T) {
	t.Run("bump review", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		tmp := t.TempDir()
		agent := newS092v2LateAgent(fixer.StageBumpReview)
		rec := &recordingReporter{}
		const pkg = "media-plugins/gst-plugins-qt6"
		b := newReviewableBump(t, tmp, pkg, "1.28.6", "1.29.2")
		a, err := NewApplier(b.overlay, filepath.Join(tmp, "config"),
			WithApplierDistdir(b.distdir, ""),
			WithApplierBumpReviewer(agent),
			WithApplierReporter(rec),
		)
		if err != nil {
			t.Fatal(err)
		}
		var gates []validate.GateResult
		a.reviewBump(t.Context(), b.cand,
			pkg, "1.28.6", "1.29.2", validate.DepthDecision{Depth: validate.DepthOptions, Reason: "policy"}, &gates)
		s092v2AssertNoLateLine(t, rec, agent, pkg)
	})

	t.Run("build fix on the compile path", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		agent := newS092v2LateAgent(fixer.StageBuildFix)
		h := buildFixFixture(t, &buildFixSpy{}, buildRun{output: preparedThenConfigureFails, err: errors.New("exit status 1")})
		WithApplierDistdir(t.TempDir(), "")(h.applier)
		WithApplierBuildFixer(agent)(h.applier)
		rec := &recordingReporter{}
		WithApplierReporter(rec)(h.applier)
		if result, _ := h.applier.Apply(t.Context(), h.pkg, true); result.Success {
			t.Fatal("fixture: the fix was scripted to fail")
		}
		s092v2AssertNoLateLine(t, rec, agent, h.pkg)
	})
}
