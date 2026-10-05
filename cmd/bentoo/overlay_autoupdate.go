package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/filelock"
	"github.com/obentoo/bentoolkit/internal/common/fileutil"
	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/obentoo/bentoolkit/internal/common/tui"
	"github.com/spf13/cobra"
)

// autoupdateOptions holds the values `overlay autoupdate` binds its flags to.
// One is allocated per newAutoupdateCmd call, so two command trees never share
// one: pflag writes a flag's default through its pointer when the flag is bound,
// so a shared target would let building a second tree reset what the first
// parsed (S060-R5.1).
//
// --ui, --all and --export are not here: they are root persistent flags every
// report-producing command honours, and stay package variables. --depth is not
// here either: it is read off the command.
type autoupdateOptions struct {
	// check triggers version checking
	check bool
	// list triggers listing pending updates
	list bool
	// apply specifies package to apply update
	apply string
	// force ignores cache when checking
	force bool
	// compile runs compile test after apply
	compile bool
	// requireIsolation is --require-isolation. Story 031 kept the
	// setting out of the registry because a registry key is expensive to move
	// once written (story 031 Constraints); story 033 added the block that owns
	// it instead — autoupdate.validate.require_isolation in config.yaml, where
	// an unknown key is a warning on stderr rather than a silently disabled
	// record. That key defaults to false, so this flag stays the way the
	// stricter behaviour is asked for on a run.
	requireIsolation bool
	// clean removes the old ebuild after a successful apply, keeping
	// only the newly created version
	clean bool
	// concurrency bounds parallel version checks and the --apply all
	// worker pool (range [1,100])
	concurrency int
	// timeout overrides the per-request HTTP timeout in seconds
	// (0 = use config autoupdate.http_timeout, default 30)
	timeout int
	// only restricts --check to a package type ("bin" or "source")
	only string
	// reviveList reports disabled (orphaned) entries whose upstream
	// version is newer than ::gentoo — a passive scan, no mutation
	reviveList bool
	// revive performs the full revive for a single "category/pkg" or
	// "all": seed from ::gentoo, re-enable, then bump to the upstream version
	revive string
	// revivable, with --check, also reports revivable orphans (disabled
	// entries absent from the overlay whose upstream is newer than ::gentoo) in
	// the same pass — read-only, no mutation
	revivable bool
	// noTUI opts out of the live TUI during --apply, streaming plain
	// rate-limited output instead. It is one of the gate's opt-outs (alongside
	// NO_COLOR and BENTOO_NO_TUI); see tuiEnabledForApply (R2.1, R2.2).
	noTUI bool
	// lint checks the overlay's packages.toml against the record model
	// (every record closed by "# END", documented by a trailing comments field,
	// with no floating comments) and validates each record's fields. Read-only.
	lint bool
	// fix, with --lint, repairs in place the violations the linter
	// reports mechanically: the retired `binary` key, a redundant
	// `enabled = true`, and the canonical field order (R7).
	//
	// It is a MODIFIER of --lint, never a mode of its own — what it repairs is
	// defined by what the linter reports — and it writes only behind the same
	// three gates story 021 built for the version pins (see confirmLintRepair).
	fix bool
	// markAutoDisabled is the ONE-SHOT migration of story 043 R1.5: it
	// stamps disabled_by = "auto" on every entry the checker disabled before that
	// field existed, so R1.3's fail-safe — an absent origin means a human decided
	// — does not freeze them permanently.
	//
	// It is a MODE, not a modifier, and it writes packages.toml behind the same
	// gates --lint --fix uses (see confirmAutoDisableMigration). It is meant to be
	// run once per registry; a second run is a no-op by construction.
	markAutoDisabled bool
	// except, with --mark-auto-disabled, names the entries the
	// migration must NOT stamp: the deliberate pins a maintainer disabled on
	// purpose. Stamping one re-arms the exact defect story 043 removes, so an
	// entry naming no record in the registry aborts the run rather than being
	// ignored — a typo here protects nothing and says nothing.
	except []string
	// yes approves the post-check registry reconciliation without a
	// prompt (S021-R3.4). It is REQUIRED for any non-interactive run: with it
	// absent and no terminal to prompt on, --check reports the divergences and
	// writes nothing at all.
	//
	// The default is false and MUST stay false. packages.toml is a published
	// artifact — the overlay it lives in auto-commits and pushes — so a flag that
	// defaulted to true would turn every piped or scripted --check into a release.
	yes bool
	// distdir is --distdir: the directory `pkgdev manifest` is given
	// as --distdir, under the same name and with the same meaning `overlay
	// manifest` uses (S030-R1.3). Empty means "not named", which lets
	// autoupdate.distdir and then the host's own DISTDIR answer instead.
	distdir string
	// distfilesCache is --distfiles-cache: the read-only cache
	// consulted before a download, again under `overlay manifest`'s name. Its
	// default IS that command's default; the empty string disables the lookup,
	// which is why the config key is only consulted when the flag was not
	// passed at all (see resolveAutoupdateDistfileDirs).
	distfilesCache string
	// noFetchCache turns OFF the per-run sharing of upstream response
	// 024 (S024-R7.1). It is an opt-OUT: the default is false, which leaves the
	// deduplication ON (S024-R7.2). Its reason to exist is bisection — a
	// suspicious version can be re-checked against an un-deduplicated run to tell
	// a real upstream change from a sharing bug.
	noFetchCache bool
	// llm is --llm: the operator's consent to spend an agent on this
	// run. ONE flag enables BOTH staged-bump capabilities — the bump reviewer and
	// the build fixer (S033-R7.1) — because two names to turn one feature on is a
	// tax on the operator, not a choice they wanted.
	//
	// Configuration only ever SUBTRACTS from it: `autoupdate.validate.review` or
	// `fix_on_failure` set to false switches that one back off (S033-R7.2), while
	// neither key can enable anything on a run where this flag is absent. The
	// direction matters — the flag is where the cost is consented to.
	llm bool
}

// autoupdateRun is everything one invocation resolved before its mode runs.
// It is built in runAutoupdate and passed down as the receiver of every mode;
// it is never stored in a package variable and holds no context.Context
// (containedctx): the run context is a parameter of each mode that needs one.
type autoupdateRun struct {
	opts *autoupdateOptions
	// deps is the tree's dependencies: every seam a mode reaches is read
	// through it, never through a package variable (story 060, C6).
	deps        *deps
	dirs        autoupdateDistfileDirs
	validate    autoupdateValidatePolicy
	validateCfg config.ValidateConfig
	// uiConfig is the configuration the apply path resolves its renderer
	// against. nil is legal and reads as "nothing configured".
	uiConfig *config.Config
}

// autoupdateDistfileDirs is the answer to "which directories does the Manifest
// step work in", resolved ONCE in runAutoupdate and read by every mode that can
// reach that step (--apply, --apply all, --revive, --clean).
//
// It is resolved there, and not at each call site, because the two inputs are
// only both in scope there: the parsed config (appCtx.Config) and the *cobra
// command, which is the only thing that can tell an unpassed --distfiles-cache
// from one explicitly set to the empty string. It is carried on the run
// (autoupdateRun.dirs), which every such mode receives.
//
// The zero value is the safe one: no distdir named (the host's own DISTDIR
// answers) and no cache (no directory is read).
type autoupdateDistfileDirs struct {
	// Distdir is the --distdir flag, the highest rung of the precedence.
	Distdir string
	// ConfiguredDistdir is autoupdate.distdir, consulted only when Distdir is
	// empty. The precedence itself lives in distfiles.Resolve, which is handed
	// both rungs — this type does not re-implement it.
	ConfiguredDistdir string
	// Cache is the effective read-only distfiles cache, already reduced to one
	// value because there is no second-chance rung below it: "" means the
	// lookup is off.
	Cache string
}

// autoupdateValidatePolicy is the staged-bump validation policy this run applies:
// the depth table translated out of config, plus the two switches that decide
// what an unproved bump means (S033-R2, R3.13).
//
// It is resolved ONCE, in runAutoupdate, where both the config and the
// *cobra.Command are in scope — exactly as autoupdateRun.dirs is, and for the same
// reason: three Applier construction sites need the same answer, and a mode that
// silently missed it would validate nothing while looking like working software.
type autoupdateValidatePolicy struct {
	// Policy is the per-class depth table and the per-package overrides.
	Policy validate.DepthPolicy
	// Depth is --depth, nil when the operator did not type it. A POINTER because
	// `none` is a depth an operator may legitimately ask for, and a zero value
	// indistinguishable from "unset" is the one confusion that switches
	// validation off in silence.
	Depth *validate.Depth
	// RequireIsolation and RequireProof are the config keys behind
	// --require-isolation and R3.13's refusal.
	RequireIsolation bool
	RequireProof     bool
}

