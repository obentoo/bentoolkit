package snapshottest_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/obentoo/bentoolkit/internal/snapshot/snapshottest"
)

// MockRunner stands in for the production runner wherever a driver takes a
// snapshot.Runner, including the streaming seam a multi-stage pipe needs.
var (
	_ snapshot.Runner = (*snapshottest.MockRunner)(nil)
	_ snapshot.Piper  = (*snapshottest.MockRunner)(nil)
)

// A caller that reuses its buffers after Run must not rewrite what the mock
// recorded: the record is a copy, not an alias of the caller's slices.
func TestMockRunnerRecordsCopiesOfArgsAndStdin(t *testing.T) {
	m := &snapshottest.MockRunner{}
	args := []string{"list", "--json"}
	stdin := []byte("payload")

	if _, err := m.Run(context.Background(), "snapper", args, stdin); err != nil {
		t.Fatalf("Run: %v", err)
	}
	args[0] = "MUTATED"
	stdin[0] = 'X'

	if len(m.Calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(m.Calls))
	}
	want := snapshottest.RunnerCall{Name: "snapper", Args: []string{"list", "--json"}, Stdin: []byte("payload")}
	if got := m.Calls[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("recorded call changed with the caller's buffers:\n got: %#v\nwant: %#v", got, want)
	}
}

// Two identical calls are two records, in order, never merged into one.
func TestMockRunnerRecordsEveryCallInOrder(t *testing.T) {
	m := &snapshottest.MockRunner{}
	ctx := context.Background()
	for _, name := range []string{"btrbk", "btrbk", "systemctl"} {
		if _, err := m.Run(ctx, name, []string{"run"}, nil); err != nil {
			t.Fatalf("Run(%s): %v", name, err)
		}
	}
	var names []string
	for _, c := range m.Calls {
		names = append(names, c.Name)
	}
	if want := []string{"btrbk", "btrbk", "systemctl"}; !reflect.DeepEqual(names, want) {
		t.Errorf("recorded calls %v, want %v", names, want)
	}
	if m.Calls[0].Stdin != nil {
		t.Errorf("a call with no stdin recorded stdin %q", m.Calls[0].Stdin)
	}
}

func TestMockRunnerDelegatesToRunFunc(t *testing.T) {
	t.Run("without RunFunc every command succeeds with no output", func(t *testing.T) {
		m := &snapshottest.MockRunner{}
		out, err := m.Run(context.Background(), "btrbk", []string{"run"}, nil)
		if out != nil || err != nil {
			t.Errorf("Run = (%q, %v), want (nil, nil)", out, err)
		}
	})
	t.Run("with RunFunc its result is returned and it sees the call", func(t *testing.T) {
		boom := errors.New("boom")
		var seen []string
		m := &snapshottest.MockRunner{RunFunc: func(_ context.Context, name string, args []string, stdin []byte) ([]byte, error) {
			seen = append(seen, name+" "+strings.Join(args, " ")+" <"+string(stdin))
			return []byte("out"), boom
		}}
		out, err := m.Run(context.Background(), "snapper", []string{"-c", "root"}, []byte("in"))
		if string(out) != "out" || !errors.Is(err, boom) {
			t.Errorf("Run = (%q, %v), want (\"out\", boom)", out, err)
		}
		if want := []string{"snapper -c root <in"}; !reflect.DeepEqual(seen, want) {
			t.Errorf("RunFunc saw %v, want %v", seen, want)
		}
		if len(m.Calls) != 1 {
			t.Errorf("recorded %d calls, want 1 even when RunFunc fails", len(m.Calls))
		}
	})
}

// echoStage makes every stage print "<name>(<stdin>)", so the chain of stdins
// is visible in the final output.
func echoStage(_ context.Context, name string, _ []string, stdin []byte) ([]byte, error) {
	return []byte(name + "(" + string(stdin) + ")"), nil
}

func TestMockRunnerPipeFeedsEachStageThePreviousStdout(t *testing.T) {
	m := &snapshottest.MockRunner{RunFunc: echoStage}
	stages := []snapshot.PipeStage{
		{Name: "btrfs", Args: []string{"send", "/snap/1"}},
		{Name: "zstd", Args: []string{"-c"}},
		{Name: "rclone", Args: []string{"rcat", "remote:obj"}},
	}

	out, err := m.Pipe(context.Background(), stages)
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	if want := "rclone(zstd(btrfs()))"; string(out) != want {
		t.Errorf("Pipe output %q, want %q: each stage reads the previous stage's stdout, the first reads nothing", out, want)
	}
	if m.Calls[0].Stdin != nil {
		t.Errorf("the first stage was fed stdin %q; it has no previous stage", m.Calls[0].Stdin)
	}
	wantCalls := []snapshottest.RunnerCall{
		{Name: "btrfs", Args: []string{"send", "/snap/1"}},
		{Name: "zstd", Args: []string{"-c"}, Stdin: []byte("btrfs()")},
		{Name: "rclone", Args: []string{"rcat", "remote:obj"}, Stdin: []byte("zstd(btrfs())")},
	}
	if !reflect.DeepEqual(m.Calls, wantCalls) {
		t.Errorf("every stage lands in Calls with its own args:\n got: %#v\nwant: %#v", m.Calls, wantCalls)
	}
	if want := [][]string{{"btrfs", "zstd", "rclone"}}; !reflect.DeepEqual(m.PipeCalls, want) {
		t.Errorf("PipeCalls = %v, want %v", m.PipeCalls, want)
	}
}

// Two identical pipes are two PipeCalls entries.
func TestMockRunnerPipeRecordsEachPipe(t *testing.T) {
	m := &snapshottest.MockRunner{}
	stages := []snapshot.PipeStage{{Name: "btrfs"}, {Name: "zstd"}}
	for range 2 {
		if _, err := m.Pipe(context.Background(), stages); err != nil {
			t.Fatalf("Pipe: %v", err)
		}
	}
	if want := [][]string{{"btrfs", "zstd"}, {"btrfs", "zstd"}}; !reflect.DeepEqual(m.PipeCalls, want) {
		t.Errorf("PipeCalls = %v, want %v", m.PipeCalls, want)
	}
}

func TestMockRunnerPipeStopsAtTheFailingStage(t *testing.T) {
	boom := errors.New("boom")
	m := &snapshottest.MockRunner{RunFunc: func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
		if name == "zstd" {
			return nil, boom
		}
		return echoStage(ctx, name, args, stdin)
	}}
	out, err := m.Pipe(context.Background(), []snapshot.PipeStage{{Name: "btrfs"}, {Name: "zstd"}, {Name: "rclone"}})
	if !errors.Is(err, boom) {
		t.Fatalf("Pipe error %v does not wrap the stage's error", err)
	}
	if !strings.Contains(err.Error(), `"zstd"`) {
		t.Errorf("Pipe error %q does not name the failing stage", err)
	}
	if out != nil {
		t.Errorf("a failed pipe returned output %q", out)
	}
	for _, c := range m.Calls {
		if c.Name == "rclone" {
			t.Error("the stage after the failing one still ran")
		}
	}
}
