package tray

// Pinned contract (story 097; the design names the builders but no signature,
// so these are pinned here):
//
//	func diskSysMessage(d sysmon.Disk, c *condition, urgency byte) (notify.Message, string)
//	func memorySysMessage(m sysmon.Memory, c *condition, urgency byte) (notify.Message, string)
//	func cpuSysMessage(p sysmon.Pressure, lasted time.Duration, c *condition, urgency byte) (notify.Message, string)
//	func networkSysMessage(ev netEvent, replacesID uint32, urgency byte) (notify.Message, string)
//
// Each returns the notification to send and the tooltip line. The message is
// System, replaces the condition's last notification id (0 for a condition
// never notified), and carries the urgency it was given. Send escapes the
// body itself, so the builders hand over plain text. Formats follow the
// design's examples: "1.4 GiB free (1.2%)", "412 MiB of 15.5 GiB available",
// "Pressure 86% for 7 min".

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/sysmon"
	"github.com/obentoo/bentoolkit/internal/tray/messages"
)

const (
	msgMiB uint64 = 1 << 20
	msgGiB uint64 = 1 << 30
)

func msgASCII(t *testing.T, what, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			t.Errorf("%s %q carries a non-ASCII byte at %d", what, s, i)
			return
		}
	}
}

func msgWantContains(t *testing.T, what, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("%s = %q, want it to contain %q", what, s, sub)
		}
	}
}

func msgWantEnvelope(t *testing.T, m notify.Message, replaces uint32, urgency byte) {
	t.Helper()
	if !m.System {
		t.Errorf("Message.System = false, want true")
	}
	if m.ReplacesID != replaces {
		t.Errorf("Message.ReplacesID = %d, want %d", m.ReplacesID, replaces)
	}
	if m.Urgency != urgency {
		t.Errorf("Message.Urgency = %d, want %d", m.Urgency, urgency)
	}
	if m.NoticeID != "" {
		t.Errorf("Message.NoticeID = %q, want empty: a system alert is not a notice", m.NoticeID)
	}
	if m.Summary == "" || m.Body == "" {
		t.Errorf("Message summary %q / body %q, want both set", m.Summary, m.Body)
	}
	msgASCII(t, "summary", m.Summary)
}

// TestSysMessageDiskR2_8: the path, the free space in GiB or MiB and the free
// percentage with one decimal.
func TestSysMessageDiskR2_8(t *testing.T) {
	t.Run("GiB amount and percent", func(t *testing.T) {
		d := sysmon.Disk{Path: "/var/tmp/portage", Dev: 1, Size: 125 * msgGiB, Free: 1536 * msgMiB}
		m, line := diskSysMessage(d, &condition{level: levelCritical, notifID: 7}, 2)
		msgWantEnvelope(t, m, 7, 2)
		msgWantContains(t, "summary", m.Summary, "/var/tmp/portage")
		msgWantContains(t, "body", m.Body, "1.5 GiB", "1.2%")
		msgWantContains(t, "tooltip line", line, "/var/tmp/portage")
	})
	t.Run("MiB amount below 1 GiB", func(t *testing.T) {
		d := sysmon.Disk{Path: "/", Dev: 2, Size: 100 * msgGiB, Free: 512 * msgMiB}
		m, _ := diskSysMessage(d, &condition{level: levelCritical}, 2)
		msgWantContains(t, "body", m.Body, "512", "MiB", "0.5%")
	})
	t.Run("hostile: 1023 MiB stays in MiB, 1024 MiB is shown in GiB", func(t *testing.T) {
		under := sysmon.Disk{Path: "/", Dev: 2, Size: 100 * msgGiB, Free: 1023 * msgMiB}
		m, _ := diskSysMessage(under, &condition{level: levelCritical}, 2)
		msgWantContains(t, "body", m.Body, "1023", "MiB")
		at := sysmon.Disk{Path: "/", Dev: 2, Size: 100 * msgGiB, Free: msgGiB}
		m, _ = diskSysMessage(at, &condition{level: levelCritical}, 2)
		msgWantContains(t, "body", m.Body, "GiB")
		if strings.Contains(m.Body, "1024") {
			t.Errorf("body = %q, want 1 GiB shown in GiB, not as 1024 MiB", m.Body)
		}
	})
	t.Run("hostile: a never-notified condition replaces nothing", func(t *testing.T) {
		d := sysmon.Disk{Path: "/var/cache/distfiles", Dev: 3, Size: 500 * msgGiB, Free: 15 * msgGiB}
		m, _ := diskSysMessage(d, &condition{level: levelWarning}, 1)
		msgWantEnvelope(t, m, 0, 1)
	})
	t.Run("hostile: the summary path is plain text, not pre-escaped", func(t *testing.T) {
		d := sysmon.Disk{Path: "/mnt/a&b<c", Dev: 4, Size: 100 * msgGiB, Free: msgGiB}
		m, _ := diskSysMessage(d, &condition{level: levelCritical}, 2)
		msgWantContains(t, "summary", m.Summary, "/mnt/a&b<c")
		if strings.Contains(m.Summary, "&amp;") || strings.Contains(m.Body, "&amp;") {
			t.Errorf("summary %q / body %q pre-escaped; Send escapes the body itself", m.Summary, m.Body)
		}
	})
	t.Run("hostile: nested paths render distinct tooltip lines", func(t *testing.T) {
		seen := map[string]string{}
		for _, p := range []string{"/", "/var/tmp", "/var/tmp/portage"} {
			d := sysmon.Disk{Path: p, Dev: 5, Size: 100 * msgGiB, Free: msgGiB}
			_, line := diskSysMessage(d, &condition{level: levelCritical}, 2)
			if other, dup := seen[line]; dup {
				t.Errorf("paths %q and %q render the same tooltip line %q", other, p, line)
			}
			seen[line] = p
			msgWantContains(t, "tooltip line", line, p)
		}
	})
}

