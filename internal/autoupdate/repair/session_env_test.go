package repair

import (
	"slices"
	"testing"
)

func TestSessionEnvDropsSecretNamesExactly(t *testing.T) {
	environ := []string{"PATH=/bin", "GITHUB_TOKEN=x", "GITHUB_TOKEN_HINT=keep", "HOME=/h", "BENTOO_FETCH_S=y", "EMPTY=", "MY_KEY=z=z"}
	got := SessionEnv(environ, []string{"GITHUB_TOKEN", "BENTOO_FETCH_S", "MY_KEY", ""})
	want := []string{"PATH=/bin", "GITHUB_TOKEN_HINT=keep", "HOME=/h", "EMPTY="}
	if !slices.Equal(got, want) {
		t.Fatalf("SessionEnv = %q, want %q", got, want)
	}
	if got := SessionEnv(environ, nil); !slices.Equal(got, environ) {
		t.Fatalf("no secret names changed the environment: %q", got)
	}
}
