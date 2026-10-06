package main

// Every exit path is proved in a re-exec'd child that calls the real main(),
// so os.Exit runs for real and nothing a defer would do is taken for granted
// (the osExit-intercept trap of cmd/bentoo).

import (
	"bufio"
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const childEnv = "BENTOO_TRAY_TEST_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		os.Args = append([]string{"bentoo-tray"}, strings.Fields(os.Getenv("BENTOO_TRAY_TEST_ARGS"))...)
		main()
		return
	}
	os.Exit(m.Run())
}

// startBus runs a private dbus-daemon the test can kill; it skips locally but
// fails in CI when the daemon is missing.
func startBus(t *testing.T) (string, *exec.Cmd) {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("dbus-daemon not found: %v", err)
		}
		t.Skip("dbus-daemon not found")
	}
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line), cmd
}

// syncBuffer is a bytes.Buffer safe to read while os/exec's copy goroutine
// writes the running child's output into it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type child struct {
	cmd             *exec.Cmd
	stdout, stderr  *syncBuffer
	home, stateHome string
}

// newChild prepares the test binary to run main() with an isolated home,
// config and state directory.
func newChild(t *testing.T, sessionAddr, systemAddr string, extraEnv map[string]string, args ...string) *child {
	t.Helper()
	home := t.TempDir()
	c := &child{stdout: &syncBuffer{}, stderr: &syncBuffer{}, home: home, stateHome: filepath.Join(home, "state")}
	c.cmd = exec.Command(os.Args[0])
	env := []string{
		childEnv + "=1",
		"BENTOO_TRAY_TEST_ARGS=" + strings.Join(args, " "),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"XDG_STATE_HOME=" + c.stateHome,
		"PATH=" + os.Getenv("PATH"),
		"DBUS_SESSION_BUS_ADDRESS=" + sessionAddr,
		"DBUS_SYSTEM_BUS_ADDRESS=" + systemAddr,
		// Under -cover (CI runs -coverprofile) a child test binary without
		// GOCOVERDIR prints a plain "warning: GOCOVERDIR not set" line to
		// stderr, which the key=value assertions would read as the tray's.
		"GOCOVERDIR=" + t.TempDir(),
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	c.cmd.Env = env
	c.cmd.Stdout, c.cmd.Stderr = c.stdout, c.stderr
	return c
}

// wait returns the exit code, failing the test after d.
func (c *child) wait(t *testing.T, d time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				t.Fatalf("the process was killed by %v instead of exiting; stderr:\n%s", ws.Signal(), c.stderr)
			}
			return ee.ExitCode()
		}
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
		return 0
	case <-time.After(d):
		_ = c.cmd.Process.Kill()
		t.Fatalf("the process did not exit within %v; stderr:\n%s", d, c.stderr)
		return -1
	}
}

func (c *child) run(t *testing.T, d time.Duration) int {
	t.Helper()
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return c.wait(t, d)
}

var logKey = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// parseKV parses one slog text line into its keys and values, or reports why
// it is not key=value.
func parseKV(line string) (map[string]string, error) {
	out := map[string]string{}
	rest := line
	for rest != "" {
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 || !logKey.MatchString(rest[:eq]) {
			return nil, errors.New("no key=value at " + strconv.Quote(rest))
		}
		key := rest[:eq]
		rest = rest[eq+1:]
		var val string
		if strings.HasPrefix(rest, `"`) {
			q, err := strconv.QuotedPrefix(rest)
			if err != nil {
				return nil, err
			}
			val, _ = strconv.Unquote(q)
			rest = rest[len(q):]
		} else {
			sp := strings.IndexByte(rest, ' ')
			if sp < 0 {
				sp = len(rest)
			}
			val, rest = rest[:sp], rest[sp:]
		}
		out[key] = val
		rest = strings.TrimPrefix(rest, " ")
	}
	return out, nil
}

// logLines checks every stderr line is a key=value event with time, level and
// msg (R13.1), and returns them parsed.
func logLines(t *testing.T, stderr string) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, line := range strings.Split(strings.TrimRight(stderr, "\n"), "\n") {
		if line == "" {
			continue
		}
		kv, err := parseKV(line)
		if err != nil {
			t.Errorf("stderr line is not key=value (%v): %s", err, line)
			continue
		}
		for _, k := range []string{"time", "level", "msg"} {
			if _, ok := kv[k]; !ok {
				t.Errorf("log line lacks %q: %s", k, line)
			}
		}
		out = append(out, kv)
	}
	return out
}

