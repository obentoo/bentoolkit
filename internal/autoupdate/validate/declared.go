package validate

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Declared is what the upstream archive says its build options are.
//
// Sources records the archive members actually read. It is not bookkeeping: it
// is the gate's evidence, and the report prints it. A PASS whose sources are
// not shown cannot be audited — the operator has no way to tell "read the
// option file and found nothing wrong" from "found no option file to read".
type Declared struct {
	Root       []Option
	Subproject map[string][]Option
	Sources    []string
}

// ErrBuildSystemUndetermined reports that the archive carries no meson.build at
// any project root, so nothing here can say what builds it.
//
// This is a sentinel and not a guess on purpose (R4.3). The caller turns it into
// a SKIPPED outcome that says the build system was undetermined; inferring
// "probably autotools" from the absence of one file would be exactly the
// unearned confidence this story removes.
var ErrBuildSystemUndetermined = errors.New("no meson.build found: build system undetermined")

// Option-file names, newest first. Meson renamed meson_options.txt to
// meson.options in 1.1 and still accepts both, so both are in the wild
// simultaneously — including mid-migration, in one archive, which is why this
// is a precedence and not a choice.
var mesonOptionFiles = []string{"meson.options", "meson_options.txt"}

// OptionsFromArchive reads the build options an upstream archive declares.
//
// It never unpacks the archive: one listing pass picks the members worth
// reading, and each is extracted to stdout (see extractArchiveMember). Only the
// members it parses are extracted, which is R1.5.
//
// # The namespacing rule
//
// Meson puts one option file at each PROJECT root. What lives under
// subprojects/<name>/ is a separate project with its own root, and its options
// are addressed as -D<name>:<option>= (design.md D3). Filing a subproject's
// options under the root would compare them against declarations that never
// list them — every one would surface as an undeclared option, and a gate whose
// first finding is false is a gate someone switches off.
//
// A subproject is recognised by its option file alone. Requiring a meson.build
// beside it would drop real subprojects that ship only options, and the cost of
// a false subproject is nil: nothing compares against a namespace the ebuild
// never addresses.
func OptionsFromArchive(ctx context.Context, archive string) (Declared, error) {
	members, err := listArchiveMembers(ctx, archive)
	if err != nil {
		return Declared{}, err
	}

	prefix, ok := projectRoot(members)
	if !ok {
		// Name what WAS found rather than only what was not. "SKIPPED: build
		// system is cmake" tells the operator this archive is out of scope by
		// design (R4.2); "SKIPPED: undetermined" tells them the gate could not
		// tell (R4.3). Both are honest, and they call for different actions —
		// collapsing them into one message loses that.
		return Declared{}, fmt.Errorf("%s: %w%s", archive, ErrBuildSystemUndetermined, detectedSystem(members))
	}

	var declared Declared

	// The root's own options. Absent is a legitimate answer, not a failure: a
	// Meson project may declare none (R1.4), and treating that as an error would
	// turn a whole class of packages into false SKIPPEDs.
	if member, found := pickOptionFile(members, prefix); found {
		opts, err := readOptions(ctx, archive, member, "")
		if err != nil {
			return Declared{}, err
		}
		declared.Root = opts
		declared.Sources = append(declared.Sources, member)
	}

	// Subprojects, in name order so two runs over one archive render alike.
	subs := subprojectDirs(members, prefix)
	names := make([]string, 0, len(subs))
	for name := range subs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		member, found := pickOptionFile(members, subs[name])
		if !found {
			continue
		}
		opts, err := readOptions(ctx, archive, member, name)
		if err != nil {
			return Declared{}, err
		}
		if declared.Subproject == nil {
			declared.Subproject = make(map[string][]Option, len(names))
		}
		declared.Subproject[name] = opts
		declared.Sources = append(declared.Sources, member)
	}

	return declared, nil
}

// readOptions extracts one option file and parses it, stamping each option with
// the member it came from and the subproject it belongs to. Those two fields
// are what let a finding name its own evidence.
func readOptions(ctx context.Context, archive, member, subproject string) ([]Option, error) {
	data, err := extractArchiveMember(ctx, archive, member)
	if err != nil {
		return nil, fmt.Errorf("reading declared options from %s: %w", archive, err)
	}
	opts := parseMesonOptions(data)
	for i := range opts {
		opts[i].Subproject = subproject
		opts[i].Source = member
	}
	return opts, nil
}

