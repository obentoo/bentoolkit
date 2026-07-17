package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	pathsFn = func() []string { return []string{userPath, sysPath} }
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

// TestPaths asserts the searched locations are returned in order and both name
// the secrets file.
func TestPaths(t *testing.T) {
	got := Paths()
	if len(got) == 0 {
		t.Fatal("Paths() returned no locations")
	}
	for _, p := range got {
		if !strings.Contains(p, "secrets") {
			t.Errorf("path %q does not name the secrets file", p)
		}
	}
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
