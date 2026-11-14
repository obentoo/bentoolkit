package autoupdate

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 3.1 —
// R1.1, R1.2, R1.3, R1.4, R1.5, R1.7, R5.1, R7.1, R7.3, R7.4, R8.1, Q11, Q13.
//
// Integration: a real Applier on a temp overlay, through the STAGED path
// (staging root outside the overlay — the production shape, where the
// candidate reaches the overlay only at promotion), every child process
// mocked to succeed, the clock injected with WithApplierNowFunc and the
// logger captured.
//
// Contract: design.md "Applier integration" — after the promotion decision
// and BEFORE promotion, load the block; non-nil and no cooldown_exempt -> add
// one entry per promoted package (group = sorted cooldown_with closure), save;
// a load or save failure refuses the promotion. ApplyResult.CooldownAdded
// carries the entries. One structured log event per add (package, version).
//
// R1.1's ordering ("before the ebuild file appears") is proved through its
// failure half: when the save fails, the ebuild never appears and no pin is
// written. A promotion that ran before the save could not satisfy that.
//
// The fixture and log sink defined here are reused by claims_cooldown_test.go
// (3.2), applier_cooldown_minage_test.go (3.3) and cooldown_contract_test.go
// (4.3); createTestEbuildFile, mockExecCommandSuccess and hashOverlayTree come
// from applier_test.go and applier_promote_test.go.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// cooldownNow is the injected clock: every entry this applier adds must carry
// it (in UTC) as its Since.
var cooldownNow = time.Date(2026, 11, 7, 7, 10, 0, 0, time.UTC)

const cooldownEmptyBlock = "# BEGIN bentoo-cooldown format=1 days=7\n" +
	"# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand.\n" +
	"# END bentoo-cooldown\n"

const cooldownHandMasks = "# hand-written mask\n=media-libs/sdl3-3.4.20\n\n"

// cooldownLogSink captures every slog record with its attributes.
type cooldownLogSink struct {
	mu      sync.Mutex
	records []cooldownLogRecord
}

type cooldownLogRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

func (s *cooldownLogSink) Enabled(context.Context, slog.Level) bool { return true }
func (s *cooldownLogSink) WithAttrs([]slog.Attr) slog.Handler       { return s }
func (s *cooldownLogSink) WithGroup(string) slog.Handler            { return s }
func (s *cooldownLogSink) Handle(_ context.Context, r slog.Record) error {
	rec := cooldownLogRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	s.mu.Lock()
	s.records = append(s.records, rec)
	s.mu.Unlock()
	return nil
}

// cooldownEvents returns the records about pkg-version whose message names
// the cooldown.
func (s *cooldownLogSink) cooldownEvents(pkg, version string) []cooldownLogRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []cooldownLogRecord
	for _, r := range s.records {
		if strings.Contains(strings.ToLower(r.msg), "cooldown") && r.attrs["package"] == pkg && r.attrs["version"] == version {
			out = append(out, r)
		}
	}
	return out
}

type cooldownBump struct{ pkg, from, to string }

type cooldownFixture struct {
	a       *Applier
	overlay string
	pending *PendingList
	logs    *cooldownLogSink
	pinsMu  sync.Mutex
	pins    map[string]string
}

func (f *cooldownFixture) pin(pkg string) string {
	f.pinsMu.Lock()
	defer f.pinsMu.Unlock()
	return f.pins[pkg]
}

