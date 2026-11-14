package buildenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// The container runtimes the container backend drives through their CLI.
const (
	RuntimePodman = "podman"
	RuntimeDocker = "docker"
)

// ErrImageNotLoaded is returned when the configured image is not in the
// runtime's local store. bentoolkit never pulls or imports an image.
var ErrImageNotLoaded = errors.New("the build image is not loaded")

// ContainerSeams are the process seams of the container transport, so a test
// can see every argv it builds without a runtime installed.
type ContainerSeams struct {
	LookPath    func(string) (string, error)
	ExecCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd
}

func (s ContainerSeams) lookPath() func(string) (string, error) {
	if s.LookPath != nil {
		return s.LookPath
	}
	return exec.LookPath
}

func (s ContainerSeams) command() func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	if s.ExecCommand != nil {
		return s.ExecCommand
	}
	return exec.CommandContext
}

// Container is the container transport: one container per Start, from an image
// already loaded in Podman or Docker, reached with `<runtime> exec`.
type Container struct {
	runtime string
	image   string
	seams   ContainerSeams
	name    string
	// imageID is the id the runtime gave the image, read when it was checked.
	imageID string
	// sysAdmin grants the container CAP_SYS_ADMIN, which Portage needs to put
	// a build in its own network, IPC and PID namespaces, and CAP_NET_ADMIN,
	// which brings the loopback up inside them. Only rootless Podman
	// gets it: there the capability is bounded by the user namespace, while a
	// rootful runtime (Docker's daemon, Podman run as root) would hand it over
	// against the host.
	sysAdmin bool
}

// containerSeq numbers the containers this process starts.
var containerSeq atomic.Int64

// NewContainer resolves runtime ("" picks Podman, then Docker) and checks that
// image is loaded there. It pulls nothing: a missing image is an error that
// says how to load one.
func NewContainer(ctx context.Context, runtime, image string, seams ContainerSeams) (*Container, error) {
	if strings.TrimSpace(image) == "" {
		return nil, errors.New("no build image is configured: set autoupdate.build.container.image to an image loaded in Podman or Docker")
	}
	resolved, err := resolveRuntime(runtime, seams.lookPath())
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	inspect := seams.command()(ctx, resolved, "image", "inspect", "--format", "{{.Id}}", image)
	inspect.Stdout, inspect.Stderr = &out, &out
	if err := inspect.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s has no image %q (bentoolkit never pulls one; load it with `%s load -i <file>`): %w",
			ErrImageNotLoaded, resolved, image, resolved, err)
	}
	return &Container{runtime: resolved, image: image, imageID: strings.TrimSpace(out.String()), seams: seams,
		sysAdmin: rootlessPodman(ctx, resolved, seams)}, nil
}

// rootlessPodman reports whether runtime is Podman running without root. Any
// doubt (Docker, an error, an unexpected answer) is a no.
func rootlessPodman(ctx context.Context, runtime string, seams ContainerSeams) bool {
	if runtime != RuntimePodman {
		return false
	}
	out, err := seams.command()(ctx, runtime, "info", "--format", "{{.Host.Security.Rootless}}").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func resolveRuntime(runtime string, lookPath func(string) (string, error)) (string, error) {
	switch runtime {
	case "":
		for _, candidate := range []string{RuntimePodman, RuntimeDocker} {
			if _, err := lookPath(candidate); err == nil {
				return candidate, nil
			}
		}
		return "", errors.New("neither podman nor docker is on PATH, so the container backend cannot run")
	case RuntimePodman, RuntimeDocker:
		if _, err := lookPath(runtime); err != nil {
			return "", fmt.Errorf("%s is not on PATH: %w", runtime, err)
		}
		return runtime, nil
	}
	return "", fmt.Errorf("unknown container runtime %q: want %s or %s", runtime, RuntimePodman, RuntimeDocker)
}

// Name is "container".
func (c *Container) Name() string { return "container" }

// Identity names the image and the id the runtime gave it.
func (c *Container) Identity() string {
	if c.imageID == "" {
		return c.image
	}
	return c.image + " (" + c.imageID + ")"
}

// Start runs a new detached container that only sleeps; every command then
// runs in it through exec. --pull=never keeps the no-pull promise, --rm
// removes the container once it is stopped, and rootless Podman adds
// CAP_SYS_ADMIN and CAP_NET_ADMIN so Portage's network sandbox works inside it.
func (c *Container) Start(ctx context.Context) error {
	c.name = "bentoo-build-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(containerSeq.Add(1), 10)
	var out bytes.Buffer
	args := []string{"run", "--detach", "--rm", "--pull=never", "--name", c.name}
	if c.sysAdmin {
		args = append(args, "--cap-add=SYS_ADMIN,NET_ADMIN")
	}
	args = append(args, c.image, "sleep", "infinity")
	cmd := c.seams.command()(ctx, c.runtime, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s run %s: %w%s", c.runtime, c.image, err, tail(out.Bytes()))
	}
	return nil
}

// Command runs argv in the container as root, in dir, with env.
func (c *Container) Command(ctx context.Context, argv, env []string, dir string) *exec.Cmd {
	args := []string{"exec", "--interactive"}
	if dir != "" {
		args = append(args, "--workdir", dir)
	}
	for _, kv := range env {
		args = append(args, "--env", kv)
	}
	args = append(args, c.name)
	args = append(args, argv...)
	return c.seams.command()(ctx, c.runtime, args...)
}

// stopTimeout bounds the removal of a container.
const stopTimeout = 30 * time.Second

// Stop removes the container. It outlives the cancellation of ctx, because it
// is also what an interrupted run calls on its way out, and is bounded by
// stopTimeout instead.
func (c *Container) Stop(ctx context.Context) error {
	if c.name == "" {
		return nil
	}
	name := c.name
	c.name = ""
	var out bytes.Buffer
	rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()
	cmd := c.seams.command()(rmCtx, c.runtime, "rm", "--force", name)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s rm %s: %w%s", c.runtime, name, err, tail(out.Bytes()))
	}
	return nil
}
