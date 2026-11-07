package autoupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

const requiredUsePolicy = "[required]\npython_single_target = [\"python3_15\"]\n"

func writeRequiredUseFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// requiredUseFixture builds a published overlay holding vim at oldCompat and a
// staged candidate at newCompat, and returns the applier and the candidate.
func requiredUseFixture(t *testing.T, policy, oldCompat, newCompat string) (*Applier, candidatePaths) {
	t.Helper()
	overlay, staged := t.TempDir(), t.TempDir()
	if policy != "" {
		writeRequiredUseFile(t, filepath.Join(overlay, ".autoupdate", "targets.toml"), policy)
	}
	ebuild := func(compat string) string {
		return "EAPI=8\nPYTHON_COMPAT=( " + compat + " )\ninherit python-single-r1\n"
	}
	writeRequiredUseFile(t, filepath.Join(overlay, "app-editors/vim/vim-9.2.1275.ebuild"), ebuild(oldCompat))
	candPath := filepath.Join(staged, "app-editors/vim/vim-9.2.1302.ebuild")
	writeRequiredUseFile(t, candPath, ebuild(newCompat))
	return &Applier{overlayPath: overlay}, candidatePaths{staged: true, repoRoot: staged, pkgDir: filepath.Dir(candPath), ebuildPath: candPath}
}

func TestRequiredUseGateFailsOnRegression(t *testing.T) {
	a, cand := requiredUseFixture(t, requiredUsePolicy, "python3_{12..15}", "python3_{12..14}")
	res, ok := a.requiredUseGate(cand, "app-editors/vim", "9.2.1302")
	if !ok || res.Gate != validate.GateRequiredUse || res.Outcome != validate.OutcomeFailed || res.Declined != validate.DeclineCandidate {
		t.Fatalf("gate = %+v, want a FAILED required-use stamped on the candidate", res)
	}
	if len(res.Findings) != 1 || res.Findings[0].Severity != validate.SeverityError {
		t.Fatalf("findings = %+v, want one error", res.Findings)
	}
	if promoted, _ := validate.PromotionDecision([]validate.GateResult{res}, nil); promoted {
		t.Fatal("PromotionDecision promoted a candidate the required-use gate failed")
	}
}

func TestRequiredUseGateWarnsOnPreexistingGap(t *testing.T) {
	a, cand := requiredUseFixture(t, requiredUsePolicy, "python3_{12..14}", "python3_{12..14}")
	res, ok := a.requiredUseGate(cand, "app-editors/vim", "9.2.1302")
	if !ok || res.Outcome != validate.OutcomePass || len(res.Findings) != 1 || res.Findings[0].Severity != validate.SeverityWarning {
		t.Fatalf("gate = %+v, want PASS with one warning", res)
	}
}

func TestRequiredUseGatePasses(t *testing.T) {
	a, cand := requiredUseFixture(t, requiredUsePolicy, "python3_{12..14}", "python3_{12..15}")
	res, ok := a.requiredUseGate(cand, "app-editors/vim", "9.2.1302")
	if !ok || res.Outcome != validate.OutcomePass || len(res.Findings) != 0 {
		t.Fatalf("gate = %+v, want a clean PASS", res)
	}
}

func TestRequiredUseGateAbsentWithoutPolicy(t *testing.T) {
	a, cand := requiredUseFixture(t, "", "python3_15", "python3_12")
	if res, ok := a.requiredUseGate(cand, "app-editors/vim", "9.2.1302"); ok {
		t.Fatalf("gate = %+v, want none without a policy", res)
	}
}

func TestRequiredUseGateSkips(t *testing.T) {
	t.Run("broken policy", func(t *testing.T) {
		a, cand := requiredUseFixture(t, "[required\n", "python3_15", "python3_15")
		res, ok := a.requiredUseGate(cand, "app-editors/vim", "9.2.1302")
		if !ok || res.Outcome != validate.OutcomeSkipped || res.Reason == "" {
			t.Fatalf("gate = %+v, want a SKIPPED with a reason", res)
		}
	})
	t.Run("unreadable compat", func(t *testing.T) {
		a, cand := requiredUseFixture(t, requiredUsePolicy, "python3_15", "${X}")
		res, ok := a.requiredUseGate(cand, "app-editors/vim", "9.2.1302")
		if !ok || res.Outcome != validate.OutcomeSkipped {
			t.Fatalf("gate = %+v, want SKIPPED", res)
		}
	})
	t.Run("missing candidate", func(t *testing.T) {
		a, cand := requiredUseFixture(t, requiredUsePolicy, "python3_15", "python3_15")
		cand.ebuildPath = filepath.Join(cand.repoRoot, "nope.ebuild")
		res, ok := a.requiredUseGate(cand, "app-editors/vim", "9.2.1302")
		if !ok || res.Outcome != validate.OutcomeSkipped {
			t.Fatalf("gate = %+v, want SKIPPED", res)
		}
	})
}

func TestRequiredUseResolveFailure(t *testing.T) {
	a, _ := requiredUseFixture(t, requiredUsePolicy, "python3_15", "python3_15")
	wrap := func(constraints ...string) error {
		return fmt.Errorf("%w: %w", validate.ErrDependenciesUndetermined, &validate.RequiredUseError{Constraints: constraints})
	}

	res, ok := a.requiredUseResolveFailure("app-editors/vim", "9.2.1302",
		wrap("python? ( exactly-one-of ( python_single_target_python3_12 ) )", "images? ( !minimal )"))
	if !ok || res.Outcome != validate.OutcomeFailed || len(res.Findings) != 1 {
		t.Fatalf("result = %+v, want FAILED naming only the policy's constraint", res)
	}
	if _, ok := a.requiredUseResolveFailure("app-editors/vim", "9.2.1302", wrap("images? ( !minimal )")); ok {
		t.Error("a constraint outside the policy was blamed on the candidate")
	}
	if _, ok := a.requiredUseResolveFailure("app-editors/vim", "9.2.1302", errors.New("emerge: not found")); ok {
		t.Error("a resolve that merely failed was read as REQUIRED_USE")
	}
	noPolicy := &Applier{overlayPath: t.TempDir()}
	if _, ok := noPolicy.requiredUseResolveFailure("app-editors/vim", "9.2.1302", wrap("python_single_target_python3_12")); ok {
		t.Error("an overlay without a policy failed a candidate")
	}
}
