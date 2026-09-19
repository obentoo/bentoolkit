package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `distfile fetch` is the command an ebuild's pkg_nofetch is allowed to name,
// which makes its WIRING part of the contract: an ebuild that prints a command
// the binary does not expose sends the user to a shell error instead of to
// their distfile. These tests pin the shape that text depends on — the command
// path, the two flags it documents, and the single argument it takes.

func TestDistfileCommandIsRegisteredOnRoot(t *testing.T) {
	// distfileCmd is a lookup into the production tree (see root.go), so this
	// asserts the constructor is reached from newRootCmd and not merely that a
	// constructor exists.
	if distfileCmd.Name() != "distfile" {
		t.Fatalf("root has no `distfile` command, got %q", distfileCmd.Name())
	}
	if distfileCmd.Short == "" || distfileCmd.Long == "" {
		t.Error("`distfile` should carry both a short and a long description")
	}
	if distfileFetchCmd.CommandPath() != "bentoo distfile fetch" {
		t.Errorf("command path = %q, want `bentoo distfile fetch`", distfileFetchCmd.CommandPath())
	}
	if distfileFetchCmd.Run == nil {
		t.Error("`distfile fetch` should have a Run function")
	}
}

func TestDistfileFetchFlags(t *testing.T) {
	for _, name := range []string{"distdir", "version"} {
		flag := distfileFetchCmd.Flags().Lookup(name)
		if flag == nil {
			t.Fatalf("`distfile fetch` should have a --%s flag", name)
		}
		if flag.Value.Type() != "string" {
			t.Errorf("--%s should be a string, got %s", name, flag.Value.Type())
		}
	}
}

// The help text is the only place the two defaults are stated, and each of them
// is a decision somebody could quietly change: DISTDIR is asked of the host
// rather than assumed, and the version comes from the overlay rather than from
// the newest release upstream.
func TestDistfileFetchHelpStatesItsDefaults(t *testing.T) {
	long := distfileFetchCmd.Long
	for _, needle := range []string{"portageq distdir", "--version", "Exit codes"} {
		if !strings.Contains(long, needle) {
			t.Errorf("`distfile fetch --help` should mention %q", needle)
		}
	}
}

func TestDistfileFetchTakesExactlyOneArgument(t *testing.T) {
	for _, args := range [][]string{
		{"distfile", "fetch"},
		{"distfile", "fetch", "app-misc/a", "app-misc/b"},
	} {
		// A fresh tree per case: cobra keeps parsed state on the command, and
		// these two cases would otherwise share it.
		if _, err := executeCommand(newRootCmd(), args...); err == nil {
			t.Errorf("%v: expected an argument-count refusal, got none", args)
		}
	}
}

