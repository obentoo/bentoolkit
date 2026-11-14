package buildenv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// PrepareRequest is what one build needs inside the environment.
type PrepareRequest struct {
	// StagedRoot is the staged repository; it is shipped whole.
	StagedRoot string
	// Distfiles are the absolute paths of the archives the build reads.
	Distfiles []string
	// RepoName and RepoSection register StagedRoot in the environment's
	// repos.conf under the name Stage gave it.
	RepoName, RepoSection string
	// Atom is the exact candidate, "=cat/pkg-version::repo", whose build
	// dependencies are installed before any gate runs.
	Atom string
}

// Session is one environment for one bentoo run: started at the first build,
// started afresh before a verdict when it has already built, and stopped at
// the end. It satisfies validate.BuildBackend; the build children are moved
// into the environment by Adapt, after their environment and directory are
// set, so Wrap leaves the argv alone.
type Session struct {
	t Transport

	mu      sync.Mutex
	running bool
	// builds counts the build children Adapt sent in since the last Start.
	builds int
	// lastFresh says whether the last build child ran in an environment that
	// had built nothing before it.
	lastFresh bool
}

// NewSession wraps t. Nothing starts until the first Prepare.
func NewSession(t Transport) *Session { return &Session{t: t} }

// Name is the transport's name.
func (s *Session) Name() string { return s.t.Name() }

// Wrap leaves the command as it is: Adapt moves it into the environment once
// its environment and directory are known.
func (s *Session) Wrap(name string, arg []string) (string, []string) { return name, arg }

// Prepare starts the environment if it is not running, ships the staged tree
// and the distfiles in, registers the staged repository, and installs the
// candidate's build dependencies. Every failure wraps ErrEnvironment.
func (s *Session) Prepare(ctx context.Context, req PrepareRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.startLocked(ctx); err != nil {
		return err
	}
	ship := append([]string{req.StagedRoot}, req.Distfiles...)
	if err := s.shipLocked(ctx, ship); err != nil {
		return err
	}
	conf := "/etc/portage/repos.conf/" + req.RepoName + ".conf"
	if out, err := s.runLocked(ctx, []string{"sh", "-c", `mkdir -p /etc/portage/repos.conf && cat > "$1"`, "sh", conf},
		strings.NewReader(req.RepoSection)); err != nil {
		return fmt.Errorf("%w: registering the staged repository %s: %w%s", ErrEnvironment, req.RepoName, err, tail(out))
	}
	// --with-bdeps=y because --onlydeps alone leaves the build dependencies
	// out, and those are the ones a build gate needs.
	if out, err := s.runLocked(ctx, []string{"emerge", "-1", "--onlydeps", "--with-bdeps=y", "--getbinpkg",
		"--quiet", "--color=n", req.Atom}, nil); err != nil {
		return fmt.Errorf("%w: installing the build dependencies of %s: %w%s", ErrEnvironment, req.Atom, err, tail(out))
	}
	return nil
}

// Adapt returns cmd moved into the environment: the same argv, directory,
// standard streams and process attributes, with the environment variables the
// gate set and none of this host's own (PATH, HOME and the like).
func (s *Session) Adapt(ctx context.Context, cmd *exec.Cmd) *exec.Cmd {
	s.mu.Lock()
	s.builds++
	s.lastFresh = s.builds == 1
	s.mu.Unlock()

	argv := append([]string(nil), cmd.Args...)
	if len(argv) == 0 {
		argv = []string{cmd.Path}
	}
	adapted := s.t.Command(ctx, argv, gateEnv(cmd.Env), cmd.Dir)
	adapted.Stdin, adapted.Stdout, adapted.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	adapted.SysProcAttr = cmd.SysProcAttr
	adapted.WaitDelay = cmd.WaitDelay
	return adapted
}

// Isolated measures, inside the environment, what validate.ProbeIsolation
// measures on this host: whether a build there can be put in its own network,
// IPC and PID namespaces. The host's answer says nothing about a container
// that runs without the capability.
func (s *Session) Isolated(ctx context.Context) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return false, "the " + s.t.Name() + " environment is not running"
	}
	out, err := s.runLocked(ctx, []string{"unshare", "--net", "--ipc", "--pid", "--fork", "true"}, nil)
	if err != nil {
		return false, fmt.Sprintf("could not create a network namespace in the %s environment: %v%s", s.t.Name(), err, tail(out))
	}
	return true, ""
}

// Fresh replaces an environment that has already built with a new one, so the
// pass whose verdict is recorded starts from a clean disk. An environment that
// has built nothing is fresh already and is left alone.
func (s *Session) Fresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.builds == 0 {
		return nil
	}
	if err := s.t.Stop(ctx); err != nil {
		return fmt.Errorf("%w: stopping the used environment: %w", ErrEnvironment, err)
	}
	s.running = false
	return s.startLocked(ctx)
}

// LastPassFresh reports whether the last build child ran in an environment
// that had built nothing before it.
func (s *Session) LastPassFresh() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastFresh
}

// Close stops the environment. It is safe to call when nothing started, and
// twice.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return nil
	}
	s.running = false
	if err := s.t.Stop(ctx); err != nil {
		return fmt.Errorf("stopping the %s build environment: %w", s.t.Name(), err)
	}
	return nil
}

func (s *Session) startLocked(ctx context.Context) error {
	if s.running {
		return nil
	}
	if err := s.t.Start(ctx); err != nil {
		return fmt.Errorf("%w: starting the %s environment: %w", ErrEnvironment, s.t.Name(), err)
	}
	s.running, s.builds = true, 0
	return nil
}

// shipLocked sends paths into the environment by tar, at the same absolute
// paths. Sent rather than shared, so an agent's edit on this host never reaches
// a build through a stale shared mount.
func (s *Session) shipLocked(ctx context.Context, paths []string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeTar(pw, paths)) }()
	out, err := s.runLocked(ctx, []string{"tar", "-C", "/", "-xf", "-"}, pr)
	_ = pr.Close()
	if err != nil {
		return fmt.Errorf("%w: shipping %s: %w%s", ErrEnvironment, strings.Join(paths, ", "), err, tail(out))
	}
	return nil
}

func (s *Session) runLocked(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	cmd := s.t.Command(ctx, argv, nil, "/")
	cmd.Stdin = stdin
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}

// gateEnv keeps the variables a gate set for the build and drops this host's
// own, which mean nothing inside the environment.
func gateEnv(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		switch {
		case key == "PATH", key == "HOME", key == "USER", key == "LOGNAME", key == "SHELL", key == "PWD",
			key == "TERM", strings.HasPrefix(key, "XDG_"):
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

// tail is the last lines of a command's output, for an error message.
func tail(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return ""
	}
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return "\n" + strings.Join(lines, "\n")
}
