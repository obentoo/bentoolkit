package dbustest_test

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
)

// socketPath extracts the unix socket path from a bus address such as
// "unix:path=/tmp/dbustest-123/dbus-abc,guid=...". It fails the test when the
// address is not a path-based unix address.
func socketPath(t *testing.T, addr string) string {
	t.Helper()
	const prefix = "unix:path="
	if !strings.HasPrefix(addr, prefix) {
		t.Fatalf("address %q is not a unix:path= address", addr)
	}
	path, _, _ := strings.Cut(strings.TrimPrefix(addr, prefix), ",")
	return path
}

// waitUnreachable polls addr until a connection is refused, failing the test
// when the bus still accepts connections after the deadline.
func waitUnreachable(t *testing.T, addr, why string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := dbus.Connect(addr)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("the bus at %s still accepts connections %s", addr, why)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestStartKillable_StartsAHermeticPrivateBus: the address is a working bus,
// it is not the developer's own session bus, and it is hermetic — no service
// directories, so nothing on it can activate a real desktop service — with a
// socket path that fits sun_path.
func TestStartKillable_StartsAHermeticPrivateBus(t *testing.T) {
	addr, cmd := dbustest.StartKillable(t)
	if addr == "" {
		t.Fatal("StartKillable returned an empty address")
	}
	if cmd == nil || cmd.Process == nil {
		t.Fatalf("StartKillable returned no running process: %+v", cmd)
	}
	if own := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); own != "" && addr == own {
		t.Fatalf("StartKillable returned the real session bus %s", addr)
	}
	if path := socketPath(t, addr); len(path) >= 108 {
		t.Errorf("socket path %q is %d bytes; a unix socket path must fit in 108", path, len(path))
	}

	conn := connect(t, addr)
	if names := conn.Names(); len(names) == 0 || !strings.HasPrefix(names[0], ":") {
		t.Errorf("connection names = %q, want a unique name", names)
	}

	// Hostile half: a stock --session daemon would list every service file on
	// the host here, and a call to an unowned name would launch it.
	var activatable []string
	if err := conn.BusObject().Call("org.freedesktop.DBus.ListActivatableNames", 0).Store(&activatable); err != nil {
		t.Fatalf("ListActivatableNames: %v", err)
	}
	for _, name := range activatable {
		if name != "org.freedesktop.DBus" {
			t.Errorf("the bus can activate %q; want no service directories (activatable: %q)", name, activatable)
			break
		}
	}
}

// TestStartKillable_ProcessIsTheDaemonOfItsAddress: the returned process is
// the daemon behind the returned address and no other. Two calls give two
// buses and two processes; killing one process takes down its own bus and
// leaves the other one running.
func TestStartKillable_ProcessIsTheDaemonOfItsAddress(t *testing.T) {
	addrA, cmdA := dbustest.StartKillable(t)
	addrB, cmdB := dbustest.StartKillable(t)

	// Hostile half (wrong collapse): two calls must not share a bus or a process.
	if addrA == addrB {
		t.Fatalf("two StartKillable calls returned the same address %s", addrA)
	}
	if cmdA == nil || cmdB == nil || cmdA.Process == nil || cmdB.Process == nil {
		t.Fatalf("StartKillable returned no running process: %+v, %+v", cmdA, cmdB)
	}
	if cmdA.Process.Pid == cmdB.Process.Pid {
		t.Fatalf("two StartKillable calls returned the same process %d", cmdA.Process.Pid)
	}

	ca, cb := connect(t, addrA), connect(t, addrB)
	reply, err := ca.RequestName("org.obentoo.Test.Killable", dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName on bus A: %v, %v", reply, err)
	}
	var has bool
	if err := cb.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.obentoo.Test.Killable").Store(&has); err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("a name owned on one killable bus is visible on another")
	}

	// Killing A's process ends bus A ...
	if err := cmdA.Process.Kill(); err != nil {
		t.Fatalf("killing the daemon of %s: %v", addrA, err)
	}
	waitUnreachable(t, addrA, "after its process was killed")

	// ... and (hostile half, wrong split) bus B still answers: the process
	// returned for A was A's daemon, not B's.
	if err := cb.BusObject().Call("org.freedesktop.DBus.Peer.Ping", 0).Err; err != nil {
		t.Errorf("bus B stopped answering when bus A's process was killed: %v", err)
	}
	conn, err := dbus.Connect(addrB)
	if err != nil {
		t.Errorf("bus B refuses new connections after bus A's process was killed: %v", err)
	} else {
		_ = conn.Close()
	}
}

