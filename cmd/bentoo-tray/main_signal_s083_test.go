package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/desktop/dbusx"
	"github.com/obentoo/bentoolkit/internal/tray"
)

const s083StoppedWhileStarting = "while starting"

// s083ExitCode runs exitCode with a text logger and returns the code and the
// parsed log lines.
func s083ExitCode(ctx context.Context, t *testing.T, err error) (int, []map[string]string, string) {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	code := exitCode(ctx, log, err)
	return code, logLines(t, buf.String()), buf.String()
}

// s083Stopping counts the INFO lines saying the tray stopped while starting.
func s083Stopping(lines []map[string]string) int {
	n := 0
	for _, l := range lines {
		if l["level"] == "INFO" && strings.Contains(l["msg"], s083StoppedWhileStarting) && strings.Contains(strings.ToLower(l["msg"]), "stop") {
			n++
		}
	}
	return n
}

func s083HasMsg(lines []map[string]string, level, msg string) bool {
	for _, l := range lines {
		if l["level"] == level && l["msg"] == msg {
			return true
		}
	}
	return false
}

func s083Contexts() (signalled, live context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx, context.Background()
}

var (
	errS083NameRequestCanceled = fmt.Errorf("starting bentoo-tray: %w",
		fmt.Errorf("requesting bus name %s: %w", dbusx.BusName, context.Canceled))
	errS083ConnectCanceled = fmt.Errorf("connecting to the session bus at unix:path=/run/user/1000/bus: %w: %w",
		context.Canceled, errors.New("read unix ->/run/user/1000/bus: use of closed network connection"))
	errS083NameRequestTimedOut = fmt.Errorf("starting bentoo-tray: %w",
		fmt.Errorf("requesting bus name %s: %w", dbusx.BusName, context.DeadlineExceeded))
	errS083StateUnreadable = fmt.Errorf("starting bentoo-tray: %w",
		errors.New("read state /home/u/.local/state/bentoo-notices/state.json: permission denied"))
)

// TestS083Run_ExitCodeTreatsAStopDuringStartupAsAStop is R2.1 (and R1.4 for
// the App's nil): a startup that ended because the stop signal cancelled it,
// in the name request or while connecting, exits 0 with one INFO line and no
// ERROR line.
func TestS083Run_ExitCodeTreatsAStopDuringStartupAsAStop(t *testing.T) {
	signalled, _ := s083Contexts()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"during the name request", errS083NameRequestCanceled},
		{"while connecting to the session bus", errS083ConnectCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, lines, raw := s083ExitCode(signalled, t, tc.err)
			if code != exitOK {
				t.Errorf("exit code %d, want %d for a stop signal during startup; log:\n%s", code, exitOK, raw)
			}
			if n := s083Stopping(lines); n != 1 {
				t.Errorf("%d INFO lines saying the tray stopped %s, want 1; log:\n%s", n, s083StoppedWhileStarting, raw)
			}
			if hasLevel(lines, "ERROR") {
				t.Errorf("a stop signal was logged at ERROR; log:\n%s", raw)
			}
		})
	}
	t.Run("the App stopped after acquiring the name", func(t *testing.T) {
		code, lines, raw := s083ExitCode(signalled, t, nil)
		if code != exitOK || len(lines) != 0 {
			t.Errorf("exit code %d with %d lines, want %d and none (the App logged the stop); log:\n%s", code, len(lines), exitOK, raw)
		}
	})
}

