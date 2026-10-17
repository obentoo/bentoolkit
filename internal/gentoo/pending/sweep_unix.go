//go:build unix

package pending

import "syscall"

// sweepGroup sends SIGKILL to whatever is left of process group pgid once its
// leader has been waited for: a helper the command left behind would
// otherwise outlive a normal exit, still holding the output pipe.
//
// Neither refusal leaves anything to do. ESRCH means the group is empty, the
// usual case; EPERM means what is left runs as another user. -pgid still names
// this group while any of it runs: the kernel does not hand out a pid number
// that a live process uses as its group id.
func sweepGroup(pgid int) {
	syscall.Kill(-pgid, syscall.SIGKILL) //nolint:errcheck // best effort: see the doc above
}
