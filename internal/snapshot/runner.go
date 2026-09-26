package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// execCommand is the injectable context-aware command factory. It defaults to
// exec.CommandContext and is overridable in tests so engine/shipper drivers run
// without a real btrbk/systemd binary (AD3, R2.4). Mirrors the
// internal/autoupdate claude_code.go seam.
var execCommand = exec.CommandContext

// runnerEnv returns the environment every execRunner child process inherits: the
// parent environment with LC_ALL=C appended, so snapper and btrbk emit
// locale-independent dates and messages that the parsers can actually match
// (R4.1). The appended entry overrides any LC_ALL inherited from the parent
// because os/exec keeps only the last value of a duplicated key. It is a pure
// helper so the locale contract is unit-testable without spawning a process.
func runnerEnv() []string {
	return append(os.Environ(), "LC_ALL=C")
}

// Runner is the subprocess seam shared by the engine and shipper drivers. Every
// external command goes through Run, or through the optional piper seam for a
// streamed multi-stage pipe (053 R5.1); both bind each process to ctx via
// exec.CommandContext so a cancelled parent kills the child (R8.1). stdin is
// piped on the process's standard input, never placed in argv.
type Runner interface {
	Run(ctx context.Context, name string, args []string, stdin []byte) (stdout []byte, err error)
}

// execRunner is the production Runner backed by execCommand. An optional reporter
// surfaces stage/done progress for each command; a nil reporter (the default) is
// normalized to a no-op so behavior is unchanged from before this story (R3.3).
type execRunner struct {
	reporter tui.Reporter
	taskID   string
}

// Run executes name with args under ctx. When err is non-nil and the command
// wrote to stderr, the trimmed stderr is joined onto the error so callers can
// wrap it (e.g. with ErrEngineFailed) without losing the diagnostic.
//
// It emits a TaskStage(taskID, name) before running and a TaskDone(taskID, ok)
// after (R6.2: snapshot subprocess sites get stage/done events). Snapshot
// commands do not stream meaningful progress, so no live tail is attached.
//
// The child always runs under LC_ALL=C (see runnerEnv) so its output is stable
// enough to parse on a non-English host (R4.1); the trade-off is that a
// command's own error text arrives in English, and it is surfaced verbatim.
func (e execRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	rep := e.reporter
	if rep == nil {
		rep = tui.Noop()
	}
	rep.TaskStage(e.taskID, name)

	cmd := execCommand(ctx, name, args...)
	// Pin the child locale to C so every subprocess (snapper, btrbk) produces
	// parseable, non-localized output regardless of the host locale (R4.1).
	cmd.Env = runnerEnv()
	// On cancel, CommandContext kills only the direct child; orphaned
	// grandchildren (shell pipelines) can keep the stdout/stderr pipes open and
	// stall Wait. WaitDelay forces the pipes closed shortly after cancel.
	cmd.WaitDelay = time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			err = errors.Join(err, errors.New(s))
		}
	}
	rep.TaskDone(e.taskID, err == nil, "", "")
	return stdout.Bytes(), err
}

// piper is the optional streaming seam of a Runner (053 R5.1). runPipe requires
// it: a multi-stage pipe such as `btrfs send | zstd | rclone rcat` must not hold
// any stage's whole output in memory, which a Run-only Runner cannot avoid.
type piper interface {
	Pipe(ctx context.Context, stages []pipeStage) (stdout []byte, err error)
}

// pipeTailCap bounds what execRunner.Pipe keeps of each stage's stderr and of
// the last stage's stdout: the last 64 KiB. rclone rcat prints nothing that
// matters on stdout, and a stderr tail is what a diagnostic needs.
const pipeTailCap = 64 << 10

// tailBuffer is an io.Writer that keeps only the last limit bytes written to it.
type tailBuffer struct {
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= b.limit {
		b.buf = append(b.buf[:0], p[n-b.limit:]...)
		return n, nil
	}
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	return n, nil
}

