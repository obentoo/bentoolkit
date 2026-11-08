package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

func TestGraduationURL(t *testing.T) {
	until := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for raw, want := range map[string]string{
		"https://api.github.com/repos/o/r/commits?sha=main&per_page=50":                               "until=2026-11-01T09%3A00%3A00Z",
		"https://gitlab.freedesktop.org/api/v4/projects/mesa%2Fmesa/repository/commits?ref_name=main": "until=2026-11-01T09%3A00%3A00Z",
		"https://api.github.com/repos/o/r/commits?sha=main&until=2020-01-01T00:00:00Z":                "until=2026-11-01T09%3A00%3A00Z",
		"https://api.github.com/repos/o/r/commits/main":                                               "",
		"https://example.org/releases.json":                                                           "",
		"not a url":                                                                                   "",
	} {
		got, ok := graduationURL(raw, until)
		if want == "" {
			if ok {
				t.Errorf("graduationURL(%q) = %q, want refused", raw, got)
			}
			continue
		}
		if !ok || !strings.Contains(got, want) || strings.Count(got, "until=") != 1 {
			t.Errorf("graduationURL(%q) = %q (%v), want one %s", raw, got, ok, want)
		}
		if strings.Contains(raw, "mesa%2Fmesa") && !strings.Contains(got, "mesa%2Fmesa") {
			t.Errorf("graduationURL lost the escaped project path: %q", got)
		}
	}
}

// graduationServer answers a GitHub commit list: the HEAD commit, or, when
// asked with until=, the older commit — and records the until it was asked.
func graduationServer(t *testing.T, head, older []byte) (*httptest.Server, *string) {
	t.Helper()
	var mu sync.Mutex
	asked := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if u := r.URL.Query().Get("until"); u != "" {
			mu.Lock()
			*asked = u
			mu.Unlock()
			_, _ = w.Write(older)
			return
		}
		_, _ = w.Write(head)
	}))
	t.Cleanup(srv.Close)
	return srv, asked
}