// newAutoupdateCmd builds `overlay autoupdate`. d is the tree's dependencies;
// every run of this command reads its seams through autoupdateRun.deps.
func newAutoupdateCmd(d *deps) *cobra.Command {
	// One options value per tree, never shared: see autoupdateOptions.
	o := &autoupdateOptions{}
	cmd := &cobra.Command{
		Use:         "autoupdate [package]",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Check and apply ebuild version updates",
		Long: `Automatically check upstream sources for new versions and apply updates.

Distfiles (--apply, --revive and --clean, which all regenerate a Manifest):

  --distdir            Directory pkgdev downloads into and digests from. It
                       defaults to the DISTDIR this machine's own package
                       manager reports (portageq distdir, itself normally
                       /var/cache/distfiles) — NOT to a temporary directory, so
                       a distfile fetched here is a distfile the next run and
                       the package manager both reuse. Set it in the config file
                       as autoupdate.distdir; this flag wins over that key.
  --distfiles-cache    Read-only cache consulted before downloading: a file
                       already there is symlinked into the working distdir
                       instead of fetched again. Nothing is ever written back
                       to it. Defaults to /var/cache/distfiles, and passing ""
                       disables the lookup. Set it in the config file as
                       autoupdate.distfiles_cache; this flag wins over that key.

  The two default to the same directory, which makes the lookup a no-op — there
  is nothing to link when the cache IS the working distdir. They differ once you
  point --distdir somewhere else, and that is when the cache starts paying off.

  Both names, and both meanings, are the ones ` + "`bentoo overlay manifest`" + ` already
  uses.

Examples:
  bentoo overlay autoupdate --check              Check all packages for updates
  bentoo overlay autoupdate --check net-misc/foo Check specific package
  bentoo overlay autoupdate --check --force      Check ignoring cache
  bentoo overlay autoupdate --check --no-fetch-cache  Check with one request per read, not one per URL
  bentoo overlay autoupdate --check --yes        Check, then write the registry pins without prompting
  bentoo overlay autoupdate --check --only source Check only source packages
  bentoo overlay autoupdate --check --only bin    Check only binary packages
  bentoo overlay autoupdate --list               List pending updates
  bentoo overlay autoupdate --apply net-misc/foo Apply update for package
  bentoo overlay autoupdate --apply all          Apply all pending updates
  bentoo overlay autoupdate --apply net-misc/foo --compile  Apply and compile test
  bentoo overlay autoupdate --apply net-misc/foo --clean    Apply and remove the old ebuild
  bentoo overlay autoupdate --revive-list         List orphaned packages with a newer upstream
  bentoo overlay autoupdate --check --revivable   Check active packages AND report revivable orphans
  bentoo overlay autoupdate --revive net-misc/foo Revive an orphan: seed from ::gentoo and bump
  bentoo overlay autoupdate --revive all          Revive every revivable orphan
  bentoo overlay autoupdate --lint                Check packages.toml against the record model
  bentoo overlay autoupdate --lint --fix          Repair what the linter can, after showing the diff
  bentoo overlay autoupdate --lint --fix --yes    Repair unattended (this overlay auto-commits and pushes)
  bentoo overlay autoupdate --mark-auto-disabled --except dev-libs/icu-compat,media-libs/libjxl-compat
                                                  ONE-SHOT: unfreeze the entries the checker disabled before
                                                  disabled_by existed, sparing the two deliberate pins. Run it
                                                  once per registry; without --yes it prints the plan and stops
  bentoo overlay autoupdate --apply all --distdir /srv/distfiles   Download into a specific directory
  bentoo overlay autoupdate --apply all --distfiles-cache ""       Never reuse a cached distfile`,
		// A method value bound to this tree's own options, not a closure:
		// contextcheck follows a closure's call into runAutoupdate and would ask
		// every newRootCmd caller for a context it has no use for.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAutoupdate(cmd, args, o, d)
		},
	}
	cmd.Flags().BoolVar(&o.check, "check", false, "Check for updates")
	cmd.Flags().BoolVar(&o.list, "list", false, "List pending updates")
	cmd.Flags().StringVar(&o.apply, "apply", "", "Apply update for specified package, or \"all\" for every pending update")
	cmd.Flags().BoolVar(&o.force, "force", false, "Ignore cache when checking")
	cmd.Flags().BoolVar(&o.compile, "compile", false, "Run compile test after apply. This PRIVILEGED gate stops at src_compile and never runs src_install: extending it would mean a second prompt, a second privilege escalation and a second copy of the repair loop (S042-D7). --depth=install is the unprivileged path that goes further, and the two are mutually exclusive")
	// --depth is read OFF THE COMMAND rather than bound to a package variable
	// (newValidateCmd's convention): the value is a rung of a ladder that has to
	// be parsed and rejected by name, and a package variable would carry one
	// invocation's depth into the next inside a single test binary.
	//
	// It is what gives S033-R2.7 a surface at all. Without it the resolver would
	// be answering a question nobody could ask, and `--compile` would be the only
	// way to reach a build gate.
	cmd.Flags().String("depth", "", "With --apply: validate every bump to this rung of the ladder instead of the one its class and the config select — none, options, patches, configure, compile or install, each including every rung before it. This REPLACES classification, the package tier and configuration, in either direction, and it is the only input allowed to ask for less. Anything above \"options\" starts a build and therefore applies one package at a time")
	cmd.Flags().BoolVar(&o.requireIsolation, "require-isolation", false, "With --compile: SKIP the compile test rather than run it without a verified network namespace. Without this an unisolated compile still runs, and its pass is labelled \"unverified isolation\" — creating the namespace needs privilege an ordinary user does not have, and Portage reports network-sandbox in FEATURES either way")
	cmd.Flags().BoolVarP(&o.clean, "clean", "c", false, "With --apply: sweep that package's directory after a successful apply. WITHOUT --apply: sweep the whole overlay — every package directory holding an ebuild no registry entry claims — optionally narrowed by a positional <category> or <category/package>. The full plan is printed BEFORE the confirmation, and the ebuilds are DELETED from an overlay that auto-commits and pushes, which is why an unattended sweep requires --yes. A directory whose entry has no version pin, or that no entry claims, is reported and left alone")
	cmd.Flags().IntVar(&o.concurrency, "concurrency", autoupdate.DefaultConcurrency, "max parallel checks/applies (1-100). A standalone --clean sweep does NOT take this default: it runs one directory at a time unless the flag is passed explicitly, because whether concurrent pkgdev manifest runs contend on DISTDIR or on pkgdev's own locking was never measured")
	cmd.Flags().IntVar(&o.timeout, "timeout", 0, "per-request HTTP timeout in seconds for --check (0 = use config autoupdate.http_timeout, default 30)")
	cmd.Flags().StringVar(&o.only, "only", "", "Restrict --check to packages of this type: \"bin\" or \"source\"")
	cmd.Flags().BoolVar(&o.reviveList, "revive-list", false, "List disabled (orphaned) packages whose upstream is newer than ::gentoo")
	cmd.Flags().StringVar(&o.revive, "revive", "", "Revive an orphaned package by seeding from ::gentoo and bumping it, or \"all\" for every revivable orphan")
	cmd.Flags().BoolVar(&o.revivable, "revivable", false, "With --check, also report revivable orphans (disabled+absent, upstream newer than ::gentoo) in the same pass")
	// --no-tui is DEPRECATED IN ITS HELP TEXT ONLY (S044-R3.5). Its behaviour is
	// untouched: it still disables the live TUI, it still answers to NO_COLOR
	// and BENTOO_NO_TUI, and it still outranks --ui and BENTOO_UI, because it is
	// applied as ModePlain at the flag layer rather than as a competing
	// mechanism (S044-R3.4).
	//
	// pflag's MarkDeprecated is deliberately NOT used. It hides the flag from
	// --help and prints a notice on every use, and both are behaviour changes —
	// the requirement asks for a word in the help text and explicitly not for a
	// change in what the flag does. A hidden flag is also the opposite of what a
	// deprecation is for: the operator who still passes it is exactly the reader
	// who needs to be told what replaced it.
	cmd.Flags().BoolVar(&o.noTUI, "no-tui", false, "DEPRECATED, use --ui=plain instead. It is still honoured and its behaviour has not changed: it disables the live TUI and streams plain output, it is exactly --ui=plain, and it OUTRANKS both --ui and BENTOO_UI — so --no-tui --ui=fullscreen renders plain, because an opt-out a flag could override would not be an opt-out. The two environment variables it has always answered to, NO_COLOR and BENTOO_NO_TUI, are unchanged and mean the same thing as passing it")
	cmd.Flags().BoolVar(&o.lint, "lint", false, "Check packages.toml against the record model: layout (# END marker, comments field last, no floating comments), field set (unknown or retired keys, redundant enabled = true, canonical field order) and semantics (invalid or ambiguous entries, undeclared release lines, commit tracking with no base source)")
	// No back-quoted words in this usage string: pflag reads the first one as the
	// flag's value placeholder and strips the quotes, which would render a bool
	// flag as "--fix binary".
	cmd.Flags().BoolVar(&o.fix, "fix", false, "With --lint: repair in place the violations that have a mechanical fix (the retired binary key, a redundant enabled = true, the canonical field order). The unified diff is printed BEFORE the confirmation, and packages.toml is PUBLISHED — this overlay auto-commits and pushes, so the write reaches origin — which is why an unattended repair requires --yes. Findings no repair can guess (an entry tracking commits with no base source) are reported and left to a human")
	// The presentation flags. All three change what this run SHOWS and none of
	// them changes what it does: the same packages are scanned, validated and
	// acted upon, and the exit status is the same, whichever way they are set
	// (S044-R8.4, S044-R9.6). Their variables live beside the plumbing that
	// consumes them, in overlay_autoupdate_ui.go.
	//
	// No back-quoted words in any of the three usage strings — see the --fix
	// note above: pflag reads the first one as the flag's value placeholder, so
	// a quoted ".md" here would render this as "--export .md".

	// No back-quoted words in either usage string below — see the --fix note
	// above: pflag reads the first one as the flag's value placeholder.
	cmd.Flags().BoolVar(&o.markAutoDisabled, "mark-auto-disabled", false, "ONE-SHOT MIGRATION: stamp disabled_by = \"auto\" on every entry the checker disabled before that key existed, so the reconciliation is free to re-enable them when their ebuild returns. Without it those entries state no origin, which now reads as a deliberate decision and freezes them for good. An entry that is held, already stamped, still enabled, or named in --except is left alone, and a second run changes nothing. The full plan is printed BEFORE the confirmation, and packages.toml is PUBLISHED — this overlay auto-commits and pushes, so the write reaches origin — which is why an unattended migration requires --yes")
	cmd.Flags().StringSliceVar(&o.except, "except", nil, "With --mark-auto-disabled: the entries the migration must NOT stamp, comma-separated or repeated. These are the pins a maintainer disabled ON PURPOSE — for this overlay, dev-libs/icu-compat and media-libs/libjxl-compat — and stamping one hands it back to the reconciliation that re-enabled and bumped it before. An entry naming no record in packages.toml ABORTS the run: a typo protects nothing and would otherwise pass in silence")
	cmd.Flags().BoolVarP(&o.yes, "yes", "y", false, "Approve without prompting whatever this run would otherwise stop and ask about: the post-check registry reconciliation, a --lint --fix repair, a --mark-auto-disabled migration, and a standalone --clean sweep. REQUIRED for any non-interactive write — without it, a piped or scripted run prints what it would do and changes nothing. Note the reach: with --clean this DELETES ebuilds, and with --fix or --mark-auto-disabled it rewrites packages.toml, in an overlay that auto-commits and pushes")

	// The two distfile directories. The names are byte-identical to `overlay
	// manifest`'s and so is what they mean; the DEFAULT of --distdir is not,
	// and cannot be, because the two commands genuinely differ there. An unset
	// --distdir on `overlay manifest` means a temporary directory discarded
	// after the run (that command needs no sudo, and its help says so). Here it
	// means the DISTDIR the host names, because a temporary one is the defect
	// this whole path was rewritten to remove: on the machine it was measured
	// on, /tmp is a tmpfs, so every distfile a bump fetched went into RAM.
	// Copying that command's help string verbatim would make this flag describe
	// behaviour this command does not have (S030-R1.3 asks for one vocabulary,
	// not one sentence).
	// No back-quoted words in either usage string — see the --fix note above:
	// pflag reads the first one as the flag's value placeholder and strips the
	// quotes, so "portageq distdir" in back-quotes would render this as
	// "--distdir portageq distdir".
	cmd.Flags().StringVar(&o.distdir, "distdir", "", "Distfiles directory used by pkgdev (default: the host's own DISTDIR, as reported by portageq distdir; overrides the autoupdate.distdir config key)")
	cmd.Flags().StringVar(&o.distfilesCache, "distfiles-cache", distfiles.DefaultCache, "Read-only distfiles cache consulted before download (\"\" disables; overrides the autoupdate.distfiles_cache config key)")
	cmd.Flags().BoolVar(&o.noFetchCache, "no-fetch-cache", false, "Fetch each URL per record instead of sharing one response across records that declare it")
	cmd.Flags().BoolVar(&o.llm, "llm", false, "With --apply: let the configured claude-code agent take part in validation. It enables BOTH capabilities — the bump reviewer, which reads what changed between the two versions and may ask for MORE validation than the depth policy chose, and the build fixer, which repairs the STAGED ebuild after a failed build and re-runs the same gate to decide. Set autoupdate.validate.review or fix_on_failure to false to switch one of them back off; neither key enables anything without this flag. Requires the claude CLI on PATH and provider = \"claude-code\" — otherwise the run warns once and proceeds exactly as it would have without the flag")
	return cmd
}

// resolveAutoupdateDistfileDirs decides which directories the Manifest step
// works in: the --distdir flag and autoupdate.distdir, handed on as the two
// rungs distfiles.Resolve consumes, plus the one effective read-only cache.
//
// # Why the cache needs cacheFlagWasSet and the distdir does not
//
// An unset --distdir is the empty string and so is an explicit --distdir "";
// both mean "I am not naming one", so the config key can simply answer when the
// flag is empty. The cache is the opposite: "" is a MEANING there — it disables
// the lookup, which is how `overlay manifest` documents it — so "the flag is
// empty" cannot be read as "the flag is absent". pflag's Changed is the only
// thing that distinguishes them, and a flag that was passed always wins.
//
// # Why only the config values are validated
//
// A relative path typed at a shell is unambiguous: it resolves against the
// directory the operator is standing in. The same string in a config file
// resolves against whatever directory the process happened to start in, which
// for a long-running sweep is arbitrary — so a relative autoupdate.distdir is
// rejected with a warning naming the key, rather than silently creating a
// download directory somewhere unpredictable. "~" is fine; it is expanded
// downstream.
func (o *autoupdateOptions) resolveAutoupdateDistfileDirs(cfg *config.Config, cacheFlagWasSet bool) autoupdateDistfileDirs {
	dirs := autoupdateDistfileDirs{Distdir: o.distdir}

	var configuredCache string
	if cfg != nil {
		dirs.ConfiguredDistdir = sanitizeConfiguredDir("autoupdate.distdir", cfg.Autoupdate.GetDistdir())
		configuredCache = sanitizeConfiguredDir("autoupdate.distfiles_cache", cfg.Autoupdate.GetDistfilesCache())
	}

	switch {
	case cacheFlagWasSet:
		dirs.Cache = o.distfilesCache
	case configuredCache != "":
		dirs.Cache = configuredCache
	default:
		dirs.Cache = o.distfilesCache // the flag's own default
	}
	return dirs
}

// sanitizeConfiguredDir validates a directory path that arrived from the config
// file — external input reaching a filesystem operation. It returns the path, or
// "" (treated everywhere as "not configured") after warning which key was
// ignored and why. Silence would be the wrong outcome twice over: the operator
// would believe the key took effect, and the run would use a different directory
// from the one the file names.
func sanitizeConfiguredDir(key, path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~") || filepath.IsAbs(path) {
		return path
	}
	logger.Warn("ignoring %s = %q: a directory in the config file must be an absolute path (or start with ~), because a relative one resolves against whatever directory this process was started in", key, path)
	return ""
}

// tuiEnabledForApply is the apply-path gate: the live region is on iff the mode
// this run resolved to draws one (autoupdateUsesTUI, S046-R3.3).
//
// It no longer calls tui.Enabled. The decision moved to report.ResolveMode so
// that one ui.mode governs this command AND `overlay manifest` instead of each
// deciding for itself. Nothing changes for an operator who configured nothing:
// with no ui.mode and no flags the resolution is `auto`, which yields inline on
// a terminal and plain off one — precisely the two answers tui.Enabled gave
// (R3.7). All three opt-outs are still honoured, the --no-tui flag among them;
// resolveUIMode folds it together with NO_COLOR and BENTOO_NO_TUI (R2.1, R2.2).
func (ar *autoupdateRun) tuiEnabledForApply() bool {
	return ar.autoupdateUsesTUI()
}

