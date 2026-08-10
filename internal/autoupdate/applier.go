// Package autoupdate provides update application functionality for ebuild autoupdate.
package autoupdate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/fileutil"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// fixSandboxRoot is the seam the LLM manifest fixer's private distdir is created
// under, a variable for the same reason resolveDistdir is one in sweep.go: the
// production value asks the host a question, and a test must be able to answer it
// without a portageq on the machine running the suite.
//
// It returns a ROOT for os.MkdirTemp, not a directory to use — "" is a valid
// answer and means "os.TempDir()", which is what a host without portageq gets.
//
// Only tests replace it.
var fixSandboxRoot = distfiles.TempRoot

// manifestTimeout bounds a single `pkgdev manifest` invocation. The manifest
// step touches the network (fetching SRC_URI distfiles to digest), so it gets a
// generous-but-finite budget; without it a stalled fetch would hang Apply
// indefinitely.
const manifestTimeout = 5 * time.Minute

// qaCheckTimeout bounds the advisory `pkgcheck scan` run after an LLM fix. It is
// a local, read-only lint of a single package, so a tight budget is enough; the
// scan is best-effort and never blocks the apply.
const qaCheckTimeout = 2 * time.Minute

// Error variables for applier errors
var (
	// ErrEbuildNotFound is returned when the source ebuild file is not found
	ErrEbuildNotFound = errors.New("source ebuild file not found")
	// ErrManifestFailed is returned when the manifest command fails
	ErrManifestFailed = errors.New("manifest command failed")
	// ErrCompileFailed is returned when the compile test fails
	ErrCompileFailed = errors.New("compile test failed")
	// ErrNoPrivilegeEscalation is returned when neither sudo nor doas is available
	ErrNoPrivilegeEscalation = errors.New("no privilege escalation tool available (sudo or doas)")
	// ErrUserDeclined is returned when user declines the compile confirmation
	ErrUserDeclined = errors.New("user declined compile test")
	// ErrInvalidNewVersion is returned when the detected upstream version cannot
	// be coerced into a well-formed Gentoo PV (e.g. it carries a tag prefix that
	// survives normalization, or is not a version at all).
	ErrInvalidNewVersion = errors.New("invalid new version for ebuild")
	// ErrObsoletePending is returned (wrapped) when a pending entry no longer
	// matches the live overlay: the package was removed, or the overlay is
	// already at/beyond the target version. The entry is pruned and the outcome
	// is reported as obsolete, not as a failure.
	ErrObsoletePending = errors.New("obsolete pending entry")
	// ErrEbuildExists is returned when the destination ebuild a copy would write
	// is already present in the overlay. Overwriting it would destroy a file the
	// applier never authored — see copyEbuild's guard for why this is fatal
	// rather than a silent overwrite.
	ErrEbuildExists = errors.New("destination ebuild already exists")
)

// ApplyResult represents the result of applying an update.
type ApplyResult struct {
	// Package is the full package name (category/package)
	Package string
	// OldVersion is the version before the update
	OldVersion string
	// NewVersion is the version after the update
	NewVersion string
	// Success indicates whether the apply operation succeeded
	Success bool
	// Error contains any error that occurred during application
	Error error
	// LogPath is the path to the compile log if compilation failed
	LogPath string
	// IsolationVerified reports whether the compile gate PROVED it could create
	// a network namespace before building. It is false when the compile did not
	// run at all.
	//
	// It exists because a compile-gate pass used to claim more than it had. The
	// gate never checked: Portage reports network-sandbox in FEATURES, creating
	// the namespace needs privilege an ordinary user does not have, and Portage
	// says nothing when the unshare fails. So a green could mean "built with the
	// network cut off" or "built with full network access", and nothing printed
	// told them apart.
	IsolationVerified bool
	// IsolationReason says why isolation could not be verified, and is empty
	// exactly when IsolationVerified is true. On a run skipped by
	// --require-isolation it is the reason the compile never happened.
	IsolationReason string
	// CleanedOldVersion is the highest version whose ebuild --clean removed. It
	// is the single-version view of CleanRemoved, kept because callers that
	// print one "Removed: pkg-X.ebuild" line predate the sweep; empty when clean
	// was off, removed nothing, or was blocked.
	CleanedOldVersion string
	// CleanKept maps each version --clean left in place to the registry entry
	// key that claims it (R6.1). An empty value means the version was kept by a
	// rule rather than by an entry — the live -9999 rule or the last-non-live
	// floor (R4.3) — since a registry key is never itself empty.
	CleanKept map[string]string
	// CleanRemoved lists the versions whose ebuilds --clean actually deleted,
	// ascending. It is the executed plan, not the intended one: a sweep stopped
	// by a removal failure reports the prefix it managed to delete.
	CleanRemoved []string
	// CleanWarning records a non-fatal failure of the --clean step (the update
	// itself still succeeded). Empty on success.
	CleanWarning string
	// RegistryWarning records a non-fatal failure to write the applied version
	// back into packages.toml (S021-R2.4). It is deliberately NOT CleanWarning:
	// the two report different steps — the pin is written on every successful
	// apply, the sweep only under --clean — and the CLI prints CleanWarning on a
	// line labelled "Clean:", so reusing it would blame the wrong step for a
	// registry failure. Empty when the pin was recorded.
	RegistryWarning string
	// Obsolete indicates the pending entry no longer corresponds to anything to
	// apply: the package was removed from the overlay, or the overlay is already
	// at/beyond the target version. The entry is pruned from pending.json and
	// this is NOT counted as a failure (Success stays false, Error stays nil).
	Obsolete bool
	// Held indicates the package carries hold = true in packages.toml and was
	// therefore refused. Like Obsolete this is NOT a failure (Success false,
	// Error nil), but unlike Obsolete the pending entry is KEPT: the update is
	// real and still pending, it just may not be applied automatically. A held
	// package reaches this point at all because an explicit
	// `--check <pkg> --force` bypasses the checker's hold filter and records the
	// update, so the entry is already in pending.json by the time `--apply all`
	// walks it.
	Held bool
	// ObsoleteReason explains, in user-facing terms, why the entry was deemed
	// obsolete. Empty unless Obsolete is true.
	ObsoleteReason string
	// Fixed indicates a gate failed and was recovered by an LLM fixer: the ebuild
	// was edited and BENTOO'S OWN re-run of that same gate then succeeded. Two
	// gates can set it — the manifest step (`pkgdev manifest`) and, since story
	// 033, the build gate — and in both cases the flag records the re-run's
	// verdict, never the agent's self-report (S033-R8.2). A bump whose re-run
	// still failed leaves this false, because a "fixed" flag on something still
	// broken is worse than no flag. Only meaningful on the success path.
	Fixed bool
	// FixSummary is the fixer's one-line description of what it changed in the
	// ebuild. Empty unless Fixed is true.
	FixSummary string
	// QASummary holds advisory `pkgcheck` output captured after an LLM fix, when
	// pkgcheck is available. It never changes Success — the apply already passed
	// the authoritative manifest re-run — but surfaces any QA findings the agent's
	// edit may have introduced so a human can review before committing. Empty when
	// pkgcheck is absent, reported nothing, or no fix was applied.
	QASummary string
	// StagedPath is the staged tree this bump was validated in: the
	// single-package repository validate.Stage built, which by construction lives
	// OUTSIDE the published overlay.
	//
	// It is set as soon as the tree exists and kept through every failure, because
	// it is what makes a failure inspectable (S033-R3.6): the operator can read the
	// exact ebuild the gates read and re-run a gate by hand, without repeating the
	// work that produced it. Retention is expressed by the path itself — one tree
	// per package and version — so there is no index to consult and nothing to
	// unlock. The reports name it: applySummary appends it to the failure line, and
	// the CLI prints it under "Staged:".
	//
	// A COMPLETED promotion clears it again. The staged tree has served its purpose
	// once its bytes are in the overlay, and naming a path beside a success invites
	// the operator to go and read a tree that says nothing the overlay does not
	// already say — while the field's meaning stays the single one worth carrying:
	// "here is the evidence of what went wrong".
	//
	// Empty, therefore, in three cases: a successful apply, an apply that ran
	// without a staging root, and one that failed before staging.
	StagedPath string
	// DepthRequested is how far this bump was ASKED to be validated: the depth
	// class, tier, configuration and the operator's flags resolved to (S033-R2,
	// R2.2, R2.7), raised by a reviewer escalation where one applied (R7.5).
	//
	// It is a string and not a validate.Depth for the reason
	// validate.EbuildResult states: Depth is an int whose ORDERING is its
	// contract, so it would reach a report as a number and force every reader to
	// carry the ladder to interpret it. The spelling here is the one `--depth`
	// accepts and Depth.String prints.
	DepthRequested string
	// DepthReached is how far validation ACTUALLY got — the deepest rung whose
	// own gate reported PASS, never deeper than DepthRequested.
	//
	// The pair is the R4 rule ("an outcome names its own reach") applied to the
	// ladder itself: a bump promoted with its build gates skipped for want of an
	// installed dependency has DepthRequested "configure" and DepthReached
	// short of it, so nobody can read the green as "it builds" (R3.12).
	DepthReached string
	// DepthReason names the input that decided the depth, and — when the two
	// depths differ — why validation stopped short, naming the atoms or the host
	// condition that stopped it. It is never empty on a staged apply.
	DepthReason string
	// ValidationSource says which of R10's two paths this bump took: "staged"
	// when a retained tree that had already been proved was promoted as it stood
	// (R10.1), "this-run" when the gates ran here (R10.2). It is R10.3 — state
	// per package which of the two happened — carried on the result so the
	// reports and the summary line can both say it.
	//
	// Empty on an apply that ran with no staging root at all, because on that
	// path no gate runs and neither answer would be true. The two constants are
	// ValidationSourceStaged and ValidationSourceThisRun.
	ValidationSource string
}

// Applier handles update application for packages.
// It coordinates between pending list and file system operations.
type Applier struct {
	// overlayPath is the path to the overlay directory
	overlayPath string
	// pending manages pending updates
	pending *PendingList
	// logsDir is the directory for storing compile logs
	logsDir string
	// confirmFunc is a function to prompt for user confirmation (injectable for testing)
	confirmFunc func(prompt string) bool
	// execCommand is a function to create exec.Cmd bound to a context
	// (injectable for testing). It defaults to exec.CommandContext so a
	// cancelled context kills the spawned manifest/compile process.
	execCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd
	// ctx is the parent context for all spawned external commands. It is set
	// via WithApplierContext and originates in cmd/ (signal.NotifyContext), so a
	// SIGINT or deadline kills in-flight ebuild/compile processes. Defaults to
	// context.Background().
	ctx context.Context
	// pendingDeleteFn is the function Apply invokes to remove a package from
	// pending.json after the full success path (S002-R3.1). It defaults to
	// a.pending.Delete and is overridable via WithApplierPendingDeleteFunc
	// purely for tests that need to simulate a Delete failure (S002-R3.4).
	// Production callers never supply this option.
	pendingDeleteFn func(pkg string) error
	// setVersionsFn is the function Apply invokes to record the version it just
	// applied in the overlay's registry (S021-R2.1). It defaults to
	// SetPackageVersions and is overridable via WithApplierSetVersionsFunc
	// purely for tests that need to force a write failure without a real
	// registry (S021-R2.4). Production callers never supply this option.
	setVersionsFn func(overlayPath string, pins map[string]string) error
	// isolationProbe measures whether this process can create a network
	// namespace, so a compile-gate pass can state the fidelity it actually
	// verified rather than the one Portage's FEATURES implies. Defaults to
	// validate.ProbeIsolation; injectable via WithApplierIsolationProbe.
	isolationProbe func() (bool, string)
	// requireIsolation, when true, makes the compile gate SKIP rather than run
	// unisolated: an unisolated compile after the operator asked for isolation
	// produces exactly the meaningless green they asked to avoid. Set via
	// WithApplierRequireIsolation, which is the only way in: whether the value
	// came from the --require-isolation flag or from the config key story 033
	// added for it (autoupdate.validate.require_isolation, default false), it
	// arrives through that option, so this field stays the single input the
	// gate reads.
	requireIsolation bool
	// clean, when true, makes a successful Apply remove the previous version's
	// ebuild and regenerate the Manifest so only the freshly created version
	// remains. Set via WithApplierClean (the --clean / -c CLI flag).
	clean bool
	// configs holds the per-package autoupdate configuration, keyed exactly as
	// packages.toml is — "category/package", or "category/package:slot" for a
	// multi-slot package. It is consulted for the optional [meta] block that
	// drives an authenticated distfile fetch (serial-gated downloads) and for
	// the slot's `revision`; packages without either follow the normal
	// pkgdev-from-SRC_URI path with a plain PV. Set via
	// WithApplierPackagesConfig; nil disables authenticated fetching entirely.
	configs map[string]PackageConfig
	// fixer, when non-nil, is invoked when the manifest step fails: it drives an
	// LLM agent to repair the ebuild (e.g. a SRC_URI whose URL convention changed
	// between versions) before the Applier re-runs the manifest to confirm. Set
	// via WithApplierFixer; nil keeps the original fail-fast behaviour.
	fixer ManifestFixer
	// buildFixer, when non-nil, is invoked when the build gate fails for a reason
	// attributable to the ebuild (a patch that no longer applies, a configure
	// option upstream dropped): it drives an LLM agent to repair the STAGED ebuild,
	// after which the applier re-runs the same gate and that re-run — never the
	// agent's self-report — decides the outcome (S033-R8.1, S033-R8.2). Set via
	// WithApplierBuildFixer; nil keeps the original fail-fast behaviour.
	buildFixer BuildFixer
	// reporter is the progress sink Apply emits its lifecycle to (TaskStart →
	// TaskStage → TaskDone). Set via WithApplierReporter; defaults to tui.Noop()
	// so the silent, fully-buffered behaviour predating the TUI is preserved and
	// every existing test stays byte-identical (S010-R3.3).
	reporter tui.Reporter
	// runAttached executes the compile-test command and returns its combined
	// output. The privileged child needs the REAL terminal for the sudo/doas
	// password prompt (S010-R4.1), which rules out capturing its stdout/stderr through
	// a StreamCapture pipe the way runManifest does. The default (set in
	// NewApplier) is exactly cmd.CombinedOutput, so the compile-log path stays
	// byte-identical to the pre-TUI behaviour (S010-R3.3/S010-R7.1). The apply driver
	// (sub-task 4.1) overrides it via WithApplierRunAttached to release the
	// terminal for the prompt and tee the raw output to the TTY and a capture
	// buffer. A nil override is normalized back to the CombinedOutput default.
	runAttached func(cmd *exec.Cmd) ([]byte, error)
	// distdir, configuredDistdir and distfilesCache are carried, unread, from
	// the CLI to the sweeper this applier builds for the Manifest step: the
	// --distdir flag, autoupdate.distdir, and the read-only cache named by
	// --distfiles-cache / autoupdate.distfiles_cache (S030-R1.3). Nothing on
	// Applier interprets them — see sweeper() and runManifest, which own the
	// precedence — and all three empty is the production default that lands on
	// the host's own DISTDIR with no cache lookup.
	distdir           string
	configuredDistdir string
	distfilesCache    string
	// stagingRoot is the directory the staged trees are built under — in
	// production <configDir>/staging (S033-D1). Non-empty is what turns the whole
	// staged pipeline on: the candidate is materialised there instead of in the
	// published overlay, every gate reads it there, and the overlay is written
	// exactly once, by promotion, at the end (S033-R3.2).
	//
	// Empty is the pre-staging path, byte for byte what every release before story
	// 033 did, and it is what a caller that omits WithApplierStagingRoot gets. The
	// three construction sites in cmd/ are wired by sub-task 12.1, which is also
	// where the gates that make the staged path worth taking are inserted.
	stagingRoot string
	// validatePolicy is the configured depth table and its per-package
	// exceptions, translated out of autoupdate.validate by whoever built the
	// Applier (validate.DepthPolicy documents why the translation is the
	// caller's job). The zero value is not "no validation": classDepth answers
	// the deepest rung for a class it has no row for, because failing to
	// configure a depth is not evidence that a bump is small.
	validatePolicy validate.DepthPolicy
	// flagDepth is `--depth`, nil when the operator did not type it. It is a
	// POINTER because DepthNone is a depth an operator may legitimately ask for,
	// and a zero value indistinguishable from "unset" is the one confusion that
	// switches validation off in silence (validate.DepthRequest.FlagDepth).
	flagDepth *validate.Depth
	// requireProof refuses to promote a bump whose build gates were SKIPPED
	// (S033-R3.13). It is the opt-in counterweight to R3.12: false — the default
	// — publishes such a bump with the depth it did not reach named, because a
	// host that lacks a build dependency says nothing about the bump and
	// refusing every one of them would make the feature inert on an ordinary
	// workstation.
	requireProof bool
	// reviewer, when non-nil, reads the two versions' build-declaration
	// difference and may ask for MORE validation than policy chose (S033-R7,
	// R7.5). Its proposal is advisory and one-way: validate.Escalate combines it
	// with the policy floor and can only raise. Set via WithApplierBumpReviewer;
	// nil skips the review entirely.
	reviewer BumpReviewer
	// lookPath answers "is this tool installed at all" for the build gates,
	// which ask it before spawning so an absent Portage is reported as a named
	// SKIP rather than as an opaque failure. It defaults to exec.LookPath and is
	// replaced together with execCommand — see WithExecCommand for why the two
	// are one seam and not two.
	lookPath func(name string) (string, error)
}

