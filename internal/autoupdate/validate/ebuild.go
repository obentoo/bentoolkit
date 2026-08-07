package validate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Passed is what the ebuild hands the build.
//
// The slices are exhaustive and disjoint: every `-D…=` an ebuild writes lands
// in exactly one of them. That is the property the comparison rests on — an
// unwritten fourth category ("names we could not make sense of, dropped") would
// be a silent hole, and a silent hole in a parity check reads as a pass.
type Passed struct {
	Project    []Option
	BuiltIn    []Option
	Unresolved []Unresolved
}

// Unresolved is an option name the ebuild builds at runtime instead of writing
// out, so no static reader can say what it will be.
//
// It is REPORTED, never dropped (R2.3). Dropping it is exactly the false-clean
// this project has already measured once and removed: the gate would say
// "nothing wrong here" about an ebuild it could not fully read.
type Unresolved struct {
	Text string // the text as written, e.g. `-D${plugin}=`
	Line int    // 1-based, so the operator can go straight to it
}

// OptionsFromEbuild reads the option names an ebuild passes to the build.
//
// # Only the name side
//
// Just the text between `-D` and the first `=` is considered. Values are
// irrelevant to a parity check, and ignoring them is what makes the unresolved
// rule precise instead of noisy: `-Dexamples=$(usex test enabled disabled)` is
// an ordinary ebuild line whose NAME is perfectly static, while `-D${plugin}=`
// is a name assembled at runtime. A reader that looked at whole arguments would
// flag the first, and a gate that warns about every normal ebuild is a gate
// someone switches off.
//
// `--enable-<name>` and `--with-<name>` are deliberately not extracted: with
// only Meson declarations on the upstream side there is nothing to compare them
// against, so the result would be an output no requirement consumes.
//
// Source is set to "<ebuild file name>:<line>" so every finding can point at
// the line that caused it.
func OptionsFromEbuild(path string) (Passed, error) {
	file, err := os.Open(path) //nolint:gosec // reading an overlay ebuild is this gate's whole purpose
	if err != nil {
		return Passed{}, fmt.Errorf("reading ebuild %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	base := filepath.Base(path)
	var passed Passed

	scanner := bufio.NewScanner(file)
	// Ebuild lines are short, but a generated one-liner is not unheard of, and
	// the default 64 KiB token limit would turn it into a truncated read that
	// looks like a clean one.
	scanner.Buffer(make([]byte, 0, 64*1024), maxArchiveBytes)

	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		// A wholly commented line is skipped. Upstream's own removed options
		// often survive as a commented `-Dfoo=` for a release or two, and
		// reading one as passed would produce a finding about an argument the
		// build never receives — the first false error is what gets a gate
		// switched off. Only a full-line comment is dropped: a mid-line `#` can
		// be a parameter expansion like ${x#y}, and stripping that would be a
		// worse bug than the one it fixes.
		if strings.HasPrefix(strings.TrimSpace(text), "#") {
			continue
		}
		collectPassedOptions(text, line, base, &passed)
	}
	if err := scanner.Err(); err != nil {
		return Passed{}, fmt.Errorf("reading ebuild %s: %w", path, err)
	}

	return passed, nil
}

// collectPassedOptions classifies every `-D…=` on one line into passed.
func collectPassedOptions(text string, line int, base string, passed *Passed) {
	source := base + ":" + strconv.Itoa(line)

	for i := 0; i+1 < len(text); i++ {
		if text[i] != '-' || text[i+1] != 'D' {
			continue
		}
		// The character before must not be part of a longer word, so
		// `--Dfoo=` or `x-Dfoo=` is not read as an option.
		if i > 0 && !isArgBoundary(text[i-1]) {
			continue
		}

		rest := text[i+2:]
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			// No `=` left on this line: `-DNDEBUG` in CFLAGS, or a wrapped
			// argument. Either way there is no name/value pair to compare.
			break
		}
		name := rest[:eq]
		i += 2 + eq

		switch {
		case name == "":
			// `-D=value` assigns nothing; there is no name to compare.
		case strings.Contains(name, "$"):
			// R2.3: reported with the text as written, never dropped.
			passed.Unresolved = append(passed.Unresolved, Unresolved{
				Text: "-D" + name + "=",
				Line: line,
			})
		default:
			subproject, bare := splitSubproject(name)
			if !validOptionName(bare) {
				continue
			}
			opt := Option{Name: bare, Subproject: subproject, Source: source}
			// Classified before it can reach Project: a built-in that got as
			// far as the comparison would be checked against declarations no
			// option file ever carries, and become a false error.
			if isBuiltInOption(bare) {
				passed.BuiltIn = append(passed.BuiltIn, opt)
				continue
			}
			passed.Project = append(passed.Project, opt)
		}
	}
}

// splitSubproject separates `<subproject>:<name>` into its two halves. A name
// with no colon belongs to the root project.
func splitSubproject(name string) (subproject, bare string) {
	if sub, rest, ok := strings.Cut(name, ":"); ok {
		return sub, rest
	}
	return "", name
}

// validOptionName reports whether bare looks like an option name at all.
//
// Anything else is skipped rather than reported: a name side that is neither an
// identifier nor an expansion is not an option this gate can say anything
// about, and inventing a finding for it would be noise.
func validOptionName(bare string) bool {
	if bare == "" || !isIdentByte(bare[0]) {
		return false
	}
	for i := 1; i < len(bare); i++ {
		if c := bare[i]; !isIdentByte(c) && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// isArgBoundary reports whether c can precede an argument on a command line.
func isArgBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '(', '"', '\'', '`':
		return true
	}
	return false
}
