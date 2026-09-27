//go:build unix

package main

import (
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Story 054, R5.8: `overlay push` and `overlay pull` cancel their git call on
// SIGINT. The remote is B4's reproduction — a git:// server that accepts the
// connection and never answers — so git waits forever unless the command's
// signal context stops it. The signal is sent to this process once git has
// connected, i.e. while the command is genuinely blocked on the network.
func TestOverlayGitCommandsStopOnSIGINT(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func()
	}{
		{"push", func() { runPush(pushCmd, nil) }},
		{"pull", func() { runPull(pullCmd, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connected := silentGitRemote(t)
			overlayWithSilentRemote(t, connected.addr)

			// A sink keeps SIGINT from killing the test binary when a
			// regression leaves the command without a handler: the test then
			// fails on its deadline instead of taking the whole run down.
			sink := make(chan os.Signal, 1)
			signal.Notify(sink, syscall.SIGINT)
			t.Cleanup(func() { signal.Stop(sink) })

			done := make(chan int, 1)
			go func() { done <- withExitIntercept(tc.run) }()

			select {
			case <-connected.ch:
			case <-time.After(15 * time.Second):
				t.Fatal("instrument: git never connected to the silent remote")
			}
			sent := time.Now()
			if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
				t.Fatal(err)
			}

			select {
			case code := <-done:
				if took := time.Since(sent); took > 6*time.Second {
					t.Errorf("overlay %s returned %v after SIGINT, want within 6 s (R5.8)", tc.name, took)
				}
				if code != 1 {
					t.Errorf("overlay %s exit code = %d after SIGINT, want 1", tc.name, code)
				}
			case <-time.After(8 * time.Second):
				t.Fatalf("overlay %s still blocked on the silent remote 8 s after SIGINT: the command does not cancel its git call on a signal (R5.8)", tc.name)
			}
		})
	}
}

type silentRemote struct {
	addr string
	ch   chan struct{}
}

// silentGitRemote accepts git:// connections and never writes a byte.
func silentGitRemote(t *testing.T) silentRemote {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := silentRemote{addr: ln.Addr().String(), ch: make(chan struct{})}
	var (
		mu   sync.Mutex
		held []net.Conn
	)
	go func() {
		first := true
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
			if first {
				close(r.ch)
				first = false
			}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			_ = c.Close()
		}
	})
	return r
}

// overlayWithSilentRemote makes the test overlay a repo with one commit whose
// branch tracks origin, origin being the silent git:// remote at addr.
func overlayWithSilentRemote(t *testing.T, addr string) {
	t.Helper()
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	t.Cleanup(cleanup)
	if err := os.WriteFile(filepath.Join(overlayDir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"checkout", "-q", "-b", "main"},
		{"add", "README"},
		{"commit", "-q", "-m", "c1"},
		{"remote", "add", "origin", "git://" + addr + "/overlay"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
		{"config", "branch.main.remote", "origin"},
		{"config", "branch.main.merge", "refs/heads/main"},
		{"commit", "-q", "--allow-empty", "-m", "c2"},
	} {
		if err := runGitCmd(overlayDir, args...); err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
	}
}