// newCooldownFixture builds an overlay holding each bump's `from` ebuild (and
// any extra versions), the pending bumps, profiles/package.mask with mask
// (no file when mask is ""), and a staged Applier wired to cfgs.
func newCooldownFixture(t *testing.T, bumps []cooldownBump, extra map[string][]string, cfgs map[string]registry.PackageConfig, mask string, opts ...ApplierOption) *cooldownFixture {
	t.Helper()
	tmp := t.TempDir()
	f := &cooldownFixture{overlay: filepath.Join(tmp, "overlay"), logs: &cooldownLogSink{}, pins: map[string]string{}}
	configDir := filepath.Join(tmp, "config")

	for _, b := range bumps {
		createTestEbuildFile(t, f.overlay, b.pkg, b.from)
		dir := filepath.Join(f.overlay, filepath.FromSlash(b.pkg))
		name := filepath.Base(b.pkg)
		manifest := "DIST " + name + "-" + b.from + ".tar.xz 100 BLAKE2B ab SHA512 cd\n"
		if err := os.WriteFile(filepath.Join(dir, "Manifest"), []byte(manifest), 0o644); err != nil {
			t.Fatalf("writing the Manifest of %s: %v", b.pkg, err)
		}
	}
	for pkg, versions := range extra {
		for _, v := range versions {
			createTestEbuildFile(t, f.overlay, pkg, v)
		}
	}
	for _, d := range []string{"profiles", ".autoupdate", "metadata"} {
		if err := os.MkdirAll(filepath.Join(f.overlay, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if mask != "" {
		if err := os.WriteFile(filepath.Join(f.overlay, "profiles", "package.mask"), []byte(mask), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var err error
	f.pending, err = NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating the pending list: %v", err)
	}
	for _, b := range bumps {
		if err := f.pending.Add(PendingUpdate{Package: b.pkg, CurrentVersion: b.from, NewVersion: b.to, Status: StatusPending}); err != nil {
			t.Fatalf("pending.Add(%s): %v", b.pkg, err)
		}
	}

	if cfgs == nil {
		cfgs = map[string]registry.PackageConfig{}
	}
	base := []ApplierOption{
		WithApplierPendingList(f.pending),
		WithExecCommand(mockExecCommandSuccess),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
		WithApplierPackagesConfig(&registry.PackagesConfig{Packages: cfgs}),
		WithApplierLogger(slog.New(f.logs)),
		WithApplierNowFunc(func() time.Time { return cooldownNow }),
		WithApplierSetVersionsFunc(func(_ string, written map[string]string) error {
			f.pinsMu.Lock()
			defer f.pinsMu.Unlock()
			for k, v := range written {
				f.pins[k] = v
			}
			return nil
		}),
	}
	f.a, err = NewApplier(f.overlay, configDir, append(base, opts...)...)
	if err != nil {
		t.Fatalf("creating the applier: %v", err)
	}
	return f
}

func (f *cooldownFixture) ebuildExists(pkg, version string) bool {
	_, err := os.Stat(filepath.Join(f.overlay, filepath.FromSlash(pkg), filepath.Base(pkg)+"-"+version+".ebuild"))
	return err == nil
}

func (f *cooldownFixture) mask(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.overlay, "profiles", "package.mask"))
	if err != nil {
		t.Fatalf("reading profiles/package.mask: %v", err)
	}
	return string(b)
}

// unmaskList returns the atoms of .autoupdate/cooldown.unmask, sorted; nil
// when the file does not exist.
func (f *cooldownFixture) unmaskList(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.overlay, ".autoupdate", "cooldown.unmask"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading .autoupdate/cooldown.unmask: %v", err)
	}
	var atoms []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			atoms = append(atoms, line)
		}
	}
	sort.Strings(atoms)
	return atoms
}

// block loads the block through the package's own reader; the contract test
// (4.3) separately proves the reader and the writer agree.
func (f *cooldownFixture) block(t *testing.T) *cooldown.Block {
	t.Helper()
	b, err := cooldown.Load(f.overlay)
	if err != nil {
		t.Fatalf("cooldown.Load after the apply: %v", err)
	}
	if b == nil {
		t.Fatal("cooldown.Load returned nil: the block is gone from profiles/package.mask")
	}
	return b
}

func entryFor(b *cooldown.Block, pkg, version string) (cooldown.Entry, bool) {
	for _, e := range b.Entries {
		if e.Package == pkg && e.Version == version {
			return e, true
		}
	}
	return cooldown.Entry{}, false
}

func applyOne(t *testing.T, f *cooldownFixture, pkg string) *ApplyResult {
	t.Helper()
	result, _ := f.a.Apply(t.Context(), pkg)
	if result == nil {
		t.Fatalf("Apply(%s) returned a nil result", pkg)
	}
	return result
}

