package validate

// Authored for story 033, sub-task 9.2 — R2, R2.2, R2.3, R2.6, R2.7, R2.8.
//
// ONE PLACE THAT ANSWERS "HOW DEEP DOES THIS BUMP GO, AND WHY". The second half
// of that sentence is why every case below asserts the REASON as well as the
// depth. A report that says `depth: options` tells the operator nothing they can
// act on; `depth: options (policy: patch bump)` and `depth: options (override:
// eight hours of compile for a browser we do not patch)` are different facts
// with different remedies, and the difference is invisible unless the resolver
// carries it.
//
// THE ORDER OF AUTHORITY, increasing: class → policy; the package TIER may lower
// it to none; configuration may override with a stated reason; `--depth`
// replaces the whole computation; `--compile` is `--depth=compile`.
//
// R2.3 HAS A TRAP AND IT GETS TWO ROWS. `PackageConfig.Type` is EMPTY for most
// records — the type is auto-detected from the ebuild (RESTRICT=bindist, a -bin
// suffix, a binary SRC_URI) and the resolved answer lives on the checker's
// result (checker.go:72-75). A resolver that reads the raw config field gets ""
// for almost every binary package in the registry and cheerfully schedules a
// compile for a prebuilt blob. So the binary case is asserted twice: once with
// the field set, once with it empty and the resolved type supplied.
//
// R2.6 IS THE HONESTY RULE. An override that LOWERS the depth below what policy
// chose did not validate the bump more cheaply — it validated it less. Reporting
// that as "validated" would let a package opt out of the gate and still appear
// in the proved column, which is the one number this whole story is about.
//
// This file pins `DepthPolicy{ByClass, Overrides}`, `DepthOverride{Depth,
// Reason}`, `DepthRequest{Package, Class, ResolvedType, Policy, FlagDepth,
// Compile}`, `DepthDecision{Depth, Reason, SkippedByPolicy}` and
// `ResolveDepth(DepthRequest) DepthDecision`.

import (
	"strings"
	"testing"
)

// housePolicy is the shape an operator's config.yaml resolves to: cheap bumps
// get the static gate, a series crossing earns the configure gate, and a major
// bump earns a compile.
func housePolicy() DepthPolicy {
	return DepthPolicy{
		ByClass: map[Class]Depth{
			ClassRevision: DepthOptions,
			ClassPatch:    DepthOptions,
			ClassSeries:   DepthConfigure,
			ClassMajor:    DepthCompile,
		},
		Overrides: map[string]DepthOverride{},
	}
}

func requestFor(class Class) DepthRequest {
	return DepthRequest{
		Package:      "media-plugins/gst-plugins-qt6",
		Class:        class,
		ResolvedType: "source",
		Policy:       housePolicy(),
	}
}

// TestResolveDepth_EachClassMapsToItsConfiguredDepth is R2.2, and the reason
// assertion is half of it: the report has to be able to say that POLICY decided.
func TestResolveDepth_EachClassMapsToItsConfiguredDepth(t *testing.T) {
	tests := []struct {
		name  string
		class Class
		want  Depth
	}{
		{"revision", ClassRevision, DepthOptions},
		{"patch", ClassPatch, DepthOptions},
		{"series crossing", ClassSeries, DepthConfigure},
		{"major", ClassMajor, DepthCompile},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveDepth(requestFor(tt.class))

			if got.Depth != tt.want {
				t.Errorf("depth = %v, want %v", got.Depth, tt.want)
			}
			if got.Reason == "" {
				t.Error("the decision carries no reason; the report cannot say which input decided")
			}
			if !strings.Contains(strings.ToLower(got.Reason), "policy") {
				t.Errorf("reason %q does not name policy as the deciding input", got.Reason)
			}
			if got.SkippedByPolicy {
				t.Error("a depth chosen by policy is reported as skipped by policy")
			}
		})
	}
}

