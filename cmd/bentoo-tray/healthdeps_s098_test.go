package main

// Story 098, sub-task 6.1: bentoo-tray resolves the emerge follower, the
// mount reader and the systemd/udisks2 monitors at startup, through the same
// injected hostHealth as story 097. Every test passes fakes, so none opens
// /var/log/emerge.log, reads /proc/self/mountinfo, runs portageq or touches a
// bus.
//
// Contract (design.md, cmd/bentoo-tray; signatures pinned by this test):
//
//	type emergeFollower interface { // *sysmon.EmergeFollower
//		Start() error
//		Read() ([]sysmon.EmergeEvent, error)
//	}
//	type unitsProbe interface { // *unitmon.Monitor
//		Present(ctx context.Context) (bool, error)
//		Failed(ctx context.Context) ([]unitmon.Unit, error)
//		Changed() <-chan struct{}
//	}
//	type smartProbe interface { // *smartmon.Monitor
//		Available(ctx context.Context) (bool, error)
//		Drives(ctx context.Context) ([]smartmon.Drive, error)
//		Changed() <-chan struct{}
//	}
//
//	hostHealth gains:
//		Emerge      func(path string) emergeFollower // builds the follower for tray.monitors.emerge.log
//		Mounts      tray.MountReader
//		SystemUnits func() (unitsProbe, error) // the system manager's monitor; an error means the system bus is unreachable
//		UserUnits   func() (unitsProbe, error) // the user manager's monitor on the session bus
//		Smart       func() (smartProbe, error) // the udisks2 monitor; an error means the system bus is unreachable
//	A nil hostHealth field is a capability the host lacks: nothing is opened,
//	asked or logged for it, and its Deps field stays nil. Calling a func field
//	is opening that monitor, so a disabled monitor's func is never called.
//
//	resolveHealth(ctx, log, cfg config.TrayConfig, h hostHealth) returns a
//	struct whose fields Sys, Monitors and DiskPaths are healthDeps' three
//	results and whose fields PortageTmp, Emerge, Mounts, SystemUnits,
//	UserUnits and Smart are the tray.Deps fields of the same names. The
//	monitor fields are interfaces; an absent or disabled monitor is a nil
//	interface, never a typed nil. healthDeps keeps its signature and returns
//	resolveHealth's Sys, Monitors and DiskPaths.
//
// Every new startup line carries monitor=<emerge|units|smart> and exactly one
// level key (Q13); a units line names its manager as a key=value pair whose
// value is system or user.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/smartmon"
	"github.com/obentoo/bentoolkit/internal/desktop/unitmon"
	"github.com/obentoo/bentoolkit/internal/sysmon"
)

// s098EmergeLog is a path no host has, so following it for real would fail.
const s098EmergeLog = "/s098-no-such-root/var/log/emerge.log"

// s098DefaultEmergeLog is the documented default (R1.1).
const s098DefaultEmergeLog = "/var/log/emerge.log"

// s098Follower fakes the emerge log follower.
type s098Follower struct {
	startErr error
	starts   atomic.Int32
	reads    atomic.Int32
}

func (f *s098Follower) Start() error { f.starts.Add(1); return f.startErr }
func (f *s098Follower) Read() ([]sysmon.EmergeEvent, error) {
	f.reads.Add(1)
	return nil, nil
}

// s098Units fakes one systemd manager's monitor.
type s098Units struct {
	present      bool
	presentErr   error
	presentAsked atomic.Int32
	failedAsked  atomic.Int32
	changed      chan struct{}
}

func (u *s098Units) Present(context.Context) (bool, error) {
	u.presentAsked.Add(1)
	return u.present, u.presentErr
}
func (u *s098Units) Failed(context.Context) ([]unitmon.Unit, error) {
	u.failedAsked.Add(1)
	return nil, nil
}
func (u *s098Units) Changed() <-chan struct{} { return u.changed }

// s098Smart fakes the udisks2 monitor.
type s098Smart struct {
	available   bool
	availErr    error
	availAsked  atomic.Int32
	drivesAsked atomic.Int32
	changed     chan struct{}
}

func (s *s098Smart) Available(context.Context) (bool, error) {
	s.availAsked.Add(1)
	return s.available, s.availErr
}
func (s *s098Smart) Drives(context.Context) ([]smartmon.Drive, error) {
	s.drivesAsked.Add(1)
	return nil, nil
}
func (s *s098Smart) Changed() <-chan struct{} { return s.changed }

