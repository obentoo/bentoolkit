package validate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrDependenciesUndetermined reports that this host could not be ASKED whether
// a candidate's build dependencies are satisfied — Portage is absent, the
// staged tree is not there, the resolve failed or timed out.
//
// IT IS A DIFFERENT ANSWER FROM "a dependency is missing", and the difference is
// the reason this sentinel exists. Both make the caller skip the build gates
// (R5.3, R6.2), but only one of them can name an atom to install. A caller that
// read a bare false as "something is missing" would print a skip blaming a
// dependency nobody has evidence for; errors.Is against this sentinel is how it
// tells the two apart, and it survives a reworded message the way a
// strings.Contains on the text would not.
var ErrDependenciesUndetermined = errors.New("whether the build dependencies are satisfied could not be determined")

// portageRepositoriesVar is the environment variable carrying a whole
// repository configuration as a VALUE. It is the mechanism that lets the staged
// tree be made visible without writing a repos.conf fragment anywhere: see
// DependenciesSatisfied.
const portageRepositoriesVar = "PORTAGE_REPOSITORIES"

// portageRepoConfigPaths are the two locations Portage reads repository
// configuration from when portageRepositoriesVar is unset, IN THE ORDER PORTAGE
// READS THEM — the distribution's defaults first, the host's own second, so the
// host's values win. They are portage.const's GLOBAL_CONFIG_PATH and
// USER_CONFIG_PATH; either may be a file or a directory of files.
var portageRepoConfigPaths = []string{
	"/usr/share/portage/config/repos.conf",
	"/etc/portage/repos.conf",
}

// installActions are the merge-list tags that mean "this package would be
// installed". Every other tag is deliberately ignored: `nomerge` names a package
// that is ALREADY installed (so it satisfies a dependency rather than missing
// one), and `uninstall` and `blocks` are not installs at all. Counting a nomerge
// as unsatisfied would skip a gate this host could perfectly well have run.
var installActions = map[string]bool{"ebuild": true, "binary": true}

// depsResolveTimeout bounds one pretend resolve.
//
// Generous on purpose, and for the opposite reason to a UI timeout: `emerge -p`
// on a cold metadata cache spends minutes regenerating it before it can answer
// anything, and a bound that fired there would report UNDETERMINED for a host
// that could in fact answer — quietly turning a slow machine into a machine
// whose bumps are never really validated. This exists for the resolve that never
// returns at all, not to keep the gate snappy.
const depsResolveTimeout = 5 * time.Minute

// BuildDeps is the set of process- and host-level seams the build gates run
// through, injected as functions so that every branch — the tool is missing, the
// resolve failed, the host cannot isolate — stays reachable in a test on a host
// where the real thing behaves the other way.
//
// # Why it is declared here
//
// design.md groups BuildDeps beside RunBuildGates in build.go, but
// DependenciesSatisfied is its FIRST consumer and lands first; declaring it
// there would leave this file naming a type that does not exist yet. All four
// seams are therefore present from the start even though this file reads only
// two, so that the compile gate arriving later widens no struct and rewrites no
// call site.
//
// # A nil field is not a bug
//
// Every field may be nil, and nil means "use the real thing". Each consumer
// normalises what it reads (see commandFactory and binaryLookup), so a caller
// that needs no substitution passes BuildDeps{} instead of assembling defaults
// it does not care about — and, more to the point, a nil seam can never reach a
// call site and panic there.
type BuildDeps struct {
	// ExecCommand builds a command whose output this package CAPTURES, in the
	// shape the rest of the repository already uses for an exec seam. It
	// defaults to the package-level execCommand of archive.go.
	ExecCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd

	// RunAttached executes a command and returns its combined output. It is the
	// seam the compile gate needs rather than this one: a long build is streamed
	// and the caller keeps the transcript, which is a different relationship with
	// the child process than "collect stdout and parse it".
	RunAttached func(cmd *exec.Cmd) ([]byte, error)

	// LookPath answers "is this tool installed at all". It is separate from
	// ExecCommand so the absent-binary branch stays reachable in a test on a host
	// that does have Portage — which is every host this is developed on. It
	// defaults to the package-level lookPath of pkgcheck.go.
	LookPath func(name string) (string, error)

	// IsolationProbe measures whether this process can create a network
	// namespace, returning the answer and, when it is no, the reason. It defaults
	// to ProbeIsolation and is consumed by the compile gate.
	IsolationProbe func() (bool, string)
}