// buildApplyReporter selects the apply backend per the gate (tuiEnabledForApply)
// and returns a tui.Reporter, the extra ApplierOptions that wire it into the
// Applier, and a finish func to run once the applies complete.
//
// Plain branch (non-TTY / opt-out): a rate-limited plainReporter streams the tail
// to stderr with NO ANSI (R2.2/R2.3); finish closes the batch.
//
// TUI branch: a Bubble Tea Program is started and bound to ctx so Ctrl-C invokes
// cancel — cancelling the apply context, which kills the in-flight child
// (Apply runs under it) and triggers the existing orphan rollback (R5.1/R5.2). The
// extra options also route the in-UI y/n confirm (R4.2) and release the terminal
// for the compile step's sudo/doas prompt while teeing the child's output to a
// capture buffer the failure path still logs (R4.1). finish closes the batch,
// stops the program, and waits for it to exit so the terminal is restored before
// the summary is printed.
//
// finish is idempotent in both branches (sync.Once): the caller defers it, so
// every early return closes the batch and restores the terminal, and also calls
// it once explicitly before the summary, which must print after the TUI is gone.
// It returns nothing and writes no state, so its order relative to the overlay
// lock's release does not matter.
func (ar *autoupdateRun) buildApplyReporter(ctx context.Context, cancel context.CancelFunc, total int) (tui.Reporter, []autoupdate.ApplierOption, func()) {
	if !ar.tuiEnabledForApply() {
		r := tui.NewPlainReporter(os.Stderr, time.Second)
		r.BatchStart(total)
		return r, []autoupdate.ApplierOption{autoupdate.WithApplierReporter(r)}, sync.OnceFunc(func() { r.BatchDone("") })
	}

	prog, r := tui.New(ctx, cancel, os.Stdout, os.Stdin)
	prog.Start()
	r.BatchStart(total)
	extra := []autoupdate.ApplierOption{
		autoupdate.WithApplierReporter(r),
		// In-UI y/n confirmation rendered by the model instead of reading stdin
		// behind the program (R4.2).
		autoupdate.WithConfirmFunc(prog.Confirm),
		// Hand the real terminal to the compile child so a sudo/doas password
		// prompt is visible (R4.1), while teeing its stdout+stderr to a capture
		// buffer the failure path still saves (the applier's Output: %s contract).
		autoupdate.WithApplierRunAttached(func(cmd *exec.Cmd) ([]byte, error) {
			var buf bytes.Buffer
			cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
			cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
			// Only a Stdin the caller left nil gets the terminal. The privileged
			// compile leaves it nil so sudo/doas can prompt; the build gates'
			// `ebuild` sets an empty one, because it runs in its own process
			// group and reading the terminal from there stops it on SIGTTIN
			// (S054-R3.2).
			if cmd.Stdin == nil {
				cmd.Stdin = os.Stdin
			}
			err := tui.RunAttached(prog, cmd)
			return buf.Bytes(), err
		}),
	}
	finish := sync.OnceFunc(func() {
		r.BatchDone("")
		prog.Stop()
		// Wait for the program goroutine to exit so the terminal is restored
		// before the summary prints. A context-cancel (Ctrl-C) outcome surfaces as
		// tea.ErrProgramKilled here — the EXPECTED cancellation result, not a
		// failure — so it is intentionally not escalated (R2.4); the manual TTY
		// gate covers a program that never started cleanly.
		if err := prog.Wait(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
			logger.Debug("apply: live TUI program exited with error: %v", err)
		}
	})
	return r, extra, finish
}

// runAutoupdate is the RunE of `overlay autoupdate`: o holds the flags of the
// tree that allocated it, and d that tree's dependencies.
func runAutoupdate(cmd *cobra.Command, args []string, o *autoupdateOptions, d *deps) error {
	ar := &autoupdateRun{opts: o, deps: d}
	const (
		minConcurrency = 1
		maxConcurrency = 100
	)

	// Validate --concurrency BEFORE any package work so a bad value fails fast
	// with a clear message and a non-zero exit (R4.2). The accepted range
	// mirrors autoupdate.WithConcurrency's [1, 100] bound.
	if ar.opts.concurrency < minConcurrency || ar.opts.concurrency > maxConcurrency {
		return failWith(1, fmt.Errorf("--concurrency must be in range [%d, %d], got %d", minConcurrency, maxConcurrency, ar.opts.concurrency))
	}

	// Validate --timeout up front: a negative value is a typo, and 0 is the
	// sentinel for "use the configured/default value".
	if ar.opts.timeout < 0 {
		return failWith(1, fmt.Errorf("--timeout must be >= 0 seconds, got %d", ar.opts.timeout))
	}

	// Validate --only up front so a typo fails fast rather than silently
	// checking everything. Only "bin"/"source" (or unset) are accepted.
	switch ar.opts.only {
	case "", "bin", "source":
		// valid
	default:
		return failWith(1, fmt.Errorf("--only must be \"bin\" or \"source\", got %q", ar.opts.only))
	}

	// --fix repairs what --lint reports, so without --lint there is nothing for
	// it to repair. Validated here, beside the other flag checks and before any
	// config or network work, so `--check --fix` fails in a millisecond with a
	// message naming the command that works — rather than after a full check run,
	// or (worse) silently, leaving the operator believing the registry was
	// repaired when it was never even read.
	if ar.opts.fix && !ar.opts.lint {
		return failWith(1, fmt.Errorf("--fix repairs what --lint reports, so it is valid only together with it — run: bentoo overlay autoupdate --lint --fix"))
	}

	// The same reasoning one flag over: --except names what the migration must
	// spare, so without the migration it spares nothing and states nothing. The
	// failure mode it prevents is the expensive one — an operator who typed the
	// exclusion list but forgot the mode would read "nothing to do" and believe
	// their pins had been protected by a run that never looked at them.
	if len(ar.opts.except) > 0 && !ar.opts.markAutoDisabled {
		return failWith(1, fmt.Errorf("--except names the entries --mark-auto-disabled must not stamp, so it is valid only together with it — run: bentoo overlay autoupdate --mark-auto-disabled --except <atom>[,<atom>...]"))
	}

	// --lint and --mark-auto-disabled are both MODES, and the dispatch below is a
	// switch: given both, the first case wins and the other silently does
	// nothing. That is tolerable for a read-only mode and not for this one — the
	// migration runs ONCE, and an operator told "record model OK" would have no
	// way to notice that the ~90 entries they came to unfreeze are still frozen.
	// Refusing costs one re-run; the silent version costs a scan cycle nobody
	// knows was skipped.
	if ar.opts.markAutoDisabled && ar.opts.lint {
		return failWith(1, fmt.Errorf("--mark-auto-disabled and --lint are separate modes and only one runs per invocation — run them one after the other"))
	}

	appCtx, err := loadAppContextNoValidation(cmd)
	if err != nil {
		return failWith(1, fmt.Errorf("loading config: %w", err))
	}

	overlayPath := appCtx.OverlayPath

	// Determine config directory for autoupdate
	configDir, err := autoupdateConfigDir()
	if err != nil {
		return failWith(1, err)
	}

	// The process-wide context (func commandContext): overlay autoupdate is
	// annotated cancellable, so the first SIGINT, SIGTERM or SIGHUP cancels
	// the run. The Checker threads it through every outbound HTTP/LLM call, so
	// the run aborts within ~2 s of a signal (R3.1), and every child the modes
	// spawn in their own process group (git, pkgdev, the `claude` CLI, the
	// unprivileged ebuild) receives it, since no terminal signal reaches them
	// (story 054, R4.4).
	runCtx := commandContext(cmd)

	// Compute the autoupdate cache TTL from config (R2.1, R2.2). GetCacheTTL
	// returns the user-configured value when positive, otherwise the
	// 3600-second default — so the duration here is always positive and safe
	// to pass to WithCacheTTL inside runCheck.
	cacheTTL := time.Duration(appCtx.Config.Autoupdate.GetCacheTTL()) * time.Second

	// Resolve the two distfile directories once, here, where both the config and
	// the *cobra.Command are in scope (S030-R1.3). Every mode that regenerates a
	// Manifest reads the result; a mode that does not (--check, --list, --lint)
	// simply never looks at it.
	ar.dirs = o.resolveAutoupdateDistfileDirs(appCtx.Config, cmd.Flags().Changed("distfiles-cache"))

	// The staged-bump validation policy, resolved here for the same reason and in
	// the same place as the distfile directories above (S033-R2). A --depth that
	// does not name a rung is fatal BEFORE any package work: it is a typo the
	// operator must fix, and silently validating at the class depth instead would
	// be the run they did not ask for.
	ar.validate, err = o.resolveAutoupdateValidatePolicy(appCtx.Config, cmd)
	if err != nil {
		return failWith(1, err)
	}
	// The same block, unresolved, for the two --llm capabilities: their keys are
	// tri-state and only mean something next to the flag (S033-R7.2).
	ar.validateCfg = appCtx.Config.Autoupdate.Validate

	// The renderer, resolved here for the same reason and in the same place as
	// the two above: --ui, --no-tui and the ui.mode key are only all in scope
	// once the config is parsed (S044-D4).
	//
	// The ANSWER is deliberately not kept. resolveAutoupdateUIMode is a pure
	// function of the flags, the config and the terminal, so whoever needs the
	// mode asks for it where it is used and gets the same value; see its doc
	// comment for why a package variable holding it would be worse than the
	// three os.Getenv calls it saves.
	//
	// The apply path's gate is two call frames below runApply and carries no
	// config of its own, so the value it resolves against is parked here — the
	// same once-per-run hand-down the three decisions above use.
	//
	// # This call had two jobs and now has one
	//
	// It used to end the run on any error, citing S044-R3.9: "IF --ui is given a
	// value outside the accepted set … SHALL NOT run the check" — the flag, and
	// nothing else. resolveAutoupdateUIMode resolves the whole precedence chain,
	// so the gate fired on all four sources while claiming the authority of one.
	// Story 046's Task 4 made that gap total: root.go's PersistentPreRunE now
	// validates --ui for all 30 commands before any run function, so by the time
	// this line runs the flag has already been judged, and every rejection left
	// here is an AMBIENT one — a BENTOO_UI or a ui.mode, inherited from a shell
	// profile or a config file rather than typed for this run, which S046-R3.7
	// answers the opposite way: the report renders in plain and says so, and the
	// run keeps the status it would have had.
	//
	// Two things went wrong while the exit stood here, both measured: an
	// unusable BENTOO_UI cost this command its entire report, and on a run that
	// was going to fail for a reason of its own it replaced the operator's real
	// diagnostic — "packages.toml not found in overlay" — with one about a
	// display key that could not have caused it.
	//
	// What survives is the job the citation never covered, and it is why the
	// call is not simply deleted: S044-R3.6's downgrade sentence reaches stderr
	// HERE, before any package work, because resolving is what routes it — and
	// warnUIDowngrade keeps it to one line however many times the mode is
	// resolved after this.
	ar.uiConfig = appCtx.Config

	if _, err := resolveAutoupdateUIMode(appCtx.Config, o.noTUI, ar.deps.uiIsTerminal); err != nil {
		// Debug, not Error, and that is R3.6 rather than indifference. The
		// refusal is stated once per run, at Warn, by whoever produces a report
		// — presentCheckReport, through reportModeOrPlain — naming the source,
		// the value and the mode used instead. One typo answered in two voices
		// is the duplication 11.1 already paid for once at the root. The line is
		// kept so a --verbose run can still see where the resolution first
		// failed, which is several frames earlier than where it is announced.
		logger.Debug("the ambient UI mode did not resolve before the package work: %v", err)
	}

	// One run per overlay (S056-R4.1): every mode that writes the overlay or
	// the registry holds <overlay>/.autoupdate.bentoo-lock until it returns, so
	// a cron run and a manual run never edit packages.toml and the ebuilds at
	// once. It is taken here, after the config resolved and before the mode
	// runs, so the registry-fix loop inside --check runs under it too.
	if ar.autoupdateNeedsOverlayLock() {
		lock, err := acquireOverlayLock(overlayPath)
		if err != nil {
			if errors.Is(err, filelock.ErrLocked) {
				return failWith(1, fmt.Errorf("another bentoo run holds the overlay: %w", err))
			}
			return failWith(1, fmt.Errorf("cannot take the overlay lock: %w", err))
		}
		// Released by the defer on every return, including after a signal
		// cancelled the mode's context (S056-R4.7): every mode returns its
		// outcome and func main ends the process only after this function has
		// returned. The registration with exitProcess stays as a second net;
		// Release is idempotent, so the two never conflict.
		unregister := registerExitCleanup(lock.Release)
		defer func() {
			unregister()
			lock.Release()
		}()
		sweepStaleTemps(overlayPath, configDir)
	}

	// Handle different modes. Each returns its outcome (func exitWith), which
	// is the command's: the deferred lock release runs before func main maps it
	// to the exit status.
	switch {
	case ar.opts.lint:
		return ar.runLint(overlayPath)
	case ar.opts.markAutoDisabled:
		// Directly below --lint because it reads and writes the same file for the
		// same kind of reason: both are registry maintenance rather than a
		// version scan. The two can never both be true here — the guard above
		// rejects that combination before any file is opened — so the relative
		// order of these two cases is documentation, not behaviour.
		return ar.runMarkAutoDisabled(overlayPath)
	case ar.opts.check:
		return ar.runCheck(runCtx, overlayPath, configDir, args, cacheTTL, appCtx.Config, appCtx.Config.Autoupdate.LLM)
	case ar.opts.list:
		return ar.runList(configDir)
	case ar.opts.apply == "all":
		return ar.runApplyAll(runCtx, overlayPath, configDir, appCtx.Config.Autoupdate.LLM)
	case ar.opts.apply != "":
		return ar.runApply(runCtx, overlayPath, configDir, ar.opts.apply, appCtx.Config.Autoupdate.LLM)
	case ar.opts.reviveList:
		return ar.runReviveList(runCtx, overlayPath, configDir, cacheTTL, appCtx.Config, appCtx.Config.Autoupdate.LLM)
	case ar.opts.revive != "":
		return ar.runRevive(runCtx, overlayPath, configDir, ar.opts.revive, cacheTTL, appCtx.Config, appCtx.Config.Autoupdate.LLM)
	case ar.opts.clean:
		// MUST stay below both --apply cases (S027-G6): above them it would
		// convert every existing `--apply … --clean` invocation into an
		// overlay-wide sweep. With --apply present those cases match first and
		// --clean keeps meaning "sweep the directory this apply touched".
		// The concurrency decision is resolved HERE, where cmd is in scope:
		// reading the command from inside runSweep would mean handing it a
		// *cobra.Command only to ask it one question.
		return ar.runSweep(runCtx, overlayPath, args, ar.sweepConcurrency(cmd.Flags().Changed("concurrency")))
	default:
		// No flag specified, show help
		cmd.Help() //nolint:errcheck // help output failure is not actionable
		return nil
	}
}

