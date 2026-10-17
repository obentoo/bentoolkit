package tray

// Pinned contract (story 098, design.md "internal/tray/sample.go and
// app.go"): monitor names "emerge", "units", "readonly", "smart";
// notificationKey has an explicit case per monitor ("emerge", one shared
// "units", "readonly:<mount point>", "smart:<object path>", "cpu") and a
// default returning key.Monitor; alerts() lists SMART, read-only, story
// 097's disk/memory/CPU/network block, emerge, then units sorted by manager
// then name.
//
// Hostile halves first: a new monitor must never fall back to the CPU key
// (its withdraw would close the CPU notification), two mount points or two
// drives must never share a key, and the same target under two monitors must
// not either; only then the shared units key, the one place two targets must
// come together.

import (
	"slices"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/tray/messages"
)

// TestS098Keys_R5_7_NoMonitorFallsBackToCPU: every 098 monitor, and a
// monitor this story does not know, gets a key that is not the CPU key.
func TestS098Keys_R5_7_NoMonitorFallsBackToCPU(t *testing.T) {
	for _, k := range []condKey{
		{Monitor: "emerge"},
		{Monitor: "units", Target: "system:sshd.service"},
		{Monitor: "readonly", Target: "/home"},
		{Monitor: "smart", Target: "/org/freedesktop/UDisks2/drives/Samsung_SSD_980_S1"},
		{Monitor: "some-future-monitor", Target: "x"},
	} {
		if got := notificationKey(k); got == "cpu" {
			t.Errorf("notificationKey(%+v) = %q: a non-CPU condition must never take the CPU key", k, got)
		}
	}
	if got := notificationKey(condKey{Monitor: "some-future-monitor", Target: "x"}); got != "some-future-monitor" {
		t.Errorf("notificationKey of an unknown monitor = %q, want the monitor name %q", got, "some-future-monitor")
	}
}

// TestS098Keys_R5_4_R5_7_DistinctTargetsAndMonitorsNeverShareAKey: two mount
// points, two drives, and one target string under each of the disk, read-only
// and SMART monitors all get distinct keys, distinct from memory, CPU,
// network, emerge and units.
func TestS098Keys_R5_4_R5_7_DistinctTargetsAndMonitorsNeverShareAKey(t *testing.T) {
	keys := []condKey{
		{Monitor: "readonly", Target: "/mnt/data"},
		{Monitor: "readonly", Target: "/mnt/data2"},
		{Monitor: "readonly", Target: "/mnt/my disk"},
		{Monitor: "smart", Target: "/org/freedesktop/UDisks2/drives/A"},
		{Monitor: "smart", Target: "/org/freedesktop/UDisks2/drives/B"},
		{Monitor: "disk", Target: "/x"},
		{Monitor: "readonly", Target: "/x"},
		{Monitor: "smart", Target: "/x"},
		{Monitor: "memory"},
		{Monitor: "cpu"},
		{Monitor: "network"},
		{Monitor: "emerge"},
		{Monitor: "units", Target: "system:a.service"},
	}
	seen := map[string]condKey{}
	for _, k := range keys {
		got := notificationKey(k)
		if other, dup := seen[got]; dup {
			t.Errorf("notificationKey(%+v) = notificationKey(%+v) = %q: two conditions share one notification", k, other, got)
		}
		seen[got] = k
	}
}

