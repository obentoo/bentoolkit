package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
)

// =============================================================================
// The autoupdate command's distfile directories (S030 sub-task 5.2, R1.3)
//
// R1.3 asks for ONE vocabulary: the autoupdate path must accept the distdir and
// the distfiles cache under the same names, and with the same meaning, that
// `overlay manifest` already uses. So these tests compare the two commands
// directly rather than restating what either one is expected to say.
//
// # The safety rule these tests are written around
//
// The directory under discussion is, in production, /var/cache/distfiles — this
// host's real DISTDIR, holding thousands of real distfiles, and the manifest
// step quarantines (renames) and cleans up (deletes) inside it. No test here may
// reach it. Two rules keep that true:
//
//   - every path a test hands to distfiles.Resolve is a directory the test
//     itself created under t.TempDir(), so the probe and any cleanup act on the
//     test's own tree;
//   - the DEFAULT is only ever compared as a STRING. "the default equals
//     distfiles.DefaultCache" is a fact about a flag's registered value, and
//     asking the filesystem about it would prove nothing extra while touching
//     the one directory that must not be touched.
// =============================================================================

// distdirTestState saves and restores the package-level flag variables these
// tests drive. They are process-global (pflag binds them at init), so a test
// that left one modified would change the behaviour of every test that runs
// after it.
func distdirTestState(t *testing.T) {
	t.Helper()
	origDistdir, origCache, origDirs := autoupdateDistdir, autoupdateDistfilesCache, autoupdateDirs
	t.Cleanup(func() {
		autoupdateDistdir = origDistdir
		autoupdateDistfilesCache = origCache
		autoupdateDirs = origDirs
	})
}

// ownedDir creates a directory inside this test's own temporary tree and
// returns it. Every path these tests resolve comes from here — see the safety
// rule at the top of the file.
func ownedDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating the test's own directory %s: %v", dir, err)
	}
	return dir
}

// resolvedPath runs the real precedence over the two rungs the command
// produced, and returns the directory it chose.
//
// It calls distfiles.Resolve — the production resolver — on purpose: the
// precedence is ITS behaviour, and asserting on the two rungs alone would only
// prove that this command copied two strings. It is safe here precisely because
// the caller has already named a directory it owns: Resolve consults the host
// (portageq distdir) and then /var/cache/distfiles only when BOTH rungs are
// empty, which no caller of this helper does.
func resolvedPath(t *testing.T, dirs autoupdateDistfileDirs) string {
	t.Helper()
	if dirs.Distdir == "" && dirs.ConfiguredDistdir == "" {
		t.Fatal("refusing to resolve with nothing named: that is the rung that reaches this host's own DISTDIR")
	}
	dir, err := distfiles.Resolve(dirs.Distdir, dirs.ConfiguredDistdir)
	if err != nil {
		t.Fatalf("resolving distdir(explicit=%q, configured=%q): %v", dirs.Distdir, dirs.ConfiguredDistdir, err)
	}
	t.Cleanup(dir.Cleanup) // no-op for a pre-existing directory, which is all these tests pass
	return dir.Path
}

// captureStderr collects what fn writes to os.Stderr. config.LoadFrom reports an
// unknown YAML key there as a warning and keeps going, so "the key was accepted"
// is only observable by reading that stream.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating the stderr pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatalf("closing the stderr pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("reading the captured stderr: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("closing the read end: %v", err)
	}
	return buf.String()
}

