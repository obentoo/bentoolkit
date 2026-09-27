// Package fileutil provides shared helpers for file modes and for writing files
// that survive a crash, so every write site uses one source for permissions and
// one audited sequence of temp file, sync and rename.
//
// The package logs nothing: it must not import internal/common/logger, which
// would be an import cycle, so every helper returns an error that names the
// file it concerns and leaves the logging to its caller.
package fileutil

import "os"

// CacheFileMode is the permission mode applied to cache and log files.
//
// It is intentionally restrictive (0600 — owner-only read/write) because
// cache and log files may hold secrets or sensitive upstream metadata
// (for example credentials, tokens, or repository details). Group and
// world access is denied to avoid leaking that data to other local users.
const CacheFileMode os.FileMode = 0600
