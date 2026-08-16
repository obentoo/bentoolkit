package autoupdate

// Authored for story 033, sub-task 12.1 — R2, R2.2, R2.7, R3, R3.3, R3.12, R3.13.
//
// THE PIPELINE, REACHED FROM THE COMMAND THE OPERATOR ACTUALLY RUNS. Everything
// blocks A and B built is inert until it sits inside `Apply`, between the
// manifest step and success. These are integration assertions in the honest
// sense: they drive the real `Apply` and watch what it spawns, what it writes,
// and in which order.
//
// SEVEN THINGS ARE ASSERTED, AND THEY FAIL FOR DIFFERENT REASONS.
//
//  1. Depth SELECTS the gates. A series crossing earns the configure gate; a
//     patch bump does not.
//  2. A gate error writes NO PIN — the hazard applier.go:693-698 spells out:
//     `--clean` sweeps by pin, so a pin for a bump that is not on disk aims the
//     deletion rule at the only ebuild present.
//  3. StatusValidated moves to AFTER the static gates (protocol §5.7), which
//     changes the pending.json state machine `--list` renders.
//  4. Anything above depth `options` serialises the applies (D14).
//  5. The revive path gets the same pipeline.
//  6. `--depth` reaches the resolver (R2.7). Task 9.2 only RESOLVES a depth;
//     without a surface on this command, `--compile` would be the only way to
//     reach a build gate at all.
//  7. R3.12 and R3.13, the pair that decides what a SKIP means for publishing:
//     a bump whose build gates could not run for want of an installed
//     dependency is promoted with its unreached depth NAMED, and refused
//     instead when the operator has set `require_proof`. This is the honest
//     middle between "publish everything" and "publish nothing on a host that
//     cannot build" — M-D measured that unsatisfied dependencies are common,
//     and turning every one of them into a refusal would make the feature inert
//     for exactly the reason D11 rejects requiring isolation by default.
//
// This file pins `WithApplierValidatePolicy(validate.DepthPolicy)`,
// `WithApplierDepth(validate.Depth)`, `WithApplierRequireProof(bool)`,
// `RequiresSerialApply(validate.Depth) bool` and the result fields
// `ApplyResult.DepthReached` / `.DepthRequested` / `.DepthReason`.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// gateSpy records what Apply spawned and what the pending status was at the
// moment of each spawn, which is how the ORDER of the pipeline is observed
// without reaching inside it.
type gateSpy struct {
	mu       sync.Mutex
	spawns   []string
	statusAt []UpdateStatus
}

func (g *gateSpy) record(name string, args []string, status UpdateStatus) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.spawns = append(g.spawns, name+" "+strings.Join(args, " "))
	g.statusAt = append(g.statusAt, status)
}

func (g *gateSpy) ran(substr string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.spawns {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// gatesPolicy is the policy these cases resolve against: a patch bump stops at
// the static gate, a series crossing earns the configure gate.
func gatesPolicy() validate.DepthPolicy {
	return validate.DepthPolicy{
		ByClass: map[validate.Class]validate.Depth{
			validate.ClassRevision: validate.DepthOptions,
			validate.ClassPatch:    validate.DepthOptions,
			validate.ClassSeries:   validate.DepthConfigure,
			validate.ClassMajor:    validate.DepthCompile,
		},
		Overrides: map[string]validate.DepthOverride{},
	}
}

// gatesSetup describes one fixture. Zero values are the ordinary case: every
// child succeeds and the dependency pre-check answers "satisfied".
type gatesSetup struct {
	from, to   string
	failEbuild bool
	// emergeOutput is what the M-D pretend run prints. Empty means the default
	// clean answer: only the package under test.
	emergeOutput string
	opts         []ApplierOption
}

// gatesFixture lays out an overlay at `from`, a pending bump to `to`, and an
// applier carrying the staging root and the depth policy.
func gatesFixture(t *testing.T, setup gatesSetup) (*Applier, *PendingList, *gateSpy, string, string) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	const pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFile(t, overlayDir, pkg, setup.from)

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: setup.from,
		NewVersion:     setup.to,
		Status:         StatusPending,
	})

	emerge := setup.emergeOutput
	if emerge == "" {
		emerge = "[ebuild  N    ] media-plugins/gst-plugins-qt6-" + setup.to + "\n"
	}

	spy := &gateSpy{}
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		var status UpdateStatus
		if u, ok := pending.Get(pkg); ok {
			status = u.Status
		}
		spy.record(name, arg, status)

		if name == "emerge" {
			return exec.CommandContext(ctx, "printf", "%s", emerge)
		}
		if setup.failEbuild && (name == "ebuild" || containsArg(arg, "configure") || containsArg(arg, "compile")) {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "true")
	}

	base := []ApplierOption{
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierValidatePolicy(gatesPolicy()),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	}
	applier, err := NewApplier(overlayDir, configDir, append(base, setup.opts...)...)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return applier, pending, spy, overlayDir, pkg
}

