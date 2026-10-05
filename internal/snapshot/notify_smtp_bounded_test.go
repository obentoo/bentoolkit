package snapshot

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// s053SilentSMTP accepts connections and never speaks: the B5 stall.
func s053SilentSMTP(t *testing.T) (addr string, accepted <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	acc := make(chan struct{}, 8)
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			acc <- struct{}{}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().String(), acc
}

func s053EmailTo(t *testing.T, addr string) emailNotifier {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := EmailConfig{To: []string{"ops@example.org"}, From: "bentoo@example.org"}
	cfg.SMTP.Host, cfg.SMTP.Port = host, port
	return emailNotifier{cfg: cfg, runner: &MockRunner{}}
}

type s053OKEngine struct{}

func (s053OKEngine) Name() string { return "ok" }
func (s053OKEngine) Create(_ context.Context, sv string) (Snapshot, error) {
	return Snapshot{ID: "1", Subvolume: sv, Path: sv + "/.snapshots/1/snapshot"}, nil
}
func (s053OKEngine) Prune(context.Context, string, Retention) ([]Snapshot, error) { return nil, nil }
func (s053OKEngine) List(context.Context, string) ([]Snapshot, error)             { return nil, nil }

func TestSMTPTimeout_EqualsNotifyHTTPTimeout(t *testing.T) {
	if smtpTimeout != notifyHTTPTimeout {
		t.Errorf("smtpTimeout = %v, notifyHTTPTimeout = %v, want both 15s", smtpTimeout, notifyHTTPTimeout)
	}
	if smtpTimeout != 15*time.Second {
		t.Errorf("smtpTimeout = %v, notifyHTTPTimeout = %v, want both 15s", smtpTimeout, notifyHTTPTimeout)
	}
}

// TestEmailNotifier_SMTPStallBoundedByTimeout pins R8.1/R8.3.
func TestEmailNotifier_SMTPStallBoundedByTimeout(t *testing.T) {
	orig := smtpTimeout
	smtpTimeout = 200 * time.Millisecond
	t.Cleanup(func() { smtpTimeout = orig })
	addr, _ := s053SilentSMTP(t)
	n := s053EmailTo(t, addr)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- n.Notify(t.Context(), failRun()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Errorf("Notify = nil against a silent server, want an error")
		}
		if elapsed := time.Since(start); elapsed > smtpTimeout+time.Second {
			t.Errorf("Notify took %v, want <= smtpTimeout + 1s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Notify still blocked after 5s against a silent SMTP server")
	}

	t.Run("run result and exit unchanged", func(t *testing.T) {
		lc := &logCapture{}
		warns := lc.all
		m := &Manager{
			engine:     s053OKEngine{},
			notifier:   multiNotifier{notifiers: []Notifier{n}, on: []string{"success", "failure"}, log: lc.logger()},
			subvolumes: []string{"/home"},
		}
		type out struct {
			res RunResult
			err error
		}
		ch := make(chan out, 1)
		go func() { r, e := m.Run(t.Context()); ch <- out{r, e} }()
		select {
		case o := <-ch:
			if o.err != nil || o.res.Failed() {
				t.Errorf("Run = (%+v, %v), want a successful run", o.res, o.err)
			}
			if len(warns()) != 1 {
				t.Errorf("warnings = %q, want exactly one notifier warning", warns())
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("Manager.Run still blocked after 5s on a stalled notifier")
		}
	})
}

// TestEmailNotifier_SMTPCtxCancelAborts pins R8.2.
func TestEmailNotifier_SMTPCtxCancelAborts(t *testing.T) {
	addr, accepted := s053SilentSMTP(t)
	n := s053EmailTo(t, addr)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- n.Notify(ctx, failRun()) }()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatalf("the notifier never connected")
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Errorf("Notify = nil after cancel, want an error")
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("Notify returned %v after cancel, want promptly (< 1s)", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Notify ignored ctx cancellation for 5s")
	}
}
