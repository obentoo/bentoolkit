package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/autoupdate/buildenv"
	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
)

// buildEnvironment is a build backend that is a separate machine: the files a
// build reads must be sent in, its dependencies installed there, and its
// commands moved there once their environment is set. buildenv.Session is one.
type buildEnvironment interface {
	Prepare(ctx context.Context, req buildenv.PrepareRequest) error
	Adapt(ctx context.Context, cmd *exec.Cmd) *exec.Cmd
	Fresh(ctx context.Context) error
	LastPassFresh() bool
	Isolated(ctx context.Context) (bool, string)
	Alive(ctx context.Context) bool
	Identity() string
}

// environment is the build backend when it is a separate machine, nil on the
// host.
func (a *Applier) environment() buildEnvironment {
	env, _ := a.buildBackend.(buildEnvironment)
	return env
}

// prepareEnvironment readies a fresh environment for one build of the staged
// candidate: the tree and its distfiles shipped in, the staged repository
// registered, the build dependencies installed. Every pass starts fresh, so
// the pass whose verdict is recorded never inherits an earlier build's disk.
func (a *Applier) prepareEnvironment(ctx context.Context, env buildEnvironment, cand candidatePaths, pkg, version string) error {
	if err := env.Fresh(ctx); err != nil {
		return err
	}
	name, section, err := validate.StagedRepoSection(cand.repoRoot, pkg, version)
	if err != nil {
		return fmt.Errorf("%w: naming the staged repository: %w", buildenv.ErrEnvironment, err)
	}
	category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return fmt.Errorf("%w: %q does not name category/package", buildenv.ErrEnvironment, pkg)
	}
	distfilesNeeded, err := a.environmentDistfiles(cand)
	if err != nil {
		return fmt.Errorf("%w: %w", buildenv.ErrEnvironment, err)
	}
	return env.Prepare(ctx, buildenv.PrepareRequest{
		StagedRoot:  cand.repoRoot,
		Distfiles:   distfilesNeeded,
		RepoName:    name,
		RepoSection: section,
		Atom:        "=" + category + "/" + pkgName + "-" + version + "::" + name,
	})
}

// environmentDistfiles names the archives the staged Manifest declares, in the
// directory the gates read archives from. A declared archive missing here is
// left out: the build's own fetch then reports it, as it would on this host.
func (a *Applier) environmentDistfiles(cand candidatePaths) ([]string, error) {
	body, err := os.ReadFile(filepath.Join(cand.pkgDir, "Manifest"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the staged Manifest: %w", err)
	}
	dir := a.staticGateDistdir(cand)
	if dir == "" {
		return nil, nil
	}
	var paths []string
	for _, name := range distfiles.ManifestDistFilenames(body) {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() { //nolint:gosec // G703: ManifestDistFilenames returns base names only, so p stays in dir
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// adaptBuildChild moves a build child into the environment when there is one.
func (a *Applier) adaptBuildChild(ctx context.Context, cmd *exec.Cmd) *exec.Cmd {
	if env := a.environment(); env != nil {
		return env.Adapt(ctx, cmd)
	}
	return cmd
}

// lastPassFresh says whether the last build ran in an environment that had
// built nothing before it. Always false on the host.
func (a *Applier) lastPassFresh() bool {
	env := a.environment()
	return env != nil && env.LastPassFresh()
}

// lockEnvironment takes the environment's build lock and returns its release.
// An applier built as a struct literal has none and builds unserialised.
func (a *Applier) lockEnvironment() func() {
	if a.envMu == nil {
		return func() {}
	}
	a.envMu.Lock()
	return a.envMu.Unlock
}

// recordedEnvironment names what the gates ran against, for the proof record:
// the environment's image or ISO, or this host's ::gentoo sync time.
func (a *Applier) recordedEnvironment() string {
	if env := a.environment(); env != nil {
		return env.Identity()
	}
	return hostEnvironment(a.gentooPath)
}

// buildFaultEvidenceFor is the free evidence about a failed build: its
// transcript and, when it ran in a separate environment, whether that
// environment still answers.
func (a *Applier) buildFaultEvidenceFor(ctx context.Context, first buildAttempt) buildFaultEvidence {
	ev := buildFaultEvidence{transcript: first.transcript}
	if env := a.environment(); env != nil {
		ev.environmentLost = !env.Alive(ctx)
	}
	return ev
}
