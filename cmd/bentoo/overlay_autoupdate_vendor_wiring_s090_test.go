package main

// Story 090, sub-task 3.3 — R1.2, R5.1.
//
// Every production Applier must reach its vendor step with the configured
// autoupdate.vendor block and an HTTP client. A construction site that dropped
// either option would look like working software: with no client the vendor
// step is silently off, and with no block the default host and no upload
// command would replace what the operator configured. So each of the four sites
// is driven through the real CLI and the Applier it builds is read back.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// errS090StopAtApplier ends a run at its Applier construction: everything this
// test asserts is decided by then.
var errS090StopAtApplier = errors.New("test stops the run at the applier")

// s090VendorTimeout is autoupdate.http_timeout in the test config. It differs
// from the 30s default so a client built without the config cannot pass.
const s090VendorTimeout = 7 * time.Second

// s090WantVendor is the autoupdate.vendor block the test config declares.
func s090WantVendor() config.VendorConfig {
	return config.VendorConfig{
		Hosts:         []string{"distfiles.example.org"},
		UploadCommand: []string{"/bin/true", "{path}"},
	}
}

// s090AppendVendorConfig adds the autoupdate block to the harness's config.
func s090AppendVendorConfig(t *testing.T, c *testCLI) {
	t.Helper()
	path := filepath.Join(c.Home(), ".config", "bentoo", "config.yaml")
	block := "autoupdate:\n" +
		"  http_timeout: 7\n" +
		"  vendor:\n" +
		"    hosts: [distfiles.example.org]\n" +
		"    upload_command: [\"/bin/true\", \"{path}\"]\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("opening the config %s: %v", path, err)
	}
	if _, err := f.WriteString(block); err != nil {
		_ = f.Close()
		t.Fatalf("appending the autoupdate block to %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing the config %s: %v", path, err)
	}
}

// s090SeedPending writes one pending update into the run's autoupdate state
// directory, which --apply all and the --check validation read.
func s090SeedPending(t *testing.T, c *testCLI) {
	t.Helper()
	dir := filepath.Join(c.Home(), ".config", "bentoo", "autoupdate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating the autoupdate state directory %s: %v", dir, err)
	}
	pending, err := autoupdate.NewPendingList(dir)
	if err != nil {
		t.Fatalf("NewPendingList(%s): %v", dir, err)
	}
	if err := pending.Add(autoupdate.PendingUpdate{
		Package:        "dev-test/foo",
		CurrentVersion: "1.0.0",
		NewVersion:     "1.0.1",
		Status:         autoupdate.StatusPending,
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}
}

// TestAutoupdateAppliersGetVendorWiring drives each Applier construction site
// and asserts the Applier it builds carries the configured vendor block and a
// client whose request timeout is autoupdate.http_timeout.
func TestAutoupdateAppliersGetVendorWiring(t *testing.T) {
	sites := []struct {
		name  string
		args  []string
		setup func(t *testing.T, c *testCLI)
	}{
		{
			name: "runApply",
			args: []string{"overlay", "autoupdate", "--apply", "dev-test/foo"},
		},
		{
			name:  "runApplyAll",
			args:  []string{"overlay", "autoupdate", "--apply", "all"},
			setup: s090SeedPending,
		},
		{
			name: "runRevive",
			args: []string{"overlay", "autoupdate", "--revive", "dev-test/foo"},
			setup: func(t *testing.T, c *testCLI) {
				t.Helper()
				writeReviveRegexConfig(t, c.Overlay(), "dev-test/foo", "http://127.0.0.1:1")
				srcDir := filepath.Join(t.TempDir(), "src")
				writeReviveSrcEbuild(t, srcDir, "foo", "1.0.0")
				withFakeGentoo(c.deps, &fakeReviveProvider{
					versions: map[string][]string{"dev-test/foo": {"1.0.0"}},
					dir:      srcDir,
				})
			},
		},
		{
			name: "checkValidation",
			args: []string{"overlay", "autoupdate", "--check", "--llm", "--yes"},
			setup: func(t *testing.T, c *testCLI) {
				t.Helper()
				writeReviveRegexConfig(t, c.Overlay(), "dev-test/foo", "http://127.0.0.1:1")
				s090SeedPending(t, c)
			},
		},
	}

	for _, site := range sites {
		t.Run(site.name, func(t *testing.T) {
			var built []*autoupdate.Applier
			c := newTestCLI(t, withDeps(func(d *deps) {
				d.newApplier = func(overlayPath, configDir string, opts ...autoupdate.ApplierOption) (*autoupdate.Applier, error) {
					a, err := autoupdate.NewApplier(overlayPath, configDir, opts...)
					if err != nil {
						t.Errorf("%s: building the applier from the site's options: %v", site.name, err)
						return nil, err
					}
					built = append(built, a)
					return nil, errS090StopAtApplier
				}
			}))
			s090AppendVendorConfig(t, c)
			if site.setup != nil {
				site.setup(t, c)
			}

			stdout, stderr, code := c.Run(site.args...)

			if len(built) != 1 {
				t.Fatalf("%s built %d applier(s), want 1 (exit %d)\nstdout:\n%s\nstderr:\n%s",
					site.name, len(built), code, stdout, stderr)
			}
			a := built[0]
			if got, want := a.VendorConfig(), s090WantVendor(); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: the applier's vendor block = %+v, want the configured %+v", site.name, got, want)
			}
			client := a.HTTPClient()
			if client == nil {
				t.Fatalf("%s: the applier has no HTTP client, so its vendor step is off", site.name)
			}
			if got := client.Config().Timeout; got != s090VendorTimeout {
				t.Errorf("%s: the vendor client's request timeout = %s, want autoupdate.http_timeout %s",
					site.name, got, s090VendorTimeout)
			}
		})
	}
}
