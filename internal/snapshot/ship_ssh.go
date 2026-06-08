package snapshot

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidShipTarget is returned when an ssh ship entry has a missing or
// malformed target (expected user@host:/path).
var ErrInvalidShipTarget = errors.New("invalid ssh ship target")

// sshShipper replicates snapshots to a remote btrbk target over ssh. bentoolkit
// moves no bytes itself (AD5): the target is contributed to the rendered
// btrbk.conf and btrbk performs send/receive during engine Create. Send therefore
// only records which target was served.
type sshShipper struct {
	cfg ShipConfig
}

// newSSHShipper validates the target and builds the shipper.
func newSSHShipper(cfg ShipConfig) (*sshShipper, error) {
	if !validSSHTarget(cfg.Target) {
		return nil, fmt.Errorf("%w: ship %q target %q (want user@host:/path)", ErrInvalidShipTarget, cfg.Name, cfg.Target)
	}
	return &sshShipper{cfg: cfg}, nil
}

// Name returns the ship's configured name, or "ssh" when unnamed.
func (s *sshShipper) Name() string {
	if s.cfg.Name != "" {
		return s.cfg.Name
	}
	return "ssh"
}

// Send reports the replication target for snap. The transfer itself is delegated
// to btrbk (Delegated=true), so no subprocess runs here (R3.2).
func (s *sshShipper) Send(_ context.Context, snap Snapshot) (ShipReport, error) {
	return ShipReport{
		Target:    s.cfg.Target,
		Snapshot:  snap.ID,
		Delegated: true,
		Note:      "transfer delegated to btrbk target",
	}, nil
}

// target returns the raw ssh target string for inclusion in btrbk.conf.
func (s *sshShipper) target() string { return s.cfg.Target }

// validSSHTarget reports whether target looks like user@host:/path: a non-empty
// user before '@', a host after it, and a ':' introducing the remote path.
func validSSHTarget(target string) bool {
	target = strings.TrimSpace(target)
	at := strings.IndexByte(target, '@')
	if at <= 0 {
		return false
	}
	rest := target[at+1:]
	colon := strings.IndexByte(rest, ':')
	return colon > 0 && colon < len(rest)-1
}

// collectShipTargets returns the ssh targets across all ship entries, in order,
// for contribution to the btrbk.conf. Non-ssh ship types are ignored (none exist
// in story 004).
func collectShipTargets(ships []ShipConfig) []string {
	var targets []string
	for _, sh := range ships {
		if sh.Type == "ssh" && sh.Target != "" {
			targets = append(targets, sh.Target)
		}
	}
	return targets
}