// resolveHTTPTimeout resolves the per-request HTTP timeout for --check and the
// revive flows: the --timeout flag when positive, otherwise
// autoupdate.http_timeout from config (which itself falls back to a 30s default).
// The result is always a positive duration, safe to pass to WithHTTPRequestTimeout.
func (ar *autoupdateRun) resolveHTTPTimeout(cfg *config.Config) time.Duration {
	secs := ar.opts.timeout
	if secs <= 0 {
		secs = cfg.Autoupdate.GetHTTPTimeout()
	}
	return time.Duration(secs) * time.Second
}

// runCheck handles the --check flag. cacheTTL must be a positive duration —
// the caller resolves it from AutoupdateConfig.GetCacheTTL, which guarantees a
// positive value (R2.1, R2.2). A non-positive cacheTTL is treated as "use the
// Checker default" and the WithCacheTTL option is skipped, since WithCacheTTL
// rejects non-positive values at construction time.
func (ar *autoupdateRun) runCheck(ctx context.Context, overlayPath, configDir string, args []string, cacheTTL time.Duration, cfg *config.Config, llmCfg config.LLMConfig) error {
	opts := []autoupdate.CheckerOption{
		autoupdate.WithConfigDir(configDir),
		autoupdate.WithConcurrency(ar.opts.concurrency),
		// Per-request HTTP timeout (flag > config > 30s default). The Checker
		// derives the larger per-operation budget so the retry attempts fit.
		autoupdate.WithHTTPRequestTimeout(ar.resolveHTTPTimeout(cfg)),
		// Restrict the batch to a package type when --only is set; empty is a
		// no-op (checks every package). Ignored on the single-package path.
		autoupdate.WithTypeFilter(ar.opts.only),
		// NewChecker authenticates api.github.com itself: it resolves the token
		// from GITHUB_TOKEN/GH_TOKEN via the secrets chain (github.ResolveToken).
		// Tune per-host HTTP rate limits: GitHub ~10/s and GitLab ~3/s (the two
		// hosts that dominate packages.toml), every other host at the conservative
		// 6s default. Without this the uniform 1-req/6s-per-host limiter serialises
		// the ~220 GitHub/GitLab packages, making a large --concurrency pointless.
		autoupdate.WithRateLimiter(autoupdate.NewRateLimiter(autoupdate.WithTunedHostPolicies())),
		// Share one response across every record that declares the same URL — on
		// by default (S024-R7.2). --no-fetch-cache turns it off, restoring one
		// request per read exactly as before story 024, so a suspicious result can
		// be compared against an un-deduplicated run (S024-R7.1).
		autoupdate.WithFetchCache(!ar.opts.noFetchCache),
	}
	if cacheTTL > 0 {
		opts = append(opts, autoupdate.WithCacheTTL(cacheTTL))
	}

	// Wire an LLM provider into the check path (R5.2). newConfiguredLLMProvider
	// returns (nil, nil) when no provider is configured, (provider, nil) on
	// success, and (typed-nil, err) on a construction failure. The error must be
	// the PRIMARY guard: a failed constructor boxes a nil concrete pointer into a
	// NON-nil interface, so we wire WithLLMClient only on err==nil AND p!=nil —
	// never a typed-nil (which would make fetchUpstreamVersion dereference a nil
	// receiver). On failure we Warn and continue; --check still runs, skipping LLM
	// extraction. WithLLMProviderConfigured records that a provider WAS requested
	// (provider != "") so the Checker suppresses its "unused llm_prompt" Warn
	// (R5.3) and we avoid a double-warn with the failure line just below.
	if p, err := newConfiguredLLMProvider(llmCfg); err != nil {
		logger.Warn("LLM provider %q unavailable; --check will skip LLM version extraction: %v", llmCfg.Provider, err)
	} else if p != nil {
		opts = append(opts, autoupdate.WithLLMClient(p))
	}
	opts = append(opts, autoupdate.WithLLMProviderConfigured(llmCfg.Provider != ""))
	opts = append(opts, autoupdate.WithGentooPath(gentooRepoPath()))

	// Progress feedback: CheckAll fans out concurrently and otherwise prints
	// nothing until the final table, so show a live [pct%] done/total counter on
	// a single self-rewriting line (mirrors `overlay compare`). The callback is
	// driven by CheckAll's atomic counter, so the count is monotonic even though
	// it fires from many goroutines. Suppressed under --quiet; harmless on the
	// single-package path (CheckPackage never fires it).
	if !quiet {
		opts = append(opts, autoupdate.WithProgressCallback(func(done, total uint64) {
			percent := uint64(0)
			if total > 0 {
				percent = (done * 100) / total
			}
			fmt.Printf("\r  Checking: [%3d%%] %d/%d", percent, done, total)
		}))
	}

	// Capture the fully-assembled option set so the story-014 registry-fix
	// re-check can build a FRESH Checker that reloads packages.toml after an agent
	// edit (AD4: CheckPackage reads the config cached at NewChecker time, so there
	// is no in-place reload — a new Checker is required).
	newChecker := func() (*autoupdate.Checker, error) {
		return autoupdate.NewChecker(overlayPath, opts...)
	}

	checker, err := newChecker()
	if err != nil {
		return failWith(1, fmt.Errorf("failed to initialize checker: %w", err))
	}

	if len(args) > 0 {
		// Check specific package
		pkg := args[0]
		result, err := checker.CheckPackage(ctx, pkg, ar.opts.force)
		if err != nil {
			// A removed ebuild is not a hard error: auto-disable the orphaned
			// entry and report it as info so repeated runs stay quiet.
			if errors.Is(err, autoupdate.ErrNoEbuildFound) {
				if derr := checker.DisableOrphans([]string{pkg}); derr != nil {
					logger.Warn("failed to disable orphaned package %s: %v", pkg, derr)
				}
				logger.Info("%s has no ebuild in the overlay — disabled in packages.toml", pkg)
				return nil
			}
			return failWith(1, fmt.Errorf("failed to check package %s: %w", pkg, err))
		}
		if result.Skipped != "" {
			logger.Info("%s skipped: %s in packages.toml", pkg, result.Skipped)
			return nil
		}
		// S045-R1.2: the one package this run scanned, as the same report the
		// batch path builds and through the same render — one element, joined
		// with the zero validation half because nothing validates here. This
		// path prices nothing, so no plan has been put in front of the operator
		// and the report states its own.
		//
		// D6 keeps the return below: only the batch path may reconcile the
		// registry, and moving the render past this point must not move the
		// return with it.
		//
		// It validated nothing, so it planned nothing, so it left nothing
		// unreached — which is what nothingValidated states, and what
		// checkReport answers for an empty plan on a batch run whose plan came
		// out empty too.
		const noPlanWasPrinted = false
		single := checkReport([]autoupdate.CheckResult{*result}, nothingValidated())
		ar.presentCheckReport(single, noPlanWasPrinted)
		return nil
	}

	// Check all packages. CheckAll never returns a fatal error: every
	// per-package failure is captured in the BatchResult.
	result := checker.CheckAll(ctx, ar.opts.force)

	// Clear the progress line before rendering results so the counter does not
	// bleed into the table. Mirrors `overlay compare`'s clear step.
	if !quiet {
		fmt.Print("\r                                        \r")
	}

	// Emit one stderr line per per-package failure. FormatFailures is called
	// only after CheckAll has fully completed, so the output is deterministic.
	//
	// It stays HERE, where it has always been, even though the scan it
	// qualifies is now drawn at the end of the run (S045-D1): these lines
	// therefore reach stderr BEFORE the report reaches stdout, where they used
	// to follow it. That is the lesser change. Moving them down beside the
	// report would put per-package failures AFTER the interactive registry-fix
	// prompt below — the prompt whose whole subject is those same failures —
	// and an operator would be asked to repair packages it had not yet been
	// told about.
	if result.HasFailures() {
		result.FormatFailures(os.Stderr)
	}

	// Offer an interactive LLM registry repair for the packages that failed
	// upstream-version extraction (story 014). Gated to a usable claude-code fixer
	// AND an interactive stdin. newConfiguredRegistryFixer returns a TRUE nil
	// interface for a non-claude provider; a construction error Warns and is
	// treated as absent — never box a nil pointer (AD9). When the gate is false
	// (non-claude provider, no claude CLI, or non-TTY stdin) the output and exit
	// code below are exactly as before this story (R7.x / R10.1).
	fixer, ferr := ar.deps.checkRegistryFixer(llmCfg)
	if ferr != nil {
		logger.Warn("LLM registry fixer unavailable; --check will not offer registry repair: %v", ferr)
		fixer = nil
	}
	if fixer != nil && ar.deps.checkInteractive() {
		if perr := promptRegistryFixes(ctx, overlayPath, fixer, result.Failures, os.Stdin, newChecker); perr != nil {
			logger.Warn("registry-fix prompt ended with an error: %v", perr)
		}
	}

	// --revivable: in the same pass, also scan the disabled+absent (orphaned)
	// entries and report those an autoupdate could revive (upstream newer than
	// ::gentoo), reusing the checker --check already built. Read-only and
	// best-effort — it never changes the check's exit code.
	if ar.opts.revivable {
		ar.reportRevivableOrphans(ctx, checker, cfg)
	}

	// S033-R9.1: put every pending update through the gates at its resolved
	// depth, and publish none of it. It runs BEFORE the reconciliation below for
	// the same reason that one runs last: the reconciliation is the only prompt in
	// this command that can write to the overlay, and it must be the last thing on
	// screen rather than scrolled off by a validation report.
	validated, planPrinted := ar.runPendingValidation(ctx, overlayPath, configDir, result.Items, llmCfg)

	// S045-R1.1/R1.3, D1: the run's one report, built where both of its halves
	// are finally in hand — the scan above, and whatever the validation just
	// contributed — and drawn exactly once.
	//
	// It is drawn HERE rather than where the scan finished because a report
	// rendered there could not carry a result the gates had not produced yet:
	// everything that can contribute to this report has contributed by this
	// line. planPrinted travels into render.Options.SkipPlan so the plan the
	// operator read before the confirmation prompt is not drawn to them a
	// second time (S045-R2.3).
	//
	// "The run reached the end of its plan" needs no parameter of its own: it
	// is the envelope's fact, established by buildReport above and carried
	// inside the very value being joined here — so the screen and the export
	// state it once, from one place (D1).
	joined := checkReport(result.Items, validated)
	ar.presentCheckReport(joined, planPrinted)

	// S021-R3.2/R3.3/R3.4: compare the registry against the overlay and, behind
	// ONE confirmation, write the pins back. It runs here, at the very end of the
	// batch path, for three reasons:
	//
	//   - it reads packages.toml from disk, and the LLM registry fixer above may
	//     have just rewritten it; running earlier would reconcile a file that no
	//     longer exists in that form;
	//   - the one prompt in this command that can PUBLISH must be the last thing
	//     on screen, not scrolled off by the revivable-orphan report;
	//   - it is best-effort, exactly like that report: it never changes the exit
	//     code, so a non-TTY run that writes nothing still exits 0 (R3.4).
	//
	// Only this batch path reconciles. The single-package path above returns
	// before reaching here on purpose — see reconcileRegistryAfterCheck.
	ar.reconcileRegistryAfterCheck(overlayPath)

	// Return the contract-defined code: 0 all-ok, 1 partial, 2 total fail.
	return exitWith(result.ExitCode())
}

// autoupdateNeedsOverlayLock reports whether the selected mode writes the
// overlay or its registry and so must hold the overlay lock. It mirrors the
// dispatch in runAutoupdate: --list, --lint without --fix and the help default
// only read and take no lock; every other mode takes it.
func (ar *autoupdateRun) autoupdateNeedsOverlayLock() bool {
	switch {
	case ar.opts.lint:
		return ar.opts.fix
	case ar.opts.markAutoDisabled, ar.opts.check:
		return true
	case ar.opts.list:
		return false
	case ar.opts.apply != "", ar.opts.reviveList, ar.opts.revive != "", ar.opts.clean:
		return true
	default:
		return false
	}
}

// acquireOverlayLock takes the overlay's exclusive lock, waiting up to
// filelock.Wait for a live holder and reaping one left by a dead run. A
// timeout wraps filelock.ErrLocked and names the lock path and holder's PID.
func acquireOverlayLock(overlayPath string) (*filelock.Lock, error) {
	return filelock.Acquire(filepath.Join(overlayPath, ".autoupdate.bentoo-lock"), "bentoo overlay autoupdate")
}

// sweepStaleTemps removes the temporary files killed runs left under the
// overlay (skipping .git) and directly in the autoupdate config dir, one Warn
// line per removed path (S056-R6.1). It runs with the overlay lock held, and
// only files whose writer's PID is dead are removed, so no live writer loses
// its file. A sweep failure is logged and the run proceeds: debris is a
// nuisance, not a reason to refuse the run.
func sweepStaleTemps(overlayPath, configDir string) {
	for _, target := range []struct {
		root      string
		recursive bool
	}{{overlayPath, true}, {configDir, false}} {
		removed, err := fileutil.RemoveStaleTemps(target.root, target.recursive)
		for _, path := range removed {
			logger.Warn("removed a temporary file left by a killed run: %s", path)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Warn("sweeping temporary files left by killed runs under %s: %v", target.root, err)
		}
	}
}

// stdinIsTerminal reports whether standard input is an interactive terminal (a
// character device) rather than a pipe, regular file, or /dev/null. The
// story-014 registry-fix prompt is shown only when this is true, so a piped or
// CI run of --check keeps its existing non-interactive output and exit code
// (R7.3, AD8). output.IsTerminal probes stdout; this probes stdin specifically.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ---------------------------------------------------------------------------
// Post-check registry reconciliation (S021-R3.2, R3.3, R3.4)
// ---------------------------------------------------------------------------

