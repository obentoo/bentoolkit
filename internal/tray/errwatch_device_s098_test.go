package tray

import "testing"

// The remount rule is per mount point and device: another device mounted
// read-only at a point seen read-write, or a read-only mount stacked on a
// read-write one at the same point, is a first sight, not a remount.
func TestS098ErrwatchMount_AnotherDeviceAtTheSamePointIsAFirstSight(t *testing.T) {
	seen := map[string]bool{}
	if l := classifyMount(errwatchMount("/media/usb", "/dev/sdf1", "vfat", false), seen); l != levelOK {
		t.Fatalf("/dev/sdf1 rw = level %d, want ok", l)
	}
	if l := classifyMount(errwatchMount("/media/usb", "/dev/sr0", "udf", true), seen); l != levelOK {
		t.Errorf("another device read-only at /media/usb = level %d, want ok: it was never seen rw", l)
	}
	if l := classifyMount(errwatchMount("/media/usb", "/dev/sdf1", "vfat", true), seen); l != levelCritical {
		t.Errorf("/dev/sdf1 back read-only at /media/usb = level %d, want critical", l)
	}
}
