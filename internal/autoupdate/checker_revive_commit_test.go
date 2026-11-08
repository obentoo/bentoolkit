package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// commitServer serves a one-commit GitHub "list commits" body, the shape a
// track = "commit" record reads through path and commit_sha_path.
func commitServer(t *testing.T, sha, date string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"sha":"` + sha + `","commit":{"committer":{"date":"` + date + `"}}}]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// commitTrackedOrphan is the media-libs/tg_owt record shape: a disabled
// commit-tracked entry with base_from = "none".
func commitTrackedOrphan(url string) registry.PackageConfig {
	return registry.PackageConfig{
		Enabled:       boolPtr(false),
		Track:         "commit",
		URL:           url,
		Parser:        "json",
		Path:          "[0].commit.committer.date",
		Transform:     [][]string{{"T.*", ""}, {"-", ""}},
		CommitSHAPath: "[0].sha",
		BaseFrom:      "none",
	}
}

// TestFindRevivableOrphans_CommitTrackedSameSnapshot is the media-libs/tg_owt
// regression: upstream HEAD is the commit ::gentoo already ships as
// 0_pre20261030, so the raw date 20261030 must not be read as a newer version.
func TestFindRevivableOrphans_CommitTrackedSameSnapshot(t *testing.T) {
	pkg := "media-libs/tg_owt"
	srv := commitServer(t, "020feec5c040fdae2cf752c2c2be62ddde7c236d", "2026-10-30T18:45:44Z")
	checker := newReviveChecker(t, map[string]registry.PackageConfig{pkg: commitTrackedOrphan(srv.URL)})
	prov := &fakeProvider{versions: map[string][]string{pkg: {"0_pre20260803", "0_pre20261030"}}}

	got, err := checker.FindRevivableOrphans(t.Context(), prov)
	if err != nil {
		t.Fatalf("FindRevivableOrphans: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("same snapshot reported as revivable: %+v", got)
	}
}

// TestFindRevivableOrphans_CommitTrackedNewerSnapshot checks the other side: a
// newer upstream commit is revivable, and the reported upstream version is a
// real snapshot version built on ::gentoo's base and suffix (its revision
// dropped), not the bare commit date.
func TestFindRevivableOrphans_CommitTrackedNewerSnapshot(t *testing.T) {
	pkg := "media-libs/tg_owt"
	srv := commitServer(t, "1111111111111111111111111111111111111111", "2026-11-08T09:00:00Z")
	checker := newReviveChecker(t, map[string]registry.PackageConfig{pkg: commitTrackedOrphan(srv.URL)})
	prov := &fakeProvider{versions: map[string][]string{pkg: {"0_pre20261030-r1"}}}

	got, err := checker.FindRevivableOrphans(t.Context(), prov)
	if err != nil {
		t.Fatalf("FindRevivableOrphans: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(got), got)
	}
	if got[0].UpstreamVersion != "0_pre20261108" {
		t.Errorf("UpstreamVersion = %q, want 0_pre20261108", got[0].UpstreamVersion)
	}
	if got[0].GentooVersion != "0_pre20261030-r1" {
		t.Errorf("GentooVersion = %q, want 0_pre20261030-r1", got[0].GentooVersion)
	}
}
