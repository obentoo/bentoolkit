package snapshot

import (
	"errors"
	"testing"
)

func TestNewEngine_KnownAndUnknown(t *testing.T) {
	eng, err := newEngine(EngineConfig{Driver: "btrbk"}, nil, &MockRunner{})
	if err != nil {
		t.Fatalf("newEngine btrbk: %v", err)
	}
	if _, ok := eng.(*btrbkEngine); !ok {
		t.Errorf("newEngine btrbk returned %T, want *btrbkEngine", eng)
	}

	if _, err := newEngine(EngineConfig{Driver: "zfs"}, nil, nil); !errors.Is(err, ErrInvalidDriver) {
		t.Errorf("unknown engine: err = %v, want ErrInvalidDriver", err)
	}
}

func TestNewShipper_KnownAndUnknown(t *testing.T) {
	sh, err := newShipper(ShipConfig{Type: "ssh", Target: "u@h:/p"}, &MockRunner{}, Retention{})
	if err != nil {
		t.Fatalf("newShipper ssh: %v", err)
	}
	if _, ok := sh.(*sshShipper); !ok {
		t.Errorf("newShipper ssh returned %T", sh)
	}

	rs, err := newShipper(ShipConfig{Type: "restic", Repo: "repo", PasswordFile: "/pw"}, &MockRunner{}, Retention{Daily: 7})
	if err != nil {
		t.Fatalf("newShipper restic: %v", err)
	}
	if _, ok := rs.(*resticShipper); !ok {
		t.Errorf("newShipper restic returned %T, want *resticShipper", rs)
	}

	if _, err := newShipper(ShipConfig{Type: "rsync"}, nil, Retention{}); !errors.Is(err, ErrInvalidDriver) {
		t.Errorf("unknown ship: err = %v, want ErrInvalidDriver", err)
	}
}

func TestNewScheduler_KnownAndUnknown(t *testing.T) {
	sc, err := newScheduler(ScheduleConfig{Backend: "systemd"}, "/etc/bentoo/snapshot.toml", &MockRunner{})
	if err != nil {
		t.Fatalf("newScheduler systemd: %v", err)
	}
	if _, ok := sc.(*systemdScheduler); !ok {
		t.Errorf("newScheduler systemd returned %T", sc)
	}

	if _, err := newScheduler(ScheduleConfig{Backend: "cron"}, "", nil); !errors.Is(err, ErrInvalidDriver) {
		t.Errorf("unknown scheduler: err = %v, want ErrInvalidDriver", err)
	}
}

func TestNewNotifier_NoDriversIsNoop(t *testing.T) {
	// Notifiers are selected by which NotifyConfig sub-tables are populated, not by
	// a driver enum, so an empty config configures nothing and yields the no-op.
	n, err := newNotifier(NotifyConfig{})
	if err != nil {
		t.Fatalf("newNotifier empty: %v", err)
	}
	if _, ok := n.(noopNotifier); !ok {
		t.Errorf("newNotifier empty returned %T, want noopNotifier", n)
	}
}
