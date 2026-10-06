package main

// The changelog section that ships bentoo-tray's own version (0.33.1, or
// [Unreleased] until the cut) names the new --version first line and the new
// feed User-Agent. An entry filed under the already released [0.33.0] does not
// count.

import "testing"

func TestChangelog_ShippingSectionRecordsTheTrayVersion(t *testing.T) {
	section := shippingSection(readRepoDoc(t, "CHANGELOG.md"), "0.33.1")
	if section == "" {
		t.Fatal("CHANGELOG.md has neither a [0.33.1] nor an [Unreleased] section")
	}
	requireContains(t, "CHANGELOG.md (section shipping 0.33.1)", section,
		"bentoo-tray version 0.1.0",
		"bentoo-tray/0.1.0",
	)
}
