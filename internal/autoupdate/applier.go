package autoupdate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/autoupdate/parse"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/fileutil"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/procgroup"
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
	// ErrInvalidAuxValue is returned (wrapped) when a pending update's AuxValue
	// is outside auxValueRe. The value was scraped from an upstream page and is
	// written into the bash source of the new ebuild, so anything that could
	// close the quoted assignment or reach the shell is refused, not repaired.
	ErrInvalidAuxValue = errors.New("invalid aux value for ebuild")
	// ErrInvalidCommitHash is returned (wrapped) when a pending update's
	// CommitHash is not 40 lowercase hex — the only form substituteCommitHash
	// can find again on the next bump.
	ErrInvalidCommitHash = errors.New("invalid commit hash for ebuild")
	// ErrInvalidRequiredVersion is returned (wrapped) when a version captured
	// for a `requires` entry and replayed from pending.json is not a Gentoo
	// version as written. It is about to be written into bash, so the bump fails
	// before anything is staged or copied.
	ErrInvalidRequiredVersion = errors.New("invalid required version for ebuild")
	// ErrRequirementsNotCaptured marks a pending entry of a record that declares
	// `requires` but carries no captured version for one of them — an entry
	// queued before the record declared it, or by an older release. The bump
	// waits until `--check` captures the requirement.
	ErrRequirementsNotCaptured = errors.New("requirements not captured; re-run --check to capture them")
	// ErrRequirementPinNotFound is returned (wrapped) when the record declares a
	// pin the new ebuild does not carry: the registry and the ebuild disagree.
	ErrRequirementPinNotFound = errors.New("pinned requirement not found in ebuild")
)