// Pipe runs stages at once, stage i's stdout connected to stage i+1's stdin by
// an OS pipe, so the stream never passes through this process (053 R5.1). The
// pipe ends are *os.File on purpose: os/exec hands a file straight to the
// child, while any other io.Reader/io.Writer costs an in-process copy.
//
// Every stage runs under a context derived from ctx, with the same Env and
// WaitDelay as Run. The first stage to exit non-zero cancels the others; every
// stage is still waited for. The returned error names the root failure (see
// pipeRootFailure), wraps its exit error and carries the last
// 64 KiB of every stage's stderr (053 R5.4). When ctx itself is cancelled, the
// error also wraps ctx.Err() (053 R5.6). It returns the last 64 KiB of the
// final stage's stdout.
func (e execRunner) Pipe(ctx context.Context, stages []pipeStage) ([]byte, error) {
	if len(stages) == 0 {
		return nil, nil
	}
	rep := e.reporter
	if rep == nil {
		rep = tui.Noop()
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmds := make([]*exec.Cmd, len(stages))
	stderrs := make([]*tailBuffer, len(stages))
	for i, st := range stages {
		cmd := execCommand(pctx, st.name, st.args...)
		cmd.Env = runnerEnv()
		cmd.WaitDelay = time.Second
		stderrs[i] = &tailBuffer{limit: pipeTailCap}
		cmd.Stderr = stderrs[i]
		cmds[i] = cmd
	}
	stdout := &tailBuffer{limit: pipeTailCap}
	cmds[len(cmds)-1].Stdout = stdout

	// The parent's copies of every pipe end are closed once the children hold
	// theirs; a copy left open here would keep the next stage from seeing EOF.
	var ends []*os.File
	closeEnds := func() error {
		var errs []error
		for _, f := range ends {
			if err := f.Close(); err != nil {
				errs = append(errs, fmt.Errorf("archive pipe: release pipe end %s: %w", f.Name(), err))
			}
		}
		ends = nil
		return errors.Join(errs...)
	}
	for i := range len(cmds) - 1 {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, errors.Join(fmt.Errorf("archive pipe: connect stage %q to stage %q: %w", stages[i].name, stages[i+1].name, err), closeEnds())
		}
		cmds[i].Stdout, cmds[i+1].Stdin = w, r
		ends = append(ends, r, w)
	}

	started := 0
	var startErr error
	for i, cmd := range cmds {
		rep.TaskStage(e.taskID, stages[i].name)
		if err := cmd.Start(); err != nil {
			rep.TaskDone(e.taskID, false, "", "")
			startErr = fmt.Errorf("archive pipe stage %q: start: %w", stages[i].name, err)
			cancel()
			break
		}
		started++
	}
	closeErr := closeEnds()

	type exit struct {
		stage int
		err   error
	}
	exits := make(chan exit, started)
	for i := range started {
		go func() { exits <- exit{i, cmds[i].Wait()} }()
	}
	stageErrs := make([]error, started)
	for range started {
		x := <-exits
		stageErrs[x.stage] = x.err
		rep.TaskDone(e.taskID, x.err == nil, "", "")
		if x.err != nil {
			// One failed stage fails the pipe: the others are killed, and still
			// waited for (bounded by WaitDelay), so none outlives this call.
			cancel()
		}
	}
	failed := pipeRootFailure(stageErrs)

	if startErr != nil {
		return nil, errors.Join(startErr, closeErr, ctx.Err())
	}
	if failed < 0 {
		if closeErr != nil {
			return nil, closeErr
		}
		return stdout.buf, nil
	}
	err := fmt.Errorf("archive pipe stage %q: %w", stages[failed].name, errors.Join(stageErrs[failed], pipeStderr(stages, stderrs)))
	return nil, errors.Join(err, closeErr, ctx.Err())
}

// pipeRootFailure picks the stage to blame for a failed pipe, or -1 when every
// stage succeeded. Which Wait returns first says little: a failing stage takes
// its neighbours down with it (SIGPIPE upstream, a truncated stream downstream,
// SIGKILL from the cancel), and goroutine scheduling reorders the reports. So a
// stage that exited on its own is preferred over one killed by a signal, a
// signal other than SIGPIPE over SIGPIPE, and among equals the stage furthest
// upstream, whose output every later stage consumed.
func pipeRootFailure(stageErrs []error) int {
	best, bestRank := -1, 0
	for i, err := range stageErrs {
		if err == nil {
			continue
		}
		rank := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(interface {
				Signaled() bool
				Signal() syscall.Signal
			}); ok && ws.Signaled() {
				rank = 1
				if ws.Signal() == syscall.SIGPIPE {
					rank = 2
				}
			}
		}
		if best < 0 || rank < bestRank {
			best, bestRank = i, rank
		}
	}
	return best
}

// pipeStderr joins the non-empty stderr tail of every stage, each labelled with
// its stage name, or returns nil when no stage wrote to stderr.
func pipeStderr(stages []pipeStage, stderrs []*tailBuffer) error {
	var errs []error
	for i, b := range stderrs {
		if s := strings.TrimSpace(string(b.buf)); s != "" {
			errs = append(errs, fmt.Errorf("stage %q stderr: %s", stages[i].name, s))
		}
	}
	return errors.Join(errs...)
}

