package snapshot

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// EmergeHookRoot is the filesystem root under which the Portage emerge-hook
// paths are WRITTEN (hook script + bashrc). It is "/" in production; tests
// point it at a temp dir — mirroring the exported StateDir seam in result.go.
//
// Note the write/runtime distinction: EmergeHookRoot only prefixes where files
// are written. The managed bashrc block always sources the hook script's
// absolute RUNTIME path (/etc/portage/bashrc.d/...), never the prefixed one,
// because that is where Portage will find it on the running system.
var EmergeHookRoot = "/"

// emergeHookRuntimeScript is the hook script's absolute path at RUNTIME — the
// path the bashrc managed block sources. Deliberately NOT prefixed with
// EmergeHookRoot (see EmergeHookRoot doc).
const emergeHookRuntimeScript = "/etc/portage/bashrc.d/50-bentoo-snapshot.sh"

// Managed-block markers in /etc/portage/bashrc. Exact strings — the uninstall
// strip and the idempotent re-install both match on them.
const (
	emergeHookBlockBegin = "# >>> bentoo snapshot hook >>>"
	emergeHookBlockEnd   = "# <<< bentoo snapshot hook <<<"
)

// emergeHookManagedLine heads every file/block this command owns, telling
// anyone inspecting /etc/portage how it got there and how to remove it.
const emergeHookManagedLine = "# Managed by 'bentoo snapshot hook' — do not edit; use --uninstall to remove."

// emergeHookScript is the drop-in sourced by /etc/portage/bashrc for every
// ebuild phase. Defining functions named pre_pkg_setup/post_pkg_postinst makes
// Portage call them around those phases; ${T} is the per-package temp dir,
// shared across the phases of one package build, so it hands the pre-snapshot
// number from the pre hook to the post hook.
//
// The pre/post pair is created with --cleanup-algorithm number: snapper's
// number cleanup prunes exactly these snapshots, governed by the
// NUMBER_CLEANUP="yes" key bentoo manages in the snapper config (see
// managedSnapperKeys in snapper_config.go).
//
// Guards: a missing snapper binary or a failing snapper call must NEVER break
// an emerge — every snapper invocation is best-effort (`|| return 0` /
// `|| true`).
const emergeHookScript = emergeHookManagedLine + `
#
# Portage phase hooks: snapper pre/post snapshot pair around each emerged
# package. Pairs use --cleanup-algorithm number and are pruned by snapper's
# number cleanup (NUMBER_CLEANUP="yes" in the bentoo-managed snapper config).

pre_pkg_setup() {
	command -v snapper >/dev/null 2>&1 || return 0
	snapper -c root create --type pre --cleanup-algorithm number \
		--print-number --description "bentoo: emerge ${CATEGORY}/${PF}" \
		> "${T}/bentoo-snapper-pre" 2>/dev/null || true
}

post_pkg_postinst() {
	command -v snapper >/dev/null 2>&1 || return 0
	[ -f "${T}/bentoo-snapper-pre" ] || return 0
	local pre_number
	pre_number="$(cat "${T}/bentoo-snapper-pre" 2>/dev/null)"
	[ -n "${pre_number}" ] || return 0
	snapper -c root create --type post --pre-number "${pre_number}" \
		--cleanup-algorithm number \
		--description "bentoo: emerge ${CATEGORY}/${PF}" \
		>/dev/null 2>&1 || true
}
`

// emergeHookBashrcBlock is the managed block ensured in /etc/portage/bashrc:
// a guarded source of the hook script's runtime path, delimited by the exact
// markers so it can be replaced in place and stripped on uninstall.
const emergeHookBashrcBlock = emergeHookBlockBegin + "\n" +
	emergeHookManagedLine + "\n" +
	"[ -f " + emergeHookRuntimeScript + " ] && source " + emergeHookRuntimeScript + "\n" +
	emergeHookBlockEnd + "\n"

// emergeHookScriptPath is where the hook script is WRITTEN: under EmergeHookRoot.
func emergeHookScriptPath() string {
	return filepath.Join(EmergeHookRoot, "etc", "portage", "bashrc.d", "50-bentoo-snapshot.sh")
}