// seriesBump and patchBump are the two fixtures nearly every case starts from.
func seriesBump(opts ...ApplierOption) gatesSetup {
	return gatesSetup{from: "1.28.6", to: "1.29.2", opts: opts}
}

func patchBump(opts ...ApplierOption) gatesSetup {
	return gatesSetup{from: "1.28.5", to: "1.28.6", opts: opts}
}

// =============================================================================
// Story 035, sub-task 2.2 — the gate reads the directory the run filled
// =============================================================================

// TestStaticGateDistdir_PrefersWhatTheRunFetched pins the precedence R1.1 and
// R1.2 turn on, and the empty-directory case that is easy to get wrong.
//
// The private distdir is created BEFORE the manifest step runs, so it exists
// even on paths that fetched nothing. Preferring it while empty would hand the
// gate the same empty room the story exists to stop handing it, with the shared
// distdir — which does hold an archive — sitting right there unread.
func TestStaticGateDistdir_PrefersWhatTheRunFetched(t *testing.T) {
	shared := t.TempDir()
	if err := os.WriteFile(filepath.Join(shared, "shared.tar.gz"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seeding the shared distdir: %v", err)
	}

	fetchedWithArchive := t.TempDir()
	if err := os.WriteFile(filepath.Join(fetchedWithArchive, "fetched.tar.gz"), []byte("y"), 0o644); err != nil {
		t.Fatalf("seeding the fetched distdir: %v", err)
	}
	fetchedEmpty := t.TempDir()

	a := &Applier{distdir: shared}

	for _, tc := range []struct {
		name string
		cand candidatePaths
		want string
		why  string
	}{
		{
			name: "the run fetched an archive",
			cand: candidatePaths{fetchedDistdir: fetchedWithArchive},
			want: fetchedWithArchive,
			why: "on a host that has never fetched this release, what the manifest step downloaded is the ONLY " +
				"copy of the candidate's archive; reading the shared distdir instead is the defect",
		},
		{
			name: "the manifest step fetched nothing",
			cand: candidatePaths{fetchedDistdir: fetchedEmpty},
			want: shared,
			why: "an empty private directory means nothing was fetched, and an empty room is what the gate must " +
				"NOT be handed while the shared distdir holds the archive",
		},
		{
			name: "no manifest step ran at all",
			cand: candidatePaths{},
			want: shared,
			why:  "the unstaged path never had a private directory: it wrote into the shared one all along",
		},
		{
			name: "the private directory is gone",
			cand: candidatePaths{fetchedDistdir: filepath.Join(t.TempDir(), "removed")},
			want: shared,
			why:  "\"I could not look\" and \"there was nothing to see\" lead to the same choice: read the other one",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.staticGateDistdir(tc.cand); got != tc.want {
				t.Errorf("staticGateDistdir = %q, want %q\n%s", got, tc.want, tc.why)
			}
		})
	}
}

