package autoupdate

// Authored for story 060, sub-task 1.2 — R2.1, R2.2, R2.3, R2.4.
//
// Written from the contract (design C2):
//
//	func StalePinBatch(divs []Divergence) map[string]string
//
// StalePin maps Key to Disk; UnclaimedEbuild, NoEbuild and any unknown kind are
// never written; nil when nothing is writable. It is the only rule that turns a
// Divergence into a registry write, and packages.toml is published — so the
// hostile fixtures come first:
//
//  1. wrongly COLLAPSE: an UnclaimedEbuild whose bare atom is the same string
//     as a StalePin registry key (net-misc/rclone is both in the real overlay),
//     placed AFTER the stale pin so a Key→Disk rule over every kind would
//     overwrite the right pin with the stray ebuild's version; and a NoEbuild
//     whose empty Disk would blank an entry;
//  2. wrongly SPLIT/DROP: slot and label siblings ("…:4.1", "…@esr") that share
//     a directory with the bare key must each keep their own pin;
//  3. the benign batch.
//
// Red on arrival: StalePinBatch does not exist.

import (
	"reflect"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestS060StalePinBatchAReportOnlyKindNeverOverwritesAPin is the collapse
// case. Every ordering is tried, so a rule that "happens" to see the stale pin
// last cannot pass.
func TestS060StalePinBatchAReportOnlyKindNeverOverwritesAPin(t *testing.T) {
	stale := Divergence{Key: "net-misc/rclone", Kind: StalePin, Pin: "", Disk: "1.71.1"}
	stray := Divergence{Key: "net-misc/rclone", Kind: UnclaimedEbuild, Disk: "1.9.0"}
	gone := Divergence{Key: "net-misc/rclone", Kind: NoEbuild, Pin: "1.70.0", Disk: ""}
	unknown := Divergence{Key: "net-misc/rclone", Kind: DivergenceKind(99), Disk: "6.6.6"}

	orders := map[string][]Divergence{
		"stale first": {stale, stray, gone, unknown},
		"stale last":  {unknown, gone, stray, stale},
		"stale mid":   {stray, stale, unknown, gone},
	}
	want := map[string]string{"net-misc/rclone": "1.71.1"}
	for name, divs := range orders {
		t.Run(name, func(t *testing.T) {
			got := StalePinBatch(divs)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("StalePinBatch = %v, want %v — only StalePin is writable; an unclaimed ebuild, an entry with no ebuild and an unknown kind sharing its key must not change the pin (R2.1, R2.2)", got, want)
			}
		})
	}
}

// TestS060StalePinBatchNeverWritesAReportOnlyKind is R2.2 for each kind alone,
// and R2.3's precondition: with nothing writable the batch is nil, so the
// caller has nothing to confirm and nothing to write.
func TestS060StalePinBatchNeverWritesAReportOnlyKind(t *testing.T) {
	for _, tt := range []struct {
		name string
		divs []Divergence
	}{
		{name: "nil input", divs: nil},
		{name: "empty input", divs: []Divergence{}},
		{name: "unclaimed ebuild", divs: []Divergence{{Key: "net-misc/rclone", Kind: UnclaimedEbuild, Disk: "1.9.0"}}},
		{name: "no ebuild", divs: []Divergence{{Key: "dev-util/gone", Kind: NoEbuild, Pin: "3.0.0"}}},
		{name: "unknown kind", divs: []Divergence{{Key: "app-misc/new", Kind: DivergenceKind(42), Disk: "1.0"}}},
		{name: "every report-only kind together", divs: []Divergence{
			{Key: "net-misc/rclone", Kind: UnclaimedEbuild, Disk: "1.9.0"},
			{Key: "dev-util/gone", Kind: NoEbuild, Pin: "3.0.0"},
			{Key: "app-misc/new", Kind: DivergenceKind(42), Disk: "1.0"},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := StalePinBatch(tt.divs)
			if got != nil {
				t.Errorf("StalePinBatch = %#v, want nil — nothing here is writable (R2.2, R2.3)", got)
			}
		})
	}
}

