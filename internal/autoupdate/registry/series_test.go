package registry

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateDistinctEntries covers the cross-entry rule: two entries for one
// package must each be able to say which ebuilds are its own.
func TestValidateDistinctEntries(t *testing.T) {
	base := PackageConfig{URL: "https://example.com/x", Parser: "json", Path: "version"}
	with := func(series string) PackageConfig {
		c := base
		c.Series = series
		return c
	}

	t.Run("distinct series accepted", func(t *testing.T) {
		cfg := &PackagesConfig{Packages: map[string]PackageConfig{
			"app-office/libreoffice@stable":  with(`^26\.2\.`),
			"app-office/libreoffice@testing": with(`^26\.8\.`),
		}}
		if err := cfg.ValidateAll(nil); err != nil {
			t.Fatalf("rejected: %v", err)
		}
	})

	t.Run("distinct slots accepted", func(t *testing.T) {
		cfg := &PackagesConfig{Packages: map[string]PackageConfig{
			"net-libs/webkit-gtk:4.1": base,
			"net-libs/webkit-gtk:6":   base,
		}}
		if err := cfg.ValidateAll(nil); err != nil {
			t.Fatalf("rejected: %v", err)
		}
	})

	t.Run("label alone is rejected", func(t *testing.T) {
		cfg := &PackagesConfig{Packages: map[string]PackageConfig{
			"app-office/libreoffice@stable":  base,
			"app-office/libreoffice@testing": base,
		}}
		err := cfg.ValidateAll(nil)
		if err == nil {
			t.Fatal("two entries with no filter accepted")
		}
		if !strings.Contains(err.Error(), "same ebuilds") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("empty label is rejected", func(t *testing.T) {
		cfg := base
		if err := ValidatePackageConfig(nil, "app-office/libreoffice@", &cfg); !errors.Is(err, ErrInvalidPackageKey) {
			t.Fatalf("got %v, want ErrInvalidPackageKey", err)
		}
	})

	t.Run("uncompilable series is rejected", func(t *testing.T) {
		cfg := with(`^(26\.2`)
		if err := ValidatePackageConfig(nil, "app-office/libreoffice@stable", &cfg); err == nil {
			t.Fatal("uncompilable series accepted")
		}
	})
}
