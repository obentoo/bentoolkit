package validate

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
)

// The host sandbox: on the host backend, `ebuild` runs inside bubblewrap.
//
// A build runs code the bump brought in (configure scripts, build systems,
// test suites) as the invoking user. Without a sandbox that code can read
// everything the user can: ~/.ssh, the bentoo secrets, a release signing key,
// and reach the network and the user's sockets (docker.sock is root). The
// sandbox gives it a read-only view of the system with every place a user
// keeps secrets replaced by an empty tmpfs, and no network, IPC or PID
// namespace shared with the host. Only the staged tree and the distdir are
// visible (read-only) and one private PORTAGE_TMPDIR is writable.

// sandboxBinary is bubblewrap's executable name.
const sandboxBinary = "bwrap"

// sandboxProbeTimeout bounds the one probe run per process.
const sandboxProbeTimeout = 10 * time.Second

// sandboxHiddenDirs are replaced by an empty tmpfs inside the sandbox. They
// are where a user's secrets, other builds' work directories and the host's
// sockets live: /home (every user's files, the workspace and its keys), /root,
// /tmp and /var/tmp (other builds and stray files), /run (ssh-agent,
// gpg-agent, D-Bus, docker.sock, the DNS resolver's socket) and
// /etc/bentoo (the system-wide secrets file).
var sandboxHiddenDirs = []string{"/home", "/root", "/tmp", "/var/tmp", "/run", "/etc/bentoo"}

// sandboxBaseArgs are the bubblewrap options shared by the probe and every
// build: new user, mount, network, IPC, PID, UTS and cgroup namespaces, a
// new session (no TIOCSTI into the terminal), and the child dies with bentoo.
var sandboxBaseArgs = []string{
	"--die-with-parent", "--new-session", "--unshare-all",
	"--ro-bind", "/", "/",
	"--dev", "/dev",
	"--proc", "/proc",
}

// sandboxArgs returns the bubblewrap argv (without the binary) for a build
// that may read readOnly paths, write tmpdir, starts in dir, and runs name
// with arg. A hidden directory the host lacks is not mounted, and a read-only
// path that does not exist is skipped by bwrap's -try variant.
func sandboxArgs(readOnly []string, tmpdir, dir, name string, arg []string) []string {
	args := append([]string(nil), sandboxBaseArgs...)
	for _, dir := range sandboxHiddenDirs {
		if _, err := os.Stat(dir); err == nil {
			args = append(args, "--tmpfs", dir)
		}
	}
	for _, p := range readOnly {
		if p != "" {
			args = append(args, "--ro-bind-try", p, p)
		}
	}
	args = append(args, "--bind", tmpdir, tmpdir, "--chdir", dir, "--")
	return append(append(args, name), arg...)
}

// sandboxEnv returns env adapted to the sandbox: the private PORTAGE_TMPDIR,
// Portage's own user and group set to the invoking user's (the sandbox's user
// namespace maps no other, so Portage's chown to `portage` would fail), and
// ccache disabled, because a writable shared cache would let the build plant
// objects a later, real build links.
func sandboxEnv(env []string, tmpdir, userName, groupName string) []string {
	out := make([]string, 0, len(env)+4)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "PORTAGE_TMPDIR", "PORTAGE_USERNAME", "PORTAGE_GRPNAME":
			continue
		}
		out = append(out, kv)
	}
	out = append(out,
		"PORTAGE_TMPDIR="+tmpdir,
		"PORTAGE_USERNAME="+userName,
		"PORTAGE_GRPNAME="+groupName,
	)
	return withFeatureDisabled(out, "ccache")
}

// sandboxIdentity returns the invoking user's user and group names.
func sandboxIdentity() (string, string, error) {
	u, err := user.Current()
	if err != nil {
		return "", "", fmt.Errorf("look up the invoking user: %w", err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		return "", "", fmt.Errorf("look up group %s of user %s: %w", u.Gid, u.Username, err)
	}
	return u.Username, g.Name, nil
}

// sandboxTmpRoot is where the private PORTAGE_TMPDIR is created: root when the
// caller resolved one, else the host's own PORTAGE_TMPDIR, else /var/tmp,
// Portage's default. Never os.TempDir(): /tmp is often a small tmpfs.
func sandboxTmpRoot(ctx context.Context, root string) string {
	if root != "" {
		return root
	}
	if host := distfiles.TempRoot(ctx); host != "" {
		return host
	}
	return "/var/tmp"
}

var (
	sandboxProbeOnce   sync.Once
	sandboxProbeOK     bool
	sandboxProbeReason string
)

// ProbeHostSandbox reports whether this host can run a build in the sandbox,
// and why not when it cannot. It runs bubblewrap once per process with the
// same namespaces a build uses, because bwrap being installed says nothing
// about whether unprivileged user namespaces are allowed here.
func ProbeHostSandbox() (bool, string) {
	sandboxProbeOnce.Do(func() {
		sandboxProbeOK, sandboxProbeReason = probeHostSandbox()
	})
	return sandboxProbeOK, sandboxProbeReason
}

func probeHostSandbox() (bool, string) {
	if _, err := exec.LookPath(sandboxBinary); err != nil {
		return false, "bubblewrap (bwrap) is not installed"
	}
	// SAFE: a once-per-process probe whose answer is cached; bound by its own
	// timeout, never by one request's context, whose cancellation would cache a
	// false "no sandbox" for the rest of the run.
	ctx, cancel := context.WithTimeout(context.Background(), sandboxProbeTimeout) // SAFE: see above
	defer cancel()
	args := append(append([]string(nil), sandboxBaseArgs...), "--", "true")
	//nolint:gosec // G204: a constant binary and a fixed argument list, never a shell
	out, err := exec.CommandContext(ctx, sandboxBinary, args...).CombinedOutput()
	if err != nil {
		return false, fmt.Sprintf("bubblewrap could not create its namespaces: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return true, ""
}

// buildSandbox is one build's private PORTAGE_TMPDIR and the identity
// Portage runs as inside the sandbox.
type buildSandbox struct {
	tmpdir, user, group string
}

// newBuildSandbox creates the private PORTAGE_TMPDIR under root (see
// sandboxTmpRoot) and resolves the invoking user's names.
func newBuildSandbox(ctx context.Context, root string) (*buildSandbox, error) {
	userName, groupName, err := sandboxIdentity()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(sandboxTmpRoot(ctx, root), "bentoo-sandbox-")
	if err != nil {
		return nil, fmt.Errorf("create the sandbox's build directory: %w", err)
	}
	return &buildSandbox{tmpdir: dir, user: userName, group: groupName}, nil
}

// remove deletes the build directory. A build may leave directories without
// write permission, which os.RemoveAll cannot descend into, so permissions are
// restored first. Removal is best-effort: a leftover directory under the tmp
// root is litter, never a wrong verdict.
func (s *buildSandbox) remove() {
	_ = filepath.WalkDir(s.tmpdir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700) //nolint:gosec // G302: a private directory this process created
		}
		return nil
	})
	_ = os.RemoveAll(s.tmpdir)
}

// sandboxRefusedReason words a build refused because the sandbox is missing.
func sandboxRefusedReason(pv, reason string) string {
	return fmt.Sprintf("the build of %s was not run: building on the host backend requires the bubblewrap sandbox, "+
		"which keeps the build away from your files, secrets and network, and %s; "+
		"install sys-apps/bubblewrap, or build in --build-backend container or vm", pv, reason)
}
