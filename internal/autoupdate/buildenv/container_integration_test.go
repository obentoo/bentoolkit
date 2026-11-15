package buildenv_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/buildenv"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// containerImageEnv names a Gentoo image already loaded in Podman or Docker.
// The test is skipped without it, so the suite never needs a runtime or an
// image; with it, the container backend is proven against a real Portage.
const containerImageEnv = "BENTOO_TEST_CONTAINER_IMAGE"

// TestContainerIntegration_TheBuildGatesRunInARealContainer drives the real
// transport and the real build gates: a staged tree and its distfile are
// shipped in, the staged repository is registered, the build dependencies are
// installed by emerge, and one ebuild run up to install happens inside the
// container. Nothing is mocked. Under rootless Podman the container can
// isolate the build, so Portage's sandbox must work and no pass may read as
// unverified.
func TestContainerIntegration_TheBuildGatesRunInARealContainer(t *testing.T) {
	image := os.Getenv(containerImageEnv)
	if image == "" {
		t.Skipf("%s is not set: no image to run the container backend against", containerImageEnv)
	}
	ctx := t.Context()
	transport, err := buildenv.NewContainer(ctx, os.Getenv("BENTOO_TEST_CONTAINER_RUNTIME"), image, buildenv.ContainerSeams{})
	if err != nil {
		t.Fatal(err)
	}
	session := buildenv.NewSession(transport)
	t.Cleanup(func() {
		if err := session.Close(context.Background()); err != nil {
			t.Errorf("closing the environment: %v", err)
		}
	})

	stagedRoot, distdir := probeTree(t)
	name, section, err := validate.StagedRepoSection(stagedRoot, "app-misc/bentoo-probe", "1")
	if err != nil {
		t.Fatal(err)
	}
	err = session.Prepare(ctx, buildenv.PrepareRequest{
		StagedRoot:  stagedRoot,
		Distfiles:   []string{filepath.Join(distdir, probeArchive)},
		RepoName:    name,
		RepoSection: section,
		Atom:        "=app-misc/bentoo-probe-1::" + name,
	})
	if err != nil {
		t.Fatalf("preparing the environment: %v", err)
	}

	var ranIn []string
	gates, err := validate.RunBuildGates(ctx, validate.BuildRequest{
		StagedRoot: stagedRoot,
		Key:        "app-misc/bentoo-probe",
		Version:    "1",
		Depth:      validate.DepthInstall,
		LogDir:     t.TempDir(),
		Distdir:    distdir,
	}, validate.BuildDeps{
		IsolationProbe: func() (bool, string) { return session.Isolated(ctx) },
		RunAttached: func(cmd *exec.Cmd) ([]byte, error) {
			cmd = session.Adapt(ctx, cmd)
			ranIn = append(ranIn, strings.Join(cmd.Args, " "))
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()
			t.Logf("build transcript:\n%s", out.String())
			if strings.Contains(out.String(), "Unable to unshare") || strings.Contains(out.String(), "Unable to configure loopback") {
				t.Error("Portage could not create its sandbox namespaces in the container")
			}
			return out.Bytes(), err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ranIn) != 1 || !strings.Contains(ranIn[0], " exec ") {
		t.Fatalf("want one build child moved into the container, ran %q", ranIn)
	}
	for _, g := range gates {
		if g.Outcome != validate.OutcomePass {
			t.Errorf("gate %s = %s: %s", g.Gate, g.Outcome, g.Reason)
		}
		if isolated, _ := session.Isolated(ctx); isolated && strings.Contains(g.Reason, "unverified isolation") {
			t.Errorf("gate %s is labelled unverified in an isolated environment: %s", g.Gate, g.Reason)
		}
	}
	if !session.LastPassFresh() {
		t.Error("the only pass did not count as a fresh environment")
	}
}

const probeArchive = "bentoo-probe-1.tar.gz"

// probeTree writes a staged repository holding app-misc/bentoo-probe-1, whose
// archive is a local file and whose build dependency is already in any Gentoo
// stage, and returns the tree and the directory holding the archive. Both live
// under t.TempDir, the same private (0700) kind of directory a real run stages
// into.
func probeTree(t *testing.T) (stagedRoot, distdir string) {
	t.Helper()
	stagedRoot, distdir = t.TempDir(), t.TempDir()
	archive := probeTarball(t)
	if err := os.WriteFile(filepath.Join(distdir, probeArchive), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum512(archive)
	files := map[string]string{
		"profiles/repo_name": "bentoo-staging-probe\n",
		"metadata/layout.conf": "masters = gentoo\nthin-manifests = true\n" +
			"manifest-hashes = SHA512\nmanifest-required-hashes = SHA512\n",
		"app-misc/bentoo-probe/Manifest": "DIST " + probeArchive + " " +
			strconv.Itoa(len(archive)) + " SHA512 " + hex.EncodeToString(sum[:]) + "\n",
		"app-misc/bentoo-probe/bentoo-probe-1.ebuild": `EAPI=8
DESCRIPTION="bentoolkit container backend probe"
HOMEPAGE="https://example.invalid/"
SRC_URI="https://example.invalid/${P}.tar.gz"
LICENSE="MIT"
SLOT="0"
KEYWORDS="~amd64"
RESTRICT="mirror"
DEPEND="app-arch/zstd"

src_compile() {
	cat greeting > built || die
}

src_install() {
	insinto /usr/share/${PN}
	doins built
}
`,
	}
	for rel, body := range files {
		p := filepath.Join(stagedRoot, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return stagedRoot, distdir
}

func probeTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("hello from the container\n")
	for _, h := range []*tar.Header{
		{Name: "bentoo-probe-1/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "bentoo-probe-1/greeting", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
