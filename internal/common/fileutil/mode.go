// Package fileutil provides shared helpers for file mode handling so that
// cache and log write-sites use a single, consistent source for permissions.
package fileutil

import (
	"errors"
	"os"
	"syscall"
)

// CacheFileMode is the permission mode applied to cache and log files.
//
// It is intentionally restrictive (0600 — owner-only read/write) because
// cache and log files may hold secrets or sensitive upstream metadata
// (for example credentials, tokens, or repository details). Group and
// world access is denied to avoid leaking that data to other local users.
const CacheFileMode os.FileMode = 0600

// Logger is the minimal logging surface fileutil needs. It is defined
// locally — rather than importing internal/common/logger — to avoid an
// import cycle. The real *logger.Logger structurally satisfies this
// interface via its Warn(format string, args ...interface{}) method.
type Logger interface {
	Warn(format string, args ...interface{})
}

// chmodFunc is the chmod implementation used by SafeChmod. It is a package
// variable so tests can override it to simulate filesystems that do not
// support chmod.
var chmodFunc = os.Chmod

// SafeChmod applies mode to the file at path.
//
// Some filesystems (for example certain network or FUSE mounts, or
// read-only mounts) do not support chmod and report this via the errnos
// syscall.EOPNOTSUPP, syscall.EPERM, or syscall.EROFS. os.Chmod returns
// these wrapped in an *os.PathError, so they are detected with errors.Is.
// When such an error occurs, SafeChmod swallows it, emits exactly one Warn
// line through log, and returns nil — tightening permissions is best-effort
// on filesystems that cannot honor it. Any other error is returned as-is.
func SafeChmod(path string, mode os.FileMode, log Logger) error {
	err := chmodFunc(path, mode)
	if err == nil {
		return nil
	}

	if errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EROFS) {
		log.Warn("could not set permissions on %s: filesystem does not support chmod: %v", path, err)
		return nil
	}

	return err
}