// TestApplyGates_NoFetchedDistfilesSurviveAnApply is story 033 sub-task 3.1's
// guarantee, re-authored at the level that now discharges it (story 035, R2.1,
// R2.2, sub-task 4.2).
//
// # A scope correction, not a weakening
//
// 3.1 asserted `TestRunStagedManifest_PrivateDistdirIsRemovedEvenOnFailure` — a
// `defer os.RemoveAll` inside the manifest step. That defer deleted the run's own
// fetched archive before the run's own static gates could read it, which is the
// defect this story exists to remove, so the removal moved one level up. The
// GUARANTEE is unchanged and still mandatory: a sweep over forty packages must
// not leak one 6 MB archive per package into the scratch filesystem the private
// directory was created to protect. Only the level that discharges it moved,
// and this is where it is now asserted.
//
// Both paths, because they fail differently. The failing one is where a cleanup
// is forgotten; the successful one is where a `defer` placed after an early
// `return` would never be reached.
func TestApplyGates_NoFetchedDistfilesSurviveAnApply(t *testing.T) {
	for _, tc := range []struct {
		name        string
		from, to    string
		failBuild   bool
		wantSuccess bool
	}{
		// A series crossing earns the configure gate, and every build child
		// fails, so the apply fails after the fetch.
		{name: "a failed apply", from: "1.28.6", to: "1.29.2", failBuild: true, wantSuccess: false},
		// A patch bump stops at the static gate and every child succeeds, so the
		// bump is promoted — and the removal still has to fire.
		{name: "a successful apply", from: "1.28.5", to: "1.28.6", failBuild: false, wantSuccess: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandbox := t.TempDir()
			prev := fixSandboxRoot
			fixSandboxRoot = func() string { return sandbox }
			t.Cleanup(func() { fixSandboxRoot = prev })

			tmp := t.TempDir()
			overlayDir := filepath.Join(tmp, "overlay")
			configDir := filepath.Join(tmp, "config")
			const pkg = "media-plugins/gst-plugins-qt6"

			createTestEbuildFile(t, overlayDir, pkg, tc.from)

			pending, err := NewPendingList(configDir)
			if err != nil {
				t.Fatalf("creating pending list: %v", err)
			}
			pending.Add(PendingUpdate{Package: pkg, CurrentVersion: tc.from, NewVersion: tc.to, Status: StatusPending})

			// pkgdev puts a file in whatever --distdir it is handed. An empty
			// directory would be taken by an implementation that only ever calls
			// os.Remove, so the case would not distinguish the two.
			//
			// fetchedInto is captured so the assertions below can prove the
			// directory EXISTED. Without it, an apply that died before the
			// manifest step passes for the trivial reason that it fetched
			// nothing — the vacuous green it would be easiest to ship.
			var fetchedInto []string
			seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
				if name == "pkgdev" {
					for i, s := range arg {
						if s == "--distdir" && i+1 < len(arg) {
							fetchedInto = append(fetchedInto, arg[i+1])
							return exec.CommandContext(ctx, "sh", "-c",
								"set -e; : > \""+arg[i+1]+"/gst-plugins-good-"+tc.to+".tar.gz\"")
						}
					}
					return exec.CommandContext(ctx, "true")
				}
				if name == "emerge" {
					return exec.CommandContext(ctx, "printf", "%s",
						"[ebuild  N    ] media-plugins/gst-plugins-qt6-"+tc.to+"\n")
				}
				if tc.failBuild && name == "ebuild" {
					return exec.CommandContext(ctx, "false")
				}
				return exec.CommandContext(ctx, "true")
			}

			applier, err := NewApplier(overlayDir, configDir,
				WithApplierPendingList(pending),
				WithExecCommand(seam),
				WithConfirmFunc(func(string) bool { return true }),
				WithApplierStagingRoot(filepath.Join(tmp, "staging")),
				WithApplierValidatePolicy(gatesPolicy()),
				WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
			)
			if err != nil {
				t.Fatalf("creating applier: %v", err)
			}

			result, _ := applier.Apply(pkg, false)
			if result.Success != tc.wantSuccess {
				t.Fatalf("the apply returned success=%v, want %v (err=%v) — this case is about the removal on "+
					"THAT path, so it must actually take it", result.Success, tc.wantSuccess, result.Error)
			}

			if len(fetchedInto) == 0 {
				t.Fatal("pkgdev was never given a --distdir, so the apply never reached the manifest step and " +
					"this case proved nothing about the removal")
			}
			for _, dir := range fetchedInto {
				if _, statErr := os.Stat(dir); statErr == nil {
					t.Errorf("the fetched distdir %q still exists after the apply", dir)
				}
			}

			entries, err := os.ReadDir(sandbox)
			if err != nil {
				t.Fatalf("reading the sandbox root: %v", err)
			}
			var leaked []string
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "bentoo-staged-distfiles-") {
					leaked = append(leaked, e.Name())
				}
			}
			if len(leaked) != 0 {
				t.Errorf("the apply left %v under the sandbox root — the removal fires on every path, and a "+
					"sweep over forty packages that leaks one per bump fills the filesystem it protects", leaked)
			}
		})
	}
}

