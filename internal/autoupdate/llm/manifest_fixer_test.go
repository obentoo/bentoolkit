package llm

import (
	"fmt"
	"os/exec"
	"testing"
)

// exitErrWithCode runs a trivial `sh -c "exit N"` to manufacture a real
// *exec.ExitError carrying the given code, so formatFixerError's errors.As/
// ExitCode() extraction (AD5) is exercised without invoking the real claude CLI.
func exitErrWithCode(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if err == nil {
		t.Fatalf("expected a non-nil exit error for code %d", code)
	}
	return err
}