// reconcileRegistryAfterCheck is the post-check reconciliation: it compares the
// whole registry against the overlay, prints the three classes of disagreement,
// and — behind exactly one confirmation covering all of them (R3.2) — writes the
// repairable ones back in a single batch (design D4).
//
// # Why a confirmation at all
//
// packages.toml is a published artifact. The overlay it lives in auto-commits
// and pushes, so a write here reaches origin within minutes: a wrong pin is not
// a local mistake to fix before anyone notices, it is a released one. Hence the
// three gates, in order of how much they trust the caller: --yes writes
// unattended because the operator asked for that in so many words; an
// interactive terminal is asked; anything else prints the divergences and
// writes nothing (R3.4).
//
// # Why only the batch path calls this
//
// Reconcile walks the ENTIRE registry, not the packages named on the command
// line — it has no notion of a subset. Calling it from `--check <one-package>`
// would answer a question about one package with a prompt to write a few hundred
// pins, and would pay the whole registry's directory-read cost for a command
// meant to be quick. design.md's check sequence starts at CheckAll for the same
// reason. A maintainer who wants the reconciliation runs a full --check.
//
// Every failure here is best-effort and non-fatal: an unreadable registry, a
// declined prompt and a failed write all leave the check's exit code alone. The
// check itself already succeeded; reconciliation is bookkeeping on top of it.
func (ar *autoupdateRun) reconcileRegistryAfterCheck(overlayPath string) {
	cfg, err := autoupdate.LoadPackagesConfig(overlayPath)
	if err != nil {
		// Nothing to reconcile against. The check has already reported whatever
		// this meant for the packages themselves, so this is a debug note, not a
		// second error line about the same file.
		logger.Debug("reconcile: skipped, no usable packages.toml: %v", err)
		return
	}

	divs := autoupdate.Reconcile(overlayPath, cfg.Packages)
	if len(divs) == 0 {
		// R3.2 is conditional on divergences existing: with none, print nothing
		// at all and let the check's own output stand.
		return
	}

	pins := autoupdate.StalePinBatch(divs)
	displayDivergences(divs, len(pins))

	if len(pins) == 0 {
		// Every divergence is report-only (an unclaimed ebuild, an entry whose
		// directory is empty). There is no batch, so there is nothing to confirm:
		// asking "write 0 pins?" would train the operator to say yes.
		output.Info.Println("  Nothing to write: no entry's pin disagrees with the overlay.")
		return
	}

	if !ar.confirmRegistryWrite(divs, len(pins)) {
		// R3.3: return WITHOUT calling the writer. Not "call it with an empty
		// map", not "call it and roll back" — the file is never opened, so it is
		// byte-identical by construction rather than by care.
		return
	}

	// D4: one call, the whole batch. SetPackageVersions does one read and one
	// atomic rename, so a partially-written registry is never reachable.
	if err := ar.deps.registryWriter(overlayPath, pins); err != nil {
		// Reported, never swallowed — but not fatal: the check itself succeeded
		// and its exit code says so. The next run proposes the same batch again.
		logger.Error("reconcile: failed to write %d version pin(s) to packages.toml: %v", len(pins), err)
		output.Error.Fprintf(os.Stderr, "  The registry was NOT updated: %v\n", err)
		return
	}
	output.Success.Printf("  Wrote %d version pin(s) to packages.toml.\n", len(pins))
	output.Info.Println("  Review the diff before it is published: 'bentoo overlay diff'")
}

// displayDivergences prints the three classes grouped, then the batch summary
// the confirmation is about. writable is the number of entries that would be
// written — the stale-pin count, never len(divs), because the other two classes
// are reported and left alone.
//
// The full list is printed, not a sample: the operator is approving one diff
// (D4), and a truncated list hides exactly the line that is wrong.
func displayDivergences(divs []autoupdate.Divergence, writable int) {
	fmt.Println()
	output.Header.Println("Registry Reconciliation")
	fmt.Println()
	output.Info.Printf("  The registry and the overlay disagree on %d point(s).\n\n", len(divs))

	// Counted up front, in their own loop, so the summary below cannot depend on
	// which groups happened to be printed or in what order.
	var firstTime, corrected int
	for _, d := range divs {
		if d.Kind != autoupdate.StalePin {
			continue
		}
		if d.Pin == "" {
			firstTime++
		} else {
			corrected++
		}
	}

	// Grouped by class, each group keeping Reconcile's order (sorted by key,
	// then by class, then in Gentoo version order) so two runs over an unchanged
	// overlay print the identical list.
	// Returns how many lines the group printed, so a caller can append a
	// follow-up line only to a group that actually appeared (R7.1).
	//
	// # The key column is measured here, which is why `line` is handed its key
	//
	// All three line formats below used to open with %-45s. Nobody measured 45,
	// and it is wrong in both directions at once. Too wide: a group of registry
	// keys shaped like `net-libs/webkit-gtk:4.1` — 23 cells — pays 22 cells of
	// empty air on every row. Too narrow: the longest atom this overlay
	// actually holds, `media-plugins/gst-plugins-adaptivedemux2`, is 40 cells,
	// so the guess is five cells from the day a key overruns the column and
	// pushes the second field right on that row alone, which is worse than no
	// alignment at all. Neither error is visible where the number is typed,
	// because the width that is correct depends on the keys THIS run produced
	// (R6.2).
	//
	// The width a group needs is the widest key IN THAT GROUP, which is knowable
	// only once the group's members have been collected. This closure already
	// collected them; it just threw the keys away by formatting each line as it
	// went. So it now collects the divergences rather than their finished lines,
	// measures the column over them, and hands each `line` its key already laid
	// into it — the loop-that-prints-as-it-goes being exactly how a typed width
	// survives.
	//
	// Per group, not across all three: a group is a table under its own heading,
	// and "Pins to write" should not be widened by one long key from a list
	// printed further down that no reader is comparing it against column by
	// column.
	printGroup := func(kind autoupdate.DivergenceKind, heading string, line func(d autoupdate.Divergence, key string) string) int {
		var members []autoupdate.Divergence
		var keys []string
		for _, d := range divs {
			if d.Kind == kind {
				members = append(members, d)
				keys = append(keys, d.Key)
			}
		}
		if len(members) == 0 {
			return 0
		}
		// Display cells, never bytes (R6.1): the width and the padding are the
		// same measurement, so a key and the column holding it cannot disagree
		// about how wide it is. The space that separates the key from what
		// follows stays in each format string below, where it is a gap between
		// two columns and not part of either (R6.3).
		width := render.ColumnWidth(keys)

		fmt.Printf("  %s (%d):\n", heading, len(members))
		for _, d := range members {
			fmt.Printf("    %s\n", line(d, padColumn(d.Key, width)))
		}
		fmt.Println()
		return len(members)
	}

	_ = printGroup(autoupdate.StalePin, "Pins to write", func(d autoupdate.Divergence, key string) string {
		if d.Pin == "" {
			return fmt.Sprintf("%s (no pin) → %s", key, d.Disk)
		}
		return fmt.Sprintf("%s %s → %s", key, d.Pin, d.Disk)
	})
	unclaimed := printGroup(autoupdate.UnclaimedEbuild, "Ebuilds no entry keeps — NOT written", func(d autoupdate.Divergence, key string) string {
		return fmt.Sprintf("%s %s", key, d.Disk)
	})
	if unclaimed > 0 {
		// R7.1: this report used to end at the finding. Naming the command that
		// acts on it closes the loop — an unclaimed ebuild is removed by a
		// sweep, and until story 027 the only sweep ran inside an apply, so a
		// package already at its upstream version could never reach one.
		output.Info.Println("  Remove them with: bentoo overlay autoupdate --clean")
		fmt.Println()
	}
	_ = printGroup(autoupdate.NoEbuild, "Entries whose directory holds no ebuild — NOT written", func(d autoupdate.Divergence, key string) string {
		if d.Pin == "" {
			return fmt.Sprintf("%s (no pin)", key)
		}
		return fmt.Sprintf("%s pins %s", key, d.Pin)
	})

	if writable == 0 {
		return
	}

	// A1: state the number of entries about to be written, and split it the way
	// the operator needs to read it. On the first run these are ~316 entries
	// gaining a pin they never had; on later runs the same number means
	// something very different, so the two are counted apart.
	output.Warning.Printf("  About to write %d version pin(s) into packages.toml", writable)
	switch {
	case corrected == 0:
		output.Warning.Printf(" — all %d pinned for the FIRST time.\n", firstTime)
	case firstTime == 0:
		output.Warning.Printf(" — %d stale pin(s) corrected.\n", corrected)
	default:
		output.Warning.Printf(" — %d pinned for the FIRST time, %d stale pin(s) corrected.\n", firstTime, corrected)
	}
	output.Warning.Println("  packages.toml is PUBLISHED: this overlay auto-commits and pushes, so this write reaches origin.")
}

// confirmRegistryWrite is the write gate (R3.2, R3.4). It reports whether the
// batch of `writable` pins may be written, and prints why whenever the answer is
// no — a run that silently declines to write is indistinguishable from one that
// wrote and failed to say so.
func (ar *autoupdateRun) confirmRegistryWrite(divs []autoupdate.Divergence, writable int) bool {
	if ar.opts.yes {
		// R3.4: an explicit, in-so-many-words approval. Stdin is never read on
		// this path, so it works from a pipe, a cron job or a CI step.
		output.Warning.Printf("  --yes given: writing %d version pin(s) without a prompt.\n", writable)
		return true
	}
	if !ar.deps.registryPromptIsInteractive() {
		// R3.4: the divergences above ARE the report; this run writes nothing.
		output.Warning.Println("  Not an interactive terminal and --yes was not given: nothing written.")
		output.Info.Printf("  Re-run with --yes to write these %d version pin(s) unattended.\n", writable)
		return false
	}
	// R3.2: ONE question covering every divergence above, not one per entry.
	fmt.Println()
	return ar.deps.confirmRegistryWrite(fmt.Sprintf(
		"Write %d version pin(s) to packages.toml? (%d divergence(s) reviewed)", writable, len(divs)))
}

// reportRevivableOrphans is the --check --revivable add-on: it resolves the
// ::gentoo provider and appends the revivable-orphan report to a --check run. It
// is read-only and best-effort — a provider-resolution failure warns and returns
// without affecting the check's exit code. checker is the one --check already
// built, so its loaded packages.toml and token wiring are reused.
func (ar *autoupdateRun) reportRevivableOrphans(ctx context.Context, checker *autoupdate.Checker, cfg *config.Config) {
	prov, err := ar.deps.resolveGentooProvider(ctx, cfg)
	if err != nil {
		logger.Warn("revivable-orphan scan skipped: %v", err)
		return
	}
	defer prov.Close() //nolint:errcheck // every provider Close is a no-op that returns nil; there is nothing to act on

	candidates, ferr := checker.FindRevivableOrphans(ctx, prov)
	if ferr != nil {
		logger.Warn("revivable-orphan scan completed with soft errors: %v", ferr)
	}
	displayReviveCandidates(candidates)
}

// runList handles the --list flag
func (ar *autoupdateRun) runList(configDir string) error {
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		return failWith(1, fmt.Errorf("failed to load pending list: %w", err))
	}

	updates := pending.List()
	displayPendingUpdates(updates)
	return nil
}

// runLint handles the --lint flag: it checks the overlay's packages.toml
// against the record model and prints one line per violation, grouped in file
// order. It exits non-zero when anything is reported, so the same command works
// as a pre-commit gate on the registry.
//
// With --fix it hands over to runLintFix after the report, which repairs what
// the rules above can repair and then owns the exit code — see there.
func (ar *autoupdateRun) runLint(overlayPath string) error {
	issues, err := autoupdate.LintPackagesConfig(overlayPath)
	// Issues found by the text scan are printed even when the file then fails to
	// parse — a missing marker is worth reporting alongside the syntax error.
	for _, issue := range issues {
		output.Error.Println("  " + issue.String())
	}
	if err != nil {
		// --fix adds nothing on this path: a file that does not load cannot be
		// repaired either, and RepairPackagesConfig refuses to start on one.
		return failWith(1, fmt.Errorf("failed to lint packages.toml: %w", err))
	}

	if len(issues) > 0 {
		printLintTally(issues)
	}

	if ar.opts.fix {
		// From here the repair owns the verdict: the exit code must describe the
		// registry as it stands AFTER the run, which the list above no longer
		// does.
		return ar.runLintFix(overlayPath, issues)
	}

	if len(issues) == 0 {
		output.Success.Println("packages.toml: record model OK")
		return nil
	}
	return failWith(1, fmt.Errorf("packages.toml: %d issue(s)", len(issues)))
}

// printLintTally closes the report with a per-rule count: a registry
// mid-migration reports the same rule hundreds of times, so the detail lines
// above say WHERE and this says WHAT.
func printLintTally(issues []autoupdate.LintIssue) {
	counts := make(map[string]int, len(issues))
	rules := make([]string, 0, len(issues))
	for _, issue := range issues {
		if counts[issue.Rule] == 0 {
			rules = append(rules, issue.Rule)
		}
		counts[issue.Rule]++
	}
	sort.Strings(rules)

	// The rule column is as wide as the widest rule name THIS run reported, in
	// display cells (R6.2). The 26 that used to be typed here was a guess about
	// a vocabulary that belongs to internal/autoupdate's lint rules and not to
	// this printer: two cells past `bracket-line-in-comments`, the longest of
	// the thirteen, which makes it 15 cells of empty air on a tally that
	// reported only `field-order` and one longer rule name away from
	// overflowing without anyone here noticing. Every rule this run found is in
	// hand before the first line is printed, so there was never anything to
	// guess about.
	//
	// The single space in the format is the gap to the count beside it — the
	// air BETWEEN two columns, which nothing in a run's data can make wider, so
	// it is written down where a width is measured (R6.3).
	width := render.ColumnWidth(rules)

	fmt.Println()
	for _, rule := range rules {
		fmt.Printf("  %s %d\n", padColumn(rule, width), counts[rule])
	}
	fmt.Println()
}

