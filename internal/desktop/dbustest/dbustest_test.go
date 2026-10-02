package dbustest_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
)

func connect(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatalf("connecting to %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestSession_StartsAPrivateBus: the address is a working bus, and it is not
// the developer's own session bus.
func TestSession_StartsAPrivateBus(t *testing.T) {
	addr := dbustest.Session(t)
	if addr == "" {
		t.Fatal("Session returned an empty address")
	}
	if own := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); own != "" && addr == own {
		t.Fatalf("Session returned the real session bus %s", addr)
	}
	conn := connect(t, addr)
	if names := conn.Names(); len(names) == 0 || !strings.HasPrefix(names[0], ":") {
		t.Errorf("connection names = %q, want a unique name", names)
	}
}

// TestSession_EachCallIsIsolated: two sessions are two buses; a name owned on
// one is not visible on the other.
func TestSession_EachCallIsIsolated(t *testing.T) {
	a, b := dbustest.Session(t), dbustest.Session(t)
	if a == b {
		t.Fatalf("two Session calls returned the same address %s", a)
	}
	ca, cb := connect(t, a), connect(t, b)
	reply, err := ca.RequestName("org.obentoo.Test.Isolation", dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName on bus a: %v, %v", reply, err)
	}
	var has bool
	if err := cb.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.obentoo.Test.Isolation").Store(&has); err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("a name owned on one test bus is visible on another")
	}
}

// TestSession_DaemonStopsAtCleanup: the bus dies with the test that started it.
func TestSession_DaemonStopsAtCleanup(t *testing.T) {
	var addr string
	t.Run("owner", func(t *testing.T) {
		addr = dbustest.Session(t)
		connect(t, addr) // the bus was reachable while its test ran
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := dbus.Connect(addr)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("the bus at %s still accepts connections after its test ended", addr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestSession_ChildHelper only runs inside the re-exec'd child of
// TestSession_MissingDaemon.
func TestSession_ChildHelper(t *testing.T) {
	if os.Getenv("DBUSTEST_CHILD") != "1" {
		t.Skip("helper for TestSession_MissingDaemon")
	}
	dbustest.Session(t)
	t.Log("CHILD-REACHED-END")
}

// TestSession_MissingDaemon: without dbus-daemon the helper skips locally but
// fails in CI, so the coverage floor can never be met by skipped D-Bus tests.
func TestSession_MissingDaemon(t *testing.T) {
	emptyPath := t.TempDir()
	run := func(ci string) (string, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSession_ChildHelper$", "-test.v", "-test.count=1")
		env := []string{"DBUSTEST_CHILD=1", "PATH=" + emptyPath, "HOME=" + os.Getenv("HOME")}
		if ci != "" {
			env = append(env, "CI="+ci)
		}
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run("")
	if err != nil {
		t.Fatalf("without CI the child failed; want a skip:\n%s", out)
	}
	if !strings.Contains(out, "--- SKIP: TestSession_ChildHelper") || strings.Contains(out, "CHILD-REACHED-END") {
		t.Errorf("without CI and without dbus-daemon the helper did not skip:\n%s", out)
	}

	out, err = run("true")
	if err == nil {
		t.Errorf("with CI=true and no dbus-daemon the child passed; want a failure:\n%s", out)
	}
	if !strings.Contains(out, "--- FAIL: TestSession_ChildHelper") || strings.Contains(out, "CHILD-REACHED-END") {
		t.Errorf("with CI=true the helper did not fail the test:\n%s", out)
	}
}
