package vendor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Runner runs one child process. env is the child's complete environment and
// the returned bytes are its combined stdout and stderr. Implementations put
// the child in its own process group and stop it when ctx is done.
type Runner interface {
	Run(ctx context.Context, dir string, env []string, name string, args ...string) (combined []byte, err error)
}

// GenerateRequest describes one vendor tarball to build.
type GenerateRequest struct {
	SourcePath string // the source archive, already in a local distdir
	VendorFile string // bare file name of the tarball to write
	OutDir     string // where the tarball is written (the staged distdir)
	WorkRoot   string // parent for the private work dir (PORTAGE_TMPDIR)
	Timeout    time.Duration
}

// outputTailSize bounds how much of a failed child's output an error carries:
// the end of the output, where go prints the reason it stopped.
const outputTailSize = 4 << 10

// srcSubdir is the work dir entry the source archive is unpacked into.
const srcSubdir = "src"

// scrubbedEnv lists inherited variables that would change what go or tar
// produce. Each one is dropped from the child environment; the step sets its
// own value where it needs one.
var scrubbedEnv = []string{"GOFLAGS", "GOTOOLCHAIN", "XZ_OPT", "XZ_DEFAULTS", "TAR_OPTIONS"}

// Generate unpacks req.SourcePath into a private work dir under req.WorkRoot,
// runs `go mod vendor` in its single top directory and writes
// <top>/vendor as req.VendorFile into req.OutDir: an xz tar with entries
// sorted by name, owned by 0:0 with numeric ids and dated at the epoch, so the
// same source always gives the same bytes. It returns the tarball's path.
//
// The work dir is removed on every path, read-only module files included. When
// only that removal fails, the tarball is already in req.OutDir and the
// removal error is returned.
func Generate(ctx context.Context, r Runner, req GenerateRequest) (tarball string, err error) {
	if err := req.validate(); err != nil {
		return "", err
	}
	err = withWorkDir(req.WorkRoot, func(work string) error {
		top, err := vendorTree(ctx, r, work, req)
		if err != nil {
			return err
		}
		tarball, err = pack(ctx, r, work, top, req)
		return err
	})
	if err != nil {
		return "", err
	}
	return tarball, nil
}

func (req GenerateRequest) validate() error {
	if req.VendorFile == "" || filepath.Base(req.VendorFile) != req.VendorFile {
		return fmt.Errorf("vendor tarball name %q is not a bare file name", req.VendorFile)
	}
	if req.Timeout <= 0 {
		return fmt.Errorf("vendor %s: timeout %s is not positive", req.VendorFile, req.Timeout)
	}
	return nil
}

// withWorkDir runs fn in a fresh directory under root and removes that
// directory afterwards. A removal error is returned only when fn succeeded, so
// it never hides the failure that matters.
func withWorkDir(root string, fn func(work string) error) (err error) {
	work, err := os.MkdirTemp(root, "bentoo-vendor-")
	if err != nil {
		return fmt.Errorf("create vendor work dir: %w", err)
	}
	defer func() {
		if rmErr := removeWorkDir(work); rmErr != nil && err == nil {
			err = fmt.Errorf("remove vendor work dir %s: %w", work, rmErr)
		}
	}()
	return fn(work)
}

// removeWorkDir makes every entry under dir owner-writable, because go leaves
// its module files read-only, then removes dir. The walk is confined to dir by
// os.Root and skips symlinks, so no chmod lands outside the work dir. A failed
// chmod is reported only when it makes the removal fail.
func removeWorkDir(dir string) error {
	var firstErr error
	if root, err := os.OpenRoot(dir); err != nil {
		firstErr = err
	} else {
		firstErr = makeOwnerWritable(root)
		if err := root.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return errors.Join(err, firstErr)
	}
	return nil
}

// makeOwnerWritable adds u+w to every entry under root, and u+rx to every
// directory so its entries can be listed and removed. It keeps walking past
// errors and returns the first one.
func makeOwnerWritable(root *os.Root) error {
	var firstErr error
	note := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	walkErr := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			note(err)
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			note(err)
			return nil
		}
		want := info.Mode().Perm() | 0o200
		if d.IsDir() {
			want |= 0o700
		}
		if want != info.Mode().Perm() {
			if err := root.Chmod(p, want); err != nil {
				note(err)
			}
		}
		return nil
	})
	if walkErr != nil {
		note(walkErr)
	}
	return firstErr
}

