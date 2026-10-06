package validate

import (
	"fmt"
	"strings"
)

// This file is the one place that answers "how deep does this bump go, and
// why". Every path through ResolveDepth produces a reason naming the input that
// decided, because `depth: options` alone gives an operator nothing to act on.
//
// THE ORDER OF AUTHORITY, increasing:
//
//	1. class → policy                     the configured depth for the bump's class
//	2. the binary tier lowers it to none   derived, not configured
//	3. configuration overrides it          carrying its stated reason
//	4. an operator flag replaces it        --depth, --compile
//
// ONLY AN EXPLICIT OPERATOR FLAG MAY LOWER DEPTH SILENTLY. Every other lowering
// is reported as skipped by policy and never as validated: a bump that received
// fewer gates than policy asked for proved less, not more cheaply.
//
// THE BUMP REVIEWER IS NOT A FIFTH LEVEL. Its proposal arrives after
// ResolveDepth has answered and is combined by Escalate, which may only RAISE
// it; moving it inside ResolveDepth would give it the authority to lower.

// typeBinary is the resolved package type that is validated at depth none.
//
// The match against it is EXACT — no trimming, no case folding — for the reason
// ParseDepth is exact: this string decides whether a bump is built at all, and
// the only values that can reach here are produced by Checker.resolveType or
// validated by the config loader, both of which emit exactly "bin" or "source".
// A value in any other spelling is therefore not a binary record whose type was
// written oddly; it is a type nobody resolved, and the safe reading of that is
// the full set of gates rather than none.
const typeBinary = "bin"

// DepthOverride is a per-package instruction to validate at a depth other than
// the one policy chose, together with the reason it exists.
//
// The reason is a field and not a comment because it is mandatory and
// consequential: an override that lowers depth removes a gate, and the report
// has to be able to say on whose authority. An override that lowers
// WITHOUT a reason is refused rather than applied — see ResolveDepth.
type DepthOverride struct {
	// Depth is the depth to use in place of the class default.
	Depth Depth
	// Reason is why this package is not validated the way its class says. It
	// travels into the decision verbatim so the report quotes the operator
	// rather than paraphrasing them.
	Reason string
}

// DepthPolicy is the configured half of a depth decision: what each class earns,
// and which packages are exceptions.
//
// It is expressed in this package's own types rather than in the config
// package's strings so that internal/autoupdate/validate stays free of a
// dependency on internal/common/config — the import already runs the other way
// (autoupdate imports validate), and a resolver that reads config would put the
// depth table behind a file format. Translating the configured strings into
// Depth values, and rejecting a typo by name through ParseDepth, is the job of
// whoever builds this struct.
type DepthPolicy struct {
	// ByClass is the depth each bump class earns. A class missing from
	// the map does not mean "no validation": see classDepth.
	ByClass map[Class]Depth
	// Overrides is keyed by the full package atom ("category/package"), the
	// same spelling the registry uses.
	Overrides map[string]DepthOverride
}

// DefaultDepthPolicy is the depth table this toolkit SHIPS: a revision or patch
// bump is read but never built, and a series or major bump is taken as far as
// configure.
//
// An empty policy must not mean this one's fallback. classDepth falls back to
// DepthCompile, which is right for a hole in a filled-in table but wrong for a
// table nobody filled in: then every revision bump would compile, a cost nobody
// agreed to. So a caller that supplies no table gets the shipped one.
//
// config.DefaultDepthRevision and its three siblings hold the same values as
// strings, because the config layer answers GetDepthForClass for a key the
// operator did not write, and validate cannot import config. THE TWO MUST BE
// READ TOGETHER: a change to either is a change to both.
func DefaultDepthPolicy() DepthPolicy {
	return DepthPolicy{
		ByClass: map[Class]Depth{
			ClassRevision: DepthOptions,
			ClassPatch:    DepthOptions,
			ClassSeries:   DepthConfigure,
			ClassMajor:    DepthConfigure,
		},
		Overrides: map[string]DepthOverride{},
	}
}