// auxValueRe and commitHashRe are the shapes an upstream-supplied value must
// have before any writer puts it into an ebuild — checkUpstreamValues applies
// them in Applier.Apply, Applier.Validate and applySubstitutions. `$` without `(?m)` only
// matches at the end of the text, so a trailing newline is refused.
var (
	auxValueRe   = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,128}$`)
	commitHashRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
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
	// CompileDistdir is the directory THIS RUN resolved for the privileged
	// compile gate's archives — the apply's own fetch, then --distdir, then the
	// configured one, whichever staticGateDistdir chose. It is recorded WHATEVER
	// then happened to it, which is the point: a reader who only saw the
	// enforced directory could not tell a run that resolved none from one whose
	// privilege tool could not carry the one it had.
	//
	// It is empty when the compile did not run at all — including the run
	// --require-isolation refused — and when this run resolved no directory of
	// its own, which is the honest answer rather than an invented default:
	// Portage then answers from its own configuration.
	CompileDistdir string
	// CompileDistdirEnforced reports whether that directory actually reached the
	// ebuild child, and it is a separate fact because the privilege tool decides
	// it. Measured on this host: sudo's env_reset
	// discards an exported DISTDIR, so what carries the value is sudo's own
	// argument form, `sudo DISTDIR=<dir> ebuild …`. `doas` has no such form —
	// handed one it would try to execute a program named "DISTDIR=…" — so on a
	// doas host the directory is resolved and NOT enforced.
	//
	// The pair therefore states three things a single field could not: nothing
	// resolved (empty, false), resolved and enforced (dir, true), resolved but
	// uncarriable (dir, false). The compile gate's reason is what says the third
	// one out loud, rather than letting a PASS imply a hermeticity the run never
	// had.
	CompileDistdirEnforced bool
	// CleanedOldVersion is the highest version whose ebuild --clean removed. It
	// is the single-version view of CleanRemoved, kept because callers that
	// print one "Removed: pkg-X.ebuild" line predate the sweep; empty when clean
	// was off, removed nothing, or was blocked.
	CleanedOldVersion string
	// CleanKept maps each version --clean left in place to the registry entry
	// key that claims it. An empty value means the version was kept by a
	// rule rather than by an entry — the live -9999 rule or the last-non-live
	// floor — since a registry key is never itself empty.
	CleanKept map[string]string
	// CleanRemoved lists the versions whose ebuilds --clean actually deleted,
	// ascending. It is the executed plan, not the intended one: a sweep stopped
	// by a removal failure reports the prefix it managed to delete.
	CleanRemoved []string
	// CleanWarning records a non-fatal failure of the --clean step (the update
	// itself still succeeded). Empty on success.
	CleanWarning string
	// RegistryWarning records a non-fatal failure to write the applied version
	// back into packages.toml. It is deliberately NOT CleanWarning:
	// the two report different steps — the pin is written on every successful
	// apply, the sweep only under --clean — and the CLI prints CleanWarning on a
	// line labelled "Clean:", so reusing it would blame the wrong step for a
	// registry failure. Empty when the pin was recorded.
	RegistryWarning string
	// MetadataCacheWarning records a non-fatal failure to regenerate the
	// package's metadata/md5-cache entries after the bump. Empty on success,
	// and when the overlay keeps no md5-cache.
	MetadataCacheWarning string
	// Obsolete indicates the pending entry no longer corresponds to anything to
	// apply: the package was removed from the overlay, or the overlay is already
	// at/beyond the target version. The entry is pruned from pending.json and
	// this is NOT counted as a failure (Success stays false, Error stays nil).
	Obsolete bool
	// Held indicates the package carries hold = true in packages.toml and was
	// therefore refused. Like Obsolete this is NOT a failure (Success false,
	// Error nil), but unlike Obsolete the pending entry is KEPT: the update is
	// real and still pending, it just may not be applied automatically. A held
	// package reaches this point when its update was recorded before the hold
	// was set (CheckPackage skips held packages, but pending.json outlives the
	// check that wrote it).
	//
	// enabled = false is refused the same way and reported through the same
	// field: both are a maintainer's "do not auto-bump", and HoldReason names
	// which of the two keys said so.
	Held bool
	// Waiting lists the requirements a bump is waiting for, as atoms
	// ("~dev-lang/dart-3.14.0"), or the instruction to re-run --check when the
	// pending entry predates them. Like Held it is NOT a failure (Success false,
	// Error nil) and the pending entry is kept: the bump applies once the
	// required version is in the overlay or ::gentoo.
	Waiting []string
	// HoldReason is the packages.toml key that refused the package
	// ("hold = true" or "enabled = false"). Empty unless Held is true.
	HoldReason string
	// ObsoleteReason explains, in user-facing terms, why the entry was deemed
	// obsolete. Empty unless Obsolete is true.
	ObsoleteReason string
	// Fixed indicates a gate failed and was recovered by an LLM fixer: the ebuild
	// was edited and BENTOO'S OWN re-run of that same gate then succeeded. Two
	// gates can set it — the manifest step (`pkgdev manifest`) and the build
	// gate — and in both cases the flag records the re-run's verdict, never the
	// agent's self-report. A bump whose re-run
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
	// it is what makes a failure inspectable: the operator can read the exact
	// ebuild the gates read and re-run a gate by hand, without repeating the work
	// that produced it. Retention is expressed by the path itself — one tree per
	// package and version — so there is no index to consult and nothing to unlock.
	// applySummary appends it to the failure line; the CLI prints it as "Staged:".
	//
	// A COMPLETED promotion clears it again: once the bytes are in the overlay the
	// tree says nothing new, and the field keeps its one meaning — "here is the
	// evidence of what went wrong". Empty, therefore, after a successful apply,
	// an apply with no staging root, and one that failed before staging.
	StagedPath string
	// DepthRequested is how far this bump was ASKED to be validated: the depth
	// class, tier, configuration and the operator's flags resolved to, raised by
	// a reviewer escalation where one applied.
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
	// The pair is the rule "an outcome names its own reach" applied to the
	// ladder itself: a bump promoted with its build gates skipped for want of an
	// installed dependency has DepthRequested "configure" and DepthReached
	// short of it, so nobody can read the green as "it builds".
	DepthReached string
	// DepthReason names the input that decided the depth, and — when the two
	// depths differ — why validation stopped short, naming the atoms or the host
	// condition that stopped it. It is never empty on a staged apply.
	DepthReason string
	// ValidationSource says which of the two validation paths this bump took:
	// "staged" when a retained tree that had already been proved was promoted as
	// it stood, "this-run" when the gates ran here. It states per package which
	// of the two happened, carried on the result so the reports and the summary
	// line can both say it.
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
	// gentooPath is the ::gentoo tree the bump re-reads before it renames an
	// ebuild forward. Empty disables the check entirely; WithApplierGentooPath
	// sets it, and cmd/ defaults it to /var/db/repos/gentoo.
	//
	// WHY THIS FIELD EXISTS. A bump copies OUR ebuild to the new version and
	// never looks at ::gentoo. Every fix the distribution made to that revision
	// in the meantime is dropped in silence, because nothing in the process
	// reads the other tree. A 2026-09-04 audit traced 40 findings across the
	// overlay to exactly this, with named commits (fe40ab830 webkit, 2600d0981
	// nodejs, 736e2d100 libqmi); the very next day's run reproduced it live on
	// media-libs/mesa and net-misc/modemmanager, carrying a stale RUST_MIN_VER
	// and a lowered gobject-introspection floor into fresh ebuilds.
	//
	// The check WARNS and never merges. Merging would need to know which side
	// is right, which is a judgement the applier has no basis to make; a
	// warning at the moment of the rename puts it in front of the one person
	// who does, while the bump is still in their hands.
	gentooPath string

	// reenabled holds packages re-enabled in packages.toml after this Applier
	// loaded it (--revive enables an orphan's entry, then applies it). Their
	// enabled = false snapshot in configs is stale and must not refuse the bump.
	// Guarded by reenabledMu: revives run concurrently on one Applier.
	reenabledMu sync.Mutex
	reenabled   map[string]bool
	// pending manages pending updates
	pending *PendingList
	// logsDir is the directory for storing compile logs
	logsDir string
	// configDir is the bentoo autoupdate config directory — in production
	// ~/.config/bentoo/autoupdate — and it is held for exactly one purpose: the
	// cache.json in it is where a host-caused build failure records the
	// precondition it found unmet. The record may live NOWHERE else;
	// the obvious alternative, packages.toml, sits in an overlay that
	// auto-commits and pushes within minutes, so one workstation's unreadable key
	// would be published as a claim about everyone's. Empty means "no cache to
	// write to" and is silently skipped, which only a test can produce.
	configDir string
	// confirmFunc is a function to prompt for user confirmation (injectable for testing)
	confirmFunc func(prompt string) bool
	// execCommand is a function to create exec.Cmd bound to a context
	// (injectable for testing). It defaults to exec.CommandContext so a
	// cancelled context kills the spawned manifest/compile process.
	execCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd
	// pendingDeleteFn is the function Apply invokes to remove a package from
	// pending.json after the full success path. It defaults to
	// a.pending.Delete and is overridable via WithApplierPendingDeleteFunc
	// purely for tests that need to simulate a Delete failure.
	// Production callers never supply this option.
	pendingDeleteFn func(pkg string) error
	// setVersionsFn is the function Apply invokes to record the version it just
	// applied in the overlay's registry. It defaults to
	// SetPackageVersions and is overridable via WithApplierSetVersionsFunc
	// purely for tests that need to force a write failure without a real
	// registry. Production callers never supply this option.
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
	// came from the --require-isolation flag or from the config key
	// autoupdate.validate.require_isolation (default false), it
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
	configs map[string]registry.PackageConfig
	// fixer, when non-nil, is invoked when the manifest step fails: it drives an
	// LLM agent to repair the ebuild (e.g. a SRC_URI whose URL convention changed
	// between versions) before the Applier re-runs the manifest to confirm. Set
	// via WithApplierFixer; nil keeps the original fail-fast behaviour.
	fixer fixer.ManifestFixer
	// buildFixer, when non-nil, is invoked when the build gate fails for a reason
	// attributable to the ebuild (a patch that no longer applies, a configure
	// option upstream dropped): it drives an LLM agent to repair the STAGED ebuild,
	// after which the applier re-runs the same gate and that re-run — never the
	// agent's self-report — decides the outcome. Set via
	// WithApplierBuildFixer; nil keeps the original fail-fast behaviour.
	buildFixer fixer.BuildFixer
	// reporter is the progress sink Apply emits its lifecycle to (TaskStart →
	// TaskStage → TaskDone). Set via WithApplierReporter; defaults to tui.Noop()
	// so the silent, fully-buffered behaviour predating the TUI is preserved and
	// every existing test stays byte-identical.
	reporter tui.Reporter
	// runAttached executes the compile-test command and returns its combined
	// output. The privileged child needs the REAL terminal for the sudo/doas
	// password prompt, which rules out capturing its stdout/stderr through
	// a StreamCapture pipe the way runManifest does. The default (set in
	// NewApplier) is exactly cmd.CombinedOutput, so the compile-log path stays
	// byte-identical to the pre-TUI behaviour. The apply driver
	// overrides it via WithApplierRunAttached to release the
	// terminal for the prompt and tee the raw output to the TTY and a capture
	// buffer. A nil override is normalized back to the CombinedOutput default.
	runAttached func(cmd *exec.Cmd) ([]byte, error)
	// distdir, configuredDistdir and distfilesCache are carried, unread, from
	// the CLI to the sweeper this applier builds for the Manifest step: the
	// --distdir flag, autoupdate.distdir, and the read-only cache named by
	// --distfiles-cache / autoupdate.distfiles_cache. Nothing on
	// Applier interprets them — see sweeper() and runManifest, which own the
	// precedence — and all three empty is the production default that lands on
	// the host's own DISTDIR with no cache lookup.
	distdir           string
	configuredDistdir string
	distfilesCache    string
	// stagingRoot is the directory the staged trees are built under — in
	// production <configDir>/staging. Non-empty is what turns the whole
	// staged pipeline on: the candidate is materialised there instead of in the
	// published overlay, every gate reads it there, and the overlay is written
	// exactly once, by promotion, at the end.
	//
	// Empty is the pre-staging path, byte for byte what every release before
	// staging did, and it is what a caller that omits WithApplierStagingRoot gets.
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
	// requireProof refuses to promote a bump whose build gates were SKIPPED. It
	// is the opt-in counterweight to the default: false publishes such a bump
	// with the depth it did not reach named, because a
	// host that lacks a build dependency says nothing about the bump and
	// refusing every one of them would make the feature inert on an ordinary
	// workstation.
	requireProof bool
	// reviewer, when non-nil, reads the two versions' build-declaration
	// difference and may ask for MORE validation than policy chose. Its proposal is advisory and one-way: validate.Escalate combines it
	// with the policy floor and can only raise. Set via WithApplierBumpReviewer;
	// nil skips the review entirely.
	reviewer fixer.BumpReviewer
	// lookPath answers "is this tool installed at all" for the build gates,
	// which ask it before spawning so an absent Portage is reported as a named
	// SKIP rather than as an opaque failure. It defaults to exec.LookPath and is
	// replaced together with execCommand — see WithExecCommand for why the two
	// are one seam and not two.
	lookPath func(name string) (string, error)

	// log receives the applier's diagnostics. Set via WithApplierLogger;
	// NewApplier leaves it discarding when the option is absent. Read it
	// through logger().
	log *slog.Logger
}

// logger returns the applier's logger, or a discarding one for an applier that
// was not built by NewApplier.
func (a *Applier) logger() *slog.Logger {
	return logging.OrDiscard(a.log)
}

// ApplierOption is a functional option for configuring Applier
type ApplierOption func(*Applier)

// WithApplierLogger sets the logger the applier, and every step it runs,
// reports its diagnostics to. Nil keeps the default, which discards them.
// Fixers and the bump reviewer are injected already built, and carry the
// logger their own options gave them.
func WithApplierLogger(l *slog.Logger) ApplierOption {
	return func(a *Applier) {
		a.log = logging.OrDiscard(l)
	}
}

// WithApplierPendingList sets a custom pending list for the applier
func WithApplierPendingList(pending *PendingList) ApplierOption {
	return func(a *Applier) {
		a.pending = pending
	}
}

// WithApplierGentooPath sets the ::gentoo tree consulted before an ebuild is
// renamed forward. An empty path disables the comparison, which is what tests
// that do not care about it get by default: a missing tree must not fail a bump.
func WithApplierGentooPath(dir string) ApplierOption {
	return func(a *Applier) {
		a.gentooPath = dir
	}
}

// WithLogsDir sets a custom logs directory for the applier.
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
// The setting lives in config.yaml as autoupdate.validate.require_isolation,
// not in the registry: a registry key is expensive to move once written, and in
// config.yaml an unknown key is a warning rather than a silently disabled
// record. It defaults to false, the behaviour before the key existed. This
// option remains the only way the value reaches the Applier, from the flag or
// from that key alike.
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

// WithApplierPendingDeleteFunc overrides the function Apply invokes to remove
// a package from pending.json after a successful apply. The default is
// a.pending.Delete. This option exists for tests that need to simulate a
// Delete failure; a nil fn is ignored.
func WithApplierPendingDeleteFunc(fn func(pkg string) error) ApplierOption {
	return func(a *Applier) {
		if fn != nil {
			a.pendingDeleteFn = fn
		}
	}
}

// WithApplierSetVersionsFunc overrides the function Apply invokes to record the
// applied version in packages.toml after a successful apply. The
// default is SetPackageVersions. This option exists for tests that need to
// simulate a write failure or to observe the pin without a registry
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
func WithApplierPackagesConfig(cfg *registry.PackagesConfig) ApplierOption {
	return func(a *Applier) {
		if cfg != nil {
			a.configs = cfg.Packages
		}
	}
}

// WithApplierFixer wires an LLM manifest fixer into the applier. When the manifest
// step fails, the applier asks the fixer to repair the ebuild and then re-runs the
// manifest to confirm. A nil fixer is ignored, preserving the fail-fast behaviour.
func WithApplierFixer(fixer fixer.ManifestFixer) ApplierOption {
	return func(a *Applier) {
		if fixer != nil {
			a.fixer = fixer
		}
	}
}

// WithApplierBuildFixer wires an LLM build fixer into the applier. When the build
// gate fails for a reason attributable to the ebuild, the applier asks the fixer
// to repair the STAGED ebuild and then re-runs the same gate to decide. A nil
// fixer is ignored, preserving the fail-fast behaviour.
//
// The nil discipline is WithApplierFixer's, deliberately and to the letter: a
// provider that could not be constructed leaves the applier exactly as it was
// rather than half-configured, so "no LLM was asked for" and "the LLM could not
// be built" produce the same, predictable run.
func WithApplierBuildFixer(fixer fixer.BuildFixer) ApplierOption {
	return func(a *Applier) {
		if fixer != nil {
			a.buildFixer = fixer
		}
	}
}

// WithApplierReporter wires a progress reporter into the applier so Apply emits
// its lifecycle (TaskStart → TaskStage → TaskDone) to the TUI/plain sink. A nil
// reporter is normalized to tui.Noop(), preserving the silent, fully-buffered
// behaviour predating the TUI.
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
// pre-TUI behaviour but swallows the sudo/doas password prompt. The apply
// driver supplies a variant that hands the child the real terminal so the
// prompt is visible while teeing its raw output to the TTY and a capture buffer
// (the captured bytes still feed saveCompileLog on failure). A nil fn is
// normalized back to the CombinedOutput default.
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
// autoupdate.distdir from the config file. Both empty — what a
// caller that omits this option gets — is the production default: the DISTDIR
// the host's own package manager reports.
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
// overlay is written exactly once, by promotion, after the gates have passed.
//
// An empty (or blank) root is ignored, exactly as WithApplierFixer ignores a nil
// fixer and for the same reason: the caller that does not supply one keeps the
// behaviour it has always had — the candidate written straight into the overlay
// and rolled back on failure — rather than getting a half-configured pipeline. The
// production root is <configDir>/staging; Stage never picks one itself,
// because the path is where the retention rule is recorded.
func WithApplierStagingRoot(dir string) ApplierOption {
	return func(a *Applier) {
		if root := strings.TrimSpace(dir); root != "" {
			a.stagingRoot = root
		}
	}
}

// WithApplierValidatePolicy supplies the configured depth table and its
// per-package exceptions, which is how far each class of bump is validated.
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
// package tier and configuration alike, in either direction.
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
// SKIPPED, for a host where an unproved publish is not acceptable —
// a builder box, or a maintainer who would rather the sweep stopped than shipped
// something unbuilt.
//
// It is an opt-in counterweight, not a contradiction: the default publishes
// such a bump with the depth it did not reach NAMED, because "this host lacks a
// build dependency" says nothing about the bump, and turning every one of those
// into a refusal would make the feature inert — the same reason isolation is
// not required by default.
func WithApplierRequireProof(require bool) ApplierOption {
	return func(a *Applier) {
		a.requireProof = require
	}
}

// WithApplierBumpReviewer wires an LLM bump reviewer into the applier. It reads
// the difference between the two versions' upstream build declarations and may
// ask for MORE validation than policy chose; validate.Escalate
// applies the proposal and can only ever raise the depth.
//
// A nil reviewer is ignored, exactly as WithApplierFixer ignores a nil fixer and
// for the same reason: a provider that could not be constructed leaves the
// applier as it was rather than half-configured, so "no LLM was asked for" and
// "the LLM could not be built" produce the same, predictable run.
func WithApplierBumpReviewer(reviewer fixer.BumpReviewer) ApplierOption {
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
		configDir:   configDir,
		confirmFunc: defaultConfirmFunc,
		execCommand: exec.CommandContext,
		// SAFE: the real measurement; replaced by WithApplierIsolationProbe in
		// tests so both answers are reachable without privilege.
		isolationProbe: validate.ProbeIsolation,
		// SAFE: the real PATH lookup the build gates ask before spawning;
		// replaced together with execCommand (see WithExecCommand).
		lookPath: exec.LookPath,
		reporter: tui.Noop(), // SAFE: silent default; replaced by WithApplierReporter
		// SAFE: default == today's behaviour (CombinedOutput), so the compile-log
		// path is byte-identical; replaced by WithApplierRunAttached.
		runAttached: func(c *exec.Cmd) ([]byte, error) { return c.CombinedOutput() },
		log:         logging.OrDiscard(nil),
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
	// raw-text write into <overlay>/.autoupdate/packages.toml.
	if applier.setVersionsFn == nil {
		applier.setVersionsFn = registry.SetPackageVersions
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
func (a *Applier) Apply(ctx context.Context, pkg string, compile bool) (result *ApplyResult, _ error) {
	result = &ApplyResult{
		Package: pkg,
	}

	// A context that is already done stops Apply before it reads the pending
	// list or touches the overlay, so the pending entry stays and the deferred
	// orphan rollback below has nothing to undo. The check sits above TaskStart
	// so the reporter never sees a task opened without its matching TaskDone.
	if err := ctx.Err(); err != nil {
		result.Error = fmt.Errorf("apply %s was not started: %w", pkg, err)
		return result, result.Error
	}

	// Open the task in the progress reporter and guarantee a matching TaskDone on
	// every return path (mirrors the deferred orphan-rollback below, keyed on the
	// same named result). The package name doubles as both the task id and its
	// display label. Under the default Noop reporter these are no-ops, so the
	// silent, fully-buffered behaviour is byte-identical to before.
	a.reporter.TaskStart(pkg, pkg)
	defer func() {
		if result == nil {
			a.reporter.TaskDone(pkg, false, "", "")
			return
		}
		a.reporter.TaskDone(pkg, result.Success, applySummary(result), "")
	}()

	run := &applyRun{a: a, pkg: pkg, compile: compile, result: result}
	if done, err := run.preflight(); done != nil {
		return done, err
	}

	// From here to promotion nothing writes into the published overlay, and
	// promotion is the last thing this function does. Everything in between is
	// preparation and gates, and the overlay must stay byte-identical while any
	// of them runs.
	//
	// rollbackPublished is that promise's counterweight. It stays nil until this
	// apply has actually placed something in the published overlay, and then undoes
	// exactly what was placed. It REPLACES the unconditional "remove one path"
	// rollback this function used to register right after copyEbuild: that was
	// correct while the candidate was written into the overlay first, and is wrong
	// the moment the candidate lives in a staged tree, where the published path does
	// not exist yet and the tree that does exist must be RETAINED, not removed.
	defer func() {
		if result == nil || result.Error == nil || run.rollbackPublished == nil {
			return
		}
		run.rollbackPublished(result.Error)
	}()

	return run.execute(ctx)
}

// applyRun is the state one Apply call carries from phase to phase. Apply owns
// the deferred cleanups; each phase method reads and fills these fields, so a
// defer that reads one at return time sees the value the last phase left.
//
// A phase that may end the apply returns the pair Apply returns; a nil result
// means the apply goes on to the next phase.
type applyRun struct {
	a       *Applier
	pkg     string
	compile bool
	result  *ApplyResult

	update         *PendingUpdate
	currentVersion string
	newVersion     string
	depth          validate.DepthDecision

	// rollbackPublished undoes what this apply placed in the published overlay;
	// nil until something was placed (see Apply).
	rollbackPublished publishedUndo

	// gates accumulates every outcome this run produces. It lives on the run
	// rather than where the first one is assigned so that the record written
	// beside the staged tree (see execute) sees the WHOLE list however Apply
	// returns — including the failing exits, whose record is the one that
	// stops the next run promoting a rejected bump.
	gates []validate.GateResult

	inputs          stagedInputs
	retainedVerdict string
	cand            candidatePaths
}

// preflight runs every check that precedes staging: the hold refusal, the
// pending lookup, the target and current versions, the requirements and the
// depth decision. Nothing has been written into the overlay on any path that
// ends the apply here.
func (r *applyRun) preflight() (*ApplyResult, error) {
	if r.refuseHeld() {
		return r.result, nil
	}

	// Get pending update
	update, found := r.a.pending.Get(r.pkg)
	if !found {
		r.result.Error = ErrPackageNotInPending
		return r.result, r.result.Error
	}
	r.update = update

	r.result.OldVersion = update.CurrentVersion

	if err := r.resolveTarget(); err != nil {
		return r.a.failApply(r.pkg, r.result, err)
	}
	if done, err := r.resolveCurrent(); done != nil {
		return done, err
	}
	if done, err := r.awaitRequirements(); done != nil {
		return done, err
	}

	// How deep this bump is validated, and on whose authority.
	// Resolved HERE, before anything is staged, for two reasons: the report can
	// then name the depth even for a bump whose tree was never built, and every
	// gate below reads one decision rather than each re-deriving its own.
	r.depth = r.a.depthFor(r.pkg, r.currentVersion, r.newVersion)
	r.result.DepthRequested = r.depth.Depth.String()
	r.result.DepthReason = r.depth.Reason
	return nil, nil
}

// refuseHeld reports whether the package is refused, recording why on result.
func (r *applyRun) refuseHeld() bool {
	// Refuse a held package before touching anything. hold = true is the
	// maintainer's "present, but never auto-bump" decision, and until now it was
	// enforced in the checker alone: CheckAll skips held packages, but an explicit
	// `--check <pkg> --force` does not, and it writes the update to pending.json
	// like any other. From there `--apply all` applied the very bump the hold
	// existed to prevent. The guard belongs here because this is the only place
	// every apply path passes through.
	//
	// enabled = false is refused here for the same reason: CheckAll skips a
	// disabled entry, but an update already in pending.json — recorded before
	// the disable, or by an explicit check — would otherwise still be applied.
	reason := r.a.refusal(r.pkg)
	if reason == "" {
		return false
	}
	r.result.Held = true
	r.result.HoldReason = reason
	if update, found := r.a.pending.Get(r.pkg); found {
		r.result.OldVersion = update.CurrentVersion
		r.result.NewVersion = update.NewVersion
	}
	return true
}

// resolveTarget validates the upstream values and sets the decorated target
// version on the run and the result. A returned error is the apply's failure.
func (r *applyRun) resolveTarget() error {
	// Upstream version detection can carry a leading tag prefix (e.g. the git
	// tag "v9.2.0588"). A Gentoo ebuild filename requires a bare PV, so strip
	// the prefix before it reaches the filename and the manifest step; otherwise
	// `pkgdev manifest` rejects it with "does not follow correct package syntax".
	// Validate up front so a non-version (or a string still invalid after
	// stripping) fails with a clear error instead of a cryptic portage one.
	newVersion := parse.StripVersionPrefix(strings.TrimSpace(r.update.NewVersion))
	if !ebuild.IsValidVersion(newVersion) {
		return fmt.Errorf("%w: %q (from %q)", ErrInvalidNewVersion, newVersion, r.update.NewVersion)
	}
	// The aux value and the commit hash come from upstream, untrimmed and
	// unrepaired here: the checker already trimmed them, so inner or trailing
	// whitespace is refused. Gated before anything is staged or copied, so a
	// refused package leaves its directory byte-identical.
	if err := checkUpstreamValues(r.pkg, r.update); err != nil {
		return err
	}
	// Attach the slot's pinned revision, when the entry declares one. Upstream
	// yields a bare PV; for a slot discriminated by its revision suffix that PV
	// names the WRONG slot's ebuild, so the whole apply — copy destination,
	// manifest, compile, clean — has to run against the decorated version from
	// here on. Validation stays on the bare upstream value above; the suffix is
	// well-formed by construction.
	r.newVersion = ebuilds.ApplyRevision(newVersion, r.a.configs[r.pkg].Revision)
	r.result.NewVersion = r.newVersion
	return nil
}

// resolveCurrent re-resolves the current version against the live overlay. It
// ends the apply on a failure or a pruned entry.
func (r *applyRun) resolveCurrent() (*ApplyResult, error) {
	// Re-resolve the current version against the live overlay rather than
	// trusting update.CurrentVersion. That field is a snapshot from check-time
	// and drifts: the overlay may have been bumped past it, or the package
	// removed entirely. Blind trust produced a cryptic "source ebuild not found"
	// when the recorded version's ebuild was already gone. Re-resolution
	// self-heals a stale current_version and lets a genuinely obsolete entry be
	// pruned with a clear outcome instead of a hard failure.
	currentVersion, err := r.a.resolveCurrentVersion(r.pkg)
	if err != nil {
		// A slot that matches nothing is a config error, not an obsolete entry:
		// the package is present, its key is wrong. Pruning would delete the
		// pending record and report success-ish, hiding the typo. Fail loudly.
		if errors.Is(err, ebuilds.ErrSlotNotFound) {
			return r.a.failApply(r.pkg, r.result, err)
		}
		// Package no longer present in the overlay (removed/renamed). The pending
		// entry is obsolete — prune it and report, not as a failure.
		return r.a.pruneObsolete(r.pkg, r.result,
			fmt.Errorf("%w: %s no longer in overlay (%w)", ErrObsoletePending, r.pkg, err))
	}
	r.currentVersion = currentVersion
	r.result.OldVersion = currentVersion

	// Overlay already at or beyond the target: the update was already applied or
	// has been superseded by a newer bump. A copy would be pointless (or a
	// downgrade) — prune the stale entry instead.
	if ebuild.CompareVersions(currentVersion, r.newVersion) >= 0 {
		return r.a.pruneObsolete(r.pkg, r.result,
			fmt.Errorf("%w: overlay already at %s (target %s)", ErrObsoletePending, currentVersion, r.newVersion))
	}
	return nil, nil
}

// awaitRequirements gates the bump on its captured requirements. It ends the
// apply on a failure or a bump left waiting.
func (r *applyRun) awaitRequirements() (*ApplyResult, error) {
	// Requirements are gated after the captured values were checked above, so an
	// invalid replayed version fails as such and is never turned into a wait, and
	// after the obsolete prune, so an entry the overlay already passed is pruned
	// rather than left waiting forever. Nothing has been written yet.
	waiting, err := r.a.unmetRequirements(r.pkg, r.update)
	if err != nil {
		if !errors.Is(err, ErrRequirementsNotCaptured) {
			return r.a.failApply(r.pkg, r.result, err)
		}
		r.result.Waiting = []string{err.Error()}
		return r.result, nil
	}
	if len(waiting) > 0 {
		r.result.Waiting = waiting
		return r.result, nil
	}
	return nil, nil
}

// execute stages the candidate, runs the manifest step and hands over to the
// gates. It owns the two cleanups armed during staging, so they run before
// Apply's own on every exit.
func (r *applyRun) execute(ctx context.Context) (*ApplyResult, error) {
	if done, err := r.reuseRetainedTree(ctx); done != nil {
		return done, err
	}

	// Materialise the candidate where the gates will read it: in a staged tree
	// outside the overlay when a staging root was configured, in the published
	// overlay otherwise.
	// Only the pre-staging branch arms the rollback, and it arms it with the one
	// path it just wrote. The staged branch leaves it nil, because after it there
	// is still nothing in the published overlay to take back.
	if prepErr := r.prepareCandidate(); prepErr != nil {
		return r.a.failApply(r.pkg, r.result, prepErr)
	}

	// A record of what the gates said, beside the tree they said it about,
	// written however this apply ends.
	//
	// A defer rather than a call at each exit, and that is not brevity: there are
	// six ways out from here down, and the ONE that must never be forgotten is the
	// failing one — an unrecorded failed tree would be promoted by the next run's
	// reuse path on a match alone. The closure reads `gates` and `depth` at return time, so it
	// records the final list and the depth a reviewer's escalation may have raised.
	if stagedRoot := r.result.StagedPath; stagedRoot != "" {
		defer func() {
			r.a.recordStagedProof(ctx, stagedRoot, r.pkg, r.newVersion, r.inputs, r.gates, r.depth.Depth)
		}()
	}

	// Run manifest command. When a fixer is wired, a failure here triggers a
	// single agentic repair-and-retry before the apply is declared failed; the
	// outcome (including whether a fix was applied) is recorded on result. On the
	// staged path this is `pkgdev manifest` inside the staged tree against a
	// private distdir, so no directory the host shares changes while it runs.
	r.a.reporter.TaskStage(r.pkg, "manifest")
	fetchedDistdir, manifestErr := r.a.runManifestWithFix(ctx, r.cand, r.pkg, r.newVersion, r.result)
	// Armed the instant the directory can exist, so every exit below — the six
	// failing ones included — takes it back. A removal added after
	// the fact is a removal one path will not have.
	defer removeStagedDistdir(r.a.logger(), fetchedDistdir)
	// And handed to the gates, which are its consumer. On a host
	// that has never fetched this release, what this step just downloaded is the
	// only copy of the candidate's archive in existence locally.
	r.cand.fetchedDistdir = fetchedDistdir
	if manifestErr != nil {
		return r.a.failApply(r.pkg, r.result, fmt.Errorf("%w: %w", ErrManifestFailed, manifestErr))
	}
	// pkgdev exiting 0 does not prove every SRC_URI file got a DIST line.
	if err := r.a.checkManifestCoverage(ctx, r.cand.pkgDir, r.pkg, r.newVersion); err != nil {
		return r.a.failApply(r.pkg, r.result, err)
	}

	return r.gateAndPromote(ctx)
}

// reuseRetainedTree asks what an earlier run already proved about this exact
// bump and, when the retained tree may be promoted as it stands, promotes it,
// which ends the apply.
//
// Taken BEFORE anything is staged, for the reason staging itself makes
// unavoidable: validate.Stage replaces the retained tree, so a question
// asked after it is a question about a tree this run just rebuilt.
//
// retainedVerdict is what that question was ANSWERED with, kept on the run
// rather than only on the result because the reviewer's re-decision
// reassigns result.DepthReason wholesale. Without it the answer survives only
// on the promoting path — which returns before that line — and every REFUSAL
// this file computes ("its record shows configure FAILED", "no readable
// record", "produced by validate rather than by the applier") reaches the
// operator as an ordinary slow apply with no explanation attached. Each
// package states which of the two happened, and the half worth stating is
// the half where the retained tree was NOT used.
func (r *applyRun) reuseRetainedTree(ctx context.Context) (*ApplyResult, error) {
	if r.a.stagingRoot == "" {
		return nil, nil
	}
	captured, err := r.a.stagedInputsFor(r.pkg, r.currentVersion, r.update)
	if err != nil {
		return r.a.failApply(r.pkg, r.result, err)
	}
	r.inputs = captured

	// One of the two answers is recorded on every staged apply, and
	// the default is the honest one — the gates below run in this run unless
	// the retained tree takes their place.
	r.result.ValidationSource = ValidationSourceThisRun

	reuse := r.a.reusableStagedTree(r.pkg, r.newVersion, r.inputs, r.depth.Depth)
	if reuse.root != "" {
		// Only when a tree was actually there. "Which of the two
		// happened" is already on the result and in the summary line; what
		// this adds is the WHY, and "there was no retained tree" explains
		// nothing an operator did not know from the absence of one.
		r.retainedVerdict = reuse.reason
		r.result.DepthReason = appendDepthReason(r.result.DepthReason, r.retainedVerdict)
	}
	if reuse.err != nil {
		// The retained tree matched this bump exactly and its distfile moved
		// underneath it. Reported against the staged proof, because that is
		// what the decision was taken on — nothing was validated here.
		r.result.ValidationSource = ValidationSourceStaged
		r.result.StagedPath = reuse.root
		return r.a.failApply(r.pkg, r.result, reuse.err)
	}
	if !reuse.promote {
		return nil, nil
	}
	return r.promoteRetained(ctx, reuse)
}

// promoteRetained publishes a retained tree that was already proved, with no
// gate run in this run.
func (r *applyRun) promoteRetained(ctx context.Context, reuse stagedReuse) (*ApplyResult, error) {
	// The hours were already spent. Nothing between here and the
	// published write runs a gate, which is the entire economic argument
	// — an operator who pays for `--check --llm` and then pays again for
	// `--apply` stops running the check first.
	r.result.ValidationSource = ValidationSourceStaged
	r.result.StagedPath = reuse.root
	r.result.DepthReached = reuse.reached

	promoted, err := r.a.promote(ctx, reuse.cand, r.pkg, r.newVersion)
	if err != nil {
		return r.a.failApply(r.pkg, r.result, err)
	}
	r.rollbackPublished = promoted

	r.result.Success = true
	// Retention's other direction, exactly as on the validating path: the
	// retained tree is a failure's evidence, and there is no failure here.
	r.result.StagedPath = ""
	r.a.completeApply(ctx, r.pkg, r.newVersion, r.result)
	return r.result, nil
}

// prepareCandidate materialises the candidate in the published overlay or in a
// staged tree; only the overlay route arms rollbackPublished.
func (r *applyRun) prepareCandidate() error {
	var prepErr error
	if r.a.stagingRoot == "" {
		r.cand, r.rollbackPublished, prepErr = r.a.prepareInOverlay(r.pkg, r.currentVersion, r.newVersion, r.update)
	} else {
		r.cand, prepErr = r.a.prepareInStagingTree(r.pkg, r.currentVersion, r.newVersion, r.update, r.result)
	}
	return prepErr
}

// gateAndPromote runs the gates on the prepared candidate and, when every gate
// allows it, publishes it and completes the apply.
func (r *applyRun) gateAndPromote(ctx context.Context) (*ApplyResult, error) {
	a, pkg, result := r.a, r.pkg, r.result

	// The static gates — the Meson option gate and the advisory QA scan, reused
	// verbatim. They read files that already exist, so they cost no build and can
	// sit ahead of StatusValidated below. That placement is the whole point of
	// the slot: a gate added ABOVE that line instead of below it would silently
	// undo the move below, and the state's meaning — "passed the static gates" —
	// would quietly go back to "the manifest ran".
	r.gates = a.runStaticGates(ctx, r.cand, pkg, r.newVersion)

	// Update status to validated.
	//
	// MOVED: this used to be written the moment the
	// manifest step returned. It now sits after the static gates, and the state's
	// MEANING NARROWS with the move — it says "passed the static gates", not "ready
	// to publish" and, on the staged path, no longer "the ebuild is in the
	// overlay". A bump can sit at `validated` having failed a later gate and never
	// been promoted, and the column `--list` renders has to be read that way.
	if err := a.pending.SetStatus(pkg, StatusValidated, ""); err != nil {
		result.Error = fmt.Errorf("failed to update status: %w", err)
		return result, result.Error
	}

	// The optional bump reviewer, after the static gates and before anything
	// is built — it reads a diff and may only ask for MORE gates, never fewer.
	// A run with no reviewer wired passes the policy depth straight through.
	r.depth = a.reviewBump(ctx, r.cand, pkg, r.currentVersion, r.newVersion, r.depth, &r.gates)
	result.DepthRequested = r.depth.Depth.String()
	// The reviewer may have raised the depth, so its reason REPLACES the policy's
	// — but the retained tree's verdict answers a different question and is put
	// back beside it. Assigning depth.Reason alone here is what used to drop it.
	result.DepthReason = appendDepthReason(r.depth.Reason, r.retainedVerdict)

	if err := r.runDepthGates(ctx); err != nil {
		return a.failApply(pkg, result, err)
	}

	// The outcome states its own reach, and says why it stops
	// where it does, whether or not this apply is about to succeed.
	a.recordDepthReached(result, r.gates, r.depth.Depth)

	if err := r.promotionRefusal(ctx); err != nil {
		return a.failApply(pkg, result, err)
	}

	// The published overlay's first and only write of this apply. On the
	// pre-staging path there is nothing to promote: copyEbuild already put the
	// candidate there and `pkgdev manifest` already regenerated the Manifest in
	// place.
	if r.cand.staged {
		promoted, err := a.promote(ctx, r.cand, pkg, r.newVersion)
		if err != nil {
			return a.failApply(pkg, result, err)
		}
		// Armed only now: from this point a failure DOES have something published
		// to take back, and promotion's own rollback is what knows the difference
		// between the ebuild (remove it) and the Manifest (restore it).
		r.rollbackPublished = promoted
	}

	result.Success = true

	// Retention, the other direction: the retained tree is a FAILURE's evidence, so the
	// path is dropped the moment there is no failure to explain. Cleared here and
	// not earlier because this line is where success is finally decided — every way
	// of not being promoted has already returned through failApply, carrying the
	// path with it — and not later because nothing below can turn this apply back
	// into a failure: the pending delete, the registry pin and the --clean sweep all
	// report their misses as warnings and deliberately leave Success true.
	//
	// The tree itself is left on disk. Removing it would be a filesystem operation
	// whose failure this path has no honest way to report, and it would buy nothing:
	// the next attempt at this same package and version restages over it, so
	// what is left is one directory per version, not a growing pile per run.
	result.StagedPath = ""

	a.completeApply(ctx, pkg, r.newVersion, result)
	return result, nil
}

// runDepthGates runs the build gates at the selected depth, or the compile
// gate when --compile was asked for. A returned error is the apply's failure;
// the reach is already recorded on result when it is returned.
func (r *applyRun) runDepthGates(ctx context.Context) error {
	// The build gates, at the depth selected above. They are the
	// generalisation of the compile gate below, so the two never both run: with
	// --compile the shipped gate keeps its prompt, its privilege and its repair
	// path, and running the depth gates beside it would build the same tree twice.
	if !r.compile {
		r.a.reporter.TaskStage(r.pkg, "build gates")
		buildGates, buildErr := r.a.runBuildGates(ctx, r.cand, r.pkg, r.newVersion, r.depth.Depth, r.result)
		r.gates = append(r.gates, buildGates...)
		if buildErr != nil {
			r.a.recordDepthReached(r.result, r.gates, r.depth.Depth)
			return buildErr
		}
		return nil
	}

	// Run compile test if requested. It runs against cand's repository, which on
	// the staged path is the staged tree: a gate that built out of the published
	// overlay would be reading a candidate that is not there yet.
	r.a.reporter.TaskStage(r.pkg, "compile")
	logPath, err := r.a.runCompile(ctx, r.cand, r.pkg, r.newVersion, r.result)
	if err != nil {
		r.result.LogPath = logPath
		r.a.recordDepthReached(r.result, r.gates, r.depth.Depth)
		return err
	}
	r.gates = append(r.gates, r.a.compileGateResult(r.cand, r.pkg, r.newVersion, r.result)...)
	return nil
}

// promotionRefusal returns the first reason the candidate may not be
// published, or nil when every rule allows it.
func (r *applyRun) promotionRefusal(ctx context.Context) error {
	// A host that asked for proof does not get a publish built on skips.
	// It is deliberately NOT folded into PromotionDecision: that function's rule
	// is "PASS or SKIPPED promotes", and this is the operator subtracting
	// from it, which is a different authority and belongs where it can be seen.
	// The same invariant, said early so the operator reads the interruption
	// instead of refuseUnproved's "proof at depth X is required" — true, but it
	// blames configuration for a Ctrl-C. promote() enforces it regardless.
	if err := r.a.refuseOnInterrupt(ctx, r.pkg, r.newVersion); err != nil {
		return err
	}

	if err := r.a.refuseUnproved(r.gates, r.pkg, r.newVersion, r.depth.Depth); err != nil {
		return err
	}

	// The candidate may be published only once every gate up to the selected
	// depth has reported PASS or SKIPPED. The rule lives in one pure function so it
	// is asserted directly rather than only through a real promotion, and so that a
	// gate added above cannot reach the overlay without passing through it.
	//
	// The staging error is nil by construction: a tree that could not be prepared
	// already withdrew the bump in prepareInStagingTree, so a promotion
	// decision is only ever reached WHERE a staged tree exists.
	//
	// The refusal is enriched with the failing gates' own error findings before it
	// leaves here (refusalWithFindings): PromotionDecision names the gate, and an
	// apply's only channel to the operator is this one error — "the options gate
	// reported FAILED" without the option it found would send them off to diff two
	// tarballs by hand, which is the work these gates replace.
	if ok, reason := validate.PromotionDecision(r.gates, nil); !ok {
		return refusalWithFindings(reason, r.gates)
	}
	return nil
}

// completeApply is the bookkeeping every promoted bump gets once the published
// overlay holds it: the pending entry is dropped, the registry pin is written and
// `--clean` sweeps.
//
// It is a function rather than the tail of Apply because promotion has TWO
// routes to this point — the gates ran here, or a retained tree that had
// already been proved was promoted as it stood — and a bump that reached
// the overlay by the second route needs exactly the same three steps. Two copies
// would diverge in the direction that hurts: a promotion with no pin leaves
// `--clean` aiming at the only ebuild present.
//
// NOTHING HERE MAY FAIL THE APPLY. Every miss is a warning on the result with
// Success left true and Error left nil — setting Error would fire the deferred
// rollback and delete the ebuild this apply just published.
func (a *Applier) completeApply(ctx context.Context, pkg, newVersion string, result *ApplyResult) {
	// Remove the now-applied package from pending.json so `--list` no
	// longer surfaces it. A Delete failure is a bookkeeping miss, not
	// an apply failure — log a Warn (through the applier's logger, which tests
	// inject to capture it) but keep result.Success == true and result.Error == nil
	// so the deferred orphan-rollback (keyed on result.Error == nil) does not
	// undo the successful apply.
	if err := a.pendingDeleteFn(pkg); err != nil {
		a.logger().Warn("pending: failed to remove the entry after successful apply "+
			"(apply itself succeeded; entry can be cleared manually)", "package", pkg, "err", err)
	}

	// Record the version that just landed on disk as the one this registry
	// entry keeps. Reached only here, past the manifest step, the compile test
	// and a COMPLETED promotion, because the registry must never claim a file
	// that is not there: `--clean` removes every ebuild no entry claims, so a pin
	// written ahead of the file would aim that rule at the only ebuild present
	// and a failed update would become a deleted package. A "validated but not
	// promoted" bump never reaches this line: it returns above through failApply.
	// The value written is newVersion — what was applied — never the pending
	// entry's upstream target, which stays pending.json's business alone. An
	// overlay with no packages.toml gets a warning here and no pin, which is the
	// honest report: nothing recorded the version.
	//
	// A failed write is a bookkeeping miss, exactly like the pending delete
	// above — warn through the applier's logger (which tests inject to capture
	// it), surface it on the result, and leave result.Success true with
	// result.Error nil. Setting result.Error here would fire the deferred
	// orphan-rollback and delete the ebuild this apply just created.
	if err := a.setVersionsFn(a.overlayPath, map[string]string{pkg: newVersion}); err != nil {
		a.logger().Warn("registry: failed to record version in packages.toml "+
			"(the update itself succeeded; the next check's reconciliation can write the pin)",
			"version", newVersion, "package", pkg, "err", err)
		result.RegistryWarning = fmt.Sprintf("could not record version = %q for %s: %v", newVersion, pkg, err)
	}

	// --clean: sweep the package directory against the registry's pins so
	// only the ebuilds an entry claims are left. This runs only on the full
	// success path and is best-effort — a blocked plan, a failed removal or a
	// failed Manifest regeneration is surfaced as a warning on the result and
	// never flips Success, because the update itself is done.
	if a.clean {
		plan, err := a.cleanPackageDir(ctx, pkg, newVersion)
		result.CleanKept = plan.Keep
		result.CleanRemoved = plan.Remove
		if n := len(plan.Remove); n > 0 {
			// The legacy single-version view: Remove is ascending, so its last
			// entry is the highest version actually removed. Set even when the
			// sweep then failed — those files really are gone.
			result.CleanedOldVersion = plan.Remove[n-1]
		}
		if err != nil {
			a.logger().Warn("clean: removing the old versions failed", "package", pkg, "err", err)
			result.CleanWarning = err.Error()
		}
	}

	// Last, so it sees the directory as --clean left it: the new version gets
	// its md5-cache entry and every removed version loses its own.
	if err := a.regenMetadataCache(ctx, pkg, newVersion); err != nil {
		a.logger().Warn("md5-cache: regeneration failed", "package", pkg, "err", err)
		result.MetadataCacheWarning = err.Error()
	}
}

// appendDepthReason adds one more sentence to the reason a result carries,
// keeping the ones already there.
//
// The depth reason is the only free-text field an operator reads to understand
// why a bump was treated the way it was, and the validation source ("the gates ran here"
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
		result.Error = fmt.Errorf("%w (also failed to update status: %v)", result.Error, serr) //nolint:errorlint // secondary error is context; wrapping it would let errors.Is match it
	}
	return result, result.Error
}

// prepareInOverlay materialises the candidate the way every release before
// staging did: the new version's ebuild is copied into the PUBLISHED package
// directory and the per-package substitutions are applied to it there.
//
// It is kept for exactly one reason — a caller that supplied no staging root gets
// the behaviour it has always had, byte for byte.
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

	// The rename just happened. This is the moment the ::gentoo copy stops
	// being consulted, so it is the moment to say so. Advisory only: it never
	// returns an error and never blocks the bump.
	a.warnIfGentooDiverges(pkg, currentVersion)
	a.warnIfFilesNameOldVersion(pkg, currentVersion, newVersion)

	cand, err := publishedCandidate(a.overlayPath, pkg, newVersion)
	if err != nil {
		return candidatePaths{}, nil, err
	}

	// copyEbuild succeeded: a fresh .ebuild now exists in the overlay. If any later
	// step (manifest, status update, compile) fails, that file is an orphan and
	// must be removed so the overlay is not left half-applied.
	undo := orphanEbuildUndo(a.logger(), cand.ebuildPath, pkg, newVersion)

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
// the published overlay, and returns where the gates will find it.
//
// Nothing here writes into the overlay: the source ebuild is READ out of it and
// the candidate is written into the staged tree. That is the whole difference from
// prepareInOverlay, and it is what lets the rule be stated as "the overlay is
// byte-identical while any gate runs" rather than as "it is put back afterwards".
//
// A staged tree that could not be built WITHDRAWS the bump instead of
// letting it through as "no gate failed": every build gate would report SKIPPED,
// nothing would have FAILED, and a candidate no gate ever read would be published.
// validate.PromotionDecision documents that vacuity at length; this function is
// where it is denied, by failing before any gate is consulted.
func (a *Applier) prepareInStagingTree(pkg, currentVersion, newVersion string, update *PendingUpdate, result *ApplyResult) (candidatePaths, error) {
	srcPath := a.EbuildPath(pkg, currentVersion)
	if srcPath == "" {
		return candidatePaths{}, fmt.Errorf("invalid package name format: %s", pkg)
	}
	body, err := os.ReadFile(srcPath) //nolint:gosec // G304: srcPath comes from candidateIn: the overlay, a package key splitPkgAtom confines to one category/package directory, and the current version read from that directory's ebuild filenames
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Same sentinel copyEbuild reports, so a caller that recognises a missing
		// source ebuild keeps recognising it on either path.
		return candidatePaths{}, fmt.Errorf("%w: %s", ebuilds.ErrEbuildNotFound, srcPath)
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
		Key:         pkg,
		Version:     newVersion,
		EbuildBytes: body,
	})
	if err != nil {
		// The ErrStageUnpreparable sentinel survives the wrap, which is the point
		// of it: the caller reacts to staging having failed without enumerating the
		// ways it can.
		return candidatePaths{}, fmt.Errorf("staging %s-%s for validation: %w", pkg, newVersion, err)
	}
	// Named on the result the moment it exists, so it is named even when
	// everything after this fails. A retained tree nobody can find is not an
	// inspectable failure.
	result.StagedPath = stagedRoot

	cand, err := stagedCandidate(stagedRoot, pkg, newVersion)
	if err != nil {
		return candidatePaths{}, err
	}
	// The same advisory prepareInOverlay gives, on the path --apply and --check
	// actually take once a staging root is configured; wired into the overlay
	// path alone it never reached a real bump.
	a.warnIfGentooDiverges(pkg, currentVersion)
	a.warnIfFilesNameOldVersion(pkg, currentVersion, newVersion)
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
//
// It is also the one function every writer of a candidate calls — Apply through
// prepareInOverlay and prepareInStagingTree, Validate through the latter — so the
// upstream-value allow-list is enforced here as the backstop, before either value
// is written: a malformed pair leaves the file byte-identical. Apply and Validate
// still refuse earlier, before anything is staged; a writer added later inherits
// this check without having to remember it.
func (a *Applier) applySubstitutions(ebuildPath, pkg string, update *PendingUpdate) error {
	if err := checkUpstreamValues(pkg, update); err != nil {
		return err
	}
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
	return a.rewriteRequirementPins(ebuildPath, pkg, update)
}

// applySummary derives the short, one-line summary handed to the reporter's
// TaskDone for an apply. It is purely cosmetic (the reporter only renders it):
// on success the new version (noting an LLM fix when one happened), on an
// obsolete prune the reason, and otherwise the failure's error text followed by
// the staged tree that failure left behind. A nil result yields the empty string.
//
// Naming the tree here is the second half of retention, and it is the half that
// makes the first half worth having: a tree retained on disk that no report points at is
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
		// The validation source at the surface the operator reads. Without it a fast
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
	case len(result.Waiting) > 0:
		return "waiting for " + strings.Join(result.Waiting, ", ")
	case result.Held:
		return "held (" + result.HoldReason + ")"
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
	best, err := ebuilds.SelectCurrentEbuild(a.logger(), a.overlayPath, pkg, a.configs[pkg].Series)
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
		a.logger().Warn("pending: failed to prune obsolete entry "+
			"(entry can be cleared manually)", "package", pkg, "err", err)
	}
	return result, nil
}

// cleanPackageDir sweeps pkg's package directory against the registry's pins:
// it deletes every non-live ebuild no entry claims, regenerates the Manifest
// once, and returns the plan it actually executed so the caller can report it.
//
// Deciding by claim instead of by file name is what makes removing more than
// one file survivable, and it also catches what a bump left behind. The plan
// is computed against a COPY of a.configs with pkg's entry pinned to newVersion:
// the pin on disk is written by a separate step of the same run, and the
// PRE-apply registry may be pinless, which would block every clean. The
// overlaid pin is the fact packages.toml is about to record. It covers one entry
// only, so a pinless SIBLING (the other release line) still blocks the directory.
//
// Nothing is removed in three cases, each returned as an error the caller
// surfaces as a warning without failing the apply: a claiming entry declares no
// pin (the error names it); pkg has no registry entry (reachable when
// packages.toml cannot be read — checked explicitly because a SIBLING entry with
// a pin could otherwise delete the ebuild this apply just created); or the
// package directory cannot be read.
func (a *Applier) cleanPackageDir(ctx context.Context, pkg, newVersion string) (sweepPlan, error) {
	cfgs, claimed := a.sweepConfigs(pkg, newVersion)

	plan, err := planSweep(a.logger(), a.overlayPath, cfgs, pkg)
	if err != nil {
		return sweepPlan{}, fmt.Errorf("cannot plan the sweep of %s: %w", pkg, err)
	}

	// The unpinned entry first: this is the blocked case that HAS an entry to name, and
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
	// sweeper so the standalone overlay sweep runs the same code rather
	// than a second implementation of "delete these ebuilds". newVersion is the
	// version this apply just created, and it is the one that remains in the
	// directory — which is what the Manifest step needs.
	return a.sweeper().execute(ctx, pkg, plan, newVersion)
}

// sweeper builds the executor for this applier's overlay, carrying the fields
// the removal loop and the Manifest step need. Deliberately no fixer: the LLM
// manifest repair stays behind runManifestWithFix on Applier, so no sweep can
// reach it.
func (a *Applier) sweeper() *sweeper {
	return newSweeper(a.overlayPath,
		withSweeperExec(a.execCommand),
		withSweeperReporter(a.reporter),
		withSweeperConfigs(a.configs),
		// Both distfile directories reach the Manifest step through here, which
		// is the only path Applier has to it: runManifest delegates to this
		// sweeper.
		withSweeperDistdir(a.distdir, a.configuredDistdir),
		withSweeperDistfilesCache(a.distfilesCache),
		withSweeperLogger(a.logger()),
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
func (a *Applier) sweepConfigs(pkg, newVersion string) (map[string]registry.PackageConfig, bool) {
	entry, ok := a.configs[pkg] // nil-safe: a nil map yields the zero value and ok == false
	if !ok {
		// Nothing to overlay. a.configs is handed over unchanged (planSweep only
		// reads it) and the caller refuses to delete anything in this case.
		return a.configs, false
	}
	cfgs := make(map[string]registry.PackageConfig, len(a.configs))
	for key, cfg := range a.configs {
		cfgs[key] = cfg
	}
	entry.Version = newVersion
	cfgs[pkg] = entry
	return cfgs, true
}

// wouldRemoveSuffix renders the candidates a blocked plan left alone, for the
// tail of its warning. It is empty when there were none, so the
// message never trails a dangling "would have removed:".
func wouldRemoveSuffix(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	return fmt.Sprintf(" (would have removed: %s)", strings.Join(versions, ", "))
}

// warnIfGentooDiverges reports that a bump just carried our own ebuild forward
// without re-reading ::gentoo's copy of the version it came from.
//
// WHAT IT COMPARES, AND WHY THAT AND NOT MORE. Only the version being left
// behind, and only when ::gentoo ships exactly that version. At the same PV the
// two files are describing the same upstream release, so a difference is a real
// decision by one side or the other and is worth a human's attention. At
// different PVs almost everything differs by construction, and a warning that
// fires on every bump is a warning nobody reads.
//
// That deliberately misses cases. A fix ::gentoo made to a version we never
// carried does not show up here. Catching those is the parity sweep's job
// (scripts/gentoo-parity.sh in the overlay), which compares whole trees offline;
// this is the cheap check that runs at the one moment the information is
// actionable.
//
// It NEVER fails the bump: no error return, no gate. An advisory that can break
// a run gets disabled, and then it advises nobody.
func (a *Applier) warnIfGentooDiverges(pkg, oldVersion string) {
	if a.gentooPath == "" {
		return
	}

	category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return
	}

	name := fmt.Sprintf("%s-%s.ebuild", pkgName, oldVersion)
	ours := filepath.Join(a.overlayPath, category, pkgName, name)
	theirs := filepath.Join(a.gentooPath, category, pkgName, name)

	theirBytes, err := os.ReadFile(theirs) //nolint:gosec // both paths are repo-relative package dirs
	if err != nil {
		// ::gentoo does not ship this PV, or does not ship this package at
		// all. Not a finding: most of the overlay is ahead by design.
		return
	}
	ourBytes, err := os.ReadFile(ours) //nolint:gosec // same
	if err != nil {
		return
	}
	if bytes.Equal(ourBytes, theirBytes) {
		return
	}

	a.reporter.TaskStage(pkg, fmt.Sprintf(
		"::gentoo also ships %s and its copy differs — the bump carried OUR %s forward without re-reading it; diff %s %s",
		oldVersion, oldVersion, theirs, ours))
}

// copyEbuild copies the source ebuild to a new file with the updated version.
// Source: {category}/{package}/{package}-{oldVersion}.ebuild
// Destination: {category}/{package}/{package}-{newVersion}.ebuild
func (a *Applier) copyEbuild(pkg, oldVersion, newVersion string) error {
	// Parse package name
	category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return fmt.Errorf("invalid package name format: %s", pkg)
	}

	// Reject same-version copy: srcPath and dstPath would coincide, so the
	// source would be the destination the copy refuses to overwrite.
	if oldVersion == newVersion {
		return fmt.Errorf("source and destination versions are equal: %s", newVersion)
	}

	// Build paths
	pkgDir := filepath.Join(a.overlayPath, category, pkgName)
	srcPath := filepath.Join(pkgDir, fmt.Sprintf("%s-%s.ebuild", pkgName, oldVersion))
	dstPath := filepath.Join(pkgDir, fmt.Sprintf("%s-%s.ebuild", pkgName, newVersion))

	// Check source exists
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ebuilds.ErrEbuildNotFound, srcPath)
	}

	// Refuse to write over an ebuild that already exists. Overwriting it would
	// silently destroy a file the applier never wrote — and Apply's deferred
	// orphan-rollback would then os.Remove it outright on any later failure,
	// turning the overwrite into deletion.
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

	body, err := os.ReadFile(srcPath) //nolint:gosec // G304: srcPath joins the overlay, a package key splitPkgAtom confines to one category/package directory, and the current version read from that directory's ebuild filenames
	if err != nil {
		return fmt.Errorf("failed to read source ebuild %s: %w", srcPath, err)
	}
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("failed to stat source ebuild %s: %w", srcPath, err)
	}

	// The check above is the fast, precise refusal; the publish itself is what
	// makes it safe. fileutil.PublishNewFile writes a synced temporary file and
	// hard-links it to dstPath, which fails if ANY entry — one created after the
	// check, or a dangling symlink the Stat could not see — already sits there,
	// and it never leaves a partial ebuild behind. The new ebuild carries the
	// source ebuild's mode rather than one the umask chose.
	if err := fileutil.PublishNewFile(dstPath, body, srcInfo.Mode().Perm()); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s (refusing to overwrite; %s-%s would be written over it)",
				ErrEbuildExists, dstPath, pkgName, oldVersion)
		}
		return fmt.Errorf("failed to publish destination ebuild %s: %w", dstPath, err)
	}

	return nil
}

// substituteCommitHash replaces the commit-hash variable assignment in an
// ebuild with newHash: EGIT_COMMIT="<sha>", GIT_COMMIT="<sha>",
// BUILD_ID="<sha>" (SHA part of SRC_URI), COMMIT="<sha>" and unquoted
// COMMIT=<sha>. COMMIT is spelled BOTH ways on purpose: a gap between the two
// patterns once failed a copied bump on a COMMIT= line that was right there,
// leaving an orphan in the published overlay.
//
// The match is deliberately narrow — a 40-hex-char SHA, one of the four names,
// start of line — so it cannot corrupt other content. The left-hand anchor
// decides which variables get their VALUE replaced: without it every
// `<ANYTHING>_COMMIT` would, including the revisions of vendored components
// (app-editors/zed's seven `local *_COMMIT`, media-libs/mesa's
// VENUS_PROTOCOL_COMMIT). Overwriting those fails silently until build time on
// a user's machine; zed shipped broken four times that way.
//
// `^[ \t]*`, not `\b`: a word boundary still matches the `_COMMIT` suffix, while
// leading whitespace keeps mesa's tab-indented GIT_COMMIT and excludes
// `local ASYNC_PROCESS_COMMIT=`. It mirrors checker.go's ebuildCommitRegex,
// which reads the same assignment with `(?m)^\s*`.
func substituteCommitHash(ebuildPath, newHash string) error {
	content, err := os.ReadFile(ebuildPath) //nolint:gosec // G304: ebuildPath is the candidate path candidateIn built from a package key splitPkgAtom confines and a version ebuild.IsValidVersion gated
	if err != nil {
		return fmt.Errorf("failed to read ebuild for hash substitution: %w", err)
	}

	reQuoted := regexp.MustCompile(`(?m)^([ \t]*(?:EGIT_COMMIT|GIT_COMMIT|BUILD_ID|COMMIT)=")[0-9a-f]{40}(")`)
	reBare := regexp.MustCompile(`(?m)^([ \t]*COMMIT=)[0-9a-f]{40}\b`)

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

	literal := literalReplacement(newHash)
	updated := reQuoted.ReplaceAllString(string(content), "${1}"+literal+"${2}")
	updated = reBare.ReplaceAllString(updated, "${1}"+literal)

	if updated == string(content) {
		return nil
	}

	if err := replaceEbuildKeepingMode(ebuildPath, updated); err != nil {
		return fmt.Errorf("failed to write ebuild after hash substitution: %w", err)
	}

	return nil
}

// substituteAuxVar replaces the quoted assignment of a free-text auxiliary
// variable in an ebuild (e.g. MY_BUILD="esr-bb23" → MY_BUILD="esr-bb24"). It is
// the sibling of substituteCommitHash but without the 40-hex-SHA lock, so it can
// carry any value captured from a regex/html upstream page. The match is bounded
// by the surrounding double quotes; the value is inserted literally and is not
// checked here — applySubstitutions, its only caller, refuses one that could
// close those quotes.
func substituteAuxVar(ebuildPath, varName, newValue string) error {
	if varName == "" {
		return fmt.Errorf("empty aux_var name for %s", ebuildPath)
	}
	content, err := os.ReadFile(ebuildPath) //nolint:gosec // G304: ebuildPath is the candidate path candidateIn built from a package key splitPkgAtom confines and a version ebuild.IsValidVersion gated
	if err != nil {
		return fmt.Errorf("failed to read ebuild for aux var substitution: %w", err)
	}

	// Anchored to the start of a line (leading indentation allowed) for the same
	// reason substituteCommitHash is: QuoteMeta pins the name but not its
	// position, so an unanchored match also fires on a longer name that merely
	// ends in it (MY_BUILD inside VENDORED_MY_BUILD) and on a commented-out
	// assignment -- and ReplaceAllString would rewrite every one of them.
	re := regexp.MustCompile(`(?m)^([ \t]*` + regexp.QuoteMeta(varName) + `=")[^"]*(")`)

	// "Nothing changed" has two very different causes, and conflating them
	// reports a missing variable that is sitting right there. Decide on presence
	// first: an ebuild that already carries the target value is correct, not
	// broken.
	//
	// Reachable whenever an upstream keeps the auxiliary value across two
	// releases -- net-misc/nxplayer shipped 10.0.59 and 10.0.60 both as build
	// _1, and the bump died claiming MY_BUILD was absent.
	if !re.Match(content) {
		return fmt.Errorf("aux var %q not found in %s", varName, ebuildPath)
	}

	updated := re.ReplaceAllString(string(content), "${1}"+literalReplacement(newValue)+"${2}")
	if updated == string(content) {
		return nil
	}

	if err := replaceEbuildKeepingMode(ebuildPath, updated); err != nil {
		return fmt.Errorf("failed to write ebuild after aux var substitution: %w", err)
	}

	return nil
}