// vendorTree unpacks the source into <work>/src, checks it is one top
// directory holding go.mod and runs `go mod vendor` there. It returns the top
// directory's name.
func vendorTree(ctx context.Context, r Runner, work string, req GenerateRequest) (string, error) {
	distfile := filepath.Base(req.SourcePath)
	src := filepath.Join(work, srcSubdir)
	if err := os.Mkdir(src, 0o700); err != nil {
		return "", fmt.Errorf("unpack %s: %w", distfile, err)
	}
	if out, err := r.Run(ctx, work, childEnv(), "tar", "-xf", req.SourcePath, "-C", src); err != nil {
		return "", fmt.Errorf("unpack %s: %w%s", distfile, err, outputTail(out))
	}
	top, err := singleGoModTop(src)
	if err != nil {
		return "", fmt.Errorf("%s: %w", distfile, err)
	}

	vctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	out, err := r.Run(vctx, filepath.Join(src, top), childEnv("GOFLAGS=-modcacherw", "GOTOOLCHAIN=local"), "go", "mod", "vendor")
	if err != nil {
		if ctx.Err() == nil && errors.Is(vctx.Err(), context.DeadlineExceeded) {
			err = timedOut(req.Timeout, err)
		}
		return "", fmt.Errorf("go mod vendor for %s: %w%s", distfile, err, outputTail(out))
	}
	return top, nil
}

// singleGoModTop returns the name of the only entry of src, which must be a
// directory (not a symlink) holding a regular go.mod file.
func singleGoModTop(src string) (string, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return "", fmt.Errorf("list unpacked source: %w", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return "", fmt.Errorf("expected a single top directory holding go.mod, found %d entr(ies): %q", len(entries), names)
	}
	top := entries[0].Name()
	info, err := os.Lstat(filepath.Join(src, top, "go.mod"))
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("expected a single top directory holding go.mod, but %s has no go.mod file", top)
	}
	return top, nil
}

// pack writes <top>/vendor as the reproducible tarball and moves it into
// req.OutDir.
func pack(ctx context.Context, r Runner, work, top string, req GenerateRequest) (string, error) {
	packed := filepath.Join(work, req.VendorFile)
	args := []string{
		"--sort=name", "--owner=0", "--group=0", "--numeric-owner", "--mtime=@0",
		"-C", filepath.Join(work, srcSubdir), "-cJf", packed, top + "/vendor",
	}
	if out, err := r.Run(ctx, work, childEnv("XZ_OPT=-9T0"), "tar", args...); err != nil {
		return "", fmt.Errorf("pack %s: %w%s", req.VendorFile, err, outputTail(out))
	}
	dst := filepath.Join(req.OutDir, req.VendorFile)
	if err := moveFile(packed, dst); err != nil {
		return "", fmt.Errorf("move %s into %s: %w", req.VendorFile, req.OutDir, err)
	}
	return dst, nil
}

// moveFile renames src to dst, copying through a temporary file in dst's
// directory when the two sit on different file systems.
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-")
	if err != nil {
		return err
	}
	if err := copyInto(tmp, src); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	return nil
}

// copyInto copies the file at src into tmp, makes it world-readable like a
// distfile, flushes it to disk and closes tmp.
func copyInto(tmp *os.File, src string) error {
	in, err := os.Open(src) //nolint:gosec // G304: src is the tarball this package just packed inside its own work dir
	if err != nil {
		return errors.Join(err, tmp.Close())
	}
	_, err = io.Copy(tmp, in)
	err = errors.Join(err, in.Close())
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if err == nil {
		err = tmp.Sync()
	}
	return errors.Join(err, tmp.Close())
}

// childEnv returns the inherited environment without scrubbedEnv, followed
// by extra.
func childEnv(extra ...string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		return slices.Contains(scrubbedEnv, key)
	})
	return append(env, extra...)
}

// timedOut describes a child stopped by the vendor timeout, keeping both the
// deadline and the child's own error in the chain.
func timedOut(limit time.Duration, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s: %w", limit, err)
	}
	return fmt.Errorf("timed out after %s: %w: %w", limit, context.DeadlineExceeded, err)
}

// outputTail formats the last outputTailSize bytes of a child's output for an
// error message, starting on a whole UTF-8 sequence.
func outputTail(out []byte) string {
	out = bytes.TrimRight(out, " \t\r\n")
	if len(out) == 0 {
		return ""
	}
	if len(out) > outputTailSize {
		out = out[len(out)-outputTailSize:]
		for i := 0; i < utf8.UTFMax && len(out) > 0 && !utf8.RuneStart(out[0]); i++ {
			out = out[1:]
		}
	}
	return "\n" + string(out)
}
