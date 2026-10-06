package ebuilds

import (
	"testing"
)

// TestReviveSplitsPackageKey is the "category/package" split the revive
// pipeline starts with: exactly two non-empty segments succeed, anything else
// fails. A ":slot" suffix is part of the packages.toml key's identity, never of
// the path built from it, so a revive seeds from net-libs/webkit-gtk, not from a
// directory named "webkit-gtk:4.1".
func TestReviveSplitsPackageKey(t *testing.T) {
	tests := []struct {
		name     string
		pkg      string
		wantCat  string
		wantName string
		wantOK   bool
	}{
		{name: "valid", pkg: "cat/pkg", wantCat: "cat", wantName: "pkg", wantOK: true},
		{name: "slot suffix dropped", pkg: "net-libs/webkit-gtk:4.1", wantCat: "net-libs", wantName: "webkit-gtk", wantOK: true},
		{name: "no slash", pkg: "noslash", wantOK: false},
		{name: "empty name", pkg: "a/", wantOK: false},
		{name: "empty category", pkg: "/b", wantOK: false},
		{name: "three segments", pkg: "a/b/c", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat, name, ok := SplitPackageKey(tt.pkg)
			if ok != tt.wantOK {
				t.Fatalf("SplitPackageKey(%q) ok = %v, want %v", tt.pkg, ok, tt.wantOK)
			}
			if ok && (cat != tt.wantCat || name != tt.wantName) {
				t.Errorf("SplitPackageKey(%q) = (%q, %q), want (%q, %q)", tt.pkg, cat, name, tt.wantCat, tt.wantName)
			}
		})
	}
}