// TestAutoupdateDistdirFlagOverridesConfig is R1.3's precedence: --distdir beats
// autoupdate.distdir, and the config key is what answers when the flag is
// absent.
//
// The assertion is on the directory the run ACTUALLY works in, not on which
// field the value landed in, because "the flag takes precedence" is a claim
// about the resolved directory. The two rungs are carried separately all the way
// down to distfiles.Resolve (S030-D2), so a wiring that dropped the flag into
// the config rung would still produce the right pair of strings and the wrong
// answer the moment both are set.
func TestAutoupdateDistdirFlagOverridesConfig(t *testing.T) {
	t.Run("the flag wins over the config key", func(t *testing.T) {
		distdirTestState(t)
		fromFlag := ownedDir(t, "from-flag")
		fromConfig := ownedDir(t, "from-config")

		autoupdateDistdir = fromFlag
		cfg := &config.Config{}
		cfg.Autoupdate.Distdir = fromConfig

		dirs := resolveAutoupdateDistfileDirs(cfg, false)
		if dirs.ConfiguredDistdir != fromConfig {
			t.Errorf("the config rung = %q, want %q: the key must still be carried, not discarded", dirs.ConfiguredDistdir, fromConfig)
		}
		if got := resolvedPath(t, dirs); got != fromFlag {
			t.Errorf("resolved %q, want the --distdir flag's %q (the config key named %q)", got, fromFlag, fromConfig)
		}
	})

	t.Run("the config key answers when the flag is absent", func(t *testing.T) {
		distdirTestState(t)
		fromConfig := ownedDir(t, "from-config")

		autoupdateDistdir = ""
		cfg := &config.Config{}
		cfg.Autoupdate.Distdir = fromConfig

		if got := resolvedPath(t, resolveAutoupdateDistfileDirs(cfg, false)); got != fromConfig {
			t.Errorf("resolved %q, want the configured %q", got, fromConfig)
		}
	})

	t.Run("neither named invents nothing", func(t *testing.T) {
		distdirTestState(t)
		autoupdateDistdir = ""

		dirs := resolveAutoupdateDistfileDirs(&config.Config{}, false)
		// Deliberately NOT resolved: with both rungs empty the precedence asks
		// this host for its own DISTDIR, which is the directory these tests must
		// never act in. What matters at this layer is that the command passes on
		// two empty strings rather than substituting a directory of its own —
		// the temporary directory it used to invent is exactly what S030-R1.1
		// removed.
		if dirs.Distdir != "" || dirs.ConfiguredDistdir != "" {
			t.Errorf("with nothing named the command produced (%q, %q), want two empty strings so the host's own DISTDIR answers", dirs.Distdir, dirs.ConfiguredDistdir)
		}
	})

	t.Run("a relative config path is refused, not resolved against an arbitrary cwd", func(t *testing.T) {
		distdirTestState(t)
		autoupdateDistdir = ""
		cfg := &config.Config{}
		cfg.Autoupdate.Distdir = "relative/distfiles"

		if dirs := resolveAutoupdateDistfileDirs(cfg, false); dirs.ConfiguredDistdir != "" {
			t.Errorf("the config rung = %q, want %q: a relative path in a config file resolves against whatever directory the process started in", dirs.ConfiguredDistdir, "")
		}
	})

	t.Run("both keys decode from the config file without an unknown-field warning", func(t *testing.T) {
		// The strict re-decode in LoadFrom uses a SECOND struct (probeConfig)
		// beside Config, and a key the second one does not know is reported as
		// unknown even though the first accepted it. Both targets reuse
		// AutoupdateConfig itself, so the new keys reach both — but that is
		// exactly the kind of thing that is true by inspection and false in
		// practice, so it is asserted here.
		distdir := ownedDir(t, "configured-distdir")
		cache := ownedDir(t, "configured-cache")
		path := filepath.Join(t.TempDir(), "config.yaml")
		body := "overlay:\n  path: " + ownedDir(t, "overlay") + "\nautoupdate:\n  distdir: " + distdir + "\n  distfiles_cache: " + cache + "\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("writing the config file: %v", err)
		}

		var cfg *config.Config
		var loadErr error
		stderr := captureStderr(t, func() { cfg, loadErr = config.LoadFrom(path) })
		if loadErr != nil {
			t.Fatalf("config.LoadFrom(%s): %v", path, loadErr)
		}
		if strings.Contains(stderr, "field distdir") || strings.Contains(stderr, "field distfiles_cache") {
			t.Errorf("loading the config reported an unknown key; the strict re-decode does not know these fields:\n%s", stderr)
		}
		if cfg.Autoupdate.GetDistdir() != distdir {
			t.Errorf("autoupdate.distdir decoded as %q, want %q", cfg.Autoupdate.GetDistdir(), distdir)
		}
		if cfg.Autoupdate.GetDistfilesCache() != cache {
			t.Errorf("autoupdate.distfiles_cache decoded as %q, want %q", cfg.Autoupdate.GetDistfilesCache(), cache)
		}
	})
}

