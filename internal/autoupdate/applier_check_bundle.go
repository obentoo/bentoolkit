package autoupdate

import (
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// adoptForCheck is the check's side of WithAdoptedStagedTree: the candidate a
// --repair session left in the kept tree is validated as it stands, never
// restaged over, with the depth raised to the one the run that kept it
// recorded. It is the apply's adoption, run on the check's own result.
func (a *Applier) adoptForCheck(pkg, currentVersion, newVersion string, update *PendingUpdate,
	depth validate.DepthDecision, local *ApplyResult) (candidatePaths, validate.DepthDecision, error) {
	r := &applyRun{a: a, pkg: pkg, result: local, update: update,
		currentVersion: currentVersion, newVersion: newVersion, depth: depth}
	if err := r.adoptCandidate(a.adoptedRoot); err != nil {
		return candidatePaths{}, depth, err
	}
	return r.cand, r.depth, nil
}

// writeCheckBundle leaves the repair bundle of a check whose bump needs the
// maintainer (HUMAN) beside its kept staged tree, so `--repair` has the
// failure, the tree and the fixer's session to work from. Since apply only
// publishes, the check is the one run left where a bump fails.
//
// A bump that is READY after a repair has its bundle removed: the repair is
// done, and a marked bundle would keep `--apply` from publishing the proof.
// Any other state leaves an existing bundle alone. A bundle that cannot be
// written is a WARN; the check's verdict is already decided.
func (a *Applier) writeCheckBundle(pkg string, local *ApplyResult, cand candidatePaths, gates []validate.GateResult,
	depth validate.Depth, manifestFailed bool) {
	if local.StagedPath == "" {
		return
	}
	switch report.State(proofState(gates, depth)) {
	case report.Ready:
		if a.adoptedRoot != "" {
			if err := repair.RemoveBundle(local.StagedPath); err != nil {
				a.logger().Warn("repair: the repaired bump is proven but its repair bundle could not be removed",
					"package", pkg, "staged_root", local.StagedPath, "err", err)
			}
		}
		return
	case report.Human:
	default:
		return
	}

	stage, failure := checkFailure(gates, manifestFailed)
	bundle := repair.Bundle{
		Schema:         repair.SchemaVersion,
		Package:        pkg,
		CurrentVersion: local.OldVersion,
		Candidate:      local.NewVersion,
		Stage:          stage,
		Failure:        repair.Redact(failure, secrets.Resolved()),
		RefusedTools:   local.RefusedTools,
		StagedRoot:     local.StagedPath,
		EbuildPath:     cand.ebuildPath,
		UpstreamURLs:   a.configs[pkg].UpstreamURLs(),
		FixerSession:   local.FixerSession,
		Created:        time.Now().UTC(),
	}
	if a.adoptedRoot != "" {
		bundle.RepairedAt = bundle.Created
	}
	if err := repair.WriteBundle(bundle); err != nil {
		a.logger().Warn("repair: could not write the repair bundle beside the kept staged tree (the check's verdict is unchanged)",
			"package", pkg, "staged_root", local.StagedPath, "err", err)
		return
	}
	local.RepairBundle = local.StagedPath + "/" + repair.BundleFile
}

// checkFailure names the stage that stopped a HUMAN bump, in the bundle's
// "gate:<name>" / "manifest" spelling, and the text the session is handed:
// the first failed gate with its findings, else the first gate that declined
// because of the candidate.
func checkFailure(gates []validate.GateResult, manifestFailed bool) (stage, failure string) {
	for _, g := range gates {
		if g.Outcome != validate.OutcomeFailed {
			continue
		}
		lines := []string{g.Reason}
		for _, f := range g.Findings {
			lines = append(lines, f.Detail)
		}
		return "gate:" + g.Gate, strings.Join(lines, "\n")
	}
	for _, g := range gates {
		if g.Outcome == validate.OutcomeSkipped && g.Declined == validate.DeclineCandidate {
			if manifestFailed {
				return "manifest", g.Reason
			}
			return "gate:" + g.Gate, g.Reason
		}
	}
	return "check", "the check could not attribute the failure to one gate"
}