// ApplierOption is a functional option for configuring Applier
type ApplierOption func(*Applier)

// WithApplierPendingList sets a custom pending list for the applier
func WithApplierPendingList(pending *PendingList) ApplierOption {
	return func(a *Applier) {
		a.pending = pending
	}
}

// WithLogsDir sets a custom logs directory for the applier
func WithLogsDir(dir string) ApplierOption {
	return func(a *Applier) {
		a.logsDir = dir
	}
}

// WithConfirmFunc sets a custom confirmation function for the applier
func WithConfirmFunc(fn func(prompt string) bool) ApplierOption {
	return func(a *Applier) {
		a.confirmFunc = fn
	}
}

// WithApplierIsolationProbe replaces the network-namespace probe the compile
// gate measures with. It defaults to validate.ProbeIsolation; tests supply
// their own so both answers are reachable without privilege and without root.
func WithApplierIsolationProbe(fn func() (bool, string)) ApplierOption {
	return func(a *Applier) {
		if fn != nil {
			a.isolationProbe = fn
		}
	}
}

// WithApplierRequireIsolation makes the compile gate REFUSE to run unisolated.
//
// Story 031 kept this out of configuration (its Constraints): a registry key is
// expensive to move once written, and the block that would own the setting did
// not exist yet. Story 033 added that block — autoupdate.validate.require_isolation
// in config.yaml, where an unknown key is a warning rather than a silently
// disabled record, defaulting to false so 031's behaviour is unchanged
// (S033-R6.6). This option remains the only way the value reaches the Applier,
// from the flag or from that key alike.
func WithApplierRequireIsolation(require bool) ApplierOption {
	return func(a *Applier) {
		a.requireIsolation = require
	}
}

// WithExecCommand sets a custom context-aware exec.Command function for testing.
// The function mirrors exec.CommandContext so injected commands also observe
// context cancellation.
//
// It replaces the PATH LOOKUP as well, and the two are deliberately one seam.
// The build gates ask whether `ebuild` and `emerge` exist before spawning them,
// so that a host with no Portage reports a named SKIP instead of an opaque
// failure. Leaving that question on the real PATH while the command itself is
// substituted would let the HOST decide whether the substituted child ever runs
// — `ebuild` exists on a Gentoo box and nowhere else — so the same code would
// take different branches on different machines for reasons that have nothing to
// do with the bump under test. A caller that has replaced how a child process is
// CREATED has replaced the process layer entire; this makes that true.
func WithExecCommand(fn func(ctx context.Context, name string, arg ...string) *exec.Cmd) ApplierOption {
	return func(a *Applier) {
		if fn == nil {
			return
		}
		a.execCommand = fn
		a.lookPath = func(name string) (string, error) { return name, nil }
	}
}

// WithApplierContext sets the parent context for the applier. The context is
// threaded into every spawned external command (pkgdev manifest, compile test),
// so cancelling it (e.g. on SIGINT or a deadline) kills in-flight processes.
// A nil context is ignored, leaving the default context.Background().
func WithApplierContext(ctx context.Context) ApplierOption {
	return func(a *Applier) {
		if ctx != nil {
			a.ctx = ctx
		}
	}
}

// WithApplierPendingDeleteFunc overrides the function Apply invokes to remove
// a package from pending.json after a successful apply (S002-R3.1). The default is
// a.pending.Delete. This option exists for tests that need to simulate a
// Delete failure (S002-R3.4); a nil fn is ignored.
func WithApplierPendingDeleteFunc(fn func(pkg string) error) ApplierOption {
	return func(a *Applier) {
		if fn != nil {
			a.pendingDeleteFn = fn
		}
	}
}

// WithApplierSetVersionsFunc overrides the function Apply invokes to record the
// applied version in packages.toml after a successful apply (S021-R2.1). The
// default is SetPackageVersions. This option exists for tests that need to
// simulate a write failure (S021-R2.4) or to observe the pin without a registry
// on disk; a nil fn is ignored. Production callers never supply it.
func WithApplierSetVersionsFunc(fn func(overlayPath string, pins map[string]string) error) ApplierOption {
	return func(a *Applier) {
		if fn != nil {
			a.setVersionsFn = fn
		}
	}
}

// WithApplierClean enables removal of the previous version's ebuild after a
// successful apply, leaving only the newly created version (and pruning the
// Manifest's now-orphaned distfile entries). Mirrors the --clean / -c CLI flag.
func WithApplierClean(clean bool) ApplierOption {
	return func(a *Applier) {
		a.clean = clean
	}
}

// WithApplierPackagesConfig supplies the per-package autoupdate config so the
// applier can honour a package's [meta] authenticated-fetch instructions before
// running the manifest step. A nil config (or one without a matching package)
// leaves the normal pkgdev-from-SRC_URI behaviour unchanged.
func WithApplierPackagesConfig(cfg *PackagesConfig) ApplierOption {
	return func(a *Applier) {
		if cfg != nil {
			a.configs = cfg.Packages
		}
	}
}

// WithApplierFixer wires an LLM manifest fixer into the applier. When the manifest
// step fails, the applier asks the fixer to repair the ebuild and then re-runs the
// manifest to confirm. A nil fixer is ignored, preserving the fail-fast behaviour.
func WithApplierFixer(fixer ManifestFixer) ApplierOption {
	return func(a *Applier) {
		if fixer != nil {
			a.fixer = fixer
		}
	}
}

// WithApplierBuildFixer wires an LLM build fixer into the applier. When the build
// gate fails for a reason attributable to the ebuild, the applier asks the fixer
// to repair the STAGED ebuild and then re-runs the same gate to decide
// (S033-R8.1, S033-R8.2). A nil fixer is ignored, preserving the fail-fast
// behaviour.
//
// The nil discipline is WithApplierFixer's, deliberately and to the letter: a
// provider that could not be constructed leaves the applier exactly as it was
// rather than half-configured, so "no LLM was asked for" and "the LLM could not
// be built" produce the same, predictable run.
func WithApplierBuildFixer(fixer BuildFixer) ApplierOption {
	return func(a *Applier) {
		if fixer != nil {
			a.buildFixer = fixer
		}
	}
}

// WithApplierReporter wires a progress reporter into the applier so Apply emits
// its lifecycle (TaskStart → TaskStage → TaskDone) to the TUI/plain sink. A nil
// reporter is normalized to tui.Noop(), preserving the silent, fully-buffered
// behaviour predating the TUI (S010-R3.3).
func WithApplierReporter(r tui.Reporter) ApplierOption {
	return func(a *Applier) {
		if r == nil {
			r = tui.Noop()
		}
		a.reporter = r
	}
}

// WithApplierRunAttached overrides how the compile test executes. The default
// (cmd.CombinedOutput) buffers the child's output, which is byte-identical to the
// pre-TUI behaviour (S010-R3.3/S010-R7.1) but swallows the sudo/doas password prompt. The
// apply driver (sub-task 4.1) supplies a variant that hands the child the real
// terminal so the prompt is visible (S010-R4.1) while teeing its raw output to the TTY
// and a capture buffer (the captured bytes still feed saveCompileLog on failure,
// S010-R7.1). A nil fn is normalized back to the CombinedOutput default.
func WithApplierRunAttached(fn func(cmd *exec.Cmd) ([]byte, error)) ApplierOption {
	return func(a *Applier) {
		if fn == nil {
			fn = func(c *exec.Cmd) ([]byte, error) { return c.CombinedOutput() }
		}
		a.runAttached = fn
	}
}

// WithApplierDistdir supplies the two configurable rungs of the distdir the
// Manifest step gives pkgdev: explicit is the --distdir flag, configured is
// autoupdate.distdir from the config file (S030-R1.3). Both empty — what a
// caller that omits this option gets — is the production default: the DISTDIR
// the host's own package manager reports (S030-R1.2).
func WithApplierDistdir(explicit, configured string) ApplierOption {
	return func(a *Applier) {
		a.distdir = explicit
		a.configuredDistdir = configured
	}
}

// WithApplierDistfilesCache supplies the read-only distfiles cache the Manifest
// step reuses already downloaded files from (--distfiles-cache /
// autoupdate.distfiles_cache). Empty disables the lookup, and empty is what a
// caller that omits this option gets.
func WithApplierDistfilesCache(dir string) ApplierOption {
	return func(a *Applier) { a.distfilesCache = dir }
}

// WithApplierStagingRoot points the applier at the directory its staged trees are
// built under, and by doing so turns the staged pipeline on: the candidate is
// materialised outside the published overlay, every gate reads it there, and the
// overlay is written exactly once, by promotion, after the gates have passed
// (S033-R3.2, S033-R3.3).
//
// An empty (or blank) root is ignored, exactly as WithApplierFixer ignores a nil
// fixer and for the same reason: the caller that does not supply one keeps the
// behaviour it has always had — the candidate written straight into the overlay
// and rolled back on failure — rather than getting a half-configured pipeline. The
// production root is <configDir>/staging (S033-D1); Stage never picks one itself,
// because the path is where the retention rule is recorded.
func WithApplierStagingRoot(dir string) ApplierOption {
	return func(a *Applier) {
		if root := strings.TrimSpace(dir); root != "" {
			a.stagingRoot = root
		}
	}
}

// WithApplierValidatePolicy supplies the configured depth table and its
// per-package exceptions, which is how far each class of bump is validated
// (S033-R2, R2.2, R2.4).
//
// The policy is expressed in the validate package's own types rather than in
// config's strings, so translating `autoupdate.validate` — including rejecting a
// mistyped depth by name through validate.ParseDepth — belongs to the caller. A
// zero policy is not "no validation": a class with no configured row falls
// through to the deepest rung, because failing to configure a depth is not
// evidence that a bump is small.
func WithApplierValidatePolicy(policy validate.DepthPolicy) ApplierOption {
	return func(a *Applier) {
		a.validatePolicy = policy
	}
}

