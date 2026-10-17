package tray

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/sysmon"
)

// A read-only bind stacked on a read-write mount of the same device lists two
// entries at one point; only the top one is what a write reaches. Seen like
// that from the first sample, it is intentional (R3.4), not a remount. A real
// remount of the top entry later is still critical (R3.3).
func TestS098SampleStack_ReadOnlyBindOnTopIsAFirstSight(t *testing.T) {
	a := &App{seenRW: map[string]bool{}}
	rw := sysmon.Mount{Point: "/srv", Source: "/dev/sdb1", FSType: "ext4"}
	ro := rw
	ro.ReadOnly = true

	if got := a.classifyMounts([]sysmon.Mount{rw, ro})["/srv"].level; got != levelOK {
		t.Fatalf("rw with a ro bind on top, first sample = level %d, want ok", got)
	}
	if got := a.classifyMounts([]sysmon.Mount{rw, ro})["/srv"].level; got != levelOK {
		t.Errorf("the same stack on the next sample = level %d, want ok", got)
	}

	b := &App{seenRW: map[string]bool{}}
	if got := b.classifyMounts([]sysmon.Mount{rw})["/srv"].level; got != levelOK {
		t.Fatalf("/srv rw = level %d, want ok", got)
	}
	if got := b.classifyMounts([]sysmon.Mount{ro})["/srv"].level; got != levelCritical {
		t.Errorf("/srv remounted ro = level %d, want critical", got)
	}
}