// s098Mounts fakes the mount table reader.
type s098Mounts struct{ asked atomic.Int32 }

func (m *s098Mounts) Mounts() ([]sysmon.Mount, error) {
	m.asked.Add(1)
	return nil, nil
}

// s098Host is story 097's fake host plus the four story 098 capabilities,
// all present and healthy unless a test changes them.
type s098Host struct {
	*s097Host
	follower    *s098Follower
	emergeMu    sync.Mutex
	emergePaths []string
	mounts      *s098Mounts
	sys, user   *s098Units
	sysOpened   atomic.Int32
	userOpened  atomic.Int32
	smart       *s098Smart
	smartOpened atomic.Int32
	sysBusErr   error // non-nil: the system bus is unreachable
}

func newS098Host() *s098Host {
	return &s098Host{
		s097Host: newS097Host(),
		follower: &s098Follower{},
		mounts:   &s098Mounts{},
		sys:      &s098Units{present: true, changed: make(chan struct{})},
		user:     &s098Units{present: true, changed: make(chan struct{})},
		smart:    &s098Smart{available: true, changed: make(chan struct{})},
	}
}

func (f *s098Host) health() hostHealth {
	h := f.s097Host.health()
	h.Emerge = func(path string) emergeFollower {
		f.emergeMu.Lock()
		f.emergePaths = append(f.emergePaths, path)
		f.emergeMu.Unlock()
		return f.follower
	}
	h.Mounts = f.mounts
	h.SystemUnits = func() (unitsProbe, error) {
		f.sysOpened.Add(1)
		if f.sysBusErr != nil {
			return nil, f.sysBusErr
		}
		return f.sys, nil
	}
	h.UserUnits = func() (unitsProbe, error) {
		f.userOpened.Add(1)
		return f.user, nil
	}
	h.Smart = func() (smartProbe, error) {
		f.smartOpened.Add(1)
		if f.sysBusErr != nil {
			return nil, f.sysBusErr
		}
		return f.smart, nil
	}
	return h
}

func (f *s098Host) built() []string {
	f.emergeMu.Lock()
	defer f.emergeMu.Unlock()
	return slices.Clone(f.emergePaths)
}

// s098Names reports whether line carries a key=value pair whose value is
// exactly v, quoted or not.
func s098Names(line, v string) bool {
	re := regexp.MustCompile(`(?:^|\s)[A-Za-z_][A-Za-z0-9_.]*="?` + regexp.QuoteMeta(v) + `"?(?:\s|$)`)
	return re.MatchString(line)
}

// s098OneLevel fails unless every line carries exactly one level key.
func s098OneLevel(t *testing.T, lines []string) {
	t.Helper()
	for _, l := range lines {
		if n := strings.Count(l, "level="); n != 1 {
			t.Errorf("line carries %d level keys, want 1: %s", n, l)
		}
	}
}

// s098FailedVia calls Failed through a resolved units field.
func s098FailedVia(t *testing.T, name string, dep any) {
	t.Helper()
	u, ok := dep.(interface {
		Failed(context.Context) ([]unitmon.Unit, error)
	})
	if !ok {
		t.Fatalf("%s = %#v, want a monitor with Failed(ctx) ([]unitmon.Unit, error)", name, dep)
	}
	if _, err := u.Failed(context.Background()); err != nil {
		t.Fatalf("%s.Failed: %v", name, err)
	}
}

// s098ReadVia calls Read through the resolved Emerge field.
func s098ReadVia(t *testing.T, dep any) {
	t.Helper()
	r, ok := dep.(interface {
		Read() ([]sysmon.EmergeEvent, error)
	})
	if !ok {
		t.Fatalf("Emerge = %#v, want a reader with Read() ([]sysmon.EmergeEvent, error)", dep)
	}
	if _, err := r.Read(); err != nil {
		t.Fatalf("Emerge.Read: %v", err)
	}
}

// --- systemd units: R2.1, R2.2, R2.8, R6.2, R6.4 ---

