package autoupdate

import "testing"

// TestCheckAll_DropsThePendingEntryOfADisabledPackage reproduces the stale
// kde-plasma/kwin record of 2026-11-13: disabled in packages.toml, no ebuild,
// and a `failed` pending entry that no run ever removed. An enabled package's
// entry beside it must survive, or the drop is indiscriminate.
func TestCheckAll_DropsThePendingEntryOfADisabledPackage(t *testing.T) {
	srv := jsonVersionServer(t, "2.0.0")
	content := `["kde-plasma/kwin"]
enabled = false
url = "` + srv.URL + `"
parser = "json"
path = "version"

["app-misc/kept"]
url = "` + srv.URL + `"
parser = "json"
path = "version"
`
	overlay, _ := writePackagesTOML(t, content)
	createTestEbuild(t, overlay, "app-misc/kept", "1.0.0")

	configDir := t.TempDir()
	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Add(PendingUpdate{Package: "kde-plasma/kwin", CurrentVersion: "6.7.4", NewVersion: "6.7.5", Status: StatusFailed}); err != nil {
		t.Fatal(err)
	}

	checker, err := NewChecker(overlay, WithConfigDir(configDir), WithRateLimiter(unlimitedRateLimiter()))
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	checker.CheckAll(t.Context(), false)

	reloaded, err := NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Get("kde-plasma/kwin"); ok {
		t.Error("the disabled package's pending entry survived CheckAll; the next --check would plan it again")
	}
	if _, ok := reloaded.Get("app-misc/kept"); !ok {
		t.Error("the enabled package's freshly detected entry is gone; the drop must touch disabled records only")
	}
}