func newGraduationChecker(t *testing.T, pkg, current string, cfg registry.PackageConfig, mask string, now time.Time) *Checker {
	t.Helper()
	overlayDir := t.TempDir()
	if mask != "" {
		if err := os.MkdirAll(filepath.Join(overlayDir, "profiles"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(overlayDir, "profiles", "package.mask"), []byte(mask), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := NewChecker(overlayDir,
		WithConfigDir(t.TempDir()),
		WithPackagesConfig(&registry.PackagesConfig{Packages: map[string]registry.PackageConfig{pkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
		WithNowFunc(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	createTestEbuild(t, overlayDir, pkg, current)
	return c
}

// A commit-tracked line under the cooldown queues, beside its new candidate,
// the newest upstream commit at least the cooldown's days old.
func TestCheckPackageCommitTrack_QueuesTheGraduationTarget(t *testing.T) {
	const pkg = "media-libs/grad"
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	head := makeGHCommits(struct{ sha, date, msg string }{testSHA40, "2026-11-08T09:00:00Z", "head"})
	older := makeGHCommits(struct{ sha, date, msg string }{testSHA40b, "2026-11-01T08:00:00Z", "a week ago"})
	srv, asked := graduationServer(t, head, older)

	cfg := baseCommitCfg()
	cfg.URL = srv.URL + "/repos/o/r/commits?sha=main&per_page=50"
	c := newGraduationChecker(t, pkg, "27.0.0_pre20261030", cfg, cooldownEmptyBlock, now)

	result, err := c.CheckPackage(t.Context(), pkg, true)
	if err != nil || !result.HasUpdate {
		t.Fatalf("CheckPackage: result %+v err %v, want an update", result, err)
	}
	upd, ok := c.pending.Get(pkg)
	if !ok {
		t.Fatal("no pending entry")
	}
	if upd.NewVersion != "27.0.0_pre20261108" || upd.CommitHash != testSHA40 {
		t.Errorf("candidate = %s @ %s, want 27.0.0_pre20261108 @ HEAD", upd.NewVersion, upd.CommitHash)
	}
	if upd.GraduateVersion != "27.0.0_pre20261101" || upd.GraduateCommit != testSHA40b {
		t.Errorf("graduation = %s @ %s, want 27.0.0_pre20261101 @ the older commit", upd.GraduateVersion, upd.GraduateCommit)
	}
	if want := now.AddDate(0, 0, -7).Format(time.RFC3339); *asked != want {
		t.Errorf("asked until=%q, want %q (now minus the block's 7 days)", *asked, want)
	}
}

// No block, an exempt record, or an endpoint that cannot take until=: no
// graduation target, the candidate is queued exactly as before.
func TestCheckPackageCommitTrack_NoGraduationOutsideTheCooldown(t *testing.T) {
	const pkg = "media-libs/grad"
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	head := makeGHCommits(struct{ sha, date, msg string }{testSHA40, "2026-11-08T09:00:00Z", "head"})
	older := makeGHCommits(struct{ sha, date, msg string }{testSHA40b, "2026-11-01T08:00:00Z", "older"})
	reason := "tracks a security branch"
	for name, tc := range map[string]struct {
		mask   string
		mutate func(*registry.PackageConfig, string)
	}{
		"no block": {mask: "", mutate: func(c *registry.PackageConfig, base string) { c.URL = base + "/repos/o/r/commits?sha=main" }},
		"exempt record": {mask: cooldownEmptyBlock, mutate: func(c *registry.PackageConfig, base string) {
			c.URL = base + "/repos/o/r/commits?sha=main"
			c.CooldownExempt = &reason
		}},
		"single commit": {mask: cooldownEmptyBlock, mutate: func(c *registry.PackageConfig, base string) { c.URL = base + "/repos/o/r/commits/main" }},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := graduationServer(t, head, older)
			cfg := baseCommitCfg()
			tc.mutate(&cfg, srv.URL)
			if strings.HasSuffix(cfg.URL, "/commits/main") {
				// The single-commit endpoint answers an object, not a list.
				cfg.Path, cfg.CommitSHAPath = "commit.committer.date", "sha"
				srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`{"sha":"` + testSHA40 + `","commit":{"committer":{"date":"2026-11-08T09:00:00Z"}}}`))
				})
			}
			c := newGraduationChecker(t, pkg, "27.0.0_pre20261030", cfg, tc.mask, now)
			if _, err := c.CheckPackage(t.Context(), pkg, true); err != nil {
				t.Fatalf("CheckPackage: %v", err)
			}
			upd, ok := c.pending.Get(pkg)
			if !ok {
				t.Fatal("no pending entry")
			}
			if upd.GraduateVersion != "" || upd.GraduateCommit != "" {
				t.Errorf("graduation = %q @ %q, want none", upd.GraduateVersion, upd.GraduateCommit)
			}
		})
	}
}

// The apply moves the line's unmasked snapshot to the graduation target,
// unmasked, then masks the new candidate, which supersedes the old one. The
// candidate's pending entry and pin are the candidate apply's, never the
// graduation's.
func TestCooldownApply_GraduatesTheInstallableSnapshotThenMasksTheCandidate(t *testing.T) {
	const pkg = "media-libs/cooldown-snap"
	const shaOld, shaCand, shaGrad, shaHead = "1111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222", "3333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444"
	ebuildText := func(sha string) string {
		return "EAPI=8\nDESCRIPTION=\"x\"\nHOMEPAGE=\"https://example.com\"\nGIT_COMMIT=\"" + sha +
			"\"\nSRC_URI=\"\"\nLICENSE=\"MIT\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"
	}
	since := cooldownNow.Add(-24 * time.Hour).Format(time.RFC3339)
	mask := blockWith("# cooldown since=" + since + " origin=autoupdate line=" + pkg + "\n=" + pkg + "-1.0_p20261106\n")
	cfgs := map[string]registry.PackageConfig{pkg: {Track: "commit", URL: "https://api.github.com/repos/o/r/commits?sha=main"}}
	f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0_p20261106", "1.0_p20261107"}}, nil, cfgs, mask)
	createTestEbuildFileWithContent(t, f.overlay, pkg, "1.0_p20261020", ebuildText(shaOld))
	createTestEbuildFileWithContent(t, f.overlay, pkg, "1.0_p20261106", ebuildText(shaCand))
	if err := f.pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.0_p20261106", NewVersion: "1.0_p20261107",
		CommitHash: shaHead, GraduateVersion: "1.0_p20261031", GraduateCommit: shaGrad, Status: StatusPending}); err != nil {
		t.Fatal(err)
	}

	r := applyOne(t, f, pkg)
	if !r.Success {
		t.Fatalf("the candidate apply failed: %v", r.Error)
	}
	if r.Graduated == nil || !r.Graduated.Success || r.Graduated.NewVersion != "1.0_p20261031" || r.Graduated.OldVersion != "1.0_p20261020" {
		t.Fatalf("graduation = %+v, want 1.0_p20261020 -> 1.0_p20261031 succeeded", r.Graduated)
	}
	b := f.block(t)
	if _, masked := entryFor(b, pkg, "1.0_p20261031"); masked {
		t.Errorf("the graduated snapshot is masked:\n%s", f.mask(t))
	}
	if e, masked := entryFor(b, pkg, "1.0_p20261107"); !masked || !e.First.IsZero() && !e.First.Equal(e.Since) {
		t.Errorf("candidate entry = %+v (masked %v), want masked on a fresh line", e, masked)
	}
	if f.ebuildExists(pkg, "1.0_p20261106") {
		t.Error("the superseded candidate is still in the tree")
	}
	grad, err := os.ReadFile(filepath.Join(f.overlay, "media-libs", "cooldown-snap", "cooldown-snap-1.0_p20261031.ebuild"))
	if err != nil || !strings.Contains(string(grad), shaGrad) {
		t.Errorf("the graduated ebuild does not pin the graduation commit (err %v):\n%s", err, grad)
	}
	if _, ok := f.pending.Get(pkg); ok {
		t.Error("the candidate's pending entry is still queued after its successful apply")
	}
	if pin := f.pin(pkg); pin != "1.0_p20261107" {
		t.Errorf("registry pin = %q, want the candidate 1.0_p20261107", pin)
	}
}