// commandFactory is the command builder a call must use: the injected seam when
// there is one, otherwise this package's production seam.
func (d BuildDeps) commandFactory() func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	if d.ExecCommand != nil {
		return d.ExecCommand
	}
	return execCommand
}

// binaryLookup is the PATH lookup a call must use, normalised the same way and
// for the same reason as commandFactory.
func (d BuildDeps) binaryLookup() func(name string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath
	}
	return lookPath
}

// isolationProbe is the isolation measurement a build must use, normalised the
// same way again — and the reason it is a seam at all is that BOTH answers are
// unreachable on any single host: the maintainer's machine is denied a network
// namespace (design M-B), and a machine that is granted one can never exercise
// the label the other produces.
//
// Its default is validate.ProbeIsolation, the same probe story 031 measures the
// compile gate with, so the label a build gate prints and the label the applier
// prints cannot come to disagree about this host.
func (d BuildDeps) isolationProbe() func() (bool, string) {
	if d.IsolationProbe != nil {
		return d.IsolationProbe
	}
	return ProbeIsolation
}

// attachedRunner is the runner a build must use, normalised the same way again.
//
// Its default is exactly cmd.CombinedOutput, which is the applier's own default
// for the compile it already runs: the log a failed gate retains is therefore
// byte-identical to the one that shipped before this package existed. A caller
// that has to stream the child to a real terminal — a privileged build prompting
// for a password cannot have its stdout captured into a pipe — substitutes its
// own and hands back the transcript.
func (d BuildDeps) attachedRunner() func(cmd *exec.Cmd) ([]byte, error) {
	if d.RunAttached != nil {
		return d.RunAttached
	}
	return func(cmd *exec.Cmd) ([]byte, error) { return cmd.CombinedOutput() }
}

// DependenciesSatisfied asks Portage whether this host could build the staged
// candidate at all, and reports every package that would have to be installed
// first (R5, R6).
//
// # The false-positive class it removes
//
// `ebuild … configure` NEVER INSTALLS DEPENDENCIES. It runs the phase in front
// of it and fails if a header, a library or a build tool is not already on the
// host. So a bump whose build dependencies are simply absent here fails
// configure for a reason that has nothing to do with the bump, and without this
// pre-check the gate reports a confident FAILED about an ebuild that is fine.
// Over a whole-registry sweep that is not one wrong answer — it is a report
// nobody trusts. Asking Portage first separates "the bump is broken" from "this
// host cannot build it", which are the two things a maintainer must never have
// to guess between.
//
// The answer is Portage's own, read-only and deterministic, rather than a guess
// parsed out of a build log. Measured (design M-D):
//
//	$ PORTAGE_REPOSITORIES="…host config…
//	  [bentoolkit-staging-…]
//	  location = <stagedRoot>
//	  masters = gentoo" \
//	  emerge -p --quiet "=media-plugins/gst-plugins-qt6-1.29.2::bentoolkit-staging-…"
//	[ebuild  N    ] media-plugins/gst-plugins-qt6-1.29.2
//	EXIT=0
//
// Anything the pretend run lists BEYOND the package under test is a dependency
// this host does not have.
//
// # Three states, not two
//
//	err == nil, ok == true             — satisfied; the build gates may run.
//	err == nil, ok == false, missing   — determined and unsatisfied; the caller
//	                                     reports every build gate SKIPPED and
//	                                     NAMES these atoms (R5.3).
//	err != nil                         — UNDETERMINED; the caller still skips,
//	                                     but must not name a missing dependency,
//	                                     because it does not know of one (R6.2).
//
// ok alone cannot express that, which is why the error is not an afterthought
// here: it carries the third state. On any error missing is nil, so a caller
// cannot accidentally print an atom list assembled from a run that failed.
//
// # Why nothing is written anywhere
//
// `emerge` does not inherit `ebuild`'s auto-append of the tree it is pointed at,
// so the staged repository has to be made visible some other way — and the
// obvious wrong way is a temporary repos.conf fragment dropped into
// /etc/portage/repos.conf. That produces the same answer while needing root on
// most hosts, racing every other bentoolkit process, and surviving a killed run
// as a file that quietly changes the next resolve. PORTAGE_REPOSITORIES supplies
// the whole repository configuration as an environment VALUE instead, so this
// function creates no file, no directory and no temporary anything.
//
// The pretend run is a pretend run: `-p` never installs, and the atom is pinned
// to the exact version in the staged repository with an `=` and a `::` qualifier
// so no other version of the package can answer for it.
func DependenciesSatisfied(ctx context.Context, stagedRoot, atom, version string, deps BuildDeps) (ok bool, missing []string, err error) {
	unsatisfied, err := unsatisfiedDependencies(ctx, stagedRoot, atom, version, deps)
	if err != nil {
		// The sentinel is applied HERE, at the single boundary, rather than at
		// each return inside the body — the same shape, for the same reason, as
		// Stage and ErrStageUnpreparable. It leaves no site where a future
		// failure path can forget to say "undetermined", and the wrapped cause
		// keeps its own words for the operator who has to fix it.
		return false, nil, fmt.Errorf("%w: %w", ErrDependenciesUndetermined, err)
	}
	return len(unsatisfied) == 0, unsatisfied, nil
}