// replaceEbuildKeepingMode replaces the ebuild at ebuildPath with content,
// keeping the mode it has. The replacement is atomic (fileutil.WriteFileAtomic):
// a crash leaves the previous ebuild under its name, never a truncated one that
// the overlay would commit and publish. The error names ebuildPath.
func replaceEbuildKeepingMode(ebuildPath, content string) error {
	info, err := os.Stat(ebuildPath)
	if err != nil {
		return fmt.Errorf("reading the mode of %s: %w", ebuildPath, err)
	}
	if err := fileutil.WriteFileAtomic(ebuildPath, []byte(content), info.Mode().Perm()); err != nil {
		return fmt.Errorf("replacing %s: %w", ebuildPath, err)
	}
	return nil
}

// checkUpstreamValues refuses a non-empty AuxValue outside auxValueRe or a
// non-empty CommitHash outside commitHashRe. The value is quoted with %q so a
// control byte or terminal escape cannot reach a log or notification raw.
func checkUpstreamValues(pkg string, update *PendingUpdate) error {
	if update.AuxValue != "" && !auxValueRe.MatchString(update.AuxValue) {
		return fmt.Errorf("%w for %s: %q", ErrInvalidAuxValue, pkg, update.AuxValue)
	}
	for _, atom := range slices.Sorted(maps.Keys(update.Requires)) {
		if v := update.Requires[atom]; !exactVersion(v) {
			return fmt.Errorf("%w for %s requiring %s: %q", ErrInvalidRequiredVersion, pkg, atom, v)
		}
	}
	if update.CommitHash != "" && !commitHashRe.MatchString(update.CommitHash) {
		return fmt.Errorf("%w for %s: %q", ErrInvalidCommitHash, pkg, update.CommitHash)
	}
	return nil
}

