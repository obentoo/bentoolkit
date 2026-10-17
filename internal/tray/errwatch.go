package tray

import (
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/desktop/smartmon"
	"github.com/obentoo/bentoolkit/internal/sysmon"
)

// emergeTracker turns emerge.log events into the emerge condition. A package
// counts as failed when it started and an unsuccessful exit arrived before it
// completed. emerge.log carries no process id, so concurrent runs are not
// told apart: a successful exit clears the failure but keeps every package
// still started, so a later failed exit of the other run still names it.
type emergeTracker struct {
	started map[string]bool
	order   []string // started atoms, in the order they started
	failed  []string // the atoms the active condition names; empty while ok
}

// statusInterrupted is the exit status of an emerge stopped with Ctrl+C.
const statusInterrupted = 130

// feed applies one event. ok reports a change of the condition: a non-nil
// fail raises (or replaces) it naming the failed atoms in start order, a nil
// fail clears it. A completion of only some failed atoms changes nothing
// here; the caller reads failed for the tooltip.
func (t *emergeTracker) feed(ev sysmon.EmergeEvent) (fail []string, ok bool) {
	switch ev.Kind {
	case sysmon.EmergeStarted:
		if !t.started[ev.Atom] {
			t.started[ev.Atom] = true
			t.order = append(t.order, ev.Atom)
		}
	case sysmon.EmergeCompleted:
		t.forget(ev.Atom)
		if i := slices.Index(t.failed, ev.Atom); i >= 0 {
			t.failed = slices.Delete(t.failed, i, i+1)
			if len(t.failed) == 0 {
				return nil, true
			}
		}
	case sysmon.EmergeExitOK:
		t.failed = nil
		return nil, true
	case sysmon.EmergeExitFailed:
		if ev.Status == statusInterrupted {
			t.reset()
			return nil, false
		}
		if len(t.order) == 0 {
			return nil, false
		}
		t.failed = slices.Clone(t.order)
		t.reset()
		return slices.Clone(t.failed), true
	}
	return nil, false
}

// forget drops atom from the started set.
func (t *emergeTracker) forget(atom string) {
	if !t.started[atom] {
		return
	}
	delete(t.started, atom)
	t.order = slices.DeleteFunc(t.order, func(a string) bool { return a == atom })
}

// reset forgets every started atom.
func (t *emergeTracker) reset() {
	clear(t.started)
	t.order = nil
}

// unwatchedFSTypes are pseudo, network and image filesystems whose read-only
// state says nothing about a failing disk.
var unwatchedFSTypes = []string{"tmpfs", "squashfs", "overlay", "nfs", "nfs4", "cifs", "iso9660"}

// watchedMount reports whether m is a block-device filesystem the read-only
// monitor watches.
func watchedMount(m sysmon.Mount) bool {
	if !strings.HasPrefix(m.Source, "/dev/") || strings.HasPrefix(m.FSType, "fuse.") {
		return false
	}
	return !slices.Contains(unwatchedFSTypes, m.FSType)
}

// classifyMount is the level of mount m. seenRW holds the mounts seen
// read-write, keyed by mountKey; a watched mount seen rw and now ro is
// critical, one read-only since its first sight is intentional. The key holds
// the device too, so another device mounted read-only at the same point is a
// first sight, not a remount. The caller deletes the keys of mounts that left
// the table, so a later mount there starts afresh.
func classifyMount(m sysmon.Mount, seenRW map[string]bool) level {
	if !watchedMount(m) {
		return levelOK
	}
	key := mountKey(m)
	if !m.ReadOnly {
		seenRW[key] = true
		return levelOK
	}
	if seenRW[key] {
		return levelCritical
	}
	return levelOK
}

// mountKey is the seenRW key of m: its mount point and its device.
func mountKey(m sysmon.Mount) string {
	return m.Point + "\x00" + m.Source
}

// classifyDrive is the level of drive d: critical when SMART reports it
// failing or the NVMe controller lists any critical warning.
func classifyDrive(d smartmon.Drive) level {
	if d.Failing || len(d.Warnings) > 0 {
		return levelCritical
	}
	return levelOK
}
