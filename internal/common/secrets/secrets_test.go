package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// withPaths points the package path seam at the given user/system files for the
// duration of the test and also isolates HOME to a fresh tempdir, so no test
// ever reads the developer's real ~/.config/bentoo/secrets or /etc/bentoo/secrets
// (mandatory isolation, precedent commit a77de4b). Never combined with
// t.Parallel() because t.Setenv forbids it.
func withPaths(t *testing.T, userPath, sysPath string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	orig := pathsFn
	pathsFn = func() []scopedPath {
		return []scopedPath{{name: userPath, user: true}, {name: sysPath, user: false}}
	}
	t.Cleanup(func() { pathsFn = orig })
}

// withSystemOnlyPath points the path seam at a SINGLE system-scope entry — the
// exact shape pathsFn produces when os.UserHomeDir() fails and the user-scope
// entry is dropped. HOME and XDG_CONFIG_HOME are still isolated so the chain can
// never reach the developer's real files (D9).
func withSystemOnlyPath(t *testing.T, sysPath string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	orig := pathsFn
	pathsFn = func() []scopedPath { return []scopedPath{{name: sysPath, user: false}} }
	t.Cleanup(func() { pathsFn = orig })
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestLookup_Chain pins the fixed resolution order: env → user file → system
// file, and the "total miss" contract (found=false, err=nil).
func TestLookup_Chain(t *testing.T) {
	t.Run("env hit wins over files and trims", func(t *testing.T) {
		dir := t.TempDir()
		userPath := filepath.Join(dir, "user")
		sysPath := filepath.Join(dir, "sys")
		writeFile(t, userPath, "TOK=fromfile\n", 0o600)
		withPaths(t, userPath, sysPath)
		t.Setenv("TOK", "  fromenv  ")

		got, found, err := Lookup("TOK")
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if !found || got != "fromenv" {
			t.Fatalf("Lookup = (%q, %v), want (fromenv, true)", got, found)
		}
	})

	t.Run("empty and whitespace env fall through to user file", func(t *testing.T) {
		for _, envVal := range []string{"", "   "} {
			dir := t.TempDir()
			userPath := filepath.Join(dir, "user")
			sysPath := filepath.Join(dir, "sys")
			writeFile(t, userPath, "TOK=filevalue\n", 0o600)
			withPaths(t, userPath, sysPath)
			t.Setenv("TOK", envVal)

			got, found, err := Lookup("TOK")
			if err != nil {
				t.Fatalf("env=%q: err = %v", envVal, err)
			}
			if !found || got != "filevalue" {
				t.Fatalf("env=%q: Lookup = (%q, %v), want (filevalue, true)", envVal, got, found)
			}
		}
	})

	t.Run("user scope wins over system scope", func(t *testing.T) {
		dir := t.TempDir()
		userPath := filepath.Join(dir, "user")
		sysPath := filepath.Join(dir, "sys")
		writeFile(t, userPath, "TOK=user\n", 0o600)
		writeFile(t, sysPath, "TOK=system\n", 0o600)
		withPaths(t, userPath, sysPath)
		t.Setenv("TOK", "")

		got, found, err := Lookup("TOK")
		if err != nil || !found || got != "user" {
			t.Fatalf("Lookup = (%q, %v, %v), want (user, true, nil)", got, found, err)
		}
	})

	t.Run("system scope used when user misses", func(t *testing.T) {
		dir := t.TempDir()
		userPath := filepath.Join(dir, "user") // absent
		sysPath := filepath.Join(dir, "sys")
		writeFile(t, sysPath, "TOK=system\n", 0o600)
		withPaths(t, userPath, sysPath)
		t.Setenv("TOK", "")

		got, found, err := Lookup("TOK")
		if err != nil || !found || got != "system" {
			t.Fatalf("Lookup = (%q, %v, %v), want (system, true, nil)", got, found, err)
		}
	})

	t.Run("total miss returns found=false and nil error", func(t *testing.T) {
		dir := t.TempDir()
		withPaths(t, filepath.Join(dir, "user"), filepath.Join(dir, "sys"))
		t.Setenv("TOK", "")

		got, found, err := Lookup("TOK")
		if err != nil {
			t.Fatalf("err = %v, want nil (absent file is a miss, not an error)", err)
		}
		if found || got != "" {
			t.Fatalf("Lookup = (%q, %v), want (\"\", false)", got, found)
		}
	})
}

// TestParse pins the .env-style parser semantics that must survive the promotion
// from authfetch: comments, export prefix, matched-quote stripping, first-wins on
// duplicates, blank value as a miss, invalid lines skipped, and the A=b=c== rule.
func TestParse(t *testing.T) {
	cases := []struct {
		name      string
		file      string
		lookup    string
		wantVal   string
		wantFound bool
	}{
		{"comment lines skipped", "# a comment\nTOK=value\n", "TOK", "value", true},
		{"export prefix stripped", "export TOK=value\n", "TOK", "value", true},
		{"double-quotes stripped", "TOK=\"value\"\n", "TOK", "value", true},
		{"single-quotes stripped", "TOK='value'\n", "TOK", "value", true},
		{"value keeps inner equals", "TOK=b=c==\n", "TOK", "b=c==", true},
		{"first occurrence wins", "TOK=first\nTOK=second\n", "TOK", "first", true},
		{"blank value is a miss", "TOK=\n", "TOK", "", false},
		{"invalid line skipped, later key found", "noequalshere\nTOK=value\n", "TOK", "value", true},
		{"absent key is a miss", "OTHER=x\n", "TOK", "", false},
		// A file authored on Windows must resolve identically: the parser splits on
		// "\n", so the trailing "\r" has to be trimmed off rather than becoming part
		// of the value. Pins behavior that is already correct today (R1.2).
		{"CRLF line endings leave no trailing carriage return", "TOK=value\r\n", "TOK", "value", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			userPath := filepath.Join(dir, "user")
			writeFile(t, userPath, tc.file, 0o600)
			withPaths(t, userPath, filepath.Join(dir, "sys"))
			t.Setenv(tc.lookup, "")

			got, found, err := Lookup(tc.lookup)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if found != tc.wantFound || got != tc.wantVal {
				t.Fatalf("Lookup = (%q, %v), want (%q, %v)", got, found, tc.wantVal, tc.wantFound)
			}
		})
	}
}

