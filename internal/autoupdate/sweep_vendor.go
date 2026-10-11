package autoupdate

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
	"github.com/obentoo/bentoolkit/internal/autoupdate/vendor"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/procgroup"
)

// vendorGenerateTimeout bounds one `go mod vendor` run, which downloads every
// module the source requires.
const vendorGenerateTimeout = 10 * time.Minute

// vendorDownloadTimeout bounds one distfile download (a source archive or a
// served vendor tarball), which can take far longer than an API request.
const vendorDownloadTimeout = 30 * time.Minute

var (
	// ErrVendorGenerate marks a Go vendor tarball that could not be generated,
	// or a served one that could not be compared with a regenerated tree.
	ErrVendorGenerate = errors.New("vendor tarball generation failed")
	// ErrVendorStale marks a served vendor tarball whose name does not carry
	// the candidate's version and whose vendor/modules.txt no longer matches
	// the one the candidate's source produces.
	ErrVendorStale = errors.New("pinned vendor tarball is stale")
)

// VendorOutcome is what the vendor step did for one staged candidate. The
// zero value means it produced nothing.
type VendorOutcome struct {
	Generated bool   // a tarball was produced in this run
	Tarball   string // path of the tarball inside the staged distdir
	File, URI string // the vendor distfile name and the URI it is served from
	SHA512    string // hex digest of the tarball, computed once after Generate
	// ProbeFailed reports a vendor probe that could not answer (a transport
	// error, a timeout or a 5xx): whether the tarball is served is unknown, so
	// the manifest failure that follows is the vendor step's, not the ebuild's.
	ProbeFailed bool
}

// withSweeperHTTPClient sets the client the vendor step probes and downloads
// with. A nil client disables the vendor step.
func withSweeperHTTPClient(c *fetch.RetryableHTTPClient) sweeperOption {
	return func(s *sweeper) { s.httpClient = c }
}

// withSweeperVendorHosts sets the hosts whose vendor tarballs the step owns;
// an empty list means the default host.
func withSweeperVendorHosts(hosts []string) sweeperOption {
	return func(s *sweeper) { s.vendorHosts = hosts }
}

// execRunner is the production vendor.Runner: every child is built through
// the sweeper's exec seam on the caller's context, runs in its own process
// group and is stopped with that group when the context is done.
type execRunner struct {
	exec func(ctx context.Context, name string, arg ...string) *exec.Cmd
}

// Run runs name with env as its complete environment (nil inherits this
// process's) and returns its combined output.
func (r execRunner) Run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := r.exec(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	procgroup.Group(cmd)
	err := procgroup.Result(cmd, cmd.Run())
	return out.Bytes(), err
}

// prepareVendorDistfile runs before the staged manifest step. For a Go vendor
// candidate whose vendor distfile is not served yet (a definite 404) it
// generates the tarball into distdir, so pkgdev digests it without fetching
// it. For a served tarball whose name lacks the candidate's version it checks
// that the tarball still matches the candidate's source. A probe that fails
// (transport error, timeout, 5xx, retries exhausted) is reported as
// ProbeFailed. Anything it cannot classify, a probe answered otherwise (e.g.
// 403), or a cancelled run leaves the candidate to the ordinary manifest path
// with a zero outcome and no error.
func (s *sweeper) prepareVendorDistfile(ctx context.Context, stagedRoot, pkg, version, distdir string) (VendorOutcome, error) {
	if s.httpClient == nil {
		return VendorOutcome{}, nil
	}
	c, ok := s.vendorCandidate(ctx, stagedRoot, pkg, version)
	if !ok {
		return VendorOutcome{}, nil
	}
	if _, err := os.Stat(filepath.Join(distdir, c.VendorFile)); err == nil {
		return VendorOutcome{}, nil
	}

	status, err := s.headStatus(ctx, c.VendorURI)
	switch {
	case err == nil && status == http.StatusNotFound:
		return s.generateVendor(ctx, c, pkg, version, distdir)
	case err == nil && status == http.StatusOK:
		if strings.HasSuffix(c.VendorFile, "-"+baseVersion(version)+"-vendor.tar.xz") {
			return VendorOutcome{}, nil
		}
		return VendorOutcome{}, s.checkPinnedVendor(ctx, c, pkg, version, distdir)
	case errors.Is(err, context.Canceled):
		// The run was interrupted: the upstream answered nothing wrong.
		return VendorOutcome{}, nil
	case err != nil || status >= http.StatusInternalServerError:
		s.log.Warn("vendor: probe failed", "package", pkg, "version", version,
			"uri", c.VendorURI, "status", status, "error", err)
		return VendorOutcome{ProbeFailed: true, File: c.VendorFile, URI: c.VendorURI}, nil
	default:
		s.log.Info("vendor: not owned", "package", pkg, "version", version,
			"uri", c.VendorURI, "status", status)
		return VendorOutcome{}, nil
	}
}