// WithApplierDepth is `--depth`: the one input allowed to REPLACE the class, the
// package tier and configuration alike, in either direction (S033-R2.7).
//
// It is the only lowering that is not reported as a skip, because the operator
// typing it is looking at one package and knows something the policy does not.
// A caller that omits this option leaves the pointer nil, which is how "the
// operator asked for nothing" stays distinguishable from "the operator asked for
// none" — the one confusion that switches validation off in silence.
func WithApplierDepth(depth validate.Depth) ApplierOption {
	return func(a *Applier) {
		a.flagDepth = &depth
	}
}

// WithApplierRequireProof refuses to promote a bump whose build gates were
// SKIPPED (S033-R3.13), for a host where an unproved publish is not acceptable —
// a builder box, or a maintainer who would rather the sweep stopped than shipped
// something unbuilt.
//
// It is the opt-in counterweight to R3.12 and not its contradiction: the default
// publishes such a bump with the depth it did not reach NAMED, because "this host
// lacks a build dependency" says nothing about the bump, and turning every one of
// those into a refusal would make the feature inert for exactly the reason D11
// refuses to require isolation by default.
func WithApplierRequireProof(require bool) ApplierOption {
	return func(a *Applier) {
		a.requireProof = require
	}
}

// WithApplierBumpReviewer wires an LLM bump reviewer into the applier. It reads
// the difference between the two versions' upstream build declarations and may
// ask for MORE validation than policy chose (S033-R7, R7.5); validate.Escalate
// applies the proposal and can only ever raise the depth.
//
// A nil reviewer is ignored, exactly as WithApplierFixer ignores a nil fixer and
// for the same reason: a provider that could not be constructed leaves the
// applier as it was rather than half-configured, so "no LLM was asked for" and
// "the LLM could not be built" produce the same, predictable run.
func WithApplierBumpReviewer(reviewer BumpReviewer) ApplierOption {
	return func(a *Applier) {
		if reviewer != nil {
			a.reviewer = reviewer
		}
	}
}

// NewApplier creates a new applier instance for the given overlay.
// It initializes the pending list and logs directory.
func NewApplier(overlayPath, configDir string, opts ...ApplierOption) (*Applier, error) {
	logsDir := filepath.Join(configDir, "logs")

	applier := &Applier{
		overlayPath: overlayPath,
		logsDir:     logsDir,
		confirmFunc: defaultConfirmFunc,
		execCommand: exec.CommandContext,
		// SAFE: the real measurement; replaced by WithApplierIsolationProbe in
		// tests so both answers are reachable without privilege.
		isolationProbe: validate.ProbeIsolation,
		// SAFE: the real PATH lookup the build gates ask before spawning;
		// replaced together with execCommand (see WithExecCommand).
		lookPath: exec.LookPath,
		ctx:      context.Background(), // SAFE: default parent; replaced by WithApplierContext when cmd/ wires signal.NotifyContext
		reporter: tui.Noop(),           // SAFE: silent default; replaced by WithApplierReporter (S010-R3.3)
		// SAFE: default == today's behaviour (CombinedOutput), so the compile-log
		// path is byte-identical (S010-R3.3/S010-R7.1); replaced by WithApplierRunAttached.
		runAttached: func(c *exec.Cmd) ([]byte, error) { return c.CombinedOutput() },
	}

	// Apply options first
	for _, opt := range opts {
		opt(applier)
	}

	// Initialize pending list if not provided
	if applier.pending == nil {
		pending, err := NewPendingList(configDir)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize pending list: %w", err)
		}
		applier.pending = pending
	}

	// Resolve the pending-delete sink: tests can inject a failing variant via
	// WithApplierPendingDeleteFunc; production defaults to the live pending
	// list's Delete method. Bound only after applier.pending is initialised.
	if applier.pendingDeleteFn == nil {
		applier.pendingDeleteFn = applier.pending.Delete
	}

	// A caller that configured no depth table gets the SHIPPED one, not an empty
	// map. The difference is not cosmetic: with an empty table every bump falls
	// through classDepth's last fail-safe to `compile`, so an Applier built
	// without the option would build every revision bump — which is neither the
	// documented default nor a cost anybody agreed to. Overrides are left alone;
	// a caller that supplied only overrides meant exactly that.
	if applier.validatePolicy.ByClass == nil {
		applier.validatePolicy.ByClass = validate.DefaultDepthPolicy().ByClass
	}

	// Same shape for the registry writer: a nil field means "production path",
	// so a caller that never passes WithApplierSetVersionsFunc gets the real
	// raw-text write into <overlay>/.autoupdate/packages.toml (S021-R2.1).
	if applier.setVersionsFn == nil {
		applier.setVersionsFn = SetPackageVersions
	}

	// Ensure logs directory exists
	if err := os.MkdirAll(applier.logsDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	return applier, nil
}

// Apply applies a pending update for a package.
// It copies the ebuild to the new version and runs the manifest command.
// If compile is true, it also runs a compile test with elevated privileges.
//
// The result is returned via a named value so a single deferred cleanup can
// observe whichever error the function ultimately surfaces (result.Error is
// kept in lockstep with the returned error on every path).
func (a *Applier) Apply(pkg string, compile bool) (result *ApplyResult, _ error) {
	result = &ApplyResult{
		Package: pkg,
	}

	// Open the task in the progress reporter and guarantee a matching TaskDone on
	// every return path (mirrors the deferred orphan-rollback below, keyed on the
	// same named result). The package name doubles as both the task id and its
	// display label. Under the default Noop reporter these are no-ops, so the
	// silent, fully-buffered behaviour is byte-identical to before (S010-R3.3).
	a.reporter.TaskStart(pkg, pkg)
	defer func() {
		if result == nil {
			a.reporter.TaskDone(pkg, false, "", "")
			return
		}
		a.reporter.TaskDone(pkg, result.Success, applySummary(result), "")
	}()

	// Refuse a held package before touching anything. hold = true is the
	// maintainer's "present, but never auto-bump" decision, and until now it was
	// enforced in the checker alone: CheckAll skips held packages, but an explicit
	// `--check <pkg> --force` does not, and it writes the update to pending.json
	// like any other. From there `--apply all` applied the very bump the hold
	// existed to prevent. The guard belongs here because this is the only place
	// every apply path passes through.
	if cfg, ok := a.configs[pkg]; ok && cfg.IsHeld() {
		result.Held = true
		if update, found := a.pending.Get(pkg); found {
			result.OldVersion = update.CurrentVersion
			result.NewVersion = update.NewVersion
		}
		return result, nil
	}

	// Get pending update
	update, found := a.pending.Get(pkg)
	if !found {
		result.Error = ErrPackageNotInPending
		return result, result.Error
	}

	result.OldVersion = update.CurrentVersion

	// Upstream version detection can carry a leading tag prefix (e.g. the git
	// tag "v9.2.0588"). A Gentoo ebuild filename requires a bare PV, so strip
	// the prefix before it reaches the filename and the manifest step; otherwise
	// `pkgdev manifest` rejects it with "does not follow correct package syntax".
	// Validate up front so a non-version (or a string still invalid after
	// stripping) fails with a clear error instead of a cryptic portage one.
	newVersion := stripVersionPrefix(strings.TrimSpace(update.NewVersion))
	if !ebuild.IsValidVersion(newVersion) {
		result.Error = fmt.Errorf("%w: %q (from %q)", ErrInvalidNewVersion, newVersion, update.NewVersion)
		if err := a.pending.SetStatus(pkg, StatusFailed, result.Error.Error()); err != nil {
			result.Error = fmt.Errorf("%w (also failed to update status: %v)", result.Error, err)
		}
		return result, result.Error
	}
	// Attach the slot's pinned revision, when the entry declares one. Upstream
	// yields a bare PV; for a slot discriminated by its revision suffix that PV
	// names the WRONG slot's ebuild, so the whole apply — copy destination,
	// manifest, compile, clean — has to run against the decorated version from
	// here on. Validation stays on the bare upstream value above; the suffix is
	// well-formed by construction.
	newVersion = applyRevision(newVersion, a.configs[pkg].Revision)
	result.NewVersion = newVersion

	// Re-resolve the current version against the live overlay rather than
	// trusting update.CurrentVersion. That field is a snapshot from check-time
	// and drifts: the overlay may have been bumped past it, or the package
	// removed entirely. Blind trust produced a cryptic "source ebuild not found"
	// when the recorded version's ebuild was already gone. Re-resolution
	// self-heals a stale current_version and lets a genuinely obsolete entry be
	// pruned with a clear outcome instead of a hard failure.
	currentVersion, err := a.resolveCurrentVersion(pkg)
	if err != nil {
		// A slot that matches nothing is a config error, not an obsolete entry:
		// the package is present, its key is wrong. Pruning would delete the
		// pending record and report success-ish, hiding the typo. Fail loudly.
		if errors.Is(err, ErrSlotNotFound) {
			result.Error = err
			if serr := a.pending.SetStatus(pkg, StatusFailed, result.Error.Error()); serr != nil {
				result.Error = fmt.Errorf("%w (also failed to update status: %v)", result.Error, serr)
			}
			return result, result.Error
		}
		// Package no longer present in the overlay (removed/renamed). The pending
		// entry is obsolete — prune it and report, not as a failure.
		return a.pruneObsolete(pkg, result,
			fmt.Errorf("%w: %s no longer in overlay (%v)", ErrObsoletePending, pkg, err))
	}
	result.OldVersion = currentVersion

	// Overlay already at or beyond the target: the update was already applied or
	// has been superseded by a newer bump. A copy would be pointless (or a
	// downgrade) — prune the stale entry instead.
	if ebuild.CompareVersions(currentVersion, newVersion) >= 0 {
		return a.pruneObsolete(pkg, result,
			fmt.Errorf("%w: overlay already at %s (target %s)", ErrObsoletePending, currentVersion, newVersion))
	}

	// R2/R2.2/R2.7: how deep this bump is validated, and on whose authority.
	// Resolved HERE, before anything is staged, for two reasons: the report can
	// then name the depth even for a bump whose tree was never built, and every
	// gate below reads one decision rather than each re-deriving its own.
	depth := a.depthFor(pkg, currentVersion, newVersion)
	result.DepthRequested = depth.Depth.String()
	result.DepthReason = depth.Reason

	// From here to promotion nothing writes into the published overlay, and
	// promotion is the last thing this function does. Everything in between is
	// preparation and gates, and R3.2 says the overlay must stay byte-identical
	// while any of them runs.
	//
	// rollbackPublished is that promise's counterweight. It stays nil until this
	// apply has actually placed something in the published overlay, and then undoes
	// exactly what was placed. It REPLACES the unconditional "remove one path"
	// rollback this function used to register right after copyEbuild: that was
	// correct while the candidate was written into the overlay first, and is wrong
	// the moment the candidate lives in a staged tree, where the published path does
	// not exist yet and the tree that does exist must be RETAINED (R3.6), not
	// removed.
	var rollbackPublished publishedUndo
	defer func() {
		if result == nil || result.Error == nil || rollbackPublished == nil {
			return
		}
		rollbackPublished(result.Error)
	}()

	// gates accumulates every outcome this run produces, and it is declared here
	// rather than where the first one is assigned so that the record written
	// beside the staged tree (R10.4, below) sees the WHOLE list however this
	// function returns — including the failing exits, whose record is the one that
	// stops the next run promoting a rejected bump.
	var gates []validate.GateResult

	// R10: what an earlier run already proved about this exact bump.
	//
	// Taken BEFORE anything is staged, for the reason staging itself makes
	// unavoidable: validate.Stage replaces the retained tree (R3.7), so a question
	// asked after it is a question about a tree this run just rebuilt.
	var inputs stagedInputs
	if a.stagingRoot != "" {
		captured, err := a.stagedInputsFor(pkg, currentVersion, update)
		if err != nil {
			return a.failApply(pkg, result, err)
		}
		inputs = captured

		// R10.3: one of the two answers is recorded on every staged apply, and
		// the default is the honest one — the gates below run in this run unless
		// the retained tree takes their place.
		result.ValidationSource = ValidationSourceThisRun

		reuse := a.reusableStagedTree(pkg, newVersion, inputs, depth.Depth)
		if reuse.root != "" {
			// Only when a tree was actually there. R10.3's "which of the two
			// happened" is already on the result and in the summary line; what
			// this adds is the WHY, and "there was no retained tree" explains
			// nothing an operator did not know from the absence of one.
			result.DepthReason = appendDepthReason(result.DepthReason, reuse.reason)
		}
		if reuse.err != nil {
			// The retained tree matched this bump exactly and its distfile moved
			// underneath it. Reported against the staged proof, because that is
			// what the decision was taken on — nothing was validated here.
			result.ValidationSource = ValidationSourceStaged
			result.StagedPath = reuse.root
			return a.failApply(pkg, result, reuse.err)
		}
		if reuse.promote {
			// R10.1: the hours were already spent. Nothing between here and the
			// published write runs a gate, which is the entire economic argument
			// — an operator who pays for `--check --llm` and then pays again for
			// `--apply` stops running the check first.
			result.ValidationSource = ValidationSourceStaged
			result.StagedPath = reuse.root
			result.DepthReached = reuse.reached

			promoted, err := a.promote(reuse.cand, pkg, newVersion)
			if err != nil {
				return a.failApply(pkg, result, err)
			}
			rollbackPublished = promoted

			result.Success = true
			// R3.6's other direction, exactly as on the validating path: the
			// retained tree is a failure's evidence, and there is no failure here.
			result.StagedPath = ""
			a.completeApply(pkg, newVersion, result)
			return result, nil
		}
	}

	// Materialise the candidate where the gates will read it: in a staged tree
	// outside the overlay when a staging root was configured, in the published
	// overlay otherwise.
	// Only the pre-staging branch arms the rollback, and it arms it with the one
	// path it just wrote. The staged branch leaves it nil, because after it there
	// is still nothing in the published overlay to take back.
	var (
		cand    candidatePaths
		prepErr error
	)
	if a.stagingRoot == "" {
		cand, rollbackPublished, prepErr = a.prepareInOverlay(pkg, currentVersion, newVersion, update)
	} else {
		cand, prepErr = a.prepareInStagingTree(pkg, currentVersion, newVersion, update, result)
	}
	if prepErr != nil {
		return a.failApply(pkg, result, prepErr)
	}

	// R10.4: a record of what the gates said, beside the tree they said it about,
	// written however this apply ends.
	//
	// A defer rather than a call at each exit, and that is not brevity: there are
	// six ways out from here down, and the ONE that must never be forgotten is the
	// failing one — an unrecorded failed tree is promoted by the next run's R10.1
	// on a match alone. The closure reads `gates` and `depth` at return time, so it
	// records the final list and the depth a reviewer's escalation may have raised.
	if stagedRoot := result.StagedPath; stagedRoot != "" {
		defer func() { a.recordStagedProof(stagedRoot, pkg, newVersion, inputs, gates, depth.Depth) }()
	}

	// Run manifest command. When a fixer is wired, a failure here triggers a
	// single agentic repair-and-retry before the apply is declared failed; the
	// outcome (including whether a fix was applied) is recorded on result. On the
	// staged path this is `pkgdev manifest` inside the staged tree against a
	// private distdir, so no directory the host shares changes while it runs.
	a.reporter.TaskStage(pkg, "manifest")
	if err := a.runManifestWithFix(cand, pkg, newVersion, result); err != nil {
		return a.failApply(pkg, result, fmt.Errorf("%w: %v", ErrManifestFailed, err))
	}

	// The static gates — the Meson option gate and the advisory QA scan (story
	// 031's gate, reused verbatim). They read files that already exist, so they
	// cost no build and can sit ahead of StatusValidated below. That placement is
	// the whole point of the slot: a gate added ABOVE that line instead of below
	// it would silently undo sub-task 4.1's move, and the state's meaning —
	// "passed the static gates" — would quietly go back to "the manifest ran".
	gates = a.runStaticGates(cand, pkg, newVersion)

	// Update status to validated.
	//
	// MOVED (S033-D13, protocol §5.7): this used to be written the moment the
	// manifest step returned. It now sits after the static gates, and the state's
	// MEANING NARROWS with the move — it says "passed the static gates", not "ready
	// to publish" and, on the staged path, no longer "the ebuild is in the
	// overlay". A bump can sit at `validated` having failed a later gate and never
	// been promoted, and the column `--list` renders has to be read that way.
	if err := a.pending.SetStatus(pkg, StatusValidated, ""); err != nil {
		result.Error = fmt.Errorf("failed to update status: %w", err)
		return result, result.Error
	}

	// R7: the optional bump reviewer, after the static gates and before anything
	// is built — it reads a diff and may only ask for MORE gates, never fewer.
	// A run with no reviewer wired passes the policy depth straight through.
	depth = a.reviewBump(cand, pkg, currentVersion, newVersion, depth, &gates)
	result.DepthRequested = depth.Depth.String()
	result.DepthReason = depth.Reason

	// The build gates, at the depth selected above (R3, R5, R6). They are the
	// generalisation of the compile gate below, so the two never both run: with
	// --compile the shipped gate keeps its prompt, its privilege and its repair
	// path, and running the depth gates beside it would build the same tree twice.
	if !compile {
		a.reporter.TaskStage(pkg, "build gates")
		buildGates, buildErr := a.runBuildGates(cand, pkg, newVersion, depth.Depth, result)
		gates = append(gates, buildGates...)
		if buildErr != nil {
			a.recordDepthReached(result, gates, depth.Depth)
			return a.failApply(pkg, result, buildErr)
		}
	}

	// Run compile test if requested. It runs against cand's repository, which on
	// the staged path is the staged tree: a gate that built out of the published
	// overlay would be reading a candidate that is not there yet.
	if compile {
		a.reporter.TaskStage(pkg, "compile")
		logPath, err := a.runCompile(cand, pkg, newVersion, result)
		if err != nil {
			result.LogPath = logPath
			a.recordDepthReached(result, gates, depth.Depth)
			return a.failApply(pkg, result, err)
		}
		gates = append(gates, a.compileGateResult(cand, pkg, newVersion, result)...)
	}

	// R3.12 in one line: the outcome states its own reach, and says why it stops
	// where it does, whether or not this apply is about to succeed.
	a.recordDepthReached(result, gates, depth.Depth)

	// R3.13: a host that asked for proof does not get a publish built on skips.
	// It is deliberately NOT folded into PromotionDecision: that function's rule
	// is R3.3's ("PASS or SKIPPED promotes"), and this is the operator subtracting
	// from it, which is a different authority and belongs where it can be seen.
	if err := a.refuseUnproved(gates, pkg, newVersion, depth.Depth); err != nil {
		return a.failApply(pkg, result, err)
	}

	// R3.3: the candidate may be published only once every gate up to the selected
	// depth has reported PASS or SKIPPED. The rule lives in one pure function so it
	// is asserted directly rather than only through a real promotion, and so that a
	// gate added above cannot reach the overlay without passing through it.
	//
	// The staging error is nil by construction: a tree that could not be prepared
	// already withdrew the bump in prepareInStagingTree (R3.10), so a promotion
	// decision is only ever reached WHERE a staged tree exists — which is R3.3's
	// second half.
	//
	// The refusal is enriched with the failing gates' own error findings before it
	// leaves here (refusalWithFindings): PromotionDecision names the gate, and an
	// apply's only channel to the operator is this one error — "the options gate
	// reported FAILED" without the option it found would send them off to diff two
	// tarballs by hand, which is the work this story replaces.
	if ok, reason := validate.PromotionDecision(gates, nil); !ok {
		return a.failApply(pkg, result, refusalWithFindings(reason, gates))
	}

	// The published overlay's first and only write of this apply (R3.4). On the
	// pre-staging path there is nothing to promote: copyEbuild already put the
	// candidate there and `pkgdev manifest` already regenerated the Manifest in
	// place.
	if cand.staged {
		promoted, err := a.promote(cand, pkg, newVersion)
		if err != nil {
			return a.failApply(pkg, result, err)
		}
		// Armed only now: from this point a failure DOES have something published
		// to take back, and promotion's own rollback is what knows the difference
		// between the ebuild (remove it) and the Manifest (restore it).
		rollbackPublished = promoted
	}

	result.Success = true

	// R3.6, the other direction: the retained tree is a FAILURE's evidence, so the
	// path is dropped the moment there is no failure to explain. Cleared here and
	// not earlier because this line is where success is finally decided — every way
	// of not being promoted has already returned through failApply, carrying the
	// path with it — and not later because nothing below can turn this apply back
	// into a failure: the pending delete, the registry pin and the --clean sweep all
	// report their misses as warnings and deliberately leave Success true.
	//
	// The tree itself is left on disk. Removing it would be a filesystem operation
	// whose failure this path has no honest way to report, and it would buy nothing:
	// the next attempt at this same package and version restages over it (R3.7), so
	// what is left is one directory per version, not a growing pile per run.
	result.StagedPath = ""

	a.completeApply(pkg, newVersion, result)
	return result, nil
}