// TestStartKillable_DaemonStopsAtCleanup: a daemon the test never killed dies
// with the test that started it, and its socket directory goes with it.
func TestStartKillable_DaemonStopsAtCleanup(t *testing.T) {
	var addr string
	var cmd *exec.Cmd
	t.Run("owner", func(t *testing.T) {
		addr, cmd = dbustest.StartKillable(t)
		connect(t, addr) // the bus was reachable while its test ran
	})
	if addr == "" || cmd == nil {
		// The owner subtest skipped (no dbus-daemon outside CI); a failure
		// would already have failed this test.
		t.Skip("the owner subtest started no daemon")
	}
	waitUnreachable(t, addr, "after its test ended")
	path := socketPath(t, addr)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket %s outlived its test (stat error %v)", path, err)
	}
}

// TestStartKillable_KilledByTheTestIsNotAnError: a test that kills and reaps
// the daemon itself, as the bus-loss test does, must still pass — the
// cleanup tolerates a process that is already gone.
func TestStartKillable_KilledByTheTestIsNotAnError(t *testing.T) {
	var addr string
	ok := t.Run("killer", func(t *testing.T) {
		var cmd *exec.Cmd
		addr, cmd = dbustest.StartKillable(t)
		if err := cmd.Process.Kill(); err != nil {
			t.Fatalf("killing the daemon: %v", err)
		}
		_ = cmd.Wait()
	})
	if !ok {
		t.Fatal("a test that killed its own daemon failed")
	}
	if addr == "" {
		t.Skip("the killer subtest started no daemon")
	}
	waitUnreachable(t, addr, "after the test killed its process")
}

// TestStartKillable_ChildHelper only runs inside the re-exec'd child of
// TestStartKillable_MissingDaemon.
func TestStartKillable_ChildHelper(t *testing.T) {
	if os.Getenv("DBUSTEST_KILLABLE_CHILD") != "1" {
		t.Skip("helper for TestStartKillable_MissingDaemon")
	}
	dbustest.StartKillable(t)
	t.Log("CHILD-REACHED-END")
}

// TestStartKillable_MissingDaemon: without dbus-daemon the helper skips
// locally but fails when CI holds a true value, so the D-Bus test that kills
// its own daemon can never pass a CI run by skipping.
func TestStartKillable_MissingDaemon(t *testing.T) {
	emptyPath := t.TempDir()
	run := func(ci string) (string, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStartKillable_ChildHelper$", "-test.v", "-test.count=1")
		env := []string{"DBUSTEST_KILLABLE_CHILD=1", "PATH=" + emptyPath, "HOME=" + os.Getenv("HOME")}
		if ci != "" {
			env = append(env, "CI="+ci)
		}
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	for _, ci := range []string{"", "false"} {
		out, err := run(ci)
		if err != nil {
			t.Errorf("CI=%q: the child failed; want a skip:\n%s", ci, out)
			continue
		}
		if !strings.Contains(out, "--- SKIP: TestStartKillable_ChildHelper") || strings.Contains(out, "CHILD-REACHED-END") {
			t.Errorf("CI=%q and no dbus-daemon: the helper did not skip:\n%s", ci, out)
		}
	}

	for _, ci := range []string{"true", "1"} {
		out, err := run(ci)
		if err == nil {
			t.Errorf("CI=%s and no dbus-daemon: the child passed; want a failure:\n%s", ci, out)
		}
		if !strings.Contains(out, "--- FAIL: TestStartKillable_ChildHelper") || strings.Contains(out, "CHILD-REACHED-END") {
			t.Errorf("CI=%s: the helper did not fail the test:\n%s", ci, out)
		}
	}
}