// TestApplyGates_SeriesCrossingRunsTheConfigureGate is R2.2 reaching all the way
// into the command. 1.28.6 → 1.29.2 is the bump this whole story exists for.
func TestApplyGates_SeriesCrossingRunsTheConfigureGate(t *testing.T) {
	applier, _, spy, _, pkg := gatesFixture(t, seriesBump())

	if _, err := applier.Apply(pkg, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !spy.ran("configure") {
		t.Errorf("a series crossing ran no configure gate; spawns: %v", spy.spawns)
	}
}

// TestApplyGates_PatchBumpRunsNoBuild is the other side, and the one that keeps
// the feature affordable: a patch bump pays for the static gate and nothing
// else. Without it, "validate everything deeply" quietly becomes "validation is
// too slow to leave on", which the protocol names as the real failure mode.
func TestApplyGates_PatchBumpRunsNoBuild(t *testing.T) {
	applier, _, spy, _, pkg := gatesFixture(t, patchBump())

	if _, err := applier.Apply(pkg, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if spy.ran("configure") || spy.ran("compile") {
		t.Errorf("a patch bump started a build; spawns: %v", spy.spawns)
	}
}

// TestApplyGates_DepthFlagReachesTheResolver is R2.7, and it is the requirement
// that had no surface at all before the review: 9.2 resolves a depth from a
// value nobody could supply. A patch bump would stop at `options`; the operator
// asked for compile, and the compile gate has to run.
func TestApplyGates_DepthFlagReachesTheResolver(t *testing.T) {
	applier, _, spy, _, pkg := gatesFixture(t, patchBump(WithApplierDepth(validate.DepthCompile)))

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !spy.ran("compile") {
		t.Errorf("--depth=compile on a patch bump ran no compile gate; policy would have chosen options, and the flag "+
			"replaces classification, tier and configuration (R2.7). spawns: %v", spy.spawns)
	}
	if result.DepthRequested != "compile" {
		t.Errorf("DepthRequested = %q, want %q — the report says what was asked for, not only what happened", result.DepthRequested, "compile")
	}
	if result.DepthReason == "" || !strings.Contains(strings.ToLower(result.DepthReason), "flag") {
		t.Errorf("DepthReason = %q; it must name the flag as the deciding input", result.DepthReason)
	}
}

// TestApplyGates_DepthFlagAlsoLowersTheDepth pins the other direction. The flag
// is the one input allowed to ask for less, and an operator investigating one
// package must be able to skip the hours.
func TestApplyGates_DepthFlagAlsoLowersTheDepth(t *testing.T) {
	applier, _, spy, _, pkg := gatesFixture(t, seriesBump(WithApplierDepth(validate.DepthOptions)))

	if _, err := applier.Apply(pkg, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if spy.ran("configure") {
		t.Errorf("--depth=options on a series crossing still ran the configure gate; spawns: %v", spy.spawns)
	}
}

// TestApplyGates_GateErrorLeavesTheOverlayUnchangedAndWritesNoPin is R3.3 and
// the pin hazard together. The two are asserted in one case because they are one
// failure in practice: the bump did not pass, so nothing about it may be
// published — not the ebuild, not the claim that the ebuild exists.
func TestApplyGates_GateErrorLeavesTheOverlayUnchangedAndWritesNoPin(t *testing.T) {
	pins := map[string]string{}
	setup := seriesBump(WithApplierSetVersionsFunc(func(_ string, written map[string]string) error {
		for k, v := range written {
			pins[k] = v
		}
		return nil
	}))
	setup.failEbuild = true

	applier, _, _, overlayDir, pkg := gatesFixture(t, setup)
	before := hashOverlayTree(t, overlayDir)

	result, _ := applier.Apply(pkg, false)

	if result.Success {
		t.Fatal("the configure gate failed and the apply reported success")
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("a failed gate changed the published overlay: %s -> %s", before, after)
	}
	if v, ok := pins[pkg]; ok {
		t.Errorf("a bump that failed its gate wrote the pin %s = %q; --clean sweeps by pin, so this aims the deletion "+
			"rule at the only ebuild present (applier.go:693-698)", pkg, v)
	}
}

// TestApplyGates_UnsatisfiedDependenciesPromoteWithTheUnreachedDepthNamed is
// R3.12. The build gates could not run — not because the bump is wrong, but
// because this host does not hold its dependencies (design M-D, the
// false-positive class the source protocol misses). The bump is published, and
// the report SAYS how far validation actually got, so nobody reads the green as
// "it builds".
func TestApplyGates_UnsatisfiedDependenciesPromoteWithTheUnreachedDepthNamed(t *testing.T) {
	setup := seriesBump()
	setup.emergeOutput = "[ebuild  N    ] media-libs/gst-plugins-base-1.29.2\n" +
		"[ebuild  N    ] dev-qt/qtbase-6.9.1\n" +
		"[ebuild  N    ] media-plugins/gst-plugins-qt6-1.29.2\n"

	applier, _, spy, overlayDir, pkg := gatesFixture(t, setup)

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v — unsatisfied dependencies are a reported outcome, not a failed run", err)
	}

	if !result.Success {
		t.Fatalf("a bump was refused because THIS HOST lacks its build dependencies: %v — that says nothing about the bump, "+
			"and refusing every one of them makes the feature inert on an ordinary workstation (R3.12)", result.Error)
	}
	if spy.ran("configure") {
		t.Errorf("the configure gate ran although the dependency pre-check reported unsatisfied atoms; spawns: %v", spy.spawns)
	}

	published := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	if _, statErr := os.Stat(published); statErr != nil {
		t.Errorf("the bump was reported successful but is not in the overlay: %v", statErr)
	}

	// The honesty half: the outcome names its own reach.
	if result.DepthRequested != "configure" {
		t.Errorf("DepthRequested = %q, want configure — the depth policy selected is what was ASKED for", result.DepthRequested)
	}
	if result.DepthReached == "configure" {
		t.Error("DepthReached claims configure although no build ran; a pass that overstates its own reach is the defect this story removes")
	}
	if result.DepthReason == "" {
		t.Fatal("no reason is given for stopping short of the requested depth")
	}
	for _, want := range []string{"media-libs/gst-plugins-base", "dev-qt/qtbase"} {
		if !strings.Contains(result.DepthReason, want) {
			t.Errorf("the reason %q does not name the unsatisfied atom %q; the operator cannot act on it", result.DepthReason, want)
		}
	}
}

// TestApplyGates_RequireProofRefusesTheSameBump is R3.13, the opt-in for hosts
// where an unproved publish is not acceptable — a builder box, or a maintainer
// who would rather the sweep stopped than shipped something unbuilt.
//
// Same fixture, one option different, opposite outcome: that is what makes the
// two requirements a pair rather than a contradiction.
func TestApplyGates_RequireProofRefusesTheSameBump(t *testing.T) {
	pins := map[string]string{}
	setup := seriesBump(
		WithApplierRequireProof(true),
		WithApplierSetVersionsFunc(func(_ string, written map[string]string) error {
			for k, v := range written {
				pins[k] = v
			}
			return nil
		}),
	)
	setup.emergeOutput = "[ebuild  N    ] dev-qt/qtbase-6.9.1\n" +
		"[ebuild  N    ] media-plugins/gst-plugins-qt6-1.29.2\n"

	applier, _, _, overlayDir, pkg := gatesFixture(t, setup)
	before := hashOverlayTree(t, overlayDir)

	result, _ := applier.Apply(pkg, false)

	if result.Success {
		t.Fatal("require_proof was set and a bump whose build gates never ran was published anyway (R3.13)")
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("a refused bump changed the published overlay: %s -> %s", before, after)
	}
	if len(pins) != 0 {
		t.Errorf("a refused bump wrote a pin: %v", pins)
	}
	msg := ""
	if result.Error != nil {
		msg = result.Error.Error()
	}
	if !strings.Contains(msg, "dev-qt/qtbase") {
		t.Errorf("the refusal %q does not name what stopped the gate; without it the operator cannot make the bump publishable", msg)
	}
}

// TestApplyGates_StatusValidatedIsWrittenAfterTheStaticGates is protocol §5.7,
// observed through the pending status sampled at each spawn. A bump whose
// options no longer exist is not validated, whatever the manifest says.
func TestApplyGates_StatusValidatedIsWrittenAfterTheStaticGates(t *testing.T) {
	applier, _, spy, _, pkg := gatesFixture(t, seriesBump())

	if _, err := applier.Apply(pkg, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.spawns) == 0 {
		t.Fatal("no child was spawned; the pipeline never ran")
	}

	for i, cmd := range spy.spawns {
		isManifest := strings.Contains(cmd, "pkgdev")
		isBuild := strings.Contains(cmd, "configure") || strings.Contains(cmd, "compile")

		if isManifest && spy.statusAt[i] == StatusValidated {
			t.Errorf("the pending entry was already StatusValidated when the manifest step ran (%q); "+
				"a manifest that succeeded is not a validated bump (protocol §5.7)", cmd)
		}
		if isBuild && spy.statusAt[i] != StatusValidated {
			t.Errorf("the pending entry was %q rather than StatusValidated when the build gate ran (%q); "+
				"the static gates come first and the status follows them", spy.statusAt[i], cmd)
		}
	}
}

// TestApplyGates_FailedStaticGateNeverReachesValidated is the same rule seen
// from the failure side, and it is what `--list` renders: a bump the option gate
// rejected must not sit in pending.json claiming to be validated.
func TestApplyGates_FailedStaticGateNeverReachesValidated(t *testing.T) {
	setup := seriesBump()
	setup.failEbuild = true

	applier, pending, _, _, pkg := gatesFixture(t, setup)

	if _, err := applier.Apply(pkg, false); err == nil {
		t.Fatal("a failing gate produced no error")
	}

	update, found := pending.Get(pkg)
	if !found {
		t.Fatal("the pending entry vanished after a failed apply; a real, still-pending update must survive its failure")
	}
	if update.Status == StatusValidated {
		t.Error("a bump whose gate failed is recorded as validated; --list would then offer it as ready to publish")
	}
}

// TestApplyGates_ReviveUsesTheSamePipeline is the assertion that keeps a second
// entry point from growing a second, gate-free path. A revived entry arrives
// carrying StatusValidated from an earlier run, and that must not be read as
// "the gates already passed" — the inputs may have moved since.
func TestApplyGates_ReviveUsesTheSamePipeline(t *testing.T) {
	applier, pending, spy, _, pkg := gatesFixture(t, seriesBump())
	if err := pending.SetStatus(pkg, StatusValidated, ""); err != nil {
		t.Fatalf("seeding the revived status: %v", err)
	}

	if _, err := applier.Apply(pkg, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !spy.ran("configure") {
		t.Errorf("an entry already marked validated skipped the configure gate; a status from an earlier run is not evidence "+
			"about this one. spawns: %v", spy.spawns)
	}
}

// TestRequiresSerialApply_AnyBuildDepthSerialises is D14. Concurrent builds
// contend for CPU and for space under PORTAGE_TMPDIR — measured at 60 MB for one
// gst configure — and the existing rule already serialises `--compile`.
//
// The predicate is asserted here; that cmd/bentoo's applyAllPackages consults it
// is asserted where that file is touched (overlay_autoupdate.go:1376-1379).
func TestRequiresSerialApply_AnyBuildDepthSerialises(t *testing.T) {
	tests := []struct {
		depth validate.Depth
		want  bool
		why   string
	}{
		{validate.DepthNone, false, "nothing runs, so the worker pool is free"},
		{validate.DepthOptions, false, "static reads only; this is today's concurrent path and it must stay concurrent"},
		{validate.DepthPatches, true, "unpacks a tarball and runs src_prepare"},
		{validate.DepthConfigure, true, "60 MB under PORTAGE_TMPDIR for one gst configure, measured"},
		{validate.DepthCompile, true, "the rule --compile already follows"},
	}

	for _, tt := range tests {
		if got := RequiresSerialApply(tt.depth); got != tt.want {
			t.Errorf("RequiresSerialApply(%v) = %v, want %v — %s", tt.depth, got, tt.want, tt.why)
		}
	}
}

// MERGE FRAGMENT — story 037, sub-task 3.1 (per-bump seam values in
// runStaticGates; the lend retires).
//
// Target file: internal/autoupdate/applier_gates_test.go (APPEND at the end).
// Do NOT repeat the `package autoupdate` clause.
//
// IMPORTS: none added — context, os, os/exec, path/filepath, strings and
// testing, plus the validate import, are already in the target's block.
//
// # Symbols
//
// Added: seamFindStagedPkgDir, seamGoldenBumpFixture, and the two tests.
// Borrowed, never re-declared: createTestEbuildFileWithContent
// (applier_test.go), goldenApplyEbuild and writeGoldenArchive
// (applier_golden_test.go), hashOverlayTree (applier_promote_test.go).
//
// # PINNED CONTRACT (design D4, D7 — S037-R3, R3.1, R3.2)
//
// runStaticGates supplies the option gate's names per bump, from `cand`:
// staged Manifest present (the manifest child wrote one) → the gate reads it
// directly; absent → the PUBLISHED names travel through the seam, and NO file
// is written into the staged tree while the gate answers. The write-free half
// is asserted the only way it can be from outside: the staged package
// directory is sealed read-only before the gates run, and the gate must still
// reach a verdict — under the lend, the same fixture dies on the temporary
// Manifest's own WriteFile and reports SKIPPED, which R3.3 then publishes.

// seamFindStagedPkgDir walks a staging root for the directory holding the
// named candidate ebuild, or "" when nothing is staged yet.
func seamFindStagedPkgDir(stagingRoot, ebuildName string) string {
	var found string
	_ = filepath.Walk(stagingRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && info.Name() == ebuildName {
			found = filepath.Dir(path)
		}
		return nil
	})
	return found
}

// seamGoldenBumpFixture lays out the golden bump — 1.28.6 published, 1.29.2
// pending, the published Manifest naming both archives, the shared distdir
// holding both (1.29.2's declares only qt6, so its option gate has a FAILED to
// reach) — and an applier at depth options whose pkgdev seam runs `hook`
// against the staged package directory the moment the manifest step is
// spawned. The hook fires between staging and the static gates, which is the
// only window the write-free assertion can be armed in.
func seamGoldenBumpFixture(t *testing.T, hook func(stagedPkgDir, sharedDistdir string)) (*Applier, string, string) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	distdir := filepath.Join(tmp, "distdir")
	staging := filepath.Join(tmp, "staging")
	const pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFileWithContent(t, overlayDir, pkg, "1.28.6", goldenApplyEbuild)
	pkgDir := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6")
	manifest := "DIST gst-plugins-good-1.28.6.tar.gz 100 BLAKE2B ab SHA512 cd\n" +
		"DIST gst-plugins-good-1.29.2.tar.gz 100 BLAKE2B ef SHA512 01\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing the published Manifest: %v", err)
	}
	if err := os.MkdirAll(distdir, 0o755); err != nil {
		t.Fatalf("creating distdir: %v", err)
	}
	writeGoldenArchive(t, distdir, "gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.28.6",
		[]string{"qt6", "aalib", "libcaca"})
	writeGoldenArchive(t, distdir, "gst-plugins-good-1.29.2.tar.gz", "gst-plugins-good-1.29.2",
		[]string{"qt6"})

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.28.6", NewVersion: "1.29.2", Status: StatusPending})

	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" && hook != nil {
			if dir := seamFindStagedPkgDir(staging, "gst-plugins-qt6-1.29.2.ebuild"); dir != "" {
				hook(dir, distdir)
			}
		}
		return exec.CommandContext(ctx, "true")
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(staging),
		WithApplierDistdir(distdir, ""),
		WithApplierDepth(validate.DepthOptions),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return applier, overlayDir, pkg
}

