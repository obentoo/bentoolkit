package autoupdate

// Authored for story 060, sub-task 1.4 — R4.1, R4.2, R4.3, R4.4, R4.5, R4.6, R4.7, R4.8.
//
// Written from the contract (design C4):
//
//	var ErrNoLocalPackageDir error
//	type ReviveStatus string // "revived", "skipped", "failed"
//	type ReviveOutcome struct{ Package string; Status ReviveStatus; Detail string }
//	type ReviveApplier interface {
//	    SeedFromGentoo(pkg, srcDir, version string) error
//	    Apply(ctx context.Context, pkg string, compile bool) (*ApplyResult, error)
//	}
//	func CanRevive(prov provider.Provider) error
//	func WithReviveCompile(compile bool) ReviverOption
//	func NewReviver(overlayPath string, applier ReviveApplier, prov provider.Provider,
//	    newChecker func() (*Checker, error), opts ...ReviverOption) (*Reviver, error)
//	func (r *Reviver) Revive(ctx context.Context, pkg string) ReviveOutcome
//
// The Detail strings are the ones `bentoo overlay autoupdate --revive` printed
// before this story (cmd/bentoo reviveOne at 1462803), so they are asserted
// byte for byte (U3, R4.2). The pipeline runs over a real Applier, a real
// Checker and a local httptest upstream, with a stub ::gentoo provider (R4.6).
//
// Red on arrival: none of the symbols above exist.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// s060Gentoo is a ::gentoo provider with an on-disk package directory.
type s060Gentoo struct {
	root        string
	versions    []string
	versionsErr error
	lookupErr   error
	events      *s060Events
}

func (g *s060Gentoo) GetPackageVersions(_ context.Context, category, pkg string) ([]string, error) {
	g.events.add("versions " + category + "/" + pkg)
	return g.versions, g.versionsErr
}
func (g *s060Gentoo) GetName() string   { return "gentoo" }
func (g *s060Gentoo) SupportsAPI() bool { return false }
func (g *s060Gentoo) Close() error      { return nil }
func (g *s060Gentoo) LocalPackagePath(category, pkg string) (string, error) {
	g.events.add("lookup " + category + "/" + pkg)
	if g.lookupErr != nil {
		return "", g.lookupErr
	}
	return filepath.Join(g.root, category, pkg), nil
}

var _ provider.PackageDirProvider = (*s060Gentoo)(nil)

// The real Applier is what runRevive hands the Reviver.
var _ ReviveApplier = (*Applier)(nil)

// s060APIOnlyGentoo has no local package directory.
type s060APIOnlyGentoo struct{}

func (s060APIOnlyGentoo) GetPackageVersions(context.Context, string, string) ([]string, error) {
	return []string{"1.0.0"}, nil
}
func (s060APIOnlyGentoo) GetName() string   { return "gentoo-api" }
func (s060APIOnlyGentoo) SupportsAPI() bool { return true }
func (s060APIOnlyGentoo) Close() error      { return nil }

type s060Events struct {
	mu  sync.Mutex
	log []string
}

func (e *s060Events) add(s string) {
	e.mu.Lock()
	e.log = append(e.log, s)
	e.mu.Unlock()
}