// TestResolveDepth_BinaryRecordIsNoneWhicheverWayTheTypeWasResolved is R2.3 and
// its trap. Most records leave Type empty; reading the raw field is the bug
// waiting to happen, and it schedules a compile for a prebuilt blob.
func TestResolveDepth_BinaryRecordIsNoneWhicheverWayTheTypeWasResolved(t *testing.T) {
	tests := []struct {
		name         string
		resolvedType string
		note         string
	}{
		{"type declared in packages.toml", "bin", "the operator wrote type = \"bin\""},
		{"type auto-detected from the ebuild", "bin", "the field is empty and the checker resolved it from RESTRICT=bindist / a -bin suffix / a binary SRC_URI"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := requestFor(ClassMajor) // the deepest class policy has
			req.Package = "app-editors/vscode-bin"
			req.ResolvedType = tt.resolvedType

			got := ResolveDepth(req)

			if got.Depth != DepthNone {
				t.Errorf("depth = %v, want DepthNone for a binary record (%s) — there is nothing to build", got.Depth, tt.note)
			}
			if got.Reason == "" || !strings.Contains(strings.ToLower(got.Reason), "bin") {
				t.Errorf("reason %q does not say the record is binary", got.Reason)
			}
		})
	}
}

// TestResolveDepth_TheResolvedTypeIsWhatCounts states the trap directly: an
// empty raw Type is NOT a source package, it is an unresolved one, and the
// resolver must be given the checker's answer rather than the config field.
func TestResolveDepth_TheResolvedTypeIsWhatCounts(t *testing.T) {
	req := requestFor(ClassMajor)
	req.Package = "app-editors/vscode-bin"
	req.ResolvedType = "" // nobody resolved it

	got := ResolveDepth(req)

	if got.Depth == DepthNone {
		t.Error("an UNRESOLVED type was treated as binary; a package whose type nobody determined must not silently lose its gates")
	}
	if got.Reason == "" {
		t.Error("the decision carries no reason")
	}
}

// TestResolveDepth_OverrideRaisesAndCarriesItsReason is R2.4 seen from the
// resolver: an override that asks for MORE is applied, and the stated reason
// travels with it.
func TestResolveDepth_OverrideRaisesAndCarriesItsReason(t *testing.T) {
	req := requestFor(ClassPatch) // policy would choose options
	req.Policy.Overrides["media-plugins/gst-plugins-qt6"] = DepthOverride{
		Depth:  DepthConfigure,
		Reason: "this family hardcodes an 85-option list and drifts every series",
	}

	got := ResolveDepth(req)

	if got.Depth != DepthConfigure {
		t.Errorf("depth = %v, want DepthConfigure — the override asks for more than policy", got.Depth)
	}
	if !strings.Contains(got.Reason, "85-option list") {
		t.Errorf("reason %q does not carry the override's stated reason (R2.4)", got.Reason)
	}
	if got.SkippedByPolicy {
		t.Error("an override that RAISED the depth is reported as skipped by policy")
	}
}

// TestResolveDepth_OverrideLoweringIsSkippedByPolicyNeverValidated is R2.6, and
// it is the assertion with consequences: the tally at the end of a run counts
// proved, errored and skipped, and a package that opted out of its gates belongs
// in the third column.
func TestResolveDepth_OverrideLoweringIsSkippedByPolicyNeverValidated(t *testing.T) {
	req := requestFor(ClassSeries) // policy would choose configure
	req.Policy.Overrides["media-plugins/gst-plugins-qt6"] = DepthOverride{
		Depth:  DepthOptions,
		Reason: "the build needs a GPU this host does not have",
	}

	got := ResolveDepth(req)

	if got.Depth != DepthOptions {
		t.Errorf("depth = %v, want DepthOptions — the override is honoured", got.Depth)
	}
	if !got.SkippedByPolicy {
		t.Error("an override BELOW the policy depth is not reported as skipped by policy; the bump would then appear in the proved column " +
			"having been checked less than policy asked for (R2.6)")
	}
	if !strings.Contains(got.Reason, "GPU") {
		t.Errorf("reason %q does not carry the override's stated reason", got.Reason)
	}
}

// TestResolveDepth_OverrideEqualToPolicyIsNotASkip keeps the flag meaningful.
// An override that restates what policy already chose changed nothing, and
// marking it skipped would understate the run.
func TestResolveDepth_OverrideEqualToPolicyIsNotASkip(t *testing.T) {
	req := requestFor(ClassSeries)
	req.Policy.Overrides["media-plugins/gst-plugins-qt6"] = DepthOverride{
		Depth:  DepthConfigure,
		Reason: "pinned deliberately so a policy change does not move it",
	}

	got := ResolveDepth(req)

	if got.SkippedByPolicy {
		t.Errorf("an override equal to the policy depth (%v) is reported as skipped by policy", got.Depth)
	}
}

