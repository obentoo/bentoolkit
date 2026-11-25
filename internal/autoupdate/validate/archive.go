// Package validate answers one question about an ebuild without building it:
// does it still match the source it points at?
//
// It reads the build options the upstream archive declares, reads the ones the
// ebuild passes, and subtracts. Every input comes from the local filesystem —
// the archive is already on disk, put there by the manifest step — so the
// package performs no network I/O at all, and it writes nothing anywhere.
package validate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"slices"
	"strings"
	"time"
)

// execCommand is the seam the tests drive, in the shape this repository already
// uses for external tools (internal/common/distfiles, and the Applier's own
// execCommand field). Production code never calls exec.CommandContext directly.
var execCommand = exec.CommandContext

// archiveTimeout bounds a single `tar` invocation.
//
// The value matches autoupdate.DefaultOpTimeout, and is written out here rather
// than imported because internal/autoupdate imports THIS package for the
// isolation probe — taking the constant back would be an import cycle. If one
// moves, move the other.
const archiveTimeout = 30 * time.Second

// maxArchiveBytes caps what a single `tar` invocation may hand back.
//
// Real Meson option files are a few kilobytes, so 4 MiB is three orders of
// magnitude of headroom. It is not a size limit, it is a bomb guard: without it
// a hostile or corrupt archive turns a read-only gate into memory exhaustion,
// and the failure mode of a gate that OOMs is that someone switches it off.
const maxArchiveBytes = 4 << 20

// ErrArchiveTooLarge is returned when an invocation's output exceeds
// maxArchiveBytes. It is a named error so a caller can tell "this archive is
// hostile" apart from "this archive is broken" and report each honestly.
var ErrArchiveTooLarge = errors.New("archive output exceeds the read cap")

// listArchiveMembers returns the member names inside archive that a gate can
// read, in the order tar reports them: see listingRelevant.
//
// It runs `tar -tf`, which only reads the archive's index — the archive is
// never unpacked, so listing the 5.7 MB gst tarball costs one pass and no disk.
//
// The listing is filtered while it streams, and the read cap counts only the
// names kept. Counting every name tripped the cap on a large tree whose gates
// read five files: thunderbird-161.0.source.tar.xz lists more than 4 MiB of
// names (2026-11-25), and every gate SKIPPED on it.
func listArchiveMembers(ctx context.Context, archive string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, archiveTimeout)
	defer cancel()

	cmd := execCommand(ctx, "tar", "-tf", archive)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("listing %s: opening tar stdout: %w", archive, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("listing %s: starting tar: %w", archive, err)
	}

	var members []string
	kept := 0
	heads := map[string]bool{}
	scanner := bufio.NewScanner(stdout)
	// A member name is bounded by PATH_MAX (4096) in every tar format a source
	// archive uses; 64 KiB per line leaves room without trusting the archive.
	scanner.Buffer(make([]byte, 0, 4096), 64<<10)
	for scanner.Scan() {
		name := strings.TrimRight(scanner.Text(), "\r")
		if name == "" || !listingRelevant(name, heads) {
			continue
		}
		if kept += len(name) + 1; kept > maxArchiveBytes {
			cancel()
			_ = cmd.Wait()
			return nil, fmt.Errorf("listing %s: %w (%d bytes of relevant names)", archive, ErrArchiveTooLarge, maxArchiveBytes)
		}
		members = append(members, name)
	}
	if err := scanner.Err(); err != nil {
		cancel()
		_ = cmd.Wait()
		return nil, fmt.Errorf("listing %s: reading tar output: %w", archive, err)
	}
	if err := cmd.Wait(); err != nil {
		if diag := flattenDiagnostic(stderr.String()); diag != "" {
			return nil, fmt.Errorf("listing %s: tar failed: %w: %s", archive, err, diag)
		}
		return nil, fmt.Errorf("listing %s: tar failed: %w", archive, err)
	}
	return members, nil
}

// listingRelevant reports whether a gate can read the member: a Meson file
// (meson.build, an option file), a CMake source (CMakeLists.txt, *.cmake), a
// build-system marker, or the first member under each top-level entry, which
// is all archiveRoot needs to tell a wrapper directory from several entries.
// heads records the top-level entries seen so far.
func listingRelevant(name string, heads map[string]bool) bool {
	head, _, _ := strings.Cut(strings.TrimSuffix(name, "/"), "/")
	if head != "" && !heads[head] {
		heads[head] = true
		return true
	}
	if strings.HasSuffix(name, "/") {
		return false
	}
	base := path.Base(name)
	if base == "meson.build" || strings.HasSuffix(base, ".cmake") || slices.Contains(mesonOptionFiles, base) {
		return true
	}
	for _, marker := range buildSystemMarkers {
		if base == marker.file {
			return true
		}
	}
	return false
}

