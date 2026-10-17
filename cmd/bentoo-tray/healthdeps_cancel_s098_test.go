package main

// Story 098, sub-task 6.1 fix round: the start-up bus probes.
//
//   - A stop during start-up cancels ctx: a Present or Available question cut
//     short by it is no failure, so it logs no WARN and flags nothing.
//   - A failed Present or Available sets SystemUnitsProbeFailed,
//     UserUnitsProbeFailed or SmartProbeFailed for that monitor only, so the
//     App does not WARN the same failure again.
//   - The three openers run concurrently, and their lines still come out in
//     a fixed order: system manager, user manager, SMART.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/smartmon"
	"github.com/obentoo/bentoolkit/internal/desktop/unitmon"
)

// TestS098ResolveHealth_CancelledStartLogsNoWarn: every bus question fails
// because the parent ctx ended; nothing is a WARN and no probe is flagged.
func TestS098ResolveHealth_CancelledStartLogsNoWarn(t *testing.T) {
	f := newS098Host()
	f.sys.presentErr = fmt.Errorf("asking whether org.freedesktop.systemd1 has an owner: %w", context.Canceled)
	f.user.presentErr = fmt.Errorf("asking whether org.freedesktop.systemd1 has an owner: %w", context.Canceled)
	f.smart.availErr = fmt.Errorf("listing activatable names on the system bus: %w", context.Canceled)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	log, buf := s097Logger()
	r := resolveHealth(ctx, log, config.TrayConfig{}, f.health())
	if warns := s097Lines(buf, "level=WARN"); len(warns) != 0 {
		t.Errorf("WARN lines after a stop during start-up = %q, want none\n%s", warns, buf.String())
	}
	if r.SystemUnitsProbeFailed || r.UserUnitsProbeFailed || r.SmartProbeFailed {
		t.Errorf("probe flags system=%v user=%v smart=%v after a stop, want all false",
			r.SystemUnitsProbeFailed, r.UserUnitsProbeFailed, r.SmartProbeFailed)
	}
}

// TestS098ResolveHealth_ProbeFailureFlagsOnlyItsMonitor: hostile half first,
// each failed question flags its own monitor and no other; then an absent
// manager or udisks2, which is no failure, flags nothing.
func TestS098ResolveHealth_ProbeFailureFlagsOnlyItsMonitor(t *testing.T) {
	cause := fmt.Errorf("asking the bus: %w", context.DeadlineExceeded)
	cases := []struct {
		name              string
		edit              func(*s098Host)
		system, user, smt bool
	}{
		{"system Present fails", func(f *s098Host) { f.sys.presentErr = cause }, true, false, false},
		{"user Present fails", func(f *s098Host) { f.user.presentErr = cause }, false, true, false},
		{"Available fails", func(f *s098Host) { f.smart.availErr = cause }, false, false, true},
		{"all absent", func(f *s098Host) { f.sys.present, f.user.present, f.smart.available = false, false, false }, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newS098Host()
			tc.edit(f)
			log, _ := s097Logger()
			r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
			if r.SystemUnitsProbeFailed != tc.system || r.UserUnitsProbeFailed != tc.user || r.SmartProbeFailed != tc.smt {
				t.Errorf("probe flags system=%v user=%v smart=%v, want %v %v %v",
					r.SystemUnitsProbeFailed, r.UserUnitsProbeFailed, r.SmartProbeFailed, tc.system, tc.user, tc.smt)
			}
		})
	}
}

// s098Gate holds every bus question until all three have been asked, then
// lets them answer in the order smart, user, system: the reverse of the
// order their lines must be logged in.
type s098Gate struct {
	arrived   atomic.Int32
	all       chan struct{}
	smartDone chan struct{}
	userDone  chan struct{}
	mu        sync.Mutex
	stalls    []string
}

func newS098Gate() *s098Gate {
	return &s098Gate{all: make(chan struct{}), smartDone: make(chan struct{}), userDone: make(chan struct{})}
}

