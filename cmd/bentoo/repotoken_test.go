package main

import "testing"

// TestRepoTokenName pins R3.1 normalization: a repository name becomes
// BENTOO_REPO_<NAME>_TOKEN where <NAME> is the name uppercased with every rune
// outside [A-Z0-9] replaced by "_". Distinct names that normalize to the same key
// collide by design (documented), which this test also asserts.
func TestRepoTokenName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"gentoo", "BENTOO_REPO_GENTOO_TOKEN"},
		{"my-repo", "BENTOO_REPO_MY_REPO_TOKEN"},
		{"my.repo", "BENTOO_REPO_MY_REPO_TOKEN"},
		{"meu-fork", "BENTOO_REPO_MEU_FORK_TOKEN"},
		{"Repo123", "BENTOO_REPO_REPO123_TOKEN"},
		{"a/b", "BENTOO_REPO_A_B_TOKEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := repoTokenName(tc.name); got != tc.want {
				t.Fatalf("repoTokenName(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}

	// Collision is intentional and documented: my-repo and my.repo share a key.
	if repoTokenName("my-repo") != repoTokenName("my.repo") {
		t.Error("expected my-repo and my.repo to normalize to the same token name")
	}
}