// emergeBashrcPath is where the bashrc is WRITTEN: under EmergeHookRoot.
func emergeBashrcPath() string {
	return filepath.Join(EmergeHookRoot, "etc", "portage", "bashrc")
}

// InstallEmergeHook installs the opt-in Portage emerge hook (R4.1): it writes
// the snapper pre/post hook script to /etc/portage/bashrc.d and ensures
// /etc/portage/bashrc sources it via the managed block. Idempotent: re-running
// rewrites the script and replaces the existing block in place — user bashrc
// content outside the block is preserved byte-for-byte and the block is never
// duplicated.
//
// It is called exclusively by the explicit `bentoo snapshot hook --install`
// verb — never by apply (R4.3).
func InstallEmergeHook() error {
	script := emergeHookScriptPath()
	// MkdirAll with the conventional /etc perms first; atomicWrite's internal
	// MkdirAll (0o750) then no-ops on the already-existing directories.
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		return fmt.Errorf("create hook dir %s: %w", filepath.Dir(script), err)
	}
	if err := atomicWrite(script, []byte(emergeHookScript), 0o644); err != nil {
		return fmt.Errorf("write hook script %s: %w", script, err)
	}
	return ensureEmergeBashrcBlock()
}

// UninstallEmergeHook removes the Portage emerge hook (R4.2): it deletes the
// hook script and strips the managed block from /etc/portage/bashrc, leaving
// user content intact. A bashrc left empty (or whitespace-only) is removed.
// Absent files are a clean no-op so uninstall succeeds on a never-installed
// system.
func UninstallEmergeHook() error {
	script := emergeHookScriptPath()
	if err := os.Remove(script); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove hook script %s: %w", script, err)
	}

	bashrc := emergeBashrcPath()
	existing, err := os.ReadFile(bashrc)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // never installed (or already removed): clean no-op
		}
		return fmt.Errorf("read bashrc %s: %w", bashrc, err)
	}

	stripped := stripEmergeHookBlock(existing)
	if len(bytes.TrimSpace(stripped)) == 0 {
		// Nothing but our block (plus whitespace) lived there: remove the file
		// rather than leave an empty bashrc behind.
		if err := os.Remove(bashrc); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove empty bashrc %s: %w", bashrc, err)
		}
		return nil
	}
	if err := atomicWrite(bashrc, stripped, 0o644); err != nil {
		return fmt.Errorf("write bashrc %s: %w", bashrc, err)
	}
	return nil
}

// ensureEmergeBashrcBlock creates or updates the bashrc so it contains exactly
// one managed block sourcing the hook script: any existing block is dropped
// (replacement in place, never duplication) and a fresh one is appended, with
// every byte of user content outside the block preserved.
func ensureEmergeBashrcBlock() error {
	bashrc := emergeBashrcPath()
	existing, err := os.ReadFile(bashrc)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read bashrc %s: %w", bashrc, err)
	}

	content := stripEmergeHookBlock(existing)
	if len(content) > 0 && !bytes.HasSuffix(content, []byte("\n")) {
		content = append(content, '\n')
	}
	content = append(content, emergeHookBashrcBlock...)

	if err := atomicWrite(bashrc, content, 0o644); err != nil {
		return fmt.Errorf("write bashrc %s: %w", bashrc, err)
	}
	return nil
}

// stripEmergeHookBlock removes the managed block — the begin-marker line, the
// end-marker line, and everything between — from bashrc content. Lines outside
// the block pass through untouched, so user content round-trips byte-for-byte;
// content without a block is returned unchanged.
func stripEmergeHookBlock(content []byte) []byte {
	lines := strings.Split(string(content), "\n")
	kept := make([]string, 0, len(lines))
	inBlock := false
	for _, line := range lines {
		switch trimmed := strings.TrimSpace(line); {
		case !inBlock && trimmed == emergeHookBlockBegin:
			inBlock = true
		case inBlock && trimmed == emergeHookBlockEnd:
			inBlock = false
		case !inBlock:
			kept = append(kept, line)
		}
	}
	return []byte(strings.Join(kept, "\n"))
}
