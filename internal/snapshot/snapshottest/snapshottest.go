// Package snapshottest provides test doubles for package snapshot.
package snapshottest

import (
	"context"
	"fmt"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

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
// It also implements snapshot.Piper: each Pipe invocation is recorded in
// PipeCalls by its stage names, and its stages run through Run one after
// another, each fed the previous one's stdout, so every stage still lands in
// Calls.
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
func (m *MockRunner) Pipe(ctx context.Context, stages []snapshot.PipeStage) ([]byte, error) {
	names := make([]string, len(stages))
	for i, st := range stages {
		names[i] = st.Name
	}
	m.PipeCalls = append(m.PipeCalls, names)

	var prev []byte
	for _, st := range stages {
		out, err := m.Run(ctx, st.Name, st.Args, prev)
		if err != nil {
			return nil, fmt.Errorf("archive pipe stage %q: %w", st.Name, err)
		}
		prev = out
	}
	return prev, nil
}

// Compile-time assertions: MockRunner satisfies both the Runner and its
// streaming seam.
var (
	_ snapshot.Runner = (*MockRunner)(nil)
	_ snapshot.Piper  = (*MockRunner)(nil)
)