// vendorCandidate classifies the staged candidate. ok is false when it is not
// a Go vendor candidate; fetchables that cannot be read are reported as a
// warning and also yield false.
func (s *sweeper) vendorCandidate(ctx context.Context, stagedRoot, pkg, version string) (vendor.Candidate, bool) {
	category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return vendor.Candidate{}, false
	}
	ebuildPath := filepath.Join(stagedRoot, category, pkgName, pkgName+"-"+version+".ebuild")
	body, err := os.ReadFile(ebuildPath) //nolint:gosec // G304: the candidate ebuild inside the staged tree this run built
	if err != nil {
		s.warnVendor("cannot read the staged ebuild; not a vendor candidate", pkg, version, err)
		return vendor.Candidate{}, false
	}
	// A cheap textual filter: only an ebuild naming both can be one, and
	// everything else skips egencache and the network.
	if !bytes.Contains(body, []byte("go-module")) || !bytes.Contains(body, []byte("-vendor.tar.xz")) {
		return vendor.Candidate{}, false
	}

	entry, err := s.stagedMetadataEntry(ctx, stagedRoot, category, pkgName, version)
	if err != nil {
		s.warnVendor("cannot read the candidate's fetchables; not a vendor candidate", pkg, version, err)
		return vendor.Candidate{}, false
	}
	defer entry.Close() //nolint:errcheck // read-only handle: a failed close cannot lose data
	hosts := config.VendorConfig{Hosts: s.vendorHosts}.EffectiveHosts()
	c, ok, err := vendor.Classify(entry, hosts)
	if err != nil {
		s.warnVendor("cannot read the candidate's fetchables; not a vendor candidate", pkg, version, err)
		return vendor.Candidate{}, false
	}
	if ok && !bareName(c.SourceFile) {
		s.warnVendor("source distfile is not a bare file name; not a vendor candidate", pkg, version,
			fmt.Errorf("source distfile %q", c.SourceFile))
		return vendor.Candidate{}, false
	}
	return c, ok
}