// unsatisfiedDependencies is DependenciesSatisfied's body, minus the sentinel.
// It reports causes in their own words, and DependenciesSatisfied is its only
// caller.
func unsatisfiedDependencies(ctx context.Context, stagedRoot, atom, version string, deps BuildDeps) ([]string, error) {
	stagedRoot = strings.TrimSpace(stagedRoot)
	atom = strings.TrimSpace(atom)
	version = strings.TrimSpace(version)

	// The atom is split before anything else because the split is also the
	// validator: a malformed atom must fail as a malformed atom, not as an
	// `emerge` invocation that means something unintended. It is split TWICE,
	// because a registry key has two roles here (design D4). splitContentAtom
	// answers role B: the atom PINNED in the pretend run, which must be the
	// suffix-stripped category/package — "=cat/pkg@label-1.0::repo" names a
	// package that exists in no repository, the staged tree included, since
	// Stage writes its content clean. splitStagedAtom answers role A: the
	// SUFFIXED package the staged repository's NAME was written from (R5.4),
	// which stagedRepoNameAt's recomputing fallback must be handed too, or a
	// tree missing its repo_name file resolves under a name Stage never wrote.
	category, pkg, err := splitContentAtom(atom)
	if err != nil {
		return nil, err
	}
	_, suffixedPkg, err := splitStagedAtom(atom)
	if err != nil {
		return nil, err
	}
	// The clean spelling of the key, for the two places Portage has to
	// recognise the candidate by: the pinned spec below and the subtraction of
	// the bump itself from the merge list. The error messages keep naming the
	// full key — the operator knows the bump by its registry spelling.
	cleanAtom := category + "/" + pkg
	if version == "" {
		return nil, fmt.Errorf("resolving %s: no version given, so no exact atom can be pinned for the pretend run", atom)
	}
	if stagedRoot == "" {
		return nil, fmt.Errorf("resolving %s-%s: no staged tree given for Portage to resolve the candidate from", atom, version)
	}
	// Checked up front so a staged tree that was never built fails by name here,
	// instead of as an opaque non-zero exit from a resolver that could not find
	// the repository it was told about.
	info, err := os.Stat(stagedRoot)
	if err != nil {
		return nil, fmt.Errorf("reading the staged tree %s: %w", stagedRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("the staged tree %s is not a directory", stagedRoot)
	}

	// A host with no Portage produces NO ANSWER, never a cheerful one. Without
	// this the absent binary would surface as a failed run, which is the same
	// outcome but a worse sentence for the operator to read.
	if _, err := deps.binaryLookup()("emerge"); err != nil {
		return nil, fmt.Errorf("emerge was not found on PATH, so no dependency resolve is possible on this host: %w", err)
	}

	repoName := stagedRepoNameAt(stagedRoot, suffixedPkg, version)
	repositories, err := composePortageRepositories(stagedRoot, repoName)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, depsResolveTimeout)
	defer cancel()

	spec := "=" + cleanAtom + "-" + version + "::" + repoName
	// --color=n because the merge list is PARSED here: escape sequences around
	// the atoms would make the answer depend on whether stdout looked like a
	// terminal to Portage.
	cmd := deps.commandFactory()(ctx, "emerge", "-p", "--quiet", "--color=n", spec)
	cmd.Env = append(os.Environ(), portageRepositoriesVar+"="+repositories)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("the pretend resolve of %s failed%s: %w", spec, diagnostic(stderr.String()), err)
	}
	// The CLEAN atom, matching the spec above: emerge's merge list names the
	// package as Portage knows it, so the bump subtracts itself from the list
	// only under its clean spelling — matched against the suffixed key, the
	// candidate would count as its own unsatisfied dependency.
	return unsatisfiedFrom(stdout.String(), cleanAtom), nil
}