// ask blocks name's question until all three are asked and its turn comes.
func (g *s098Gate) ask(name string, after <-chan struct{}, done chan struct{}) {
	if g.arrived.Add(1) == 3 {
		close(g.all)
	}
	if !s097Wait(g.all) {
		g.stall(name + " was asked while another question was still unasked: the probes ran one after the other")
	}
	if after != nil && !s097Wait(after) {
		g.stall(name + " waited for its turn past the guard")
	}
	if done != nil {
		close(done)
	}
}

func (g *s098Gate) stall(s string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stalls = append(g.stalls, s)
}

// s098GatedUnits is a systemd manager whose Present waits at the gate and
// then fails.
type s098GatedUnits struct {
	gate        *s098Gate
	name        string
	after, done chan struct{}
}

func (u *s098GatedUnits) Present(context.Context) (bool, error) {
	u.gate.ask(u.name, u.after, u.done)
	return false, fmt.Errorf("asking the %s manager: %w", u.name, context.DeadlineExceeded)
}
func (u *s098GatedUnits) Failed(context.Context) ([]unitmon.Unit, error) { return nil, nil }
func (u *s098GatedUnits) Changed() <-chan struct{}                       { return nil }

// s098GatedSmart is udisks2 whose Available waits at the gate and then fails.
type s098GatedSmart struct{ gate *s098Gate }

func (s *s098GatedSmart) Available(context.Context) (bool, error) {
	s.gate.ask("smart", nil, s.gate.smartDone)
	return false, fmt.Errorf("asking udisks2: %w", context.DeadlineExceeded)
}
func (s *s098GatedSmart) Drives(context.Context) ([]smartmon.Drive, error) { return nil, nil }
func (s *s098GatedSmart) Changed() <-chan struct{}                         { return nil }

// TestS098ResolveHealth_ProbesRunConcurrentlyAndLogInOrder: the three
// questions are all asked before any answers, and although they answer
// smart first and system last, the lines read system, user, smart.
func TestS098ResolveHealth_ProbesRunConcurrentlyAndLogInOrder(t *testing.T) {
	g := newS098Gate()
	f := newS098Host()
	h := f.health()
	h.SystemUnits = func() (unitsProbe, error) {
		return &s098GatedUnits{gate: g, name: "system", after: g.userDone}, nil
	}
	h.UserUnits = func() (unitsProbe, error) {
		return &s098GatedUnits{gate: g, name: "user", after: g.smartDone, done: g.userDone}, nil
	}
	h.Smart = func() (smartProbe, error) { return &s098GatedSmart{gate: g}, nil }
	log, buf := s097Logger()
	r := resolveHealth(context.Background(), log, config.TrayConfig{}, h)
	g.mu.Lock()
	stalls := g.stalls
	g.mu.Unlock()
	if len(stalls) != 0 {
		t.Fatalf("the bus probes did not run concurrently: %q", stalls)
	}
	var order []string
	for _, l := range s097Lines(buf, "level=WARN") {
		switch {
		case strings.Contains(l, "monitor=smart"):
			order = append(order, "smart")
		case s098Names(l, "system"):
			order = append(order, "system")
		case s098Names(l, "user"):
			order = append(order, "user")
		}
	}
	if got, want := strings.Join(order, ","), "system,user,smart"; got != want {
		t.Errorf("WARN line order = %s, want %s\n%s", got, want, buf.String())
	}
	if !r.SystemUnitsProbeFailed || !r.UserUnitsProbeFailed || !r.SmartProbeFailed {
		t.Errorf("probe flags system=%v user=%v smart=%v, want all true",
			r.SystemUnitsProbeFailed, r.UserUnitsProbeFailed, r.SmartProbeFailed)
	}
	if r.SystemUnits == nil || r.UserUnits == nil || r.Smart == nil {
		t.Errorf("a monitor whose question failed was dropped: system=%v user=%v smart=%v", r.SystemUnits, r.UserUnits, r.Smart)
	}
}
