package main

import "testing"

// TestEbuildBaseName pins that the clean report names the ebuild file on
// disk: a labelled or slotted line drops its suffix (apply printed
// "zed@stable-1.27.0.ebuild" for app-editors/zed@stable, a file that never
// existed).
func TestEbuildBaseName(t *testing.T) {
	for key, want := range map[string]string{
		"app-editors/zed@stable":  "zed",
		"net-libs/webkit-gtk:4.1": "webkit-gtk",
		"dev-libs/liborcus":       "liborcus",
		"dev-cpp/gtkmm@3.24":      "gtkmm",
	} {
		if got := ebuildBaseName(key); got != want {
			t.Errorf("ebuildBaseName(%q) = %q, want %q", key, got, want)
		}
	}
}
