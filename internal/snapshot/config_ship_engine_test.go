package snapshot

import (
	"errors"
	"strings"
	"testing"
)

func s053AllBinaries(t *testing.T) {
	t.Helper()
	stubLookPath(t, "btrbk", "snapper", "ssh", "restic", "rclone", "btrfs", "zstd", "mount", "systemctl")
}

func s053NamesIndex(msg string, i string) bool {
	for _, f := range []string{"[" + i + "]", "#" + i, "index " + i, "ship " + i} {
		if strings.Contains(msg, f) {
			return true
		}
	}
	return false
}

// TestValidate_SnapperWithSSHRejected pins R3.1 and both converse halves.
func TestValidate_SnapperWithSSHRejected(t *testing.T) {
	s053AllBinaries(t)
	cfg := &Config{
		Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}},
		Ship: []ShipConfig{
			{Name: "cloud", Type: "archive", Remote: "r:bkt"},
			{Name: "offsite", Type: "ssh", Target: "u@h:/b"},
		},
	}
	err := cfg.Validate()
	if !errors.Is(err, ErrShipEngineMismatch) {
		t.Fatalf("Validate = %v, want ErrShipEngineMismatch", err)
	}
	msg := err.Error()
	for _, want := range []string{"offsite", `engine.driver = "btrbk"`, "archive", "restic"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must mention %q", msg, want)
		}
	}
	if !s053NamesIndex(msg, "1") {
		t.Errorf("error %q must name the ship's index 1", msg)
	}

	t.Run("snapper with archive and restic only passes", func(t *testing.T) {
		ok := &Config{
			Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}},
			Ship: []ShipConfig{
				{Type: "archive", Remote: "r:bkt"},
				{Type: "restic", Repo: "/srv/restic", PasswordFile: "/etc/bentoo/restic.pw"},
			},
		}
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate = %v, want nil", err)
		}
	})
	t.Run("btrbk with ssh passes", func(t *testing.T) {
		ok := &Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}}, Ship: []ShipConfig{{Type: "ssh", Target: "u@h:/b"}}}
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate = %v, want nil", err)
		}
	})
}

// TestValidate_SnapperSSHBeatsMissingBinary pins R3.2.
func TestValidate_SnapperSSHBeatsMissingBinary(t *testing.T) {
	stubLookPath(t)
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}, Ship: []ShipConfig{{Type: "ssh", Target: "u@h:/b"}}}
	err := cfg.Validate()
	if !errors.Is(err, ErrShipEngineMismatch) {
		t.Errorf("Validate = %v, want ErrShipEngineMismatch ahead of binary detection", err)
	}
	if errors.Is(err, ErrDriverUnavailable) {
		t.Errorf("Validate = %v reported a missing binary instead of the mismatch", err)
	}
}