func (e *s060Events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

const s060RevivePkg = "app-misc/orphan"

// s060ReviveRig is one overlay holding a disabled orphan entry and no ebuild,
// a ::gentoo tree holding its ebuilds, and an upstream answering upstream.
type s060ReviveRig struct {
	overlayDir string
	stateDir   string
	gentoo     *s060Gentoo
	events     *s060Events
	pending    *PendingList
	applier    *Applier
	prompts    []string
	upstream   string
	newChecker func() (*Checker, error)
}

func newS060ReviveRig(t *testing.T, gentooVersions []string, upstream string) *s060ReviveRig {
	t.Helper()
	tmp := t.TempDir()
	rig := &s060ReviveRig{
		overlayDir: filepath.Join(tmp, "overlay"),
		stateDir:   filepath.Join(tmp, "state"),
		events:     &s060Events{},
		upstream:   upstream,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": rig.upstream})
	}))
	t.Cleanup(server.Close)

	gentooRoot := filepath.Join(tmp, "gentoo")
	for _, v := range gentooVersions {
		if v == "junk" {
			continue
		}
		createTestEbuildFile(t, gentooRoot, s060RevivePkg, v)
	}
	rig.gentoo = &s060Gentoo{root: gentooRoot, versions: gentooVersions, events: rig.events}

	cfgDir := filepath.Join(rig.overlayDir, ".autoupdate")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry := "[\"" + s060RevivePkg + "\"]\n" +
		"url = \"" + server.URL + "\"\n" +
		"parser = \"json\"\n" +
		"path = \"version\"\n" +
		"enabled = false\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "packages.toml"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}

	pending, err := NewPendingList(rig.stateDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	rig.pending = pending
	applier, err := NewApplier(rig.overlayDir, rig.stateDir,
		WithApplierPendingList(pending),
		WithExecCommand(func(ctx context.Context, name string, _ ...string) *exec.Cmd {
			rig.events.add("spawn " + name)
			return exec.CommandContext(ctx, "true")
		}),
		WithConfirmFunc(func(prompt string) bool {
			rig.prompts = append(rig.prompts, prompt)
			return false
		}),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
		WithApplierDistdir(t.TempDir(), ""),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	rig.applier = applier

	rig.newChecker = func() (*Checker, error) {
		// The order R4.1 fixes is observed here: by the time the re-check is
		// built, the ::gentoo ebuild must already be seeded and the entry
		// re-enabled.
		_, seedErr := os.Stat(rig.anyOverlayEbuild())
		reg, _ := os.ReadFile(filepath.Join(cfgDir, "packages.toml"))
		rig.events.add("newChecker seeded=" + boolWord(seedErr == nil) +
			" enabled=" + boolWord(!strings.Contains(string(reg), "enabled = false")))
		return NewChecker(rig.overlayDir, WithConfigDir(rig.stateDir), WithPendingList(pending))
	}
	return rig
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// anyOverlayEbuild is the path of the first ebuild seeded into the overlay's
// package directory, or a path that does not exist.
func (r *s060ReviveRig) anyOverlayEbuild() string {
	dir := filepath.Join(r.overlayDir, "app-misc", "orphan")
	matches, _ := filepath.Glob(filepath.Join(dir, "orphan-*.ebuild"))
	if len(matches) == 0 {
		return filepath.Join(dir, "none")
	}
	return matches[0]
}

func (r *s060ReviveRig) reviver(t *testing.T, opts ...ReviverOption) *Reviver {
	t.Helper()
	rv, err := NewReviver(r.overlayDir, r.applier, r.gentoo, r.newChecker, opts...)
	if err != nil {
		t.Fatalf("NewReviver: %v", err)
	}
	if rv == nil {
		t.Fatal("NewReviver returned a nil Reviver and no error")
	}
	return rv
}

func assertOutcome(t *testing.T, got ReviveOutcome, pkg string, status ReviveStatus, detail string) {
	t.Helper()
	if got.Package != pkg || got.Status != status || got.Detail != detail {
		t.Errorf("Revive outcome = {%q %q %q}\n                want {%q %q %q}", got.Package, got.Status, got.Detail, pkg, status, detail)
	}
}

// TestS060ReviveRevivesInOrder is R4.1 and R4.8: lookup, versions, seed,
// re-enable, a fresh re-check, then the apply — and the revived detail is
// "<gentoo version> → <upstream version>". The ::gentoo versions are chosen so
// a lexical "highest" (1.9.0) differs from the real one (1.10.0), and a value
// that is not a version is ignored.
func TestS060ReviveRevivesInOrder(t *testing.T) {
	rig := newS060ReviveRig(t, []string{"1.2.0", "1.10.0", "1.9.0", "junk"}, "2.0.0")

	got := rig.reviver(t).Revive(t.Context(), s060RevivePkg)

	assertOutcome(t, got, s060RevivePkg, ReviveRevived, "1.10.0 → 2.0.0")

	events := rig.events.all()
	want := []string{
		"lookup " + s060RevivePkg,
		"versions " + s060RevivePkg,
		"newChecker seeded=true enabled=true",
	}
	if len(events) < len(want)+1 {
		t.Fatalf("pipeline events = %v; want %v followed by the apply's children", events, want)
	}
	for i, w := range want {
		if events[i] != w {
			t.Errorf("pipeline event %d = %q, want %q (R4.1 order)\nall: %v", i, events[i], w, events)
		}
	}
	if !strings.HasPrefix(events[len(want)], "spawn ") {
		t.Errorf("event after the re-check = %q, want the apply's first child (R4.1)", events[len(want)])
	}
	if _, err := os.Stat(rig.applier.EbuildPath(s060RevivePkg, "2.0.0")); err != nil {
		t.Errorf("the bump was not applied: %v", err)
	}
}

// TestS060ReviveCompileReachesTheApply is R4.4 and R4.7: the compile request
// reaches the apply only when asked for.
func TestS060ReviveCompileReachesTheApply(t *testing.T) {
	t.Run("with compile", func(t *testing.T) {
		rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")

		got := rig.reviver(t, WithReviveCompile(true)).Revive(t.Context(), s060RevivePkg)

		if len(rig.prompts) != 1 || !strings.Contains(rig.prompts[0], s060RevivePkg) {
			t.Errorf("compile prompts = %q; want exactly one, for %s (R4.4)", rig.prompts, s060RevivePkg)
		}
		// The compile was declined, so the apply failed with that reason.
		if got.Status != ReviveFailed || got.Detail != ErrUserDeclined.Error() {
			t.Errorf("outcome = %+v, want failed with detail %q", got, ErrUserDeclined.Error())
		}
	})
	t.Run("without compile", func(t *testing.T) {
		rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")

		got := rig.reviver(t).Revive(t.Context(), s060RevivePkg)

		if len(rig.prompts) != 0 {
			t.Errorf("compile prompts = %q; want none without --compile (R4.7)", rig.prompts)
		}
		assertOutcome(t, got, s060RevivePkg, ReviveRevived, "1.0.0 → 2.0.0")
	})
	t.Run("explicitly false", func(t *testing.T) {
		rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")

		rig.reviver(t, WithReviveCompile(false)).Revive(t.Context(), s060RevivePkg)

		if len(rig.prompts) != 0 {
			t.Errorf("compile prompts = %q; want none (R4.7)", rig.prompts)
		}
	})
}

// TestS060ReviveSkipsWhenGentooIsCurrent is R4.3's first clause.
func TestS060ReviveSkipsWhenGentooIsCurrent(t *testing.T) {
	rig := newS060ReviveRig(t, []string{"2.0.0"}, "2.0.0")

	got := rig.reviver(t).Revive(t.Context(), s060RevivePkg)

	assertOutcome(t, got, s060RevivePkg, ReviveSkipped, "gentoo 2.0.0 already current with upstream 2.0.0")
	for _, e := range rig.events.all() {
		if strings.HasPrefix(e, "spawn ") {
			t.Errorf("a skipped revive still applied: %v", rig.events.all())
			break
		}
	}
}

// TestS060ReviveReportsEachFailingStep is R4.2: each step's failure is an
// outcome carrying the exact detail the command printed before this story.
func TestS060ReviveReportsEachFailingStep(t *testing.T) {
	t.Run("invalid package name", func(t *testing.T) {
		rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
		got := rig.reviver(t).Revive(t.Context(), "orphan")
		assertOutcome(t, got, "orphan", ReviveFailed, `invalid package name "orphan" (want category/package)`)
	})
	t.Run("package dir lookup", func(t *testing.T) {
		rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
		rig.gentoo.lookupErr = errors.New("no such directory")
		got := rig.reviver(t).Revive(t.Context(), s060RevivePkg)
		assertOutcome(t, got, s060RevivePkg, ReviveFailed, "gentoo package dir lookup failed: no such directory")
	})
	t.Run("version lookup", func(t *testing.T) {
		rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
		rig.gentoo.versionsErr = errors.New("tree unreadable")
		got := rig.reviver(t).Revive(t.Context(), s060RevivePkg)
		assertOutcome(t, got, s060RevivePkg, ReviveFailed, "gentoo version lookup failed: tree unreadable")
	})
	t.Run("no comparable version", func(t *testing.T) {
		rig := newS060ReviveRig(t, []string{"junk", ""}, "2.0.0")
		got := rig.reviver(t).Revive(t.Context(), s060RevivePkg)
		assertOutcome(t, got, s060RevivePkg, ReviveFailed, "no comparable gentoo version found")
	})
	t.Run("seed", func(t *testing.T) {
		rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
		// The provider names 1.5.0 but its directory has no such ebuild.
		rig.gentoo.versions = []string{"1.0.0", "1.5.0"}
		got := rig.reviver(t).Revive(t.Context(), s060RevivePkg)
		if got.Status != ReviveFailed || !strings.HasPrefix(got.Detail, "seed from gentoo failed: ") {
			t.Errorf("outcome = %+v, want failed with detail prefix %q", got, "seed from gentoo failed: ")
		}
	})
	t.Run("re-enable", func(t *testing.T) {
		rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
		if err := os.Remove(filepath.Join(rig.overlayDir, ".autoupdate", "packages.toml")); err != nil {
			t.Fatal(err)
		}
		got := rig.reviver(t).Revive(t.Context(), s060RevivePkg)
		if got.Status != ReviveFailed || !strings.HasPrefix(got.Detail, "re-enable in packages.toml failed: ") {
			t.Errorf("outcome = %+v, want failed with detail prefix %q", got, "re-enable in packages.toml failed: ")
		}
	})
	t.Run("checker init", func(t *testing.T) {
		rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
		rv, err := NewReviver(rig.overlayDir, rig.applier, rig.gentoo, func() (*Checker, error) {
			return nil, errors.New("bad options")
		})
		if err != nil {
			t.Fatalf("NewReviver: %v", err)
		}
		got := rv.Revive(t.Context(), s060RevivePkg)
		assertOutcome(t, got, s060RevivePkg, ReviveFailed, "checker init failed: bad options")
	})
}

// TestS060ReviveContinuesAfterAFailedTarget is R4.2's "continue": one Reviver
// serves every target, and a failed one leaves it able to revive the next.
func TestS060ReviveContinuesAfterAFailedTarget(t *testing.T) {
	rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
	rv := rig.reviver(t)

	first := rv.Revive(t.Context(), "not-a-package")
	second := rv.Revive(t.Context(), s060RevivePkg)

	if first.Status != ReviveFailed {
		t.Errorf("first outcome = %+v, want failed", first)
	}
	assertOutcome(t, second, s060RevivePkg, ReviveRevived, "1.0.0 → 2.0.0")
}

// TestS060ReviveNeedsALocalPackageDir is R4.5's domain half: an API-only
// ::gentoo cannot seed a base ebuild, and NewReviver says so with a sentinel
// the caller can test before it processes any target.
func TestS060ReviveNeedsALocalPackageDir(t *testing.T) {
	rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")

	rv, err := NewReviver(rig.overlayDir, rig.applier, s060APIOnlyGentoo{}, rig.newChecker)

	if !errors.Is(err, ErrNoLocalPackageDir) {
		t.Errorf("NewReviver error = %v, want it to wrap ErrNoLocalPackageDir (R4.5)", err)
	}
	if rv != nil {
		t.Errorf("NewReviver returned a Reviver alongside the error")
	}
	if ErrNoLocalPackageDir == nil || ErrNoLocalPackageDir.Error() != "the gentoo provider has no local package directory" {
		t.Errorf("ErrNoLocalPackageDir = %v, want the design's sentinel text", ErrNoLocalPackageDir)
	}
}

// TestS060ReviveStatusValues pins the three status strings the summary keys on.
func TestS060ReviveStatusValues(t *testing.T) {
	for got, want := range map[ReviveStatus]string{ReviveRevived: "revived", ReviveSkipped: "skipped", ReviveFailed: "failed"} {
		if string(got) != want {
			t.Errorf("ReviveStatus %q, want %q", got, want)
		}
	}
}

// s060ObsoleteApplier seeds through the real applier and answers Apply the way
// the applier does when the pending bump is already behind the overlay: no
// error, an obsolete result carrying its reason.
type s060ObsoleteApplier struct {
	real    *Applier
	reason  string
	applied []string
	compile []bool
}

func (a *s060ObsoleteApplier) SeedFromGentoo(pkg, srcDir, version string) error {
	return a.real.SeedFromGentoo(pkg, srcDir, version)
}

func (a *s060ObsoleteApplier) Apply(_ context.Context, pkg string, compile bool) (*ApplyResult, error) {
	a.applied = append(a.applied, pkg)
	a.compile = append(a.compile, compile)
	return &ApplyResult{Package: pkg, Obsolete: true, ObsoleteReason: a.reason}, nil
}

var _ ReviveApplier = (*s060ObsoleteApplier)(nil)

// TestS060ReviveSkipsAnObsoleteBump is R4.3's second clause: an apply that
// reports the bump obsolete makes the target skipped, and the detail is the
// applier's own reason, byte for byte.
func TestS060ReviveSkipsAnObsoleteBump(t *testing.T) {
	rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
	stub := &s060ObsoleteApplier{real: rig.applier, reason: "2.0.0 is not newer than the overlay's 2.1.0; dropped from pending"}

	rv, err := NewReviver(rig.overlayDir, stub, rig.gentoo, rig.newChecker)
	if err != nil {
		t.Fatalf("NewReviver: %v", err)
	}
	got := rv.Revive(t.Context(), s060RevivePkg)

	assertOutcome(t, got, s060RevivePkg, ReviveSkipped, stub.reason)
	if len(stub.applied) != 1 || stub.applied[0] != s060RevivePkg || stub.compile[0] {
		t.Errorf("Apply calls = %v (compile %v), want one for %s without compile", stub.applied, stub.compile, s060RevivePkg)
	}
}

// TestS060ReviveCanRevive is the up-front check runRevive makes before any
// other work (R4.5's domain half): an API-only provider cannot seed a revive,
// a provider with a local package directory can.
func TestS060ReviveCanRevive(t *testing.T) {
	if err := CanRevive(s060APIOnlyGentoo{}); !errors.Is(err, ErrNoLocalPackageDir) {
		t.Errorf("CanRevive(API-only) = %v, want an error wrapping ErrNoLocalPackageDir", err)
	}
	if err := CanRevive(&s060Gentoo{events: &s060Events{}}); err != nil {
		t.Errorf("CanRevive(local package dir) = %v, want nil", err)
	}
}
