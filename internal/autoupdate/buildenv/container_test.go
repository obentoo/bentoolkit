package buildenv

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

type recordedCommands struct {
	argv [][]string
	// rootless is what `podman info` answers for Host.Security.Rootless.
	rootless string
}

func (r *recordedCommands) seams(onPath []string, failInspect bool) ContainerSeams {
	return ContainerSeams{
		LookPath: func(name string) (string, error) {
			if slices.Contains(onPath, name) {
				return "/usr/bin/" + name, nil
			}
			return "", exec.ErrNotFound
		},
		ExecCommand: func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			r.argv = append(r.argv, append([]string{name}, arg...))
			if failInspect && len(arg) > 1 && arg[0] == "image" {
				return exec.CommandContext(ctx, "false")
			}
			if len(arg) > 0 && arg[0] == "info" {
				return exec.CommandContext(ctx, "echo", r.rootless)
			}
			return exec.CommandContext(ctx, "true")
		},
	}
}

func TestContainer_PodmanFirstAndNeverAPull(t *testing.T) {
	rec := &recordedCommands{}
	c, err := NewContainer(t.Context(), "", "bentoo/stage3:latest", rec.seams([]string{"docker", "podman"}, false))
	if err != nil {
		t.Fatal(err)
	}
	if c.runtime != RuntimePodman {
		t.Errorf("runtime = %s, want podman first", c.runtime)
	}
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	run := strings.Join(rec.argv[len(rec.argv)-1], " ")
	for _, want := range []string{"podman run", "--pull=never", "--rm", "bentoo/stage3:latest", "sleep infinity"} {
		if !strings.Contains(run, want) {
			t.Errorf("start %q lacks %q", run, want)
		}
	}

	c.Command(t.Context(), []string{"ebuild", "x.ebuild", "clean", "configure"}, []string{"DISTDIR=/d"}, "/staging")
	got := strings.Join(rec.argv[len(rec.argv)-1], " ")
	want := "podman exec --interactive --workdir /staging --env DISTDIR=/d " + c.name + " ebuild x.ebuild clean configure"
	if got != want {
		t.Errorf("exec = %q\nwant %q", got, want)
	}

	if err := c.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stop := strings.Join(rec.argv[len(rec.argv)-1], " "); !strings.HasPrefix(stop, "podman rm --force bentoo-build-") {
		t.Errorf("stop = %q, want a forced rm of the container", stop)
	}
	for _, a := range rec.argv {
		if slices.Contains(a, "pull") {
			t.Errorf("a pull was run: %v", a)
		}
	}
}

func TestContainer_AMissingImageSaysHowToLoadOne(t *testing.T) {
	rec := &recordedCommands{}
	_, err := NewContainer(t.Context(), "docker", "bentoo/stage3:latest", rec.seams([]string{"docker"}, true))
	if !errors.Is(err, ErrImageNotLoaded) || !strings.Contains(err.Error(), "docker load") {
		t.Fatalf("err = %v, want ErrImageNotLoaded naming `docker load`", err)
	}
}

func TestContainer_RuntimeAndImageAreRequired(t *testing.T) {
	rec := &recordedCommands{}
	if _, err := NewContainer(t.Context(), "", "", rec.seams([]string{"podman"}, false)); err == nil {
		t.Error("no image configured was accepted")
	}
	if _, err := NewContainer(t.Context(), "", "img", rec.seams(nil, false)); err == nil {
		t.Error("no runtime on PATH was accepted")
	}
	if _, err := NewContainer(t.Context(), "lxc", "img", rec.seams([]string{"lxc"}, false)); err == nil {
		t.Error("an unknown runtime was accepted")
	}
}

// TestContainer_OnlyRootlessPodmanGetsSysAdmin: Portage's network sandbox needs
// CAP_SYS_ADMIN, which is granted only where a user namespace bounds it.
func TestContainer_OnlyRootlessPodmanGetsSysAdmin(t *testing.T) {
	for _, tc := range []struct {
		name, runtime, rootless string
		want                    bool
	}{
		{"rootless podman", RuntimePodman, "true", true},
		{"rootful podman", RuntimePodman, "false", false},
		{"docker", RuntimeDocker, "true", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordedCommands{rootless: tc.rootless}
			c, err := NewContainer(t.Context(), tc.runtime, "img", rec.seams([]string{tc.runtime}, false))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			run := rec.argv[len(rec.argv)-1]
			if got := slices.Contains(run, "--cap-add=SYS_ADMIN,NET_ADMIN"); got != tc.want {
				t.Errorf("start %q: SYS_ADMIN granted = %v, want %v", run, got, tc.want)
			}
		})
	}
}
