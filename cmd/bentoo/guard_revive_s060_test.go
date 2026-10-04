package main

// Characterization guard for story 060 — R4.5, U2, U3.
//
// GREEN AT 1462803 BY DESIGN. Sub-task 1.4 moves the revive pipeline into
// autoupdate.Reviver, and runRevive then learns that the configured ::gentoo
// provider has no local package directory from NewReviver's wrapped
// ErrNoLocalPackageDir instead of its own type assertion. This guard pins what
// the operator sees on that path, so the move cannot change it: the same
// configuration hint, byte for byte, on stderr, exit status 1, and no target
// processed.
//
// Driven only through testCLI with real flags and a real config file — no
// package variable, no seam — so it compiles at 1462803 and after every task of
// the story, and it runs from the start of the story.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const s060ReviveHint = "the resolved gentoo provider has no local package directory; revive needs an on-disk ::gentoo tree.\n" +
	"Configure a local gentoo repository in ~/.config/bentoo/config.yaml:\n" +
	"  repositories:\n" +
	"    gentoo:\n" +
	"      provider: local\n" +
	"      path: /var/db/repos/gentoo\n" +
	"(or force a clone-backed provider so the package tree is available on disk)\n"

// TestS060GuardReviveNeedsALocalGentooTree runs both forms of --revive. The
// "all" form is the hostile one: the check must stay AHEAD of the orphan scan,
// or a registry with no orphan answers "Nothing to revive" with exit 0 and the
// operator never learns the provider cannot revive anything (design C4,
// "Order").
func TestS060GuardReviveNeedsALocalGentooTree(t *testing.T) {
	for _, target := range []string{"all", "dev-util/claude-code"} {
		t.Run(target, func(t *testing.T) {
			s060GuardReviveHint(t, target)
		})
	}
}

func s060GuardReviveHint(t *testing.T, target string) {
	t.Helper()
	c := s058Env(t) // testCLI plus XDG state/cache dirs and no ambient tokens
	s058WriteRegistry(t, c.Overlay(), s058CleanRegistry)

	// An API-only ::gentoo: the GitHub provider has no on-disk package tree.
	configPath := filepath.Join(c.Home(), ".config", "bentoo", "config.yaml")
	cfg, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("repositories:\n  gentoo:\n    provider: github\n    url: gentoo/gentoo\n")...)
	if err := os.WriteFile(configPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := c.Run("overlay", "autoupdate", "--revive", target)

	if code != 1 {
		t.Errorf("exit status = %d, want 1 (R4.5, U2)\nstderr: %s", code, stderr)
	}
	if strings.Count(stderr, s060ReviveHint) != 1 {
		t.Errorf("stderr does not carry the configuration hint exactly once (R4.5, U3)\nwant block:\n%s\ngot:\n%s", s060ReviveHint, stderr)
	}
	if strings.Contains(stdout+stderr, "Reviving ") {
		t.Errorf("a target was processed although the provider cannot seed one (R4.5)\nstdout: %s", stdout)
	}
	if strings.Contains(stdout+stderr, "Nothing to revive") {
		t.Errorf("the orphan scan ran before the provider check (R4.5)\nstdout: %s", stdout)
	}
}