// displayPendingField returns s unchanged when every rune is printable, and
// quoted and escaped otherwise. --list reads the pending-updates file without
// loading packages.toml, so nothing upstream has checked what it prints: an
// apply error carries text from outside, and an entry recorded before the
// packages.toml key check may carry a hostile key.
func displayPendingField(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0 {
		return s
	}
	return strconv.Quote(s)
}

// displayPendingUpdates formats and displays pending updates
func displayPendingUpdates(updates []autoupdate.PendingUpdate) {
	if len(updates) == 0 {
		logger.Info("No pending updates")
		return
	}

	fmt.Println()
	output.Header.Println("Pending Updates")
	fmt.Println()

	for _, u := range updates {
		statusColor := getStatusColor(u.Status)
		statusStr := output.Sprintf(statusColor, "[%s]", displayPendingField(string(u.Status)))

		output.Package.Printf("  %s\n", displayPendingField(u.Package))
		fmt.Printf("    Version: %s → %s\n", displayPendingField(u.CurrentVersion), displayPendingField(u.NewVersion))
		fmt.Printf("    Status:  %s\n", statusStr)
		if u.Error != "" {
			output.Error.Printf("    Error:   %s\n", displayPendingField(u.Error))
		}
		fmt.Printf("    Detected: %s\n", u.DetectedAt.Format("2006-01-02 15:04:05"))
		fmt.Println()
	}

	output.Info.Printf("Total: %d pending update(s)\n", len(updates))
	output.Info.Println("Use 'bentoo overlay autoupdate --apply <package>' to apply an update")
	output.Info.Println("Or 'bentoo overlay autoupdate --apply all' to apply every pending update")
}

// getStatusColor returns the appropriate color for an update status
func getStatusColor(status autoupdate.UpdateStatus) *color.Color {
	switch status {
	case autoupdate.StatusPending:
		return output.Warning
	case autoupdate.StatusValidated:
		return output.Success
	case autoupdate.StatusFailed:
		return output.Error
	case autoupdate.StatusApplied:
		return output.Info
	default:
		return output.Dim
	}
}

// loadPackagesConfigForApply loads the overlay's packages.toml so the applier
// can honour any [meta] authenticated-fetch instructions. It is best-effort: a
// missing or unparseable config is not fatal to --apply (only serial-gated
// packages need it), so it logs a debug note and returns nil, leaving the
// normal pkgdev-from-SRC_URI path intact for every package.
func loadPackagesConfigForApply(overlayPath string) *autoupdate.PackagesConfig {
	cfg, err := autoupdate.LoadPackagesConfig(overlayPath)
	if err != nil {
		logger.Debug("apply: no usable packages.toml (%v); authenticated fetch disabled", err)
		return nil
	}
	return cfg
}

// applierFixerOption builds the optional LLM manifest-fixer option for --apply.
// The fixer is wired automatically whenever the configured provider supports
// agentic file editing (claude-code); for every other provider it is a no-op
// (WithApplierFixer(nil) is ignored). A configured-but-unconstructable fixer
// (e.g. the `claude` CLI is absent) is logged as a Warn and --apply proceeds with
// its original fail-fast manifest behaviour.
//
// The fixer needs no context of its own here: Apply threads the context it is
// given into FixManifest, so a SIGINT/SIGTERM already cancels an in-flight agent
// process.
func applierFixerOption(llmCfg config.LLMConfig) autoupdate.ApplierOption {
	fixer, err := newConfiguredManifestFixer(llmCfg)
	if err != nil {
		logger.Warn("LLM manifest fixer unavailable; --apply will not auto-fix failed manifests: %v", err)
		return autoupdate.WithApplierFixer(nil)
	}
	return autoupdate.WithApplierFixer(fixer)
}

// applierGentooPathOption carries the ::gentoo tree into every Applier this
// command builds, so the bump can re-read the copy it is about to rename past.
//
// One helper rather than four copies for the same reason applierDistfileOptions
// is one: a mode that silently missed it would go back to renaming our ebuild
// forward with nothing watching, which is precisely the failure this option
// exists to catch and which looks like working software.
//
// BENTOO_GENTOO_REPO overrides the path; empty disables the check. The default
// is where portage puts it, so the common case needs no configuration - a check
// nobody has to switch on is a check that is actually on.
func applierGentooPathOption() autoupdate.ApplierOption {
	return autoupdate.WithApplierGentooPath(gentooRepoPath())
}

// gentooRepoPath is the ::gentoo tree: BENTOO_GENTOO_REPO, else where Portage
// puts it. The check report reads it too, to tell a required version that
// ::gentoo already ships from one nothing provides.
func gentooRepoPath() string {
	if path := os.Getenv("BENTOO_GENTOO_REPO"); path != "" {
		return path
	}
	return "/var/db/repos/gentoo"
}

// applierDistfileOptions carries the resolved distfile directories into every
// Applier this command builds, so --apply, --apply all and --revive all reach
// the Manifest step with the same two directories (S030-R1.3). One helper rather
// than three copies of the same two lines: a mode that silently missed them
// would fall back to the host's DISTDIR with no cache, which looks like working
// software.
func (ar *autoupdateRun) applierDistfileOptions() []autoupdate.ApplierOption {
	return []autoupdate.ApplierOption{
		autoupdate.WithApplierDistdir(ar.dirs.Distdir, ar.dirs.ConfiguredDistdir),
		autoupdate.WithApplierDistfilesCache(ar.dirs.Cache),
	}
}

// resolveAutoupdateValidatePolicy translates `autoupdate.validate` and the
// --depth flag into what the Applier reads (S033-R2, R2.2, R2.4, R2.7, R6.6,
// R3.13).
//
// # Why the translation lives here and not in validate
//
// internal/autoupdate/validate deliberately knows nothing about the config
// package — the import already runs the other way — so the depth table crosses
// as validate's own types and the STRINGS are turned into rungs here, by
// validate.ParseDepth, which rejects a typo by name. Doing it anywhere else would
// put the ladder's vocabulary in two places and the copy would be the stale one.
//
// # An unusable override is reported, never fatal
//
// A per-package override with no reason, or with a depth that names no rung, is
// logged and DROPPED, which leaves that package at its class depth — the safe
// direction, since an override is the one input that can quietly reduce how much
// a bump is checked. One bad entry must not cost the operator the rest of the
// file, which is the treatment an unknown config key already gets.
//
// Only --depth is fatal: it is this invocation's explicit instruction, and
// running at some other depth than the one that was typed is a different run.
func (o *autoupdateOptions) resolveAutoupdateValidatePolicy(cfg *config.Config, cmd *cobra.Command) (autoupdateValidatePolicy, error) {
	validateCfg := &cfg.Autoupdate.Validate

	policy := autoupdateValidatePolicy{
		Policy: validate.DepthPolicy{
			ByClass:   map[validate.Class]validate.Depth{},
			Overrides: map[string]validate.DepthOverride{},
		},
		// --require-isolation stays a flag OR the key: story 031's flag was the
		// only way in until story 033 gave the setting a home, and neither
		// supersedes the other.
		RequireIsolation: o.requireIsolation || validateCfg.GetRequireIsolation(),
		RequireProof:     validateCfg.GetRequireProof(),
	}

	for class, name := range map[validate.Class]string{
		validate.ClassRevision: "revision",
		validate.ClassPatch:    "patch",
		validate.ClassSeries:   "series",
		validate.ClassMajor:    "major",
	} {
		// GetDepthForClass returns the configured value VERBATIM and never the
		// empty string, so a typo reaches ParseDepth and is rejected by name here
		// rather than silently repaired in the config layer.
		spelled := validateCfg.GetDepthForClass(name)
		depth, err := validate.ParseDepth(spelled)
		if err != nil {
			logger.Warn("autoupdate.validate.depths.%s: %v; that class keeps its shipped default instead", name, err)
			continue
		}
		policy.Policy.ByClass[class] = depth
	}

	for _, err := range validateCfg.OverrideErrors() {
		logger.Warn("%v", err)
	}
	for pkg, override := range validateCfg.Packages {
		if strings.TrimSpace(override.Reason) == "" {
			continue // already reported by OverrideErrors above
		}
		depth, err := validate.ParseDepth(override.Depth)
		if err != nil {
			logger.Warn("autoupdate.validate.packages.%s: %v; the override is ignored and %s keeps its class depth", pkg, err, pkg)
			continue
		}
		policy.Policy.Overrides[pkg] = validate.DepthOverride{Depth: depth, Reason: override.Reason}
	}

	spelled, err := cmd.Flags().GetString("depth")
	if err != nil {
		return policy, fmt.Errorf("reading --depth: %w", err)
	}
	if spelled == "" {
		return policy, nil
	}
	depth, err := validate.ParseDepth(spelled)
	if err != nil {
		return policy, fmt.Errorf("--depth: %w", err)
	}
	policy.Depth = &depth
	return policy, nil
}

// applierValidateOptions carries the resolved validation policy into every
// Applier this command builds, so --apply, --apply all and --revive all reach the
// gates with the same depth table, the same staging root and the same two
// switches (S033-12.1).
//
// One helper rather than three copies of the same six lines: a mode that silently
// missed them would apply bumps with no gate at all, which — unlike a missing
// distdir — looks exactly like success.
//
// The staging root is <configDir>/staging (S033-D1). It is what turns the whole
// staged pipeline on: without it the candidate is written straight into the
// published overlay and no gate runs, which is every release before this one.
func (ar *autoupdateRun) applierValidateOptions(configDir string) []autoupdate.ApplierOption {
	opts := []autoupdate.ApplierOption{
		autoupdate.WithApplierStagingRoot(filepath.Join(configDir, stagingDirName)),
		autoupdate.WithApplierValidatePolicy(ar.validate.Policy),
		autoupdate.WithApplierRequireIsolation(ar.validate.RequireIsolation),
		autoupdate.WithApplierRequireProof(ar.validate.RequireProof),
	}
	if ar.validate.Depth != nil {
		opts = append(opts, autoupdate.WithApplierDepth(*ar.validate.Depth))
	}
	return opts
}

// stagingDirName is the ONE spelling of the staged-tree directory under the
// autoupdate config dir (S033-D1).
//
// Both entry points join it — `--apply` through applierValidateOptions and
// `overlay validate --depth` through autoupdateStagingRoot — because the path is
// where the retention and reuse rules are recorded (R3.7, R10.1). Two spellings
// would put the two commands' staged trees in different places, and a tree
// proved by one would silently never be found by the other.
const stagingDirName = "staging"

// autoupdateConfigDir is where this command keeps its state: the pending list,
// the retained logs and the staged trees.
//
// It is a function rather than four copies of the same filepath.Join so that
// `overlay validate --depth` and `overlay autoupdate --apply` cannot come to
// disagree about which directory that is.
func autoupdateConfigDir() (string, error) {
	dir, err := config.AutoupdateDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve the autoupdate state directory: %w", err)
	}
	return dir, nil
}

// autoupdateStagingRoot is the directory staged trees are prepared under.
//
// It is deliberately NOT under the overlay and NOT under os.TempDir() (S033-D1):
// a tree under the overlay is an unclaimed ebuild that `--clean` deletes and
// `overlay validate` reports, and a tree under /tmp does not survive the run,
// which R3.6 and R3.7 require so a failure can still be inspected afterwards.
func autoupdateStagingRoot() (string, error) {
	dir, err := autoupdateConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, stagingDirName), nil
}

// runApply handles the --apply flag. ctx is passed to Apply so a SIGINT/SIGTERM
// cancels the in-flight `pkgdev manifest`
// or compile child process within ~2 s (R1.1, R1.2). The existing orphan
// rollback path then removes the half-applied .ebuild (R1.3).
func (ar *autoupdateRun) runApply(ctx context.Context, overlayPath, configDir, pkg string, llmCfg config.LLMConfig) error {
	// Derive a cancelable apply context from the signal-aware ctx so the TUI's
	// Ctrl-C (which invokes cancel) cancels the in-flight child Apply runs
	// under it and triggers the existing orphan rollback (R5.1/R5.2).
	applyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// buildApplyReporter wires the reporter into extra (WithApplierReporter), so
	// the reporter value itself is not needed at this call site.
	_, extra, finish := ar.buildApplyReporter(applyCtx, cancel, 1)
	// Every early return closes the batch and restores the terminal through
	// this; finish is idempotent (func buildApplyReporter), so the explicit
	// call before the summary below is the one that does the work there.
	defer finish()

	opts := []autoupdate.ApplierOption{
		autoupdate.WithApplierClean(ar.opts.clean),
		autoupdate.WithApplierPackagesConfig(loadPackagesConfigForApply(overlayPath)),
		applierFixerOption(llmCfg),
	}
	opts = append(opts, applierGentooPathOption())
	opts = append(opts, ar.applierDistfileOptions()...)
	opts = append(opts, ar.applierValidateOptions(configDir)...)
	opts = append(opts, applierLLMOptions(ar.opts.llm, llmCfg, ar.validateCfg)...)
	opts = append(opts, extra...)

	applier, err := autoupdate.NewApplier(overlayPath, configDir, opts...)
	if err != nil {
		return failWith(1, fmt.Errorf("failed to initialize applier: %w", err))
	}

	// The applier's TaskStart now surfaces "applying <pkg>" through the reporter
	// (the plain backend prints a START line; the TUI shows the task), so the
	// previous output.Info Printf is intentionally gone.

	result, err := applier.Apply(applyCtx, pkg, ar.opts.compile)

	// Stop the TUI and restore the terminal BEFORE the summary so the inline run
	// history stays in scrollback and displayApplyResult prints to a clean line.
	finish()

	if err != nil {
		ar.displayApplyResult(result)
		return exitWith(1)
	}

	ar.displayApplyResult(result)
	return nil
}