// completeApply is the bookkeeping every promoted bump gets once the published
// overlay holds it: the pending entry is dropped, the registry pin is written and
// `--clean` sweeps.
//
// It is a function rather than the tail of Apply because story 033 gave promotion
// TWO routes to this point — the gates ran here, or a retained tree that had
// already been proved was promoted as it stood (R10.1) — and a bump that reached
// the overlay by the second route needs exactly the same three steps. Two copies
// would diverge in the direction that hurts: a promotion with no pin leaves
// `--clean` aiming at the only ebuild present.
//
// NOTHING HERE MAY FAIL THE APPLY. Every miss is a warning on the result with
// Success left true and Error left nil — setting Error would fire the deferred
// rollback and delete the ebuild this apply just published (S021-UB5).
func (a *Applier) completeApply(pkg, newVersion string, result *ApplyResult) {
	// S002-R3.1: remove the now-applied package from pending.json so `--list` no
	// longer surfaces it. S002-R3.4: a Delete failure is a bookkeeping miss, not
	// an apply failure — log a Warn (via the package warnLogf sink so tests
	// can capture it) but keep result.Success == true and result.Error == nil
	// so the deferred orphan-rollback (keyed on result.Error == nil) does not
	// undo the successful apply.
	if err := a.pendingDeleteFn(pkg); err != nil {
		warnLogf("pending: failed to remove %s after successful apply: %v "+
			"(apply itself succeeded; entry can be cleared manually)", pkg, err)
	}

	// S021-R2.1: record the version that just landed on disk as the one this
	// registry entry keeps. Reached only here, past the manifest step, past the
	// compile test and — since story 033 — past a COMPLETED promotion, because the
	// registry must never claim a file that is not there: `--clean` removes every
	// ebuild no entry claims, so a pin written ahead of the file would aim that
	// rule at the only ebuild present and a failed update would become a deleted
	// package (S021-R2.2). "Validated but not promoted" is the case story 033 adds
	// to that list, and it reaches this line by exactly one route — it does not:
	// every way of not being promoted returns above through failApply.
	// The value written is newVersion — what was applied — never the pending
	// entry's upstream target, which stays pending.json's business alone
	// (S021-UB4). An overlay with no packages.toml gets a warning here and no
	// pin, which is the honest report: nothing recorded the version.
	//
	// S021-R2.4: a failed write is a bookkeeping miss, exactly like the pending
	// delete above — warn through the package warnLogf sink (so tests can capture
	// it), surface it on the result, and leave result.Success true with
	// result.Error nil. Setting result.Error here would fire the deferred
	// orphan-rollback and delete the ebuild this apply just created (S021-UB5).
	if err := a.setVersionsFn(a.overlayPath, map[string]string{pkg: newVersion}); err != nil {
		warnLogf("registry: failed to record version = %q for %s in packages.toml: %v "+
			"(the update itself succeeded; the next check's reconciliation can write the pin)",
			newVersion, pkg, err)
		result.RegistryWarning = fmt.Sprintf("could not record version = %q for %s: %v", newVersion, pkg, err)
	}

	// --clean (R4.1): sweep the package directory against the registry's pins so
	// only the ebuilds an entry claims are left. This runs only on the full
	// success path and is best-effort — a blocked plan, a failed removal or a
	// failed Manifest regeneration is surfaced as a warning on the result and
	// never flips Success, because the update itself is done (R4.4).
	if a.clean {
		plan, err := a.cleanPackageDir(pkg, newVersion)
		result.CleanKept = plan.Keep
		result.CleanRemoved = plan.Remove
		if n := len(plan.Remove); n > 0 {
			// The legacy single-version view: Remove is ascending, so its last
			// entry is the highest version actually removed. Set even when the
			// sweep then failed — those files really are gone.
			result.CleanedOldVersion = plan.Remove[n-1]
		}
		if err != nil {
			warnLogf("clean: %v", err)
			result.CleanWarning = err.Error()
		}
	}
}

// appendDepthReason adds one more sentence to the reason a result carries,
// keeping the ones already there.
//
// The depth reason is the only free-text field an operator reads to understand
// why a bump was treated the way it was, and R10.3's answer ("the gates ran here"
// or "an earlier run had already proved this") has to sit BESIDE the depth
// decision rather than replace it — the two answer different questions and both
// are needed to make sense of a four-second apply.
func appendDepthReason(existing, added string) string {
	switch {
	case strings.TrimSpace(added) == "":
		return existing
	case strings.TrimSpace(existing) == "":
		return added
	}
	return existing + "; " + added
}

// failApply records err as this apply's outcome and mirrors it into
// pending.json, returning the pair every failing path of Apply returns.
//
// It exists because that three-line dance was repeated at seven exits and the
// repetition was load-bearing: result.Error must be set BEFORE the function
// returns, because the deferred rollback keys on it, and a SetStatus that itself
// fails must be appended to the original error rather than replacing it. One
// helper is one place for both rules.
func (a *Applier) failApply(pkg string, result *ApplyResult, err error) (*ApplyResult, error) {
	result.Error = err
	if serr := a.pending.SetStatus(pkg, StatusFailed, result.Error.Error()); serr != nil {
		// Keep the original error; just say that the status could not be recorded.
		result.Error = fmt.Errorf("%w (also failed to update status: %v)", result.Error, serr)
	}
	return result, result.Error
}

// prepareInOverlay materialises the candidate the way every release before story
// 033 did: the new version's ebuild is copied into the PUBLISHED package directory
// and the per-package substitutions are applied to it there.
//
// It is kept for exactly one reason — a caller that supplied no staging root gets
// the behaviour it has always had, byte for byte, until sub-task 12.1 wires the
// staging root into the three construction sites in cmd/.
//
// The rollback is RETURNED rather than registered, so Apply arms it only once this
// function has succeeded. That leaves a window this function must close itself: a
// substitution failure returns an error and NO rollback, so there is nothing for
// Apply to arm and the ebuild copied one line earlier survives in the published
// tree. That is how sys-apps/asus-ec-sensors-0_p20260809 was left behind — carrying
// the previous version's COMMIT=, with no Manifest entry and no md5-cache — and,
// because this overlay auto-commits and pushes, published in that state.
func (a *Applier) prepareInOverlay(pkg, currentVersion, newVersion string, update *PendingUpdate) (candidatePaths, publishedUndo, error) {
	if err := a.copyEbuild(pkg, currentVersion, newVersion); err != nil {
		return candidatePaths{}, nil, fmt.Errorf("failed to copy ebuild: %w", err)
	}

	cand, err := publishedCandidate(a.overlayPath, pkg, newVersion)
	if err != nil {
		return candidatePaths{}, nil, err
	}

	// copyEbuild succeeded: a fresh .ebuild now exists in the overlay. If any later
	// step (manifest, status update, compile) fails, that file is an orphan and
	// must be removed so the overlay is not left half-applied.
	undo := orphanEbuildUndo(cand.ebuildPath, pkg, newVersion)

	// Run it HERE rather than handing it back, because a failing substitution is
	// the one caller that never gets to. Handing back both an error and a rollback
	// would only move the same trap to Apply, which arms nothing on an error path.
	if err := a.applySubstitutions(cand.ebuildPath, pkg, update); err != nil {
		undo(err)
		return candidatePaths{}, nil, err
	}

	return cand, undo, nil
}