// projectRoot returns the directory prefix of the archive's project root, with
// its trailing slash, and whether the archive is a Meson project at all.
//
// # meson.build must be AT the root, not merely somewhere
//
// This was measured the hard way. An earlier version took the shallowest
// meson.build wherever it sat, and on the live overlay that made net-libs/
// webkit-gtk — a CMake package — report 89 error findings. WebKit vendors
// third-party code that uses Meson, so the archive does carry a meson.build,
// several directories down; the gate adopted it as the project root and then
// compared webkit's CMake `-DENABLE_*=` cache variables against an unrelated
// subproject's option file. Every one came out undeclared.
//
// That is the exact failure this story keeps naming: the first false error is
// what gets a gate switched off, and 89 of them would have done it on the first
// run. So the rule is positional, not existential — a Meson project declares
// itself with a meson.build at ITS OWN ROOT, and an archive without one there
// is reported undetermined (R4.3) with whatever marker WAS found named beside
// it (R4.2), rather than validated against a file that belongs to someone else.
func projectRoot(members []string) (string, bool) {
	prefix := archiveRoot(members)
	for _, m := range members {
		if m == prefix+"meson.build" {
			return prefix, true
		}
	}
	return "", false
}

// archiveRoot returns the single top-level directory every member sits under,
// with its trailing slash, or "" when the members are not wrapped in one.
//
// A release tarball wraps everything in <name>-<version>/, which is the case
// that matters. An archive with several top-level entries has no wrapper, and
// its root is the archive itself.
func archiveRoot(members []string) string {
	var top string
	for _, m := range members {
		head, _, _ := strings.Cut(strings.TrimSuffix(m, "/"), "/")
		if head == "" {
			continue
		}
		if top == "" {
			top = head
			continue
		}
		if head != top {
			return "" // more than one top-level entry: no wrapper directory.
		}
	}
	if top == "" {
		return ""
	}
	// One top-level NAME is not yet a wrapper — an archive holding a single
	// file has one too. It is a wrapper only once something lives inside it.
	prefix := top + "/"
	for _, m := range members {
		if strings.HasPrefix(m, prefix) && len(m) > len(prefix) {
			return prefix
		}
	}
	return ""
}

// buildSystemMarkers maps a marker file to the build system it names. Only
// files a build system REQUIRES at its project root are listed: a marker that
// merely often accompanies one would turn this from an observation into the
// guess R4.3 refuses to make.
var buildSystemMarkers = []struct{ file, system string }{
	{"CMakeLists.txt", "cmake"},
	{"configure.ac", "autotools"},
	{"configure.in", "autotools"},
	{"configure", "a configure script"},
	{"Cargo.toml", "cargo"},
	{"setup.py", "python setuptools"},
	{"pyproject.toml", "python (PEP 517)"},
	{"go.mod", "go modules"},
	{"CMakePresets.json", "cmake"},
	{"Makefile.am", "automake"},
	{"Makefile", "make"},
	{"SConstruct", "scons"},
	{"waf", "waf"},
}

// detectedSystem returns ", but <system> was found" when the archive carries a
// marker for a build system this gate does not read, or "" when it carries none
// this knows about.
//
// It is deliberately shallow. Reading a CMake or Autotools option surface is
// out of scope (story Out of Scope), so all this has to do is let the report
// distinguish "out of scope" from "could not tell" — the two SKIPPED reasons
// R4.2 and R4.3 ask for. It never changes an outcome and never overrides the
// sentinel.
func detectedSystem(members []string) string {
	best, bestDepth := "", -1
	for _, m := range members {
		base := path.Base(m)
		for _, marker := range buildSystemMarkers {
			if base != marker.file {
				continue
			}
			// Shallowest wins, so a vendored Makefile deep inside a tree does
			// not outrank the marker at the project root.
			depth := strings.Count(m, "/")
			if bestDepth == -1 || depth < bestDepth {
				best, bestDepth = marker.system, depth
			}
		}
	}
	if best == "" {
		return ""
	}
	return ", but " + best + " was found: reading its options is out of scope"
}

// pickOptionFile returns the option file inside dir, honouring the
// meson.options-over-meson_options.txt precedence when an archive carries both.
func pickOptionFile(members []string, dir string) (string, bool) {
	for _, name := range mesonOptionFiles {
		want := dir + name
		for _, m := range members {
			if m == want {
				return m, true
			}
		}
	}
	return "", false
}

// subprojectDirs maps each subproject name to its directory prefix.
func subprojectDirs(members []string, prefix string) map[string]string {
	base := prefix + "subprojects/"
	dirs := map[string]string{}
	for _, m := range members {
		rest, ok := strings.CutPrefix(m, base)
		if !ok {
			continue
		}
		name, _, ok := strings.Cut(rest, "/")
		if !ok || name == "" {
			continue
		}
		dirs[name] = base + name + "/"
	}
	return dirs
}
