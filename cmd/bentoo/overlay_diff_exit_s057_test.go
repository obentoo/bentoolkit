package main

// Story 057, sub-task 5.1 — R5.1: git diff's exit status is read with
// errors.As, so a wrapped *exec.ExitError with code 1 still means "differences
// found". Real child processes, not a hand-built ExitError.

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

func TestGitDiffFoundDifferences(t *testing.T) {
	exit := func(code int) error {
		t.Helper()
		err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
		if err == nil {
			t.Fatalf("the fixture is wrong: exit %d returned no error", code)
		}
		return err
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"exit 1", exit(1), true},
		{"exit 1 wrapped", fmt.Errorf("running git diff: %w", exit(1)), true},
		{"exit 2 is a failure", exit(2), false},
		{"exit 2 wrapped is a failure", fmt.Errorf("x: %w", exit(2)), false},
		{"not an exit status", errors.New("exit status 1"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitDiffFoundDifferences(tc.err); got != tc.want {
				t.Errorf("gitDiffFoundDifferences(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