// prepareInStagingTree materialises the candidate in a tree of its own, outside
// the published overlay, and returns where the gates will find it (R3, R3.1).
//
// Nothing here writes into the overlay: the source ebuild is READ out of it and
// the candidate is written into the staged tree. That is the whole difference from
// prepareInOverlay, and it is what lets R3.2 be stated as "the overlay is
// byte-identical while any gate runs" rather than as "it is put back afterwards".
//
// A staged tree that could not be built WITHDRAWS the bump (R3.10) instead of
// letting it through as "no gate failed": every build gate would report SKIPPED,
// nothing would have FAILED, and a candidate no gate ever read would be published.
// validate.PromotionDecision documents that vacuity at length; this function is
// where it is denied, by failing before any gate is consulted.
func (a *Applier) prepareInStagingTree(pkg, currentVersion, newVersion string, update *PendingUpdate, result *ApplyResult) (candidatePaths, error) {
	srcPath := a.EbuildPath(pkg, currentVersion)
	if srcPath == "" {
		return candidatePaths{}, fmt.Errorf("invalid package name format: %s", pkg)
	}
	body, err := os.ReadFile(srcPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Same sentinel copyEbuild reports, so a caller that recognises a missing
		// source ebuild keeps recognising it on either path.
		return candidatePaths{}, fmt.Errorf("%w: %s", ErrEbuildNotFound, srcPath)
	case err != nil:
		return candidatePaths{}, fmt.Errorf("failed to read source ebuild %s: %w", srcPath, err)
	}

	// The refusal copyEbuild makes, taken here so that a bump which can never be
	// published does not first spend a manifest run and a build proving itself. It
	// is deliberately not the only place it is taken: promotion re-checks, because
	// the gates in between take minutes and a check that old describes a package
	// directory that may have moved on.
	if err := refuseExistingEbuild(a.EbuildPath(pkg, newVersion), pkg, newVersion); err != nil {
		return candidatePaths{}, err
	}

	stagedRoot, err := validate.Stage(validate.StageRequest{
		Overlay:     a.overlayPath,
		StagingRoot: a.stagingRoot,
		Atom:        pkg,
		Version:     newVersion,
		EbuildBytes: body,
	})
	if err != nil {
		// The ErrStageUnpreparable sentinel survives the wrap, which is the point
		// of it: the caller reacts to staging having failed without enumerating the
		// ways it can.
		return candidatePaths{}, fmt.Errorf("staging %s-%s for validation: %w", pkg, newVersion, err)
	}
	// R3.6: named on the result the moment it exists, so it is named even when
	// everything after this fails. A retained tree nobody can find is not an
	// inspectable failure.
	result.StagedPath = stagedRoot

	cand, err := stagedCandidate(stagedRoot, pkg, newVersion)
	if err != nil {
		return candidatePaths{}, err
	}
	if err := a.applySubstitutions(cand.ebuildPath, pkg, update); err != nil {
		return candidatePaths{}, err
	}
	return cand, nil
}

// applySubstitutions rewrites the per-package variables the checker captured into
// the candidate ebuild, wherever that candidate currently lives.
//
// Both substitutions must precede the manifest step, because the variable they
// write typically feeds SRC_URI and the manifest step fetches the URL built from
// it. Taking the path as an argument rather than recomputing it from a.overlayPath
// is what lets the staged candidate be the one edited: an edit applied to the
// published tree here would be an unvalidated write into the overlay, and a staged
// tree validated without the substitution would prove the wrong file.
func (a *Applier) applySubstitutions(ebuildPath, pkg string, update *PendingUpdate) error {
	// Snapshot packages tracked by commit (track="commit"): point SRC_URI's
	// commit-hash variable at the correct tarball.
	if update.CommitHash != "" {
		if err := substituteCommitHash(ebuildPath, update.CommitHash); err != nil {
			return fmt.Errorf("failed to substitute commit hash: %w", err)
		}
	}
	// Packages declaring aux_var/aux_pattern: the free-text auxiliary variable
	// (e.g. MY_BUILD="esr-bbNN"). The aux_var NAME comes from config; the value
	// travels in the pending update.
	if update.AuxValue != "" {
		if err := substituteAuxVar(ebuildPath, a.configs[pkg].AuxVar, update.AuxValue); err != nil {
			return fmt.Errorf("failed to substitute aux var: %w", err)
		}
	}
	return nil
}

// applySummary derives the short, one-line summary handed to the reporter's
// TaskDone for an apply. It is purely cosmetic (the reporter only renders it):
// on success the new version (noting an LLM fix when one happened), on an
// obsolete prune the reason, and otherwise the failure's error text followed by
// the staged tree that failure left behind. A nil result yields the empty string.
//
// Naming the tree here is the second half of R3.6, and it is the half that makes
// the first half worth having: a tree retained on disk that no report points at is
// not an inspectable failure, it is a directory the operator will only find by
// going looking for it — which is exactly the "re-run the bump from scratch to see
// what happened" cost retention exists to remove. A success names nothing, because
// a promoted bump clears StagedPath.
func applySummary(result *ApplyResult) string {
	if result == nil {
		return ""
	}

	switch {
	case result.Success:
		summary := result.NewVersion
		if result.Fixed {
			summary += " (fixed)"
		}
		// R10.3 at the surface the operator actually reads. Without it a fast
		// green and a proved green are the same line: an apply that took four
		// seconds because an earlier run paid for the gates looks exactly like one
		// that took four seconds because nothing was checked.
		//
		// Empty on the pre-staging path, where no gate runs and neither answer
		// would be true — which also keeps that path's summary byte-identical to
		// every release before this one.
		switch result.ValidationSource {
		case ValidationSourceStaged:
			summary += " (promoted from the tree an earlier run staged and validated)"
		case ValidationSourceThisRun:
			summary += " (validated in this run)"
		}
		return summary
	case result.Obsolete:
		return result.ObsoleteReason
	case result.Held:
		return "held (hold = true)"
	}

	// What is left is a failure. Its error text is the summary — except that
	// failApply is the only route here and it always records one, so the empty
	// fallback is a defence against a future exit that forgets, not a live case.
	summary := ""
	if result.Error != nil {
		summary = result.Error.Error()
	}
	if result.StagedPath == "" {
		return summary
	}
	note := "staged tree kept at " + result.StagedPath
	if summary == "" {
		return note
	}
	return summary + " (" + note + ")"
}

// resolveCurrentVersion returns the highest-version, non-live ebuild version
// actually present in the overlay for pkg — restricted to pkg's slot when its
// key carries one. It shares the checker's selection (getCurrentVersion) so
// Apply works off the live overlay state instead of the pending entry's
// possibly-stale current_version. Returns ErrNoEbuildFound when the package
// directory is absent or holds no parsable, non-live ebuild in the slot.
func (a *Applier) resolveCurrentVersion(pkg string) (string, error) {
	best, err := selectCurrentEbuild(a.overlayPath, pkg, a.configs[pkg].Series)
	if err != nil {
		return "", err
	}
	return best.Version, nil
}

// pruneObsolete marks result as an obsolete pending entry, removes it from the
// pending list (best-effort), and returns it with a nil error so callers do not
// count it as a failure. reason is surfaced to the user verbatim via
// ObsoleteReason.
func (a *Applier) pruneObsolete(pkg string, result *ApplyResult, reason error) (*ApplyResult, error) {
	result.Obsolete = true
	result.ObsoleteReason = reason.Error()
	if err := a.pendingDeleteFn(pkg); err != nil {
		warnLogf("pending: failed to prune obsolete entry %s: %v "+
			"(entry can be cleared manually)", pkg, err)
	}
	return result, nil
}

// cleanPackageDir sweeps pkg's package directory against the registry's pins:
// it deletes every non-live ebuild no entry claims, regenerates the Manifest
// once, and returns the plan it actually executed so the caller can report it.
//
// The predecessor removed a single file by name. That was safe only because it
// never looked at the rest of the directory — and equally blind to everything
// the bump left behind (an older release still on disk, a version bumped and
// superseded between two cleans). Deciding by claim instead of by name is what
// makes removing more than one file survivable.
//
// The plan is computed against a COPY of a.configs in which pkg's entry is
// pinned to newVersion. The pin on disk is written by a separate step of the
// same run, so planning against a.configs verbatim would use the PRE-apply
// registry — where every record is pinless today, meaning every clean would
// block and the feature would ship dead. The overlaid pin is not a prediction:
// the version just applied IS the one this entry now keeps, and writing it to
// packages.toml records that same fact in the other file. The overlay covers
// exactly one entry, so a pinless SIBLING — the other release line of a
// two-entry directory — still has no pin here and still blocks the whole
// directory (UB3).
//
// Nothing is removed at all in three cases. Each is returned as an error, which
// the caller surfaces as a warning without failing the apply (R4.4):
//
//   - a claiming entry declares no pin (R5.1); the error names it (R6.2);
//   - pkg has no entry in the registry at all, so nothing here authorises a
//     deletion. This is reachable in production — loadPackagesConfigForApply
//     returns nil when packages.toml cannot be read — and refusing to sweep is
//     exactly right then. The guard is explicit rather than left to planSweep's
//     no-claims block because a SIBLING entry can still claim the atom WITH a
//     pin, and that plan would cheerfully delete the ebuild this very apply
//     just created;
//   - the package directory cannot be read, which planSweep reports as an
//     error rather than as an empty plan.
func (a *Applier) cleanPackageDir(pkg, newVersion string) (sweepPlan, error) {
	cfgs, claimed := a.sweepConfigs(pkg, newVersion)

	plan, err := planSweep(a.overlayPath, cfgs, pkg)
	if err != nil {
		return sweepPlan{}, fmt.Errorf("cannot plan the sweep of %s: %w", pkg, err)
	}

	// R5.1/R6.2 first: this is the blocked case that HAS an entry to name, and
	// naming it is what lets a maintainer unblock the directory.
	if plan.Blocked != "" {
		return plan, fmt.Errorf("nothing removed from %s: registry entry %q has no version pin, "+
			"so the sweep cannot tell which ebuilds that entry keeps%s",
			pkg, plan.Blocked, wouldRemoveSuffix(plan.WouldRemove))
	}
	if !claimed {
		// Report what an authorised sweep would have done, delete nothing.
		if len(plan.Remove) > 0 {
			plan.WouldRemove, plan.Remove = plan.Remove, nil
		}
		return plan, fmt.Errorf("nothing removed from %s: no packages.toml entry claims it, "+
			"so nothing here says which ebuilds are kept%s", pkg, wouldRemoveSuffix(plan.WouldRemove))
	}

	// Execute the plan. The removal loop and the Manifest regeneration live on
	// sweeper (S027-D3) so the standalone overlay sweep runs the same code rather
	// than a second implementation of "delete these ebuilds". newVersion is the
	// version this apply just created, and it is the one that remains in the
	// directory — which is what the Manifest step needs (S027-G2).
	return a.sweeper().execute(pkg, plan, newVersion)
}

// sweeper builds the executor for this applier's overlay, carrying the fields
// the removal loop and the Manifest step need. Deliberately no fixer: the LLM
// manifest repair stays behind runManifestWithFix on Applier, so no sweep can
// reach it (S027-R4.5).
func (a *Applier) sweeper() *sweeper {
	return newSweeper(a.overlayPath,
		withSweeperContext(a.ctx),
		withSweeperExec(a.execCommand),
		withSweeperReporter(a.reporter),
		withSweeperConfigs(a.configs),
		// Both distfile directories reach the Manifest step through here, which
		// is the only path Applier has to it: runManifest delegates to this
		// sweeper (S030-R1.3).
		withSweeperDistdir(a.distdir, a.configuredDistdir),
		withSweeperDistfilesCache(a.distfilesCache),
	)
}

// sweepConfigs returns the registry the sweep plans against — a copy of
// a.configs with pkg's entry pinned to newVersion — and whether pkg has an entry
// at all.
//
// a.configs is never mutated: it is shared state the rest of Apply reads for the
// hold flag, the slot's revision, the series filter and the [meta] block, and a
// version written into it here would outlive this call. The copy is shallow —
// entries are copied by value and only Version is rewritten — so the maps and
// slices inside an entry are shared with a.configs and, like a.configs, only
// ever read.
func (a *Applier) sweepConfigs(pkg, newVersion string) (map[string]PackageConfig, bool) {
	entry, ok := a.configs[pkg] // nil-safe: a nil map yields the zero value and ok == false
	if !ok {
		// Nothing to overlay. a.configs is handed over unchanged (planSweep only
		// reads it) and the caller refuses to delete anything in this case.
		return a.configs, false
	}
	cfgs := make(map[string]PackageConfig, len(a.configs))
	for key, cfg := range a.configs {
		cfgs[key] = cfg
	}
	entry.Version = newVersion
	cfgs[pkg] = entry
	return cfgs, true
}

// wouldRemoveSuffix renders the candidates a blocked plan left alone, for the
// tail of its warning (R5.1's report). It is empty when there were none, so the
// message never trails a dangling "would have removed:".
func wouldRemoveSuffix(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	return fmt.Sprintf(" (would have removed: %s)", strings.Join(versions, ", "))
}