// literalReplacement escapes a value for use inside a regexp replacement
// template, so that ReplaceAllString inserts its bytes unchanged. The template
// around it still expands `${1}`/`${2}` (the `NAME="` prefix and the closing
// quote); only the value's own `$` is doubled. Without it an upstream value
// such as `a${1}b` would expand to the capture group instead of being written.
// substituteCommitHash and substituteAuxVar do not validate the value —
// applySubstitutions, their only caller, refuses a malformed one before either
// is reached.
func literalReplacement(value string) string {
	return strings.ReplaceAll(value, "$", "$$")
}

// runManifestWithFix runs the manifest step and, when it fails and an LLM fixer is
// configured, performs a single agentic repair-and-retry:
//
//  1. Run `pkgdev manifest`; on success, return nil (no fix needed).
//  2. When the failure belongs to the machine, not the ebuild, report that and
//     return without invoking a fixer (see refuseFixOnEnvironmentFailure).
//  3. If no fixer is wired, return the original error (legacy fail-fast).
//  4. Otherwise let the fixer edit the ebuild, then re-run `pkgdev manifest`
//     ONCE; that run — bentoo's own, not the agent's self-report — decides:
//     success records result.Fixed/FixSummary, failure returns a combined error
//     and Apply's deferred orphan-rollback removes the half-applied ebuild.
//
// One fix attempt per apply: the agent iterates internally (bounded by its
// --max-turns). Every path is scoped to cand, never a.overlayPath, so on the
// staged path an agent rewriting SRC_URI does it outside the overlay that
// commits and pushes itself.
// The returned path is the private distdir the caller's gates read and the
// caller then removes. When the fixer runs, the directory the AGENT downloaded
// into becomes that path, and the first distdir is removed when superseded.
func (a *Applier) runManifestWithFix(ctx context.Context, cand candidatePaths, pkg, version string, result *ApplyResult) (string, error) {
	distdir, firstErr := a.runManifestFor(ctx, cand, pkg, version)
	if firstErr == nil {
		return distdir, nil
	}

	// The environment gate. It sits between the failed manifest and everything
	// that would set a fix attempt up, because the cheapest fixer invocation is
	// the one that never happens.
	if envErr := a.refuseFixOnEnvironmentFailure(pkg, version, firstErr); envErr != nil {
		return distdir, envErr
	}

	if a.fixer == nil {
		return distdir, firstErr
	}

	pkgDir := cand.pkgDir
	if pkgDir == "" {
		// Malformed name: nothing the fixer can scope to; surface the original error.
		return distdir, firstErr
	}

	// Writable distdir the agent can pass to `pkgdev manifest --distdir` while it
	// self-verifies, so its checks never touch the system DISTDIR. Private, and
	// removed before this apply ends — that part is the point and does not change.
	// WHO removes it does: see the transfer below.
	//
	// The ROOT it is made under does. An empty root means os.TempDir(), which on
	// the measured host is a 31 GB tmpfs, so the agent's own verification
	// downloads landed in RAM — the same defect the manifest step avoids, on a
	// second path. fixSandboxRoot
	// asks the host for PORTAGE_TMPDIR and answers "" when it cannot, which is
	// what os.MkdirTemp already means by "use the default": a host without
	// portageq keeps exactly today's behaviour.
	fixDistdir, err := os.MkdirTemp(fixSandboxRoot(ctx), "bentoo-fix-distfiles-")
	if err != nil {
		// Can't give the agent a private distdir; don't attempt the fix.
		return distdir, fmt.Errorf("%w (manifest fix skipped: failed to create temp distdir: %v)", firstErr, err) //nolint:errorlint // secondary context; the manifest failure is the cause
	}

	// The first distdir's completed downloads move into fixDistdir before it is
	// removed below: they are this version's real bytes, and discarding them made
	// the repair depend on the upstream host a second time. Only regular files
	// move. Its symlinks into the distfiles cache are dropped, not carried: the
	// manifest fixer holds Write, and a link in its directory would let it
	// overwrite the cache entry. Both directories live under fixSandboxRoot, so
	// the move is a rename, not a copy. What is still missing among the names
	// derived from the published package is then COPIED in from the distfiles
	// cache or the host DISTDIR, for the same reason never linked.
	if cand.staged && distdir != "" {
		carried, carryErrs := distfiles.CarryOver(distdir, fixDistdir)
		for _, e := range carryErrs {
			a.logger().Warn("manifest fix: could not carry a downloaded distfile over",
				"package", pkg, "version", version, "file", e.Name, "error", e.Err)
		}
		sw := a.sweeper()
		copied, copyErrs := distfiles.CopyFromSources(fixDistdir,
			sw.stagedDistfileSources(ctx, fixDistdir), sw.stagedExpectedDistfiles(pkg, version))
		for _, e := range copyErrs {
			a.logger().Warn("manifest fix: could not copy a cached distfile",
				"package", pkg, "version", version, "file", e.Name, "source", e.Source, "error", e.Err)
		}
		a.logger().Info("manifest fix: distdir prepared",
			"package", pkg, "version", version, "carried", carried, "copied", copied)
		if carried+copied > 0 {
			a.reporter.Log("info", fmt.Sprintf("reusing %d downloaded and %d cached distfile(s) for the %s-%s repair",
				carried, copied, pkg, version))
		}
	}

	// THE TRANSFER. From this line the agent's directory is what
	// this function returns, on every path below including the failing ones, and
	// the caller's `defer removeStagedDistdir` is what takes it back — the same
	// single removal that already covers the ordinary path. It
	// used to be a `defer os.RemoveAll(fixDistdir)` here, which deleted the only
	// copy of the candidate's archive on this host before any gate had looked at
	// it: measured 2026-08-22 on media-libs/mesa, where 134 MB the repair had
	// fetched were discarded unread and the option gate then reported SKIPPED
	// against /var/cache/distfiles. The lifetime is unchanged; only the owner is.
	//
	// And the FIRST distdir is superseded right here rather than at the re-check,
	// because from this point nothing reads it: the agent fetches into fixDistdir,
	// the re-check is computed against fixDistdir, and the gates read what this
	// returns. Removing it now keeps a superseded copy of a 6 MB archive off the
	// scratch filesystem for the agent's whole run, not merely for the QA scan.
	removeStagedDistdir(a.logger(), distdir)
	distdir = fixDistdir

	a.logger().Info("manifest failed; invoking LLM fixer to repair the ebuild", "package", pkg, "version", version)
	a.reporter.TaskStage(pkg, "llm-fix")
	a.reporter.Log("info", fmt.Sprintf("manifest failed for %s-%s; invoking LLM fixer to repair the ebuild", pkg, version))

	fixRes, fixErr := a.fixer.FixManifest(ctx, fixer.ManifestFixRequest{
		Package:       pkg,
		Version:       version,
		PkgDir:        pkgDir,
		EbuildPath:    cand.ebuildPath,
		ManifestError: firstErr.Error(),
		DistDir:       fixDistdir,
		UpstreamURLs:  a.configs[pkg].UpstreamURLs(),
	})
	if fixErr != nil {
		return distdir, fmt.Errorf("%w (LLM fix attempt failed: %v)", firstErr, fixErr) //nolint:errorlint // secondary context; the manifest failure is the cause
	}

	// Authoritative re-check: trust bentoo's own manifest run, not the agent's
	// self-report.
	//
	// It runs IN the directory the agent downloaded into, so what it verifies and
	// what the gates below read are the same bytes. A second, empty distdir would
	// verify nothing when the fix SUCCEEDED: the agent leaves a COMPLETE Manifest,
	// pkgdev downloads nothing, and the gates fall back to a shared DISTDIR that
	// may never have held the release.
	//
	// KNOWN LIMIT, stated rather than solved. `--force` (added by
	// runStagedManifestIn for a supplied directory) re-digests the bytes THE AGENT
	// BROUGHT: the surviving Manifest is bentoo's own, but the content is not
	// independently checked against upstream. Re-downloading would be, and was
	// rejected on cost: ~133 MB per media-libs/mesa bump, and mesa bumps daily.
	//
	// The path returned is whatever the re-check ran against, on its failure paths
	// too — the caller must be able to take the directory back however this ends.
	a.reporter.TaskStage(pkg, "re-check")
	recheckDistdir, secondErr := a.runManifestForIn(ctx, distdir, cand, pkg, version)
	distdir = recheckDistdir
	if secondErr != nil {
		return distdir, fmt.Errorf("%w (LLM fix applied but manifest still failed: %v)%s", firstErr, secondErr, llm.RefusedToolsNote(fixRes.DeniedTools)) //nolint:errorlint // secondary context; the manifest failure is the cause
	}

	result.Fixed = true
	result.FixSummary = fixRes.Summary
	// Name the model that made the edit, and SAY SO when it was an alias.
	// FormatModelUsed renders "model alias \"opus\"" rather
	// than a bare "opus", because the same alias resolves to a different model
	// over time: an audit of a bad edit months from now must not read the bare
	// word as a pinned identity. One string feeds both sinks so the operator's
	// log and the TUI report can never drift apart.
	fixLine := fmt.Sprintf("LLM fixer repaired %s-%s using %s: %s",
		pkg, version, fixer.FormatModelUsed(fixRes.Model), fixRes.Summary)
	a.logger().Info("LLM fixer repaired the ebuild",
		"package", pkg, "version", version, "model", fixer.FormatModelUsed(fixRes.Model), "summary", fixRes.Summary)
	a.reporter.Log("info", fixLine)

	// Advisory QA gate: the manifest re-run proves the distfile fetches and
	// digests, but not that the agent's edit is QA-clean. Run pkgcheck (when
	// available) on the package and attach any findings for human review. This is
	// best-effort and never flips Success — a fixed-and-fetchable ebuild is still
	// applied; the QA notes just travel with the result.
	if qa := a.runQACheck(ctx, pkgDir, pkg); qa != "" {
		result.QASummary = qa
		a.logger().Warn("qa: pkgcheck reported findings after the LLM fix", "package", pkg, "version", version, "findings", qa)
	}
	return distdir, nil
}

