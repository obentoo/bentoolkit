// Package dbustest starts private D-Bus message buses for integration tests,
// so no test ever talks to the developer's own session or system bus.
package dbustest

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// daemonBinary is the bus daemon Session runs.
const daemonBinary = "dbus-daemon"

// startTimeout bounds how long Session waits for the daemon to print its
// address before it gives up.
const startTimeout = 10 * time.Second

// busConfig is a session-type bus that listens in a private directory and,
// unlike the stock session.conf, has no service directories: a call to a name
// no test registered fails instead of activating the developer's real
// notification daemon or desktop portal on the test bus.
const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=%DIR%</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// Session starts a private dbus-daemon and returns its address. The daemon is
// killed when t ends. Every call starts a new, isolated bus, so a test that
// needs a second bus (a fake system bus, say) calls Session twice.
//
// When dbus-daemon is not installed the test is skipped, except when CI is
// set to a true value: there it fails, so skipped D-Bus tests can never pass
// a CI run.
func Session(t testing.TB) string {
	t.Helper()

	path, err := exec.LookPath(daemonBinary)
	if err != nil {
		if inCI() {
			t.Fatalf("dbustest: %s is required in CI: %v", daemonBinary, err)
		}
		t.Skipf("dbustest: %s not installed: %v", daemonBinary, err)
	}

	// A short directory of its own, not t.TempDir(): a unix socket path must
	// fit in 108 bytes, and t.TempDir() embeds up to 64 bytes of test name.
	dir, err := os.MkdirTemp("", "dbustest-") //nolint:usetesting // t.TempDir() can push the bus socket path past the 108-byte sun_path limit; removed by t.Cleanup below.
	if err != nil {
		t.Fatalf("dbustest: creating the bus directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	confPath := filepath.Join(dir, "bus.conf")
	conf := strings.ReplaceAll(busConfig, "%DIR%", dir)
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatalf("dbustest: writing %s: %v", confPath, err)
	}

	//nolint:gosec // G204: path comes from exec.LookPath and confPath is a file this function wrote; no test input reaches the command line.
	cmd := exec.Command(path, "--config-file="+confPath, "--nofork", "--print-address=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("dbustest: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("dbustest: starting %s: %v", path, err)
	}
	// Registered before the address is read, so a daemon that never answers
	// is still killed. Cleanups run last-in first-out: the daemon dies before
	// its directory is removed.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	addr, err := readAddress(stdout)
	if err != nil {
		// Wait before reading stderr: until Wait returns, os/exec may still be
		// copying the daemon's stderr into the buffer.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("dbustest: reading the address of %s: %v; stderr: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return addr
}

// readAddress reads the first line the daemon prints, its bus address.
func readAddress(stdout io.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		done <- result{line, err}
	}()

	timer := time.NewTimer(startTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			return "", r.err
		}
		addr := strings.TrimSpace(r.line)
		if addr == "" {
			return "", errors.New("the daemon printed an empty address")
		}
		return addr, nil
	case <-timer.C:
		return "", errors.New("timed out after " + startTimeout.String())
	}
}

// inCI reports whether the CI environment variable holds a true value
// ("true", "1", ...), as GitHub Actions and most CI systems set it.
func inCI() bool {
	ci, err := strconv.ParseBool(os.Getenv("CI"))
	return err == nil && ci
}
