package main

import (
	"path/filepath"
	"testing"
)

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

// isolateTokenEnv gives a test a clean token environment. Both HOME and
// XDG_CONFIG_HOME must be redirected (D9): pathsFn honors XDG_CONFIG_HOME BEFORE
// $HOME/.config, so redirecting HOME alone still reads the developer's real
// ~/.config/bentoo/secrets — green here, red on clean CI.
func isolateTokenEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
}

// TestResolveRepoToken pins the D3 precedence change: --token outranks a per-repo
// token (R3.2), a per-repo token outranks the global one (R3.3), and an empty
// pair falls through to github.ResolveToken. Before D3 a per-repo token beat
// everything, including an explicit flag — defensible while the token lived in
// the config file the user was editing, indefensible once it lives in a secrets
// file the flag cannot override.
func TestResolveRepoToken(t *testing.T) {
	// R3.2 — the regression guard. Reinstating the old `repoInfo.Token == ""`
	// condition would let the per-repo value win here and fail this case.
	t.Run("flag beats a per-repo token", func(t *testing.T) {
		isolateTokenEnv(t)

		got, err := resolveRepoToken("from-flag", "from-repo")
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "from-flag" {
			t.Fatalf("resolveRepoToken = %q, want %q (--token must outrank a per-repo token)", got, "from-flag")
		}
	})

	t.Run("flag beats the global token too", func(t *testing.T) {
		isolateTokenEnv(t)
		t.Setenv("GITHUB_TOKEN", "from-global")

		got, err := resolveRepoToken("from-flag", "")
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "from-flag" {
			t.Fatalf("resolveRepoToken = %q, want %q", got, "from-flag")
		}
	})

	// R3.3 — per-repo still outranks the global token.
	t.Run("per-repo beats the global token", func(t *testing.T) {
		isolateTokenEnv(t)
		t.Setenv("GITHUB_TOKEN", "from-global")

		got, err := resolveRepoToken("", "from-repo")
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "from-repo" {
			t.Fatalf("resolveRepoToken = %q, want %q (a per-repo token must outrank the global one)", got, "from-repo")
		}
	})

	t.Run("empty flag and empty per-repo fall through to the global token", func(t *testing.T) {
		isolateTokenEnv(t)
		t.Setenv("GITHUB_TOKEN", "from-global")

		got, err := resolveRepoToken("", "")
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "from-global" {
			t.Fatalf("resolveRepoToken = %q, want %q", got, "from-global")
		}
	})

	t.Run("nothing set anywhere resolves to empty without error", func(t *testing.T) {
		isolateTokenEnv(t)

		got, err := resolveRepoToken("", "")
		if err != nil {
			t.Fatalf("err = %v, want nil (an absent token means anonymous access, not a failure)", err)
		}
		if got != "" {
			t.Fatalf("resolveRepoToken = %q, want empty", got)
		}
	})
}
