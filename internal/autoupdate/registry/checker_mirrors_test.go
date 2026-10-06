package registry

import (
	"testing"
)

func TestValidateMirrors(t *testing.T) {
	base := PackageConfig{URL: "https://example.org/v.json", Parser: "json", Path: "version"}
	for _, tc := range []struct {
		name    string
		mirrors []string
		ok      bool
	}{
		{"valid", []string{"https://mirror.example.net/v.json"}, true},
		{"relative", []string{"/v.json"}, false},
		{"ftp", []string{"ftp://mirror.example.net/v.json"}, false},
		{"repeats url", []string{"https://example.org/v.json"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Mirrors = tc.mirrors
			err := ValidatePackageConfig(nil, "app-misc/foo", &cfg)
			if (err == nil) != tc.ok {
				t.Errorf("ValidatePackageConfig(nil, %v) = %v, want ok=%v", tc.mirrors, err, tc.ok)
			}
		})
	}
}