// TestS098ResolveHealth_R2_1_R2_2_EachManagerGetsItsOwnMonitor: hostile half
// first. The system and user monitors are two values of the same type; a
// crossed wiring hands the system manager's monitor to UserUnits and still
// compiles. Calling Failed through each field must reach that manager's
// monitor only.
func TestS098ResolveHealth_R2_1_R2_2_EachManagerGetsItsOwnMonitor(t *testing.T) {
	f := newS098Host()
	log, buf := s097Logger()
	r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())

	s098FailedVia(t, "SystemUnits", r.SystemUnits)
	if s, u := f.sys.failedAsked.Load(), f.user.failedAsked.Load(); s != 1 || u != 0 {
		t.Errorf("Failed through SystemUnits reached the system monitor %d and the user monitor %d times, want 1 and 0", s, u)
	}
	s098FailedVia(t, "UserUnits", r.UserUnits)
	if s, u := f.sys.failedAsked.Load(), f.user.failedAsked.Load(); s != 1 || u != 1 {
		t.Errorf("Failed through UserUnits: system monitor asked %d and user monitor %d times in total, want 1 and 1", s, u)
	}
	if s, u := f.sysOpened.Load(), f.userOpened.Load(); s != 1 || u != 1 {
		t.Errorf("opened the system monitor %d and the user monitor %d times, want once each", s, u)
	}
	if s, u := f.sys.presentAsked.Load(), f.user.presentAsked.Load(); s != 1 || u != 1 {
		t.Errorf("asked Present of the system manager %d and the user manager %d times, want once each", s, u)
	}
	if l := s097Lines(buf, "monitor=units"); len(l) != 0 {
		t.Errorf("units lines %q, want none with both managers present", l)
	}
}

// TestS098ResolveHealth_R6_4_OneManagerOffKeepsTheOther: both converses.
// Turning one manager off must not open it, and must not take the other one
// with it.
func TestS098ResolveHealth_R6_4_OneManagerOffKeepsTheOther(t *testing.T) {
	t.Run("units.system false", func(t *testing.T) {
		f := newS098Host()
		log, buf := s097Logger()
		cfg := s097TrayCfg(t, "monitors:\n  units:\n    system: false\n")
		r := resolveHealth(context.Background(), log, cfg, f.health())
		if n := f.sysOpened.Load(); n != 0 {
			t.Errorf("opened the system monitor %d times with units.system false, want 0", n)
		}
		if n := f.sys.presentAsked.Load(); n != 0 {
			t.Errorf("asked the system manager Present %d times with units.system false, want 0", n)
		}
		if r.SystemUnits != nil {
			t.Errorf("SystemUnits = %#v, want nil with units.system false", r.SystemUnits)
		}
		s098FailedVia(t, "UserUnits", r.UserUnits)
		if n := f.user.failedAsked.Load(); n != 1 {
			t.Errorf("UserUnits does not reach the user monitor (asked %d times)", n)
		}
		if l := s097Lines(buf, "level=WARN", "monitor=units"); len(l) != 0 {
			t.Errorf("units WARN lines %q, want none: a manager switched off is not degraded", l)
		}
	})
	t.Run("units.user false", func(t *testing.T) {
		f := newS098Host()
		log, buf := s097Logger()
		cfg := s097TrayCfg(t, "monitors:\n  units:\n    user: false\n")
		r := resolveHealth(context.Background(), log, cfg, f.health())
		if n := f.userOpened.Load(); n != 0 {
			t.Errorf("opened the user monitor %d times with units.user false, want 0", n)
		}
		if n := f.user.presentAsked.Load(); n != 0 {
			t.Errorf("asked the user manager Present %d times with units.user false, want 0", n)
		}
		if r.UserUnits != nil {
			t.Errorf("UserUnits = %#v, want nil with units.user false", r.UserUnits)
		}
		s098FailedVia(t, "SystemUnits", r.SystemUnits)
		if n := f.sys.failedAsked.Load(); n != 1 {
			t.Errorf("SystemUnits does not reach the system monitor (asked %d times)", n)
		}
		if l := s097Lines(buf, "level=WARN", "monitor=units"); len(l) != 0 {
			t.Errorf("units WARN lines %q, want none: a manager switched off is not degraded", l)
		}
	})
}

