package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// TestApply_PublishesNothingWithoutAProof drives the real command: with no
// `--check --llm` behind the bump, `--apply <pkg>` runs nothing, writes no
// ebuild, says why and how to prove it, and exits non-zero; `--apply all`
// counts it as unproven, not as a failure.
func TestApply_PublishesNothingWithoutAProof(t *testing.T) {
	for _, target := range []string{"dev-test/cdf-foo", "all"} {
		t.Run(target, func(t *testing.T) {
			spawned := 0
			c := newTestCLI(t, withDeps(func(d *deps) {
				d.newApplier = func(overlayPath, configDir string, opts ...autoupdate.ApplierOption) (*autoupdate.Applier, error) {
					opts = append(opts, autoupdate.WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
						spawned++
						return exec.CommandContext(ctx, "true")
					}))
					return autoupdate.NewApplier(overlayPath, configDir, opts...)
				}
			}))
			plantUncooledBump(t, c)

			stdout, stderr, code := c.Run("overlay", "autoupdate", "--apply", target)

			if cdfEbuildExists(c, "1.0.1") {
				t.Error("the unproven bump was written into the overlay")
			}
			if spawned != 0 {
				t.Errorf("%d child process(es) ran for a bump with no proof", spawned)
			}
			if !strings.Contains(stdout, "Unproven") || !strings.Contains(stdout, "--check --llm dev-test/cdf-foo") {
				t.Errorf("the output does not say the bump is unproven and how to prove it:\n%s\n%s", stdout, stderr)
			}
			if wantCode := map[string]int{"all": 0}[target]; target != "all" {
				if code == 0 {
					t.Errorf("--apply %s exited 0 for a bump it did not publish", target)
				}
			} else if code != wantCode {
				t.Errorf("--apply all exited %d; an unproven bump is not a failure", code)
			}
		})
	}
}