func applyAll(t *testing.T, f *cooldownFixture) map[string]*ApplyResult {
	t.Helper()
	results, _ := f.a.ApplyAll(t.Context(), f.pending.List(), 1)
	byPkg := map[string]*ApplyResult{}
	for _, r := range results {
		if r != nil {
			byPkg[r.Package] = r
		}
	}
	return byPkg
}

// TestCooldownApply_APromotedBumpIsMaskedInBothFiles: R1.1 + R1.2. The new
// version is promoted, its atom is in the block with the injected Since and
// origin autoupdate, the unmask list carries the same atom, the previous
// ebuild is still there, and the result reports the addition.
func TestCooldownApply_APromotedBumpIsMaskedInBothFiles(t *testing.T) {
	const pkg = "app-misc/cooldown-foo"
	f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "1.1"}}, nil, nil, cooldownHandMasks+cooldownEmptyBlock)

	result := applyOne(t, f, pkg)
	if !result.Success {
		t.Fatalf("the apply failed: %v", result.Error)
	}
	if !f.ebuildExists(pkg, "1.1") || !f.ebuildExists(pkg, "1.0") {
		t.Errorf("after the apply: 1.1 present=%v, 1.0 present=%v; want both", f.ebuildExists(pkg, "1.1"), f.ebuildExists(pkg, "1.0"))
	}

	e, ok := entryFor(f.block(t), pkg, "1.1")
	if !ok {
		t.Fatalf("no cooldown entry for %s-1.1:\n%s", pkg, f.mask(t))
	}
	if !e.Since.Equal(cooldownNow) {
		t.Errorf("Since = %s, want the injected clock %s", e.Since, cooldownNow)
	}
	if e.Origin != "autoupdate" {
		t.Errorf("Origin = %q, want autoupdate", e.Origin)
	}
	if got := f.unmaskList(t); strings.Join(got, " ") != "="+pkg+"-1.1" {
		t.Errorf("cooldown.unmask = %v, want [=%s-1.1]", got, pkg)
	}
	if !strings.HasPrefix(f.mask(t), cooldownHandMasks) {
		t.Errorf("the hand-written masks above the block changed:\n%s", f.mask(t))
	}
	if len(result.CooldownAdded) != 1 || result.CooldownAdded[0].Package != pkg || result.CooldownAdded[0].Version != "1.1" {
		t.Errorf("result.CooldownAdded = %+v, want one entry for %s-1.1", result.CooldownAdded, pkg)
	}
}

// TestCooldownApply_TheBlockIsTheSwitch: R1.4. Hostile half first — with
// the block the bump IS masked (a cooldown that never engages would pass the
// other two) — then: without a block, or without package.mask at all, the
// bump is promoted as today, package.mask is byte-identical and no cooldown
// file is written.
func TestCooldownApply_TheBlockIsTheSwitch(t *testing.T) {
	const pkg = "app-misc/cooldown-foo"

	t.Run("block present", func(t *testing.T) {
		f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "1.1"}}, nil, nil, cooldownHandMasks+cooldownEmptyBlock)
		if r := applyOne(t, f, pkg); !r.Success {
			t.Fatalf("the apply failed: %v", r.Error)
		}
		if _, ok := entryFor(f.block(t), pkg, "1.1"); !ok {
			t.Errorf("the overlay carries a block and the bump was not masked:\n%s", f.mask(t))
		}
	})

	for name, mask := range map[string]string{"hand masks, no block": cooldownHandMasks, "no package.mask": ""} {
		t.Run(name, func(t *testing.T) {
			f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "1.1"}}, nil, nil, mask)
			r := applyOne(t, f, pkg)
			if !r.Success {
				t.Fatalf("the apply failed without a block: %v", r.Error)
			}
			if !f.ebuildExists(pkg, "1.1") {
				t.Error("the bump was not promoted")
			}
			if mask != "" && f.mask(t) != mask {
				t.Errorf("package.mask changed although it has no block:\n%s", f.mask(t))
			}
			if _, err := os.Stat(filepath.Join(f.overlay, "profiles", "package.mask")); mask == "" && err == nil {
				t.Error("profiles/package.mask was created")
			}
			if got := f.unmaskList(t); got != nil {
				t.Errorf("cooldown.unmask was written: %v", got)
			}
			if len(r.CooldownAdded) != 0 {
				t.Errorf("CooldownAdded = %+v without a block", r.CooldownAdded)
			}
		})
	}
}