// TestS098ResolveHealth_R6_2_UnitsDisabledOpensNothing: units.enabled false
// opens neither manager's monitor, asks nothing and logs nothing for it, even
// with units.system and units.user left true.
func TestS098ResolveHealth_R6_2_UnitsDisabledOpensNothing(t *testing.T) {
	f := newS098Host()
	log, buf := s097Logger()
	cfg := s097TrayCfg(t, "monitors:\n  units:\n    enabled: false\n    system: true\n    user: true\n")
	r := resolveHealth(context.Background(), log, cfg, f.health())
	if s, u := f.sysOpened.Load(), f.userOpened.Load(); s != 0 || u != 0 {
		t.Errorf("opened the system monitor %d and the user monitor %d times with units disabled, want 0", s, u)
	}
	if s, u := f.sys.presentAsked.Load(), f.user.presentAsked.Load(); s != 0 || u != 0 {
		t.Errorf("asked Present %d (system) and %d (user) times with units disabled, want 0", s, u)
	}
	if r.SystemUnits != nil || r.UserUnits != nil {
		t.Errorf("SystemUnits = %#v, UserUnits = %#v; want both nil with units disabled", r.SystemUnits, r.UserUnits)
	}
	if l := s097Lines(buf, "monitor=units"); len(l) != 0 {
		t.Errorf("units lines %q, want none with units disabled", l)
	}
}

// TestS098ResolveHealth_R2_8_AbsentManagerIsOneInfoNamingIt: hostile half
// first. With one manager absent the INFO must name that manager and not the
// other, in both directions, and the other manager stays watched.
func TestS098ResolveHealth_R2_8_AbsentManagerIsOneInfoNamingIt(t *testing.T) {
	for _, tc := range []struct {
		absent, kept string
	}{
		{absent: "user", kept: "system"},
		{absent: "system", kept: "user"},
	} {
		t.Run(tc.absent+" manager absent", func(t *testing.T) {
			f := newS098Host()
			gone, stays := f.user, f.sys
			if tc.absent == "system" {
				gone, stays = f.sys, f.user
			}
			gone.present = false
			log, buf := s097Logger()
			r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())

			absentDep, keptDep := any(r.UserUnits), any(r.SystemUnits)
			if tc.absent == "system" {
				absentDep, keptDep = r.SystemUnits, r.UserUnits
			}
			if absentDep != nil {
				t.Errorf("the absent %s manager's monitor = %#v, want nil", tc.absent, absentDep)
			}
			s098FailedVia(t, "the "+tc.kept+" monitor", keptDep)
			if n := stays.failedAsked.Load(); n != 1 {
				t.Errorf("the %s monitor was not kept: Failed reached it %d times, want 1", tc.kept, n)
			}

			lines := s097Lines(buf, "monitor=units")
			if len(lines) != 1 {
				t.Fatalf("units lines = %q, want exactly one INFO\n%s", lines, buf.String())
			}
			if !strings.Contains(lines[0], "level=INFO") {
				t.Errorf("units line %q, want level=INFO: an absent manager is not degraded", lines[0])
			}
			if !s098Names(lines[0], tc.absent) {
				t.Errorf("units line %q does not name the %s manager as a key=value pair", lines[0], tc.absent)
			}
			if s098Names(lines[0], tc.kept) {
				t.Errorf("units line %q names the %s manager, which is present", lines[0], tc.kept)
			}
			s098OneLevel(t, lines)
		})
	}
}

// TestS098ResolveHealth_R2_1_UnreachableSystemBusLeavesTheUserManager: no
// system bus is one line naming the system manager, no system monitor, and
// the user manager on the session bus is still watched.
func TestS098ResolveHealth_R2_1_UnreachableSystemBusLeavesTheUserManager(t *testing.T) {
	f := newS098Host()
	f.sysBusErr = errors.New("dial unix /s098-no-such-root/system_bus_socket: connect: no such file or directory")
	log, buf := s097Logger()
	r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
	if r.SystemUnits != nil {
		t.Errorf("SystemUnits = %#v, want nil without a system bus", r.SystemUnits)
	}
	s098FailedVia(t, "UserUnits", r.UserUnits)
	if n := f.user.failedAsked.Load(); n != 1 {
		t.Errorf("UserUnits does not reach the user monitor (asked %d times)", n)
	}
	lines := s097Lines(buf, "monitor=units")
	if len(lines) != 1 || !s098Names(lines[0], "system") || s098Names(lines[0], "user") {
		t.Errorf("units lines = %q, want exactly one naming the system manager only\n%s", lines, buf.String())
	}
	s098OneLevel(t, lines)
}