// TestAutoupdateDistfilesCacheDefaultMatchesManifestCommand pins the half of
// R1.3 that IS shared verbatim: both commands consult the same cache directory
// by default, so an operator who has one populated gets the reuse on both paths
// without configuring anything.
//
// Every assertion here is a string comparison. The default names a directory
// holding thousands of real distfiles; asking the filesystem about it would add
// nothing to what "the two registered defaults are equal" already proves.
func TestAutoupdateDistfilesCacheDefaultMatchesManifestCommand(t *testing.T) {
	autoFlag := autoupdateCmd.Flags().Lookup("distfiles-cache")
	manifestFlag := manifestCmd.Flags().Lookup("distfiles-cache")
	if autoFlag == nil || manifestFlag == nil {
		t.Fatalf("--distfiles-cache is missing: autoupdate has it = %t, manifest has it = %t", autoFlag != nil, manifestFlag != nil)
	}

	if autoFlag.DefValue != manifestFlag.DefValue {
		t.Errorf("autoupdate --distfiles-cache defaults to %q but overlay manifest defaults to %q; R1.3 is one cache under one name", autoFlag.DefValue, manifestFlag.DefValue)
	}
	if autoFlag.DefValue != distfiles.DefaultCache {
		t.Errorf("autoupdate --distfiles-cache defaults to %q, want distfiles.DefaultCache (%q)", autoFlag.DefValue, distfiles.DefaultCache)
	}

	t.Run("an unset flag with no config key resolves to that same default", func(t *testing.T) {
		distdirTestState(t)
		autoupdateDistfilesCache = autoFlag.DefValue // what pflag leaves there when the flag is not passed
		if got := resolveAutoupdateDistfileDirs(&config.Config{}, false).Cache; got != distfiles.DefaultCache {
			t.Errorf("with no flag and no config key the cache resolved to %q, want %q", got, distfiles.DefaultCache)
		}
	})

	t.Run("the config key answers when the flag is not passed", func(t *testing.T) {
		distdirTestState(t)
		autoupdateDistfilesCache = autoFlag.DefValue
		cfg := &config.Config{}
		cfg.Autoupdate.DistfilesCache = "/srv/mirror/distfiles"
		if got := resolveAutoupdateDistfileDirs(cfg, false).Cache; got != "/srv/mirror/distfiles" {
			t.Errorf("the cache resolved to %q, want the configured %q", got, "/srv/mirror/distfiles")
		}
	})

	t.Run("a passed flag beats the config key, including the empty string that disables it", func(t *testing.T) {
		distdirTestState(t)
		cfg := &config.Config{}
		cfg.Autoupdate.DistfilesCache = "/srv/mirror/distfiles"

		autoupdateDistfilesCache = "/srv/other/distfiles"
		if got := resolveAutoupdateDistfileDirs(cfg, true).Cache; got != "/srv/other/distfiles" {
			t.Errorf("the cache resolved to %q, want the flag's %q", got, "/srv/other/distfiles")
		}

		// "" is a MEANING for this flag — it disables the lookup, which is how
		// `overlay manifest` documents it — so a passed empty flag must not be
		// read as "unset" and quietly replaced by the config key.
		autoupdateDistfilesCache = ""
		if got := resolveAutoupdateDistfileDirs(cfg, true).Cache; got != "" {
			t.Errorf("--distfiles-cache \"\" resolved to %q, want \"\": passing it explicitly disables the lookup", got)
		}
	})
}

// TestAutoupdateDistdirFlagNamesMatchManifestCommand is R1.3 at its most
// literal: the same two names, spelled identically, taking the same kind of
// value on both commands. A synonym here — --dist-dir, --distfiles-dir — is the
// defect this requirement exists to prevent, and it is the kind that only
// surfaces when an operator has already typed the wrong one.
func TestAutoupdateDistdirFlagNamesMatchManifestCommand(t *testing.T) {
	for _, name := range []string{"distdir", "distfiles-cache"} {
		autoFlag := autoupdateCmd.Flags().Lookup(name)
		manifestFlag := manifestCmd.Flags().Lookup(name)
		if manifestFlag == nil {
			t.Fatalf("overlay manifest has no --%s; this test compares against it and can no longer do so", name)
		}
		if autoFlag == nil {
			t.Errorf("the autoupdate command has no --%s, but overlay manifest does (R1.3: one vocabulary for one concept)", name)
			continue
		}
		if autoFlag.Value.Type() != manifestFlag.Value.Type() {
			t.Errorf("--%s takes a %s on autoupdate and a %s on overlay manifest", name, autoFlag.Value.Type(), manifestFlag.Value.Type())
		}
		// pflag reads the first back-quoted word of a usage string as the
		// flag's value placeholder and strips the quotes, so a back-quoted
		// phrase renders as "--distdir portageq distdir" in --help.
		if strings.Contains(autoFlag.Usage, "`") {
			t.Errorf("--%s's usage contains a back-quoted word, which pflag turns into the value placeholder: %q", name, autoFlag.Usage)
		}
		// Both names must be documented where an operator looks for them.
		if !strings.Contains(autoupdateCmd.Long, "--"+name) {
			t.Errorf("--%s is not documented in the autoupdate command's long help", name)
		}
	}

	// The one thing that is deliberately NOT copied. `overlay manifest` says an
	// unset --distdir means "temporary directory removed after run", and that is
	// true there. On this path it is false by design: S030 replaced the
	// temporary directory with the host's own DISTDIR precisely because the
	// temporary one was on a tmpfs, so every distfile a bump fetched went into
	// RAM. Copying the sentence would ship a flag whose help contradicts its
	// code, which is worse than two differently-worded helps.
	usage := autoupdateCmd.Flags().Lookup("distdir").Usage
	if strings.Contains(strings.ToLower(usage), "temporary directory") {
		t.Errorf("--distdir's help claims a temporary directory, which this path no longer uses (S030-R1.1): %q", usage)
	}
	if !strings.Contains(usage, "DISTDIR") {
		t.Errorf("--distdir's help does not say what its default actually is: %q", usage)
	}
}
