package autoupdate

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/obentoo/bentoolkit/internal/common/procgroup"
)

const (
	// vendorUploadTimeout bounds one run of autoupdate.vendor.upload_command.
	vendorUploadTimeout = 5 * time.Minute
	// vendorVerifyAttempts and vendorVerifyWait shape the check of the served
	// bytes after an upload: a CDN can still answer from the 404 it cached for
	// the probe a few moments earlier.
	vendorVerifyAttempts = 3
	vendorVerifyWait     = 2 * time.Second
	// vendorUploadTailSize bounds how much of the upload command's combined
	// output is kept for the error message.
	vendorUploadTailSize = 4 << 10
	// vendorKeptDir is the directory, at the root of a staged tree, an
	// unpublished vendor tarball is kept in for the operator to upload.
	vendorKeptDir = ".bentoo-vendor"
)

var (
	// ErrVendorUpload marks a generated vendor tarball whose publication was not
	// proved: the upload command failed or timed out, the served bytes differ
	// from the generated ones, or the tarball changed before it was published.
	ErrVendorUpload = errors.New("vendor tarball upload failed")
	// ErrVendorUploadPending marks a generated vendor tarball that was kept
	// rather than published because no upload command is configured. Its
	// wrapped text names the kept path and the URL it must be served from.
	ErrVendorUploadPending = errors.New("vendor tarball upload pending")
)

// publishVendorDistfile runs right before a staged candidate is promoted. When
// the vendor step generated a tarball in this run, the candidate may only be
// published once that tarball is served: it is uploaded with the configured
// command and the served bytes are compared with the digested ones, or, with
// no command configured, kept in the staged tree and the package failed as
// "upload pending". A returned error has already been recorded by failApply.
func (a *Applier) publishVendorDistfile(ctx context.Context, cand candidatePaths, pkg, version string, out VendorOutcome, result *ApplyResult) error {
	if !out.Generated {
		return nil
	}
	a.substage(result, pkg, "upload")
	// Only the bytes the Manifest digested may be published.
	if sum, err := fileSHA512(out.Tarball); err != nil || sum != out.SHA512 {
		if err == nil {
			err = fmt.Errorf("sha512 %s now, %s digested", sum, out.SHA512)
		}
		return a.failVendor(pkg, result, vendorUploadError(pkg, version, out.URI,
			"generated tarball changed or vanished before publication", err))
	}
	if len(a.vendorConfig.UploadCommand) == 0 {
		return a.parkVendorTarball(cand, pkg, version, out, result)
	}
	if err := a.uploadVendorTarball(ctx, pkg, version, out); err != nil {
		return a.failVendor(pkg, result, err)
	}
	if err := a.verifyServedVendor(ctx, pkg, version, out); err != nil {
		return a.failVendor(pkg, result, err)
	}
	a.logger().Info("vendor: served tarball matches the generated one",
		"package", pkg, "version", version, "uri", out.URI)
	return nil
}

// uploadVendorTarball runs the upload command, without a shell, on the
// caller's context in its own process group, bounded by vendorUploadTimeout.
func (a *Applier) uploadVendorTarball(ctx context.Context, pkg, version string, out VendorOutcome) error {
	argv := vendorUploadArgv(a.vendorConfig.UploadCommand, out.Tarball, out.File)
	a.logger().Info("vendor: uploading the generated tarball",
		"package", pkg, "version", version, "program", argv[0], "uri", out.URI)
	a.logger().Debug("vendor: upload command", "package", pkg, "version", version,
		"command", loggableUploadCommand(a.vendorConfig.UploadCommand))

	uctx, cancel := context.WithTimeout(ctx, vendorUploadTimeout)
	defer cancel()
	cmd := a.execCommand(uctx, argv[0], argv[1:]...)
	tail := &tailWriter{max: vendorUploadTailSize}
	cmd.Stdout = tail
	cmd.Stderr = tail
	procgroup.Group(cmd)
	err := procgroup.Result(cmd, cmd.Run())
	if err == nil {
		return nil
	}
	reason := "upload command failed"
	if ctx.Err() == nil && errors.Is(uctx.Err(), context.DeadlineExceeded) {
		reason = fmt.Sprintf("upload command timed out after %s", vendorUploadTimeout)
	}
	// The output stays out of the error, which reaches pending.json and the
	// terminal: upload programs echo request headers and signing details on
	// failure. It is logged at DEBUG with every argv value masked.
	a.logger().Debug("vendor: upload command output", "package", pkg, "version", version,
		"output", redactArgv(tail.text(), argv[1:]))
	return vendorUploadError(pkg, version, out.URI, reason,
		fmt.Errorf("%s: %w (output at debug log level)", argv[0], err))
}

