package snapshot

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// execCommand is the injectable context-aware command factory. It defaults to
// exec.CommandContext and is overridable in tests so engine/shipper drivers run
// without a real btrbk/systemd binary (AD3, R2.4). Mirrors the
// internal/autoupdate claude_code.go seam.
var execCommand = exec.CommandContext

// Runner is the subprocess seam shared by the engine and shipper drivers. Every
// external command goes through Run, which binds the process to ctx via
// exec.CommandContext so a cancelled parent kills the child (R8.1). stdin is
// piped on the process's standard input, never placed in argv.
type Runner interface {
	Run(ctx context.Context, name string, args []string, stdin []byte) (stdout []byte, err error)
}

// execRunner is the production Runner backed by execCommand.
type execRunner struct{}

// Run executes name with args under ctx. When err is non-nil and the command
// wrote to stderr, the trimmed stderr is joined onto the error so callers can
// wrap it (e.g. with ErrEngineFailed) without losing the diagnostic.
func (execRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	cmd := execCommand(ctx, name, args...)
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
	return stdout.Bytes(), err
}

// defaultRunner returns the production Runner used by the factories when no
// Runner is injected.
func defaultRunner() Runner { return execRunner{} }

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