// TestLookup_ErrorMapping pins D1/D2: absent file is a miss, a present-but-
// unreadable user-scope file is ErrUnreadable, an EISDIR user path is
// ErrUnreadable, and a permission-denied SYSTEM-scope file is a silent miss.
func TestLookup_ErrorMapping(t *testing.T) {
	t.Run("EISDIR on user path maps to ErrUnreadable", func(t *testing.T) {
		dir := t.TempDir()
		userPath := filepath.Join(dir, "userdir")
		if err := os.MkdirAll(userPath, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		withPaths(t, userPath, filepath.Join(dir, "sys"))
		t.Setenv("TOK", "")

		_, _, err := Lookup("TOK")
		if !errors.Is(err, ErrUnreadable) {
			t.Fatalf("err = %v, want wrapping ErrUnreadable", err)
		}
	})

	t.Run("unreadable user-scope file maps to ErrUnreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores permission bits; assertion is vacuous")
		}
		dir := t.TempDir()
		userPath := filepath.Join(dir, "user")
		writeFile(t, userPath, "TOK=value\n", 0o000)
		withPaths(t, userPath, filepath.Join(dir, "sys"))
		t.Setenv("TOK", "")

		_, _, err := Lookup("TOK")
		if !errors.Is(err, ErrUnreadable) {
			t.Fatalf("err = %v, want wrapping ErrUnreadable", err)
		}
	})

	t.Run("permission-denied system-scope file is a silent miss", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores permission bits; assertion is vacuous")
		}
		dir := t.TempDir()
		sysPath := filepath.Join(dir, "sys")
		writeFile(t, sysPath, "TOK=value\n", 0o000)
		withPaths(t, filepath.Join(dir, "user"), sysPath)
		t.Setenv("TOK", "")

		got, found, err := Lookup("TOK")
		if err != nil {
			t.Fatalf("system-scope EACCES must be a silent miss, got err = %v", err)
		}
		if found || got != "" {
			t.Fatalf("Lookup = (%q, %v), want (\"\", false)", got, found)
		}
	})

	// F-1: when os.UserHomeDir() fails, pathsFn drops the user-scope entry and the
	// system file lands at index 0. Scope must be carried by the entry itself and
	// never inferred from list position — otherwise the expected EACCES on a
	// root-owned /etc/bentoo/secrets is reported as ErrUnreadable and every Lookup
	// fails hard for a normal user whose HOME is unset.
	t.Run("system scope stays system scope when it is the only path", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores permission bits; assertion is vacuous")
		}
		dir := t.TempDir()
		sysPath := filepath.Join(dir, "sys")
		writeFile(t, sysPath, "TOK=value\n", 0o000)
		withSystemOnlyPath(t, sysPath)
		t.Setenv("TOK", "")

		got, found, err := Lookup("TOK")
		if err != nil {
			t.Fatalf("EACCES on the system-scope file must be a silent miss even when it is the only entry, got err = %v", err)
		}
		if found || got != "" {
			t.Fatalf("Lookup = (%q, %v), want (\"\", false)", got, found)
		}
	})
}

