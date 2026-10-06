package ebuilds

import (
	"testing"
)

// TestSplitPkgLabel covers the key form that lets one package carry several
// entries. The label is identity only, so every path- and slot-aware consumer
// must drop it.
func TestSplitPkgLabel(t *testing.T) {
	tests := []struct {
		key      string
		rest     string
		label    string
		atom     string
		slot     string
		category string
		pkgName  string
	}{
		{"app-misc/hello", "app-misc/hello", "", "app-misc/hello", "", "app-misc", "hello"},
		{"app-office/libreoffice@testing", "app-office/libreoffice", "testing", "app-office/libreoffice", "", "app-office", "libreoffice"},
		{"net-libs/webkit-gtk:4.1", "net-libs/webkit-gtk:4.1", "", "net-libs/webkit-gtk", "4.1", "net-libs", "webkit-gtk"},
		{"net-libs/webkit-gtk:4.1@lts", "net-libs/webkit-gtk:4.1", "lts", "net-libs/webkit-gtk", "4.1", "net-libs", "webkit-gtk"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			rest, label := SplitPkgLabel(tt.key)
			if rest != tt.rest || label != tt.label {
				t.Fatalf("splitPkgLabel = (%q, %q), want (%q, %q)", rest, label, tt.rest, tt.label)
			}
			atom, slot := SplitPkgSlot(tt.key)
			if atom != tt.atom || slot != tt.slot {
				t.Fatalf("splitPkgSlot = (%q, %q), want (%q, %q)", atom, slot, tt.atom, tt.slot)
			}
			cat, pn, ok := SplitPkgAtom(tt.key)
			if !ok || cat != tt.category || pn != tt.pkgName {
				t.Fatalf("splitPkgAtom = (%q, %q, %v), want (%q, %q, true)", cat, pn, ok, tt.category, tt.pkgName)
			}
		})
	}
}