// stagedRepoNameAt answers by what name Portage will know the staged tree.
//
// profiles/repo_name is authoritative because it is the file PORTAGE itself
// reads: a section header that named the repository something else would leave
// the `::` qualifier on the emerge command pointing at a repository nothing
// matches, and the resolve would fail for a reason no message would explain.
// Stage writes that file, so in production this always reads it; the derived
// fallback is Stage's own naming rule reused, so a tree without one still gets
// the name Stage would have given it.
func stagedRepoNameAt(stagedRoot, pkg, version string) string {
	raw, err := os.ReadFile(filepath.Join(stagedRoot, "profiles", "repo_name")) //nolint:gosec // the path is profiles/repo_name inside the staged tree the caller named
	if err == nil {
		if name := strings.TrimSpace(string(raw)); name != "" {
			return name
		}
	}
	return stagedRepoName(pkg, version)
}

// composePortageRepositories renders the value portageRepositoriesVar carries:
// this host's own repository configuration, plus one section for the staged
// tree.
func composePortageRepositories(stagedRoot, repoName string) (string, error) {
	cfg, err := hostReposConfig()
	if err != nil {
		return "", err
	}

	// Absolute, because a relative location in a repos.conf is resolved against a
	// working directory Portage never promised to be in.
	location, err := filepath.Abs(stagedRoot)
	if err != nil {
		return "", fmt.Errorf("resolving the staged tree path %s: %w", stagedRoot, err)
	}

	if cfg.has(repoName) {
		// Overwriting would silently point an existing repository at the staged
		// tree, which is a worse outcome than refusing to answer.
		return "", fmt.Errorf("this host already configures a repository named %q, so the staged tree at %s cannot be registered under that name", repoName, location)
	}
	cfg.set(repoName, "location", location)

	// The masters line is stage.go's own spelling of the D2 rule, taken apart
	// rather than retyped: the section written here and the layout.conf written
	// into the tree must not be able to name different masters.
	mastersKey, mastersValue, _ := strings.Cut(stagedMasters, "=")
	cfg.set(repoName, strings.TrimSpace(mastersKey), strings.TrimSpace(mastersValue))

	return cfg.render(), nil
}

