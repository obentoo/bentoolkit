package targets

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Offered maps a group to the implementations a package offers in it. A group
// absent from the map is one the package does not use.
type Offered map[string][]string

func (o Offered) add(group, impl string) {
	if !slices.Contains(o[group], impl) {
		o[group] = append(o[group], impl)
	}
}

// FromIUSE reads what a package offers from its metadata-cache IUSE, which the
// eclasses have already expanded — and already pruned of implementations the
// installed eclass no longer supports — so it is the authoritative answer.
func FromIUSE(iuse string) Offered {
	o := Offered{}
	for _, flag := range strings.Fields(iuse) {
		flag = strings.TrimLeft(flag, "+-")
		for _, group := range Groups {
			if impl, ok := strings.CutPrefix(flag, group+"_"); ok && impl != "" {
				o.add(group, impl)
				break
			}
		}
	}
	return o
}

// compatSource ties an eclass to the group it generates and the variable it
// reads the implementations from.
type compatSource struct {
	eclass string
	group  string
	varRef string
}

// compatSources lists the eclasses that turn a *_COMPAT variable into USE
// flags. python-any-r1 and ruby-single are absent on purpose: they generate an
// any-of dependency, not flags, so REQUIRED_USE never involves them.
var compatSources = []compatSource{
	{"python-single-r1", "python_single_target", "PYTHON_COMPAT"},
	{"python-r1", "python_targets", "PYTHON_COMPAT"},
	{"lua-single", "lua_single_target", "LUA_COMPAT"},
	{"lua", "lua_targets", "LUA_COMPAT"},
	{"ruby-ng", "ruby_targets", "USE_RUBY"},
	{"ruby-fakegem", "ruby_targets", "USE_RUBY"},
}

var (
	inheritLine      = regexp.MustCompile(`(?m)^[ \t]*inherit[ \t]+([^#\n]+)`)
	distutilsSingle  = regexp.MustCompile(`(?m)^[ \t]*DISTUTILS_SINGLE_IMPL=["']?1`)
	braceGroup       = regexp.MustCompile(`\{([^{}]*)\}`)
	numericRange     = regexp.MustCompile(`^(-?\d+)\.\.(-?\d+)$`)
	compatAssignment = map[string]*regexp.Regexp{}
)

func init() {
	for _, v := range []string{"PYTHON_COMPAT", "LUA_COMPAT"} {
		compatAssignment[v] = regexp.MustCompile(`(?ms)^[ \t]*` + v + `=\(([^)]*)\)`)
	}
	compatAssignment["USE_RUBY"] = regexp.MustCompile(`(?m)^[ \t]*USE_RUBY=["']([^"']*)["']`)
}

// FromEbuild reads what a package offers from the ebuild text, for a candidate
// no metadata cache describes yet. It answers an error, never a guess, when an
// inherited eclass's variable cannot be read literally (absent, or built from
// another variable): an empty offer there would be reported as a gap the
// ebuild may not have.
func FromEbuild(content []byte) (Offered, error) {
	text := string(content)
	inherited := map[string]bool{}
	for _, m := range inheritLine.FindAllStringSubmatch(text, -1) {
		for _, e := range strings.Fields(m[1]) {
			inherited[e] = true
		}
	}
	if inherited["distutils-r1"] {
		if distutilsSingle.MatchString(text) {
			inherited["python-single-r1"] = true
		} else {
			inherited["python-r1"] = true
		}
	}

	o := Offered{}
	for _, src := range compatSources {
		if !inherited[src.eclass] {
			continue
		}
		m := compatAssignment[src.varRef].FindStringSubmatch(text)
		if m == nil {
			return nil, fmt.Errorf("%s is inherited but %s is not assigned literally", src.eclass, src.varRef)
		}
		if strings.Contains(m[1], "$") {
			return nil, fmt.Errorf("%s is built from another variable (%s), which this check cannot expand", src.varRef, strings.TrimSpace(m[1]))
		}
		o[src.group] = nil
		for _, word := range strings.Fields(m[1]) {
			for _, impl := range ExpandBraces(strings.Trim(word, `"'`)) {
				o.add(src.group, impl)
			}
		}
	}
	return o, nil
}

// ExpandBraces performs the bash brace expansion ebuilds use in *_COMPAT:
// comma lists ({13,14}) and numeric ranges ({12..15}), left to right.
func ExpandBraces(word string) []string {
	loc := braceGroup.FindStringSubmatchIndex(word)
	if loc == nil {
		return []string{word}
	}
	prefix, body, suffix := word[:loc[0]], word[loc[2]:loc[3]], word[loc[1]:]

	var alternatives []string
	if r := numericRange.FindStringSubmatch(body); r != nil {
		from, errFrom := strconv.Atoi(r[1])
		to, errTo := strconv.Atoi(r[2])
		if errFrom != nil || errTo != nil {
			return []string{word}
		}
		step := 1
		if to < from {
			step = -1
		}
		for i := from; ; i += step {
			alternatives = append(alternatives, strconv.Itoa(i))
			if i == to {
				break
			}
		}
	} else if strings.Contains(body, ",") {
		alternatives = strings.Split(body, ",")
	} else {
		// Bash leaves a brace group with neither form untouched.
		return []string{word}
	}

	var out []string
	for _, alt := range alternatives {
		out = append(out, ExpandBraces(prefix+alt+suffix)...)
	}
	return out
}