// TestLookup_LooseModeStillReads pins D5's "warn, never block": a group/world-
// accessible user file (mode & 0o077 != 0) still returns its value.
func TestLookup_LooseModeStillReads(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits; loose-mode path is not exercised")
	}
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user")
	writeFile(t, userPath, "TOK=value\n", 0o644) // 0o644 & 0o077 != 0 → loose
	withPaths(t, userPath, filepath.Join(dir, "sys"))
	t.Setenv("TOK", "")

	got, found, err := Lookup("TOK")
	if err != nil {
		t.Fatalf("loose mode must warn, not block: err = %v", err)
	}
	if !found || got != "value" {
		t.Fatalf("Lookup = (%q, %v), want (value, true)", got, found)
	}
}

// recordingLogger captures Warn calls so the loose-mode warning can be asserted
// directly instead of by capturing os.Stderr.
type recordingLogger struct{ lines []string }

func (r *recordingLogger) Warn(format string, args ...interface{}) {
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

// withRecordingLogger swaps the package's default stderr logger for a recorder
// and resets the once-guard, giving each test a fresh process-lifetime warning
// budget. The reset is what makes the "at most once" assertion real rather than
// vacuous: without it, a warning spent by an earlier test would make any later
// count of zero-or-one pass regardless of the guard.
func withRecordingLogger(t *testing.T) *recordingLogger {
	t.Helper()
	rec := &recordingLogger{}
	origLogger, origOnce := warnLogger, looseWarnOnce
	warnLogger = rec
	looseWarnOnce = new(sync.Once)
	t.Cleanup(func() {
		warnLogger = origLogger
		looseWarnOnce = origOnce
	})
	return rec
}

// TestLooseModeWarning pins R6.2: a group/world-accessible secrets file warns at
// most ONCE per process, the warning identifies the offending file and its mode,
// and it never echoes the secret it just read (R6.1).
func TestLooseModeWarning(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits; the loose-mode path is not exercised")
	}

	t.Run("two reads of a loose file emit exactly one warning", func(t *testing.T) {
		const secretValue = "s3cr3t-token-value"
		dir := t.TempDir()
		userPath := filepath.Join(dir, "user")
		writeFile(t, userPath, "TOK="+secretValue+"\n", 0o644)
		if err := os.Chmod(userPath, 0o644); err != nil { // defeat a restrictive umask
			t.Fatalf("chmod: %v", err)
		}
		withPaths(t, userPath, filepath.Join(dir, "sys"))
		t.Setenv("TOK", "")
		rec := withRecordingLogger(t)

		for i := 1; i <= 2; i++ {
			if _, found, err := Lookup("TOK"); err != nil || !found {
				t.Fatalf("read %d: Lookup = (found %v, err %v), want a hit", i, found, err)
			}
		}

		if len(rec.lines) != 1 {
			t.Fatalf("got %d warnings %q, want exactly 1 (R6.2: at most once per process)", len(rec.lines), rec.lines)
		}
		warning := rec.lines[0]
		if !strings.Contains(warning, userPath) {
			t.Errorf("warning %q does not name the offending file %q", warning, userPath)
		}
		if !strings.Contains(warning, "644") {
			t.Errorf("warning %q does not report the file mode", warning)
		}
		if strings.Contains(warning, secretValue) {
			t.Fatalf("warning leaked the secret value: %q", warning)
		}
	})

	t.Run("an owner-only file emits no warning", func(t *testing.T) {
		dir := t.TempDir()
		userPath := filepath.Join(dir, "user")
		writeFile(t, userPath, "TOK=value\n", 0o600)
		withPaths(t, userPath, filepath.Join(dir, "sys"))
		t.Setenv("TOK", "")
		rec := withRecordingLogger(t)

		if _, found, err := Lookup("TOK"); err != nil || !found {
			t.Fatalf("Lookup = (found %v, err %v), want a hit", found, err)
		}
		if len(rec.lines) != 0 {
			t.Fatalf("got %d warnings %q, want none for a 0600 file", len(rec.lines), rec.lines)
		}
	})
}