// hostReposConfig reads the repository configuration this host is running with.
//
// PORTAGE_REPOSITORIES REPLACES the file-based configuration rather than adding
// to it (portage/repository/config.py: load_repository_config reads the variable
// INSTEAD of the files whenever it is set). So the staged section cannot be
// handed over on its own — a value carrying only the staged tree would hide
// gentoo from the resolve, and every answer on every host would be "nothing can
// be built here".
//
// The same precedence is honoured rather than reimplemented: an environment that
// already sets the variable — a container, a chroot, a test harness — IS the
// configuration Portage would have used, so it is the base. Only when it is
// unset are the files read, in Portage's own order.
func hostReposConfig() (*reposConfig, error) {
	cfg := newReposConfig()

	if inherited := os.Getenv(portageRepositoriesVar); strings.TrimSpace(inherited) != "" {
		cfg.merge(inherited)
		return cfg, nil
	}

	for _, path := range portageRepoConfigPaths {
		if err := cfg.mergePath(path); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// reposConfig is a repos.conf held in the order it was read: section names in
// first-appearance order, each section's keys likewise, values last-assignment
// wins.
//
// The order is kept, rather than a map being rendered in whatever order Go feels
// like, so that two runs on one host compose byte-identical configurations. A
// resolve that differed run to run would be a resolve nobody could reproduce
// from a report.
type reposConfig struct {
	order    []string
	sections map[string]*reposSection
}

// reposSection is one [name] block: its keys, in first-appearance order, and
// their current values.
type reposSection struct {
	keys   []string
	values map[string]string
}

func newReposConfig() *reposConfig {
	return &reposConfig{sections: make(map[string]*reposSection)}
}

func (c *reposConfig) has(name string) bool {
	_, ok := c.sections[name]
	return ok
}

// ensure returns the named section, creating it — and recording where it first
// appeared — if this is the first time it has been seen.
func (c *reposConfig) ensure(name string) *reposSection {
	if section, ok := c.sections[name]; ok {
		return section
	}
	section := &reposSection{values: make(map[string]string)}
	c.sections[name] = section
	c.order = append(c.order, name)
	return section
}

// set assigns one key in one section, last assignment winning.
func (c *reposConfig) set(name, key, value string) {
	section := c.ensure(name)
	if _, seen := section.values[key]; !seen {
		section.keys = append(section.keys, key)
	}
	section.values[key] = value
}

// merge folds one repos.conf body into c with the semantics Portage's own parser
// has when it reads several files into one ConfigParser: a section seen again is
// EXTENDED, and a key seen again is OVERWRITTEN by the later file.
//
// # Why this is a merge and not a concatenation
//
// Measured against the installed Portage: joining two config files end to end
// and handing the result over is rejected outright —
//
//	!!! Error while reading repo config file: While reading from
//	'<io.StringIO>' [line 24]: section 'gentoo' already exists
//
// A duplicate section is an error WITHIN a single stream even though it is the
// normal case ACROSS files, and every host that has ever run `eselect repo`
// carries a second [gentoo] section in /etc/portage/repos.conf. Concatenating
// would therefore report UNDETERMINED on the typical Gentoo host — the failure
// mode this whole function exists to distinguish, produced by the function
// itself.
//
// The reader is deliberately narrow, like carriedLayout's: comments and lines it
// cannot read as a header, an assignment or a continuation are dropped, because
// they carry no repository configuration.
func (c *reposConfig) merge(body string) {
	section, key := "", ""

	for line := range strings.Lines(body) {
		trimmed := strings.TrimSpace(line)
		indented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")

		switch {
		case trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";"):
			continue

		case strings.HasPrefix(trimmed, "["):
			name, _, closed := strings.Cut(strings.TrimPrefix(trimmed, "["), "]")
			if !closed {
				continue
			}
			section, key = strings.TrimSpace(name), ""
			c.ensure(section)

		case section == "":
			// An assignment before any header is a line Portage's parser rejects
			// outright; carrying it forward would only move the rejection.
			continue

		case indented && key != "":
			// A value continued on an indented line. It is re-emitted indented so
			// the rendered config parses back to the same value, rather than to a
			// value plus a stray line.
			c.set(section, key, c.sections[section].values[key]+"\n    "+trimmed)

		default:
			name, value, assigned := strings.Cut(trimmed, "=")
			if !assigned {
				continue
			}
			key = strings.TrimSpace(name)
			c.set(section, key, strings.TrimSpace(value))
		}
	}
}

// mergePath folds one configured location into c. It may be a file or a
// directory of files, because Portage accepts either at both of its config
// paths.
//
// A location that does not exist is not an error: a host configured only through
// the other path is an ordinary host, and a host with no Portage at all was
// already refused by the binary lookup. A location that exists but cannot be
// READ is an error, because silently composing a configuration that is missing
// gentoo would turn a permission problem into a wrong answer about a bump.
func (c *reposConfig) mergePath(root string) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return fmt.Errorf("reading the Portage repository configuration %s: %w", path, err)
		case entry.IsDir():
			if path != root && skipReposConfEntry(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		case skipReposConfEntry(entry.Name()):
			return nil
		default:
			return c.mergeFile(path)
		}
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// mergeFile folds one configuration file into c.
func (c *reposConfig) mergeFile(path string) error {
	raw, err := os.ReadFile(path) //nolint:gosec // the path comes from walking Portage's own configuration locations, not from input
	if err != nil {
		return fmt.Errorf("reading the Portage repository configuration %s: %w", path, err)
	}
	c.merge(string(raw))
	return nil
}

// skipReposConfEntry mirrors the filter Portage's own directory walk applies:
// a name beginning with "." or ending with "~" is not configuration.
//
// This is not tidiness. The maintainer's host carries
// /etc/portage/repos.conf/eselect-repo.conf~ — an eselect backup still declaring
// a [torbrowser] repository whose location has since been removed. Reading it
// would hand Portage a repository pointing at a directory that is not there, and
// the resolve would fail for a reason that has nothing to do with any bump.
func skipReposConfEntry(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~")
}

// render writes the configuration back out as a single repos.conf stream, one
// section per name — which is the whole point of having merged them.
func (c *reposConfig) render() string {
	var body strings.Builder
	for _, name := range c.order {
		section := c.sections[name]
		body.WriteString("[" + name + "]\n")
		for _, key := range section.keys {
			body.WriteString(key + " = " + section.values[key] + "\n")
		}
		body.WriteString("\n")
	}
	return body.String()
}

// unsatisfiedFrom reads the packages an `emerge -p --quiet` run says it would
// install, minus the one under test — which is the subject of the question, not
// an answer to it.
//
// The listed order is kept: it is the order Portage would build them in, which
// is the order an operator installing them by hand wants.
func unsatisfiedFrom(out, atom string) []string {
	var unsatisfied []string
	for line := range strings.Lines(out) {
		spec, listed := listedPackage(line)
		if !listed || namesPackageUnderTest(spec, atom) {
			continue
		}
		unsatisfied = append(unsatisfied, spec)
	}
	return unsatisfied
}

// listedPackage reads one merge-list line — `[ebuild  N    ] cat/pkg-1.2.3` —
// and reports the package spec it names, if it names one that would be
// installed.
//
// Only the first field after the tag is taken: with --quiet a line may still
// carry USE flags and a download size after the atom, and none of that is part
// of the package's name.
func listedPackage(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	tag, rest, closed := strings.Cut(strings.TrimPrefix(trimmed, "["), "]")
	if !closed {
		return "", false
	}

	action := strings.Fields(tag)
	if len(action) == 0 || !installActions[action[0]] {
		return "", false
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

// namesPackageUnderTest reports whether a listed spec names the package being
// validated, in any version and from any repository.
//
// ANY version, deliberately: a resolve may list a second slot or the installed
// version alongside the candidate, and none of those is a dependency this host
// is missing. The version is cut with Gentoo's own rule — the name ends at the
// last hyphen followed by a digit — so a sibling package whose name merely
// starts the same, media-plugins/gst-plugins-qt6-extra beside
// media-plugins/gst-plugins-qt6, is still reported.
func namesPackageUnderTest(spec, atom string) bool {
	name, _, _ := strings.Cut(spec, "::")
	if name == atom {
		return true
	}
	rest, prefixed := strings.CutPrefix(name, atom+"-")
	return prefixed && rest != "" && rest[0] >= '0' && rest[0] <= '9'
}