// TestS098ResolveHealth_R2_8_PresentErrorIsOneWarn: a failed Present call is
// one WARN carrying the cause and naming the manager; the other manager is
// unaffected.
func TestS098ResolveHealth_R2_8_PresentErrorIsOneWarn(t *testing.T) {
	f := newS098Host()
	f.sys.presentErr = fmt.Errorf("asking whether org.freedesktop.systemd1 has an owner: %w", context.DeadlineExceeded)
	log, buf := s097Logger()
	r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
	warns := s097Lines(buf, "level=WARN", "monitor=units")
	if len(warns) != 1 {
		t.Fatalf("units WARN lines = %q, want 1\n%s", warns, buf.String())
	}
	if !strings.Contains(warns[0], "deadline exceeded") {
		t.Errorf("units WARN %q does not carry the cause", warns[0])
	}
	if !s098Names(warns[0], "system") || s098Names(warns[0], "user") {
		t.Errorf("units WARN %q, want it to name the system manager only", warns[0])
	}
	s098OneLevel(t, warns)
	s098FailedVia(t, "UserUnits", r.UserUnits)
	if n := f.user.failedAsked.Load(); n != 1 {
		t.Errorf("UserUnits does not reach the user monitor (asked %d times)", n)
	}
}

// --- SMART: R4.8, R6.2 ---

// TestS098ResolveHealth_R4_8_SmartAvailableIsWired: udisks2 available hands
// the monitor to Deps, opened and asked once, with no "unavailable" line.
func TestS098ResolveHealth_R4_8_SmartAvailableIsWired(t *testing.T) {
	f := newS098Host()
	log, buf := s097Logger()
	r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
	d, ok := any(r.Smart).(interface {
		Drives(context.Context) ([]smartmon.Drive, error)
	})
	if !ok {
		t.Fatalf("Smart = %#v, want a monitor with Drives(ctx) ([]smartmon.Drive, error)", r.Smart)
	}
	if _, err := d.Drives(context.Background()); err != nil {
		t.Fatalf("Smart.Drives: %v", err)
	}
	if n := f.smart.drivesAsked.Load(); n != 1 {
		t.Errorf("Smart does not reach the udisks2 monitor (Drives asked %d times)", n)
	}
	if o, a := f.smartOpened.Load(), f.smart.availAsked.Load(); o != 1 || a != 1 {
		t.Errorf("opened the SMART monitor %d and asked Available %d times, want once each", o, a)
	}
	if l := s097Lines(buf, "disk health monitoring is unavailable"); len(l) != 0 {
		t.Errorf("lines %q, want none with udisks2 available", l)
	}
}

// TestS098ResolveHealth_R4_8_SmartUnavailableIsOneInfo: udisks2 absent, or no
// system bus, is one INFO saying disk health monitoring is unavailable and no
// SMART monitor.
func TestS098ResolveHealth_R4_8_SmartUnavailableIsOneInfo(t *testing.T) {
	for name, edit := range map[string]func(*s098Host){
		"udisks2 absent": func(f *s098Host) { f.smart.available = false },
		"no system bus": func(f *s098Host) {
			f.sysBusErr = errors.New("dial unix /s098-no-such-root/system_bus_socket: connect: no such file or directory")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newS098Host()
			edit(f)
			log, buf := s097Logger()
			r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
			if r.Smart != nil {
				t.Errorf("Smart = %#v, want nil when %s", r.Smart, name)
			}
			lines := s097Lines(buf, "disk health monitoring is unavailable")
			if len(lines) != 1 {
				t.Fatalf("lines saying disk health monitoring is unavailable = %q, want 1\n%s", lines, buf.String())
			}
			if !strings.Contains(lines[0], "level=INFO") || !strings.Contains(lines[0], "monitor=smart") {
				t.Errorf("line %q, want level=INFO and monitor=smart", lines[0])
			}
			if l := s097Lines(buf, "level=WARN", "monitor=smart"); len(l) != 0 {
				t.Errorf("SMART WARN lines %q, want none: an absent udisks2 is not degraded", l)
			}
			s098OneLevel(t, lines)
		})
	}
}

