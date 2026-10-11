package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// R1.2: an absent host list means the overlay's own distfiles host.
func TestVendorEffectiveHostsDefaultsWhenAbsent(t *testing.T) {
	got := VendorConfig{}.EffectiveHosts()
	if !slices.Equal(got, []string{"distfiles.obentoo.org"}) {
		t.Fatalf("EffectiveHosts() of an absent list = %v, want [distfiles.obentoo.org]", got)
	}
}

// R1.2 converse: a configured list replaces the default, it is not merged
// with it, or the operator could never take distfiles.obentoo.org out.
func TestVendorEffectiveHostsHonoursAConfiguredList(t *testing.T) {
	got := VendorConfig{Hosts: []string{"mirror.example.org"}}.EffectiveHosts()
	if !slices.Equal(got, []string{"mirror.example.org"}) {
		t.Fatalf("EffectiveHosts() = %v, want exactly [mirror.example.org]", got)
	}
}

// R5.3: only {path} and {file} are placeholders, and the first element must
// name a program. Hostile near-misses come first, accepted forms last.
func TestVendorValidateUploadCommand(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		wantErr string // "" = accepted; otherwise the key[index] the error must name
	}{
		{"near miss: plural", []string{"up", "{paths}"}, "autoupdate.vendor.upload_command[1]"},
		{"near miss: upper case", []string{"up", "--file={PATH}"}, "autoupdate.vendor.upload_command[1]"},
		{"near miss: inner space", []string{"up", "{ file}"}, "autoupdate.vendor.upload_command[1]"},
		{"unknown token", []string{"up", "a", "{url}"}, "autoupdate.vendor.upload_command[2]"},
		{"unknown beside a known one", []string{"up", "{file}{name}"}, "autoupdate.vendor.upload_command[1]"},
		{"empty first element", []string{"", "{path}"}, "autoupdate.vendor.upload_command[0]"},
		{"known tokens embedded", []string{"wrangler", "obentoo-distfiles/{file}", "--file={path}"}, ""},
		{"both tokens in one element, twice", []string{"up", "{file}:{path}:{file}"}, ""},
		{"no placeholder at all", []string{"up", "--remote"}, ""},
		{"unset", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VendorConfig{UploadCommand: tc.argv}.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate(%q) = %v, want nil", tc.argv, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate(%q) = nil, want an error naming %s", tc.argv, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate(%q) = %q, want it to name %s", tc.argv, err, tc.wantErr)
			}
		})
	}
}

func s090WriteConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// R5.3: the refusal happens at load time.
func TestVendorLoadFromRefusesABadUploadCommand(t *testing.T) {
	path := s090WriteConfig(t, "autoupdate:\n  vendor:\n    upload_command:\n      - up\n      - \"{dest}\"\n")
	cfg, err := LoadFrom(path)
	if err == nil {
		t.Fatalf("LoadFrom accepted upload_command [up {dest}]; got %+v", cfg.Autoupdate.Vendor)
	}
	if !strings.Contains(err.Error(), "autoupdate.vendor.upload_command[1]") {
		t.Fatalf("LoadFrom error %q does not name autoupdate.vendor.upload_command[1]", err)
	}
}

// R1.1/R5.1 wiring: both keys decode under autoupdate.vendor.
func TestVendorLoadFromDecodesBothKeys(t *testing.T) {
	path := s090WriteConfig(t, "autoupdate:\n  vendor:\n    hosts: [mirror.example.org]\n    upload_command: [up, \"--file={path}\"]\n")
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	v := cfg.Autoupdate.Vendor
	if !slices.Equal(v.Hosts, []string{"mirror.example.org"}) || !slices.Equal(v.UploadCommand, []string{"up", "--file={path}"}) {
		t.Fatalf("decoded vendor block = %+v", v)
	}
}