// TestS098Keys_R5_4_ExactKeys pins each key as the design spells it, the
// shared units key included: every unit, of either manager, is one key.
func TestS098Keys_R5_4_ExactKeys(t *testing.T) {
	cases := []struct {
		key  condKey
		want string
	}{
		{condKey{Monitor: "emerge"}, "emerge"},
		{condKey{Monitor: "units", Target: "system:sshd.service"}, "units"},
		{condKey{Monitor: "units", Target: "user:pipewire.service"}, "units"},
		{condKey{Monitor: "readonly", Target: "/home"}, "readonly:/home"},
		{condKey{Monitor: "smart", Target: "/org/freedesktop/UDisks2/drives/A"}, "smart:/org/freedesktop/UDisks2/drives/A"},
		{condKey{Monitor: "cpu"}, "cpu"},
		{condKey{Monitor: "memory"}, "memory"},
		{condKey{Monitor: "network"}, "network"},
		{condKey{Monitor: "disk", Target: "/var/tmp"}, "disk:/var/tmp"},
	}
	for _, tc := range cases {
		if got := notificationKey(tc.key); got != tc.want {
			t.Errorf("notificationKey(%+v) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// keysApp is an App with no running loop, for alerts().
func keysApp(t *testing.T, diskPaths ...string) *App {
	t.Helper()
	mon, _ := config.TrayConfig{}.GetMonitors()
	a := New(Deps{DiskPaths: diskPaths, Monitors: &mon})
	a.network = newNetWatch(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), mon.Network)
	return a
}

func keysActive(a *App, k condKey, l level, text string) {
	a.health[k] = &condition{level: l, text: text}
}

// TestS098Alerts_R5_5_TooltipOrder: SMART lines, read-only lines, the 097
// block (disk, memory, CPU, network), emerge, then units sorted by manager
// then unit name; any critical makes the tooltip critical.
func TestS098Alerts_R5_5_TooltipOrder(t *testing.T) {
	a := keysApp(t, "/")
	// Inserted in an order that matches none of the wanted ones.
	keysActive(a, condKey{Monitor: "units", Target: "user:b.service"}, levelWarning, "UNIT user b")
	keysActive(a, condKey{Monitor: "emerge"}, levelWarning, "EMERGE")
	keysActive(a, condKey{Monitor: "units", Target: "system:z.service"}, levelWarning, "UNIT system z")
	keysActive(a, condKey{Monitor: "cpu"}, levelWarning, "CPU")
	keysActive(a, condKey{Monitor: "readonly", Target: "/home"}, levelCritical, "RO /home")
	keysActive(a, condKey{Monitor: "memory"}, levelWarning, "MEMORY")
	keysActive(a, condKey{Monitor: "units", Target: "user:a.service"}, levelWarning, "UNIT user a")
	keysActive(a, condKey{Monitor: "disk", Target: "/"}, levelWarning, "DISK /")
	keysActive(a, condKey{Monitor: "smart", Target: "/org/freedesktop/UDisks2/drives/A"}, levelCritical, "SMART A")
	keysActive(a, condKey{Monitor: "units", Target: "system:a.service"}, levelWarning, "UNIT system a")
	a.network.notifiedDown = true
	netLine := messages.Text(messages.SysAlertNetwork)

	for range 20 { // map order must not leak into the tooltip
		lines, critical, _ := a.alerts()
		if !critical {
			t.Fatalf("alerts() critical = false with SMART and read-only criticals active")
		}
		if len(lines) != 11 {
			t.Fatalf("alerts() = %q, want 11 lines (10 conditions and the network)", lines)
		}
		want := []string{"SMART A", "RO /home", "DISK /", "MEMORY", "CPU", netLine,
			"EMERGE", "UNIT system a", "UNIT system z", "UNIT user a", "UNIT user b"}
		if !slices.Equal(lines, want) {
			t.Fatalf("alerts() = %q\nwant         %q", lines, want)
		}
	}
}

// TestS098Alerts_R5_5_OkAndTextlessConditionsAreNotListed: a cleared 098
// condition, or one with no tooltip line, shows nothing; a warning alone is
// not critical.
func TestS098Alerts_R5_5_OkAndTextlessConditionsAreNotListed(t *testing.T) {
	a := keysApp(t)
	keysActive(a, condKey{Monitor: "smart", Target: "/org/freedesktop/UDisks2/drives/A"}, levelOK, "")
	keysActive(a, condKey{Monitor: "readonly", Target: "/home"}, levelCritical, "")
	keysActive(a, condKey{Monitor: "units", Target: "system:a.service"}, levelOK, "")
	keysActive(a, condKey{Monitor: "emerge"}, levelWarning, "EMERGE")
	lines, critical, _ := a.alerts()
	if !slices.Equal(lines, []string{"EMERGE"}) {
		t.Errorf("alerts() = %q, want only the emerge line", lines)
	}
	if critical {
		t.Errorf("alerts() critical = true; the only listed line is a warning")
	}
}
