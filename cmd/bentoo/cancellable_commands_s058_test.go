//go:build unix

package main

// Authored for story 058, sub-task 1.4 — R4.6, R4.7, R4.8, R2.10.
//
// Written from story.md R4.6–R4.8 and design.md "Process-wide signal context"
// (Policy) and "Exit-code table", never from an implementation.
//
// Two promises are pinned here, against the REAL tree:
//
//   - R4.7: the commands carrying Annotations["bentoo/cancellable"] == "true"
//     are exactly the ones whose handler installed a signal context at
//     6be73ec — the list in cancellableCommands below, found with
//     grep -n 'signalContext(\|signal.NotifyContext(' on the production files
//     at 6be73ec and followed to each command. Both directions are hostile: a
//     command missing from the tree's set would now die on Ctrl+C mid-work; a
//     command added to it would now swallow a Ctrl+C it used to die of.
//   - R4.8 / R4.6: a re-exec'd child running the real tree through the real
//     main is interrupted by its first SIGINT while it waits on an external
//     tool or a network peer, and exits with the code measured at 6be73ec by
//     this same test (see red-evidence.yaml). A command that installed no
//     signal context at 6be73ec is killed by that same first signal.
//
// `notice new` and `notice revise`, which main gained after 6be73ec, are
// held to the same promises measured at e051559, the main commit the branch
// integrates.
//
// Four cancellable commands are not pinned (see s058CancellablePins for the
// reason each could not be interrupted mid-work at 6be73ec).
//
// The child calls main() itself, so this file runs unchanged at 6be73ec (where
// the pins were measured) and after the migration (where they must still hold).
//
// Red on arrival (after 1.1–1.3): no command carries the annotation, so the
// policy leaves every command non-cancellable and the first SIGINT kills each
// pinned child instead of letting it return its code.

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

const (
	s058CancellableArgsEnv = "BENTOO_TEST_S058_CANCELLABLE_ARGS"
	s058CancellableMarkEnv = "BENTOO_TEST_S058_CANCELLABLE_MARKS"
	s058CancellableKey     = "bentoo/cancellable"
)

// cancellableCommands is the reviewable list R4.7 asks for: every command whose
// handler, or a helper it calls, called signalContext or built its own
// signal.NotifyContext at 6be73ec (20 sites in 20 handlers; overlay commit has
// two, in runCommit and commitOverlay).
func cancellableCommands() []string {
	return []string{
		"distfile fetch",
		"overlay add",
		"overlay analyze",
		"overlay autoupdate",
		"overlay commit",
		"overlay compare",
		"overlay manifest",
		"overlay prune",
		"overlay pull",
		"overlay push",
		"overlay staged clean",
		"overlay status",
		"overlay validate",
		"snapshot apply",
		"snapshot list",
		"snapshot prune",
		"snapshot restore",
		"snapshot rollback",
		"snapshot run",
		"snapshot status",
		// Main gained these after 6be73ec; both installed a signal context at
		// e051559, the main commit the branch integrates (R4.7).
		"notice new",
		"notice revise",
	}
}

// s058AnnotatedCommands walks the tree built by newRootCmd and returns every
// command path (below the root) whose cancellable annotation is set, with the
// raw value when it is not exactly "true".
func s058AnnotatedCommands() (annotated []string, oddValues []string) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if v, ok := c.Annotations[s058CancellableKey]; ok {
			path := strings.TrimPrefix(c.CommandPath(), "bentoo ")
			if c.Parent() == nil {
				path = "<root>"
			}
			if v == "true" {
				annotated = append(annotated, path)
			} else {
				oddValues = append(oddValues, path+"="+v)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd())
	sort.Strings(annotated)
	return annotated, oddValues
}

