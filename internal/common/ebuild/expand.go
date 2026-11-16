package ebuild

import (
	"regexp"
	"strconv"
	"strings"
)

// ExpandVersionRefs expands, in an ebuild variable's value, the references
// that need no bash to resolve: ${PV}, ${PVR}, $(ver_cut N[-M]) and one level
// of indirection through a variable the ebuild itself assigns at the top
// level from those same references — dev-lang/python's
//
//	PYVER=$(ver_cut 1-2)
//	SLOT="${PYVER}"
//
// body is the ebuild's text, pvr its version with any -rN revision (the
// filename's version). ok is false when anything is left unresolved: a
// variable an eclass sets (gcc's ${GCCMAJOR}, llvm's ${LLVM_MAJOR}), a
// variable assigned from another variable, a command substitution other than
// ver_cut. An unresolved value must narrow nothing, so callers treat !ok as
// "unknown", never as a literal.
func ExpandVersionRefs(value, body, pvr string) (string, bool) {
	pv := stripRevisionSuffix(pvr)
	// A ver_cut range PV does not have yields "" — ver_cut itself would
	// print nothing — and pasting that "" into the value would turn an
	// unknown into a wrong literal ("3/$(ver_cut 9)" -> "3/"). It is recorded
	// and the whole value reported unresolved instead.
	cutFailed := false
	expandDirect := func(v string) string {
		v = strings.NewReplacer("${PVR}", pvr, "${PV}", pv).Replace(v)
		return verCutCallRe.ReplaceAllStringFunc(v, func(call string) string {
			cut := VerCut(pv, verCutCallRe.FindStringSubmatch(call)[1])
			if cut == "" {
				cutFailed = true
			}
			return cut
		})
	}

	value = expandDirect(value)
	value = varRefRe.ReplaceAllStringFunc(value, func(ref string) string {
		m := varRefRe.FindStringSubmatch(ref)
		name := m[1]
		if name == "" {
			name = m[2]
		}
		assigned, found := topLevelAssignment(body, name)
		if !found {
			return ref
		}
		// One level only: a value that still references something after the
		// direct expansion stays unresolved and fails the check below.
		return expandDirect(assigned)
	})
	if cutFailed || value == "" || strings.ContainsAny(value, "$`") {
		return "", false
	}
	return value, true
}

// varRefRe matches ${NAME} and $NAME. Function-call forms ($(...)) are not
// variable references and are left for the unresolved check.
var varRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// topLevelAssignment returns the value of the last `NAME=value` assignment
// that starts at column 0, with its quotes removed. Column 0 is the ebuild's
// global scope, which Portage evaluates while sourcing; an indented assignment
// lives inside a function or a conditional and is ignored. The LAST one wins
// because bash leaves the later global assignment in effect. An unquoted
// $(command) is taken whole: `PYVER=$(ver_cut 1-2)` has a space inside.
func topLevelAssignment(body, name string) (string, bool) {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=("[^"\n]*"|'[^'\n]*'|\$\([^)\n]*\)|[^\s#]*)`)
	all := re.FindAllStringSubmatch(body, -1)
	if len(all) == 0 {
		return "", false
	}
	return strings.Trim(all[len(all)-1][1], `"'`), true
}

var verCutCallRe = regexp.MustCompile(`\$\(ver_cut ([0-9]+(?:-[0-9]*)?)\)`)

// verComponentRe is ver_cut's notion of a component: a run of digits or a run
// of letters; everything else separates.
var verComponentRe = regexp.MustCompile(`[0-9]+|[A-Za-z]+`)

// VerCut implements ver_cut for the "N", "N-M" and "N-" ranges: the substring
// of pv from the start of component N to the end of component M. It returns
// "" for a range ver_cut would not produce.
func VerCut(pv, spec string) string {
	comps := verComponentRe.FindAllStringIndex(pv, -1)
	if len(comps) == 0 {
		return ""
	}
	from, to := spec, spec
	if a, b, found := strings.Cut(spec, "-"); found {
		from, to = a, b
	}
	start, err := strconv.Atoi(from)
	if err != nil || start < 1 {
		return ""
	}
	end := len(comps)
	if to != "" {
		if end, err = strconv.Atoi(to); err != nil {
			return ""
		}
	}
	if start > len(comps) {
		return ""
	}
	if end > len(comps) {
		end = len(comps)
	}
	if end < start {
		return ""
	}
	return pv[comps[start-1][0]:comps[end-1][1]]
}

// stripRevisionSuffix turns "1.2-r3" into "1.2", the ${PV} of the ebuild.
func stripRevisionSuffix(version string) string {
	if i := strings.LastIndex(version, "-r"); i > 0 {
		if _, err := strconv.Atoi(version[i+2:]); err == nil {
			return version[:i]
		}
	}
	return version
}
