package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testXML = `<?xml version="1.0" encoding="utf-8"?>
<repositories version="1.0">
  <repo quality="core" status="official">
    <name>gentoo</name>
    <source type="rsync">rsync://rsync.gentoo.org/gentoo-portage</source>
    <source type="git">https://github.com/gentoo-mirror/gentoo.git</source>
    <source type="git">git+ssh://github.com/gentoo-mirror/gentoo.git</source>
    <source type="git">https://anongit.gentoo.org/git/repo/sync/gentoo.git</source>
  </repo>
  <repo quality="experimental" status="unofficial">
    <name>guru</name>
    <source type="git">https://github.com/gentoo-mirror/guru.git</source>
  </repo>
  <repo quality="experimental" status="unofficial">
    <name>gitlab-overlay</name>
    <source type="git">https://gitlab.com/user/overlay.git</source>
  </repo>
  <repo quality="experimental" status="unofficial">
    <name>generic-overlay</name>
    <source type="git">https://anongit.example.org/repo.git</source>
  </repo>
  <repo quality="experimental" status="unofficial">
    <name>rsync-only</name>
    <source type="rsync">rsync://example.org/repo</source>
  </repo>
  <repo quality="experimental" status="unofficial">
    <name>multi-source</name>
    <source type="git">https://gitlab.com/org/multi.git</source>
    <source type="git">https://github.com/org/multi.git</source>
  </repo>
</repositories>`

// --- Task 1.1: XML Parsing Tests ---

func TestParseRepositoriesXML_Valid(t *testing.T) {
	repos, err := parseRepositoriesXML([]byte(testXML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 6 {
		t.Fatalf("expected 6 repos, got %d", len(repos))
	}
	if repos[0].Name != "gentoo" {
		t.Errorf("expected first repo name 'gentoo', got %q", repos[0].Name)
	}
	if len(repos[0].Sources) != 4 {
		t.Errorf("expected 4 sources for gentoo, got %d", len(repos[0].Sources))
	}
}

func TestParseRepositoriesXML_Malformed(t *testing.T) {
	_, err := parseRepositoriesXML([]byte("<broken>"))
	if err == nil {
		t.Fatal("expected error for malformed XML")
	}
}

func TestParseRepositoriesXML_Empty(t *testing.T) {
	repos, err := parseRepositoriesXML([]byte(`<repositories></repositories>`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("expected 0 repos, got %d", len(repos))
	}
}

func TestParseRepositoriesXML_MissingFields(t *testing.T) {
	xml := `<repositories><repo><name>test</name></repo></repositories>`
	repos, err := parseRepositoriesXML([]byte(xml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if len(repos[0].Sources) != 0 {
		t.Errorf("expected 0 sources, got %d", len(repos[0].Sources))
	}
}

// --- Task 1.2: Source URL Selection Tests ---

func TestSelectBestSource_GitHub(t *testing.T) {
	repo := xmlRepo{
		Name: "gentoo",
		Sources: []xmlSource{
			{Type: "rsync", URI: "rsync://rsync.gentoo.org/gentoo-portage"},
			{Type: "git", URI: "https://github.com/gentoo-mirror/gentoo.git"},
			{Type: "git", URI: "https://anongit.gentoo.org/git/repo/sync/gentoo.git"},
		},
	}
	info, err := selectBestSource(repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "github" {
		t.Errorf("expected provider 'github', got %q", info.Provider)
	}
	if info.URL != "gentoo-mirror/gentoo" {
		t.Errorf("expected URL 'gentoo-mirror/gentoo', got %q", info.URL)
	}
	if info.Branch != "master" {
		t.Errorf("expected branch 'master', got %q", info.Branch)
	}
}

func TestSelectBestSource_GitLab(t *testing.T) {
	repo := xmlRepo{
		Name:    "test",
		Sources: []xmlSource{{Type: "git", URI: "https://gitlab.com/user/overlay.git"}},
	}
	info, err := selectBestSource(repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "gitlab" {
		t.Errorf("expected provider 'gitlab', got %q", info.Provider)
	}
	if info.URL != "https://gitlab.com/user/overlay.git" {
		t.Errorf("expected full URL, got %q", info.URL)
	}
}

func TestSelectBestSource_SkipsNonHTTPS(t *testing.T) {
	repo := xmlRepo{
		Name: "test",
		Sources: []xmlSource{
			{Type: "rsync", URI: "rsync://example.org/repo"},
			{Type: "git", URI: "git+ssh://git@github.com/org/repo.git"},
			{Type: "git", URI: "https://github.com/org/repo.git"},
		},
	}
	info, err := selectBestSource(repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "github" {
		t.Errorf("expected 'github', got %q", info.Provider)
	}
}

func TestSelectBestSource_GitHubPreferredOverGitLab(t *testing.T) {
	repo := xmlRepo{
		Name: "multi",
		Sources: []xmlSource{
			{Type: "git", URI: "https://gitlab.com/org/multi.git"},
			{Type: "git", URI: "https://github.com/org/multi.git"},
		},
	}
	info, err := selectBestSource(repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "github" {
		t.Errorf("expected github preferred, got %q", info.Provider)
	}
}

func TestSelectBestSource_GenericHTTPSFallback(t *testing.T) {
	repo := xmlRepo{
		Name:    "test",
		Sources: []xmlSource{{Type: "git", URI: "https://anongit.example.org/repo.git"}},
	}
	info, err := selectBestSource(repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "git" {
		t.Errorf("expected 'git', got %q", info.Provider)
	}
}

func TestSelectBestSource_NoUsableSource(t *testing.T) {
	repo := xmlRepo{
		Name:    "test",
		Sources: []xmlSource{{Type: "rsync", URI: "rsync://example.org/repo"}},
	}
	_, err := selectBestSource(repo)
	if err == nil {
		t.Fatal("expected error for no usable source")
	}
}

func TestSelectBestSource_GitSuffixStripped(t *testing.T) {
	repo := xmlRepo{
		Name:    "test",
		Sources: []xmlSource{{Type: "git", URI: "https://github.com/org/repo.git"}},
	}
	info, err := selectBestSource(repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.URL != "org/repo" {
		t.Errorf("expected 'org/repo', got %q", info.URL)
	}
}

func TestSelectBestSource_DefaultBranch(t *testing.T) {
	repo := xmlRepo{
		Name:    "test",
		Sources: []xmlSource{{Type: "git", URI: "https://github.com/org/repo.git"}},
	}
	info, err := selectBestSource(repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Branch != "master" {
		t.Errorf("expected default branch 'master', got %q", info.Branch)
	}
}

// --- Task 1.3: Registry Tests ---

func newTestRegistry(t *testing.T, server *httptest.Server) *RepositoryRegistry {
	t.Helper()
	dir := t.TempDir()
	return &RepositoryRegistry{
		CacheDir: dir,
		CacheTTL: defaultCacheTTL,
		XMLPath:  filepath.Join(dir, registryXMLFile),
		url:      server.URL,
	}
}

func TestRegistry_FreshDownload(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, testXML)
	}))
	defer server.Close()

	reg := newTestRegistry(t, server)
	names, err := reg.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("expected non-empty list")
	}
	if hits != 1 {
		t.Errorf("expected 1 server hit, got %d", hits)
	}
}

func TestRegistry_CachedFileSkipsDownload(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, testXML)
	}))
	defer server.Close()

	reg := newTestRegistry(t, server)

	// Write cached file
	if err := os.WriteFile(reg.XMLPath, []byte(testXML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := reg.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hits != 0 {
		t.Errorf("expected 0 server hits (cached), got %d", hits)
	}
}

func TestRegistry_ExpiredCacheRedownloads(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, testXML)
	}))
	defer server.Close()

	reg := newTestRegistry(t, server)

	// Write cached file with old mod time
	if err := os.WriteFile(reg.XMLPath, []byte(testXML), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-25 * time.Hour)
	os.Chtimes(reg.XMLPath, oldTime, oldTime)

	_, err := reg.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hits != 1 {
		t.Errorf("expected 1 server hit (expired cache), got %d", hits)
	}
}

func TestRegistry_SyncForcesDownload(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, testXML)
	}))
	defer server.Close()

	reg := newTestRegistry(t, server)

	// Write fresh cache
	if err := os.WriteFile(reg.XMLPath, []byte(testXML), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := reg.Sync(); err != nil {
		t.Fatalf("Sync error: %v", err)
	}
	if hits != 1 {
		t.Errorf("expected 1 server hit (sync forced), got %d", hits)
	}
}