// DepthRequest is everything ResolveDepth is allowed to look at. It performs no
// I/O of its own, so a depth decision is reproducible from these fields alone
// and can be replayed in a report.
type DepthRequest struct {
	// Package is the full atom ("category/package"), used to find an override.
	Package string
	// Class is how far the bump moved, from Classify. Callers must pass the
	// class Classify returns even when it also returns an error — see
	// ClassifyForDepth.
	Class Class
	// ResolvedType is the checker's answer ("bin" or "source"), NOT the raw
	// PackageConfig.Type field. That field is empty for most records and the
	// type is auto-detected from the ebuild (RESTRICT=bindist, a -bin suffix, a
	// binary SRC_URI); a resolver reading the raw field would see "" for almost
	// every binary package in the registry and schedule a compile for a
	// prebuilt blob. It arrives as a plain string because the resolved type
	// lives on autoupdate.CheckResult, and this package cannot import
	// autoupdate without an import cycle.
	//
	// Empty means nobody resolved it. That is NOT the same as "source": an
	// unresolved record keeps every gate its class earns, because losing gates
	// must never be something that happens by omission.
	ResolvedType string
	// Policy is the configured depth table and its per-package exceptions.
	Policy DepthPolicy
	// FlagDepth is `--depth`, nil when the operator did not give it. It is a
	// pointer because DepthNone is a meaningful value an operator may ask for,
	// and the zero value would otherwise be indistinguishable from "unset" —
	// the one confusion that switches validation off in silence.
	FlagDepth *Depth
	// Compile is `--compile`, which is exactly `--depth=compile`.
	Compile bool
}

// DepthDecision is a depth and the case for it.
type DepthDecision struct {
	// Depth is how far this bump is validated.
	Depth Depth
	// Reason names the input that decided and, where one exists, quotes the
	// stated justification. It is never empty.
	Reason string
	// SkippedByPolicy reports that this bump received LESS validation than
	// policy chose for its class, and therefore must be counted as skipped
	// rather than validated. An explicit operator flag never sets it:
	// that is the operator's own instruction, not a package opting out.
	SkippedByPolicy bool
}

// ResolveDepth answers how deep a bump is validated and why.
//
// It applies the four levels of authority in order, and it is total: every
// request produces a depth and a non-empty reason, and no input other than an
// explicit operator flag can lower the depth without that being reported.
func ResolveDepth(req DepthRequest) DepthDecision {
	// 4. An explicit flag replaces the whole computation — class, tier and
	// configuration. The operator typing it is looking at one package and
	// knows something the policy does not, which is also why this is the one
	// input allowed to lower depth without being called a skip.
	if depth, spelling, ok := flagDepth(req); ok {
		return DepthDecision{
			Depth:  depth,
			Reason: fmt.Sprintf("flag: %s replaces the class, the package tier and configuration", spelling),
		}
	}

	// 1. class → policy. This is also the yardstick every later lowering
	// is measured against: "below what policy selected" means below this value,
	// not below whatever the previous step left behind.
	policyDepth, reason := classDepth(req.Policy, req.Class)
	depth := policyDepth

	// 2. A binary record is validated at depth none: there is no source
	// to unpack, patch, configure or compile, so no build gate can run.
	if req.ResolvedType == typeBinary {
		depth = DepthNone
		reason = fmt.Sprintf("binary record (resolved type %q): there is nothing to build from source, so no build gate can run", typeBinary)
	}

	// 3. Configuration overrides the class default and the tier, carrying its
	// stated reason.
	if override, ok := req.Policy.Overrides[req.Package]; ok {
		depth, reason = applyOverride(depth, policyDepth, override)
	}

	// The skipped-by-policy rule, applied once for every way depth can end up
	// below what policy chose, so a new lowering path cannot be added without
	// inheriting it.
	skipped := depth < policyDepth
	if skipped {
		reason += fmt.Sprintf("; this is less than the %s depth policy chose, so the bump is reported as skipped by policy, never as validated", policyDepth)
	}

	return DepthDecision{Depth: depth, Reason: reason, SkippedByPolicy: skipped}
}