// TestPaths pins the EXACT chain Lookup searches and its order: the user-scope
// file under $XDG_CONFIG_HOME/bentoo/secrets first, then the fixed
// /etc/bentoo/secrets literal. It drives the REAL pathsFn — deliberately not the
// withPaths override — because the layout itself is what is under test, and it
// isolates BOTH HOME and XDG_CONFIG_HOME so it can never read the developer's
// real files (D9, precedent commit a77de4b).
func TestPaths(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got := Paths()
	want := []string{
		filepath.Join(xdg, "bentoo", "secrets"),
		"/etc/bentoo/secrets",
	}
	if len(got) != len(want) {
		t.Fatalf("Paths() = %q (%d entries), want %q (%d entries)", got, len(got), want, len(want))
	}
	if got[0] != want[0] {
		t.Errorf("Paths()[0] = %q, want the user-scope file %q first", got[0], want[0])
	}
	if got[1] != want[1] {
		t.Errorf("Paths()[1] = %q, want the system-scope literal %q second", got[1], want[1])
	}
}

// userScopePath returns the location of the single user-scope entry the REAL
// pathsFn produces under the ambient HOME/XDG_CONFIG_HOME. It selects by the
// entry's scope tag rather than by list position, so it keeps asserting the right
// entry if the chain ever grows, and it fails loudly if the user-scope entry is
// missing or duplicated. It sets no environment variable of its own: each caller
// establishes its own HOME + XDG_CONFIG_HOME isolation so that isolation stays
// visible (and auditable) at the call site.
func userScopePath(t *testing.T) string {
	t.Helper()
	var found []string
	for _, e := range pathsFn() {
		if e.user {
			found = append(found, e.name)
		}
	}
	if len(found) != 1 {
		t.Fatalf("pathsFn() yielded %d user-scope entries %q, want exactly 1", len(found), found)
	}
	return found[0]
}

// TestPaths_XDGUserScope pins R1.5: the user-scope file lives under
// $XDG_CONFIG_HOME when that variable is set, and under $HOME/.config when it is
// not — the same rule config.ConfigPaths follows. Both cases exercise the REAL
// pathsFn rather than the withPaths seam, since the path construction is exactly
// what is being verified, and both isolate HOME as well as XDG_CONFIG_HOME (D9).
func TestPaths_XDGUserScope(t *testing.T) {
	t.Run("XDG_CONFIG_HOME set locates the user file under it", func(t *testing.T) {
		home := t.TempDir()
		// A sibling of home, never below it: this path is reachable ONLY by
		// honoring XDG_CONFIG_HOME, so a silent fallback to $HOME/.config cannot
		// produce it by coincidence.
		xdg := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", xdg)

		want := filepath.Join(xdg, "bentoo", "secrets")
		if got := userScopePath(t); got != want {
			t.Fatalf("user-scope path = %q, want %q", got, want)
		}
	})

	t.Run("XDG_CONFIG_HOME unset falls back to $HOME/.config", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		// t.Setenv(k, "") SETS the variable to empty rather than unsetting it.
		// That is deliberate and sufficient here, not a coverage gap: pathsFn reads
		// the variable with os.Getenv and branches on `xdg == ""`, and os.Getenv
		// cannot distinguish "set to empty" from "absent" — both yield "" and take
		// the same fallback. An os.Unsetenv variant would re-run the identical
		// branch while giving up t.Setenv's automatic restore.
		t.Setenv("XDG_CONFIG_HOME", "")

		want := filepath.Join(home, ".config", "bentoo", "secrets")
		if got := userScopePath(t); got != want {
			t.Fatalf("user-scope path = %q, want %q", got, want)
		}
	})
}

