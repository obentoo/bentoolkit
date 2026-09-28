package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// Story 068, sub-task 2.1 — R2.2, R2.4 through the real command: `bentoo
// distfile fetch` for a record naming GITHUB_TOKEN exits 1, sends nothing,
// never prints the value, and prints the same text --lint reports.
func TestAuthFetchRefusal_SameTextOnEveryPath(t *testing.T) {
	const sentinel = "S068-CMD-SENTINEL-must-never-leave-51be"
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("BINARY"))
	}))
	defer srv.Close()

	overlay := t.TempDir()
	for _, dir := range []string{"profiles", "metadata", ".autoupdate", "app-misc/leak"} {
		if err := os.MkdirAll(filepath.Join(overlay, filepath.FromSlash(dir)), 0o750); err != nil {
			t.Fatalf("laying out the overlay: %v", err)
		}
	}
	record := `["app-misc/leak"]
url = "https://upstream.test/releases.json"
parser = "json"
path = "tag_name"
meta = { fetch_url = "` + srv.URL + `/download", fetch_serial_env = "GITHUB_TOKEN", fetch_serial_field = "key", fetch_filename = "leak-{version}.tar.gz" }
comments = """the story's reproduction"""
# END
`
	if err := os.WriteFile(filepath.Join(overlay, ".autoupdate", "packages.toml"), []byte(record), 0o600); err != nil {
		t.Fatalf("writing packages.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "app-misc", "leak", "leak-1.0.ebuild"), []byte("EAPI=8\n"), 0o600); err != nil {
		t.Fatalf("writing the ebuild: %v", err)
	}
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	if err := os.MkdirAll(filepath.Join(xdg, "bentoo"), 0o750); err != nil {
		t.Fatalf("laying out the config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "bentoo", "config.yaml"), []byte("overlay:\n  path: "+overlay+"\n"), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("GITHUB_TOKEN", sentinel)

	dest := t.TempDir()
	cmd := newDistfileFetchCmd()
	distfileFetchDistdir = dest
	distfileFetchVersion = ""

	var (
		code   int
		exited bool
	)
	errOut := captureStderr(t, func() {
		code, exited = captureExit(t, func() { runDistfileFetch(cmd, []string{"app-misc/leak"}) })
	})

	if n := hits.Load(); n != 0 {
		t.Errorf("the endpoint received %d request(s); want none", n)
	}
	if !exited || code != 1 {
		t.Errorf("exit = (%d, exited=%v), want (1, true)", code, exited)
	}
	if strings.Contains(errOut, sentinel) {
		t.Errorf("stderr carries the variable's value: %s", errOut)
	}
	for _, needle := range []string{"GITHUB_TOKEN", "fetch_serial_env", "BENTOO_FETCH_GITHUB_TOKEN"} {
		if !strings.Contains(errOut, needle) {
			t.Errorf("the refusal does not name %q; stderr: %s", needle, errOut)
		}
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Errorf("the destination is not empty after a refused run: %v", entries)
	}

	// Same text: the command prints FetchAuthDistfile's error, and --lint reports it.
	_, fetchErr := autoupdate.FetchAuthDistfile(context.Background(), autoupdate.AuthDistfileRequest{
		OverlayPath: overlay, Package: "app-misc/leak", Version: "1.0", DestDir: t.TempDir(),
	})
	if fetchErr == nil {
		t.Fatalf("FetchAuthDistfile accepted the record (endpoint hits now %d)", hits.Load())
	}
	if !strings.Contains(errOut, fetchErr.Error()) {
		t.Errorf("stderr does not carry the refusal text %q; stderr: %s", fetchErr.Error(), errOut)
	}
	issues, err := autoupdate.LintPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	found := false
	for _, is := range issues {
		if is.Package == "app-misc/leak" && is.Rule == autoupdate.LintInvalidConfig && strings.Contains(is.Message, fetchErr.Error()) {
			found = true
		}
	}
	if !found {
		t.Errorf("--lint reports no invalid-config issue carrying %q; issues: %+v", fetchErr.Error(), issues)
	}
}
