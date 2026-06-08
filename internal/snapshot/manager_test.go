package snapshot

import (
	"context"
	"errors"
	"testing"
)

// --- recording fakes -------------------------------------------------------

type recordingEngine struct {
	events    *[]string
	createErr map[string]error
}

func (e *recordingEngine) Name() string { return "fake" }
func (e *recordingEngine) Create(_ context.Context, sv string) (Snapshot, error) {
	*e.events = append(*e.events, "create:"+sv)
	return Snapshot{Subvolume: sv, ID: "snap-" + sv}, e.createErr[sv]
}
func (e *recordingEngine) Prune(_ context.Context, sv string, _ Retention) ([]Snapshot, error) {
	*e.events = append(*e.events, "prune:"+sv)
	return nil, nil
}
func (e *recordingEngine) List(_ context.Context, _ string) ([]Snapshot, error) { return nil, nil }

type recordingShipper struct {
	name   string
	events *[]string
	err    error
}

func (s *recordingShipper) Name() string { return s.name }
func (s *recordingShipper) Send(_ context.Context, snap Snapshot) (ShipReport, error) {
	*s.events = append(*s.events, "ship:"+s.name+":"+snap.Subvolume)
	return ShipReport{Target: s.name}, s.err
}

type countingNotifier struct {
	calls int
	last  RunResult
}

func (n *countingNotifier) Notify(_ context.Context, res RunResult) error {
	n.calls++
	n.last = res
	return nil
}

// --- tests -----------------------------------------------------------------

func TestManagerRun_PipelineOrderAndNotifyOnce(t *testing.T) {
	var events []string
	notifier := &countingNotifier{}
	m := &Manager{
		engine:     &recordingEngine{events: &events},
		shippers:   []Shipper{&recordingShipper{name: "s1", events: &events}, &recordingShipper{name: "s2", events: &events}},
		notifier:   notifier,
		subvolumes: []string{"/home"},
	}

	res, err := m.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"create:/home", "prune:/home", "ship:s1:/home", "ship:s2:/home"}
	if !equalStrings(events, want) {
		t.Errorf("pipeline order = %v, want %v", events, want)
	}
	if notifier.calls != 1 {
		t.Errorf("notifier invoked %d times, want 1", notifier.calls)
	}
	if len(res.Stages) != 4 || res.Failed() {
		t.Errorf("result = %+v, want 4 ok stages", res)
	}
}

func TestManagerRun_OneShipFailsOthersAttempted(t *testing.T) {
	var events []string
	m := &Manager{
		engine: &recordingEngine{events: &events},
		shippers: []Shipper{
			&recordingShipper{name: "bad", events: &events, err: errors.New("connection refused")},
			&recordingShipper{name: "good", events: &events},
		},
		notifier:   &countingNotifier{},
		subvolumes: []string{"/"},
	}

	res, err := m.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from failed ship")
	}
	if !res.Failed() {
		t.Error("result.Failed() = false, want true")
	}
	// The good shipper must still have been attempted after the bad one failed.
	if !contains(events, "ship:good:/") {
		t.Errorf("second ship not attempted: %v", events)
	}

	var badFailed, goodOK bool
	for _, s := range res.Stages {
		if s.Stage == StageShip && s.Target == "bad" && s.Status == StatusFailed {
			badFailed = true
		}
		if s.Stage == StageShip && s.Target == "good" && s.Status == StatusOK {
			goodOK = true
		}
	}
	if !badFailed || !goodOK {
		t.Errorf("stage statuses wrong: badFailed=%v goodOK=%v stages=%+v", badFailed, goodOK, res.Stages)
	}
}

func TestManagerRun_CreateFailureSkipsSubvolume(t *testing.T) {
	var events []string
	m := &Manager{
		engine:     &recordingEngine{events: &events, createErr: map[string]error{"/": errors.New("subvolume not found")}},
		shippers:   []Shipper{&recordingShipper{name: "s1", events: &events}},
		notifier:   &countingNotifier{},
		subvolumes: []string{"/"},
	}

	res, _ := m.Run(context.Background())
	// Only the failed create event — no prune, no ship for this subvolume.
	if !equalStrings(events, []string{"create:/"}) {
		t.Errorf("events = %v, want only the failed create", events)
	}
	if !res.Failed() {
		t.Error("result.Failed() = false, want true")
	}
}

func TestManagerRun_CtxCancelShortCircuits(t *testing.T) {
	var events []string
	notifier := &countingNotifier{}
	m := &Manager{
		engine:     &recordingEngine{events: &events},
		shippers:   []Shipper{&recordingShipper{name: "s1", events: &events}},
		notifier:   notifier,
		subvolumes: []string{"/home", "/"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run

	res, err := m.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(events) != 0 {
		t.Errorf("no stages should run on a cancelled context, got %v", events)
	}
	if res.Err == "" {
		t.Error("result.Err not set on cancellation")
	}
}

func TestNewManager_BuildsFromConfig(t *testing.T) {
	cfg := Config{
		Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}},
		Ship:   []ShipConfig{{Type: "ssh", Target: "u@h:/p"}},
	}
	m, err := NewManager(cfg, "/etc/bentoo/snapshot.toml", &MockRunner{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if len(m.shippers) != 1 || len(m.subvolumes) != 1 {
		t.Errorf("manager wiring = %+v", m)
	}

	// Unknown driver propagates the factory error.
	if _, err := NewManager(Config{Engine: EngineConfig{Driver: "zfs"}}, "", nil); !errors.Is(err, ErrInvalidDriver) {
		t.Errorf("NewManager unknown engine: err = %v, want ErrInvalidDriver", err)
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
