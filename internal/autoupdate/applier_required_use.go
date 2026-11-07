package autoupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/targets"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// requiredUseGate checks the staged candidate against the overlay's
// .autoupdate/targets.toml. ok is false when the overlay declares no policy:
// there is then nothing to report, and no gate is listed.
//
// Only a REGRESSION fails: an implementation the candidate drops while a
// published version of the same package offers it. A gap the published
// versions already have is a warning, because refusing the bump would keep the
// older version, which is no more installable — the sweep, `overlay targets`,
// is where that backlog is worked down.
func (a *Applier) requiredUseGate(cand candidatePaths, pkg, version string) (validate.GateResult, bool) {
	policy, err := targets.LoadPolicy(a.overlayPath)
	if errors.Is(err, targets.ErrNoPolicy) {
		return validate.GateResult{}, false
	}
	skip := func(format string, args ...any) (validate.GateResult, bool) {
		return validate.GateResult{
			Gate:    validate.GateRequiredUse,
			Outcome: validate.OutcomeSkipped,
			Reason:  fmt.Sprintf(format, args...),
		}, true
	}
	if err != nil {
		a.logger().Warn("the target policy could not be read", "package", pkg, "version", version, "err", err)
		return skip("the target policy could not be read, so %s-%s was not checked against it: %v", pkg, version, err)
	}

	atom := PackageAtom(pkg)
	content, err := os.ReadFile(cand.ebuildPath)
	if err != nil {
		return skip("the staged ebuild of %s-%s could not be read: %v", pkg, version, err)
	}
	offered, err := targets.FromEbuild(content)
	if err != nil {
		return skip("what %s-%s offers could not be read from its text: %v", pkg, version, err)
	}

	gaps := policy.Check(atom, offered)
	if len(gaps) == 0 {
		return validate.GateResult{
			Gate:    validate.GateRequiredUse,
			Outcome: validate.OutcomePass,
			Reason:  fmt.Sprintf("%s-%s offers every implementation %s requires", pkg, version, targets.PolicyFile),
		}, true
	}

	published := a.publishedOffers(atom, cand.ebuildPath)
	var findings []validate.Finding
	regressed := false
	for _, g := range gaps {
		severity, detail := validate.SeverityWarning, fmt.Sprintf("%s; no published version offers it either", g)
		if lost := lostImpls(g, published); len(lost) > 0 {
			regressed = true
			severity = validate.SeverityError
			detail = fmt.Sprintf("%s; a published version offers %s, so this bump would take it away", g, strings.Join(lost, " "))
		}
		findings = append(findings, validate.Finding{Gate: validate.GateRequiredUse, Severity: severity, Detail: detail})
	}

	if !regressed {
		return validate.GateResult{
			Gate:     validate.GateRequiredUse,
			Outcome:  validate.OutcomePass,
			Reason:   fmt.Sprintf("%s-%s lacks a required implementation the published versions already lack; `bentoo overlay targets` lists it", pkg, version),
			Findings: findings,
		}, true
	}
	return validate.GateResult{
		Gate:    validate.GateRequiredUse,
		Outcome: validate.OutcomeFailed,
		Reason: fmt.Sprintf("%s-%s drops an implementation %s requires and a published version offers; widen its *_COMPAT before publishing",
			pkg, version, targets.PolicyFile),
		Findings: findings,
		Declined: validate.DeclineCandidate,
	}, true
}

// publishedOffers reads what every published ebuild of atom offers, skipping
// the one being replaced in place and any whose text cannot be read: an
// unreadable sibling only narrows what counts as a regression.
func (a *Applier) publishedOffers(atom, candidatePath string) []targets.Offered {
	paths, err := filepath.Glob(filepath.Join(a.overlayPath, atom, "*.ebuild"))
	if err != nil {
		return nil
	}
	var offers []targets.Offered
	for _, path := range paths {
		if filepath.Base(path) == filepath.Base(candidatePath) {
			continue
		}
		content, err := os.ReadFile(path) //nolint:gosec // G304: an ebuild of the overlay this applier was constructed for
		if err != nil {
			continue
		}
		if o, err := targets.FromEbuild(content); err == nil {
			offers = append(offers, o)
		}
	}
	return offers
}

// lostImpls returns the missing implementations of g that some published
// version offers.
func lostImpls(g targets.Gap, published []targets.Offered) []string {
	var lost []string
	for _, impl := range g.Missing {
		for _, o := range published {
			if slices.Contains(o[g.Group], impl) {
				lost = append(lost, impl)
				break
			}
		}
	}
	return lost
}

// requiredUseResolveFailure turns a pretend resolve refused for REQUIRED_USE
// into a FAILED required-use gate — but only when a refused constraint involves
// a group the overlay's policy requires. That is what makes it the candidate's
// fault under the overlay's own declaration; any other unsatisfied constraint is
// this host's USE, and keeps the undetermined answer it always had.
func (a *Applier) requiredUseResolveFailure(pkg, version string, err error) (validate.GateResult, bool) {
	var rue *validate.RequiredUseError
	if !errors.As(err, &rue) {
		return validate.GateResult{}, false
	}
	policy, perr := targets.LoadPolicy(a.overlayPath)
	if perr != nil {
		return validate.GateResult{}, false
	}
	var findings []validate.Finding
	for _, c := range rue.Constraints {
		if policy.MentionsRequiredGroup(c) {
			findings = append(findings, validate.Finding{Gate: validate.GateRequiredUse, Severity: validate.SeverityError, Detail: c})
		}
	}
	if len(findings) == 0 {
		return validate.GateResult{}, false
	}
	return validate.GateResult{
		Gate:    validate.GateRequiredUse,
		Outcome: validate.OutcomeFailed,
		Reason: fmt.Sprintf("emerge refused %s-%s: its REQUIRED_USE cannot be met with the implementations %s requires",
			pkg, version, targets.PolicyFile),
		Findings: findings,
		Declined: validate.DeclineCandidate,
	}, true
}
