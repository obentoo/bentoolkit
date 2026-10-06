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
// without a real btrbk/systemd binary. Mirrors the
// internal/autoupdate claude_code.go seam.
var execCommand = exec.CommandContext

// runnerEnv returns the environment every execRunner child process inherits: the
// parent environment with LC_ALL=C appended, so snapper and btrbk emit
// locale-independent dates and messages that the parsers can actually match.
// The appended entry overrides any LC_ALL inherited from the parent
// because os/exec keeps only the last value of a duplicated key. It is a pure
// helper so the locale contract is unit-testable without spawning a process.
func runnerEnv() []string {
	return append(os.Environ(), "LC_ALL=C")
}

// Runner is the subprocess seam shared by the engine and shipper drivers. Every
// external command goes through Run, or through the optional Piper seam for a
// streamed multi-stage pipe; both bind each process to ctx via
// exec.CommandContext so a cancelled parent kills the child. stdin is
// piped on the process's standard input, never placed in argv.
type Runner interface {
	Run(ctx context.Context, name string, args []string, stdin []byte) (stdout []byte, err error)
}

// execRunner is the production Runner backed by execCommand. An optional reporter
// surfaces stage/done progress for each command; a nil reporter (the default) is
// normalized to a no-op, so the runner emits no progress events.
type execRunner struct {
	reporter tui.Reporter
	taskID   string
}

// Run executes name with args under ctx. When err is non-nil and the command
// wrote to stderr, the trimmed stderr is joined onto the error so callers can
// wrap it (e.g. with ErrEngineFailed) without losing the diagnostic.
//
// It emits a TaskStage(taskID, name) before running and a TaskDone(taskID, ok)
// after. Snapshot
// commands do not stream meaningful progress, so no live tail is attached.
//
// The child always runs under LC_ALL=C (see runnerEnv) so its output is stable
// enough to parse on a non-English host; the trade-off is that a
// command's own error text arrives in English, and it is surfaced verbatim.
func (e execRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	rep := e.reporter
	if rep == nil {
		rep = tui.Noop()
	}
	rep.TaskStage(e.taskID, name)

	cmd := execCommand(ctx, name, args...)
	// Pin the child locale to C so every subprocess (snapper, btrbk) produces
	// parseable, non-localized output regardless of the host locale.
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

// Piper is the optional streaming seam of a Runner. runPipe requires
// it: a multi-stage pipe such as `btrfs send | zstd | rclone rcat` must not hold
// any stage's whole output in memory, which a Run-only Runner cannot avoid.
type Piper interface {
	Pipe(ctx context.Context, stages []PipeStage) (stdout []byte, err error)
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
// an OS pipe, so the stream never passes through this process. The
// pipe ends are *os.File on purpose: os/exec hands a file straight to the
// child, while any other io.Reader/io.Writer costs an in-process copy.
//
// Every stage runs under a context derived from ctx, with the same Env and
// WaitDelay as Run. The first stage to exit non-zero cancels the others; every
// stage is still waited for. The returned error names the root failure (see
// pipeRootFailure), wraps its exit error and carries the last
// 64 KiB of every stage's stderr. When ctx itself is cancelled, the
// error also wraps ctx.Err(). It returns the last 64 KiB of the
// final stage's stdout.
func (e execRunner) Pipe(ctx context.Context, stages []PipeStage) ([]byte, error) {
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
		cmd := execCommand(pctx, st.Name, st.Args...)
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
			return nil, errors.Join(fmt.Errorf("archive pipe: connect stage %q to stage %q: %w", stages[i].Name, stages[i+1].Name, err), closeEnds())
		}
		cmds[i].Stdout, cmds[i+1].Stdin = w, r
		ends = append(ends, r, w)
	}

	started := 0
	var startErr error
	for i, cmd := range cmds {
		rep.TaskStage(e.taskID, stages[i].Name)
		if err := cmd.Start(); err != nil {
			rep.TaskDone(e.taskID, false, "", "")
			startErr = fmt.Errorf("archive pipe stage %q: start: %w", stages[i].Name, err)
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
	err := fmt.Errorf("archive pipe stage %q: %w", stages[failed].Name, errors.Join(stageErrs[failed], pipeStderr(stages, stderrs)))
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
func pipeStderr(stages []PipeStage, stderrs []*tailBuffer) error {
	var errs []error
	for i, b := range stderrs {
		if s := strings.TrimSpace(string(b.buf)); s != "" {
			errs = append(errs, fmt.Errorf("stage %q stderr: %s", stages[i].Name, s))
		}
	}
	return errors.Join(errs...)
}

// defaultRunner returns the production Runner used by the factories when no
// Runner is injected. Its reporter is nil (normalized to a no-op in Run), so it
// emits no progress events.
func defaultRunner() Runner { return execRunner{} }

// NewReportingRunner returns a production Runner that emits stage/done progress
// events to r (keyed by id) for every command it runs. A nil reporter is
// normalized to a no-op.
//
// It has no production caller yet: cmd/bentoo's deps.snapshotRunner is nil in
// production. It stays because it is the only place that sets
// execRunner.reporter and execRunner.taskID. execRunner is unexported, so
// nothing outside the package can build a reporting Runner, and without this
// constructor both fields stay zero and Run's progress-event path is dead code.
// Wiring it is one assignment: deps.snapshotRunner already reaches every driver
// through NewManager, newEngine, newShipper and newScheduler, so
// NewReportingRunner(rep, id) there turns a snapshot run's subprocesses into
// stage/done events. TestSnapshotRunnerEmitsStageDone and
// TestSnapshotRunnerReportsFailure prove that Run emits stage:<id>:<name>
// before a command and done:<id>:<ok> after it, on success and on failure.
func NewReportingRunner(r tui.Reporter, id string) Runner {
	if r == nil {
		r = tui.Noop()
	}
	return execRunner{reporter: r, taskID: id}
}

// Compile-time assertion: the production Runner implements the streaming seam.
var _ Piper = execRunner{}