// TestS058CancellableSetMatchesTheTree (R4.7): the tree's cancellable set and
// cancellableCommands are the same set, compared in both directions.
func TestS058CancellableSetMatchesTheTree(t *testing.T) {
	want := map[string]bool{}
	for _, p := range cancellableCommands() {
		want[p] = true
	}
	annotated, odd := s058AnnotatedCommands()
	got := map[string]bool{}
	for _, p := range annotated {
		got[p] = true
	}

	// Would wrongly swallow a signal: annotated, but installed no signal
	// context at 6be73ec (a group command, the root, or a plain command).
	var extra []string
	for _, p := range annotated {
		if !want[p] {
			extra = append(extra, p)
		}
	}
	if len(extra) > 0 {
		t.Errorf("these commands are marked cancellable but installed no signal context at 6be73ec — a first Ctrl+C they used to die of would now be swallowed (R4.6, R4.7):\n  %s", strings.Join(extra, "\n  "))
	}
	// Would wrongly die: installed a signal context at 6be73ec, not annotated.
	var missing []string
	for _, p := range cancellableCommands() {
		if !got[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d of %d commands that installed a signal context at 6be73ec are not marked cancellable — a first Ctrl+C would now kill them mid-work instead of cancelling their context (R4.7):\n  %s",
			len(missing), len(cancellableCommands()), strings.Join(missing, "\n  "))
	}
	if len(odd) > 0 {
		t.Errorf("the %q annotation must read exactly \"true\"; these carry another value, which the policy reads as not cancellable:\n  %s", s058CancellableKey, strings.Join(odd, "\n  "))
	}
	// Every listed command must exist and be runnable in the real tree.
	root := newRootCmd()
	for _, p := range cancellableCommands() {
		cmd, _, err := root.Find(strings.Fields(p))
		if err != nil || cmd == nil || cmd.CommandPath() != "bentoo "+p || !cmd.Runnable() {
			t.Errorf("cancellableCommands names %q, which is not a runnable command of the tree (found %v, err %v)", p, cmd, err)
		}
	}
}

// TestS058CancellableHelperChild is not a test: in the child role it runs the
// real tree through the real main, with the arguments the parent passed.
func TestS058CancellableHelperChild(t *testing.T) {
	raw := os.Getenv(s058CancellableArgsEnv)
	if raw == "" {
		t.Skip("child role only")
	}
	os.Args = append([]string{"bentoo"}, strings.Split(raw, "\x1f")...)
	main()
	// A main that returns is a 0 exit in production (at 6be73ec a handler
	// that ended without osExit did exactly that); mirror it.
	os.Exit(0)
}

// s058CancellableTools are the external programs a command may wait on. Each
// is replaced by a stub that records its call, marks the run ready and sleeps,
// so the signal lands while the command waits on a child of its own.
var s058CancellableTools = []string{
	"git", "pkgdev", "pkgcheck", "claude", "snapper", "btrbk", "btrfs",
	"systemctl", "findmnt", "mount", "umount", "ebuild", "emerge", "curl",
	"wget", "ssh", "rsync", "sudo", "doas", "pkexec", "portageq", "equery",
	"qlist", "gh", "gemato",
	// The editor `notice new` and `notice revise` open: the fixture names it
	// in EDITOR, so the host's own VISUAL and EDITOR never reach a child.
	s058CancellableEditor,
}

// s058CancellableEditor is the stub editor the fixture sets as EDITOR.
const s058CancellableEditor = "s058-editor"

// s058CancellableFixture is one child's world: a HOME with a config, an overlay
// that is a git repository holding one package, a stub directory first on
// PATH, a local HTTP peer that never answers, and a proxy that never answers.
type s058CancellableFixture struct {
	home, overlay, marks string
	env                  []string
	server               *httptest.Server
}

