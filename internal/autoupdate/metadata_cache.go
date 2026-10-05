package autoupdate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

// metadataCacheTimeout bounds one egencache run. It regenerates a single
// package — measured at a quarter of a second — so this is generous.
const metadataCacheTimeout = 2 * time.Minute

// defaultGentooRepo is where Portage puts ::gentoo, used when no gentoo path
// was configured on the Applier.
const defaultGentooRepo = "/var/db/repos/gentoo"

// regenMetadataCache regenerates pkg's metadata/md5-cache entries in the
// published overlay: the new version gets its entry, and entries left by
// versions whose ebuild is gone are removed. A bump used to do neither, and the
// overlay once carried 548 orphaned entries.
//
// An overlay that keeps no metadata/md5-cache is left alone. egencache is
// pointed at the checkout through --repositories-configuration: without it, it
// resolves the repository by name to the synced copy Portage reads and would
// rewrite THAT cache instead. With a package atom it touches only that
// package's entries, which is what lets it run after every bump.
func (a *Applier) regenMetadataCache(ctx context.Context, pkg, version string) error {
	if fi, err := os.Stat(filepath.Join(a.overlayPath, "metadata", "md5-cache")); err != nil || !fi.IsDir() {
		return nil
	}
	category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return fmt.Errorf("cannot regenerate the md5-cache of %q: not a category/package atom", pkg)
	}
	conf, repo, err := repositoriesConfiguration(a.overlayPath, a.gentooPath)
	if err != nil {
		return fmt.Errorf("md5-cache of %s not regenerated: %w", pkg, err)
	}
	if _, err := a.lookPath("egencache"); err != nil {
		return fmt.Errorf("md5-cache of %s not regenerated: egencache is not on PATH", pkg)
	}

	runCtx, cancel := context.WithTimeout(ctx, metadataCacheTimeout)
	defer cancel()
	cmd := a.execCommand(runCtx, "egencache", "--update", "--repo", repo,
		"--repositories-configuration", conf, category+"/"+pkgName)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("md5-cache of %s not regenerated: egencache: %w: %s", pkg, err, strings.TrimSpace(out.String()))
	}
	// egencache exits 0 when it fails on a version ("Error processing …,
	// continuing"), so its status cannot be the verdict: the entry must exist.
	entry := filepath.Join(a.overlayPath, "metadata", "md5-cache", category, pkgName+"-"+version)
	if _, err := os.Stat(entry); err != nil {
		return fmt.Errorf("md5-cache of %s: egencache wrote no entry for %s: %s", pkg, version, strings.TrimSpace(out.String()))
	}
	return nil
}

// repositoriesConfiguration builds the repos.conf text that makes egencache
// read overlayPath as its repository, with ::gentoo as its only master. It
// returns the text and the repository's name.
func repositoriesConfiguration(overlayPath, gentooPath string) (string, string, error) {
	nameBytes, err := os.ReadFile(filepath.Join(overlayPath, "profiles", "repo_name")) //nolint:gosec // G304: a fixed name under the configured overlay
	if err != nil {
		return "", "", fmt.Errorf("reading profiles/repo_name: %w", err)
	}
	name := strings.TrimSpace(string(nameBytes))
	if name == "" || strings.ContainsAny(name, "[]\n") {
		return "", "", fmt.Errorf("profiles/repo_name %q is not a repository name", name)
	}
	masters, err := layoutMasters(overlayPath)
	if err != nil {
		return "", "", err
	}
	for _, m := range masters {
		if m != "gentoo" {
			return "", "", fmt.Errorf("master %q in metadata/layout.conf has no known location; only gentoo is supported", m)
		}
	}
	if gentooPath == "" {
		gentooPath = defaultGentooRepo
	}
	conf := fmt.Sprintf("[DEFAULT]\nmain-repo = gentoo\n[gentoo]\nlocation = %s\n[%s]\nlocation = %s\nmasters = %s\n",
		gentooPath, name, overlayPath, strings.Join(masters, " "))
	return conf, name, nil
}

// layoutMasters reads the masters line of overlayPath's metadata/layout.conf.
func layoutMasters(overlayPath string) ([]string, error) {
	f, err := os.Open(filepath.Join(overlayPath, "metadata", "layout.conf")) //nolint:gosec // G304: a fixed name under the configured overlay
	if err != nil {
		return nil, fmt.Errorf("reading metadata/layout.conf: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only handle: a failed close cannot lose data
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok && strings.TrimSpace(key) == "masters" {
			return strings.Fields(value), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading metadata/layout.conf: %w", err)
	}
	return nil, errors.New("metadata/layout.conf declares no masters")
}
