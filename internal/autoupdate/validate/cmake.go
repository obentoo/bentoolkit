package validate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// systemCMake is Declared.System for a CMake project.
const systemCMake = "cmake"

// cmakeChunk is how many CMake files one tar call extracts: one pass over the
// archive per chunk, not per file, and well under maxArchiveBytes for the
// small text files CMake sources are.
const cmakeChunk = 100

// cmakeOptionCall matches option() and cmake_dependent_option(), the commands
// that declare a project's public switches. CMake commands are
// case-insensitive; the leading boundary keeps add_compile_options( and
// target_compile_options( out.
var cmakeOptionCall = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(?:cmake_dependent_option|option)\s*\(\s*([A-Za-z_][A-Za-z0-9_-]*)`)

// cmakeIdentifier is any name CMake source can mention. A hyphen is allowed:
// option(python-bindings ...) is a valid CMake option.
var cmakeIdentifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_-]*`)

// cmakeBuiltName matches a name assembled at configure time, such as
// BUILD_PLUGIN_${NAME} or ENABLE_${pkg}: its literal prefix is captured.
var cmakeBuiltName = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\$\{`)

// cmakeFromArchive reads a CMake project's option surface: the options its
// option() and cmake_dependent_option() calls declare, and every identifier
// any of its CMake files mentions.
//
// # Why "mentioned anywhere", not "declared by option()"
//
// An ebuild passes -D for much more than options: cache variables set(... CACHE),
// plain variables read with if() or ${}, switches a subdirectory or an included
// module declares. Calling each of those undeclared would be the webkit-gtk
// failure again (89 false errors, see projectRoot), and the first false error
// is what gets a gate switched off. So an error needs the name to appear in no
// CMake file of the release at all: that is what an option upstream removed
// looks like, and nothing else does.
func cmakeFromArchive(ctx context.Context, archive string, members []string) (Declared, error) {
	var files []string
	for _, m := range members {
		if strings.HasSuffix(m, "/") {
			continue
		}
		if base := path.Base(m); base == "CMakeLists.txt" || strings.HasSuffix(base, ".cmake") {
			if validMemberName(m) == nil {
				files = append(files, m)
			}
		}
	}

	declared := Declared{System: systemCMake, Known: map[string]bool{}, BuiltPrefixes: map[string]bool{}}
	seen := map[string]bool{}
	source := path.Base(archive) + " CMake sources"
	for start := 0; start < len(files); start += cmakeChunk {
		chunk := files[start:min(start+cmakeChunk, len(files))]
		args := append([]string{"-xOf", archive, "--"}, chunk...)
		out, err := runTar(ctx, archive, fmt.Sprintf("extracting %d CMake file(s)", len(chunk)), args...)
		if err != nil {
			return Declared{}, fmt.Errorf("reading the CMake sources of %s: %w", archive, err)
		}
		text := stripCMakeComments(out)
		declared.learn(text)
		for _, match := range cmakeOptionCall.FindAllSubmatch(text, -1) {
			name := string(match[1])
			if !seen[name] {
				seen[name] = true
				declared.Root = append(declared.Root, Option{Name: name, Source: source})
			}
		}
	}
	declared.Sources = append(declared.Sources, fmt.Sprintf("%d CMake file(s)", len(files)))
	return declared, nil
}

// stripCMakeComments drops line comments, so an option a release commented out
// does not count as declared. A '#' inside a quoted string is kept: the
// comment starts only at a '#' outside quotes.
func stripCMakeComments(src []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(src))
	for _, line := range bytes.Split(src, []byte("\n")) {
		inQuote := false
		cut := len(line)
		for i, c := range line {
			switch {
			case c == '"' && (i == 0 || line[i-1] != '\\'):
				inQuote = !inQuote
			case c == '#' && !inQuote:
				cut = i
			}
			if cut != len(line) {
				break
			}
		}
		out.Write(line[:cut])
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// cmakeBuiltinPrefixes and cmakeBuiltinSuffixes name the variables an ebuild
// sets that CMake itself, find_package() or a framework module defines, so a
// release's own sources need not mention them.
var (
	cmakeBuiltinPrefixes = []string{"CMAKE_", "FETCHCONTENT_", "KDE_INSTALL_", "ECM_", "QT_", "Qt5", "Qt6", "Python_", "Python3_", "PYTHON_"}
	cmakeBuiltinSuffixes = []string{"_DIR", "_ROOT", "_EXECUTABLE", "_INCLUDE_DIR", "_INCLUDE_DIRS", "_LIBRARY", "_LIBRARIES"}
	cmakeBuiltinNames    = map[string]bool{"BUILD_SHARED_LIBS": true, "BUILD_TESTING": true}
)

// isCMakeBuiltin reports whether name is defined by CMake or a module rather
// than by the project.
func isCMakeBuiltin(name string) bool {
	if cmakeBuiltinNames[name] {
		return true
	}
	for _, p := range cmakeBuiltinPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, s := range cmakeBuiltinSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// learn adds every name text mentions to Known, and the literal prefix of every
// name it assembles at configure time to BuiltPrefixes.
func (d *Declared) learn(text []byte) {
	for _, name := range cmakeIdentifier.FindAll(text, -1) {
		d.Known[string(name)] = true
	}
	for _, m := range cmakeBuiltName.FindAllSubmatch(text, -1) {
		if prefix := string(m[1]); len(prefix) >= 3 {
			d.BuiltPrefixes[prefix] = true
		}
	}
}

// LearnCMakePatches adds what the package's own patches mention: a patch in
// files/ can add an option the release does not have (mbedtls's
// ENABLE_SLOTTED_VERSION), and the ebuild that passes it is right.
func (d *Declared) LearnCMakePatches(pkgDir string) {
	if d.System != systemCMake {
		return
	}
	for _, pattern := range []string{"*.patch", "*.diff"} {
		matches, _ := filepath.Glob(filepath.Join(pkgDir, "files", pattern))
		for _, m := range matches {
			if body, err := os.ReadFile(m); err == nil { //nolint:gosec // G304: a patch of the package under validation
				d.learn(body)
			}
		}
	}
}

// cmakeKnows reports whether a name the ebuild passes is accounted for by the
// release: mentioned by its CMake files or the package's patches, defined by
// CMake or a module, or assembled at configure time from a known prefix.
func (d Declared) cmakeKnows(name string) bool {
	if d.Known[name] || isCMakeBuiltin(name) {
		return true
	}
	for prefix := range d.BuiltPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