// stagedMetadataEntry generates the md5-cache entry of the staged candidate
// with egencache and opens it. The staged tree has no md5-cache before pkgdev
// runs, and the entry is where the expanded SRC_URI is read from.
func (s *sweeper) stagedMetadataEntry(ctx context.Context, stagedRoot, category, pkgName, version string) (*os.File, error) {
	conf, repo, err := repositoriesConfiguration(stagedRoot, "")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(stagedRoot, "metadata", "md5-cache", category, pkgName+"-"+version)
	// egencache exits 0 and keeps the previous entry when the ebuild no longer
	// sources; removing it first makes such a run read as "no entry", never as
	// the SRC_URI of an earlier edit.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale md5-cache entry: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()
	// egencache refuses a versioned atom ("Atom is too specific"); the staged
	// tree holds this one version only, so the package name selects it.
	out, err := execRunner{exec: s.execCommand}.Run(runCtx, "", nil, "egencache",
		"--repositories-configuration="+conf, "--repo", repo, "--update", category+"/"+pkgName)
	if err != nil {
		return nil, fmt.Errorf("egencache: %w: %s", err, strings.TrimSpace(string(out)))
	}
	f, err := os.Open(path) //nolint:gosec // G304: the md5-cache entry egencache just wrote inside the staged tree
	if err != nil {
		return nil, fmt.Errorf("egencache wrote no usable entry: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return f, nil
}

// generateVendor builds the vendor tarball into distdir and describes it.
func (s *sweeper) generateVendor(ctx context.Context, c vendor.Candidate, pkg, version, distdir string) (VendorOutcome, error) {
	s.reporter.TaskStage(pkg, "vendor")
	defer s.reporter.TaskStage(pkg, "manifest")
	s.log.Info("vendor: tarball not served; generating it", "package", pkg, "version", version, "uri", c.VendorURI)

	src, err := s.vendorSource(ctx, c, distdir)
	if err != nil {
		return VendorOutcome{}, vendorError(pkg, version, c.VendorFile, err)
	}
	tarball, err := vendor.Generate(ctx, execRunner{exec: s.execCommand}, vendor.GenerateRequest{
		SourcePath: src,
		VendorFile: c.VendorFile,
		OutDir:     distdir,
		WorkRoot:   fixSandboxRoot(ctx),
		Timeout:    vendorGenerateTimeout,
	})
	if err != nil {
		return VendorOutcome{}, vendorError(pkg, version, c.VendorFile, err)
	}
	sum, err := fileSHA512(tarball)
	if err != nil {
		return VendorOutcome{}, vendorError(pkg, version, c.VendorFile, err)
	}
	s.log.Info("vendor: generated tarball", "package", pkg, "version", version, "file", c.VendorFile, "path", tarball)
	return VendorOutcome{Generated: true, Tarball: tarball, File: c.VendorFile, URI: c.VendorURI, SHA512: sum}, nil
}

// checkPinnedVendor compares a served vendor tarball whose name does not
// carry the candidate's version with a tree regenerated from the candidate's
// source, and fails the package when the two vendor/modules.txt differ. A
// tarball that cannot be downloaded leaves the candidate unchecked.
func (s *sweeper) checkPinnedVendor(ctx context.Context, c vendor.Candidate, pkg, version, distdir string) error {
	s.reporter.TaskStage(pkg, "vendor")
	defer s.reporter.TaskStage(pkg, "manifest")

	tmp, err := os.CreateTemp(fixSandboxRoot(ctx), "bentoo-vendor-pinned-")
	if err != nil {
		return vendorError(pkg, version, c.VendorFile, err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best effort: a leftover temp file under the scratch root is harmless
	err = s.getInto(ctx, c.VendorURI, tmp)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		s.warnVendor("cannot download the served tarball; stale check skipped", pkg, version, err)
		return nil
	}

	src, err := s.vendorSource(ctx, c, distdir)
	if err != nil {
		return vendorError(pkg, version, c.VendorFile, err)
	}
	differs, err := vendor.ModulesTxtDiffers(ctx, execRunner{exec: s.execCommand}, tmp.Name(), vendor.GenerateRequest{
		SourcePath: src,
		WorkRoot:   fixSandboxRoot(ctx),
		Timeout:    vendorGenerateTimeout,
	})
	if err != nil {
		return vendorError(pkg, version, c.VendorFile, err)
	}
	if differs {
		return fmt.Errorf("%w: %w: %s-%s: %s does not match the vendor tree of PV %s; "+
			"point the ebuild's vendor distfile at ${P}-vendor.tar.xz",
			ErrManifestFailed, ErrVendorStale, pkg, version, c.VendorFile, baseVersion(version))
	}
	return nil
}

// vendorSource returns the path of the candidate's source archive in distdir,
// downloading it there first when it is absent.
func (s *sweeper) vendorSource(ctx context.Context, c vendor.Candidate, distdir string) (string, error) {
	dest := filepath.Join(distdir, c.SourceFile)
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	tmp, err := os.CreateTemp(distdir, "."+c.SourceFile+".part-")
	if err != nil {
		return "", fmt.Errorf("download %s: %w", c.SourceFile, err)
	}
	err = s.getInto(ctx, c.SourceURI, tmp)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644) //nolint:gosec // G302: a distfile, world-readable like every other one
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dest)
	}
	if err != nil {
		return "", errors.Join(fmt.Errorf("download %s: %w", c.SourceFile, err), os.Remove(tmp.Name()))
	}
	return dest, nil
}

// headStatus probes uri and returns the status of the final answer.
func (s *sweeper) headStatus(ctx context.Context, uri string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, uri, nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.httpClient.DoWithContext(ctx, req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// getInto downloads uri into w; any answer but 200 is an error. The transfer
// is bounded by vendorDownloadTimeout, not by the API-sized http_timeout.
func (s *sweeper) getInto(ctx context.Context, uri string, w io.Writer) error {
	dctx, cancel := context.WithTimeout(ctx, vendorDownloadTimeout)
	defer cancel()
	resp, err := s.httpClient.Download(dctx, uri)
	if err != nil {
		return fmt.Errorf("GET %s: %w", uri, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", uri, resp.StatusCode)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("GET %s: %w", uri, err)
	}
	return nil
}

// warnVendor reports a vendor step that stepped aside, naming the package,
// the version and why, to the log and to the progress sink.
func (s *sweeper) warnVendor(reason, pkg, version string, err error) {
	s.log.Warn("vendor: step skipped", "reason", reason, "package", pkg, "version", version, "error", err)
	s.reporter.Log("warn", fmt.Sprintf("vendor: %s: %s-%s: %v", reason, pkg, version, err))
}

// vendorError wraps a vendor step failure with the package, version and
// vendor distfile it belongs to.
func vendorError(pkg, version, file string, err error) error {
	return fmt.Errorf("%w: %w: %s-%s %s: %w", ErrManifestFailed, ErrVendorGenerate, pkg, version, file, err)
}

// fileSHA512 returns the hex SHA512 of the file at path.
func fileSHA512(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the tarball the vendor step just wrote
	if err != nil {
		return "", fmt.Errorf("digest %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle: a failed close cannot lose data
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("digest %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// bareName reports whether name is a plain file name with no directory part.
func bareName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}
