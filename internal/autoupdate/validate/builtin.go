package validate

import "strings"

// Meson's built-in options — the ones Meson defines itself and no project
// declares in an option file. Reference:
// https://mesonbuild.com/Builtin-options.html
//
// # Why this table has to be right, in both directions
//
// A built-in this table MISSES is compared against the archive's declarations,
// finds nothing, and becomes a FALSE ERROR. That is the worse direction: the
// first false error is what gets a gate switched off, and then every true one
// goes unread too. Measured against the live overlay on 2026-08-07, exactly two
// ebuilds pass a built-in, both `-Db_ndebug=`.
//
// A table too WIDE is the mirror failure — a real project option swallowed by
// an over-eager rule is never compared, so a removed upstream option passes
// unnoticed and the gate silently stops doing its job. That is why the compiler
// rules below are anchored to Meson's actual language list instead of matching
// any name that happens to end in `_args`: `option('extra_args')` is a name a
// project may legitimately declare.
//
// Keep this in sync with the reference above when Meson adds an option.

// builtinFixed holds the built-ins with fixed names — the directory family and
// the core options.
var builtinFixed = map[string]bool{
	// Directory options.
	"prefix": true, "bindir": true, "datadir": true, "includedir": true,
	"infodir": true, "libdir": true, "libexecdir": true, "licensedir": true,
	"localedir": true, "localstatedir": true, "mandir": true, "sbindir": true,
	"sharedstatedir": true, "sysconfdir": true,

	// Core options.
	"auto_features": true, "backend": true, "backend_max_links": true,
	"buildtype": true, "cmake_prefix_path": true, "debug": true,
	"default_both_libraries": true, "default_library": true, "errorlogs": true,
	"force_fallback_for": true, "genvslite": true, "install_umask": true,
	"layout": true, "optimization": true, "pkg_config_path": true,
	"prefer_static": true, "stdsplit": true, "strip": true, "unity": true,
	"unity_size": true, "vsenv": true, "warning_level": true, "werror": true,
	"wrap_mode": true,

	// Machine-file options, accepted on the command line like the rest.
	"cross_file": true, "native_file": true,
}

// mesonLanguages is the set Meson recognises, and it is what anchors the
// compiler-option rules below. Without it, `<lang>_args` degenerates into "any
// name ending in _args" and starts swallowing project options.
var mesonLanguages = map[string]bool{
	"c": true, "cpp": true, "cs": true, "cuda": true, "cython": true,
	"d": true, "fortran": true, "java": true, "masm": true, "nasm": true,
	"objc": true, "objcpp": true, "rust": true, "swift": true, "vala": true,
	"linearasm": true,
}

// compilerSuffixes are the per-language option families.
var compilerSuffixes = []string{
	"_args", "_link_args", "_std", "_winlibs",
	"_eh", "_rtti", "_debugstl", "_thread_count", "_module_std",
}

// isBuiltInOption reports whether a passed option name is one Meson defines
// itself, and therefore one no option file will ever declare (R2.4).
//
// The name given here is already stripped of any `<subproject>:` prefix: a
// built-in addressed at a subproject is still a built-in, and comparing it
// against that subproject's declarations would be the same false error one
// namespace along.
func isBuiltInOption(name string) bool {
	if builtinFixed[name] {
		return true
	}
	// Base options: the whole `b_` family (b_ndebug, b_lto, b_sanitize, …).
	// Open-ended by design, so it is matched by rule rather than enumerated.
	if strings.HasPrefix(name, "b_") {
		return true
	}
	// Module options are written `-D<module>.<option>=`, and the `build.`
	// prefix that addresses the build machine in a cross build has the same
	// shape. A dot is reserved for exactly this: Meson does not allow one in a
	// project option name, so the rule cannot swallow a real declaration.
	if strings.Contains(name, ".") {
		return true
	}
	return isCompilerOption(name)
}

// isCompilerOption matches the per-language families, anchored to a real Meson
// language so `extra_args` — a name a project may declare — is not mistaken for
// one of them.
func isCompilerOption(name string) bool {
	for _, suffix := range compilerSuffixes {
		lang, ok := strings.CutSuffix(name, suffix)
		if ok && mesonLanguages[lang] {
			return true
		}
	}
	return false
}
