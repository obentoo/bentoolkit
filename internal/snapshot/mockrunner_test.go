package snapshot

import (
	"context"
	"fmt"
)

// runnerCall records a single invocation captured by mockRunner.
type runnerCall struct {
	Name  string
	Args  []string
	Stdin []byte
}

// mockRunner is the in-package twin of snapshottest.MockRunner. Tests in
// package snapshot reach unexported identifiers, and snapshottest imports
// snapshot, so they cannot import it without a cycle; this copy keeps the same
// behaviour: it records every call and delegates to RunFunc, returning
// (nil, nil) when RunFunc is nil.
//
// It also implements Piper: each Pipe invocation is recorded in PipeCalls by
// its stage names, and its stages run through Run one after another, each fed
// the previous one's stdout, so every stage still lands in Calls.
type mockRunner struct {
	RunFunc   func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error)
	Calls     []runnerCall
	PipeCalls [][]string
}

// Run records the call (copying args/stdin so later mutation by the caller cannot
// corrupt the record) and delegates to RunFunc when set.
func (m *mockRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	call := runnerCall{Name: name}
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
func (m *mockRunner) Pipe(ctx context.Context, stages []PipeStage) ([]byte, error) {
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

// Compile-time assertions: mockRunner satisfies both the Runner and its
// streaming seam.
var (
	_ Runner = (*mockRunner)(nil)
	_ Piper  = (*mockRunner)(nil)
)