// copyEbuild copies the source ebuild to a new file with the updated version.
// Source: {category}/{package}/{package}-{oldVersion}.ebuild
// Destination: {category}/{package}/{package}-{newVersion}.ebuild
func (a *Applier) copyEbuild(pkg, oldVersion, newVersion string) error {
	// Parse package name
	category, pkgName, ok := splitPkgAtom(pkg)
	if !ok {
		return fmt.Errorf("invalid package name format: %s", pkg)
	}

	// Reject same-version copy: srcPath and dstPath would coincide, and
	// os.Create truncates the destination before io.Copy reads, silently
	// zeroing the source ebuild.
	if oldVersion == newVersion {
		return fmt.Errorf("source and destination versions are equal: %s", newVersion)
	}

	// Build paths
	pkgDir := filepath.Join(a.overlayPath, category, pkgName)
	srcPath := filepath.Join(pkgDir, fmt.Sprintf("%s-%s.ebuild", pkgName, oldVersion))
	dstPath := filepath.Join(pkgDir, fmt.Sprintf("%s-%s.ebuild", pkgName, newVersion))

	// Check source exists
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrEbuildNotFound, srcPath)
	}

	// Refuse to write over an ebuild that already exists. os.Create truncates,
	// so without this the copy silently destroys a file the applier never wrote
	// — and Apply's deferred orphan-rollback would then os.Remove it outright on
	// any later failure, turning truncation into deletion.
	//
	// The oldVersion == newVersion check above only covers the case where source
	// and destination are the same file. A distinct destination can still exist
	// whenever the package directory holds several ebuilds whose versions are not
	// totally ordered by the selection that produced oldVersion — most notably a
	// multi-slot package, where the slots share a PV series and the revision
	// suffix discriminates them (net-libs/webkit-gtk: -r410/-r411 = SLOT 4.1,
	// -r600/-r601 = SLOT 6). Bumping the 4.1 ebuild from 2.52.4-r411 towards
	// 2.52.5 targets webkit-gtk-2.52.5.ebuild — which is the SLOT 6 ebuild.
	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("%w: %s (refusing to overwrite; %s-%s would be written over it)",
			ErrEbuildExists, dstPath, pkgName, oldVersion)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to stat destination ebuild %s: %w", dstPath, err)
	}

	// Open source file
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source ebuild: %w", err)
	}
	defer src.Close() //nolint:errcheck

	// Create destination file
	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("failed to create destination ebuild: %w", err)
	}
	defer dst.Close() //nolint:errcheck

	// Copy content
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to copy ebuild content: %w", err)
	}

	// Sync to ensure data is written
	if err := dst.Sync(); err != nil {
		return fmt.Errorf("failed to sync destination ebuild: %w", err)
	}

	return nil
}

// substituteCommitHash replaces the commit-hash variable assignment in an
// ebuild with newHash. It handles the four variable names used in the overlay:
//
//	EGIT_COMMIT="<sha>"   (vulkan-*, spirv-*)
//	GIT_COMMIT="<sha>"    (glslang, modemmanager)
//	BUILD_ID="<sha>"      (cursor — version-tracked; SHA is part of the SRC_URI)
//	COMMIT="<sha>"        (asus-ec-sensors)
//	COMMIT=<sha>          (sqlitebrowser — no quotes)
//
// COMMIT is spelled BOTH ways on purpose. It used to appear only in the unquoted
// pattern, written from the one package that omits the quotes, and the quoted
// pattern listed the other three names — so COMMIT="<sha>" fell through the gap
// between them and was reported as "no commit hash variable found" against an
// ebuild whose COMMIT= line is right there. That failure lands AFTER the ebuild
// has been copied, which is how it left an orphan in the published overlay.
//
// The substitution is deliberately narrow (anchored to known variable names +
// 40-hex-char SHA) so it cannot accidentally corrupt other content. The
// alternation needs no left-hand boundary: the whole matched assignment is
// captured in group 1 and written back verbatim, so a match that starts at the
// COMMIT inside EGIT_COMMIT still reproduces the EGIT_ prefix untouched.
func substituteCommitHash(ebuildPath, newHash string) error {
	content, err := os.ReadFile(ebuildPath)
	if err != nil {
		return fmt.Errorf("failed to read ebuild for hash substitution: %w", err)
	}

	reQuoted := regexp.MustCompile(`((?:EGIT_COMMIT|GIT_COMMIT|BUILD_ID|COMMIT)=")[0-9a-f]{40}(")`)
	reBare := regexp.MustCompile(`(COMMIT=)[0-9a-f]{40}\b`)

	// "Nothing changed" has two very different causes, and conflating them
	// reports a missing variable that is sitting right there. Decide on presence
	// first: an ebuild that already pins the target hash is correct, not broken.
	//
	// This is reachable now that a base version has its own source: a bump can be
	// a pure base correction (mesa 26.2.0 → 26.3.0 while upstream's HEAD has not
	// moved), where the hash to write is the one already in the file.
	if !reQuoted.Match(content) && !reBare.Match(content) {
		return fmt.Errorf("no commit hash variable (EGIT_COMMIT/GIT_COMMIT/BUILD_ID/COMMIT) found in %s", ebuildPath)
	}

	updated := reQuoted.ReplaceAllString(string(content), "${1}"+newHash+"${2}")
	updated = reBare.ReplaceAllString(updated, "${1}"+newHash)

	if updated == string(content) {
		return nil
	}

	if err := os.WriteFile(ebuildPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("failed to write ebuild after hash substitution: %w", err)
	}

	return nil
}

// substituteAuxVar replaces the quoted assignment of a free-text auxiliary
// variable in an ebuild (e.g. MY_BUILD="esr-bb23" → MY_BUILD="esr-bb24"). It is
// the sibling of substituteCommitHash but without the 40-hex-SHA lock, so it can
// carry any value captured from a regex/html upstream page. The variable name is
// anchored exactly (QuoteMeta) and the value is bounded by the surrounding double
// quotes, so the substitution cannot bleed past the assignment.
func substituteAuxVar(ebuildPath, varName, newValue string) error {
	if varName == "" {
		return fmt.Errorf("empty aux_var name for %s", ebuildPath)
	}
	content, err := os.ReadFile(ebuildPath)
	if err != nil {
		return fmt.Errorf("failed to read ebuild for aux var substitution: %w", err)
	}

	re := regexp.MustCompile(`(` + regexp.QuoteMeta(varName) + `=")[^"]*(")`)
	updated := re.ReplaceAllString(string(content), "${1}"+newValue+"${2}")

	if updated == string(content) {
		return fmt.Errorf("aux var %q not found in %s", varName, ebuildPath)
	}

	if err := os.WriteFile(ebuildPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("failed to write ebuild after aux var substitution: %w", err)
	}

	return nil
}

// runManifestWithFix runs the manifest step and, when it fails and an LLM fixer is
// configured, performs a single agentic repair-and-retry:
//
//  1. Run `pkgdev manifest`; on success, return nil (no fix needed).
//  2. Classify the failure. When it belongs to the machine and not to the
//     ebuild, report that and return — no fixer is constructed or invoked
//     (S030-R3.3/R3.4; see refuseFixOnEnvironmentFailure).
//  3. If no fixer is wired, return the original error (legacy fail-fast).
//  4. Otherwise invoke the fixer to edit the ebuild in place, then re-run
//     `pkgdev manifest` ONCE. That second run — bentoo's own, not the agent's
//     self-report — is the authoritative success check:
//     - success  → record result.Fixed/FixSummary and return nil.
//     - failure  → return a combined error (original + post-fix) so the caller
//     marks the apply failed; the deferred orphan-rollback in Apply removes the
//     half-applied ebuild.
//
// Exactly one fix attempt is made per apply: the agent iterates internally (bounded
// by its --max-turns), so there is no external retry loop here.
//
// Every path here is scoped to cand, never to a.overlayPath: on the staged path the
// manifest runs in the staged tree and the fixer edits the staged ebuild, so a fix
// attempt is itself covered by R3.2 — an agent rewriting SRC_URI must not be doing
// it inside a repository that commits and pushes itself.
func (a *Applier) runManifestWithFix(cand candidatePaths, pkg, version string, result *ApplyResult) error {
	firstErr := a.runManifestFor(cand, pkg, version)
	if firstErr == nil {
		return nil
	}

	// The gate (S030-D6). It sits between the failed manifest and everything
	// that would set a fix attempt up, because the cheapest fixer invocation is
	// the one that never happens.
	if envErr := a.refuseFixOnEnvironmentFailure(pkg, version, firstErr); envErr != nil {
		return envErr
	}

	if a.fixer == nil {
		return firstErr
	}

	pkgDir := cand.pkgDir
	if pkgDir == "" {
		// Malformed name: nothing the fixer can scope to; surface the original error.
		return firstErr
	}

	// Writable distdir the agent can pass to `pkgdev manifest --distdir` while it
	// self-verifies, so its checks never touch the system DISTDIR. Private and
	// removed on return — that part is the point and does not change.
	//
	// The ROOT it is made under does. An empty root means os.TempDir(), which on
	// the host S030 was measured on is a 31 GB tmpfs, so the agent's own
	// verification downloads landed in RAM — the very defect R1.1 removes from
	// the manifest step, on a second path no task in 1-6 reached. fixSandboxRoot
	// asks the host for PORTAGE_TMPDIR and answers "" when it cannot, which is
	// what os.MkdirTemp already means by "use the default": a host without
	// portageq keeps exactly today's behaviour.
	distdir, err := os.MkdirTemp(fixSandboxRoot(), "bentoo-fix-distfiles-")
	if err != nil {
		// Can't give the agent a private distdir; don't attempt the fix.
		return fmt.Errorf("%v (manifest fix skipped: failed to create temp distdir: %w)", firstErr, err)
	}
	defer func() { _ = os.RemoveAll(distdir) }()

	logger.Info("manifest failed for %s-%s; invoking LLM fixer to repair the ebuild", pkg, version)
	a.reporter.TaskStage(pkg, "llm-fix")
	a.reporter.Log("info", fmt.Sprintf("manifest failed for %s-%s; invoking LLM fixer to repair the ebuild", pkg, version))

	fixRes, fixErr := a.fixer.FixManifest(a.ctx, ManifestFixRequest{
		Package:       pkg,
		Version:       version,
		PkgDir:        pkgDir,
		EbuildPath:    cand.ebuildPath,
		ManifestError: firstErr.Error(),
		DistDir:       distdir,
	})
	if fixErr != nil {
		return fmt.Errorf("%v (LLM fix attempt failed: %w)", firstErr, fixErr)
	}

	// Authoritative re-check: trust bentoo's own manifest run, not the agent's
	// self-report.
	a.reporter.TaskStage(pkg, "re-check")
	if secondErr := a.runManifestFor(cand, pkg, version); secondErr != nil {
		return fmt.Errorf("%v (LLM fix applied but manifest still failed: %v)", firstErr, secondErr)
	}

	result.Fixed = true
	result.FixSummary = fixRes.Summary
	// Name the model that made the edit, and SAY SO when it was an alias
	// (S030-R4.1/R4.2). FormatModelUsed renders "model alias \"opus\"" rather
	// than a bare "opus", because the same alias resolves to a different model
	// over time: an audit of a bad edit months from now must not read the bare
	// word as a pinned identity. One string feeds both sinks so the operator's
	// log and the TUI report can never drift apart.
	fixLine := fmt.Sprintf("LLM fixer repaired %s-%s using %s: %s",
		pkg, version, FormatModelUsed(fixRes.Model), fixRes.Summary)
	logger.Info("%s", fixLine)
	a.reporter.Log("info", fixLine)

	// Advisory QA gate: the manifest re-run proves the distfile fetches and
	// digests, but not that the agent's edit is QA-clean. Run pkgcheck (when
	// available) on the package and attach any findings for human review. This is
	// best-effort and never flips Success — a fixed-and-fetchable ebuild is still
	// applied; the QA notes just travel with the result.
	if qa := a.runQACheck(pkgDir, pkg); qa != "" {
		result.QASummary = qa
		warnLogf("qa: pkgcheck reported findings for %s-%s after the LLM fix:\n%s", pkg, version, qa)
	}
	return nil
}

// refuseFixOnEnvironmentFailure decides whether a failed manifest step is one
// the LLM fixer must never see, and it is the whole of S030-D6.
//
// It returns a non-nil error ONLY for an environment verdict: the apply fails
// with it, and the caller returns before anything constructs a fix attempt
// (S030-R3.3). The error names the package, states that the cause was the
// environment and NOT the ebuild, gives the reason the classifier observed, and
// carries what `pkgdev manifest` printed — everything R3.4 asks be reported. For
// every other verdict it returns nil, which means "carry on", and today's path
// runs unchanged down to the authoritative re-run of pkgdev that decides success.
//
// # Why refuse at all
//
// An LLM fixer handed a machine failure has exactly one repair available to it —
// rewrite SRC_URI — so it spends ten minutes and up to thirty turns concluding
// that a correct ebuild is wrong, and this repository commits and pushes on its
// own. The measured case is sharper still: both packages applied on a later run
// with nothing changed (S030-M3a). The fixer was billed against quota for a
// condition that had already stopped existing.
//
// The precedent is promptRegistryFixes, which already refuses to offer a repair
// unless the failure wraps ErrFetchFailed (cmd/bentoo/overlay_autoupdate_fixregistry.go:89).
// This is the same rule moved to the manifest path, keyed on a classification
// rather than on a single sentinel.
//
// # Why it runs before the `a.fixer == nil` early return
//
// The verdict is a fact about the failure, not about the configuration: an
// operator whose distdir filled up needs to read that it was the distdir whether
// or not an LLM happens to be wired, or the same failure gets a different
// diagnosis on two machines and the one without a fixer hand-edits an ebuild
// that was never broken. Nothing is spent to get it — the classifier only
// observes state that is already there — and on a healthy machine the verdict is
// "repairable" and the error is returned byte-for-byte as before.
//
// # When there is nothing to classify
//
// Some failures raised before pkgdev ran are genuinely unanswerable — an invalid
// package atom says nothing about any machine — and those still fall through.
// R3.5 makes an unanswerable check mean "repairable": a wrong classification
// must cost a wasted fixer invocation, never a lost repair.
//
// A distdir that could not be prepared is NOT one of them, and treating it as
// one was this gate's original defect (found by `stories validate 030`). Probe
// answers that question definitively, before pkgdev is ever spawned, and R3.1
// requires the answer to reach here. See environmentVerdict.
func (a *Applier) refuseFixOnEnvironmentFailure(pkg, version string, firstErr error) error {
	verdict := environmentVerdict(firstErr)
	if verdict == nil {
		return nil
	}

	// Reported here, not returned quietly: the apply's own error reaches the
	// summary at the end of a batch, while this line lands next to the package it
	// belongs to in a run that keeps going.
	envErr := fmt.Errorf("%s-%s: %w (the LLM fixer was not invoked: the only repair available to it is to rewrite the ebuild, and the ebuild is not what failed)",
		pkg, version, verdict)
	warnLogf("%s", envErr.Error())
	a.reporter.Log("warn", envErr.Error())
	return envErr
}