// runApplyAll handles `--apply all`: it applies every pending update, reusing a
// single Applier so the pending list and logs directory are loaded once. ctx
// bounds every Apply so a SIGINT/SIGTERM cancels the in-flight `pkgdev manifest`
// or compile child process (R1.1, R1.2).
//
// The package list is snapshotted up front: Apply mutates the underlying
// pending list (a successful apply deletes its entry), so iterating over the
// live map would be unsafe. Each Apply is independent — a failure on one
// package never aborts the others — and the process exits non-zero when any
// package failed, matching the single-package --apply contract.
//
// Without --compile the applies run concurrently across a worker pool bounded by
// --concurrency, so the slow, network-bound `pkgdev manifest` step of each
// package overlaps instead of running one at a time. With --compile they stay
// serial so the elevated compile step's confirmation prompt and sudo invocation
// are not interleaved. Both paths live in (*autoupdate.Applier).ApplyAll.
func (ar *autoupdateRun) runApplyAll(ctx context.Context, overlayPath, configDir string, llmCfg config.LLMConfig) error {
	// Read the pending list up front so the reporter's batch denominator (and the
	// "nothing to do" short-circuit) are known before the TUI program starts. The
	// applier built below loads the same pending.json, and Apply mutates it as it
	// goes, so this snapshot is the count we iterate over (mirrors the existing
	// snapshot rationale).
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		return failWith(1, fmt.Errorf("failed to load pending list: %w", err))
	}
	updates := pending.List()
	if len(updates) == 0 {
		logger.Info("No pending updates to apply")
		return nil
	}

	// Derive a cancelable apply context from the signal-aware ctx so the TUI's
	// Ctrl-C (which invokes cancel) cancels the in-flight child Apply runs
	// under it and triggers the existing orphan rollback (R5.1/R5.2).
	applyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	_, extra, finish := ar.buildApplyReporter(applyCtx, cancel, len(updates))
	// Every early return closes the batch and restores the terminal through
	// this; finish is idempotent (func buildApplyReporter), so the explicit
	// call before the batch summary below is the one that does the work there.
	defer finish()

	opts := []autoupdate.ApplierOption{
		autoupdate.WithApplierClean(ar.opts.clean),
		autoupdate.WithApplierPackagesConfig(loadPackagesConfigForApply(overlayPath)),
		// Reuse the pending list already loaded so the applier and this snapshot
		// share one in-memory source of truth.
		autoupdate.WithApplierPendingList(pending),
		applierFixerOption(llmCfg),
	}
	opts = append(opts, applierGentooPathOption())
	opts = append(opts, ar.applierDistfileOptions()...)
	opts = append(opts, ar.applierValidateOptions(configDir)...)
	// One Applier serves the whole batch, so the two agents are constructed ONCE
	// here — a per-package construction would warn once per package on a host with
	// no claude CLI, and pay the PATH lookup as many times.
	opts = append(opts, applierLLMOptions(ar.opts.llm, llmCfg, ar.validateCfg)...)
	opts = append(opts, extra...)

	applier, err := autoupdate.NewApplier(overlayPath, configDir, opts...)
	if err != nil {
		return failWith(1, fmt.Errorf("failed to initialize applier: %w", err))
	}

	// The applier's TaskStart surfaces each package through the reporter, so the
	// previous output.Info Printf per package is intentionally gone.
	results, failures := applier.ApplyAll(applyCtx, updates, ar.opts.compile, ar.opts.concurrency)

	// Stop the TUI and restore the terminal BEFORE the summary so the inline run
	// history stays in scrollback and displayApplyAllResults prints cleanly.
	finish()

	ar.displayApplyAllResults(results, failures)

	if failures > 0 {
		return exitWith(1)
	}
	return nil
}

// displayApplyAllResults renders the per-package outcomes of `--apply all`
// followed by an aggregate summary line.
func (ar *autoupdateRun) displayApplyAllResults(results []*autoupdate.ApplyResult, failures int) {
	for _, result := range results {
		ar.displayApplyResult(result)
	}

	applied, obsolete, held, waiting := 0, 0, 0, 0
	for _, r := range results {
		switch {
		case r == nil:
		case r.Obsolete:
			obsolete++
		case len(r.Waiting) > 0:
			waiting++
		case r.Held:
			held++
		case r.Success:
			applied++
		}
	}

	fmt.Println()
	output.Header.Println("Apply All Summary")
	output.Success.Printf("  Applied:  %d\n", applied)
	if obsolete > 0 {
		output.Warning.Printf("  Obsolete: %d (pruned from pending)\n", obsolete)
	}
	if held > 0 {
		output.Warning.Printf("  Held:     %d (hold = true; kept in pending)\n", held)
	}
	if waiting > 0 {
		output.Warning.Printf("  Waiting:  %d (required version not available yet; kept in pending)\n", waiting)
	}
	if failures > 0 {
		output.Error.Printf("  Failed:   %d\n", failures)
	}
	if applied > 0 {
		output.Info.Println("Don't forget to commit the changes with 'bentoo overlay commit'")
	}
}

// displayApplyResult formats and displays a single apply outcome.
// It is a no-op when result is nil. Otherwise it prints the package and
// version transition, then reports status (obsolete, held, success, or failure)
// plus any available details such as obsolete reason, LLM fix/QA summary,
// cleaned old-version info/warnings, and — on a failure — the log path and the
// staged tree the failed bump left behind.
func (ar *autoupdateRun) displayApplyResult(result *autoupdate.ApplyResult) {
	if result == nil {
		return
	}

	fmt.Println()
	output.Header.Println("Apply Result")
	fmt.Println()

	output.Package.Printf("  %s\n", result.Package)
	fmt.Printf("    Version: %s → %s\n", result.OldVersion, result.NewVersion)

	if result.Obsolete {
		output.Warning.Println("    Status:  Obsolete (pruned from pending)")
		if result.ObsoleteReason != "" {
			output.Info.Printf("    Reason:  %s\n", result.ObsoleteReason)
		}
		return
	}

	if len(result.Waiting) > 0 {
		output.Warning.Println("    Status:  Waiting (kept in pending)")
		for _, w := range result.Waiting {
			output.Info.Printf("    Waiting: %s\n", w)
		}
		return
	}

	if result.Held {
		output.Warning.Printf("    Status:  Held (%s; kept in pending)\n", result.HoldReason)
		output.Info.Printf("    Reason:  bumped by hand — drop %q in packages.toml to automate it\n", result.HoldReason)
		return
	}

	if result.Success {
		output.Success.Println("    Status:  Success")
		ar.displayCompileIsolation(result)
		if result.Fixed {
			output.Warning.Printf("    Fixed:   manifest repaired by LLM — %s\n", result.FixSummary)
		}
		if result.QASummary != "" {
			output.Warning.Printf("    QA:      pkgcheck findings after the fix — review before committing:\n%s\n", result.QASummary)
		}
		displayCleanReport(result)
		if result.CleanWarning != "" {
			// R6.2's blocked-entry line arrives here and nowhere else: a plan
			// blocked by a pinless entry is returned as an error naming that
			// entry, which Apply stores in CleanWarning. ApplyResult carries no
			// sweepPlan and therefore no Blocked field, so this IS the line —
			// printing a second one would repeat the same entry name twice.
			output.Warning.Printf("    Clean:   %s\n", result.CleanWarning)
		}
		if result.RegistryWarning != "" {
			// Its own label, deliberately not "Clean:". The pin is written on
			// every successful apply while the sweep runs only under --clean, so
			// filing a registry failure under the clean step would blame a step
			// that may not even have run.
			output.Warning.Printf("    Registry: %s\n", result.RegistryWarning)
		}
		if result.MetadataCacheWarning != "" {
			output.Warning.Printf("    Cache:   %s\n", result.MetadataCacheWarning)
		}
		output.Success.Println("\n✓ Update applied successfully")
		output.Info.Println("Don't forget to commit the changes with 'bentoo overlay commit'")
	} else {
		output.Error.Println("    Status:  Failed")
		if result.Error != nil {
			output.Error.Printf("    Error:   %v\n", result.Error)
		}
		if result.LogPath != "" {
			output.Info.Printf("    Log:     %s\n", result.LogPath)
		}
		if result.StagedPath != "" {
			// S033-R3.6's second half, at the operator's end of it: the bump was
			// validated in a tree of its own outside the overlay, that tree is kept
			// when the bump is not promoted, and this line is what makes it findable.
			// Without it the tree still survives, but the only way to see the ebuild
			// the gates actually read is to run the whole bump again.
			//
			// Failure branch only, and that is not merely where it happens to sit: a
			// promoted bump clears StagedPath precisely so no success ever points at
			// a tree whose bytes are already in the overlay.
			output.Info.Printf("    Staged:  %s\n", result.StagedPath)
			output.Info.Println("             (the tree the gates read — inspect it, or re-run a gate there by hand)")
		}
	}
}

// displayCompileIsolation states how much the compile gate actually verified.
//
// It prints nothing when no compile ran — there is no fidelity to report about
// a step that did not happen — and nothing extra when the namespace was
// verified, since a plain "Success" already means what it says (R7.2).
//
// The two lines it does print exist because the gate used to claim more than it
// had. Portage reports network-sandbox in FEATURES whether or not the namespace
// was created, creating one needs privilege an ordinary user does not have, and
// Portage warns about neither. So a green could mean "built with the network
// cut off" or "built with full network access", and nothing distinguished them
// (R7.3). The skip line is the same honesty one step further: with
// --require-isolation the compile did not run, and saying "Success" without
// saying that would be the same lie in a new place (R7.4).
func (ar *autoupdateRun) displayCompileIsolation(result *autoupdate.ApplyResult) {
	if !ar.opts.compile || result.IsolationVerified || result.IsolationReason == "" {
		return
	}
	if ar.opts.requireIsolation {
		output.Warning.Println("    Compile: SKIPPED (--require-isolation, and no network namespace)")
	} else {
		output.Warning.Println("    Compile: PASS (unverified isolation)")
	}
	output.Info.Printf("    Reason:  %s\n", result.IsolationReason)
}

// displayCleanReport prints what the --clean sweep did to the package
// directory: every version it removed, and every version it kept together with
// the registry entry that claims it (R6.1).
//
// Naming the claiming entry is the point. "Kept 1.28.5" invites the question
// the report exists to answer — kept by whom, and may I delete it? — while
// "kept 1.28.5, claimed by media-plugins/gst-plugins-vpx@stable" says which
// record to edit if that is wrong. An empty claimant is not an omission: it
// means a RULE kept the file rather than an entry (a live -9999 ebuild, which no
// pin can ever name, or the R4.3 floor that refuses to leave a directory with no
// release at all), and saying which rule is what stops it reading as a bug.
//
// It prints nothing when --clean did not run, since every field is then empty.
func displayCleanReport(result *autoupdate.ApplyResult) {
	pkgName := filepath.Base(result.Package)

	switch {
	case len(result.CleanRemoved) > 1:
		// The legacy one-version line below under-reports a multi-file sweep,
		// and under-reporting a deletion is the one direction this report must
		// not fail in. List every ebuild that went away.
		names := make([]string, 0, len(result.CleanRemoved))
		for _, v := range result.CleanRemoved {
			names = append(names, fmt.Sprintf("%s-%s.ebuild", pkgName, v))
		}
		fmt.Printf("    Removed: %s (%d ebuilds)\n", strings.Join(names, ", "), len(names))
	case result.CleanedOldVersion != "":
		// The single-removal case keeps its original wording verbatim: it is by
		// far the common one and nothing about it changed.
		fmt.Printf("    Removed: %s-%s.ebuild (old version)\n", pkgName, result.CleanedOldVersion)
	}

	if len(result.CleanKept) == 0 {
		return
	}
	// Gentoo version order, the same order the sweep reports its removals in, so
	// the two lists about one directory never disagree about what "oldest" means.
	kept := make([]string, 0, len(result.CleanKept))
	for v := range result.CleanKept {
		kept = append(kept, v)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if c := ebuild.CompareVersions(kept[i], kept[j]); c != 0 {
			return c < 0
		}
		// Two versions the comparison calls equal ("1.0" and "1.0-r0") are still
		// two files; order them by text so the report is total and stable.
		return kept[i] < kept[j]
	})
	fmt.Println("    Kept:")
	for _, v := range kept {
		if key := result.CleanKept[v]; key != "" {
			fmt.Printf("      %s-%s.ebuild — claimed by %s\n", pkgName, v, key)
		} else {
			fmt.Printf("      %s-%s.ebuild — kept by rule (live ebuild, or the last release standing)\n", pkgName, v)
		}
	}
}

