package messages_test

// Story 099, sub-task 5.2: the catalog entries for the update notices
// (design.md, "internal/tray/messages"), English and ASCII. Hostile half
// first: the four notices must read differently, or the user cannot tell an
// advisory from pending updates.

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/tray/messages"
)

var s099Keys = map[string]messages.Key{
	"SysUpdatesSummary":     messages.SysUpdatesSummary,
	"SysUpdatesBody":        messages.SysUpdatesBody,
	"SysInfoUpdates":        messages.SysInfoUpdates,
	"SysWorldBrokenSummary": messages.SysWorldBrokenSummary,
	"SysWorldBrokenBody":    messages.SysWorldBrokenBody,
	"SysInfoWorldBroken":    messages.SysInfoWorldBroken,
	"SysGLSASummary":        messages.SysGLSASummary,
	"SysGLSABody":           messages.SysGLSABody,
	"SysAlertGLSA":          messages.SysAlertGLSA,
	"SysSyncSummary":        messages.SysSyncSummary,
	"SysSyncBody":           messages.SysSyncBody,
	"SysAlertSync":          messages.SysAlertSync,
}

func TestS099Keys_SummariesAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, name := range []string{"SysUpdatesSummary", "SysWorldBrokenSummary", "SysGLSASummary", "SysSyncSummary"} {
		s := messages.Text(s099Keys[name])
		if other, dup := seen[s]; dup {
			t.Errorf("%s and %s both read %q", name, other, s)
		}
		seen[s] = name
	}
}

func TestS099Keys_EveryEntryIsPresentEnglishASCII(t *testing.T) {
	for name, k := range s099Keys {
		s := messages.Text(k)
		if s == "" || strings.HasPrefix(s, "messages.Key(") {
			t.Errorf("%s has no catalog entry (Text = %q)", name, s)
			continue
		}
		for _, r := range s {
			if r > 0x7e || (r < 0x20 && r != '\n') {
				t.Errorf("%s = %q carries non-ASCII or control rune %U", name, s, r)
				break
			}
		}
	}
}

func TestS099Keys_R2_8_WorldBrokenTellsWhatToRun(t *testing.T) {
	if b := messages.Text(messages.SysWorldBrokenBody); !strings.Contains(b, "emerge -puDN @world") {
		t.Errorf("SysWorldBrokenBody = %q, want it to tell the user to run emerge -puDN @world", b)
	}
}