// TestS098ResolveHealth_R6_2_SmartDisabledOpensNothing: hostile half. A user
// who switched SMART off has not lost disk health monitoring to the host, so
// nothing is opened and no "unavailable" line is logged.
func TestS098ResolveHealth_R6_2_SmartDisabledOpensNothing(t *testing.T) {
	f := newS098Host()
	f.smart.available = false
	log, buf := s097Logger()
	cfg := s097TrayCfg(t, "monitors:\n  smart:\n    enabled: false\n")
	r := resolveHealth(context.Background(), log, cfg, f.health())
	if o, a := f.smartOpened.Load(), f.smart.availAsked.Load(); o != 0 || a != 0 {
		t.Errorf("opened the SMART monitor %d and asked Available %d times with smart disabled, want 0", o, a)
	}
	if r.Smart != nil {
		t.Errorf("Smart = %#v, want nil with smart disabled", r.Smart)
	}
	if l := s097Lines(buf, "disk health"); len(l) != 0 {
		t.Errorf("lines %q, want none with smart disabled", l)
	}
	if l := s097Lines(buf, "monitor=smart"); len(l) != 0 {
		t.Errorf("SMART lines %q, want none with smart disabled", l)
	}
}

// TestS098ResolveHealth_R4_8_AvailableErrorIsOneWarn: a failed Available
// call is one WARN carrying the cause.
func TestS098ResolveHealth_R4_8_AvailableErrorIsOneWarn(t *testing.T) {
	f := newS098Host()
	f.smart.availErr = fmt.Errorf("listing activatable names on the system bus: %w", context.DeadlineExceeded)
	log, buf := s097Logger()
	resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
	warns := s097Lines(buf, "level=WARN", "monitor=smart")
	if len(warns) != 1 || !strings.Contains(warns[0], "deadline exceeded") {
		t.Errorf("SMART WARN lines = %q, want one carrying the cause\n%s", warns, buf.String())
	}
	s098OneLevel(t, warns)
}

// --- emerge: R1.1, R1.11, R6.2, R6.3 ---

// TestS098ResolveHealth_R6_3_EmergeFollowsTheConfiguredLog: the follower is
// built once for the configured path (or the default without one), started
// once, and that same started follower is the one Deps reads.
func TestS098ResolveHealth_R6_3_EmergeFollowsTheConfiguredLog(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"configured log": {yaml: "monitors:\n  emerge:\n    log: " + s098EmergeLog + "\n", want: s098EmergeLog},
		"default log":    {yaml: "", want: s098DefaultEmergeLog},
	} {
		t.Run(name, func(t *testing.T) {
			f := newS098Host()
			log, buf := s097Logger()
			r := resolveHealth(context.Background(), log, s097TrayCfg(t, tc.yaml), f.health())
			if got := f.built(); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("built the emerge follower for %q, want once for %q", got, tc.want)
			}
			if n := f.follower.starts.Load(); n != 1 {
				t.Errorf("started the emerge follower %d times, want once (R1.1: seek to the end at startup)", n)
			}
			s098ReadVia(t, r.Emerge)
			if n := f.follower.reads.Load(); n < 1 {
				t.Error("Emerge does not reach the started follower")
			}
			if l := s097Lines(buf, "monitor=emerge"); len(l) != 0 {
				t.Errorf("emerge lines %q, want none when the log opens", l)
			}
		})
	}
}

// TestS098ResolveHealth_R1_11_StartFailureIsOneWarnAndKeepsTheFollower: a
// log the user cannot open is one WARN naming the path and the cause; the
// follower stays in Deps so each sample retries it.
func TestS098ResolveHealth_R1_11_StartFailureIsOneWarnAndKeepsTheFollower(t *testing.T) {
	f := newS098Host()
	f.follower.startErr = &fs.PathError{Op: "open", Path: s098EmergeLog, Err: fs.ErrPermission}
	log, buf := s097Logger()
	cfg := s097TrayCfg(t, "monitors:\n  emerge:\n    log: "+s098EmergeLog+"\n")
	r := resolveHealth(context.Background(), log, cfg, f.health())
	warns := s097Lines(buf, "level=WARN", "monitor=emerge")
	if len(warns) != 1 {
		t.Fatalf("emerge WARN lines = %q, want 1\n%s", warns, buf.String())
	}
	if !strings.Contains(warns[0], s098EmergeLog) || !strings.Contains(warns[0], "permission denied") {
		t.Errorf("emerge WARN %q, want the path %s and the cause", warns[0], s098EmergeLog)
	}
	s098OneLevel(t, warns)
	if n := f.follower.starts.Load(); n != 1 {
		t.Errorf("started the emerge follower %d times at startup, want once; later samples retry", n)
	}
	s098ReadVia(t, r.Emerge)
	if n := f.follower.reads.Load(); n < 1 {
		t.Error("the follower that failed to start was dropped; R1.11 retries it at every sample")
	}
}