// TestStaticGates_AnswerWriteFreeFromAReadOnlyStagedTree is R3.2's whole
// claim in one fixture: the staged tree carries no Manifest, its package
// directory cannot be written to at all, and the option gate must still
// answer — FAILED, because upstream 1.29.2 declares neither aalib nor libcaca
// and the ebuild passes both. A gate that needs to write a temporary file
// into the tree (the lend) dies here with SKIPPED, and R3.3 publishes the
// bump on the strength of a gate that read nothing.
func TestStaticGates_AnswerWriteFreeFromAReadOnlyStagedTree(t *testing.T) {
	var sealed []string
	applier, overlayDir, pkg := seamGoldenBumpFixture(t, func(stagedPkgDir, _ string) {
		if err := os.Chmod(stagedPkgDir, 0o555); err != nil {
			t.Errorf("sealing the staged package directory: %v", err)
			return
		}
		sealed = append(sealed, stagedPkgDir)
	})
	// Registered AFTER the fixture's own t.TempDir, so it runs BEFORE the
	// directory's RemoveAll (cleanups are LIFO): the seal must be lifted or the
	// retained staged tree cannot be deleted.
	t.Cleanup(func() {
		for _, dir := range sealed {
			_ = os.Chmod(dir, 0o755)
		}
	})

	before := hashOverlayTree(t, overlayDir)
	result, _ := applier.Apply(pkg, false)

	if len(sealed) == 0 {
		t.Fatal("the staged package directory was never found and sealed, so this case proved nothing " +
			"about a write-free gate")
	}
	if result.Success {
		t.Fatal("the apply PUBLISHED the golden bump although its option gate had a FAILED to report; the " +
			"gate could not answer from a read-only staged tree, which means it needed to write into the tree " +
			"to read names — the exact coupling R3.2 removes")
	}
	msg := ""
	if result.Error != nil {
		msg = result.Error.Error()
	}
	for _, want := range []string{"aalib", "libcaca"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal %q does not name %q; the gate answered, so its findings must reach the "+
				"operator", msg, want)
		}
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("the refused bump changed the published overlay: %s -> %s", before, after)
	}
	for _, dir := range sealed {
		if _, err := os.Stat(filepath.Join(dir, "Manifest")); err == nil {
			t.Errorf("a Manifest exists in the staged package directory %s after the gates; nothing may be "+
				"written into the staged tree while the static gates answer (R3.2)", dir)
		}
	}
}