// TestUserPath pins F-H: UserPath reports the user-scope file when the chain has
// one, and reports absence rather than silently substituting a different scope
// when it does not.
//
// The second case is the whole point. Callers use this to tell a user where to
// put a secret, and the obvious spelling — Paths()[0] — is correct ONLY while a
// user-scope entry exists. With $HOME unresolvable that entry is dropped and
// index 0 slides to the root-owned /etc/bentoo/secrets, so the advice becomes
// "write your secret into a 0600 file you cannot open". Paths() is never empty,
// so nothing fails loudly; the caller just prints a plausible, unusable path.
// Both cases drive the REAL pathsFn, since the scope selection is what is under
// test, and both isolate HOME and XDG_CONFIG_HOME (D9).
func TestUserPath(t *testing.T) {
	t.Run("returns the user-scope file when the chain has one", func(t *testing.T) {
		home := t.TempDir()
		// A sibling of home, never below it: reachable only by honoring
		// XDG_CONFIG_HOME, so a fallback to $HOME/.config cannot produce it by
		// coincidence.
		xdg := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", xdg)

		got, ok := UserPath()
		if !ok {
			t.Fatal("UserPath() reported no user-scope path with HOME and XDG_CONFIG_HOME set")
		}
		if want := filepath.Join(xdg, "bentoo", "secrets"); got != want {
			t.Errorf("UserPath() = %q, want %q", got, want)
		}
		// It must name the entry Lookup actually consults, not an independently
		// re-derived path that could drift from the chain.
		if want := userScopePath(t); got != want {
			t.Errorf("UserPath() = %q, want the chain's own user-scope entry %q", got, want)
		}
	})

	t.Run("reports absence when $HOME is unresolvable", func(t *testing.T) {
		// An empty HOME makes os.UserHomeDir() fail on unix — the state of the
		// snapshot systemd timer running as root, which is exactly where a caller
		// indexing Paths()[0] would hand out /etc/bentoo/secrets.
		t.Setenv("HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "")

		got, ok := UserPath()
		if ok {
			t.Fatalf("UserPath() = (%q, true), want no user-scope path when $HOME is unresolvable", got)
		}
		if got != "" {
			t.Errorf("UserPath() = %q with ok=false, want the empty string", got)
		}
		// The failure mode being guarded against, stated positively: the system
		// file is what Paths()[0] would have offered here.
		if p := Paths(); len(p) > 0 && p[0] != "/etc/bentoo/secrets" {
			t.Errorf("Paths()[0] = %q, want /etc/bentoo/secrets (the premise of this case)", p[0])
		}
	})
}

// TestScrub pins the regression contract: a secret value never survives in the
// scrubbed output, so it cannot reach a log line or an error string.
func TestScrub(t *testing.T) {
	const secret = "s3cr3t-token"
	in := "request failed: token=" + secret + " rejected"
	got := Scrub(in, secret)
	if strings.Contains(got, secret) {
		t.Fatalf("Scrub left the secret in %q", got)
	}
}

// TestPathsFn_HomeUnresolvable_YieldsSystemScopeOnly drives the REAL pathsFn
// down its drop-the-user-entry branch, rather than the hand-assembled shape
// withSystemOnlyPath builds.
//
// This is the branch F-1 lived in. The scope tag exists so that dropping the
// user-scope entry cannot promote /etc/bentoo/secrets to user scope, which
// would turn its by-design EACCES into a hard ErrUnreadable (D2) for exactly
// the population that depends on it: the snapshot systemd timer, running as
// root with no $HOME (D4). The error-mapping test covers the consequence, but
// only against a seam override — so a refactor writing `user: true` here would
// keep every other test green while silently reinstating F-1.
func TestPathsFn_HomeUnresolvable_YieldsSystemScopeOnly(t *testing.T) {
	// An empty HOME makes os.UserHomeDir() fail on unix. XDG_CONFIG_HOME is
	// blanked too so the assertion reflects the design's stated scenario
	// ("$HOME unresolvable → user-scope path skipped"); pathsFn returns before
	// consulting XDG, so this only removes ambient noise.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	got := pathsFn()

	if len(got) != 1 {
		t.Fatalf("pathsFn() returned %d entries, want exactly 1 (system scope): %+v", len(got), got)
	}
	if got[0].name != "/etc/bentoo/secrets" {
		t.Errorf("pathsFn()[0].name = %q, want /etc/bentoo/secrets", got[0].name)
	}
	if got[0].user {
		t.Error("pathsFn() tagged the system-scope file as user scope; " +
			"its by-design EACCES will surface as ErrUnreadable instead of a silent miss (F-1, D2)")
	}
}