// TestSysMessageMemoryR3_5: available and total memory in MiB or GiB and the
// free swap.
func TestSysMessageMemoryR3_5(t *testing.T) {
	t.Run("with swap", func(t *testing.T) {
		mem := sysmon.Memory{Total: 15872 * msgMiB, Available: 412 * msgMiB, SwapTotal: 8 * msgGiB, SwapFree: 1536 * msgMiB}
		m, line := memorySysMessage(mem, &condition{level: levelCritical, notifID: 11}, 2)
		msgWantEnvelope(t, m, 11, 2)
		msgWantContains(t, "body", m.Body, "412", "MiB", "15.5 GiB", "1.5 GiB")
		if line == "" {
			t.Errorf("tooltip line is empty")
		}
	})
	t.Run("without swap the body still states the free swap", func(t *testing.T) {
		mem := sysmon.Memory{Total: 15872 * msgMiB, Available: 1536 * msgMiB}
		m, _ := memorySysMessage(mem, &condition{level: levelWarning}, 1)
		msgWantEnvelope(t, m, 0, 1)
		msgWantContains(t, "body", m.Body, "1.5 GiB", "15.5 GiB")
		if !strings.Contains(strings.ToLower(m.Body), "swap") {
			t.Errorf("body = %q, want it to state the free swap", m.Body)
		}
	})
}

// TestSysMessageCPUR4_5: the pressure percentage and how long it has lasted.
func TestSysMessageCPUR4_5(t *testing.T) {
	p := sysmon.Pressure{SomeAvg60: 86, HasFull: true}
	m, line := cpuSysMessage(p, 7*time.Minute, &condition{level: levelWarning, notifID: 3}, 1)
	msgWantEnvelope(t, m, 3, 1)
	msgWantContains(t, "body", m.Body, "86", "%", "7 min")
	if line == "" {
		t.Errorf("tooltip line is empty")
	}
}

// TestSysMessageSummariesDistinct: each monitor has its own summary, so the
// notifications of two monitors are told apart.
func TestSysMessageSummariesDistinct(t *testing.T) {
	disk, _ := diskSysMessage(sysmon.Disk{Path: "/", Size: 100 * msgGiB, Free: msgGiB}, &condition{level: levelCritical}, 2)
	mem, _ := memorySysMessage(sysmon.Memory{Total: 16 * msgGiB, Available: 512 * msgMiB}, &condition{level: levelCritical}, 2)
	cpu, _ := cpuSysMessage(sysmon.Pressure{SomeAvg60: 90}, 6*time.Minute, &condition{level: levelWarning}, 1)
	if disk.Summary == mem.Summary || disk.Summary == cpu.Summary || mem.Summary == cpu.Summary {
		t.Errorf("summaries not distinct: disk %q, memory %q, cpu %q", disk.Summary, mem.Summary, cpu.Summary)
	}
}

