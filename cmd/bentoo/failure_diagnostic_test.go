package main

import (
	"errors"
	"fmt"
	"testing"
)

// TestFailureDiagnostic pins what a caller that reports an error itself shows:
// the cause of a failWith status, never its bare "exit status 1", which is how
// every refused repair session in offerRepairs used to read.
func TestFailureDiagnostic(t *testing.T) {
	cause := errors.New("--repair dev-libs/liborcus: the session's permission rules cannot carry the path")
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"failWith shows its cause":            {failWith(1, cause), cause.Error()},
		"wrapped failWith shows its cause":    {fmt.Errorf("offer: %w", failWith(1, cause)), cause.Error()},
		"bare exitWith keeps its own text":    {exitWith(2), "exit status 2"},
		"a plain error is shown as it stands": {cause, cause.Error()},
	} {
		t.Run(name, func(t *testing.T) {
			if got := failureDiagnostic(tc.err); got != tc.want {
				t.Errorf("failureDiagnostic(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