func hasLevel(lines []map[string]string, level string) bool {
	for _, l := range lines {
		if l["level"] == level {
			return true
		}
	}
	return false
}

func busConn(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func owned(t *testing.T, conn *dbus.Conn) bool {
	t.Helper()
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.obentoo.BentooTray").Store(&has); err != nil {
		t.Fatal(err)
	}
	return has
}

// TestRun_UnreachableBusExitsOneNamingTheAddress is R1.3.
func TestRun_UnreachableBusExitsOneNamingTheAddress(t *testing.T) {
	c := newChild(t, "unix:path=/nonexistent/bentoo-tray-bus", "unix:path=/nonexistent/bentoo-tray-bus", nil)
	if code := c.run(t, 10*time.Second); code != 1 {
		t.Fatalf("exit code %d, want 1; stderr:\n%s", code, c.stderr)
	}
	if !strings.Contains(c.stderr.String(), "/nonexistent/bentoo-tray-bus") {
		t.Errorf("stderr does not name the bus address:\n%s", c.stderr)
	}
	if !hasLevel(logLines(t, c.stderr.String()), "ERROR") {
		t.Errorf("no ERROR line:\n%s", c.stderr)
	}
}

// alreadyRunning starts a child while the test itself owns the tray's name.
func alreadyRunning(t *testing.T, env map[string]string) (int, string) {
	t.Helper()
	session, _ := startBus(t)
	system, _ := startBus(t)
	holder := busConn(t, session)
	if reply, err := holder.RequestName("org.obentoo.BentooTray", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("holding the name: %v %v", reply, err)
	}
	c := newChild(t, session, system, env)
	code := c.run(t, 10*time.Second)
	return code, c.stderr.String()
}

// TestRun_AlreadyRunningExitsZeroWithInfo is R1.2.
func TestRun_AlreadyRunningExitsZeroWithInfo(t *testing.T) {
	code, stderr := alreadyRunning(t, nil)
	if code != 0 {
		t.Fatalf("exit code %d, want 0; stderr:\n%s", code, stderr)
	}
	found := false
	for _, l := range logLines(t, stderr) {
		if l["level"] == "INFO" && strings.Contains(strings.ToLower(l["msg"]), "already running") {
			found = true
		}
	}
	if !found {
		t.Errorf("no INFO line saying an instance is already running:\n%s", stderr)
	}
}

