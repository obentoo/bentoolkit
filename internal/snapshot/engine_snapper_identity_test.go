package snapshot

import (
	"context"
	"strings"
	"testing"
)

// TestSnapperEngine_CreateDerivesPath pins R1.1: the snapshot snapper just made
// carries ID = the --print-number output and the SAME Path List derives for it.
// "/" and "/var/log" are the hostile shapes: a join that mishandles the root or
// a nested subvolume would diverge from parseSnapperListJSON.
func TestSnapperEngine_CreateDerivesPath(t *testing.T) {
	cases := []struct{ sv, out, wantID, wantPath string }{
		{"/home", "42\n", "42", "/home/.snapshots/42/snapshot"},
		{"/", "7\n", "7", "/.snapshots/7/snapshot"},
		{"/var/log", "  1234 \n", "1234", "/var/log/.snapshots/1234/snapshot"},
	}
	for _, tc := range cases {
		t.Run(tc.sv, func(t *testing.T) {
			warns := captureWarn(t)
			mock := &MockRunner{RunFunc: func(context.Context, string, []string, []byte) ([]byte, error) {
				return []byte(tc.out), nil
			}}
			snap, err := newSnapperEngine(EngineConfig{Driver: "snapper"}, mock).Create(t.Context(), tc.sv)
			if err != nil {
				t.Fatalf("Create(%q): %v", tc.sv, err)
			}
			if snap.ID != tc.wantID || snap.Path != tc.wantPath || snap.Subvolume != tc.sv {
				t.Errorf("Create(%q) = {ID:%q Path:%q Subvolume:%q}, want {ID:%q Path:%q Subvolume:%q}",
					tc.sv, snap.ID, snap.Path, snap.Subvolume, tc.wantID, tc.wantPath, tc.sv)
			}
			listed := parseSnapperListJSON([]byte(`{"cfg":[{"number":`+tc.wantID+`}]}`), tc.sv)
			if len(listed) != 1 || listed[0].Path != snap.Path || listed[0].ID != snap.ID {
				t.Errorf("Create and List disagree for %q: Create {%q %q}, List %+v", tc.sv, snap.ID, snap.Path, listed)
			}
			if w := warns(); len(w) != 0 {
				t.Errorf("a parseable number must not warn, got %q", w)
			}
		})
	}
}

// TestSnapperEngine_CreateUnparseableNumberWarns pins R1.4: output that is not a
// positive decimal integer yields an unidentified snapshot (never a Path built
// from garbage), a nil error, and exactly one warning naming S and quoting it.
func TestSnapperEngine_CreateUnparseableNumberWarns(t *testing.T) {
	for _, out := range []string{"", "\n", "0\n", "-3\n", "abc\n", "42 43\n", "4.2\n", "42\n43\n"} {
		t.Run(strings.TrimSpace(out), func(t *testing.T) {
			warns := captureWarn(t)
			mock := &MockRunner{RunFunc: func(context.Context, string, []string, []byte) ([]byte, error) {
				return []byte(out), nil
			}}
			snap, err := newSnapperEngine(EngineConfig{Driver: "snapper"}, mock).Create(t.Context(), "/home")
			if err != nil {
				t.Fatalf("Create: %v, want nil (snapper create succeeded)", err)
			}
			if snap.ID != "" || snap.Path != "" {
				t.Errorf("output %q: got {ID:%q Path:%q}, want both empty", out, snap.ID, snap.Path)
			}
			w := warns()
			if len(w) != 1 {
				t.Fatalf("output %q: got %d warnings %q, want exactly 1", out, len(w), w)
			}
			if !strings.Contains(w[0], "/home") {
				t.Errorf("warning %q does not name the subvolume /home", w[0])
			}
			if trimmed := strings.TrimSpace(out); trimmed != "" && !strings.Contains(w[0], strings.Split(trimmed, "\n")[0]) {
				t.Errorf("warning %q does not quote the output %q", w[0], trimmed)
			}
		})
	}
}