// flagDepth reads the operator's flags, reporting the depth, the spelling to
// name in the reason, and whether a flag was given at all.
//
// `--compile` is routed through the same return as `--depth=compile` rather than
// short-circuiting somewhere of its own, because the two are the same
// request and a second path is exactly how they would drift apart. The spelling
// still differs in the reason: the report should name the flag the operator
// actually typed.
//
// When both are given, `--depth` wins — it names a depth explicitly, while
// `--compile` is a shorthand. A command that accepts both should reject the
// combination before it reaches here rather than rely on this tie-break.
func flagDepth(req DepthRequest) (Depth, string, bool) {
	if req.FlagDepth != nil {
		return *req.FlagDepth, "--depth=" + req.FlagDepth.String(), true
	}
	if req.Compile {
		return DepthCompile, "--compile (the same request as --depth=compile)", true
	}
	return DepthNone, "", false
}

// classDepth reads the configured depth for a class.
//
// A class absent from the table falls through to the major row, and a table with
// no major row falls through to DepthCompile. NEITHER falls through to
// DepthNone, which is what a bare map lookup would return: not knowing how far a
// bump moved is not evidence that it moved a little.
//
// The fallback STAYS at compile although DepthInstall is now the deepest rung:
// it is a fail-safe for a hole in a filled-in table, not a place to raise what
// an unconfigured bump costs. Returning DepthInstall would change a shipped cost.
//
// It is the same reading Classify applies when it answers ClassMajor for a
// version it cannot parse, and the same one config.GetDepthForClass applies to
// an unrecognised class name.
func classDepth(policy DepthPolicy, class Class) (Depth, string) {
	if depth, ok := policy.ByClass[class]; ok {
		return depth, fmt.Sprintf("policy: a %s bump is validated to %s", class, depth)
	}
	if depth, ok := policy.ByClass[ClassMajor]; ok {
		return depth, fmt.Sprintf("policy: no depth is configured for a %s bump, so the major row (%s) applies", class, depth)
	}
	return DepthCompile, fmt.Sprintf("policy: no depth is configured for a %s bump, nor for major, so %s applies rather than none", class, DepthCompile)
}

// applyOverride applies a per-package override on top of the depth the class and
// the tier produced, measuring it against policyDepth — what policy chose for the
// class — because that is the comparison the reason and skip rules are about.
//
// An override that LOWERS depth and states no reason is refused rather than
// applied. The reason is mandatory precisely because a lowering removes
// a gate, and applying a reasonless one would be a code path by which something
// other than an operator flag lowers depth silently. Refusing it is loud in the
// only way this function can be loud: the policy depth stands and the reason
// says why the override did not take effect.
func applyOverride(depth, policyDepth Depth, override DepthOverride) (Depth, string) {
	if override.Reason == "" && override.Depth < depth {
		return depth, fmt.Sprintf("override refused: it lowers %s to %s without stating a reason, which a lowering override must, so the %s depth stands",
			depth, override.Depth, depth)
	}

	return override.Depth, fmt.Sprintf("override (%s, %s): %s", override.Depth, relativeToPolicy(override.Depth, policyDepth), overrideReason(override))
}

// relativeToPolicy describes an override's depth against the one policy chose.
// The report prints both numbers anyway; what an operator cannot reconstruct
// from them is whether the override bought more scrutiny or sold it, and that is
// the difference between a deliberate exception and a package quietly opting out.
//
// The lowering arm says only "below policy" because ResolveDepth appends the
// skipped-by-policy note right after it, and that note already names the policy depth. Saying
// it twice in one sentence reads as a stutter and buries the consequence.
func relativeToPolicy(overrideDepth, policyDepth Depth) string {
	switch {
	case overrideDepth > policyDepth:
		return fmt.Sprintf("above the %s depth policy chose", policyDepth)
	case overrideDepth < policyDepth:
		return "below policy"
	default:
		return "the same depth policy chose"
	}
}