func s058NewCancellableFixture(t *testing.T) *s058CancellableFixture {
	t.Helper()
	f := &s058CancellableFixture{home: t.TempDir(), marks: t.TempDir()}
	f.overlay = filepath.Join(f.home, "overlay")
	markReady := func(what string) {
		_ = os.WriteFile(filepath.Join(f.marks, "ready"), []byte(what), 0o600)
	}

	// A peer that accepts and never answers: the HTTP server for URLs the
	// fixture controls, the proxy for every other host a command reaches.
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f.server = httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		markReady("http " + r.URL.Path)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		f.server.CloseClientConnections()
		f.server.Close()
	})
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	go func() {
		for {
			conn, err := proxy.Accept()
			if err != nil {
				return
			}
			markReady("proxy")
			go func() {
				<-release
				_ = conn.Close()
			}()
		}
	}()

	// The stub tools.
	bin := t.TempDir()
	stub := "#!/bin/sh\n" +
		"echo \"$(basename \"$0\") $*\" >> \"$" + s058CancellableMarkEnv + "/calls\"\n" +
		"echo \"tool $(basename \"$0\")\" > \"$" + s058CancellableMarkEnv + "/ready\"\n" +
		"echo $$ >> \"$" + s058CancellableMarkEnv + "/pids\"\n" +
		// The stub lets go of the command's pipes, so a command killed by the
		// signal is seen to end without waiting for its orphaned stub.
		"exec sleep 60 </dev/null >/dev/null 2>&1\n"
	t.Cleanup(func() {
		// Stop every stub still sleeping, orphaned or not.
		b, _ := os.ReadFile(filepath.Join(f.marks, "pids"))
		for _, field := range strings.Fields(string(b)) {
			var pid int
			if _, err := fmt.Sscan(field, &pid); err == nil && pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	for _, tool := range s058CancellableTools {
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The overlay: a git repository (made with the real git, before the stubs
	// shadow it) holding one package, plus one uncommitted change.
	for _, dir := range []string{"profiles", "metadata", "app-misc/s058-one", "app-misc/s058-two"} {
		if err := os.MkdirAll(filepath.Join(f.overlay, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(f.overlay, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("profiles/repo_name", "s058\n")
	write("metadata/layout.conf", "masters = gentoo\n")
	write("app-misc/s058-one/s058-one-1.0.0.ebuild", "EAPI=8\nDESCRIPTION=\"s058\"\nHOMEPAGE=\"https://s058.invalid\"\n"+
		"SRC_URI=\""+f.server.URL+"/s058-one-1.0.0.tar.gz\"\nLICENSE=\"MIT\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n")
	gitEnv := append(os.Environ(), "HOME="+f.home, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = f.overlay, gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write("app-misc/s058-two/s058-two-2.0.0.ebuild", "EAPI=8\nDESCRIPTION=\"s058 two\"\nHOMEPAGE=\"https://s058.invalid\"\nSLOT=\"0\"\n")
	write("app-misc/s058-one/s058-one-1.0.1.ebuild", "EAPI=8\nDESCRIPTION=\"s058\"\nSLOT=\"0\"\n")

	// The registry autoupdate reads points at the peer that never answers.
	if err := os.MkdirAll(filepath.Join(f.overlay, ".autoupdate"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(".autoupdate/packages.toml", "[\"app-misc/s058-one\"]\nurl = \""+f.server.URL+"/version\"\nparser = \"json\"\npath = \"version\"\n")

	configDir := filepath.Join(f.home, ".config", "bentoo")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "overlay:\n  path: " + f.overlay + "\n  remote: origin\n" + "git:\n  user: Test\n  email: test@test.com\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	// The snapshot pins read this file through --config {home}/snapshot.toml.
	// The default search tries /etc/bentoo first, so without the flag a host
	// that has /etc/bentoo/snapshot.toml would feed its own config to the child
	// and a runner that has none would fail before any wait. Written from the
	// internal/snapshot schema: the snapper engine and the systemd schedule,
	// whose binaries (snapper, systemctl) are stubs on PATH, so Validate passes
	// and every snapshot command reaches a stub before the SIGINT.
	snapshotConfig := "[engine]\ndriver = \"snapper\"\nsubvolumes = [\"/\"]\nsnapshot_dir = \"/.snapshots\"\n\n" +
		"[engine.retention]\ndaily = 7\n\n" +
		"[schedule]\nbackend = \"systemd\"\non_calendar = \"daily\"\n"
	if err := os.WriteFile(filepath.Join(f.home, "snapshot.toml"), []byte(snapshotConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	proxyURL := "http://" + proxy.Addr().String()
	f.env = append(os.Environ(),
		s058CancellableMarkEnv+"="+f.marks,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+f.home,
		"XDG_CONFIG_HOME="+filepath.Join(f.home, ".config"),
		"XDG_STATE_HOME="+filepath.Join(f.home, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(f.home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(f.home, ".local", "share"),
		"HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL,
		"http_proxy="+proxyURL, "https_proxy="+proxyURL,
		"NO_PROXY=", "no_proxy=",
		"GIT_CONFIG_NOSYSTEM=1",
		"VISUAL=", "EDITOR="+s058CancellableEditor,
	)
	return f
}

// s058CancellableOutcome is how an interrupted child ended.
type s058CancellableOutcome struct {
	ready    string // what the child was waiting on when the signal was sent
	code     int
	signaled bool
	sig      syscall.Signal
	output   string
	// afterSignal is how long the child ran after its SIGINT was sent.
	afterSignal time.Duration
}

// s058InterruptChild starts the real tree with args in a re-exec'd child,
// waits until it waits on a stub tool or a network peer, sends one SIGINT and
// reports how the child ended. setup, when not nil, adds to the fixture's
// overlay what this one command needs before the child starts.
func s058InterruptChild(t *testing.T, args []string, setup func(t *testing.T, overlay string)) s058CancellableOutcome {
	t.Helper()
	f := s058NewCancellableFixture(t)
	if setup != nil {
		setup(t, f.overlay)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestS058CancellableHelperChild$", "-test.count=1")
	args = append([]string(nil), args...)
	for i := range args {
		args[i] = strings.ReplaceAll(args[i], "{home}", f.home)
	}
	// A copy, so the child's variable never lands in spare capacity of f.env.
	env := append([]string(nil), f.env...)
	env = append(env, s058CancellableArgsEnv+"="+strings.Join(args, "\x1f"))
	cmd.Env = env
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = f.overlay
	var out bytes.Buffer
	var mu sync.Mutex
	w := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return out.Write(p) })
	cmd.Stdout, cmd.Stderr = w, w
	output := func() string { mu.Lock(); defer mu.Unlock(); return out.String() }
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	})

	var ready string
	deadline := time.After(20 * time.Second)
poll:
	for {
		if b, err := os.ReadFile(filepath.Join(f.marks, "ready")); err == nil {
			ready = strings.TrimSpace(string(b))
			break poll
		}
		select {
		case <-done:
			t.Fatalf("bentoo %s ended before it waited on anything, so it could not be interrupted mid-work (exit %d):\n%s",
				strings.Join(args, " "), cmd.ProcessState.ExitCode(), output())
		case <-deadline:
			t.Fatalf("bentoo %s never waited on a stub tool or a network peer within 20s:\n%s", strings.Join(args, " "), output())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("signalling the child: %v", err)
	}
	signalled := time.Now()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("bentoo %s was still running 30s after its first SIGINT (waiting on %s):\n%s", strings.Join(args, " "), ready, output())
	}
	o := s058CancellableOutcome{ready: ready, output: output(), afterSignal: time.Since(signalled)}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		o.signaled, o.sig, o.code = true, ws.Signal(), -1
	} else {
		o.code = cmd.ProcessState.ExitCode()
	}
	return o
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// s058CancellablePin is one command interrupted by its first SIGINT: either the
// code it returned at 6be73ec, or killed (a command with no signal context).
type s058CancellablePin struct {
	command string // the command path, as in cancellableCommands
	args    []string
	want    int  // the exit code measured at the row's baseline (see baseline)
	killed  bool // measured at 6be73ec: terminated by the signal itself
	// within, when not zero, bounds the time from the SIGINT to the exit;
	// zero leaves only the harness's own 30 s deadline.
	within time.Duration
	// baseline names where want was measured; empty means 6be73ec
	// (e051559 for the notice commands).
	baseline string
	// setup, when not nil, prepares the overlay for this command alone.
	setup func(t *testing.T, overlay string)
}

// s058NoticeID is the news item s058WriteNoticeItem puts in the overlay.
const s058NoticeID = "2026-09-28-s058"

// s058WriteNoticeItem writes one published news item, the notice that
// `notice revise` opens in the editor.
func s058WriteNoticeItem(t *testing.T, overlay string) {
	t.Helper()
	dir := filepath.Join(overlay, "metadata", "news", s058NoticeID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	item := "Title: s058 notice\nAuthor: Test <test@test.com>\nPosted: 2026-09-28\nRevision: 1\n" +
		"News-Item-Format: 2.0\n\nThe notice text.\n"
	if err := os.WriteFile(filepath.Join(dir, s058NoticeID+".en.txt"), []byte(item), 0o644); err != nil {
		t.Fatal(err)
	}
}

func s058CancellablePins() []s058CancellablePin {
	return []s058CancellablePin{
		// Would wrongly survive: no signal context at 6be73ec, killed by the
		// first SIGINT while git runs.
		{command: "overlay log", args: []string{"overlay", "log"}, killed: true},
		{command: "overlay diff", args: []string{"overlay", "diff"}, killed: true},
		// Would wrongly die: each exits with its own interrupted code.
		{command: "overlay validate", args: []string{"overlay", "validate"}, want: 130},
		{command: "overlay autoupdate", args: []string{"overlay", "autoupdate", "--check", "--force"}, want: 2},
		{command: "snapshot status", args: []string{"snapshot", "status", "--config", "{home}/snapshot.toml"}, want: 0},
		{command: "distfile fetch", args: []string{"distfile", "fetch", "app-misc/s058-one"}, want: 1},
		{command: "overlay add", args: []string{"overlay", "add", "app-misc/s058-one"}, want: 1},
		{command: "overlay analyze", args: []string{"overlay", "analyze", "app-misc/s058-two"}, want: 1},
		{command: "overlay commit", args: []string{"overlay", "commit", "-m", "s058", "-y"}, want: 1},
		{command: "overlay manifest", args: []string{"overlay", "manifest", "--ui=plain"}, want: 1},
		{command: "overlay pull", args: []string{"overlay", "pull"}, want: 1},
		{command: "overlay push", args: []string{"overlay", "push"}, want: 1},
		{command: "overlay status", args: []string{"overlay", "status"}, want: 1},
		{command: "snapshot apply", args: []string{"snapshot", "apply", "--config", "{home}/snapshot.toml"}, want: 1},
		{command: "snapshot list", args: []string{"snapshot", "list", "--config", "{home}/snapshot.toml"}, want: 1},
		{command: "snapshot prune", args: []string{"snapshot", "prune", "--config", "{home}/snapshot.toml"}, want: 1},
		{command: "snapshot rollback", args: []string{"snapshot", "rollback", "1", "--yes", "--config", "{home}/snapshot.toml"}, want: 1},
		{command: "snapshot run", args: []string{"snapshot", "run", "--config", "{home}/snapshot.toml"}, want: 1},
		// Main gained these after 6be73ec; measured at e051559, each waiting
		// on the stub editor.
		{command: "notice new", args: []string{"notice", "new", "--type", "news", "--severity", "info",
			"--title", "s058", "--summary", "s058 summary.", "--name", "s058", "--published", "2026-09-28"}, want: 1},
		{command: "notice revise", args: []string{"notice", "revise", s058NoticeID}, want: 1, setup: s058WriteNoticeItem},
		// Pinned by story 070, each waiting on the repository registry
		// download: at 6be73ec both were still running 30 s after the SIGINT,
		// so the bound is what fails a fix that waits out the client timeout.
		{command: "overlay compare", args: []string{"overlay", "compare"}, want: 1, within: 5 * time.Second, baseline: "story 070"},
		{command: "overlay prune", args: []string{"overlay", "prune"}, want: 1, within: 5 * time.Second, baseline: "story 070"},
		// Not pinned, with the reason measured at 6be73ec:
		//   overlay staged clean — waits on no external program; with nothing
		//     staged it returns 0 before anything can be interrupted.
		//   snapshot restore — refuses before any wait without a configured
		//     ship entry ("no ship entry named ...").
		// Their annotation is still pinned by TestS058CancellableSetMatchesTheTree.
	}
}

// TestS058CancellableCommandsKeepTheirInterruptedCodes (R4.8, R4.6): each
// pinned command, interrupted by its first SIGINT while it waits mid-work,
// ends exactly as it did at 6be73ec. The hostile rows come first: commands
// with no signal context at 6be73ec must still die of the signal.
func TestS058CancellableCommandsKeepTheirInterruptedCodes(t *testing.T) {
	listed := map[string]bool{}
	for _, p := range cancellableCommands() {
		listed[p] = true
	}
	if signal.Ignored(syscall.SIGINT) {
		t.Skip("SIGINT is ignored in this process and so in its children: a first SIGINT cannot take its default action here")
	}
	for _, pin := range s058CancellablePins() {
		t.Run(pin.command, func(t *testing.T) {
			if pin.killed == listed[pin.command] {
				t.Fatalf("pin %q contradicts cancellableCommands: killed=%t but listed=%t", pin.command, pin.killed, listed[pin.command])
			}
			t.Parallel()
			o := s058InterruptChild(t, pin.args, pin.setup)
			label := "bentoo " + strings.Join(pin.args, " ")
			baseline := pin.baseline
			if baseline == "" {
				baseline = "6be73ec (e051559 for the notice commands)"
			}
			switch {
			case pin.killed && !o.signaled:
				t.Errorf("%s (waiting on %s) exited %d after its first SIGINT; at 6be73ec it installed no signal context and was killed by the signal (R4.6):\n%s", label, o.ready, o.code, o.output)
			case !pin.killed && o.signaled:
				t.Errorf("%s (waiting on %s) was killed by its first %v; at %s its signal context was cancelled and it exited %d (R4.7, R4.8):\n%s", label, o.ready, o.sig, baseline, pin.want, o.output)
			case !pin.killed && o.code != pin.want:
				t.Errorf("%s (waiting on %s) exited %d after its first SIGINT, want %d, the code measured at %s (R4.8):\n%s", label, o.ready, o.code, pin.want, baseline, o.output)
			case pin.killed && o.sig != syscall.SIGINT:
				t.Errorf("%s was killed by %v, want SIGINT", label, o.sig)
			case pin.within > 0 && o.afterSignal > pin.within:
				t.Errorf("%s (waiting on %s) exited %v after its first SIGINT, want within %v (S070-R5.2):\n%s", label, o.ready, o.afterSignal, pin.within, o.output)
			}
		})
	}
}
