package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// checkedRun is a finished check whose only bump is in state.
func checkedRun(state report.State) report.Run {
	return report.Run{Payload: report.AutoupdateCheck{Results: []report.ValidationRow{
		{Package: repairPkg, CandidateVersion: repairCand, State: state},
	}}}
}

// offerFixture is a repair fixture and a run whose session question is
// answered by answer, counting how often it is asked.
func offerFixture(t *testing.T, answer bool) (*repairFix, *autoupdateRun, *int) {
	t.Helper()
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 0)
	auOpts.repair = ""
	asked := 0
	d := ttyDeps(true, true)
	d.confirmRepair = func(string) bool { asked++; return answer }
	return f, testAutoupdateRunWith(auOpts, d), &asked
}

// TestOfferRepairs_AHumanBumpIsOfferedAndRepaired: at the end of an
// interactive check, a HUMAN bump with a bundle is offered a session; a yes
// runs the --repair flow, which proves the tree and publishes nothing.
func TestOfferRepairs_AHumanBumpIsOfferedAndRepaired(t *testing.T) {
	f, ar, asked := offerFixture(t, true)

	ar.offerRepairs(context.Background(), f.overlay, f.configDir, config.AutoupdateConfig{}, checkedRun(report.Human))

	if *asked != 1 {
		t.Fatalf("the session question was asked %d times, want once", *asked)
	}
	if _, err := os.Stat(f.record + ".argv"); err != nil {
		t.Fatalf("the repair session did not run: %v", err)
	}
	if _, err := os.Stat(f.bundlePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the proven repair kept its bundle (stat %v)", err)
	}
	if _, err := os.Stat(filepath.Join(f.overlay, "dev-go", "repairdemo", "repairdemo-"+repairCand+".ebuild")); err == nil {
		t.Error("the offered repair published the candidate; only --apply publishes")
	}
}

// TestOfferRepairs_NoSkipsTheBump: "n" leaves the bundle and the tree alone.
func TestOfferRepairs_NoSkipsTheBump(t *testing.T) {
	f, ar, asked := offerFixture(t, false)
	before := readFileOrEmpty(f.bundlePath)

	ar.offerRepairs(context.Background(), f.overlay, f.configDir, config.AutoupdateConfig{}, checkedRun(report.Human))

	if *asked != 1 {
		t.Fatalf("asked %d times, want once", *asked)
	}
	if _, err := os.Stat(f.record + ".argv"); err == nil {
		t.Error("a declined offer started a session")
	}
	if readFileOrEmpty(f.bundlePath) != before {
		t.Error("a declined offer changed the bundle")
	}
}

// TestOfferRepairs_NeverAsksAnUnattendedRun: --yes, a run without a terminal,
// and a bump that is not HUMAN are never offered anything.
func TestOfferRepairs_NeverAsksAnUnattendedRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(ar *autoupdateRun)
		state report.State
	}{
		{"--yes", func(ar *autoupdateRun) { ar.opts.yes = true }, report.Human},
		{"no terminal", func(ar *autoupdateRun) { ar.deps.uiIsTerminal = func() bool { return false } }, report.Human},
		{"a READY bump", func(*autoupdateRun) {}, report.Ready},
		{"a WAIT bump", func(*autoupdateRun) {}, report.Wait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, ar, asked := offerFixture(t, true)
			tc.setup(ar)
			ar.offerRepairs(context.Background(), f.overlay, f.configDir, config.AutoupdateConfig{}, checkedRun(tc.state))
			if *asked != 0 {
				t.Errorf("asked %d times, want never", *asked)
			}
		})
	}
}