// TestS098ResolveHealth_R6_2_EmergeDisabledOpensNothing: emerge disabled
// builds and starts no follower and logs nothing for it.
func TestS098ResolveHealth_R6_2_EmergeDisabledOpensNothing(t *testing.T) {
	f := newS098Host()
	log, buf := s097Logger()
	cfg := s097TrayCfg(t, "monitors:\n  emerge:\n    enabled: false\n    log: "+s098EmergeLog+"\n")
	r := resolveHealth(context.Background(), log, cfg, f.health())
	if got := f.built(); len(got) != 0 {
		t.Errorf("built the emerge follower for %q with emerge disabled, want never", got)
	}
	if n := f.follower.starts.Load(); n != 0 {
		t.Errorf("started the emerge follower %d times with emerge disabled", n)
	}
	if r.Emerge != nil {
		t.Errorf("Emerge = %#v, want nil with emerge disabled", r.Emerge)
	}
	if l := s097Lines(buf, "monitor=emerge"); len(l) != 0 {
		t.Errorf("emerge lines %q, want none with emerge disabled", l)
	}
}

// TestS098ResolveHealth_R1_5_PortageTmpWhenOnlyEmergeNeedsIt: hostile half
// first. With the disk monitor needing no portageq answer (paths configured,
// or disk off) the emerge monitor still needs PORTAGE_TMPDIR for build.log
// paths: it is asked once, DISTDIR is not asked, and the answer never
// becomes a watched disk path.
func TestS098ResolveHealth_R1_5_PortageTmpWhenOnlyEmergeNeedsIt(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml  string
		paths []string
	}{
		"disk paths configured": {
			yaml:  "monitors:\n  disk:\n    paths: [/s098-no-such-root/srv]\n",
			paths: []string{"/s098-no-such-root/srv"},
		},
		"disk monitor off": {
			yaml: "monitors:\n  disk:\n    enabled: false\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newS098Host()
			log, _ := s097Logger()
			r := resolveHealth(context.Background(), log, s097TrayCfg(t, tc.yaml), f.health())
			if n, d := f.tmpAsked.Load(), f.distAsked.Load(); n != 1 || d != 0 {
				t.Errorf("portageq asked PORTAGE_TMPDIR %d and DISTDIR %d times, want once and never", n, d)
			}
			if r.PortageTmp != s097TmpAnswer {
				t.Errorf("PortageTmp = %q, want %q", r.PortageTmp, s097TmpAnswer)
			}
			if !slices.Equal(r.DiskPaths, tc.paths) {
				t.Errorf("DiskPaths = %q, want exactly %q: PORTAGE_TMPDIR asked for emerge is not a watched disk path", r.DiskPaths, tc.paths)
			}
		})
	}
	t.Run("emerge disabled asks nothing", func(t *testing.T) {
		f := newS098Host()
		log, _ := s097Logger()
		cfg := s097TrayCfg(t, "monitors:\n  disk:\n    paths: [/s098-no-such-root/srv]\n  emerge:\n    enabled: false\n")
		r := resolveHealth(context.Background(), log, cfg, f.health())
		if n, d := f.tmpAsked.Load(), f.distAsked.Load(); n != 0 || d != 0 {
			t.Errorf("portageq asked PORTAGE_TMPDIR %d and DISTDIR %d times with emerge disabled and paths configured, want neither", n, d)
		}
		if r.PortageTmp != "" {
			t.Errorf("PortageTmp = %q, want empty: nothing asked", r.PortageTmp)
		}
	})
}

// TestS098ResolveHealth_R1_5_PortageTmpAskedOnceAndConcurrently: when both
// the disk defaults and the emerge monitor need PORTAGE_TMPDIR it is still
// asked once, concurrently with DISTDIR (097 §2.10), whichever answers
// first, and the one answer serves both.
func TestS098ResolveHealth_R1_5_PortageTmpAskedOnceAndConcurrently(t *testing.T) {
	for _, first := range []string{"DISTDIR", "PORTAGE_TMPDIR"} {
		t.Run(first+" answers first", func(t *testing.T) {
			f := newS098Host()
			b := newS097Barrier()
			b.install(f.s097Host, first)
			log, _ := s097Logger()
			r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
			if len(b.stalls) != 0 {
				t.Fatalf("the portageq questions were not asked concurrently: %q", b.stalls)
			}
			if n, d := f.tmpAsked.Load(), f.distAsked.Load(); n != 1 || d != 1 {
				t.Errorf("portageq asked PORTAGE_TMPDIR %d and DISTDIR %d times, want once each", n, d)
			}
			if r.PortageTmp != s097TmpAnswer {
				t.Errorf("PortageTmp = %q, want %q", r.PortageTmp, s097TmpAnswer)
			}
			if want := []string{"/", s097TmpAnswer, s097DistAnswer}; !slices.Equal(r.DiskPaths, want) {
				t.Errorf("DiskPaths = %q, want %q", r.DiskPaths, want)
			}
		})
	}
}