// TestS060StalePinBatchKeepsSiblingKeysApart is the split case: three registry
// keys that share one directory are three entries, each with its own pin.
func TestS060StalePinBatchKeepsSiblingKeysApart(t *testing.T) {
	divs := []Divergence{
		{Key: "net-libs/webkit-gtk", Kind: StalePin, Disk: "2.48.1"},
		{Key: "net-libs/webkit-gtk:4.1", Kind: StalePin, Pin: "2.46.0", Disk: "2.48.1-r410"},
		{Key: "net-libs/webkit-gtk:6", Kind: StalePin, Pin: "2.46.0", Disk: "2.48.1-r600"},
		{Key: "www-client/firefox@esr", Kind: StalePin, Disk: "128.3.0"},
		{Key: "www-client/firefox", Kind: StalePin, Disk: "131.0"},
	}
	want := map[string]string{
		"net-libs/webkit-gtk":     "2.48.1",
		"net-libs/webkit-gtk:4.1": "2.48.1-r410",
		"net-libs/webkit-gtk:6":   "2.48.1-r600",
		"www-client/firefox@esr":  "128.3.0",
		"www-client/firefox":      "131.0",
	}
	if got := StalePinBatch(divs); !reflect.DeepEqual(got, want) {
		t.Errorf("StalePinBatch = %v\nwant %v — sibling registry keys are distinct entries (R2.1)", got, want)
	}
}

// TestS060StalePinBatchWritesTheDiskVersion is the benign R2.1 case, and the
// first-time pin (empty Pin) is written the same way as a correction.
func TestS060StalePinBatchWritesTheDiskVersion(t *testing.T) {
	divs := []Divergence{
		{Key: "app-editors/neovim", Kind: StalePin, Pin: "", Disk: "0.11.1"},
		{Key: "dev-lang/go", Kind: StalePin, Pin: "1.26.7", Disk: "1.26.8"},
		{Key: "dev-util/gone", Kind: NoEbuild, Pin: "3.0.0"},
	}
	want := map[string]string{"app-editors/neovim": "0.11.1", "dev-lang/go": "1.26.8"}
	if got := StalePinBatch(divs); !reflect.DeepEqual(got, want) {
		t.Errorf("StalePinBatch = %v, want %v (R2.1)", got, want)
	}
}

// TestS060StalePinBatchOverReconcile drives the rule from the reconciliation
// that produces its input, entirely inside this package (R2.4): a pinless
// entry with an ebuild is a stale pin, a stray ebuild beside a correctly
// pinned sibling is report-only, and the batch carries exactly the stale pin.
func TestS060StalePinBatchOverReconcile(t *testing.T) {
	overlayDir := t.TempDir()
	createTestEbuildFile(t, overlayDir, "app-editors/neovim", "0.11.1")
	createTestEbuildFile(t, overlayDir, "net-misc/rclone", "1.71.1")
	createTestEbuildFile(t, overlayDir, "net-misc/rclone", "1.9.0")

	pkgs := map[string]registry.PackageConfig{
		"app-editors/neovim": {Parser: "json", URL: "https://example.invalid/neovim", Path: "version"},
		"net-misc/rclone":    {Parser: "json", URL: "https://example.invalid/rclone", Path: "version", Version: "1.71.1"},
	}

	divs := Reconcile(nil, overlayDir, pkgs)
	kinds := map[DivergenceKind]int{}
	for _, d := range divs {
		kinds[d.Kind]++
	}
	if kinds[StalePin] == 0 || kinds[UnclaimedEbuild] == 0 {
		t.Fatalf("fixture defect: Reconcile produced %v; want at least one StalePin and one UnclaimedEbuild", divs)
	}

	want := map[string]string{"app-editors/neovim": "0.11.1"}
	if got := StalePinBatch(divs); !reflect.DeepEqual(got, want) {
		t.Errorf("StalePinBatch(Reconcile(nil, ...)) = %v, want %v (R2.1, R2.2)", got, want)
	}

	// Pin neovim as well: everything left is report-only, so the batch is nil.
	pkgs["app-editors/neovim"] = registry.PackageConfig{Parser: "json", URL: "https://example.invalid/neovim", Path: "version", Version: "0.11.1"}
	if got := StalePinBatch(Reconcile(nil, overlayDir, pkgs)); got != nil {
		t.Errorf("with only report-only divergences StalePinBatch = %v, want nil (R2.3)", got)
	}
}
