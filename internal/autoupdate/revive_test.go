package autoupdate

// Pipeline assertions moved here from cmd/bentoo when story 060 moved the
// revive pipeline into Reviver (sub-task 1.4): the package-key split, the
// "check failed" step, the apply-failure suffixes and cancellation of the
// ::gentoo version lookup. The fixtures come from revive_s060_test.go.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// TestReviveReportsACheckFailure is the step after the re-enable: the fresh
// Checker cannot extract an upstream version, so the target fails with the
// "check failed: " detail and nothing is applied.
func TestReviveReportsACheckFailure(t *testing.T) {
	rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
	regPath := filepath.Join(rig.overlayDir, ".autoupdate", "packages.toml")
	reg, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	// The JSON answer has no "nosuch" key, so the extraction fails.
	reg = []byte(strings.Replace(string(reg), `path = "version"`, `path = "nosuch"`, 1))
	if err := os.WriteFile(regPath, reg, 0o644); err != nil {
		t.Fatal(err)
	}

	got := rig.reviver(t).Revive(t.Context(), s060RevivePkg)

	if got.Status != ReviveFailed || !strings.HasPrefix(got.Detail, "check failed: ") {
		t.Errorf("outcome = %+v, want failed with detail prefix %q", got, "check failed: ")
	}
	for _, e := range rig.events.all() {
		if strings.HasPrefix(e, "spawn ") {
			t.Errorf("a revive whose check failed still applied: %v", rig.events.all())
			break
		}
	}
}

// failingApplier seeds through the real applier and fails Apply with a result
// carrying the log and staged-tree paths a failed apply reports.
type failingApplier struct {
	real   *Applier
	result *ApplyResult
	err    error
}

func (a *failingApplier) SeedFromGentoo(pkg, srcDir, version string) error {
	return a.real.SeedFromGentoo(pkg, srcDir, version)
}

func (a *failingApplier) MarkReenabled(pkg string) { a.real.MarkReenabled(pkg) }

func (a *failingApplier) Apply(context.Context, string, bool) (*ApplyResult, error) {
	return a.result, a.err
}

// TestReviveReportsAnApplyFailure pins the detail of a failed apply byte for
// byte: the error, then the log path, then the kept staged tree (S033-R3.6),
// each suffix only when the result names it.
func TestReviveReportsAnApplyFailure(t *testing.T) {
	boom := errors.New("pkgdev manifest failed")
	tests := []struct {
		name   string
		result *ApplyResult
		want   string
	}{
		{"no result", nil, "pkgdev manifest failed"},
		{"empty result", &ApplyResult{}, "pkgdev manifest failed"},
		{"log only", &ApplyResult{LogPath: "/var/log/x.log"}, "pkgdev manifest failed (log: /var/log/x.log)"},
		{"staged only", &ApplyResult{StagedPath: "/tmp/stage"}, "pkgdev manifest failed (staged tree kept at /tmp/stage)"},
		{"log and staged", &ApplyResult{LogPath: "/var/log/x.log", StagedPath: "/tmp/stage"},
			"pkgdev manifest failed (log: /var/log/x.log) (staged tree kept at /tmp/stage)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
			stub := &failingApplier{real: rig.applier, result: tt.result, err: boom}
			rv, err := NewReviver(rig.overlayDir, stub, rig.gentoo, rig.newChecker)
			if err != nil {
				t.Fatalf("NewReviver: %v", err)
			}

			got := rv.Revive(t.Context(), s060RevivePkg)

			assertOutcome(t, got, s060RevivePkg, ReviveFailed, tt.want)
		})
	}
}

// cancelGentoo is the real GitHub provider for the ::gentoo version lookup,
// with a binary-free on-disk package directory beside it.
type cancelGentoo struct {
	*provider.GitHubProvider
	dir string
}

func (g cancelGentoo) LocalPackagePath(string, string) (string, error) { return g.dir, nil }

// TestReviveStopsOnCancel pins that the ::gentoo version lookup carries the
// context Revive is given: cancelling it ends an in-flight lookup within 1 s.
// The version provider is the real GitHub provider pointed at a host that never
// answers. The outcome is a failure whose detail carries the cancellation
// cause, and nothing is seeded.
func TestReviveStopsOnCancel(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // NewGitHubProvider creates a cache dir under $HOME
	t.Setenv("GITHUB_TOKEN", "")

	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	gentoo := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(gentoo.Close)
	t.Cleanup(func() { close(release) }) // runs first (LIFO)

	gh, err := provider.NewGitHubProvider(&provider.RepositoryInfo{Name: "gentoo", Provider: "github", URL: "test/repo"})
	if err != nil {
		t.Fatalf("NewGitHubProvider: %v", err)
	}
	gh.BaseURL = gentoo.URL
	gh.CacheDir = ""

	rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
	prov := cancelGentoo{GitHubProvider: gh, dir: filepath.Join(rig.gentoo.root, s060RevivePkg)}
	rv, err := NewReviver(rig.overlayDir, rig.applier, prov, rig.newChecker)
	if err != nil {
		t.Fatalf("NewReviver: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan ReviveOutcome, 1)
	go func() { done <- rv.Revive(ctx, s060RevivePkg) }()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the ::gentoo version lookup never reached the provider")
	}
	cancel()

	var out ReviveOutcome
	select {
	case out = <-done:
	case <-time.After(time.Second):
		t.Fatal("Revive still running 1s after its context was cancelled: the ::gentoo lookup does not carry ctx")
	}
	if out.Status != ReviveFailed {
		t.Errorf("status = %q (detail: %s), want %q", out.Status, out.Detail, ReviveFailed)
	}
	if !strings.Contains(out.Detail, context.Canceled.Error()) {
		t.Errorf("detail = %q, want it to carry the cancellation cause %q", out.Detail, context.Canceled.Error())
	}
	if _, err := os.Stat(rig.anyOverlayEbuild()); err == nil {
		t.Errorf("a cancelled revive seeded %s into the overlay", rig.anyOverlayEbuild())
	}
}
