// Package secrets resolves a named secret (an API token, a serial, ...) from a
// fixed, security-conscious chain and nothing else.
//
// Resolution order (first hit wins):
//
//  1. the process environment — os.Getenv(NAME), trimmed; empty is "unset"
//  2. the user-scope file      — $XDG_CONFIG_HOME/bentoo/secrets (else ~/.config/bentoo/secrets)
//  3. the system-scope file    — /etc/bentoo/secrets
//
// The files are ".env" style: NAME=value per line, "# ..." comments, an
// optional "export " prefix, and matched surrounding quotes stripped from the
// value. A missing file is a miss, never an error; a present-but-unreadable
// user file is ErrUnreadable, while a permission error on the root-owned
// system file is a silent miss (a normal user cannot read it by design).
//
// Config files (config.yaml) are deliberately NOT part of this chain: a secret
// never belongs in a checked-in or world-readable config, so this package will
// not read them.
//
// One deliberate exception lives OUTSIDE this package: the ${VAR} header
// expansion in packages.toml is an env-only substitution and stays on
// os.Getenv by design — it must never consult the secrets files, so it does
// not route through this package.
//
// The package depends only on the Go standard library.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrUnreadable wraps the underlying cause when a secrets file exists but its
// contents cannot be read or parsed. Callers match it with errors.Is. A missing
// file is deliberately NOT this error — absence is a normal "no secret set".
var ErrUnreadable = errors.New("secrets: file present but unreadable")

// pathsFn yields the secrets file locations in resolution order — user-scope
// first, system-scope second. It is a package var so tests can point the chain
// at temp files. os.UserHomeDir() is re-read on every call so a test that sets
// HOME is honored; if the home dir cannot be determined the user-scope entry is
// dropped and only the system-scope path is returned (never a panic).
var pathsFn = func() []string {
	const systemScope = "/etc/bentoo/secrets"

	home, err := os.UserHomeDir()
	if err != nil {
		return []string{systemScope}
	}
	// Mirror config.ConfigPaths: honor $XDG_CONFIG_HOME, else ~/.config.
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	return []string{filepath.Join(xdg, "bentoo", "secrets"), systemScope}
}

// Paths returns the secrets file locations Lookup searches, in order. Callers
// use it to build actionable "set NAME or add it to one of: ..." messages. It
// reflects exactly what Lookup consults.
func Paths() []string {
	return pathsFn()
}

// Lookup resolves name across the fixed chain env → user file → system file and
// returns the first hit.
//
//   - found=false, err=nil  → not set anywhere (the normal "no secret" case)
//   - found=true,  err=nil  → value holds the resolved secret
//   - err != nil            → a file exists but could not be read or parsed
//
// The environment is consulted first: a non-empty os.Getenv(name) (trimmed) is
// returned without touching any file; an empty or whitespace-only value is
// treated as unset and falls through. A missing file is a miss, never an error
// (D1). A present-but-unreadable user-scope file — including EISDIR or a
// permission failure — yields ErrUnreadable so a chmod-000 token can never
// silently degrade to "anonymous"; a permission error on the root-owned
// system-scope file is instead a silent miss (D2).
func Lookup(name string) (value string, found bool, err error) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v, true, nil
	}

	// The FIRST path entry is user-scope; every entry after it is system-scope.
	// That position drives the error mapping (see lookupInFile).
	for i, path := range pathsFn() {
		v, hit, err := lookupInFile(path, name, i == 0)
		if err != nil {
			return "", false, err
		}
		if hit {
			return v, true, nil
		}
	}
	return "", false, nil
}

// lookupInFile reads and parses one secrets file. userScope selects the error
// mapping: an absent file is always a miss (D1); on the user-scope file any
// other read failure (EISDIR, EACCES, ...) becomes ErrUnreadable so it cannot
// silently degrade to "anonymous"; on a system-scope file a permission error is
// a silent miss because /etc/bentoo/secrets is root-owned 0600 by design and a
// normal user always gets EACCES (D2).
func lookupInFile(path, name string, userScope bool) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return "", false, nil
		case !userScope && errors.Is(err, os.ErrPermission):
			return "", false, nil
		default:
			return "", false, fmt.Errorf("%w: %s: %v", ErrUnreadable, path, err)
		}
	}
	warnIfLoose(path)
	v, hit := parseSecrets(data, name)
	return v, hit, nil
}

// parseSecrets returns the value for name from ".env"-style data. It preserves
// the semantics promoted from autoupdate.readSecretFromFile and adds two design
// rules: a leading "export " is stripped (D7) and a blank value is a miss
// (found=false), mirroring the env-empty rule. The FIRST occurrence of the key
// wins (D6). The value is split on the FIRST '=' (so "A=b=c==" keeps "b=c==")
// and matched surrounding quotes are trimmed. Lines without an '=' are skipped.
func parseSecrets(data []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != name {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		return val, val != ""
	}
	return "", false
}

// looseWarnOnce guards the loose-mode warning so it is emitted at most once per
// process (D5): a group/world-accessible secrets file is a real risk worth
// surfacing, but repeating it on every read would be noise.
var looseWarnOnce sync.Once

// warnIfLoose emits a single stderr warning when the file at path is group- or
// world-accessible (mode & 0o077 != 0). It names the path and mode but never
// the file's contents, and never blocks the read. The warning goes straight to
// os.Stderr rather than through internal/common/logger to avoid an import cycle
// (same rationale as fileutil's locally-defined Logger seam).
func warnIfLoose(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if info.Mode()&0o077 == 0 {
		return
	}
	looseWarnOnce.Do(func() {
		fmt.Fprintf(os.Stderr,
			"warning: secrets file %s is group/world-accessible (mode %#o); restrict it with: chmod 600 %s\n",
			path, info.Mode().Perm(), path)
	})
}

// Scrub removes secret from s before it is logged or wrapped into an error, so
// a transport error echoing a request URL (or any string built from the secret)
// cannot leak it. Moved verbatim from autoupdate.scrubSecret.
func Scrub(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}