// --- read-only remounts: R3.1, R6.2 ---

// TestS098ResolveHealth_R6_2_ReadonlyGetsTheInjectedMountReader: enabled, the
// injected reader reaches Deps; disabled, it is neither read nor handed on.
func TestS098ResolveHealth_R6_2_ReadonlyGetsTheInjectedMountReader(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		f := newS098Host()
		log, _ := s097Logger()
		r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
		m, ok := any(r.Mounts).(interface {
			Mounts() ([]sysmon.Mount, error)
		})
		if !ok {
			t.Fatalf("Mounts = %#v, want a reader with Mounts() ([]sysmon.Mount, error)", r.Mounts)
		}
		before := f.mounts.asked.Load()
		if _, err := m.Mounts(); err != nil {
			t.Fatalf("Mounts: %v", err)
		}
		if f.mounts.asked.Load() != before+1 {
			t.Error("Mounts does not reach the injected mount reader")
		}
	})
	t.Run("disabled", func(t *testing.T) {
		f := newS098Host()
		log, buf := s097Logger()
		cfg := s097TrayCfg(t, "monitors:\n  readonly:\n    enabled: false\n")
		r := resolveHealth(context.Background(), log, cfg, f.health())
		if n := f.mounts.asked.Load(); n != 0 {
			t.Errorf("read the mount table %d times with readonly disabled, want 0", n)
		}
		if r.Mounts != nil {
			t.Errorf("Mounts = %#v, want nil with readonly disabled", r.Mounts)
		}
		if l := s097Lines(buf, "monitor=readonly"); len(l) != 0 {
			t.Errorf("readonly lines %q, want none with readonly disabled", l)
		}
	})
}

// --- story 097 hosts ---

// TestS098ResolveHealth_NilCapabilitiesAreAbsentAndSilent: a hostHealth with
// none of the story 098 fields (every story 097 test builds one) leaves the
// four monitors nil, logs nothing for them and asks PORTAGE_TMPDIR no more
// than the disk monitor does. healthDeps returns resolveHealth's three
// story 097 results.
func TestS098ResolveHealth_NilCapabilitiesAreAbsentAndSilent(t *testing.T) {
	f := newS097Host()
	log, buf := s097Logger()
	cfg := s097TrayCfg(t, "monitors:\n  disk:\n    paths: [/s098-no-such-root/srv]\n")
	r := resolveHealth(context.Background(), log, cfg, f.health())
	if r.Emerge != nil || r.Mounts != nil || r.SystemUnits != nil || r.UserUnits != nil || r.Smart != nil {
		t.Errorf("Emerge %#v, Mounts %#v, SystemUnits %#v, UserUnits %#v, Smart %#v; want all nil without the capabilities",
			r.Emerge, r.Mounts, r.SystemUnits, r.UserUnits, r.Smart)
	}
	if n, d := f.tmpAsked.Load(), f.distAsked.Load(); n != 0 || d != 0 {
		t.Errorf("portageq asked PORTAGE_TMPDIR %d and DISTDIR %d times without an emerge follower and with configured paths, want neither", n, d)
	}
	for _, mon := range []string{"emerge", "units", "readonly", "smart"} {
		if l := s097Lines(buf, "monitor="+mon); len(l) != 0 {
			t.Errorf("%s lines %q, want none without the capability", mon, l)
		}
	}
	if l := s097Lines(buf, "disk health"); len(l) != 0 {
		t.Errorf("lines %q, want none without the capability", l)
	}

	g := newS097Host()
	sys, m, paths := healthDeps(context.Background(), log, cfg, g.health())
	if sys != g.probe || m == nil || r.Monitors == nil || !slices.Equal(paths, r.DiskPaths) {
		t.Errorf("healthDeps = (%v, %v, %q), want the injected reader, the monitors and resolveHealth's paths %q", sys, m, paths, r.DiskPaths)
	}
}
