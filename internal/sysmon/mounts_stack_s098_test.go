package sysmon_test

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/sysmon"
)

// The kernel lists mounts in creation order: a mount moved over an existing
// mount point keeps its older ID and is listed before the mount it covers.
// ParseMountinfo puts every covering entry after the entry it covers, so the
// last entry of a point is the one a path there reaches; other points keep
// their order.
func TestS098MountinfoParse_MovedMountIsListedAfterWhatItCovers(t *testing.T) {
	table := "" +
		"2150 1 0:20 / /tmp rw - tmpfs tmpfs rw\n" +
		"2191 2192 8:33 / /srv rw,relatime - ext4 /dev/sdc1 ro\n" + // moved on top, listed first
		"2192 2150 8:17 / /srv rw,relatime - ext4 /dev/sdb1 rw\n" + // covered, listed last
		"2193 2150 8:49 / /home rw - ext4 /dev/sdd1 rw\n"
	got, err := sysmon.ParseMountinfo([]byte(table))
	if err != nil {
		t.Fatalf("ParseMountinfo: %v", err)
	}
	s098MountsSame(t, "moved mount", got, []sysmon.Mount{
		{Point: "/tmp", Source: "tmpfs", FSType: "tmpfs"},
		{Point: "/srv", Source: "/dev/sdb1", FSType: "ext4"},
		{Point: "/srv", Source: "/dev/sdc1", FSType: "ext4", ReadOnly: true},
		{Point: "/home", Source: "/dev/sdd1", FSType: "ext4"},
	})
}

// A stack already in order, and a parent loop in a malformed table, keep
// their order instead of looping.
func TestS098MountinfoParse_StackInOrderAndLoopsAreKept(t *testing.T) {
	inOrder := "" +
		"30 1 8:1 / /x rw - ext4 /dev/sda1 rw\n" +
		"31 30 8:1 / /x ro - ext4 /dev/sda1 rw\n"
	got, err := sysmon.ParseMountinfo([]byte(inOrder))
	if err != nil || len(got) != 2 || got[0].ReadOnly || !got[1].ReadOnly {
		t.Errorf("stack in order = %+v, %v; want rw then ro", got, err)
	}
	loop := "" +
		"40 41 8:1 / /y rw - ext4 /dev/sda1 rw\n" +
		"41 40 8:2 / /y rw - ext4 /dev/sda2 rw\n"
	if got, err := sysmon.ParseMountinfo([]byte(loop)); err != nil || len(got) != 2 {
		t.Errorf("parent loop = %+v, %v; want both entries back", got, err)
	}
}
