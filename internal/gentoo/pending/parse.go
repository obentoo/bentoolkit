package pending

import (
	"bytes"
	"regexp"
	"strings"
)

// versionSuffix is the package manager specification's version grammar,
// with its leading hyphen and an optional revision, anchored at the end. A
// package name may itself hold "-<digit>" (font-adobe-100dpi), so the version
// is only ever what this matches at the end of the atom.
var versionSuffix = regexp.MustCompile(`-\d+(\.\d+)*[a-z]?(_(alpha|beta|pre|rc|p)\d*)*(-r\d+)?$`)

// buildID is the build id Portage appends to the version of a binary package
// under binpkg-multi-instance (sys-libs/zlib-1.3.1-r1-1): a hyphen and digits,
// anchored at the end, ahead of the slot and the repository.
var buildID = regexp.MustCompile(`-\d+$`)

// categoryName and packageName are the specification's character sets: a
// category may not begin with a hyphen, a dot or a plus sign, a package name
// with a hyphen or a plus sign.
var (
	categoryName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9+_.-]*$`)
	packageName  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9+_-]*$`)
)

// glsaLine is one advisory line of `glsa-check --list`: the id, its marker,
// the title, and the package atoms between the LAST " ( " and the closing
// " )". The greedy title is what makes the atoms open at the last " ( ", so
// parentheses in a title stay in the title.
var glsaLine = regexp.MustCompile(`^(\d{6}-\d{2}) \[([NAU])\] (.*) \( (.*) \)$`)

// glsaMore is what glsa-check prints in place of the packages past the third.
const glsaMore = "..."

// GLSA is one advisory glsa-check reports as possibly affecting this system.
type GLSA struct {
	ID    string   // the GLSA id, as 202508-06
	Title string   // the advisory title, as printed
	Atoms []string // the affected package atoms, as printed
}

// ParseWorld returns the distinct packages, as category/name, that an
// `emerge --pretend` would merge, in the order of their first appearance. It
// reads only the lines starting with "[ebuild" or "[binary"; uninstalls,
// blockers, no-merge lines and every other line are skipped, and so is an
// atom that is not a valid category/name with a version.
func ParseWorld(stdout []byte) []string {
	var names []string
	seen := map[string]bool{}
	for line := range bytes.Lines(stdout) {
		binary := bytes.HasPrefix(line, []byte("[binary"))
		if !binary && !bytes.HasPrefix(line, []byte("[ebuild")) {
			continue
		}
		name, ok := mergedName(line, binary)
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// mergedName reads the category/name of the atom that follows the bracketed
// status of a merge line: the repository, the slot, a binary package's build
// id and the version are dropped, and what is left must be a valid
// category/name.
func mergedName(line []byte, binary bool) (string, bool) {
	_, rest, found := bytes.Cut(line, []byte("] "))
	if !found {
		return "", false
	}
	fields := strings.Fields(string(rest))
	if len(fields) == 0 {
		return "", false
	}
	atom, _, _ := strings.Cut(fields[0], "::")
	atom, _, _ = strings.Cut(atom, ":")
	if binary {
		atom = withoutBuildID(atom)
	}
	loc := versionSuffix.FindStringIndex(atom)
	if loc == nil {
		return "", false
	}
	category, pkg, found := strings.Cut(atom[:loc[0]], "/")
	if !found || !categoryName.MatchString(category) || !packageName.MatchString(pkg) ||
		versionSuffix.MatchString(pkg) {
		return "", false
	}
	return category + "/" + pkg, true
}

// withoutBuildID drops the trailing build id of a binary package's atom. It
// drops it only when what remains still ends in a version: dev-libs/bar-1 has
// no build id, its "-1" is the version itself. A version never ends in a
// second "-<digits>" (a revision is "-r<digits>"), so app/foo-1-2 can only be
// foo-1 built as id 2.
func withoutBuildID(atom string) string {
	loc := buildID.FindStringIndex(atom)
	if loc == nil || !versionSuffix.MatchString(atom[:loc[0]]) {
		return atom
	}
	return atom[:loc[0]]
}

// ParseGLSA reads the standard output of `glsa-check --list affected` and
// returns the advisories marked [N] (the system might be affected), in output
// order, one per id. Lines marked [A] (applied) or [U] (unaffected) are
// recognised and skipped; any other line, the legend included, is ignored.
//
// ok is true when the output is blank (glsa-check writes its legend to
// standard error, so a system with no advisory leaves standard output empty)
// or holds at least one recognised line. Non-blank output with none is not
// an answer: ok is false and no GLSA is returned.
func ParseGLSA(stdout []byte) ([]GLSA, bool) {
	if len(bytes.TrimSpace(stdout)) == 0 {
		return nil, true
	}
	var glsas []GLSA
	recognised := false
	seen := map[string]bool{}
	for line := range bytes.Lines(stdout) {
		m := glsaLine.FindSubmatch(bytes.TrimRight(line, " \t\r\n"))
		if m == nil {
			continue
		}
		recognised = true
		id := string(m[1])
		if string(m[2]) != "N" || seen[id] {
			continue
		}
		seen[id] = true
		glsas = append(glsas, GLSA{ID: id, Title: string(m[3]), Atoms: glsaAtoms(string(m[4]))})
	}
	return glsas, recognised
}

// glsaAtoms splits the packages of an advisory line on blanks and drops the
// "..." glsa-check prints for the ones it leaves out.
func glsaAtoms(list string) []string {
	var atoms []string
	for _, a := range strings.Fields(list) {
		if a != glsaMore {
			atoms = append(atoms, a)
		}
	}
	return atoms
}
