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
	sh, err := newShipper(ShipConfig{Type: "ssh", Target: "u@h:/p"})
	if err != nil {
		t.Fatalf("newShipper ssh: %v", err)
	}
	if _, ok := sh.(*sshShipper); !ok {
		t.Errorf("newShipper ssh returned %T", sh)
	}

	if _, err := newShipper(ShipConfig{Type: "rsync"}); !errors.Is(err, ErrInvalidDriver) {
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

func TestNewNotifier_DefaultIsNoop(t *testing.T) {
	for _, driver := range []string{"", "none"} {
		n, err := newNotifier(NotifyConfig{Driver: driver})
		if err != nil {
			t.Fatalf("newNotifier %q: %v", driver, err)
		}
		if _, ok := n.(noopNotifier); !ok {
			t.Errorf("newNotifier %q returned %T, want noopNotifier", driver, n)
		}
	}

	if _, err := newNotifier(NotifyConfig{Driver: "desktop"}); !errors.Is(err, ErrInvalidDriver) {
		t.Errorf("unknown notifier: err = %v, want ErrInvalidDriver", err)
	}
}