// TestStaticGates_AStagedManifestFromTheManifestStepIsReadDirectly is R3.1,
// the other half of the per-bump supply rule: when the manifest child DID
// write a staged Manifest, that file is the authority — the generated names,
// never the published ones, because on a bump the published Manifest answers
// about a different release (the defect R12 fixed once already).
//
// EXPECT THIS GREEN ON ARRIVAL: the lend already no-ops when a staged
// Manifest exists, so this is a REGRESSION PIN carried across the lend's
// deletion (design D4's "rewrite, not delete"). In Run mode, prove it by
// mutation: make runStaticGates feed the published names unconditionally and
// confirm this fails — the staged Manifest names an archive the published one
// has never heard of.
func TestStaticGates_AStagedManifestFromTheManifestStepIsReadDirectly(t *testing.T) {
	// The generated archive, named like nothing the published Manifest knows,
	// declaring only qt6 — so a FAILED naming aalib/libcaca is only reachable
	// by reading the STAGED Manifest and the archive it names.
	generated := t.TempDir()
	writeGoldenArchive(t, generated, "seam-proof-1.29.2.tar.gz", "seam-proof-1.29.2", []string{"qt6"})

	applier, overlayDir, pkg := seamGoldenBumpFixture(t, func(stagedPkgDir, sharedDistdir string) {
		if err := os.WriteFile(filepath.Join(stagedPkgDir, "Manifest"),
			[]byte("DIST seam-proof-1.29.2.tar.gz 100 BLAKE2B ab SHA512 cd\n"), 0o600); err != nil {
			t.Errorf("writing the staged Manifest the manifest child would have written: %v", err)
			return
		}
		body, err := os.ReadFile(filepath.Join(generated, "seam-proof-1.29.2.tar.gz"))
		if err != nil {
			t.Errorf("reading the generated archive: %v", err)
			return
		}
		// Into the SHARED distdir: this run's private fetched directory holds
		// nothing (the pkgdev stub downloads nothing), so 035's precedence —
		// untouched by this story — sends the gate to the shared one, where the
		// staged Manifest's own archive is waiting.
		if err := os.WriteFile(filepath.Join(sharedDistdir, "seam-proof-1.29.2.tar.gz"), body, 0o644); err != nil {
			t.Errorf("placing the generated archive in the shared distdir: %v", err)
		}
	})

	before := hashOverlayTree(t, overlayDir)
	result, _ := applier.Apply(pkg, false)

	if result.Success {
		t.Fatal("the apply published the bump although the staged Manifest's own archive declares neither " +
			"aalib nor libcaca; the gate did not read the Manifest the manifest step wrote (R3.1)")
	}
	msg := ""
	if result.Error != nil {
		msg = result.Error.Error()
	}
	if !strings.Contains(msg, "aalib") {
		t.Errorf("the refusal %q does not name aalib; the verdict must come from the generated archive the "+
			"staged Manifest names", msg)
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("the refused bump changed the published overlay: %s -> %s — the published tree never "+
			"carries a bentoo-written Manifest, and never changes on a refusal", before, after)
	}
}

