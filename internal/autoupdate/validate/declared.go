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
		return Declared{}, fmt.Errorf("%s: %w", archive, ErrBuildSystemUndetermined)
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

// projectRoot returns the directory prefix of the archive's top-level project,
// with its trailing slash, and whether one was found at all.
//
// The root is located by its meson.build, taking the SHALLOWEST one: a release
// tarball wraps everything in a <name>-<version>/ directory, and every
// subprojects/<name>/ below it has a meson.build of its own. Depth is what
// separates the project from its subprojects without hardcoding the wrapper's
// name.
func projectRoot(members []string) (string, bool) {
	var best string
	var bestDepth int
	found := false

	for _, m := range members {
		if path.Base(m) != "meson.build" {
			continue
		}
		dir := path.Dir(m)
		if dir == "." {
			dir = "" // meson.build sits at the archive root: no wrapper directory.
		}
		depth := 0
		if dir != "" {
			depth = strings.Count(dir, "/") + 1
		}
		// A tie is broken lexicographically, so the answer does not depend on
		// the order tar happened to store the members in.
		if !found || depth < bestDepth || (depth == bestDepth && dir < best) {
			best, bestDepth, found = dir, depth, true
		}
	}

	if !found {
		return "", false
	}
	if best == "" {
		return "", true
	}
	return best + "/", true
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