// TestCooldownApply_AFailedCooldownWriteRefusesThePromotion: R1.3 and the
// failure half of R1.1. A directory squatting on the unmask path makes the
// save fail (for root too): the bump is refused, the error names the path,
// the candidate never reaches the overlay and no pin is written.
func TestCooldownApply_AFailedCooldownWriteRefusesThePromotion(t *testing.T) {
	const pkg = "app-misc/cooldown-foo"
	f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "1.1"}}, nil, nil, cooldownEmptyBlock)
	if err := os.MkdirAll(filepath.Join(f.overlay, ".autoupdate", "cooldown.unmask", "squatter"), 0o755); err != nil {
		t.Fatal(err)
	}

	result := applyOne(t, f, pkg)

	if result.Success {
		t.Fatal("the bump was promoted although its cooldown entry could not be written; an unmasked bump is the failure this story prevents")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "cooldown.unmask") {
		t.Errorf("the refusal does not name the unmask list: %v", result.Error)
	}
	if f.ebuildExists(pkg, "1.1") {
		t.Error("the refused candidate is in the published overlay")
	}
	if got := f.pin(pkg); got != "" {
		t.Errorf("the registry pin moved to %q for a refused bump", got)
	}
}

// TestCooldownApply_AMalformedBlockRefusesOnlyWhatNeedsAnEntry: R7.3 + R7.1.
// The cooldown-eligible bump is refused naming package.mask; the exempt bump
// in the same run, which needs no entry, is promoted; the run reports both;
// the malformed file is byte-identical.
func TestCooldownApply_AMalformedBlockRefusesOnlyWhatNeedsAnEntry(t *testing.T) {
	malformed := cooldownHandMasks +
		"# BEGIN bentoo-cooldown format=1 days=7\n" +
		"this line is neither a cooldown comment nor an atom\n" +
		"# END bentoo-cooldown\n"
	reason := "upstream re-tags in place"
	f := newCooldownFixture(t,
		[]cooldownBump{{"app-misc/cooldown-foo", "1.0", "1.1"}, {"app-misc/cooldown-exempt", "2.0", "2.1"}},
		nil,
		map[string]registry.PackageConfig{"app-misc/cooldown-exempt": {CooldownExempt: &reason}},
		malformed)

	results := applyAll(t, f)

	if len(results) != 2 {
		t.Fatalf("the run reported %d results, want 2 (the rest of the run is still reported)", len(results))
	}
	foo := results["app-misc/cooldown-foo"]
	if foo == nil || foo.Success {
		t.Errorf("app-misc/cooldown-foo was promoted under a malformed block: %+v", foo)
	} else if foo.Error == nil || !strings.Contains(foo.Error.Error(), "package.mask") {
		t.Errorf("the refusal does not name profiles/package.mask: %v", foo.Error)
	}
	if f.ebuildExists("app-misc/cooldown-foo", "1.1") {
		t.Error("the refused candidate is in the published overlay")
	}
	if ex := results["app-misc/cooldown-exempt"]; ex == nil || !ex.Success {
		t.Errorf("the exempt bump, which needs no entry, was refused: %+v", ex)
	}
	if got := f.mask(t); got != malformed {
		t.Errorf("the malformed package.mask was rewritten:\n%s", got)
	}
}

