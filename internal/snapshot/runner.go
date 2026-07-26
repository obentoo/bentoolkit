package snapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
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
// external command goes through Run, which binds the process to ctx via
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

// defaultRunner returns the production Runner used by the factories when no
// Runner is injected. Its reporter is nil (normalized to a no-op in Run), so it
// is byte-for-byte equivalent to the pre-story behavior (R3.3).
func defaultRunner() Runner { return execRunner{} }

// NewReportingRunner returns a production Runner that emits stage/done progress
// events to r (keyed by id) for every command it runs. A nil reporter is
// normalized to a no-op. Drivers wire this when a TUI/plain reporter is active.
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
type MockRunner struct {
	RunFunc func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error)
	Calls   []RunnerCall
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

// Compile-time assertion that MockRunner satisfies Runner.
var _ Runner = (*MockRunner)(nil)
