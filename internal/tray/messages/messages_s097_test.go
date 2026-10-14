package messages_test

// Story 097, sub-task 3.3: the system-alert strings live in the catalog
// (R2.8, R3.5, R4.5, R5.1, R5.3, R8.3), in English and ASCII.
//
// Hostile half first: texts of different monitors, and the network's down
// and back notices, must never read the same, or the user cannot tell which
// condition a notification or tooltip line is about.

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/tray/messages"
)

// s097Keys are the keys the design adds, by name.
var s097Keys = map[string]messages.Key{
	"SysDiskSummary":    messages.SysDiskSummary,
	"SysDiskBody":       messages.SysDiskBody,
	"SysMemorySummary":  messages.SysMemorySummary,
	"SysMemoryBody":     messages.SysMemoryBody,
	"SysCPUSummary":     messages.SysCPUSummary,
	"SysCPUBody":        messages.SysCPUBody,
	"SysNetDownSummary": messages.SysNetDownSummary,
	"SysNetDownBody":    messages.SysNetDownBody,
	"SysNetBackSummary": messages.SysNetBackSummary,
	"SysAlertDisk":      messages.SysAlertDisk,
	"SysAlertMemory":    messages.SysAlertMemory,
	"SysAlertCPU":       messages.SysAlertCPU,
	"SysAlertNetwork":   messages.SysAlertNetwork,
	"SysAlertsOne":      messages.SysAlertsOne,
	"SysAlertsMany":     messages.SysAlertsMany,
}

// TestS097Messages_MonitorTextsAreDistinct: every monitor's summary, and
// every monitor's tooltip line, reads differently from every other's.
func TestS097Messages_MonitorTextsAreDistinct(t *testing.T) {
	for _, group := range [][]string{
		{"SysDiskSummary", "SysMemorySummary", "SysCPUSummary", "SysNetDownSummary", "SysNetBackSummary"},
		{"SysAlertDisk", "SysAlertMemory", "SysAlertCPU", "SysAlertNetwork"},
	} {
		seen := map[string]string{}
		for _, name := range group {
			text := messages.Text(s097Keys[name])
			if other, dup := seen[text]; dup {
				t.Errorf("%s and %s share the text %q", other, name, text)
			}
			seen[text] = name
		}
	}
}

// TestS097Messages_EveryKeyHasASCIIText: each new key has its own entry
// (not the "messages.Key(N)" fallback), non-empty, printable ASCII only: no
// control, format or no-break-space rune can hide in it.
func TestS097Messages_EveryKeyHasASCIIText(t *testing.T) {
	for name, k := range s097Keys {
		text := messages.Text(k)
		if strings.HasPrefix(text, "messages.Key(") || strings.TrimSpace(text) == "" {
			t.Errorf("%s has no catalog text (got %q)", name, text)
			continue
		}
		for i := 0; i < len(text); i++ {
			if b := text[i]; b < 0x20 || b > 0x7e {
				t.Errorf("%s: byte 0x%02x at %d is not printable ASCII in %q", name, b, i, text)
				break
			}
		}
	}
}

// TestS097Messages_SummariesMatchTheDesign pins the summaries the design
// names. The disk summary takes the watched path as its only argument and
// shows it verbatim, even when the path contains a percent sign.
func TestS097Messages_SummariesMatchTheDesign(t *testing.T) {
	fixed := map[string]string{
		"SysNetDownSummary": "Network down",
		"SysNetBackSummary": "Network back",
		"SysCPUSummary":     "CPU saturated",
		"SysMemorySummary":  "Memory almost exhausted",
	}
	for name, want := range fixed {
		if got := messages.Text(s097Keys[name]); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, path := range []string{"/var/tmp/portage", "/mnt/100%full"} {
		want := "Low disk space on " + path
		if got := messages.Format(messages.SysDiskSummary, path); got != want {
			t.Errorf("Format(SysDiskSummary, %q) = %q, want %q", path, got, want)
		}
	}
}

// TestS097Messages_AlertCountIsSingularForOne is the tooltip title suffix:
// "1 system alert", and the plural for any other count.
func TestS097Messages_AlertCountIsSingularForOne(t *testing.T) {
	for n, want := range map[int]string{
		1: "1 system alert",
		2: "2 system alerts",
		0: "0 system alerts",
	} {
		if got := messages.Count(messages.SysAlertsOne, messages.SysAlertsMany, n); got != want {
			t.Errorf("Count(SysAlertsOne, SysAlertsMany, %d) = %q, want %q", n, got, want)
		}
	}
}

// TestS097Messages_BodiesCarryMeasurements: the disk, memory and CPU bodies
// state measured values (R2.8, R3.5, R4.5), so each is a format with at
// least one verb.
func TestS097Messages_BodiesCarryMeasurements(t *testing.T) {
	for _, name := range []string{"SysDiskBody", "SysMemoryBody", "SysCPUBody"} {
		text := strings.ReplaceAll(messages.Text(s097Keys[name]), "%%", "")
		if !strings.Contains(text, "%") {
			t.Errorf("%s = %q has no format verb for its measurement", name, messages.Text(s097Keys[name]))
		}
	}
}