// distfileFetchFixture lays out everything a real run reads: a private
// XDG_CONFIG_HOME holding a config that points at an overlay, an overlay that
// passes the structure check, a registry record whose [meta] block points at a
// local server, and one ebuild so the version can be resolved from the overlay.
// It returns the overlay path and the directory the file should land in.
//
// The environment is isolated rather than borrowed: this test drives the same
// config chain a user's shell does, and reading the operator's own config would
// make the result depend on which machine ran the suite.
func distfileFetchFixture(t *testing.T, endpoint string) (destDir string) {
	t.Helper()

	overlay := t.TempDir()
	for _, dir := range []string{"profiles", "metadata", ".autoupdate", "app-misc/example"} {
		if err := os.MkdirAll(filepath.Join(overlay, filepath.FromSlash(dir)), 0o750); err != nil {
			t.Fatalf("laying out the overlay: %v", err)
		}
	}
	record := fmt.Sprintf(`["app-misc/example"]
url = "https://upstream.test/releases.json"
parser = "json"
path = "tag_name"
meta = { fetch_url = %q, fetch_filename = "Example_{version}.tar.xz", fetch_form = "platform=linux" }
comments = """a distfile no mirror may carry"""

["app-misc/plain"]
url = "https://upstream.test/releases.json"
parser = "json"
path = "tag_name"
comments = """an ordinary package, fetched from a mirror by portage itself"""
`, endpoint)
	if err := os.WriteFile(filepath.Join(overlay, ".autoupdate", "packages.toml"), []byte(record), 0o600); err != nil {
		t.Fatalf("writing packages.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "app-misc", "example", "example-2.1.0.ebuild"), []byte("EAPI=8\n"), 0o600); err != nil {
		t.Fatalf("writing the ebuild: %v", err)
	}

	xdg := t.TempDir()
	cfgDir := filepath.Join(xdg, "bentoo")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatalf("laying out the config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("overlay:\n  path: "+overlay+"\n"), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	return t.TempDir()
}

// TestDistfileFetchWritesTheFileEmergeWillLookFor is the whole command through
// the same path a user's shell takes: config, overlay, registry, download,
// file. It is the only test that proves the wiring between the three — a
// command that parses its flags and reaches nothing would pass every other test
// in this file.
func TestDistfileFetchWritesTheFileEmergeWillLookFor(t *testing.T) {
	const payload = "BINARY-DISTFILE"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	dest := distfileFetchFixture(t, srv.URL)

	// The flags are bound to package variables, so they are set AFTER the
	// command is built: the constructor's StringVar resets them to their
	// defaults, which would undo an assignment made before it.
	cmd := newDistfileFetchCmd()
	distfileFetchDistdir = dest
	distfileFetchVersion = ""

	var (
		code   int
		exited bool
	)
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runDistfileFetch(cmd, []string{"app-misc/example"}) })
	})
	if exited {
		t.Fatalf("the run exited with code %d; output: %s", code, out)
	}

	// The name is the contract: emerge looks for exactly what the Manifest
	// names, and the record's fetch_filename resolves against the version the
	// overlay carries (2.1.0), not against the newest release upstream.
	want := filepath.Join(dest, "Example_2.1.0.tar.xz")
	data, err := os.ReadFile(want) //nolint:gosec // path built from the test's own temp dir
	if err != nil {
		t.Fatalf("the distfile is not where emerge would look: %v", err)
	}
	if string(data) != payload {
		t.Errorf("file content = %q, want %q", data, payload)
	}
	if !strings.Contains(out, "Example_2.1.0.tar.xz") {
		t.Errorf("the run did not name the file it wrote; output: %s", out)
	}
}

// TestDistfileFetchRefusals pins the CLASSIFICATION, not merely the exit code.
// "there is no such record" and "this package needs no authenticated fetch" are
// different situations with different remedies, and both are one keystroke away
// from a run that succeeds — a single "fetch failed" over either would start a
// hunt for a network problem that is not there.
func TestDistfileFetchRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arg     string
		wantOut []string
	}{
		{
			name:    "a package the overlay does not track",
			arg:     "app-misc/absent",
			wantOut: []string{"app-misc/absent", "no packages.toml record"},
		},
		{
			name:    "a tracked package whose distfile is on a mirror",
			arg:     "app-misc/plain",
			wantOut: []string{"app-misc/plain", "no authenticated distfile fetch", "Nothing to do"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := distfileFetchFixture(t, "https://unused.test")

			cmd := newDistfileFetchCmd()
			distfileFetchDistdir = dest
			distfileFetchVersion = ""

			var (
				code   int
				exited bool
			)
			errOut := captureStderr(t, func() {
				code, exited = captureExit(t, func() { runDistfileFetch(cmd, []string{tc.arg}) })
			})
			if !exited || code != 1 {
				t.Fatalf("exit = (%d, exited=%v), want (1, true)", code, exited)
			}
			for _, want := range tc.wantOut {
				if !strings.Contains(errOut, want) {
					t.Errorf("the refusal does not mention %q; stderr: %s", want, errOut)
				}
			}
			// Nothing may be left behind by a run that downloaded nothing.
			entries, _ := os.ReadDir(dest)
			if len(entries) != 0 {
				t.Errorf("the destination is not empty after a refused run: %v", entries)
			}
		})
	}
}