// overrideReason keeps DepthDecision.Reason non-empty and self-explaining even
// for an override that raises depth without saying why — the case applyOverride
// lets through, since raising scrutiny needs no defence.
func overrideReason(override DepthOverride) string {
	if override.Reason == "" {
		return "no reason stated"
	}
	return override.Reason
}

// Escalate combines the depth policy already resolved — the floor — with a depth
// the bump reviewer proposes, answering the depth to validate at and the case
// for it.
//
// THE RULE IS max(floor, proposed), AND IT IS ONE-WAY. A reviewer reads a diff;
// it lacks an operator flag's authority, so it may buy more scrutiny and never
// sell any. The ladder's integer ordering (see depth.go) is what makes max mean
// "the deeper of the two".
//
// IT TAKES PRIMITIVES, NOT autoupdate.BumpReviewReport: internal/autoupdate
// imports this package, so accepting that type is an import CYCLE. The caller
// calls this only when the report's ProposedDepth pointer is non-nil, keeping
// "proposed nothing" and "proposed none" different facts.
//
// A PROPOSAL THAT RAISES DEPTH AND STATES NO REASON IS REFUSED, like a
// reasonless lowering override: a depth an operator cannot account for is one
// they will switch off. The floor is returned and the reason says why. The
// reason credits the reviewer ONLY when it actually added a gate.
func Escalate(floor, proposed Depth, reason string) (Depth, string) {
	if proposed <= floor {
		return floor, floorStands(floor, proposed)
	}

	// Trimmed rather than compared against "": a reason made of spaces reports
	// nothing an operator can act on, and would print as an empty quotation
	// beside the raised depth, which is worse than saying it was refused.
	if strings.TrimSpace(reason) == "" {
		return floor, fmt.Sprintf("reviewer proposal refused: it raises %s to %s without stating a reason, which a raised depth must carry, so the %s depth stands",
			floor, proposed, floor)
	}

	// The reviewer's words travel verbatim, as an override's do: the report
	// quotes whoever asked for the extra gate rather than paraphrasing them.
	return proposed, fmt.Sprintf("reviewer escalation (%s, above the %s depth policy chose): %s", proposed, floor, reason)
}

// floorStands explains a proposal that decided nothing, and says which of the two
// ways it decided nothing — agreeing with policy and being overruled by it are
// the same depth but not the same fact.
//
// It quotes NEITHER the proposal's reasoning NOR the reviewer, because a
// proposal that was not applied must not read as though it were: a report saying
// "the upstream diff looks harmless to me" beside a compile depth invites the
// operator to believe the reviewer chose it.
func floorStands(floor, proposed Depth) string {
	if proposed < floor {
		return fmt.Sprintf("policy: the %s depth stands; a shallower proposal of %s does not lower it, because nothing short of an explicit operator flag may",
			floor, proposed)
	}
	return fmt.Sprintf("policy: the %s depth stands; the proposal matched it and decided nothing", floor)
}

// ClassifyForDepth classifies a bump for ResolveDepth without offering the
// caller a way to throw the classification away.
//
// Classify returns ClassMajor AND an error for a version it cannot read:
// both halves matter, the class because it is the safe answer and the error
// because it explains why the bump is about to be validated at the greatest
// depth. The reflexive `if err != nil { return }` at a call site discards the
// class this contract exists to provide, and the bump then gets no validation at
// all instead of the deepest — the exact inversion of what the fallback is for.
//
// So this returns a NOTE rather than an error: there is nothing here to abort
// on. The note is empty when both versions read cleanly, and otherwise says what
// could not be read, for the caller to log or carry into the report beside the
// depth.
func ClassifyForDepth(oldPV, newPV string) (Class, string) {
	class, err := Classify(oldPV, newPV)
	if err != nil {
		return class, fmt.Sprintf("the bump could not be classified, so it is treated as %s: %v", class, err)
	}
	return class, ""
}
