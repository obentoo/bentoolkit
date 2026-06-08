package snapshot

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSSHShipper_TargetInRenderedConf(t *testing.T) {
	sh, err := newSSHShipper(ShipConfig{Name: "offsite", Type: "ssh", Target: "user@host:/backup/btrbk"})
	if err != nil {
		t.Fatalf("newSSHShipper: %v", err)
	}

	conf := renderBtrbkConf(
		EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}},
		[]string{sh.target()},
	)
	if !strings.Contains(conf, "target ssh://user@host:/backup/btrbk") {
		t.Errorf("rendered conf missing ssh target block:\n%s", conf)
	}
}

func TestSSHShipper_SendReportsTarget(t *testing.T) {
	sh, _ := newSSHShipper(ShipConfig{Name: "offsite", Type: "ssh", Target: "user@host:/backup"})

	report, err := sh.Send(context.Background(), Snapshot{ID: "home.20260608T120000"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if report.Target != "user@host:/backup" {
		t.Errorf("report.Target = %q", report.Target)
	}
	if !report.Delegated {
		t.Errorf("report.Delegated = false, want true (btrbk performs the transfer)")
	}
	if report.Snapshot != "home.20260608T120000" {
		t.Errorf("report.Snapshot = %q", report.Snapshot)
	}
}

func TestSSHShipper_InvalidTarget(t *testing.T) {
	for _, bad := range []string{"", "nohost", "user@host", "@host:/p", "user@:/p", "user@host:"} {
		if _, err := newSSHShipper(ShipConfig{Type: "ssh", Target: bad}); !errors.Is(err, ErrInvalidShipTarget) {
			t.Errorf("target %q: err = %v, want ErrInvalidShipTarget", bad, err)
		}
	}
}

func TestCollectShipTargets(t *testing.T) {
	targets := collectShipTargets([]ShipConfig{
		{Type: "ssh", Target: "a@h:/p"},
		{Type: "ssh", Target: ""},
		{Type: "ssh", Target: "b@h:/q"},
	})
	if len(targets) != 2 || targets[0] != "a@h:/p" || targets[1] != "b@h:/q" {
		t.Errorf("collectShipTargets = %v", targets)
	}
}
