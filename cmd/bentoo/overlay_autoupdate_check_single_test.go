package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// TestCheckLLMNamedPackage_ReachesValidation pins the fix for #338 item 2:
// `--check --llm <package>` is the command every "unproven" apply result
// recommends, and it returned before the validation the batch path runs. The
// named package must be planned and handed to the validator.
func TestCheckLLMNamedPackage_ReachesValidation(t *testing.T) {
	td := defaultDeps()
	auOpts := testAutoupdateOptions()
	f := checkableFixture(t)
	pinCheckFlags(t, auOpts)
	auOpts.llm = true
	auOpts.yes = true

	var built int
	td.newApplier = func(string, string, ...autoupdate.ApplierOption) (*autoupdate.Applier, error) {
		built++
		return nil, errors.New("stub validator: the test stops here")
	}

	var code int
	out := captureStdout(t, func() {
		code = exitCodeFor(testAutoupdateRunWith(auOpts, td).runCheck(context.Background(), f.overlayDir, t.TempDir(),
			[]string{"app-editors/neovim"}, 0, &config.Config{}, config.LLMConfig{}))
	})

	if code != 0 {
		t.Errorf("runCheck exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "Validation Plan") || !strings.Contains(out, "app-editors/neovim 0.11.1 → 9.9.9") {
		t.Errorf("the named package was not planned for validation; stdout:\n%s", out)
	}
	if built != 1 {
		t.Errorf("the validator was built %d times, want 1", built)
	}
}

// TestCheckNamedPackage_WithoutLLMValidatesNothing: the flag still decides
// whether the gates run, on the single-package path as on the batch one.
func TestCheckNamedPackage_WithoutLLMValidatesNothing(t *testing.T) {
	td := defaultDeps()
	auOpts := testAutoupdateOptions()
	f := checkableFixture(t)
	pinCheckFlags(t, auOpts)

	td.newApplier = func(string, string, ...autoupdate.ApplierOption) (*autoupdate.Applier, error) {
		t.Error("a check without --llm built a validator")
		return nil, errors.New("unreachable")
	}

	out := captureStdout(t, func() {
		_ = testAutoupdateRunWith(auOpts, td).runCheck(context.Background(), f.overlayDir, t.TempDir(),
			[]string{"app-editors/neovim"}, 0, &config.Config{}, config.LLMConfig{})
	})
	if strings.Contains(out, "Validation Plan") {
		t.Errorf("a check without --llm printed a validation plan; stdout:\n%s", out)
	}
}