// TestRun_LogLevelFollowsTheEnvironment is R13.2, plus an unknown value
// falling back to info with a WARN naming it.
func TestRun_LogLevelFollowsTheEnvironment(t *testing.T) {
	cases := []struct {
		level    string
		wantInfo bool
	}{
		{"", true}, {"debug", true}, {"info", true}, {"warn", false}, {"error", false}, {"bogus", true},
	}
	for _, c := range cases {
		t.Run("level="+c.level, func(t *testing.T) {
			env := map[string]string{}
			if c.level != "" {
				env["BENTOO_TRAY_LOG_LEVEL"] = c.level
			}
			code, stderr := alreadyRunning(t, env)
			if code != 0 {
				t.Fatalf("exit code %d; stderr:\n%s", code, stderr)
			}
			lines := logLines(t, stderr)
			if got := hasLevel(lines, "INFO"); got != c.wantInfo {
				t.Errorf("INFO lines present = %v, want %v:\n%s", got, c.wantInfo, stderr)
			}
			if c.level == "bogus" {
				warned := false
				for _, l := range lines {
					if l["level"] == "WARN" && strings.Contains(strings.Join(mapValues(l), " "), "bogus") {
						warned = true
					}
				}
				if !warned {
					t.Errorf("an unknown BENTOO_TRAY_LOG_LEVEL was not reported at WARN naming it:\n%s", stderr)
				}
			}
		})
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// startTray starts a child on private session and system buses, over a
// previously saved state, and waits until it owns its name (R1.1).
func startTray(t *testing.T, session, system string) *child {
	t.Helper()
	return startTrayWithConfig(t, session, system, "", true)
}

// seedState writes a valid format-1 state.json at mode 0644: the App must load
// this saved state and re-save it at the stop, and Save's 0600 proves the file
// was rewritten rather than left as seeded.
func seedState(t *testing.T, c *child) {
	t.Helper()
	path := filepath.Join(c.stateHome, "bentoo-notices", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	seed := `{"format":1,"etag":"","serial":0,"notices":{},"pause_until":"0001-01-01T00:00:00Z","failures":0}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

// startTrayWithConfig is startTray with a config.yaml written first, and a
// saved state only when withState is set.
func startTrayWithConfig(t *testing.T, session, system, configYAML string, withState bool) *child {
	t.Helper()
	c := newChild(t, session, system, nil)
	if withState {
		seedState(t, c)
	}
	if configYAML != "" {
		path := filepath.Join(c.home, "config", "bentoo", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(configYAML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.cmd.Process.Kill() })
	obs := busConn(t, session)
	deadline := time.Now().Add(10 * time.Second)
	for !owned(t, obs) {
		if time.Now().After(deadline) {
			t.Fatalf("the tray never owned org.obentoo.BentooTray; stderr:\n%s", c.stderr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return c
}

func assertStateSaved(t *testing.T, c *child) {
	t.Helper()
	path := filepath.Join(c.stateHome, "bentoo-notices", "state.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("state was not persisted at %s: %v", path, err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %v, want 0600", path, fi.Mode().Perm())
	}
}

// TestRun_SignalsStopCleanlyWithinFiveSeconds is R1.1, R1.4, R10.1 and R13.1:
// each of SIGINT, SIGTERM and SIGHUP (the logout signal) ends in exit 0 within
// 5 s, with the state saved under $XDG_STATE_HOME/bentoo-notices and the name
// released.
func TestRun_SignalsStopCleanlyWithinFiveSeconds(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			session, _ := startBus(t)
			system, _ := startBus(t)
			c := startTray(t, session, system)
			if err := c.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if code := c.wait(t, 5*time.Second); code != 0 {
				t.Fatalf("exit code %d after %v, want 0; stderr:\n%s", code, sig, c.stderr)
			}
			assertStateSaved(t, c)
			if owned(t, busConn(t, session)) {
				t.Error("the name is still owned after exit")
			}
			logLines(t, c.stderr.String())
		})
	}
}

// TestRun_BusLostExitsTwo is R1.5: the session bus dies under a running tray.
func TestRun_BusLostExitsTwo(t *testing.T) {
	session, daemon := startBus(t)
	system, _ := startBus(t)
	c := startTray(t, session, system)
	if err := daemon.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if code := c.wait(t, 10*time.Second); code != 2 {
		t.Fatalf("exit code %d after the bus died, want 2; stderr:\n%s", code, c.stderr)
	}
	if !hasLevel(logLines(t, c.stderr.String()), "ERROR") {
		t.Errorf("no ERROR line:\n%s", c.stderr)
	}
	assertStateSaved(t, c)
}

// TestRun_InsecureFeedURLLogsErrorAndKeepsRunning is R2.9 at the binary: a
// non-https tray.feed_url refuses the poller with an ERROR naming the URL, but
// the process keeps running (news and the icon still work) and stops cleanly.
func TestRun_InsecureFeedURLLogsErrorAndKeepsRunning(t *testing.T) {
	const insecure = "http://obentoo.org/notices.json"
	session, _ := startBus(t)
	system, _ := startBus(t)
	c := startTrayWithConfig(t, session, system, "tray:\n  feed_url: "+insecure+"\n", false)
	obs := busConn(t, session)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(c.stderr.String(), insecure) {
		if time.Now().After(deadline) {
			t.Fatalf("no log line names the refused URL %s:\n%s", insecure, c.stderr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if !owned(t, obs) {
		t.Fatalf("the tray stopped after refusing the feed URL; stderr:\n%s", c.stderr)
	}
	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := c.wait(t, 5*time.Second); code != 0 {
		t.Fatalf("exit code %d after SIGTERM, want 0; stderr:\n%s", code, c.stderr)
	}
	found := false
	for _, l := range logLines(t, c.stderr.String()) {
		if l["level"] == "ERROR" && strings.Contains(strings.Join(mapValues(l), " "), insecure) {
			found = true
		}
	}
	if !found {
		t.Errorf("the refused feed URL was not logged at ERROR:\n%s", c.stderr)
	}
}

// TestMain_HasNoOsExitSeam: the binary keeps one exit point (stories 058/063);
// the legacy package-level osExit variable is not copied.
func TestMain_HasNoOsExitSeam(t *testing.T) {
	fset := token.NewFileSet()
	//nolint:staticcheck // SA1019: ParseDir is deprecated because it ignores build tags; this test only lists the package's files
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		for name, f := range p.Files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					for _, n := range spec.(*ast.ValueSpec).Names {
						if n.Name == "osExit" {
							t.Errorf("%s declares a package-level osExit variable", name)
						}
					}
				}
			}
		}
	}
}