// TestCooldownApply_AnExemptRecordIsPromotedUnmasked: R5.1, beside a
// non-exempt record in the same run that IS masked.
func TestCooldownApply_AnExemptRecordIsPromotedUnmasked(t *testing.T) {
	reason := "the -bin is rebuilt from the same tag; masking it would only delay a mirror"
	f := newCooldownFixture(t,
		[]cooldownBump{{"app-misc/cooldown-exempt", "2.0", "2.1"}, {"app-misc/cooldown-foo", "1.0", "1.1"}},
		nil,
		map[string]registry.PackageConfig{"app-misc/cooldown-exempt": {CooldownExempt: &reason}},
		cooldownEmptyBlock)

	results := applyAll(t, f)
	for pkg, r := range results {
		if !r.Success {
			t.Fatalf("%s failed: %v", pkg, r.Error)
		}
	}
	b := f.block(t)
	if _, ok := entryFor(b, "app-misc/cooldown-foo", "1.1"); !ok {
		t.Error("the non-exempt bump was not masked")
	}
	if e, ok := entryFor(b, "app-misc/cooldown-exempt", "2.1"); ok {
		t.Errorf("the exempt record got a cooldown entry: %+v", e)
	}
	for _, a := range f.unmaskList(t) {
		if strings.Contains(a, "cooldown-exempt") {
			t.Errorf("cooldown.unmask lists the exempt package: %s", a)
		}
	}
}

// TestCooldownApply_CoupledPackagesShareOneGroup: R1.7 with a group of
// THREE of which two are promoted in this run. Both carry the full sorted
// closure; the unbumped member gets no entry; a prefix-named package with no
// cooldown_with is promoted alone, with no group.
func TestCooldownApply_CoupledPackagesShareOneGroup(t *testing.T) {
	const a, b, c, near = "dev-lang/cd-a", "dev-lang/cd-b", "dev-lang/cd-c", "dev-lang/cd-a-bin"
	cfgs := map[string]registry.PackageConfig{
		a: {CooldownWith: []string{b, c}},
		b: {CooldownWith: []string{a, c}},
		c: {CooldownWith: []string{a, b}},
	}
	f := newCooldownFixture(t,
		[]cooldownBump{{b, "1.0", "1.1"}, {a, "1.0", "1.1"}, {near, "1.0", "1.1"}},
		map[string][]string{c: {"1.0"}},
		cfgs, cooldownEmptyBlock)

	for pkg, r := range applyAll(t, f) {
		if !r.Success {
			t.Fatalf("%s failed: %v", pkg, r.Error)
		}
	}
	blk := f.block(t)
	const want = "dev-lang/cd-a,dev-lang/cd-b,dev-lang/cd-c"
	for _, pkg := range []string{a, b} {
		e, ok := entryFor(blk, pkg, "1.1")
		if !ok {
			t.Errorf("no entry for %s-1.1", pkg)
			continue
		}
		if e.Group != want {
			t.Errorf("%s Group = %q, want %q (the sorted cooldown_with closure)", pkg, e.Group, want)
		}
	}
	for _, e := range blk.Entries {
		if e.Package == c {
			t.Errorf("the unbumped member got an entry: %+v", e)
		}
	}
	if e, ok := entryFor(blk, near, "1.1"); !ok || e.Group != "" {
		t.Errorf("%s-1.1 entry = %+v (found %v), want an entry with no group", near, e, ok)
	}
}

