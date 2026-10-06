package registry

import (
	"testing"
)

func TestValidateAuxURL(t *testing.T) {
	base := PackageConfig{URL: "https://example.org/v.json", Parser: "json", Path: "version",
		AuxVar: "MY_BUILD", AuxPattern: `(\d+)`}
	for _, tc := range []struct {
		name   string
		mutate func(*PackageConfig)
		ok     bool
	}{
		{"path placeholder", func(c *PackageConfig) { c.AuxURL = "https://example.org/{version}/latest.txt" }, true},
		{"query placeholder", func(c *PackageConfig) { c.AuxURL = "https://example.org/latest?v={version}" }, true},
		{"host placeholder", func(c *PackageConfig) { c.AuxURL = "https://{version}.example.org/latest.txt" }, false},
		{"not http", func(c *PackageConfig) { c.AuxURL = "file:///etc/passwd" }, false},
		{"without aux_pattern", func(c *PackageConfig) {
			c.AuxURL = "https://example.org/latest.txt"
			c.AuxVar, c.AuxPattern = "", ""
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			err := ValidatePackageConfig(nil, "app-misc/foo", &cfg)
			if (err == nil) != tc.ok {
				t.Errorf("ValidatePackageConfig(nil, aux_url=%q) = %v, want ok=%v", cfg.AuxURL, err, tc.ok)
			}
		})
	}
}