// TestS083Run_ExitCodeKeepsRealFailuresFailures is R2.2, R2.3, R4.1 and
// R4.3 around the new case: a cancellation the signal did not cause, a timed
// out name request, and a failure that merely coincides with a signal are
// still startup failures; another instance and a lost bus keep their codes.
func TestS083Run_ExitCodeKeepsRealFailuresFailures(t *testing.T) {
	signalled, live := s083Contexts()
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		err      error
		want     int
		level    string
		msg      string
		mentions string
	}{
		{"canceled without a signal", live, errS083NameRequestCanceled, exitStartup, "ERROR", "bentoo-tray could not start", dbusx.BusName},
		{"name request timed out without a signal", live, errS083NameRequestTimedOut, exitStartup, "ERROR", "bentoo-tray could not start", dbusx.BusName},
		{"unreadable state with a signal", signalled, errS083StateUnreadable, exitStartup, "ERROR", "bentoo-tray could not start", "permission denied"},
		{"timed out with a signal", signalled, errS083NameRequestTimedOut, exitStartup, "ERROR", "bentoo-tray could not start", dbusx.BusName},
		{"another instance with a signal", signalled, tray.ErrAlreadyRunning, exitOK, "INFO", "bentoo-tray is already running in this session; exiting", dbusx.BusName},
		{"bus lost", live, fmt.Errorf("running: %w", tray.ErrBusLost), exitBusLost, "ERROR", "the session bus was lost; exiting", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, lines, raw := s083ExitCode(tc.ctx, t, tc.err)
			if code != tc.want {
				t.Errorf("exit code %d, want %d; log:\n%s", code, tc.want, raw)
			}
			if !s083HasMsg(lines, tc.level, tc.msg) {
				t.Errorf("no %s %q line; log:\n%s", tc.level, tc.msg, raw)
			}
			if !strings.Contains(raw, tc.mentions) {
				t.Errorf("the log does not name %q:\n%s", tc.mentions, raw)
			}
			if n := s083Stopping(lines); n != 0 {
				t.Errorf("a real failure was reported as a stop while starting; log:\n%s", raw)
			}
		})
	}
}

// s083SilentBus listens on a unix socket that accepts a connection, reads the
// client's first authentication line and never answers: a bus the tray is
// still connecting to. It returns the address and a channel that delivers
// once the client is blocked waiting for the reply.
func s083SilentBus(t *testing.T) (string, <-chan error) {
	t.Helper()
	dir, err := os.MkdirTemp("", "s083") //nolint:usetesting // t.TempDir names the path after the test, past the 108-byte unix socket path limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "bus")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case conn := <-accepted:
			_ = conn.Close()
		default:
		}
	})
	waiting := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			waiting <- err
			return
		}
		accepted <- conn
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err == nil && !strings.Contains(line, "AUTH") {
			err = fmt.Errorf("first line from the client is %q, not an AUTH command", line)
		}
		waiting <- err
	}()
	return "unix:path=" + sock, waiting
}

// TestS083Run_SignalWhileConnectingExitsZeroWithInfo is R2.1 end to end: the
// stop signal arrives while the tray is connecting to the session bus, which
// accepted the connection and has not answered. The process exits 0 with one
// INFO line saying it stopped while starting and no ERROR line.
func TestS083Run_SignalWhileConnectingExitsZeroWithInfo(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			session, waiting := s083SilentBus(t)
			c := newChild(t, session, "unix:path=/nonexistent/bentoo-tray-bus", nil)
			if err := c.cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.cmd.Process.Kill() })
			select {
			case err := <-waiting:
				if err != nil {
					t.Fatalf("the tray never started authenticating: %v; stderr:\n%s", err, c.stderr)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("the tray never connected to the session bus; stderr:\n%s", c.stderr)
			}
			if err := c.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if code := c.wait(t, 5*time.Second); code != exitOK {
				t.Fatalf("exit code %d after %v while connecting, want 0; stderr:\n%s", code, sig, c.stderr)
			}
			lines := logLines(t, c.stderr.String())
			if n := s083Stopping(lines); n != 1 {
				t.Errorf("%d INFO lines saying the tray stopped %s, want 1; stderr:\n%s", n, s083StoppedWhileStarting, c.stderr)
			}
			if hasLevel(lines, "ERROR") {
				t.Errorf("a stop signal was logged at ERROR; stderr:\n%s", c.stderr)
			}
		})
	}
}