// extractArchiveMember returns the bytes of one member of archive.
//
// `-O` sends the member to stdout, and that single flag is what makes this safe
// rather than merely careful: nothing is ever written to disk, so archive path
// traversal has no surface to act on instead of being something a check must
// catch. It is also why a validate run leaves the overlay byte-identical by
// construction rather than by discipline.
//
// # One member per call, deliberately
//
// The design describes the invocation as `tar -xOf <archive> <member>…`, and
// tar does accept several. The output of that form is the members' bytes
// CONCATENATED with no separator and in tar's storage order, so a caller cannot
// tell where one file ends and the next begins — and the caller here has to,
// because it attributes each option to the member it was declared in. One
// invocation per member costs one extra index pass and buys an unambiguous
// answer.
func extractArchiveMember(ctx context.Context, archive, member string) ([]byte, error) {
	if err := validMemberName(member); err != nil {
		return nil, fmt.Errorf("refusing to extract from %s: %w", archive, err)
	}
	// "--" terminates option parsing, so even a name that slipped past the check
	// above could not become a flag. The check is still the contract — it fails
	// loudly and before exec, where this only fails safely.
	return runTar(ctx, archive, "extracting "+member, "-xOf", archive, "--", member)
}

// validMemberName rejects the two shapes that could turn a member name, which
// comes from inside an archive we do not control, into a tar option.
func validMemberName(member string) error {
	if member == "" {
		return errors.New("empty member name")
	}
	if strings.HasPrefix(member, "-") {
		return fmt.Errorf("member name %q begins with '-' and could be read as a flag", member)
	}
	return nil
}

// runTar executes one tar invocation and returns its stdout, capped.
//
// what names the operation for the error message ("listing", "extracting X"),
// so a failure says which of the two passes broke without the caller having to
// re-wrap it. Every error carries the archive path and tar's own stderr: those
// two are what makes a failure reproducible, and this gate reports a failure it
// cannot explain as a SKIPPED outcome that quotes the diagnostic.
func runTar(ctx context.Context, archive, what string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, archiveTimeout)
	defer cancel()

	cmd := execCommand(ctx, "tar", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s %s: opening tar stdout: %w", what, archive, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s %s: starting tar: %w", what, archive, err)
	}

	// Reading one byte past the cap is what distinguishes "exactly at the cap"
	// from "over it" without buffering the overflow.
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxArchiveBytes+1))
	if len(out) > maxArchiveBytes {
		// tar is still writing into a pipe nobody will drain, so it has to be
		// killed rather than waited for. The Wait is still required — it reaps
		// the child — and its error is the kill we just caused, not a diagnosis.
		cancel()
		_ = cmd.Wait()
		return nil, fmt.Errorf("%s %s: %w (%d bytes)", what, archive, ErrArchiveTooLarge, maxArchiveBytes)
	}
	if readErr != nil {
		_ = cmd.Wait()
		return nil, fmt.Errorf("%s %s: reading tar output: %w", what, archive, readErr)
	}

	if err := cmd.Wait(); err != nil {
		// tar's diagnostics are flattened to ONE line. They routinely run to
		// four or five ("Error is not recoverable", "Child returned status 2",
		// …), and this error becomes an EbuildResult.Reason, which the text
		// report prints as the line under its ebuild. A multi-line reason
		// breaks that layout and every line-oriented reader of it.
		if diag := flattenDiagnostic(stderr.String()); diag != "" {
			return nil, fmt.Errorf("%s %s: tar failed: %w: %s", what, archive, err, diag)
		}
		return nil, fmt.Errorf("%s %s: tar failed: %w", what, archive, err)
	}
	return out, nil
}

// flattenDiagnostic collapses an external tool's stderr onto one line.
//
// Every diagnostic in this package ends up in an EbuildResult.Reason, and the
// text report prints a reason as the single line under its ebuild. tar and
// pkgcheck both emit several lines when they fail, so without this a skip
// silently becomes five records where the report expects one — and every
// line-oriented consumer of that report reads four of them as findings with no
// ebuild attached.
func flattenDiagnostic(stderr string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(stderr)), " ")
}