func TestRegistry_DownloadFailureFallsBackToEselect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	dir := t.TempDir()
	home := t.TempDir()

	// Create fake eselect cache
	eselectDir := filepath.Join(home, ".cache", "eselect-repo")
	os.MkdirAll(eselectDir, 0o755)
	os.WriteFile(filepath.Join(eselectDir, "repositories.xml"), []byte(testXML), 0o644)

	// Override home for the test
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", home)
	defer os.Setenv("HOME", origHome)

	reg := &RepositoryRegistry{
		CacheDir: dir,
		CacheTTL: defaultCacheTTL,
		XMLPath:  filepath.Join(dir, registryXMLFile),
		url:      server.URL,
	}

	names, err := reg.List()
	if err != nil {
		t.Fatalf("expected fallback to work, got error: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("expected non-empty list from fallback")
	}
}

func TestRegistry_ResolveKnownRepo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testXML)
	}))
	defer server.Close()

	reg := newTestRegistry(t, server)
	info, err := reg.Resolve("gentoo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "gentoo" {
		t.Errorf("expected name 'gentoo', got %q", info.Name)
	}
	if info.Provider != "github" {
		t.Errorf("expected provider 'github', got %q", info.Provider)
	}
	if info.URL != "gentoo-mirror/gentoo" {
		t.Errorf("expected URL 'gentoo-mirror/gentoo', got %q", info.URL)
	}
}

func TestRegistry_ResolveUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testXML)
	}))
	defer server.Close()

	reg := newTestRegistry(t, server)
	_, err := reg.Resolve("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown repo")
	}
}

func TestRegistry_ListReturnsSorted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testXML)
	}))
	defer server.Close()

	reg := newTestRegistry(t, server)
	names, err := reg.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("list not sorted: %q before %q", names[i-1], names[i])
		}
	}
}
