package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// Story 078: --list prints the pending-updates file without loading
// packages.toml, so story 077's key check never sees it. Every field it prints
// is escaped when it holds a non-printable rune.
//
// The hostile characters are built from their code points on purpose: a
// literal invisible rune in this file would compare equal to the escape and
// hide itself (see story 077).
var (
	s078Esc = string(rune(0x1b))
	s078RLO = string(rune(0x202e))
)

func TestS078ListEscapesNonPrintableFields(t *testing.T) {
	pkg := "app-misc/x" + s078Esc + "[2J"
	errText := "download failed\n    Status:  validated" + s078RLO
	status := autoupdate.UpdateStatus("failed" + s078Esc + "[31m")
	out := captureStdout(t, func() {
		displayPendingUpdates(discardLog(), []autoupdate.PendingUpdate{{
			Package:        pkg,
			CurrentVersion: "1.0",
			NewVersion:     "2.0",
			Status:         status,
			Error:          errText,
			DetectedAt:     time.Now(),
		}})
	})

	for _, raw := range []string{s078Esc, s078RLO} {
		if strings.Contains(out, raw) {
			t.Errorf("the output carries the raw character %q:\n%s", raw, out)
		}
	}
	if strings.Contains(out, "\n    Status:  validated") {
		t.Errorf("the error text forged a Status line:\n%s", out)
	}
	for _, field := range []string{pkg, errText, string(status)} {
		if want := strconv.Quote(field); !strings.Contains(out, want) {
			t.Errorf("the output does not print %s:\n%s", want, out)
		}
	}
}

// The converse: an all-printable entry renders exactly as before, non-ASCII
// letters included.
func TestS078ListPrintableEntryUnchanged(t *testing.T) {
	out := captureStdout(t, func() {
		displayPendingUpdates(discardLog(), []autoupdate.PendingUpdate{{
			Package:        "app-misc/café",
			CurrentVersion: "1.0",
			NewVersion:     "2.0",
			Status:         autoupdate.StatusFailed,
			Error:          "something went wrong",
			DetectedAt:     time.Now(),
		}})
	})
	for _, line := range []string{
		"  app-misc/café\n",
		"    Version: 1.0 → 2.0\n",
		"    Status:  [" + string(autoupdate.StatusFailed) + "]\n",
		"    Error:   something went wrong\n",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("the output lost the line %q:\n%s", line, out)
		}
	}
	if strings.Contains(out, `"`) {
		t.Errorf("a printable entry was quoted:\n%s", out)
	}
}
