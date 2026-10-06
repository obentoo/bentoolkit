//go:build unix

package procgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Group configures the unstarted cmd so that, when its context is done, the
// child and every process it started are stopped:
//
//  1. at Start, the child becomes the leader of a new process group;
//  2. cmd.Cancel sends SIGTERM to the whole group at once;
//  3. GracePeriod later, whatever is left of the group gets SIGKILL;
//  4. cmd.WaitDelay is GracePeriod, so Wait returns by then even if a process
//     that left the group (setsid) still holds the output pipe.
//
// Step 3 is a timer armed by Cancel, not left to WaitDelay, because WaitDelay's
// Process.Kill reaches the group LEADER only, leaving a SIGTERM-ignoring
// descendant orphaned and running. A group already gone is not an error
// (os.ErrProcessDone); any other refusal is returned wrapped, naming the group.
//
// Only Setpgid (and a zero Pgid, so the child LEADS the group and -pid names
// it) is written into cmd.SysProcAttr; every other field the caller set there
// is kept. The package documentation says what leaving the terminal's
// foreground group costs.
func Group(cmd *exec.Cmd) {
	leadNewGroup(cmd)
	delay := grace
	cmd.Cancel = func() error {
		// Process is non-nil: os/exec calls Cancel only after a successful Start.
		pid := cmd.Process.Pid
		if err := signalGroup(pid, syscall.SIGTERM, "SIGTERM"); err != nil {
			return err
		}
		time.AfterFunc(delay, func() { killGroup(pid) })
		return nil
	}
	cmd.WaitDelay = delay
}

// signalGroup sends sig, called name in the error, to every process in process
// group pgid. A group that no longer exists is os.ErrProcessDone, the answer
// os/exec reads as "it finished on its own"; any other refusal is
// returned wrapped, naming the group.
func signalGroup(pgid int, sig syscall.Signal, name string) error {
	err := syscall.Kill(-pgid, sig)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.ESRCH):
		return os.ErrProcessDone
	default:
		return fmt.Errorf("sending %s to process group %d: %w", name, pgid, err)
	}
}

// killGroup is Group's escalation: SIGKILL to every process still in process
// group pgid, GracePeriod after the SIGTERM.
//
// A timer runs it, so no caller is left to take an error, and neither refusal
// kill(2) can give here leaves anything to do. ESRCH means nothing of the group
// is left: it ended on SIGTERM, which is the success case. EPERM means all that
// is left runs as another user, beyond the SIGTERM as well. WaitDelay, due at
// the same instant, returns Wait either way.
//
// -pgid still names this group while any of it runs: the kernel does not hand
// out a pid number that a live process uses as its group id. The number can
// come back only after the whole group has ended, and pid numbers are handed
// out cyclically, so that takes kernel.pid_max new processes (32768 at the
// least) within the grace.
func killGroup(pgid int) {
	syscall.Kill(-pgid, syscall.SIGKILL) //nolint:errcheck // no caller to take it: see the doc above
}

// Foreground configures the unstarted cmd for a child that must stay in the
// caller's process group — the terminal's foreground group — because it may
// prompt there: sudo's password, git's ssh passphrase or https credentials.
// A child moved to a background group would be stopped by SIGTTIN at
// its first read, and the operator could no longer signal it from the terminal.
//
// When the context is done, cmd.Cancel sends SIGTERM to the direct child only;
// sudo relays SIGTERM to the command it runs, though never SIGKILL. If the
// child is still running GracePeriod later, os/exec SIGKILLs it: cmd.WaitDelay
// is GracePeriod, and once it expires os/exec calls Process.Kill, which reaches
// the direct child — here, exactly the process to stop.
//
// A child that has already exited is not an error (os.ErrProcessDone). Any
// other refusal — EPERM from a child now running as another user — is returned
// wrapped, naming the pid.
func Foreground(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		// Process is non-nil: os/exec calls Cancel only after a successful Start.
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			if errors.Is(err, os.ErrProcessDone) {
				return os.ErrProcessDone
			}
			return fmt.Errorf("sending SIGTERM to process %d: %w", cmd.Process.Pid, err)
		}
		return nil
	}
	cmd.WaitDelay = grace
}

// KillGroupNow makes a cancelled run's child die together with everything it
// spawned, so cmd.Wait comes back when the work stops rather than when the last
// orphan finishes. It is how overlay manifest stops pkgdev; new spawners use
// Group. The child leads a new process group, and on cancel cmd.Cancel sends
// SIGKILL to the whole group at once — no SIGTERM, no grace, no WaitDelay. A
// group already gone is os.ErrProcessDone; any other refusal is returned
// unwrapped, as kill(2) gave it, as overlay manifest always reported.
//
// Why the group and not WaitDelay: pkgdev's fetchers inherit the stdout pipe,
// so a cancelled run lasted as long as a grandchild (30.002s against a 30s
// sleeper, cancel at 300ms) and could not render its partial report. WaitDelay
// returns by ABANDONING those fetchers onto a distdir about to be deleted, and
// its timer also fires after a NORMAL exit, turning a lingering helper into a
// reported failure. Killing the group ends the work: 301ms.
//
// The cost: ctrl+c no longer reaches pkgdev directly but through
// signal.NotifyContext in `overlay manifest`; a setsid() descendant escapes
// (it would hang an uncancelled run anyway); and SIGKILL stays because it is
// what exec.CommandContext already sent. Group is the mode that asks first.
func KillGroupNow(cmd *exec.Cmd) {
	leadNewGroup(cmd)

	cmd.Cancel = func() error {
		// Process is non-nil here: the standard library documents that Cancel is
		// not called if Start returned an error, and it is only armed after a
		// successful Start. Setpgid with a zero Pgid makes the child the leader
		// of a new group whose id IS its pid, which is what -pid then names.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				// The group is already gone: the command finished in the instant
				// between the context being done and this call. os.ErrProcessDone
				// is the answer exec documents for that race — anything else
				// would make Wait report an error for a target that completed,
				// which is a failure invented by the reporting path.
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}

// leadNewGroup makes cmd's child, at Start, the leader of a new process group
// whose id is its own pid, which is what -pid then names. It writes Setpgid and
// a zero Pgid into the caller's SysProcAttr instead of replacing it, so every
// other field already set there (credentials, say) is kept.
func leadNewGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
}
