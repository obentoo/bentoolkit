package buildenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ErrNoISO is returned when Shidashi has no ISO to boot (its exit code 3).
var ErrNoISO = errors.New("shidashi has no ISO to boot")

// shidashiNoISO is the exit code of `shidashi vm start --dev` with no ISO.
const shidashiNoISO = 3

// VMSeams are the process seams of the VM transport.
type VMSeams struct {
	LookPath    func(string) (string, error)
	ExecCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd
}

func (s VMSeams) lookPath() func(string) (string, error) {
	if s.LookPath != nil {
		return s.LookPath
	}
	return exec.LookPath
}

func (s VMSeams) command() func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	if s.ExecCommand != nil {
		return s.ExecCommand
	}
	return exec.CommandContext
}

// VM is the virtual-machine transport: one disposable Shidashi build VM per
// Start (`shidashi vm start --dev`, which boots on a fresh work disk), reached
// with `shidashi vm run`. With a worker, Shidashi boots it on that paired build
// worker and relays every `vm run` and `vm stop` there over ssh.
type VM struct {
	shidashi string
	iso      string
	worker   string
	seams    VMSeams
	name     string
	identity string
}

// vmSeq numbers the VMs this process starts.
var vmSeq atomic.Int64

// NewVM resolves the shidashi binary and checks that it can start a build VM.
// iso is optional: unset, Shidashi boots its newest minimal ISO. worker is
// optional: unset, the VM runs where Shidashi decides (this host, or the worker
// SHIDASHI_DEV_WORKER names); set, it runs on that paired worker.
func NewVM(ctx context.Context, shidashi, iso, worker string, seams VMSeams) (*VM, error) {
	if strings.TrimSpace(shidashi) == "" {
		shidashi = "shidashi"
	}
	resolved, err := seams.lookPath()(shidashi)
	if err != nil {
		return nil, fmt.Errorf("%s is not on PATH, so the vm backend cannot run: %w", shidashi, err)
	}
	if iso != "" {
		if iso, err = filepath.Abs(iso); err != nil {
			return nil, fmt.Errorf("resolving the build ISO path: %w", err)
		}
		if info, err := os.Stat(iso); err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: %s is not a readable ISO file", ErrNoISO, iso)
		}
	}
	var out bytes.Buffer
	help := seams.command()(ctx, resolved, "vm", "start", "--help")
	help.Stdout, help.Stderr = &out, &out
	if err := help.Run(); err != nil {
		return nil, fmt.Errorf("asking %s what vm start supports: %w%s", resolved, err, tail(out.Bytes()))
	}
	if !strings.Contains(out.String(), "--dev") {
		return nil, fmt.Errorf("%s has no `vm start --dev`, the build VM the vm backend needs: update Shidashi", resolved)
	}
	worker = strings.TrimSpace(worker)
	if worker != "" && !strings.Contains(out.String(), "--worker") {
		return nil, fmt.Errorf("%s has no `vm start --worker`, which autoupdate.build.vm.worker needs: update Shidashi to 0.4.0 or newer", resolved)
	}
	return &VM{shidashi: resolved, iso: iso, worker: worker, seams: seams}, nil
}

// Name is "vm".
func (v *VM) Name() string { return "vm" }

// Identity names the ISO the VM booted, the tree pin it carries and, when it
// ran on a build worker, that worker.
func (v *VM) Identity() string { return v.identity }

// vmStarted is the JSON line `vm start --dev` prints.
type vmStarted struct {
	Name  string `json:"name"`
	ISO   string `json:"iso"`
	Pin   string `json:"pin"`
	Ready bool   `json:"ready"`
	// Worker is set when Shidashi ran the VM on a paired build worker, whether
	// --worker or SHIDASHI_DEV_WORKER chose it.
	Worker string `json:"worker"`
}

// Start boots a new build VM and waits until it answers.
func (v *VM) Start(ctx context.Context) error {
	v.name = "bentoo-build-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(vmSeq.Add(1), 10)
	args := []string{"vm", "start", "--dev", "--name", v.name}
	if v.iso != "" {
		args = append(args, "--iso", v.iso)
	}
	if v.worker != "" {
		args = append(args, "--worker", v.worker)
	}
	var stdout, stderr bytes.Buffer
	cmd := v.seams.command()(ctx, v.shidashi, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == shidashiNoISO {
			return fmt.Errorf("%w: %w%s", ErrNoISO, err, tail(stderr.Bytes()))
		}
		return fmt.Errorf("shidashi vm start --dev: %w%s", err, tail(append(stderr.Bytes(), stdout.Bytes()...)))
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var started vmStarted
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &started); err != nil {
		return fmt.Errorf("reading what shidashi vm start --dev printed: %w%s", err, tail(stdout.Bytes()))
	}
	if !started.Ready {
		return fmt.Errorf("shidashi started %s but did not report it ready", v.name)
	}
	v.identity = filepath.Base(started.ISO)
	if started.Pin != "" {
		v.identity += " (" + started.Pin + ")"
	}
	if started.Worker != "" {
		v.identity += " on worker " + started.Worker
	}
	return nil
}

// Command runs argv in the VM as root, in dir, with env as its whole
// environment. Shidashi hands the guest's shell one command line, so every word
// is quoted here and the guest sees the argv unchanged. No timeout: a
// legitimate build runs for hours, and ctx is what stops it.
func (v *VM) Command(ctx context.Context, argv, env []string, dir string) *exec.Cmd {
	var line strings.Builder
	if dir != "" {
		line.WriteString("cd " + shellQuote(dir) + " && ")
	}
	line.WriteString("exec env -i")
	for _, kv := range env {
		line.WriteString(" " + shellQuote(kv))
	}
	if !hasKey(env, "PATH") {
		line.WriteString(" " + shellQuote("PATH="+guestPath))
	}
	for _, a := range argv {
		line.WriteString(" " + shellQuote(a))
	}
	return v.seams.command()(ctx, v.shidashi, "vm", "run", "--name", v.name, "--stream", "--timeout", "0", "--", line.String())
}

// guestPath is the PATH a command gets in the guest when its environment names
// none: `env -i` clears the one the guest's shell had.
const guestPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// vmStopTimeout bounds the power-off of a VM.
const vmStopTimeout = 2 * time.Minute

// Stop powers the VM off. It outlives the cancellation of ctx, because it is
// also what an interrupted run calls on its way out, and is bounded by
// vmStopTimeout instead.
func (v *VM) Stop(ctx context.Context) error {
	if v.name == "" {
		return nil
	}
	name := v.name
	v.name = ""
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), vmStopTimeout)
	defer cancel()
	var out bytes.Buffer
	cmd := v.seams.command()(stopCtx, v.shidashi, "vm", "stop", "--name", name)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("shidashi vm stop --name %s: %w%s", name, err, tail(out.Bytes()))
	}
	return nil
}

// shellQuote quotes s as one word for a POSIX shell.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, needsQuoting) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// needsQuoting reports whether r has a meaning to the shell, or may have one.
func needsQuoting(r rune) bool {
	safe := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-./=:,+@%", r)
	return !safe
}

func hasKey(env []string, key string) bool {
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k == key {
			return true
		}
	}
	return false
}