// redactArgv masks every argv value of eight bytes or more inside text, so a
// literal credential passed as an argument is not echoed back by the log.
func redactArgv(text string, args []string) string {
	for _, arg := range args {
		if len(arg) >= 8 {
			text = strings.ReplaceAll(text, arg, "<argv>")
		}
	}
	return text
}

// loggableUploadCommand is the configured upload command as the debug log may
// show it: the program, then each argument with the same rule redactArgv
// applies to the output, an argument of eight bytes or more masked, unless it
// carries a {path} or {file} placeholder and so says where the tarball goes.
// It reads the template, never the substituted argv, so a literal credential
// written into the command is never logged.
func loggableUploadCommand(command []string) []string {
	shown := make([]string, len(command))
	for i, elem := range command {
		switch {
		case i == 0, len(elem) < 8,
			strings.Contains(elem, "{path}"), strings.Contains(elem, "{file}"):
			shown[i] = elem
		default:
			shown[i] = "<argv>"
		}
	}
	return shown
}

// vendorUploadArgv substitutes {path} and {file} inside every element of
// command in a single pass, so a placeholder that appears in a substituted
// value is never substituted again.
func vendorUploadArgv(command []string, path, file string) []string {
	r := strings.NewReplacer("{path}", path, "{file}", file)
	argv := make([]string, len(command))
	for i, elem := range command {
		argv[i] = r.Replace(elem)
	}
	return argv
}

// verifyServedVendor downloads the vendor URI and compares its SHA512 with the
// generated tarball's, trying vendorVerifyAttempts times vendorVerifyWait
// apart before calling a difference final.
func (a *Applier) verifyServedVendor(ctx context.Context, pkg, version string, out VendorOutcome) error {
	if a.httpClient == nil {
		return vendorUploadError(pkg, version, out.URI, "served tarball not verified", errors.New("no HTTP client configured"))
	}
	var last error
	for attempt := 1; attempt <= vendorVerifyAttempts; attempt++ {
		if attempt > 1 {
			if err := a.httpClient.Wait(ctx, vendorVerifyWait); err != nil {
				return vendorUploadError(pkg, version, out.URI, "served tarball not verified", err)
			}
		}
		served, err := a.servedSHA512(ctx, out.URI)
		if errors.Is(err, errServedNotFound) {
			err = a.cachedNotFound(ctx, out)
		}
		switch {
		case err != nil:
			last = vendorUploadError(pkg, version, out.URI, "served tarball could not be downloaded", err)
		case served != out.SHA512:
			last = vendorUploadError(pkg, version, out.URI, "served digest differs",
				fmt.Errorf("sha512 %s served, %s generated; the uploaded object must be removed or replaced "+
					"before the next run, which would digest whatever is served", served, out.SHA512))
		default:
			return nil
		}
		a.logger().Warn("vendor: served tarball not proved yet", "package", pkg, "version", version,
			"uri", out.URI, "attempt", attempt, "error", last)
	}
	return last
}