// environmentVerdict returns an ErrManifestEnvironment-wrapped verdict when the
// failure is the machine's rather than the ebuild's, and nil when the ebuild
// still might be at fault.
//
// There are two ways to reach a verdict, and they are not interchangeable.
//
// # Before pkgdev ran
//
// The manifest step refuses to start on a distdir it could not prepare
// (S030-R1.4) and on a distfile another writer still holds when the wait runs
// out (S030-R2.4). Both are answered, by observation, before the ebuild is read
// by anything — so neither is a statement about the ebuild, and R3.5's
// "uncertain means repairable" has nothing to say about them: nothing was
// uncertain.
//
// # What this does NOT cover, and why it is not fixed here
//
// Three more pre-pkgdev refusals are equally the machine's and still fall
// through to the fixer: a Quarantine that could not stat or rename
// (sweep.go, via internal/common/distfiles/quarantine.go), and the three
// non-timeout error returns in acquireLock — the lock file could not be
// created, taken, or inspected (internal/common/distfiles/lock.go). None of
// them carries a sentinel, so none of them is recognisable here.
//
// Adding a fourth errors.Is is deliberately NOT the fix. This gate was already
// keyed on one condition and missed the pre-flight; keying it on an enumeration
// means every future step that can refuse is a clause somebody must remember to
// add, and the rung that gets forgotten is the one that invokes an agent
// against a machine fault. The shape that closes all of them at once is to mark
// the PHASE rather than the cause — every refusal raised while preparing the
// shared directory is, by construction, not a verdict about an ebuild pkgdev
// never read. That is a change to this gate's contract and belongs in its own
// story, not bolted on here.
//
// Missing this was the original gap. The pre-flight failure carries no
// *manifestRunError — that type is built only where pkgdev itself fails — so a
// gate keyed on the type alone let every one of these through. On a host whose
// invoking user is outside group `portage` (S030-M2) that meant the whole batch
// went to the fixer, and the fixer's own sandbox distdir (see runManifestWithFix)
// is a writable temp dir, so the agent could not even reproduce the failure: its
// one remaining conclusion is that SRC_URI is wrong, on an ebuild that is not.
//
// The two sentinels are kept apart because they are different news for the
// operator, exactly as distfiles.ErrDistfileLocked's own documentation asks:
// a held distfile is transient and the next run gets it, while an unwritable
// distdir will still be unwritable next time. The decision about the fixer is
// the same for both — it is not invoked — and it is the wrapped message that
// differs.
//
// # After pkgdev ran
//
// The state pkgdev actually ran in, recovered rather than re-derived: by now the
// distdir precedence could answer differently, and classifying against a
// directory the failure never saw would be worse than not classifying.
func environmentVerdict(firstErr error) error {
	if errors.Is(firstErr, distfiles.ErrDistdirNotWritable) ||
		errors.Is(firstErr, distfiles.ErrDistfileLocked) {
		// Wrapped, not replaced: %w keeps both the sentinel above and everything
		// under it reachable, so the operator still reads which directory or
		// which distfile, and which process held it.
		return fmt.Errorf("%w: %w", ErrManifestEnvironment, firstErr)
	}

	var runErr *manifestRunError
	if !errors.As(firstErr, &runErr) {
		return nil
	}

	// firstErr, not runErr.Err: the classifier wraps what it is handed, so the
	// whole chain stays reachable to errors.Is/errors.As under the verdict. The
	// nil SpaceFunc is the production seam — it normalises to the real statfs
	// query inside ClassifyManifestFailure, it does NOT disable the check.
	verdict := ClassifyManifestFailure(runErr.Distdir, firstErr, runErr.Expected, nil)
	if !errors.Is(verdict, ErrManifestEnvironment) {
		return nil
	}
	return verdict
}

// runQACheck runs `pkgcheck scan` against pkg as an advisory, read-only QA pass
// after an LLM fix, returning the trimmed findings (empty when pkgcheck is absent,
// could not run, or reported nothing). It is deliberately non-fatal: pkgcheck
// exits non-zero whenever it finds issues, so the exit code is ignored.
//
// Only stdout is treated as findings: pkgcheck's reporter writes findings to
// stdout, while diagnostics and crashes (e.g. a GitAddon traceback when the
// overlay's git history confuses pkgcheck) go to stderr. Surfacing stderr as
// "findings" once dumped a full Python traceback onto the result, so stderr is
// captured separately and only logged at debug — a pkgcheck crash yields no QA
// noise. The scan is bounded by qaCheckTimeout.
func (a *Applier) runQACheck(pkgDir, pkg string) string {
	if _, err := lookPath("pkgcheck"); err != nil {
		logger.Debug("qa: pkgcheck not on PATH; skipping post-fix QA for %s", pkg)
		return ""
	}

	ctx, cancel := context.WithTimeout(a.ctx, qaCheckTimeout)
	defer cancel()

	// Scan the single package from its directory so pkgcheck resolves the overlay
	// repo from cwd. Scope to repo-level checks for the one package via its atom.
	cmd := a.execCommand(ctx, "pkgcheck", "scan", pkg)
	cmd.Dir = pkgDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run() // non-zero exit == findings; ignore the code, keep stdout.

	if diag := strings.TrimSpace(stderr.String()); diag != "" {
		logger.Debug("qa: pkgcheck stderr for %s (not surfaced): %s", pkg, diag)
	}
	return strings.TrimSpace(stdout.String())
}

// runManifest regenerates the Manifest file with pkgdev, delegating to the
// sweeper that owns the step (S027-D3). It stays on Applier because
// runManifestWithFix and the apply path both call it, and because keeping the
// name here left every existing caller and test untouched.
func (a *Applier) runManifest(pkg, version string) error {
	return a.sweeper().runManifest(pkg, version)
}

// runManifestFor regenerates the Manifest of whichever tree the candidate is in.
//
// The two are not the same call with a different directory: the staged one drops
// LockFetch, Quarantine and RecordFetchScope and runs against a private distdir,
// because those three defend a directory the whole machine shares and pointing
// them at a staged tree would have a validation run rearrange the host's DISTDIR.
// sweep_staged.go carries the full argument.
func (a *Applier) runManifestFor(cand candidatePaths, pkg, version string) error {
	if cand.staged {
		return a.sweeper().runStagedManifest(cand.pkgDir, pkg, version)
	}
	return a.runManifest(pkg, version)
}

// runCompile runs a compile test with elevated privileges.
// It prompts for user confirmation before executing.
// Returns the log path if compilation fails.
//
// # What story 031 added here, and what it deliberately did not
//
// It added a LABEL and a SKIP. Before running anything, the isolation probe
// measures whether this process can create a network namespace, and the answer
// is carried onto the result so a pass can state its own fidelity: a compile
// that succeeded without a namespace is still a pass, but it is no longer
// printed as the same pass as one that had it (R7.2, R7.3). With
// --require-isolation and no namespace available, the compile does not happen
// at all and the result says why (R7.4) — running it anyway would produce
// exactly the meaningless green the operator asked to avoid.
//
// Everything else is untouched (R7.5). The confirmation prompt, the sudo/doas
// requirement, the runAttached seam and both existing success and failure
// conditions are exactly as they were; the probe sits ahead of all of them and
// the only new exit is the skip.
//
// # What story 033 added, and what it deliberately did not
//
// The ebuild it builds and the repository it builds from are now TOLD to it
// (cand) instead of recomputed from a.overlayPath. On the staged path that is the
// staged tree, which is what keeps the compile gate inside S033-R3.2: a build
// reading the published overlay would be reading a candidate that has not been
// published yet, and running it there is precisely the "unvalidated ebuild sitting
// in a tree that auto-commits" this story removes.
//
// It also added the REPAIR path. A failure no longer ends the gate outright: when
// the failure is attributable to the ebuild and a build fixer is wired, an agent
// edits the staged ebuild and this same gate runs again, with the RE-RUN deciding
// (S033-R8.1, S033-R8.2). Everything up to the first failure is unchanged, and a
// run with no fixer wired still ends exactly where it used to.
func (a *Applier) runCompile(cand candidatePaths, pkg, version string, result *ApplyResult) (string, error) {
	// Measured before the prompt: asking the operator to confirm a compile that
	// --require-isolation will refuse to run would be a question with no
	// consequence.
	isolated, reason := a.isolationProbe()
	result.IsolationVerified = isolated
	result.IsolationReason = reason

	if !isolated && a.requireIsolation {
		return "", nil
	}

	// Prompt for confirmation
	prompt := fmt.Sprintf("Run compile test for %s-%s with elevated privileges?", pkg, version)
	if !a.confirmFunc(prompt) {
		return "", ErrUserDeclined
	}

	// Detect privilege escalation tool
	privTool, err := a.detectPrivilegeTool()
	if err != nil {
		return "", err
	}

	// The ebuild to build, and the repository to build it from. `ebuild` discovers
	// the repository from the path it is given and from its working directory, so
	// cmd.Dir is what decides which tree the build reads.
	if cand.ebuildPath == "" {
		return "", fmt.Errorf("invalid package name format: %s", pkg)
	}

	first := a.compileOnce(cand, pkg, version, privTool)
	if first.err == nil {
		return "", nil
	}

	return a.repairBuildAndRerun(cand, pkg, version, privTool, first, result)
}

// compileGatePhase is the `ebuild` phase the compile gate runs.
//
// It is spelled as validate.GateCompile rather than as a bare "compile" to state
// an invariant R8.2 rests on: the gate NAMED to the fixer and the phase the
// authoritative re-run actually runs are the same thing. A fixer told it must fix
// the configure gate, followed by a re-run of a shallower phase, would produce a
// green that proves nothing about the failure it claims to have repaired.
const compileGatePhase = validate.GateCompile

// buildAttempt is one invocation of the build child: what it printed, where its
// log was retained on failure, and how it failed. A nil err is a passing build.
type buildAttempt struct {
	// transcript is the child's captured output — the evidence the attribution
	// gate reasons from and the log the fixer is given.
	transcript string
	// logPath is the retained compile log, empty unless the attempt failed.
	logPath string
	// err is the failure, already wrapped in ErrCompileFailed, or nil.
	err error
}

// compileOnce spawns the build child EXACTLY ONCE and, on failure, retains its
// log.
//
// It is the single place this gate's argv is spelled, and that is what makes
// R8.2's "the SAME gate is re-run" a property of the code rather than a promise:
// the authoritative re-run after a repair calls this same function with the same
// candidate, so it cannot drift to a shallower phase where a successful prepare
// would clear a configure failure.
//
// The seam and the log are story 010's, unchanged: the privileged child needs the
// real TTY for the sudo/doas password prompt (S010-R4.1), which rules out
// capturing its streams through a StreamCapture pipe, and the default runAttached
// is exactly cmd.CombinedOutput so the retained log stays byte-identical to the
// pre-TUI behaviour (S010-R3.3/S010-R7.1).
func (a *Applier) compileOnce(cand candidatePaths, pkg, version, privTool string) buildAttempt {
	// sudo/doas ebuild <path> clean compile, bound to the applier's parent context
	// so a SIGINT or deadline kills the spawned process.
	cmd := a.execCommand(a.ctx, privTool, "ebuild", cand.ebuildPath, "clean", compileGatePhase)
	cmd.Dir = cand.repoRoot

	output, err := a.runAttached(cmd)
	attempt := buildAttempt{transcript: string(output)}
	if err != nil {
		attempt.logPath = a.saveCompileLog(pkg, version, output)
		attempt.err = fmt.Errorf("%w: %v", ErrCompileFailed, err)
	}
	return attempt
}