// refuseFixOnEnvironmentFailure decides whether a failed manifest step is one
// the LLM fixer must never see.
//
// It returns a non-nil error ONLY for an environment verdict, so the apply
// fails before a fix attempt exists; the error says the environment, NOT the
// ebuild, caused it, with the classifier's reason and pkgdev's output. Any
// other verdict returns nil and the path runs on to the authoritative re-run.
//
// A fixer handed a machine failure can only rewrite SRC_URI, so it spends
// minutes and quota concluding a correct ebuild is wrong, in a repository that
// commits and pushes itself; measured cases later applied with nothing changed.
// It is the same rule promptRegistryFixes applies (only ErrFetchFailed is
// offered a repair), keyed on a classification instead of one sentinel.
//
// It runs before the `a.fixer == nil` return because the verdict is a fact
// about the failure, not the configuration: without it a machine with no fixer
// would hand-edit an ebuild that was never broken. Failures nothing can
// classify (an invalid atom) fall through as "repairable": a wrong
// classification must cost a wasted fixer invocation, never a lost repair.
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
	a.logger().Warn("manifest failure is the environment's; the LLM fixer was not invoked",
		"package", pkg, "version", version, "err", envErr)
	a.reporter.Log("warn", envErr.Error())
	return envErr
}

// environmentVerdict returns an ErrManifestEnvironment-wrapped verdict when the
// failure is the machine's rather than the ebuild's, and nil when the ebuild
// still might be at fault. Three classes are recognised, none of them uncertain:
//
//   - Before pkgdev ran: a distdir that could not be prepared, or a distfile
//     another writer still held after the wait — nothing had read the ebuild.
//   - A command that could not start: fs.ErrNotExist in a chain with no
//     *exec.ExitError (an ExitError means the command RAN, which is evidence
//     about the ebuild). Keyed on that property, not on which site raised it.
//   - After pkgdev ran: ClassifyManifestFailure over the state recovered from
//     *manifestRunError, never re-derived — the distdir precedence could now
//     answer differently.
//
// Not covered: a Quarantine that could not stat or rename, and acquireLock's
// non-timeout errors (internal/common/distfiles); they carry no sentinel and
// still reach the fixer. The fix is to mark the PHASE (preparing the shared
// directory) rather than enumerate causes — a contract change of its own, since
// every enumerated clause is one somebody must remember to add.
func environmentVerdict(firstErr error) error {
	if errors.Is(firstErr, distfiles.ErrDistdirNotWritable) ||
		errors.Is(firstErr, distfiles.ErrDistfileLocked) {
		// Wrapped, not replaced: %w keeps both the sentinel above and everything
		// under it reachable, so the operator still reads which directory or
		// which distfile, and which process held it. The two sentinels stay
		// apart because they are different news: a held distfile is transient,
		// an unwritable distdir will still be unwritable next run.
		return fmt.Errorf("%w: %w", ErrManifestEnvironment, firstErr)
	}

	// The spawned command's working directory does not exist: a
	// failure to start, never a failure of what ran. The guard is structural —
	// any *exec.ExitError in the chain means the command DID run, and then this
	// class must not fire whatever ENOENT its wrapped output mentions. It sits
	// before the *manifestRunError gate below because both production shapes
	// carry the start failure reachably: the staged path wraps cmd.Run's error
	// plainly (sweep_staged.go), the published path wraps it inside
	// *manifestRunError, whose Unwrap keeps the chain open.
	var exitErr *exec.ExitError
	if errors.Is(firstErr, fs.ErrNotExist) && !errors.As(firstErr, &exitErr) {
		return fmt.Errorf("%w: the spawned command's working directory does not exist, so the command never started and said nothing about the ebuild: %w",
			ErrManifestEnvironment, firstErr)
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
func (a *Applier) runQACheck(ctx context.Context, pkgDir, pkg string) string {
	if _, err := lookPath("pkgcheck"); err != nil {
		a.logger().Debug("qa: pkgcheck not on PATH; skipping post-fix QA", "package", pkg)
		return ""
	}

	opCtx, cancel := context.WithTimeout(ctx, qaCheckTimeout)
	defer cancel()

	// Scan the single package from its directory so pkgcheck resolves the overlay
	// repo from cwd. Scope to repo-level checks for the one package via its atom.
	cmd := a.execCommand(opCtx, "pkgcheck", "scan", pkg)
	cmd.Dir = pkgDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run() // non-zero exit == findings; ignore the code, keep stdout.

	if diag := strings.TrimSpace(stderr.String()); diag != "" {
		a.logger().Debug("qa: pkgcheck stderr (not surfaced)", "package", pkg, "stderr", diag)
	}
	return strings.TrimSpace(stdout.String())
}

// runManifest regenerates the Manifest file with pkgdev, delegating to the
// sweeper that owns the step. It stays on Applier because
// runManifestWithFix and the apply path both call it, and because keeping the
// name here left every existing caller and test untouched.
func (a *Applier) runManifest(ctx context.Context, pkg, version string) error {
	return a.sweeper().runManifest(ctx, pkg, version)
}

// runManifestFor regenerates the Manifest of whichever tree the candidate is in.
//
// The two are not the same call with a different directory: the staged one drops
// LockFetch, Quarantine and RecordFetchScope and runs against a private distdir,
// because those three defend a directory the whole machine shares and pointing
// them at a staged tree would have a validation run rearrange the host's DISTDIR.
// sweep_staged.go carries the full argument.
//
// The returned path is the private distdir the STAGED step fetched into, and is
// "" on the published path — which has no private directory, having written into
// the shared one all along. A caller that receives a non-empty path owns it and
// must remove it; removeStagedDistdir is that removal.
func (a *Applier) runManifestFor(ctx context.Context, cand candidatePaths, pkg, version string) (string, error) {
	// An empty supplied distdir means "whatever this path would have created for
	// itself", which is the entire difference between the two entry points.
	return a.runManifestForIn(ctx, "", cand, pkg, version)
}

// runManifestForIn is runManifestFor against a distdir the caller already holds.
//
// The one caller that supplies one is the authoritative re-check after an LLM
// fix, which hands over the directory the agent downloaded into so that the
// Manifest that survives is computed from the bytes the gates will read.
// runStagedManifestIn carries what changes with a supplied directory: the
// seeding step is skipped and `--force` joins the pkgdev argv, because pkgdev
// does not re-manifest a package whose Manifest is already complete.
//
// The published branch only carries the supplied path back out, so the fix
// distdir reaches the caller's gates and removal on this path too. Its `pkgdev
// manifest` writes into the shared directory as it always has: giving it a
// private distdir would move a pre-flight and three shared-directory
// protections the environment gate is keyed on.
//
// Both branches therefore keep runManifestFor's contract: what comes back is the
// directory the caller owns and must remove, and "" only where there is none.
func (a *Applier) runManifestForIn(ctx context.Context, suppliedDistdir string, cand candidatePaths, pkg, version string) (string, error) {
	if cand.staged {
		return a.sweeper().runStagedManifestIn(ctx, suppliedDistdir, cand.pkgDir, pkg, version)
	}
	return suppliedDistdir, a.runManifest(ctx, pkg, version)
}

// removeStagedDistdir takes back what runManifestFor's staged branch created.
//
// It is a function rather than an inline os.RemoveAll so that every site which
// must not forget it is greppable, and so the empty case — the published path,
// which never had one — is answered in one place instead of at each call.
//
// The error is deliberately swallowed and logged to log (nil discards) rather
// than returned. A distdir
// that could not be removed is a leaked temporary directory; it is not a reason
// to fail a bump that passed its gates, and turning it into one would make a
// full disk reject work that was already proved.
func removeStagedDistdir(log *slog.Logger, distdir string) {
	if distdir == "" {
		return
	}
	if err := os.RemoveAll(distdir); err != nil {
		logging.OrDiscard(log).Debug("could not remove the staged distdir", "distdir", distdir, "err", err)
	}
}

// runCompile runs a compile test with elevated privileges.
// It prompts for user confirmation before executing.
// Returns the log path if compilation fails.
//
// Before anything runs, the isolation probe measures whether this process can
// create a network namespace, and the answer goes on the result so a pass
// states its own fidelity. With --require-isolation and no namespace the
// compile is skipped and the result says why — running anyway would produce
// the meaningless green the operator asked to avoid. The prompt, the sudo/doas
// requirement and the runAttached seam are otherwise unchanged.
//
// The ebuild and repository are TOLD to it (cand), not recomputed from
// a.overlayPath: on the staged path that is the staged tree, so the build never
// reads an unvalidated candidate out of a tree that auto-commits.
//
// A failure attributable to the ebuild, with a build fixer wired, goes to the
// REPAIR path: an agent edits the staged ebuild and this same gate runs again,
// the RE-RUN deciding. With no fixer wired a failure ends the gate as before.
func (a *Applier) runCompile(ctx context.Context, cand candidatePaths, pkg, version string, result *ApplyResult) (string, error) {
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

	first := a.compileOnce(ctx, cand, pkg, version, privTool)
	// Recorded as soon as a child has actually run, and on both outcomes: the
	// gate below has to be able to say which directory this build read and
	// whether the privilege tool could be made to honour it. The
	// paths above return before any build, and leave both facts unset — there is
	// nothing to state about a compile that did not happen.
	recordCompileDistdir(result, first)
	if first.err == nil {
		return "", nil
	}

	// A compile its context stopped is not a failure to repair: it
	// says nothing about the ebuild, and the fixer is an LLM invocation the
	// operator has just asked this run to stop. compileOnce has already kept the
	// partial transcript, so returning here throws no evidence away.
	if ctx.Err() != nil {
		return first.logPath, first.err
	}

	return a.repairBuildAndRerun(ctx, cand, pkg, version, privTool, first, result)
}

// compileGatePhase is the `ebuild` phase the compile gate runs.
//
// It is spelled as validate.GateCompile rather than as a bare "compile" to state
// an invariant the repair rests on: the gate NAMED to the fixer and the phase the
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
	// logPath is the retained compile log, empty unless the attempt failed or
	// was interrupted.
	logPath string
	// err is the failure, already wrapped in ErrCompileFailed, or nil. A build
	// its context stopped is not a failure: err then wraps ctx.Err() and never
	// ErrCompileFailed.
	err error
	// resolvedDistdir is the directory this attempt resolved for the build's
	// archives, and enforcedDistdir the one the privilege tool actually carried
	// to the child: equal to it under sudo, empty under a tool with no argument
	// form for an environment assignment (see privilegedDistdirArgs).
	//
	// Both are carried out of the attempt rather than re-derived by the caller
	// so that the directory REPORTED and the directory the child was handed are
	// the same resolution, not two runs of the same resolver a moment apart —
	// staticGateDistdir prefers the private fetch directory only WHILE it holds
	// something, so a second call is a second reading of the filesystem.
	//
	// They stay empty on the depth-driven build gates (applier_gates.go), whose
	// child is unprivileged and gets its DISTDIR assigned inside the validate
	// package; nothing on that path reads these.
	resolvedDistdir string
	enforcedDistdir string
}

// privilegedDistdirArgs decides HOW — and whether — a resolved distdir crosses
// the privilege boundary, returning the argument that carries it and the
// directory the child will therefore really read.
//
// The mechanism was measured, not assumed (2026-08-20, sudo-rs 0.2.14): with
// env_reset in force an exported DISTDIR reaches `ebuild` not at all, while
// sudo's own argument form, `sudo DISTDIR=<dir> ebuild …`, crosses. It beats
// --preserve-env because the value is a LITERAL ARGUMENT this run computed, so
// nothing crosses from the operator's shell — the same assigned-versus-
// allow-listed distinction validate/build.go draws.
//
// The tool is a BRANCH for correctness: `doas` has no VAR=value form and would
// try to execute a program named "DISTDIR=…", failing on every doas host (which
// detectPrivilegeTool PREFERS). doas was not measured, so nothing is invented:
// it runs as before, and the gate says the distdir was not enforced.
//
// EMPTY RESOLVES TO NOTHING, mirroring validate/build.go: no assignment and no
// invented default, so Portage answers from its own configuration.
func privilegedDistdirArgs(privTool, distdir string) (assignment []string, enforced string) {
	if distdir == "" {
		return nil, ""
	}
	switch privTool {
	case "sudo":
		return []string{"DISTDIR=" + distdir}, distdir
	default:
		return nil, ""
	}
}

// recordCompileDistdir carries one compile attempt's distdir facts onto the
// result, so the gate that reports this run can tell apart the three states:
// nothing resolved, resolved and enforced, resolved and uncarriable.
//
// It is called for every attempt that actually SPAWNED a child, including the
// authoritative re-run after a repair, because the last build that ran is the
// one the verdict is about. A compile that never happened leaves both fields at
// their zero value, which reads as "this run resolved nothing" — true, since it
// resolved nothing it could have used.
func recordCompileDistdir(result *ApplyResult, attempt buildAttempt) {
	result.CompileDistdir = attempt.resolvedDistdir
	result.CompileDistdirEnforced = attempt.enforcedDistdir != ""
}

// compileOnce spawns the build child EXACTLY ONCE and, on failure, retains its
// log.
//
// It is the single place this gate's argv is spelled, so "the SAME gate is
// re-run" is a property of the code rather than a promise: the authoritative
// re-run after a repair calls this same function with the same candidate, and
// cannot drift to a shallower phase where a prepare would clear a configure
// failure.
//
// The privileged child needs the real TTY for the sudo/doas password prompt, so
// its streams cannot go through a StreamCapture pipe; the default runAttached is
// exactly cmd.CombinedOutput, keeping the retained log byte-identical.
//
// The distdir is resolved by staticGateDistdir, the SAME resolver the option
// gate and the depth-driven build gates use, so the build reads the directory
// the gate vetted rather than whatever the host's Portage names. How it crosses
// the privilege boundary is privilegedDistdirArgs' decision.
func (a *Applier) compileOnce(ctx context.Context, cand candidatePaths, pkg, version, privTool string) buildAttempt {
	distdir := a.staticGateDistdir(cand)
	assignment, enforced := privilegedDistdirArgs(privTool, distdir)

	// The `portage` group is opened HERE and not at the moment either directory
	// was created, because this is the only gate that escalates — and escalating
	// is what makes Portage drop to uid `portage` to read them (portage_access.go
	// carries the measurement). Doing it now also catches whatever the manifest
	// step and the build fixer wrote in between: a repair that lands a fresh 0600
	// ebuild moments before the re-run would otherwise reintroduce the very
	// failure this fixes, on the second attempt only.
	//
	// Only what this run OWNS is touched: the staged tree, the directories
	// leading down to it, and the private distdir this run's manifest step
	// filled. A published overlay is not ours to re-permission, and neither is
	// the host's own DISTDIR — which is already portage's, being where Portage
	// keeps its archives.
	if err := a.grantCompileAccess(cand); err != nil {
		// No transcript, and that is the honest shape of it: no child ran, so
		// there is nothing a build said. The attribution gate reads an empty
		// transcript as "src_prepare never started", which is exactly right —
		// this is the host, and no fixer will be invoked to edit an ebuild that
		// was never the problem.
		return buildAttempt{
			err:             fmt.Errorf("%w: %w", ErrCompileFailed, err),
			resolvedDistdir: distdir,
			enforcedDistdir: enforced,
		}
	}

	// sudo [DISTDIR=<dir>] ebuild <path> clean compile, bound to the applier's
	// parent context so a SIGINT or deadline stops the spawned process (see
	// procgroup.Foreground below for how). The assignment PRECEDES the command,
	// because sudo reads the first non-assignment argument as the program to run.
	//
	// Built into a slice of its OWN rather than appended onto the one
	// privilegedDistdirArgs returned: appending to a caller's slice writes into
	// that slice's backing array whenever it has spare capacity, so the argv of
	// this run would depend on how the assignment was allocated somewhere else.
	// It costs one allocation and removes a class of bug entirely.
	args := make([]string, 0, len(assignment)+4)
	args = append(args, assignment...)
	args = append(args, "ebuild", cand.ebuildPath, "clean", compileGatePhase)
	cmd := a.execCommand(ctx, privTool, args...)
	cmd.Dir = cand.repoRoot
	// Foreground and not Group: sudo and doas ask for the
	// password on the terminal, and a child moved into a process group of its
	// own is a BACKGROUND group there, stopped by SIGTTIN at its first read — a
	// compile hung on a prompt nobody can answer. In the caller's group a cancel
	// reaches the privilege tool as SIGTERM, which sudo relays to the root
	// `ebuild`; os/exec's SIGKILL comes procgroup.GracePeriod later, and only
	// then, because SIGKILL is the one signal sudo cannot relay. doas relays
	// nothing: it execs the root `ebuild` in place, so the child IS root and
	// both signals are refused (EPERM, kept by keepStopRefusal); Wait then
	// returns only when that build ends by itself. A terminal Ctrl+C still
	// reaches it, since tty signals skip the permission check.
	procgroup.Foreground(cmd)
	stopRefused := keepStopRefusal(cmd)

	// cmd.Env is deliberately left nil here, which is a MEASURED
	// decision and not an omission. validate/build.go installs an allow-list on
	// its child because that child inherits this process's environment; this one
	// does not — the measurement showed sudo's env_reset discarding the inherited
	// environment before `ebuild` ever sees it. An allow-list on the sudo PARENT
	// would therefore filter variables the privilege tool throws away a moment
	// later, buying nothing, while risking the password prompt itself: TERM, and
	// DISPLAY/XAUTHORITY on a host with a graphical askpass, are in the preserved
	// set for a reason. The surface stays exactly today's, and the one value this
	// run needed the child to have travels as an argument instead.

	output, err := a.runAttached(cmd)
	// Foreground's WaitDelay also runs after a NORMAL exit: a compile that exited
	// 0 while a helper it left behind still held the output pipe comes back as
	// exec.ErrWaitDelay, and that is a success.
	err = procgroup.Result(cmd, err)
	attempt := buildAttempt{transcript: string(output), resolvedDistdir: distdir, enforcedDistdir: enforced}
	if err != nil {
		attempt.logPath = a.saveCompileLog(pkg, version, output)
		// Checked on the context and not on err: a compile stopped by
		// SIGTERM reports `signal: terminated`, which wraps nothing. The log above
		// is written first and on purpose — an interrupted compile's partial
		// transcript is evidence too.
		if ctxErr := ctx.Err(); ctxErr != nil {
			pid, refused := stopRefused()
			attempt.err = interruptedCompileError(pkg, version, privTool, ctxErr, pid, refused)
			if refused == nil && killedAfterGrace(cmd) {
				// The SIGTERM was delivered but not obeyed within the grace period,
				// so os/exec killed the tool; a SIGKILL is the one signal sudo
				// cannot relay, so the root build is not known to have stopped.
				attempt.err = fmt.Errorf("%w; %s (process %d) was still running %s after the stop and had to be killed, which it cannot pass on, so the privileged build may still be running",
					attempt.err, privTool, cmd.Process.Pid, procgroup.GracePeriod)
			}
			return attempt
		}
		attempt.err = fmt.Errorf("%w: %w", ErrCompileFailed, err)
	}
	return attempt
}

// killedAfterGrace reports whether the finished cmd was ended by SIGKILL, which
// for a Foreground child whose context is done means os/exec's kill after
// procgroup.GracePeriod: the stop was delivered and not obeyed in time.
func killedAfterGrace(cmd *exec.Cmd) bool {
	if cmd.ProcessState == nil {
		return false
	}
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

// interruptedCompileError is what a privileged compile its context stopped
// returns: it wraps ctxErr, says "interrupted", and wraps no
// ErrCompileFailed, because an interrupt is no verdict on the ebuild.
//
// refused is the stop the privilege tool could not be sent — EPERM, from a tool
// that now runs as root — and pid the process it was refused for (see
// keepStopRefusal). The build is then not known to have stopped, and the error
// says so and names the process, so the operator knows what to look for.
func interruptedCompileError(pkg, version, privTool string, ctxErr error, pid int, refused error) error {
	interrupted := fmt.Errorf("the compile of %s-%s was interrupted, so it says nothing about this ebuild: %w", pkg, version, ctxErr)
	if refused == nil {
		return interrupted
	}
	return fmt.Errorf("%w; %s (process %d) could not be asked to stop, so the privileged build may still be running: %w",
		interrupted, privTool, pid, refused)
}

// keepStopRefusal wraps cmd.Cancel, as procgroup.Foreground configured it, so
// that a stop the child could not be sent is kept. The returned function reads
// it once Wait (or Run) has returned: the pid the stop was refused for and the
// refusal, or a nil error when every stop was delivered.
//
// Wait's error cannot carry it: os/exec surfaces Cancel's error only when the
// child then exits 0, a refused Process.Kill after WaitDelay REPLACES it, and a
// child nothing can signal exits with whatever status it chose. The one moment
// the refusal surely exists is when Cancel returns it, so it is kept there.
//
// os/exec calls Cancel before it hands the context's outcome to Wait, so a
// Cancel that ran has returned before Wait does; the mutex covers a runner that
// starts the command and reads the refusal without waiting for it.
//
// A child that had already exited (os.ErrProcessDone) is not a refusal: there
// was nothing left to stop. A command with no Cancel is left without one,
// because os/exec refuses to start a command that has a Cancel and was not
// created by exec.CommandContext.
func keepStopRefusal(cmd *exec.Cmd) func() (pid int, refused error) {
	var (
		mu      sync.Mutex
		stopPID int
		stopErr error
	)
	read := func() (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return stopPID, stopErr
	}
	cancel := cmd.Cancel
	if cancel == nil {
		return read
	}
	cmd.Cancel = func() error {
		err := cancel()
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			mu.Lock()
			// Process is non-nil: os/exec calls Cancel only after a successful Start.
			stopPID, stopErr = cmd.Process.Pid, err
			mu.Unlock()
		}
		return err
	}
	return read
}

// repairBuildAndRerun is what happens after the build gate has failed once: the
// failure is attributed, and only if it is the EBUILD's does an agent get to see
// it — after which this gate runs again and that re-run is the verdict.
//
// The attribution runs on FREE evidence first — the transcript already held —
// and its verdict is reported whether or not a fixer is wired, for the reason
// refuseFixOnEnvironmentFailure gives: it is a fact about the failure, not the
// configuration. The two rungs that spend something (a pretend `emerge -p`
// resolve, a probe write into PORTAGE_TMPDIR) are asked only once a fixer is
// about to run, since they exist to be cheaper than the agent they prevent.
// The rung order never changes; only which rungs have evidence does. So a free
// verdict PRE-EMPTS a paid one — the saving, not a bug: a build that died in
// unpack and lacked dependencies is reported as the phase; both are the
// machine's, and the action (no fixer) is the same.
//
// FixBuild enforces the attempt bound — a bound only the caller remembers is
// not one — and this gate makes one attempt per apply, like runManifestWithFix:
// the agent iterates under its own --max-turns, and each extra external attempt
// costs a FULL rebuild.
func (a *Applier) repairBuildAndRerun(ctx context.Context, cand candidatePaths, pkg, version, privTool string, first buildAttempt, result *ApplyResult) (string, error) {
	// The free rungs. Reported to every operator, LLM or not.
	if machineErr := a.refuseBuildFixOnMachineFault(pkg, version, first, buildFaultEvidence{transcript: first.transcript}); machineErr != nil {
		return first.logPath, machineErr
	}

	if a.buildFixer == nil {
		return first.logPath, first.err
	}

	// The agent is only ever pointed at a STAGED tree. On the pre-staging
	// path the candidate lives in the published overlay — the repository that
	// auto-commits and pushes — and handing an agent Edit access there is the exact
	// thing the staging boundary exists to prevent. FixBuild would refuse
	// it too (ErrBuildFixScope), but refusing before anything is constructed keeps
	// the boundary visible at the call site rather than only inside the callee.
	if !cand.staged {
		return first.logPath, first.err
	}

	// The paid rungs, now that the alternative is a full agent invocation.
	paid := buildFaultEvidence{
		transcript:  first.transcript,
		deps:        a.buildDependencyAnswer(ctx, cand, pkg, version),
		buildTmpdir: fixSandboxRoot(ctx),
	}
	if machineErr := a.refuseBuildFixOnMachineFault(pkg, version, first, paid); machineErr != nil {
		return first.logPath, machineErr
	}

	fixLine := fmt.Sprintf("the %s gate failed for %s-%s; invoking the LLM build fixer to repair the staged ebuild", compileGatePhase, pkg, version)
	a.logger().Info("gate failed; invoking the LLM build fixer to repair the staged ebuild",
		"gate", compileGatePhase, "package", pkg, "version", version)
	a.reporter.TaskStage(pkg, "llm-build-fix")
	a.reporter.Log("info", fixLine)

	fixRes, fixErr := a.buildFixer.FixBuild(ctx, fixer.BuildFixRequest{
		Package:    pkg,
		Version:    version,
		Gate:       compileGatePhase,
		StagedDir:  cand.repoRoot,
		EbuildPath: cand.ebuildPath,
		BuildLog:   first.transcript,
		// One attempt per apply; FixBuild owns the bound and this states
		// which try it is rather than counting tries here.
		Attempt: 1,
	})
	switch {
	case errors.Is(fixErr, fixer.ErrBuildFixAttemptsExhausted):
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

	// The authoritative re-run: bentoo's own build of the same phase, never
	// the agent's account of what it did.
	a.reporter.TaskStage(pkg, "re-check")
	second := a.compileOnce(ctx, cand, pkg, version, privTool)
	// The re-run is the verdict, so its distdir facts are the ones the
	// gate must report: this build is the one the PASS would be about.
	recordCompileDistdir(result, second)
	// The interrupt rule holds for the re-run as well: a re-run its context stopped is no
	// verdict on the edit, so it is returned as the interrupt it is — never as the
	// first failure "still" standing, which would wrap ErrCompileFailed.
	if second.err != nil && ctx.Err() != nil {
		return second.logPath, second.err
	}
	if second.err != nil {
		return second.logPath, fmt.Errorf("%w (the build fixer edited the staged ebuild and the %s gate still failed on the re-run: %v)",
			first.err, compileGatePhase, second.err) //nolint:errorlint // secondary error is context; wrapping it would let errors.Is match it
	}

	result.Fixed = true
	result.FixSummary = summary
	// Name the model that made the edit, and SAY SO when it was an alias —
	// the same one string into both sinks that the manifest fix
	// path uses, so the operator's log and the TUI report cannot drift apart.
	repaired := fmt.Sprintf("LLM build fixer repaired %s-%s using %s: %s",
		pkg, version, fixer.FormatModelUsed(fixRes.Model), summary)
	a.logger().Info("LLM build fixer repaired the staged ebuild",
		"package", pkg, "version", version, "model", fixer.FormatModelUsed(fixRes.Model), "summary", summary)
	a.reporter.Log("info", repaired)
	return "", nil
}

// refuseBuildFixOnMachineFault decides whether a failed build is one the build
// fixer must never see, and reports it when it is.
//
// It returns a non-nil error ONLY for a machine verdict: the apply fails with it,
// and the caller returns before anything constructs a fix attempt. For every other
// verdict it returns nil, meaning "carry on" — down to the fixer and, past it, to
// the re-run that decides.
//
// It is refuseFixOnEnvironmentFailure's counterpart for the build gate, and it is
// deliberately a separate function rather than a second caller of it: that one
// classifies through environmentVerdict, which a build failure falls straight
// through (see build_failure.go for why that would silently reinstate the
// manifest path's old defect on this path).
func (a *Applier) refuseBuildFixOnMachineFault(pkg, version string, first buildAttempt, ev buildFaultEvidence) error {
	verdict := buildFaultVerdict(ev)
	if verdict == nil {
		return nil
	}

	// The verdict says the machine is at fault; this asks the same
	// transcript WHAT the machine was missing and writes it against the package,
	// so the next run has something to decline on instead of rebuilding into the
	// identical failure. It cannot fail this call: the apply is already failing
	// for the build's own reason, and a transcript that names no path records
	// nothing — the package is retried, exactly as it is today.
	if errors.Is(verdict, ErrBuildEnvironment) {
		a.recordUnmetPrecondition(pkg, ev.transcript)
	}

	// Reported here, not returned quietly: the apply's own error reaches the
	// summary at the end of a batch, while this line lands next to the package it
	// belongs to in a run that keeps going.
	machineErr := fmt.Errorf("%s-%s: %w (%w; the build fixer was not invoked: the only repair available to it is to edit the ebuild, and the ebuild is not what failed)",
		pkg, version, first.err, verdict)
	a.logger().Warn("build failure is the machine's; the build fixer was not invoked",
		"package", pkg, "version", version, "err", machineErr)
	a.reporter.Log("warn", machineErr.Error())
	return machineErr
}

// buildDependencyAnswer asks Portage whether this host could build the staged
// candidate at all, flattened into the shape the attribution gate reads.
//
// An UNDETERMINED answer — no Portage, a resolve that failed, a staged tree that
// is not there — becomes the zero value, which the classifier reads as "this rung
// has nothing to say" and not as "a dependency is missing". That is the
// "uncertain means repairable" bargain applied here: a question this host could not answer must cost at most a wasted
// fixer invocation, never a repair that was available.
//
// Only the exec seam is injected. LookPath is deliberately left at the validate
// package's own default, so a host with no `emerge` reaches the undetermined
// branch through the real absence rather than through a substitute.
func (a *Applier) buildDependencyAnswer(ctx context.Context, cand candidatePaths, pkg, version string) buildDependencyAnswer {
	ok, missing, err := validate.DependenciesSatisfied(ctx, cand.repoRoot, pkg, version, validate.BuildDeps{
		ExecCommand: a.execCommand,
	})
	if err != nil {
		a.logger().Debug("build fix gate: could not determine whether the build dependencies are satisfied",
			"package", pkg, "version", version, "err", err)
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
	fmt.Printf("%s [y/N]: ", prompt) //nolint:forbidigo // interactive y/N prompt, paired with the stdin read below
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
// retained tree of every bump that was not promoted and the record beside
// it. A caller that has to tell an operator where to look — or that wants
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
	category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
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
			return fmt.Errorf("%w: %s", ebuilds.ErrEbuildNotFound, srcEbuild)
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
	in, err := os.Open(src) //nolint:gosec // G304: src is the ::gentoo ebuild/metadata.xml named by a confined package key and an IsValidVersion-gated version, or an entry walked under that ::gentoo package's files/
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer in.Close() //nolint:errcheck // read-only handle: a failed close cannot lose data

	out, err := os.Create(dst) //nolint:gosec // G304: dst is the overlay package directory a confined package key names, plus the same file name or walked relative path as src
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", dst, err)
	}
	defer out.Close() //nolint:errcheck // out.Sync below is checked, so the data is on disk before this close runs; on an earlier error the copy is already reported as failed

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

// MarkReenabled tells the Applier that pkg's entry was re-enabled in
// packages.toml after the Applier loaded it, so its stale enabled = false no
// longer refuses the bump. hold = true still does.
func (a *Applier) MarkReenabled(pkg string) {
	a.reenabledMu.Lock()
	defer a.reenabledMu.Unlock()
	if a.reenabled == nil {
		a.reenabled = make(map[string]bool)
	}
	a.reenabled[pkg] = true
}

// refusal is refusedBy with the run's re-enables applied.
func (a *Applier) refusal(pkg string) string {
	reason := registry.RefusedBy(a.configs, pkg)
	if reason == "enabled = false" {
		a.reenabledMu.Lock()
		defer a.reenabledMu.Unlock()
		if a.reenabled[pkg] {
			return ""
		}
	}
	return reason
}