// TestCooldownApply_TheAtomIsBuiltFromThePromotedPVRNeverFromUpstreamText:
// R7.4 and design.md Security ("atoms are built from the record's cat/pkg and
// the promoted PVR, never from upstream text").
//
// Hostile half first: upstream says "v1.1"; the applier promotes 1.1, and the
// block and the unmask list must say =pkg-1.1 — the upstream spelling must
// appear nowhere. Then a version that is no PVR at all ("1.1;x") is refused
// and never becomes a line in either file.
func TestCooldownApply_TheAtomIsBuiltFromThePromotedPVRNeverFromUpstreamText(t *testing.T) {
	const pkg = "app-misc/cooldown-foo"

	t.Run("upstream v-prefixed tag", func(t *testing.T) {
		f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "v1.1"}}, nil, nil, cooldownEmptyBlock)
		r := applyOne(t, f, pkg)
		if !r.Success || !f.ebuildExists(pkg, "1.1") {
			t.Fatalf("precondition: the applier promotes upstream v1.1 as 1.1 (success=%v, err=%v)", r.Success, r.Error)
		}
		if _, ok := entryFor(f.block(t), pkg, "1.1"); !ok {
			t.Errorf("no entry for the promoted PVR %s-1.1:\n%s", pkg, f.mask(t))
		}
		if strings.Contains(f.mask(t), "v1.1") {
			t.Errorf("package.mask carries the upstream spelling v1.1:\n%s", f.mask(t))
		}
		if got := f.unmaskList(t); strings.Join(got, " ") != "="+pkg+"-1.1" {
			t.Errorf("cooldown.unmask = %v, want [=%s-1.1]", got, pkg)
		}
	})

	t.Run("not a PVR at all", func(t *testing.T) {
		const bad = "1.1;x"
		f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", bad}}, nil, nil, cooldownEmptyBlock)
		r := applyOne(t, f, pkg)
		if r.Success {
			t.Errorf("a bump to %q was promoted", bad)
		}
		if strings.Contains(f.mask(t), bad) {
			t.Errorf("package.mask carries the non-PVR version:\n%s", f.mask(t))
		}
		for _, a := range f.unmaskList(t) {
			if strings.Contains(a, bad) {
				t.Errorf("cooldown.unmask carries the non-PVR version: %s", a)
			}
		}
	})
}

// TestCooldownApply_AReappliedVersionKeepsOneEntryAndItsOriginalSince: R1.5
// through the applier. An entry left by an earlier attempt at the same
// version keeps its Since; the re-apply adds nothing.
func TestCooldownApply_AReappliedVersionKeepsOneEntryAndItsOriginalSince(t *testing.T) {
	const pkg = "app-misc/cooldown-foo"
	earlier := cooldownNow.Add(-48 * time.Hour)
	mask := "# BEGIN bentoo-cooldown format=1 days=7\n" +
		"# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand.\n" +
		"# cooldown since=" + earlier.Format(time.RFC3339) + " origin=autoupdate\n" +
		"=" + pkg + "-1.1\n" +
		"# END bentoo-cooldown\n"
	f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "1.1"}}, nil, nil, mask)

	r := applyOne(t, f, pkg)
	if !r.Success {
		t.Fatalf("the re-apply failed: %v", r.Error)
	}
	var n int
	var since time.Time
	for _, e := range f.block(t).Entries {
		if e.Package == pkg && e.Version == "1.1" {
			n++
			since = e.Since
		}
	}
	if n != 1 {
		t.Fatalf("%d entries for %s-1.1 after the re-apply, want 1", n, pkg)
	}
	if !since.Equal(earlier) {
		t.Errorf("Since = %s after the re-apply, want the original %s", since, earlier)
	}
	if len(r.CooldownAdded) != 0 {
		t.Errorf("CooldownAdded = %+v for an entry that already existed", r.CooldownAdded)
	}
}

// TestCooldownLog_OnePerApplierAdd: R8.1 / Q13 for additions.
// Exactly one structured record about the added atom, carrying package and
// version — and none for the exempt package promoted beside it.
func TestCooldownLog_OnePerApplierAdd(t *testing.T) {
	reason := "exempt for the log test"
	f := newCooldownFixture(t,
		[]cooldownBump{{"app-misc/cooldown-foo", "1.0", "1.1"}, {"app-misc/cooldown-exempt", "2.0", "2.1"}},
		nil,
		map[string]registry.PackageConfig{"app-misc/cooldown-exempt": {CooldownExempt: &reason}},
		cooldownEmptyBlock)

	for pkg, r := range applyAll(t, f) {
		if !r.Success {
			t.Fatalf("%s failed: %v", pkg, r.Error)
		}
	}
	if got := f.logs.cooldownEvents("app-misc/cooldown-foo", "1.1"); len(got) != 1 {
		t.Errorf("%d cooldown log records for app-misc/cooldown-foo-1.1, want exactly 1: %+v", len(got), got)
	}
	if got := f.logs.cooldownEvents("app-misc/cooldown-exempt", "2.1"); len(got) != 0 {
		t.Errorf("the exempt package logged a cooldown addition: %+v", got)
	}
}