// repairBuildAndRerun is what happens after the build gate has failed once: the
// failure is attributed, and only if it is the EBUILD's does an agent get to see
// it — after which this gate runs again and that re-run is the verdict
// (S033-R8.1, S033-R8.2, S033-R8.5).
//
// # The order, and why the gate is asked twice
//
// The attribution gate runs on FREE evidence first — the transcript this run
// already holds — and its verdict is reported whether or not a fixer is wired.
// That is refuseFixOnEnvironmentFailure's argument (applier.go:1450) and its
// precondition in one: the verdict is a fact about the failure and not about the
// configuration, and it may be taken unconditionally exactly while nothing is
// spent to get it, or two machines would give the same failure two diagnoses.
//
// The two rungs that DO spend something — a pretend `emerge -p` resolve, and a
// probe write into PORTAGE_TMPDIR — are asked only once a fixer is about to be
// invoked, because their whole purpose is to be cheaper than the agent
// invocation they prevent. The rungs are always evaluated in design D6's order;
// only which of them have evidence to speak from changes.
//
// One consequence, stated so it is not mistaken for a bug: a free verdict
// PRE-EMPTS a paid one, because the paid evidence is never gathered — that is the
// saving. So a build that both died in unpack and lacked its dependencies is
// reported as the phase, not as the dependencies. Both are the machine's, the
// action is identical (no fixer), and only the sentence the operator reads
// differs.
//
// # Exactly one attempt, and no counter here
//
// The attempt bound is enforced inside FixBuild (R8.4) — a bound only the caller
// remembers is not a bound — and this gate makes one attempt per apply, the same
// shape runManifestWithFix has: the agent iterates internally under its own
// --max-turns, and every extra external attempt costs a FULL rebuild, which for a
// real package is measured in hours.
func (a *Applier) repairBuildAndRerun(cand candidatePaths, pkg, version, privTool string, first buildAttempt, result *ApplyResult) (string, error) {
	// The free rungs. Reported to every operator, LLM or not.
	if machineErr := a.refuseBuildFixOnMachineFault(pkg, version, first, buildFaultEvidence{transcript: first.transcript}); machineErr != nil {
		return first.logPath, machineErr
	}

	if a.buildFixer == nil {
		return first.logPath, first.err
	}

	// R8.3/D7: the agent is only ever pointed at a STAGED tree. On the pre-staging
	// path the candidate lives in the published overlay — the repository that
	// auto-commits and pushes — and handing an agent Edit access there is the exact
	// thing this story's staging boundary exists to prevent. FixBuild would refuse
	// it too (ErrBuildFixScope), but refusing before anything is constructed keeps
	// the boundary visible at the call site rather than only inside the callee.
	if !cand.staged {
		return first.logPath, first.err
	}

	// The paid rungs, now that the alternative is a full agent invocation.
	paid := buildFaultEvidence{
		transcript:  first.transcript,
		deps:        a.buildDependencyAnswer(cand, pkg, version),
		buildTmpdir: fixSandboxRoot(),
	}
	if machineErr := a.refuseBuildFixOnMachineFault(pkg, version, first, paid); machineErr != nil {
		return first.logPath, machineErr
	}

	fixLine := fmt.Sprintf("the %s gate failed for %s-%s; invoking the LLM build fixer to repair the staged ebuild", compileGatePhase, pkg, version)
	logger.Info("%s", fixLine)
	a.reporter.TaskStage(pkg, "llm-build-fix")
	a.reporter.Log("info", fixLine)

	fixRes, fixErr := a.buildFixer.FixBuild(a.ctx, BuildFixRequest{
		Package:    pkg,
		Version:    version,
		Gate:       compileGatePhase,
		StagedDir:  cand.repoRoot,
		EbuildPath: cand.ebuildPath,
		BuildLog:   first.transcript,
		// One attempt per apply; FixBuild owns the bound (R8.4) and this states
		// which try it is rather than counting tries here.
		Attempt: 1,
	})
	switch {
	case errors.Is(fixErr, ErrBuildFixAttemptsExhausted):
		// "We stopped on purpose" is different news from "the agent failed", and
		// the operator acts on it differently: nothing is wrong with the machine.
		return first.logPath, fmt.Errorf("%w (the build fixer stopped on purpose: %w)", first.err, fixErr)
	case fixErr != nil:
		return first.logPath, fmt.Errorf("%w (the LLM build fix attempt failed: %w)", first.err, fixErr)
	}

	// An empty summary is the agent reporting NO CHANGE (see BuildFixResult.Summary),
	// and a re-run of an untouched tree can only reproduce the failure it already
	// produced — at the price of a whole second build. So the original failure
	// stands, unedited. The bias is deliberate: this path can only ever refuse to
	// clear a failure, never clear one on an edit nobody confirmed.
	summary := strings.TrimSpace(fixRes.Summary)
	if summary == "" {
		return first.logPath, fmt.Errorf("%w (the build fixer reported no change, so the %s gate was not re-run)", first.err, compileGatePhase)
	}

	// R8.2. The authoritative re-run: bentoo's own build of the same phase, never
	// the agent's account of what it did.
	a.reporter.TaskStage(pkg, "re-check")
	second := a.compileOnce(cand, pkg, version, privTool)
	if second.err != nil {
		return second.logPath, fmt.Errorf("%w (the build fixer edited the staged ebuild and the %s gate still failed on the re-run: %v)",
			first.err, compileGatePhase, second.err)
	}

	result.Fixed = true
	result.FixSummary = summary
	// Name the model that made the edit, and SAY SO when it was an alias
	// (S030-R4.1/R4.2) — the same one string into both sinks that the manifest fix
	// path uses, so the operator's log and the TUI report cannot drift apart.
	repaired := fmt.Sprintf("LLM build fixer repaired %s-%s using %s: %s",
		pkg, version, FormatModelUsed(fixRes.Model), summary)
	logger.Info("%s", repaired)
	a.reporter.Log("info", repaired)
	return "", nil
}

// refuseBuildFixOnMachineFault decides whether a failed build is one the build
// fixer must never see, and reports it when it is (S033-R8.5).
//
// It returns a non-nil error ONLY for a machine verdict: the apply fails with it,
// and the caller returns before anything constructs a fix attempt. For every other
// verdict it returns nil, meaning "carry on" — down to the fixer and, past it, to
// the re-run that decides.
//
// It is refuseFixOnEnvironmentFailure's counterpart for the build gate, and it is
// deliberately a separate function rather than a second caller of it: that one
// classifies through environmentVerdict, which a build failure falls straight
// through (see build_failure.go for why that would silently reinstate story 030's
// defect on this path).
func (a *Applier) refuseBuildFixOnMachineFault(pkg, version string, first buildAttempt, ev buildFaultEvidence) error {
	verdict := buildFaultVerdict(ev)
	if verdict == nil {
		return nil
	}

	// Reported here, not returned quietly: the apply's own error reaches the
	// summary at the end of a batch, while this line lands next to the package it
	// belongs to in a run that keeps going.
	machineErr := fmt.Errorf("%s-%s: %w (%w; the build fixer was not invoked: the only repair available to it is to edit the ebuild, and the ebuild is not what failed)",
		pkg, version, first.err, verdict)
	warnLogf("%s", machineErr.Error())
	a.reporter.Log("warn", machineErr.Error())
	return machineErr
}

// buildDependencyAnswer asks Portage whether this host could build the staged
// candidate at all, flattened into the shape the attribution gate reads.
//
// An UNDETERMINED answer — no Portage, a resolve that failed, a staged tree that
// is not there — becomes the zero value, which the classifier reads as "this rung
// has nothing to say" and not as "a dependency is missing". That is R3.5's bargain
// applied here: a question this host could not answer must cost at most a wasted
// fixer invocation, never a repair that was available.
//
// Only the exec seam is injected. LookPath is deliberately left at the validate
// package's own default, so a host with no `emerge` reaches the undetermined
// branch through the real absence rather than through a substitute.
func (a *Applier) buildDependencyAnswer(cand candidatePaths, pkg, version string) buildDependencyAnswer {
	ok, missing, err := validate.DependenciesSatisfied(a.ctx, cand.repoRoot, pkg, version, validate.BuildDeps{
		ExecCommand: a.execCommand,
	})
	if err != nil {
		logger.Debug("build fix gate: could not determine whether %s-%s's build dependencies are satisfied: %v", pkg, version, err)
		return buildDependencyAnswer{}
	}
	return buildDependencyAnswer{determined: true, satisfied: ok, missing: missing}
}

// detectPrivilegeTool detects whether sudo or doas is available.
func (a *Applier) detectPrivilegeTool() (string, error) {
	// Check for doas first (more secure, preferred on some systems)
	if _, err := exec.LookPath("doas"); err == nil {
		return "doas", nil
	}

	// Check for sudo
	if _, err := exec.LookPath("sudo"); err == nil {
		return "sudo", nil
	}

	return "", ErrNoPrivilegeEscalation
}

// saveCompileLog saves the compile output to a log file.
// Returns the path to the log file.
func (a *Applier) saveCompileLog(pkg, version string, output []byte) string {
	// Create log filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	safePkg := strings.ReplaceAll(pkg, "/", "_")
	logName := fmt.Sprintf("%s-%s-%s.log", safePkg, version, timestamp)
	logPath := filepath.Join(a.logsDir, logName)

	// Write log file. Compile logs use 0600 (owner-only): they may contain
	// sensitive build details. os.WriteFile applies the mode on creation.
	if err := os.WriteFile(logPath, output, fileutil.CacheFileMode); err != nil {
		// If we can't write the log, return empty path
		return ""
	}

	return logPath
}

// defaultConfirmFunc is the default confirmation function that reads from stdin.
func defaultConfirmFunc(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// StagingRoot returns the directory this applier stages candidates under, empty
// when it was constructed without one (in which case no gate runs and the
// candidate is written straight into the published overlay).
//
// It is exported because the staging root is where the EVIDENCE lives: the
// retained tree of every bump that was not promoted (R3.6) and the record beside
// it (R10.4). A caller that has to tell an operator where to look — or that wants
// to prove that a promotion really came from a tree an earlier run left there —
// cannot do either from the option it passed in, because the applier is the thing
// that decides what to do with it.
func (a *Applier) StagingRoot() string {
	return a.stagingRoot
}

// Pending returns the pending list instance.
func (a *Applier) Pending() *PendingList {
	return a.pending
}

// OverlayPath returns the overlay path.
func (a *Applier) OverlayPath() string {
	return a.overlayPath
}

// LogsDir returns the logs directory path.
func (a *Applier) LogsDir() string {
	return a.logsDir
}

// EbuildPath returns the full path to an ebuild file in the PUBLISHED overlay,
// or "" when pkg is not a well-formed category/package. It delegates so that the
// filename an ebuild carries is built in exactly one place (candidateIn).
func (a *Applier) EbuildPath(pkg, version string) string {
	cand, err := publishedCandidate(a.overlayPath, pkg, version)
	if err != nil {
		return ""
	}
	return cand.ebuildPath
}

// SeedFromGentoo copies the current ::gentoo package directory (srcPkgDir) into
// the overlay so a previously-removed (orphaned) package has a base ebuild to
// revive from. The overlay package dir may not exist yet — it is created with
// os.MkdirAll. Only the parts needed to bootstrap a bump are copied:
//
//   - the single ebuild matching gentooVersion (required; an absent source
//     ebuild is a clear error),
//   - metadata.xml (optional; skipped silently when absent),
//   - the files/ subdirectory (optional; copied recursively, preserving the
//     relative layout).
//
// The Manifest is deliberately NOT copied: it is regenerated by the later bump's
// `pkgdev manifest` step against the overlay's own distfiles. Every failure is
// wrapped with %w.
func (a *Applier) SeedFromGentoo(pkg, srcPkgDir, gentooVersion string) error {
	if pkg == "" || srcPkgDir == "" || gentooVersion == "" {
		return fmt.Errorf("SeedFromGentoo: empty argument (pkg=%q, srcPkgDir=%q, gentooVersion=%q)", pkg, srcPkgDir, gentooVersion)
	}

	// Parse package name (same split+slot-stripping as the sibling helpers).
	category, pkgName, ok := splitPkgAtom(pkg)
	if !ok {
		return fmt.Errorf("invalid package name format: %s", pkg)
	}

	// The overlay package dir was likely pruned when the package was orphaned;
	// create the full <overlayPath>/<category>/<pkg>/ path before copying.
	dst := filepath.Join(a.overlayPath, category, pkgName)
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return fmt.Errorf("failed to create overlay package dir %s: %w", dst, err)
	}

	// Required: the ::gentoo ebuild for the requested version.
	ebuildName := fmt.Sprintf("%s-%s.ebuild", pkgName, gentooVersion)
	srcEbuild := filepath.Join(srcPkgDir, ebuildName)
	if _, err := os.Stat(srcEbuild); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrEbuildNotFound, srcEbuild)
		}
		return fmt.Errorf("failed to stat source ebuild %s: %w", srcEbuild, err)
	}
	if err := copyFileContents(srcEbuild, filepath.Join(dst, ebuildName)); err != nil {
		return fmt.Errorf("failed to seed ebuild from gentoo: %w", err)
	}

	// Optional: metadata.xml — skip silently when ::gentoo does not ship one.
	srcMeta := filepath.Join(srcPkgDir, "metadata.xml")
	if _, err := os.Stat(srcMeta); err == nil {
		if err := copyFileContents(srcMeta, filepath.Join(dst, "metadata.xml")); err != nil {
			return fmt.Errorf("failed to seed metadata.xml from gentoo: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat source metadata.xml %s: %w", srcMeta, err)
	}

	// Optional: files/ subdirectory (patches, init scripts, …) — copy the whole
	// tree, preserving relative paths, when present.
	srcFiles := filepath.Join(srcPkgDir, "files")
	if info, err := os.Stat(srcFiles); err == nil {
		if info.IsDir() {
			if err := copyTree(srcFiles, filepath.Join(dst, "files")); err != nil {
				return fmt.Errorf("failed to seed files/ from gentoo: %w", err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat source files dir %s: %w", srcFiles, err)
	}

	return nil
}

// copyFileContents copies a regular file from src to dst, mirroring copyEbuild's
// open/create/io.Copy/Sync idiom. The destination is truncated if it exists.
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer in.Close() //nolint:errcheck

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", dst, err)
	}
	defer out.Close() //nolint:errcheck

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("failed to copy %s -> %s: %w", src, dst, err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("failed to sync destination file %s: %w", dst, err)
	}
	return nil
}

// copyTree recursively copies the directory rooted at src into dst, preserving
// the relative path layout. Directories are created with 0o750 and regular files
// are copied via copyFileContents. It is used to seed a package's files/ dir.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("failed to walk %s: %w", path, err)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("failed to compute relative path for %s: %w", path, err)
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o750); err != nil {
				return fmt.Errorf("failed to create dir %s: %w", target, err)
			}
			return nil
		}
		if err := copyFileContents(path, target); err != nil {
			return fmt.Errorf("failed to copy tree entry %s: %w", path, err)
		}
		return nil
	})
}
