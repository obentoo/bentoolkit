//go:build unix

package autoupdate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// hugeFailingCompile prints a start marker, ~208 KB of filler and an end
// marker, then fails. Only shell builtins: the harness PATH holds just the fake
// sudo.
const hugeFailingCompile = `printf 'COMPILE-LOG-START-054\n'
i=0
while [ $i -lt 3200 ]; do printf '%064d\n' 0; i=$((i+1)); done
printf 'COMPILE-LOG-END-054\n'
exit 1
`

// Story 054, R7.3 on the privileged compile: the retained compile log is the
// whole transcript, never the 64 KiB tail that bounds captured output in
// messages. The start marker lies ~208 KB before the end, far outside any tail.
func TestSavedCompileLogKeepsTheWholeTranscript(t *testing.T) {
	h := newCompileHarness(t, hugeFailingCompile)
	if _, err := h.applier.runCompile(t.Context(), h.cand, "media-plugins/gst-plugins-qt6", "1.29.2", &ApplyResult{}); err == nil {
		t.Fatal("a compile that exits 1 succeeded")
	}

	entries, err := os.ReadDir(h.logsDir)
	if err != nil {
		t.Fatalf("reading the logs dir: %v", err)
	}
	for _, e := range entries {
		b, rerr := os.ReadFile(filepath.Join(h.logsDir, e.Name()))
		if rerr != nil || !bytes.Contains(b, []byte("COMPILE-LOG-END-054")) {
			continue
		}
		if !bytes.Contains(b, []byte("COMPILE-LOG-START-054")) {
			t.Fatalf("compile log %s lost its start (%d bytes): it was cut to a tail (R7.3)", e.Name(), len(b))
		}
		if len(b) <= 2*tui.MaxCapturedBytes {
			t.Fatalf("compile log %s is %d bytes, want the whole ~208 KB transcript (R7.3)", e.Name(), len(b))
		}
		return
	}
	t.Fatalf("no compile log in %s holds the transcript's end (%d file(s))", h.logsDir, len(entries))
}