// TestSysMessageKeys: each builder tags its message with the condition key
// (disk:<path>, memory, cpu). Hostile half first: different paths, nested
// paths included, never share a key, and no disk key equals another
// monitor's key.
func TestSysMessageKeys(t *testing.T) {
	paths := []string{"/", "/var", "/var/tmp", "/var/tmp/portage", "/var/cache/distfiles"}
	seen := map[string]string{}
	for i, p := range paths {
		d := sysmon.Disk{Path: p, Dev: uint64(i + 1), Size: 100 * msgGiB, Free: msgGiB}
		m, _ := diskSysMessage(d, &condition{level: levelCritical, notifID: uint32(i + 1)}, 2)
		if other, dup := seen[m.Key]; dup {
			t.Errorf("paths %q and %q share the key %q", other, p, m.Key)
		}
		seen[m.Key] = p
		if want := "disk:" + p; m.Key != want {
			t.Errorf("diskSysMessage(%q).Key = %q, want %q", p, m.Key, want)
		}
		msgWantEnvelope(t, m, uint32(i+1), 2)
	}
	mem, _ := memorySysMessage(sysmon.Memory{Total: 16 * msgGiB, Available: 512 * msgMiB}, &condition{level: levelCritical, notifID: 21}, 2)
	if mem.Key != "memory" {
		t.Errorf("memorySysMessage().Key = %q, want %q", mem.Key, "memory")
	}
	msgWantEnvelope(t, mem, 21, 2)
	cpu, _ := cpuSysMessage(sysmon.Pressure{SomeAvg60: 90}, 6*time.Minute, &condition{level: levelWarning, notifID: 22}, 1)
	if cpu.Key != "cpu" {
		t.Errorf("cpuSysMessage().Key = %q, want %q", cpu.Key, "cpu")
	}
	msgWantEnvelope(t, cpu, 22, 1)
	for _, k := range []string{mem.Key, cpu.Key} {
		if p, dup := seen[k]; dup {
			t.Errorf("disk %q and another monitor share the key %q", p, k)
		}
	}
}

func msgWantCatalogText(t *testing.T, what, got string) {
	t.Helper()
	if got == "" || strings.Contains(got, "%!") || strings.Contains(got, "messages.Key(") {
		t.Errorf("%s = %q, want catalog text", what, got)
	}
	msgASCII(t, what, got)
}

// TestSysMessageNetwork: the down notification at the urgency given, the back
// notification at low urgency replacing the down, both keyed "network".
// Hostile half first: the network key never equals another monitor's key,
// and a back with no recorded down id replaces nothing.
func TestSysMessageNetwork(t *testing.T) {
	down, downLine := networkSysMessage(netDown, 0, 1)
	back, _ := networkSysMessage(netBack, 7, 0)
	t.Run("hostile: the network key is its own", func(t *testing.T) {
		disk, _ := diskSysMessage(sysmon.Disk{Path: "/", Size: 100 * msgGiB, Free: msgGiB}, &condition{level: levelCritical}, 2)
		mem, _ := memorySysMessage(sysmon.Memory{Total: 16 * msgGiB, Available: 512 * msgMiB}, &condition{level: levelCritical}, 2)
		cpu, _ := cpuSysMessage(sysmon.Pressure{SomeAvg60: 90}, 6*time.Minute, &condition{level: levelWarning}, 1)
		for _, other := range []string{disk.Key, mem.Key, cpu.Key} {
			if down.Key == other || back.Key == other {
				t.Errorf("network keys %q/%q collide with another monitor's key %q", down.Key, back.Key, other)
			}
		}
	})
	t.Run("hostile: a back with replacesID 0 carries 0", func(t *testing.T) {
		m, _ := networkSysMessage(netBack, 0, 0)
		if m.ReplacesID != 0 {
			t.Errorf("back with replacesID 0: ReplacesID = %d, want 0", m.ReplacesID)
		}
	})
	t.Run("down", func(t *testing.T) {
		if down.Key != "network" || !down.System {
			t.Errorf("down Key = %q System = %v, want \"network\"/true", down.Key, down.System)
		}
		if down.Urgency != 1 {
			t.Errorf("down Urgency = %d, want 1 (normal, as given)", down.Urgency)
		}
		if want := messages.Text(messages.SysNetDownSummary); down.Summary != want {
			t.Errorf("down Summary = %q, want %q", down.Summary, want)
		}
		msgWantCatalogText(t, "down body", down.Body)
		if down.Summary == back.Summary {
			t.Errorf("down and back share the summary %q", down.Summary)
		}
		msgWantCatalogText(t, "down tooltip line", downLine)
	})
	t.Run("back", func(t *testing.T) {
		if back.Key != "network" || !back.System {
			t.Errorf("back Key = %q System = %v, want \"network\"/true", back.Key, back.System)
		}
		if back.Urgency != 0 {
			t.Errorf("back Urgency = %d, want 0 (low)", back.Urgency)
		}
		if back.ReplacesID != 7 {
			t.Errorf("back ReplacesID = %d, want 7 (the down it replaces)", back.ReplacesID)
		}
		if want := messages.Text(messages.SysNetBackSummary); back.Summary != want {
			t.Errorf("back Summary = %q, want %q", back.Summary, want)
		}
	})
}
