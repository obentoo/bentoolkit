package validate

import (
	"fmt"
	"path"
	"sort"
)

// Severity ranks a finding. It is a string rather than an integer because it is
// rendered in a JSON document, where a number would force every consumer to
// carry the same mapping and get it right.
type Severity string

const (
	// SeverityError is a passed option the upstream source does not declare —
	// the failure class issue #33 belongs to. It is the only severity that
	// reaches the exit code.
	SeverityError Severity = "error"
	// SeverityWarning is an option name that cannot be resolved statically.
	// Reported, because a name the gate could not read is a hole in its answer.
	SeverityWarning Severity = "warning"
	// SeverityInfo is an option upstream declares and the ebuild never passes.
	// Useful to know, blocks nothing.
	SeverityInfo Severity = "info"
)

// Gate names which check produced a finding. It exists so Report.ExitCode can
// select on it: pkgcheck findings ride in the same report and must never move
// the exit code (D8).
const (
	GateOptions = "options"
	GateQA      = "qa"
)

// Finding is one thing the gate has to say about an ebuild.
type Finding struct {
	Gate     string
	Severity Severity
	Detail   string
}

// Compare subtracts the two option sets and returns the findings.
//
// It is pure — no I/O, no clock, no filesystem — which is what makes every case
// below a table row instead of a fixture, and what lets the exhaustive tests be
// exhaustive.
//
// # The three rules
//
//   - A project option the archive does not declare is an `error` (R3.1). This
//     is the whole point: it is what `-Daalib=` became when upstream removed
//     the option at 1.29.
//   - A declared option the ebuild never passes is `info` (R3.2) — upstream
//     gained something we have not adopted. Useful, never blocking.
//   - Each unresolved name is a `warning` (R3.3), quoting the text as written,
//     because a name the gate could not read is a gap in its own answer and
//     saying so is the difference between a report and a claim.
//
// Built-ins are never compared: Meson defines them and no option file declares
// them, so comparing one would manufacture an error out of a correct ebuild.
//
// Matching happens WITHIN a namespace. `sub:foo` is checked against `sub`'s
// declarations, never the root's — otherwise every subproject option an ebuild
// legitimately passes would surface as undeclared.
//
// Every Detail names its source: the ebuild line for a passed option, the
// archive member for a declared one. A finding that cannot be traced back to
// the line that caused it makes the operator grep for it, which is the
// difference between an actionable gate and an annoying one.
func Compare(d Declared, p Passed, pkg, version string) []Finding {
	var findings []Finding

	declared := declaredIndex(d)
	passed := make(map[string]bool, len(p.Project))

	// Passed but undeclared — the error case.
	for _, o := range p.Project {
		passed[o.Qualified()] = true
		if declared[o.Qualified()] {
			continue
		}
		findings = append(findings, Finding{
			Gate:     GateOptions,
			Severity: SeverityError,
			Detail: fmt.Sprintf("%s passes -D%s= but upstream %s does not declare it (%s)",
				pkg, o.Qualified(), version, o.Source),
		})
	}

	// Declared but never passed — the info case.
	for _, o := range declaredOptions(d) {
		if passed[o.Qualified()] {
			continue
		}
		findings = append(findings, Finding{
			Gate:     GateOptions,
			Severity: SeverityInfo,
			Detail: fmt.Sprintf("upstream %s declares %s, which the ebuild does not pass (%s)",
				version, o.Qualified(), o.Source),
		})
	}

	// Names built at runtime — the warning case. The line number is what makes
	// this one actionable; without it the operator is told something is wrong
	// somewhere in a file they then have to search.
	for _, u := range p.Unresolved {
		findings = append(findings, Finding{
			Gate:     GateOptions,
			Severity: SeverityWarning,
			Detail: fmt.Sprintf("%s builds an option name at runtime: %s (%s:%d) — it cannot be checked statically",
				pkg, u.Text, ebuildName(pkg, version), u.Line),
		})
	}

	return findings
}

// declaredIndex flattens Declared into a set keyed the way an ebuild addresses
// an option, so the lookup and the report cannot disagree about a name.
func declaredIndex(d Declared) map[string]bool {
	index := make(map[string]bool, len(d.Root))
	for _, o := range declaredOptions(d) {
		index[o.Qualified()] = true
	}
	return index
}

// declaredOptions returns every declared option, root first and then each
// subproject's, in the order OptionsFromArchive assembled them — which is file
// order within a file and name order across subprojects, so two runs over one
// archive render alike.
func declaredOptions(d Declared) []Option {
	out := make([]Option, 0, len(d.Root))
	out = append(out, d.Root...)

	// Sorted, because ranging a map is randomised: without this the info
	// findings would come out in a different order on every run, and a report
	// that reshuffles itself cannot be diffed between two runs.
	names := make([]string, 0, len(d.Subproject))
	for name := range d.Subproject {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, o := range d.Subproject[name] {
			// Trust the namespace the map is keyed by rather than the field on
			// the option: a hand-built Declared may leave Subproject unset, and
			// a mismatch there would silently move an option to the root.
			o.Subproject = name
			out = append(out, o)
		}
	}
	return out
}

// ebuildName reconstructs the ebuild's file name from the atom and version,
// which is the naming rule an overlay enforces anyway. It exists so an
// unresolved warning can name the file its line number belongs to without
// Compare having to take a path it needs for nothing else.
func ebuildName(pkg, version string) string {
	return path.Base(pkg) + "-" + version + ".ebuild"
}