// reviveCheckerOptions builds the Checker option set shared by the revive modes.
// It mirrors runCheck's option set exactly — config dir, concurrency,
// type filter, tuned rate limiter, cache TTL, fetch-body sharing, and the same
// LLM wiring (with the err-first nil guard) — so a revived package's upstream
// check behaves identically to a normal --check. The GitHub token is not an
// option: NewChecker resolves it itself from GITHUB_TOKEN/GH_TOKEN via the
// secrets chain. The progress callback is omitted: the revive paths drive
// single-package CheckPackage calls, which never fire it.
func (ar *autoupdateRun) reviveCheckerOptions(configDir string, cacheTTL, httpTimeout time.Duration, llmCfg config.LLMConfig) []autoupdate.CheckerOption {
	opts := []autoupdate.CheckerOption{
		autoupdate.WithConfigDir(configDir),
		autoupdate.WithConcurrency(ar.opts.concurrency),
		autoupdate.WithTypeFilter(ar.opts.only),
		autoupdate.WithHTTPRequestTimeout(httpTimeout),
		autoupdate.WithRateLimiter(autoupdate.NewRateLimiter(autoupdate.WithTunedHostPolicies())),
		// Same escape hatch as runCheck (S024-R7.1, R7.2). Setting it HERE is what
		// covers all three revive Checkers at once — both listing paths and the
		// apply path build their options through this helper — so the flag cannot
		// be honoured on --check and silently ignored on a revive.
		autoupdate.WithFetchCache(!ar.opts.noFetchCache),
	}
	if cacheTTL > 0 {
		opts = append(opts, autoupdate.WithCacheTTL(cacheTTL))
	}

	// Same err-first nil guard as runCheck: a failed constructor boxes a nil
	// concrete pointer into a NON-nil interface, so wire WithLLMClient only on
	// err==nil AND p!=nil. On failure Warn and continue (revive still runs,
	// skipping LLM extraction). WithLLMProviderConfigured suppresses the Checker's
	// "unused llm_prompt" Warn when a provider was requested.
	if p, err := newConfiguredLLMProvider(llmCfg); err != nil {
		logger.Warn("LLM provider %q unavailable; revive will skip LLM version extraction: %v", llmCfg.Provider, err)
	} else if p != nil {
		opts = append(opts, autoupdate.WithLLMClient(p))
	}
	opts = append(opts, autoupdate.WithLLMProviderConfigured(llmCfg.Provider != ""))

	return opts
}

// resolveGentooProvider resolves the ::gentoo provider the revive flow seeds
// from, mirroring `overlay compare`'s provider-resolution idiom: config repos >
// registry, with the GitHub token resolved from GITHUB_TOKEN/GH_TOKEN via the
// secrets chain (github.ResolveToken). forceClone is false so a user-configured
// local/clone repo is honoured; an API-only gentoo simply will not implement
// provider.PackageDirProvider, which runRevive detects and reports. The caller
// owns prov.Close() and decides whether a resolution error is fatal
// (runRevive/runReviveList exit non-zero; the --revivable add-on to --check only
// warns and skips the report).
func resolveGentooProvider(ctx context.Context, cfg *config.Config) (provider.Provider, error) {
	configRepos := convertConfigRepos(cfg)

	registry, err := provider.NewRepositoryRegistry()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize repository registry: %w", err)
	}

	repoInfo, err := provider.ResolveRepository(ctx, "gentoo", configRepos, registry)
	if err != nil {
		return nil, fmt.Errorf("repository 'gentoo' not found: %w", err)
	}

	// Resolve the GitHub token from GITHUB_TOKEN/GH_TOKEN via the secrets chain
	// (github.ResolveToken); a resolution error warns and continues with
	// unauthenticated access. Only fill an empty repo token so a per-repo one
	// (BENTOO_REPO_<NAME>_TOKEN, resolved by convertConfigRepos) still wins.
	token, err := github.ResolveToken()
	if err != nil {
		logger.Warn("resolving GitHub token: %v; continuing with unauthenticated GitHub API access", err)
	}
	if token != "" && repoInfo.Token == "" {
		repoInfo.Token = token
	}

	// forceClone=false: honour the resolved provider type so a configured local
	// git repo (the path that yields PackageDirProvider) is used as-is.
	prov, err := provider.NewProvider(repoInfo, false)
	if err != nil {
		return nil, fmt.Errorf("failed to create gentoo provider: %w", err)
	}
	return prov, nil
}

// runReviveList handles --revive-list: a passive report of disabled (orphaned)
// packages.toml entries whose upstream release is strictly newer than the highest
// version ::gentoo still carries. It mutates nothing — it only builds a Checker
// (the same option set as --check) and the ::gentoo provider, then prints the
// candidates FindRevivableOrphans returns as a PACKAGE | GENTOO | UPSTREAM table.
func (ar *autoupdateRun) runReviveList(ctx context.Context, overlayPath, configDir string, cacheTTL time.Duration, cfg *config.Config, llmCfg config.LLMConfig) error {
	checker, err := autoupdate.NewChecker(overlayPath, ar.reviveCheckerOptions(configDir, cacheTTL, ar.resolveHTTPTimeout(cfg), llmCfg)...)
	if err != nil {
		return failWith(1, fmt.Errorf("failed to initialize checker: %w", err))
	}

	prov, err := ar.deps.resolveGentooProvider(ctx, cfg)
	if err != nil {
		return failWith(1, err)
	}
	defer prov.Close() //nolint:errcheck // every provider Close is a no-op that returns nil; there is nothing to act on

	// FindRevivableOrphans threads ctx into every upstream and ::gentoo lookup. Soft per-package errors are returned
	// alongside the candidates, so a partial scan still reports what it found.
	candidates, err := checker.FindRevivableOrphans(ctx, prov)
	if err != nil {
		logger.Warn("revive scan completed with soft errors: %v", err)
	}

	displayReviveCandidates(candidates)
	return nil
}

// displayReviveCandidates renders the revivable-orphan report as a fixed-width
// PACKAGE | GENTOO | UPSTREAM table, reusing truncatePkgName for column
// alignment (as `overlay compare` does). An empty set prints a "nothing to
// revive" note instead of an empty table.
func displayReviveCandidates(candidates []autoupdate.ReviveCandidate) {
	if len(candidates) == 0 {
		output.Success.Println("Nothing to revive — no orphaned package has an upstream newer than ::gentoo")
		return
	}

	fmt.Println()
	output.Header.Println("Revivable Orphans")
	fmt.Println()

	output.Dim.Printf("  %s %s %s\n",
		truncatePkgName("PACKAGE", 40), truncatePkgName("GENTOO", 16), "UPSTREAM")
	for _, c := range candidates {
		output.Package.Printf("  %s ", truncatePkgName(c.Package, 40))
		fmt.Printf("%s %s\n", truncatePkgName(c.GentooVersion, 16), c.UpstreamVersion)
	}

	fmt.Println()
	output.Info.Printf("Found %d revivable orphan(s)\n", len(candidates))
	output.Info.Println("Use 'bentoo overlay autoupdate --revive <package>' to revive one, or '--revive all' for every candidate")
}

// reviveApplierOptions is the option set the --revive Applier is built with.
// It is a method so the revive wiring can be tested as it ships.
func (ar *autoupdateRun) reviveApplierOptions(overlayPath, configDir string, pending *autoupdate.PendingList) []autoupdate.ApplierOption {
	reviveOpts := []autoupdate.ApplierOption{
		autoupdate.WithApplierClean(ar.opts.clean),
		autoupdate.WithApplierPackagesConfig(loadPackagesConfigForApply(overlayPath)),
		autoupdate.WithApplierPendingList(pending),
	}
	reviveOpts = append(reviveOpts, ar.applierDistfileOptions()...)
	// The ::gentoo tree too, as on the apply paths: a revived package whose
	// `requires` is met only by ::gentoo would otherwise always read as waiting.
	reviveOpts = append(reviveOpts, applierGentooPathOption())
	// R3 reaches the revive path through the same option block as the two apply
	// paths, which is what keeps a second entry point from growing a second,
	// gate-free way into the published overlay.
	reviveOpts = append(reviveOpts, ar.applierValidateOptions(configDir)...)

	return reviveOpts
}

// runRevive handles --revive <pkg|all>: it resurrects each target orphan by
// seeding the current ::gentoo ebuild into the overlay, re-enabling the entry,
// and bumping it to the upstream version via the normal CheckPackage+Apply flow.
//
// The ::gentoo provider must expose an on-disk package directory
// (provider.PackageDirProvider); an API-only gentoo cannot seed a base ebuild, so
// that case aborts ONCE up front with a clear, actionable error. Each package is
// independent: a failure on one never aborts the others; outcomes are accumulated
// and the process exits non-zero when any package failed.
func (ar *autoupdateRun) runRevive(ctx context.Context, overlayPath, configDir, target string, cacheTTL time.Duration, cfg *config.Config, llmCfg config.LLMConfig) error {
	prov, err := ar.deps.resolveGentooProvider(ctx, cfg)
	if err != nil {
		return failWith(1, err)
	}
	defer prov.Close() //nolint:errcheck // every provider Close is a no-op that returns nil; there is nothing to act on

	// The revive seed copies the ::gentoo package dir off disk; an API-only
	// provider cannot do that. Detect it ONCE, before the checker, the orphan scan
	// and the applier, and bail with an actionable hint (mirrors `overlay
	// compare`'s local-repo guidance). Kept ahead of `--revive all`'s orphan scan
	// so its "Nothing to revive" exit 0 can never mask the hint and exit 1.
	if err := autoupdate.CanRevive(prov); err != nil {
		logger.Error("the resolved gentoo provider has no local package directory; revive needs an on-disk ::gentoo tree.")
		logger.Info("Configure a local gentoo repository in ~/.config/bentoo/config.yaml:")
		logger.Info("  repositories:")
		logger.Info("    gentoo:")
		logger.Info("      provider: local")
		logger.Info("      path: /var/db/repos/gentoo")
		logger.Info("(or force a clone-backed provider so the package tree is available on disk)")
		return exitWith(1)
	}

	// Build the initial Checker (shared option set) to resolve the target list.
	httpTimeout := ar.resolveHTTPTimeout(cfg)
	checker, err := autoupdate.NewChecker(overlayPath, ar.reviveCheckerOptions(configDir, cacheTTL, httpTimeout, llmCfg)...)
	if err != nil {
		return failWith(1, fmt.Errorf("failed to initialize checker: %w", err))
	}

	// Resolve the target package list: an explicit "category/pkg", or "all"
	// (every candidate FindRevivableOrphans reports).
	var targets []string
	if target == "all" {
		candidates, ferr := checker.FindRevivableOrphans(ctx, prov)
		if ferr != nil {
			logger.Warn("revive scan completed with soft errors: %v", ferr)
		}
		if len(candidates) == 0 {
			output.Success.Println("Nothing to revive — no orphaned package has an upstream newer than ::gentoo")
			return nil
		}
		for _, c := range candidates {
			targets = append(targets, c.Package)
		}
	} else {
		targets = []string{target}
	}

	// One shared pending list for the whole revive run. CheckPackage (which
	// writes the pending entry) and applier.Apply (which reads it) run in the
	// SAME process here — unlike the separate `--check` / `--apply` invocations
	// that each reload pending.json from disk. PendingList.Get reads its in-memory
	// map, so without a shared instance the applier (loaded before the check)
	// would never see the freshly-written entry and Apply would fail with
	// ErrPackageNotInPending. Injecting one instance into both makes the in-memory
	// state the single source of truth.
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		return failWith(1, fmt.Errorf("failed to initialize pending list: %w", err))
	}

	reviveOpts := ar.reviveApplierOptions(overlayPath, configDir, pending)

	applier, err := autoupdate.NewApplier(overlayPath, configDir, reviveOpts...)
	if err != nil {
		return failWith(1, fmt.Errorf("failed to initialize applier: %w", err))
	}

	// Each target is re-checked on a FRESH Checker so it loads the re-enabled
	// packages.toml; it shares the applier's pending list so the entry
	// CheckPackage writes is visible to Apply (same in-memory map, same process).
	newChecker := func() (*autoupdate.Checker, error) {
		return autoupdate.NewChecker(overlayPath,
			append(ar.reviveCheckerOptions(configDir, cacheTTL, httpTimeout, llmCfg), autoupdate.WithPendingList(pending))...)
	}
	reviver, err := autoupdate.NewReviver(overlayPath, applier, prov, newChecker,
		autoupdate.WithReviveCompile(ar.opts.compile))
	if err != nil {
		return failWith(1, err)
	}

	outcomes := make([]autoupdate.ReviveOutcome, 0, len(targets))
	for _, pkg := range targets {
		output.Info.Printf("Reviving %s...\n", pkg)
		outcomes = append(outcomes, reviver.Revive(ctx, pkg))
	}

	failures := displayReviveSummary(outcomes)
	if failures > 0 {
		return exitWith(1)
	}
	return nil
}

// displayReviveSummary prints per-package revive outcomes followed by an
// aggregate (revived / skipped / failed) and returns the failure count so the
// caller can set the exit code.
func displayReviveSummary(outcomes []autoupdate.ReviveOutcome) int {
	fmt.Println()
	output.Header.Println("Revive Summary")
	fmt.Println()

	var revived, skipped, failed int
	for _, o := range outcomes {
		switch o.Status {
		case autoupdate.ReviveRevived:
			revived++
			output.Success.Printf("  ✓ %s: %s\n", o.Package, o.Detail)
		case autoupdate.ReviveSkipped, autoupdate.ReviveWaiting:
			// A waiting revive is not a failure: the entry is re-enabled and its
			// bump stays pending until the required version exists.
			skipped++
			output.Warning.Printf("  - %s: %s\n", o.Package, o.Detail)
		default:
			failed++
			output.Error.Printf("  ✗ %s: %s\n", o.Package, o.Detail)
		}
	}

	fmt.Println()
	output.Success.Printf("  Revived: %d\n", revived)
	if skipped > 0 {
		output.Warning.Printf("  Skipped: %d\n", skipped)
	}
	if failed > 0 {
		output.Error.Printf("  Failed:  %d\n", failed)
	}
	if revived > 0 {
		output.Info.Println("Don't forget to commit the changes with 'bentoo overlay commit'")
	}

	return failed
}
