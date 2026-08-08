//go:build linux

package validate

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// probeNetNS is the seam ProbeIsolation measures through. It is a variable so
// both branches — granted and denied — are reachable from a test on a host
// that has privilege and on one that does not.
var probeNetNS = attemptNetNS

// netNSProbeTimeout bounds the probe child. It is short because the answer
// arrives at fork time: either clone() returns the namespace or it returns
// EPERM. The timeout is here so a wedged child cannot hold up a compile gate,
// not because the measurement is expected to be slow.
const netNSProbeTimeout = 10 * time.Second

// attemptNetNS asks the kernel for a network namespace by starting a
// short-lived child with CLONE_NEWNET requested at fork time.
//
// # What is actually being measured
//
// Only whether the child could be STARTED. clone(CLONE_NEWNET) without
// CAP_SYS_ADMIN fails with EPERM before the child exists, so Start's error is
// the kernel's answer. The child's own exit status says nothing about isolation
// — it is reaped and discarded, and its output goes to io.Discard.
//
// The binary launched is this one, with --version. Any binary would do; using
// our own removes a dependency on some other executable being at a known path
// (design D6). Under `go test` os.Executable() is the test binary, which is
// exactly why probeNetNS is a seam: no test calls this.
func attemptNetNS() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating this executable to probe with: %w", err)
	}

	// ProbeIsolation takes no context by design (D6): it answers a question
	// about THIS process rather than about a request, so the probe owns its own
	// deadline. netNSProbeTimeout bounds the child, and nothing here outlives
	// the call.
	ctx, cancel := context.WithTimeout(context.Background(), netNSProbeTimeout) // SAFE: no caller context by design (D6); bounded by netNSProbeTimeout
	defer cancel()

	// The only "variable" here is our own path from os.Executable(). Nothing
	// external reaches this call — no argument comes from an ebuild, an archive
	// or a flag — and the probe measures the fork, not what the child does.
	//nolint:gosec // G204: the binary is this process's own, via os.Executable()
	cmd := exec.CommandContext(ctx, self, "--version")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}

	if err := cmd.Start(); err != nil {
		return err
	}
	// Reaped, never judged: a non-zero exit from the child is not a failure to
	// isolate. The namespace question was already answered by Start.
	_ = cmd.Wait()
	return nil
}
