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
// child and every process it started are stopped (R1.1, R1.2, R1.3, R1.6):
//
//  1. at Start, the child becomes the leader of a new process group;
//  2. cmd.Cancel sends SIGTERM to the whole group at once;
//  3. GracePeriod later, whatever is left of the group gets SIGKILL;
//  4. cmd.WaitDelay is GracePeriod, so Wait returns by then even if a process
//     that left the group (setsid) still holds the output pipe.
//
// Step 3 is a timer armed by Cancel, not left to WaitDelay, because when
// WaitDelay expires os/exec calls Process.Kill, which reaches the group LEADER
// only: a descendant that ignored SIGTERM would outlive it, orphaned and still
// running — the very defect this package fixes.
//
// A group that is already gone when the context is done is not an error:
// Cancel returns os.ErrProcessDone, which os/exec reads as "it finished on its
// own". Any other refusal is returned wrapped, naming the group.
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
// os/exec reads as "it finished on its own" (R1.6); any other refusal is
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
// prompt there: sudo's password, git's ssh passphrase or https credentials
// (R1.4). A child moved to a background group would be stopped by SIGTTIN at
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
// wrapped, naming the pid (R3.7).
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
// orphan happens to finish (S046-R1.3, S046-R1.4). It is how overlay manifest
// has stopped pkgdev since story 046, moved here unchanged (R8.1): until story
// 054 it was stopWithDescendants, in package overlay.
//
// Configure an unstarted cmd with it. At Start the child becomes the leader of
// a new process group; when the context is done, cmd.Cancel sends SIGKILL to
// the whole group at once. There is no SIGTERM, no grace and no WaitDelay. A
// group that is already gone is os.ErrProcessDone. Any other refusal is
// returned exactly as kill(2) gave it, unwrapped, because that is the error
// overlay manifest has always reported; os/exec hands it to Wait's caller only
// when the child then exits 0.
//
// New spawners use Group. This mode exists so that an interrupted pkgdev is
// asked to stop exactly as it always has been.
//
// # The problem it solves, measured rather than assumed
//
// exec.CommandContext kills the DIRECT child on cancellation and nothing below
// it. pkgdev is a script: the process we spawn forks fetchers and helpers, and
// each of them inherits the pipe that cmd.Stdout is being copied from. Wait does
// not return while any descriptor for that pipe is open — the standard library
// says so in as many words ("If WaitDelay is zero, I/O pipes will be read until
// EOF, which might not occur until orphaned subprocesses of the command have
// also closed their descriptors") — so a cancelled target held the whole run
// open for the LIFETIME OF A GRANDCHILD. With a child that sleeps 30 seconds,
// Wait measured 30.002s after a cancel delivered at 300ms.
//
// That is not a slow interrupt, it is a broken one: the report is assembled
// before it is rendered (S046-R1.3), so a run that cannot return cannot report,
// and S046-R1.4's "render the report established up to that point" never
// happens.
//
// # Why the whole GROUP is killed, and not just bounded with WaitDelay
//
// cmd.WaitDelay is this repository's usual answer (internal/snapshot/runner.go,
// the three autoupdate fixers) and it does return promptly — but it returns by
// ABANDONING the descendant: the same measurement showed the grandchild still
// running afterwards. For a manifest run those descendants are network fetchers
// holding a distdir this process is about to delete, so "prompt" would be bought
// with orphaned downloads writing into a directory that no longer exists.
//
// WaitDelay also has a second edge that does not fit here: its timer starts when
// the child EXITS as well as when the context is done, so a lingering grandchild
// after a perfectly successful pkgdev would turn a regenerated Manifest into a
// reported failure. Story 046 exists to stop reports claiming things that did
// not happen.
//
// Putting the child in its own process group and signalling the NEGATED pid
// signals every process in it. Nothing is left holding the pipe, so Wait returns
// because the work really ended. The same measurement: 301ms.
//
// # What it costs
//
//  1. The child is no longer in the terminal's foreground process group, so a
//     ctrl+c typed at a terminal no longer reaches pkgdev directly. It reaches
//     it through us — `overlay manifest` wires signal.NotifyContext for exactly
//     this and cancels the run context, which is what calls the function below.
//     The delivery path becomes one we control instead of two racing ones.
//  2. A descendant that calls setsid() leaves the group and is beyond this
//     signal. Such a process would already hang a run that was never cancelled,
//     so nothing here is made worse; it is simply not made better either.
//  3. SIGKILL, not SIGTERM, because that is the signal exec.CommandContext
//     already sends its direct child (Process.Kill). Sending something gentler
//     to the group would change what an interrupted pkgdev is asked to do: a
//     decision story 046 had no reason to take, and one R8.1 keeps untaken.
//     Group is the mode that asks with SIGTERM first.
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
