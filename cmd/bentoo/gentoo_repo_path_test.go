package main

import (
	"os"
	"testing"
)

// TestGentooRepoPath: unset is Portage's default, a path overrides it, and
// set-but-empty is no ::gentoo tree at all, the documented off switch.
func TestGentooRepoPath(t *testing.T) {
	t.Setenv("BENTOO_GENTOO_REPO", "/srv/gentoo")
	if got := gentooRepoPath(); got != "/srv/gentoo" {
		t.Errorf("BENTOO_GENTOO_REPO=/srv/gentoo: gentooRepoPath() = %q", got)
	}

	t.Setenv("BENTOO_GENTOO_REPO", "")
	if got := gentooRepoPath(); got != "" {
		t.Errorf("BENTOO_GENTOO_REPO set and empty: gentooRepoPath() = %q, want \"\" (checks off)", got)
	}

	if err := os.Unsetenv("BENTOO_GENTOO_REPO"); err != nil {
		t.Fatal(err)
	}
	if got := gentooRepoPath(); got != "/var/db/repos/gentoo" {
		t.Errorf("BENTOO_GENTOO_REPO unset: gentooRepoPath() = %q, want /var/db/repos/gentoo", got)
	}
}
