package snapshot

import (
	"errors"
	"os/exec"
	"testing"
)

// s053ExitOf runs a shell snippet and returns its Wait error.
func s053ExitOf(t *testing.T, script string) error {
	t.Helper()
	err := exec.Command("sh", "-c", script).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("sh -c %q: %v, want an *exec.ExitError", script, err)
	}
	return err
}

// TestPipeRootFailure_PrefersOwnExitOverSignals pins the review fix of 053 R5.4:
// the stage blamed for a failed pipe is the one that failed on its own, not a
// neighbour it took down, whatever order the Waits returned in.
func TestPipeRootFailure_PrefersOwnExitOverSignals(t *testing.T) {
	s053NeedTools(t, "sh")
	exit3 := s053ExitOf(t, "exit 3")
	exit1 := s053ExitOf(t, "exit 1")
	sigpipe := s053ExitOf(t, "kill -PIPE $$")
	sigkill := s053ExitOf(t, "kill -KILL $$")

	cases := []struct {
		name string
		errs []error
		want int
	}{
		{"all succeeded", []error{nil, nil, nil}, -1},
		{"SIGPIPE upstream of the real failure", []error{sigpipe, sigpipe, exit3}, 2},
		{"real failure upstream of a truncation error", []error{exit3, exit1, nil}, 0},
		{"cancel's SIGKILL loses to the real failure", []error{sigkill, exit1, nil}, 1},
		{"SIGKILL beats SIGPIPE", []error{sigpipe, sigkill, nil}, 1},
		{"a non-exit error counts as a real failure", []error{sigpipe, errors.New("start failed"), nil}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pipeRootFailure(tc.errs); got != tc.want {
				t.Errorf("pipeRootFailure = %d, want %d", got, tc.want)
			}
		})
	}
}