// defaultRunner returns the production Runner used by the factories when no
// Runner is injected. Its reporter is nil (normalized to a no-op in Run), so it
// is byte-for-byte equivalent to the pre-story behavior (R3.3).
func defaultRunner() Runner { return execRunner{} }

// NewReportingRunner returns a production Runner that emits stage/done progress
// events to r (keyed by id) for every command it runs. A nil reporter is
// normalized to a no-op.
//
// It has NO production caller, and never has had one. Sub-task 6.1 is where
// that was measured: its pre-authored test argued for a second, recording
// runner "beside the reporting one" on the premise that this constructor's
// behaviour "is inherited by every driver in this package", and the grep that
// checked the premise found only the two tests named below. That same
// measurement error is what closed 6.1 as [~] (superseded-by: 6.2) —
// snapshot.RunResult.Stages already carried one outcome per step, in the
// semantic vocabulary a report needs, so the runner never had to accumulate
// anything. The orphaning itself is older than story 046: this function landed
// with its two tests in commit e2c0f21 and has had exactly these callers since.
//
//   - runner_reporter_test.go:57, TestSnapshotRunnerEmitsStageDone
//   - runner_reporter_test.go:80, TestSnapshotRunnerReportsFailure
//
// It stays because it is the seam story 047 wires — the story's Out of Scope
// defers the remaining commands to it — and it is the only seam there is:
//
//   - This is the ONE place in the package that sets execRunner.reporter or
//     execRunner.taskID; every other execRunner literal is the zero value.
//     Delete it and both fields are permanently zero, which makes the nil
//     branch and both rep.TaskStage/rep.TaskDone calls in execRunner.Run
//     provably dead. The deletion would not stop at this function; it would
//     take the package's whole progress-event path with it.
//   - execRunner is unexported, so nothing outside internal/snapshot can build
//     a reporting Runner by hand. This constructor is that capability's export.
//   - What 047 has to write is one assignment at the caller. cmd/bentoo's
//     `var snapshotRunner` in snapshot.go is nil in production and already reaches
//     every driver through NewManager, newEngine, newShipper and newScheduler,
//     so NewReportingRunner(rep, id) there is what turns a snapshot run's
//     subprocesses into stage/done events.
//   - Those two tests are the only executable proof that Run emits
//     stage:<id>:<name> before a command and done:<id>:<ok> after it, on
//     success and on failure alike. 047 inherits that contract already checked.
//
// The sentence this replaced said "Drivers wire this when a TUI/plain reporter
// is active". No driver does, and none ever did — it described the intended
// wiring as though it had already happened.
func NewReportingRunner(r tui.Reporter, id string) Runner {
	if r == nil {
		r = tui.Noop()
	}
	return execRunner{reporter: r, taskID: id}
}

// RunnerCall records a single invocation captured by MockRunner.
type RunnerCall struct {
	Name  string
	Args  []string
	Stdin []byte
}

// MockRunner is a test Runner that records every call and delegates behavior to
// RunFunc. With RunFunc nil it returns (nil, nil), so a driver under test runs
// its full code path while every subprocess is captured rather than executed.
//
// It also implements piper: each Pipe invocation is recorded in PipeCalls by its
// stage names, and its stages run through Run one after another, each fed the
// previous one's stdout, so every stage still lands in Calls.
type MockRunner struct {
	RunFunc   func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error)
	Calls     []RunnerCall
	PipeCalls [][]string
}

// Run records the call (copying args/stdin so later mutation by the caller cannot
// corrupt the record) and delegates to RunFunc when set.
func (m *MockRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	call := RunnerCall{Name: name}
	if args != nil {
		call.Args = append([]string(nil), args...)
	}
	if stdin != nil {
		call.Stdin = append([]byte(nil), stdin...)
	}
	m.Calls = append(m.Calls, call)

	if m.RunFunc != nil {
		return m.RunFunc(ctx, name, args, stdin)
	}
	return nil, nil
}

// Pipe records the pipe's stage names and chains its stages through Run.
// Buffering is fine inside a test double; the production seam streams.
func (m *MockRunner) Pipe(ctx context.Context, stages []pipeStage) ([]byte, error) {
	names := make([]string, len(stages))
	for i, st := range stages {
		names[i] = st.name
	}
	m.PipeCalls = append(m.PipeCalls, names)

	var prev []byte
	for _, st := range stages {
		out, err := m.Run(ctx, st.name, st.args, prev)
		if err != nil {
			return nil, fmt.Errorf("archive pipe stage %q: %w", st.name, err)
		}
		prev = out
	}
	return prev, nil
}

// Compile-time assertions: both Runners also implement the streaming seam.
var (
	_ Runner = (*MockRunner)(nil)
	_ piper  = (*MockRunner)(nil)
	_ piper  = execRunner{}
)