// TestResolveDepth_FlagBeatsConfiguration is R2.7. `--depth` replaces the whole
// computation — class, tier and configuration — because the operator typing it
// is looking at one package and knows something the policy does not.
func TestResolveDepth_FlagBeatsConfiguration(t *testing.T) {
	flag := DepthCompile
	req := requestFor(ClassRevision) // policy would choose options
	req.Package = "app-editors/vscode-bin"
	req.ResolvedType = "bin" // the tier would force none
	req.Policy.Overrides["app-editors/vscode-bin"] = DepthOverride{
		Depth:  DepthNone,
		Reason: "prebuilt",
	}
	req.FlagDepth = &flag

	got := ResolveDepth(req)

	if got.Depth != DepthCompile {
		t.Errorf("depth = %v, want DepthCompile — --depth replaces classification, tier and configuration (R2.7)", got.Depth)
	}
	if got.Reason == "" || !strings.Contains(strings.ToLower(got.Reason), "flag") {
		t.Errorf("reason %q does not name the flag as the deciding input", got.Reason)
	}
	// An explicit operator flag is the ONE input allowed to lower depth, and it
	// is not "skipped by policy" — it is the operator's own instruction.
	if got.SkippedByPolicy {
		t.Error("an explicit --depth is reported as skipped by policy")
	}
}

// TestResolveDepth_FlagMayAlsoLowerTheDepth pins the other direction of R2.7.
// The story's rule is that no input OTHER than an explicit operator flag lowers
// depth; the flag itself may.
func TestResolveDepth_FlagMayAlsoLowerTheDepth(t *testing.T) {
	flag := DepthOptions
	req := requestFor(ClassMajor) // policy would choose compile
	req.FlagDepth = &flag

	got := ResolveDepth(req)

	if got.Depth != DepthOptions {
		t.Errorf("depth = %v, want DepthOptions — the operator asked for the static gate only", got.Depth)
	}
}

// TestResolveDepth_CompileEqualsDepthCompile is R2.8. `--compile` is the shipped
// spelling and must not become a second, subtly different path.
func TestResolveDepth_CompileEqualsDepthCompile(t *testing.T) {
	viaCompile := requestFor(ClassPatch)
	viaCompile.Compile = true

	flag := DepthCompile
	viaFlag := requestFor(ClassPatch)
	viaFlag.FlagDepth = &flag

	gotCompile := ResolveDepth(viaCompile)
	gotFlag := ResolveDepth(viaFlag)

	if gotCompile.Depth != DepthCompile {
		t.Errorf("--compile resolved to %v, want DepthCompile", gotCompile.Depth)
	}
	if gotCompile.Depth != gotFlag.Depth {
		t.Errorf("--compile resolved to %v and --depth=compile to %v; they are the same request (R2.8)", gotCompile.Depth, gotFlag.Depth)
	}
	if gotCompile.SkippedByPolicy != gotFlag.SkippedByPolicy {
		t.Errorf("--compile and --depth=compile disagree about SkippedByPolicy (%v vs %v)", gotCompile.SkippedByPolicy, gotFlag.SkippedByPolicy)
	}
	if gotCompile.Reason == "" {
		t.Error("--compile produced no reason")
	}
}

// TestResolveDepth_EveryDecisionNamesItsDecidingInput sweeps the whole matrix
// for the one property the report depends on. It is written as its own case
// because a reason is easy to add to the path someone is looking at and easy to
// forget on the other four.
func TestResolveDepth_EveryDecisionNamesItsDecidingInput(t *testing.T) {
	flag := DepthPatches

	byPolicy := requestFor(ClassSeries)

	byTier := requestFor(ClassMajor)
	byTier.Package = "app-editors/vscode-bin"
	byTier.ResolvedType = "bin"

	byOverride := requestFor(ClassPatch)
	byOverride.Policy.Overrides["media-plugins/gst-plugins-qt6"] = DepthOverride{Depth: DepthCompile, Reason: "drifts every series"}

	byFlag := requestFor(ClassPatch)
	byFlag.FlagDepth = &flag

	byCompile := requestFor(ClassPatch)
	byCompile.Compile = true

	for name, req := range map[string]DepthRequest{
		"policy":    byPolicy,
		"tier":      byTier,
		"override":  byOverride,
		"flag":      byFlag,
		"--compile": byCompile,
	} {
		got := ResolveDepth(req)
		if strings.TrimSpace(got.Reason) == "" {
			t.Errorf("the %s decision carries no reason; the report can then print a depth but never say why it was chosen", name)
		}
	}
}
