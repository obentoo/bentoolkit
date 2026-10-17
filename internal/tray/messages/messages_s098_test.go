package messages_test

// Story 098: the emerge, units, read-only and SMART strings live in the
// catalog, in English and ASCII (design.md "internal/tray/messages").
//
// Hostile half first: no new summary or tooltip line may read the same as
// another monitor's, 097's included, or the user cannot tell which condition
// a notification or tooltip line is about.

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/tray/messages"
)

// s098Keys are the keys the design adds, by name.
var s098Keys = map[string]messages.Key{
	"SysEmergeSummary":   messages.SysEmergeSummary,
	"SysEmergeBody":      messages.SysEmergeBody,
	"SysAlertEmerge":     messages.SysAlertEmerge,
	"SysUnitsSummary":    messages.SysUnitsSummary,
	"SysUnitsBody":       messages.SysUnitsBody,
	"SysAlertUnit":       messages.SysAlertUnit,
	"SysReadonlySummary": messages.SysReadonlySummary,
	"SysReadonlyBody":    messages.SysReadonlyBody,
	"SysAlertReadonly":   messages.SysAlertReadonly,
	"SysSmartSummary":    messages.SysSmartSummary,
	"SysSmartBody":       messages.SysSmartBody,
	"SysAlertSmart":      messages.SysAlertSmart,
}

// TestS098Messages_TextsAreDistinctFromEveryMonitor: the new summaries and
// tooltip lines differ from each other and from story 097's.
func TestS098Messages_TextsAreDistinctFromEveryMonitor(t *testing.T) {
	for _, group := range []map[string]messages.Key{
		{
			"SysEmergeSummary": messages.SysEmergeSummary, "SysUnitsSummary": messages.SysUnitsSummary,
			"SysReadonlySummary": messages.SysReadonlySummary, "SysSmartSummary": messages.SysSmartSummary,
			"SysDiskSummary": messages.SysDiskSummary, "SysMemorySummary": messages.SysMemorySummary,
			"SysCPUSummary": messages.SysCPUSummary, "SysNetDownSummary": messages.SysNetDownSummary,
			"SysNetBackSummary": messages.SysNetBackSummary,
		},
		{
			"SysAlertEmerge": messages.SysAlertEmerge, "SysAlertUnit": messages.SysAlertUnit,
			"SysAlertReadonly": messages.SysAlertReadonly, "SysAlertSmart": messages.SysAlertSmart,
			"SysAlertDisk": messages.SysAlertDisk, "SysAlertMemory": messages.SysAlertMemory,
			"SysAlertCPU": messages.SysAlertCPU, "SysAlertNetwork": messages.SysAlertNetwork,
		},
	} {
		seen := map[string]string{}
		for name, k := range group {
			text := messages.Text(k)
			if other, dup := seen[text]; dup {
				t.Errorf("%s and %s share the text %q", other, name, text)
			}
			seen[text] = name
		}
	}
}

// TestS098Messages_EveryKeyHasASCIIText: each new key has its own entry
// (not the "messages.Key(N)" fallback), non-empty, printable ASCII only.
func TestS098Messages_EveryKeyHasASCIIText(t *testing.T) {
	for name, k := range s098Keys {
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
