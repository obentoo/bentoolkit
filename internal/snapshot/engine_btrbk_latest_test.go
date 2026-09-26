package snapshot

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// s053LatestRow renders one btrbk 0.32.7 `--format=raw list latest` row: every
// column is key='value', and quoteshell writes a ' inside a value by closing the
// quote, emitting an escaped \' and reopening it.
func s053LatestRow(typ, subvolume string) string {
	return "type='" + typ + "' source_url='/mnt/pool/home' source_host='' source_path='/mnt/pool/home'" +
		" snapshot_path='/mnt/pool/_btrbk_snap' snapshot_name='home' snapshot_subvolume='" + subvolume +
		"' target_url='ssh://h/b' status='up-to-date'"
}

func s053BtrbkListMock(out string, listErr error) *MockRunner {
	return &MockRunner{RunFunc: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
		if name == "btrbk" && slices.Contains(args, "list") {
			return []byte(out), listErr
		}
		return nil, nil
	}}
}

func s053BtrbkCreate(t *testing.T, mock *MockRunner) Snapshot {
	t.Helper()
	e := newBtrbkEngine(EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}}, nil, mock)
	e.confPath = "/etc/bentoo/btrbk.conf"
	snap, err := e.Create(t.Context(), "/home")
	if err != nil {
		t.Fatalf("Create: %v, want nil (btrbk run succeeded)", err)
	}
	return snap
}

// TestBtrbkEngine_CreateResolvesLatest pins R1.2's command: exactly one
// `btrbk -c <conf> --format=raw list latest S` after `btrbk run S`.
func TestBtrbkEngine_CreateResolvesLatest(t *testing.T) {
	_ = captureWarn(t)
	path := "/mnt/pool/_btrbk_snap/home.20260923T0400"
	mock := s053BtrbkListMock(s053LatestRow("snapshot", path)+"\n", nil)
	snap := s053BtrbkCreate(t, mock)

	want := [][]string{
		{"-c", "/etc/bentoo/btrbk.conf", "run", "/home"},
		{"-c", "/etc/bentoo/btrbk.conf", "--format=raw", "list", "latest", "/home"},
	}
	if len(mock.Calls) != len(want) {
		t.Fatalf("got %d calls %+v, want %d", len(mock.Calls), mock.Calls, len(want))
	}
	for i, w := range want {
		if mock.Calls[i].Name != "btrbk" || !slices.Equal(mock.Calls[i].Args, w) {
			t.Errorf("call %d = %s %q, want btrbk %q", i, mock.Calls[i].Name, mock.Calls[i].Args, w)
		}
	}
	if snap.Path != path || snap.ID != "home.20260923T0400" || snap.Subvolume != "/home" {
		t.Errorf("Create = {ID:%q Path:%q Subvolume:%q}, want {home.20260923T0400 %s /home}", snap.ID, snap.Path, snap.Subvolume, path)
	}
}

// TestParseBtrbkLatestRaw pins the raw-row decoding of R1.2 through Create: the
// quoteshell escape, values carrying spaces and '=', and the hostile rows that
// must NOT become candidates or must collapse into one.
func TestParseBtrbkLatestRaw(t *testing.T) {
	cases := []struct {
		name, out, wantPath, wantID string
	}{
		{"quoted apostrophe", s053LatestRow("snapshot", `/snap/it'\''s.20260923`), "/snap/it's.20260923", "it's.20260923"},
		{"space and equals in value", s053LatestRow("snapshot", "/snap/a b=c/home.1"), "/snap/a b=c/home.1", "home.1"},
		{"same subvolume on two target rows collapses to one",
			s053LatestRow("snapshot", "/snap/home.2") + "\n" + strings.Replace(s053LatestRow("snapshot", "/snap/home.2"), "ssh://h/b", "ssh://h2/b", 1) + "\n",
			"/snap/home.2", "home.2"},
		{"row whose type lacks snapshot is not a candidate",
			s053LatestRow("backup", "/target/home.9") + "\n" + s053LatestRow("snapshot", "/snap/home.3") + "\n",
			"/snap/home.3", "home.3"},
		{"key text inside another value is not a key",
			"type='snapshot' source_url='/mnt/x snapshot_subvolume='\\''/evil'\\''' snapshot_subvolume='/snap/home.4' status=''\n",
			"/snap/home.4", "home.4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warns := captureWarn(t)
			snap := s053BtrbkCreate(t, s053BtrbkListMock(tc.out, nil))
			if snap.Path != tc.wantPath || snap.ID != tc.wantID {
				t.Errorf("got {ID:%q Path:%q}, want {ID:%q Path:%q}", snap.ID, snap.Path, tc.wantID, tc.wantPath)
			}
			if w := warns(); len(w) != 0 {
				t.Errorf("an identifiable listing must not warn, got %q", w)
			}
		})
	}
}

// TestBtrbkEngine_CreateUnidentifiedWarns pins R1.3: a failed, empty or
// ambiguous listing leaves ID and Path empty with a nil error, emits exactly one
// warning naming S and the reason, and never picks one of two candidates.
func TestBtrbkEngine_CreateUnidentifiedWarns(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		listErr error
		reason  string
	}{
		{"listing command fails", "", errors.New("btrbk: config parse boom"), "boom"},
		{"no rows", "", nil, ""},
		{"only non-snapshot rows", s053LatestRow("backup", "/target/home.9") + "\n", nil, ""},
		{"two distinct snapshot subvolumes", s053LatestRow("snapshot", "/snap/home.1") + "\n" + s053LatestRow("snapshot", "/snap/home.2") + "\n", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warns := captureWarn(t)
			snap := s053BtrbkCreate(t, s053BtrbkListMock(tc.out, tc.listErr))
			if snap.ID != "" || snap.Path != "" {
				t.Errorf("got {ID:%q Path:%q}, want both empty (nothing picked)", snap.ID, snap.Path)
			}
			w := warns()
			if len(w) != 1 {
				t.Fatalf("got %d warnings %q, want exactly 1", len(w), w)
			}
			if !strings.Contains(w[0], "/home") {
				t.Errorf("warning %q does not name the subvolume", w[0])
			}
			if tc.reason != "" && !strings.Contains(w[0], tc.reason) {
				t.Errorf("warning %q does not carry the reason %q", w[0], tc.reason)
			}
		})
	}
}
