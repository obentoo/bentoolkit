package autoupdate

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// TestEnvironmentVerdict_UpstreamTroubleIsNotTheEbuilds reproduces the
// 2026-11-13 libreoffice-l10n bump: the first manifest run hit its deadline
// against an upstream answering 502, and the LLM fixer was invoked on an
// ebuild with nothing wrong in it. A timeout and a server-side HTTP error are
// the network's news, not the ebuild's; a 404 still is the ebuild's.
func TestEnvironmentVerdict_UpstreamTroubleIsNotTheEbuilds(t *testing.T) {
	exit := &exec.ExitError{}
	staged := func(output string) error {
		return fmt.Errorf("%w: staged manifest: %w", ErrManifestFailed,
			&ChildRunError{Label: childRunFirstManifest, Output: output, Err: exit})
	}

	cases := []struct {
		name string
		err  error
		env  bool
	}{
		{"timed out", fmt.Errorf("%w: staged manifest: %w after 5m: %w", ErrManifestFailed, ErrManifestTimedOut, &ChildRunError{Err: exit}), true},
		{"wget 502", staged("--2026-11-13--  https://download.example.org/a.rpm\nERROR 502: Bad Gateway.\n"), true},
		{"curl 503", staged("curl: (22) The requested URL returned error: 503\n"), true},
		{"wget 404", staged("ERROR 404: Not Found.\n"), false},
		{"404 beside a 502", staged("ERROR 502: Bad Gateway.\nERROR 404: Not Found.\n"), false},
		{"no server word", staged("!!! digest verification failed\n"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := environmentVerdict(tc.err)
			if env := errors.Is(got, ErrManifestEnvironment); env != tc.env {
				t.Errorf("environment = %v, want %v (verdict %v)", env, tc.env, got)
			}
		})
	}
}