// servedSHA512 downloads uri past any cache that honours no-cache and returns
// the hex SHA512 of the body; any answer but 200 is an error.
func (a *Applier) servedSHA512(ctx context.Context, uri string) (string, error) {
	dctx, cancel := context.WithTimeout(ctx, vendorDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, uri, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := a.httpClient.DownloadRequest(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", errServedNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	h := sha512.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// errServedNotFound is a 404 from the vendor URI during verification.
var errServedNotFound = errors.New("HTTP 404")

// cachedNotFound tells an object that was never served from one a CDN still
// answers 404 for: a distfiles CDN can cache a 404 for a year and ignore
// no-cache, while a query string is part of its cache key. When the origin,
// reached through a fresh query string, serves the generated bytes, the error
// names the purge the operator owes, because every user's fetch would still
// get the cached 404.
func (a *Applier) cachedNotFound(ctx context.Context, out VendorOutcome) error {
	sep := "?"
	if strings.Contains(out.URI, "?") {
		sep = "&"
	}
	nonce := fmt.Sprintf("%sbentoo-verify=%d", sep, time.Now().UnixNano())
	served, err := a.servedSHA512(ctx, out.URI+nonce)
	if err != nil || served != out.SHA512 {
		return errServedNotFound
	}
	return fmt.Errorf("the origin serves it but the CDN still answers 404 from its cache: "+
		"purge %s from the CDN cache, then re-run --apply", out.URI)
}

// parkVendorTarball keeps the generated tarball inside the candidate's staged
// tree and fails the package as "upload pending", naming where the tarball
// was kept and the URL it must be served from.
func (a *Applier) parkVendorTarball(cand candidatePaths, pkg, version string, out VendorOutcome, result *ApplyResult) error {
	kept, err := keepVendorTarball(out.Tarball, filepath.Join(cand.repoRoot, vendorKeptDir), out.File)
	if err != nil {
		return a.failVendor(pkg, result, vendorUploadError(pkg, version, out.URI,
			"generated tarball could not be kept in the staged tree", err))
	}
	result.StagedPath = cand.repoRoot
	a.logger().Info("vendor: no upload command; tarball kept for a manual upload",
		"package", pkg, "version", version, "path", kept, "uri", out.URI)
	return a.failVendor(pkg, result, fmt.Errorf("%w: %s-%s: upload %s to %s",
		ErrVendorUploadPending, pkg, version, kept, out.URI))
}

// keepVendorTarball copies src to dir/file, world-readable like a distfile,
// through a temporary file so a partial copy never takes the final name.
func keepVendorTarball(src, dir, file string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: a directory of distfiles inside the staged tree, readable like one
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+file+".part-")
	if err != nil {
		return "", fmt.Errorf("keep %s: %w", file, err)
	}
	err = copyFileInto(tmp, src)
	dest := filepath.Join(dir, file)
	if err == nil {
		err = os.Rename(tmp.Name(), dest)
	}
	if err != nil {
		return "", errors.Join(fmt.Errorf("keep %s: %w", file, err), os.Remove(tmp.Name()))
	}
	return dest, nil
}

// copyFileInto copies the file at src into tmp, makes it world-readable and
// closes tmp.
func copyFileInto(tmp *os.File, src string) error {
	in, err := os.Open(src) //nolint:gosec // G304: the tarball the vendor step generated in this run's distdir
	if err != nil {
		return errors.Join(err, tmp.Close())
	}
	_, err = io.Copy(tmp, in)
	err = errors.Join(err, in.Close())
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	return errors.Join(err, tmp.Close())
}

// failVendor records err as the apply's failure and returns what failApply
// returned, which names a pending-status write that also failed.
func (a *Applier) failVendor(pkg string, result *ApplyResult, err error) error {
	_, err = a.failApply(pkg, result, err)
	return err
}

// vendorUploadError wraps a publication failure with the package, version,
// vendor URL and reason.
func vendorUploadError(pkg, version, uri, reason string, err error) error {
	return fmt.Errorf("%w: %s-%s: %s: %s: %w", ErrVendorUpload, pkg, version, uri, reason, err)
}

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	max int
	buf []byte
	cut bool // earlier output was dropped
}

func (t *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > t.max {
		p, t.cut = p[len(p)-t.max:], true
	}
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf, t.cut = append(t.buf[:0], t.buf[over:]...), true
	}
	return n, nil
}

// text formats the kept output for an error message, starting on a whole
// UTF-8 sequence; it is empty when the command printed nothing.
func (t *tailWriter) text() string {
	out := strings.TrimRight(string(t.buf), " \t\r\n")
	if t.cut {
		for i := 0; i < utf8.UTFMax && out != "" && !utf8.RuneStart(out[0]); i++ {
			out = out[1:]
		}
	}
	if out == "" {
		return ""
	}
	return "\n" + out
}

// VendorUploadAction returns the "upload <path> to <uri>" action an
// ErrVendorUploadPending error carries; ok is false for any other error. The
// error text is the single source of the kept path and the URL, so every
// renderer reads them from here.
func VendorUploadAction(err error) (action string, ok bool) {
	if !errors.Is(err, ErrVendorUploadPending) {
		return "", false
	}
	msg := err.Error()
	i := strings.LastIndex(msg, ": upload ")
	if i < 0 {
		return "", false
	}
	return msg[i+len(": "):], true
}