// TestStaticGates_TheStagedManifestDecidesWhichArchiveIsRead is the
// DISCRIMINATING half of R3.1, added during Run mode because the pre-authored
// pin above cannot carry the proof its own doc-comment claims.
//
// # Why the pin above is not enough (measured, 2026-08-16)
//
// TestStaticGates_AStagedManifestFromTheManifestStepIsReadDirectly says a
// FAILED naming aalib is "only reachable by reading the STAGED Manifest". It is
// not: seamGoldenBumpFixture's published 1.29.2 archive ALSO declares only
// qt6, so both sources yield the same verdict. Feeding the published names
// unconditionally — the mutation the story's red-evidence prescribes — leaves
// that test green, and a probe confirmed the refusal names neither archive.
// The pin is real but non-discriminating; it pins the outcome, not the source.
//
// # What this one changes
//
// The PUBLISHED 1.29.2 archive declares everything the ebuild passes, so
// reading it reaches a PASS and the bump is published. The STAGED Manifest
// names an archive declaring only qt6, so reading it reaches a FAILED. The two
// sources now disagree, and the verdict says which one was read.
func TestStaticGates_TheStagedManifestDecidesWhichArchiveIsRead(t *testing.T) {
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	distdir := filepath.Join(tmp, "distdir")
	staging := filepath.Join(tmp, "staging")
	const pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFileWithContent(t, overlayDir, pkg, "1.28.6", goldenApplyEbuild)
	pkgDir := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6")
	published := "DIST gst-plugins-good-1.28.6.tar.gz 100 BLAKE2B ab SHA512 cd\n" +
		"DIST gst-plugins-good-1.29.2.tar.gz 100 BLAKE2B ef SHA512 01\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(published), 0o644); err != nil {
		t.Fatalf("writing the published Manifest: %v", err)
	}
	if err := os.MkdirAll(distdir, 0o755); err != nil {
		t.Fatalf("creating distdir: %v", err)
	}
	writeGoldenArchive(t, distdir, "gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.28.6",
		[]string{"qt6", "aalib", "libcaca"})
	// THE DISCRIMINATOR: the published candidate archive declares everything the
	// ebuild passes, so a gate reading the PUBLISHED names answers PASS.
	writeGoldenArchive(t, distdir, "gst-plugins-good-1.29.2.tar.gz", "gst-plugins-good-1.29.2",
		[]string{"qt6", "aalib", "libcaca"})
	// ...while the archive the STAGED Manifest names declares only qt6.
	writeGoldenArchive(t, distdir, "seam-proof-1.29.2.tar.gz", "seam-proof-1.29.2", []string{"qt6"})

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.28.6", NewVersion: "1.29.2", Status: StatusPending})

	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			if dir := seamFindStagedPkgDir(staging, "gst-plugins-qt6-1.29.2.ebuild"); dir != "" {
				_ = os.WriteFile(filepath.Join(dir, "Manifest"),
					[]byte("DIST seam-proof-1.29.2.tar.gz 100 BLAKE2B ab SHA512 cd\n"), 0o600)
			}
		}
		return exec.CommandContext(ctx, "true")
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(staging),
		WithApplierDistdir(distdir, ""),
		WithApplierDepth(validate.DepthOptions),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}

	result, _ := applier.Apply(pkg, false)
	if result.Success {
		t.Fatal("the apply PUBLISHED the bump: the gate answered from the PUBLISHED Manifest's archive, which " +
			"declares everything the ebuild passes. On a bump the published Manifest describes a different " +
			"release, and the staged Manifest the manifest step wrote is the authority (R3.1, design D4)")
	}
	msg := ""
	if result.Error != nil {
		msg = result.Error.Error()
	}
	if !strings.Contains(msg, "aalib") {
		t.Errorf("the refusal %q does not name aalib; the verdict must come from the archive the STAGED "+
			"Manifest names, which declares only qt6", msg)
	}
}
