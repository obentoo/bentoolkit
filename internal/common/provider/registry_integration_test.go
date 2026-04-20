package provider

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

const endToEndXML = `<?xml version="1.0"?>
<repositories version="1.0">
  <repo>
    <name>gentoo</name>
    <source type="rsync">rsync://rsync.gentoo.org/gentoo-portage</source>
    <source type="git">https://github.com/gentoo-mirror/gentoo.git</source>
    <source type="git">https://anongit.gentoo.org/git/repo/sync/gentoo.git</source>
  </repo>
  <repo>
    <name>gitlab-repo</name>
    <source type="git">https://gitlab.com/user/overlay.git</source>
  </repo>
  <repo>
    <name>generic-repo</name>
    <source type="git">https://anongit.example.org/repo.git</source>
  </repo>
</repositories>`

func TestEndToEnd_GentooResolvesToGitHub(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, endToEndXML)
	}))
	defer server.Close()

	dir := t.TempDir()
	registry := &RepositoryRegistry{
		CacheDir: dir,
		CacheTTL: defaultCacheTTL,
		XMLPath:  filepath.Join(dir, registryXMLFile),
		url:      server.URL,
	}

	info, err := ResolveRepository("gentoo", nil, registry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "gentoo" {
		t.Errorf("expected Name 'gentoo', got %q", info.Name)
	}
	if info.Provider != "github" {
		t.Errorf("expected Provider 'github', got %q", info.Provider)
	}
	if info.URL != "gentoo-mirror/gentoo" {
		t.Errorf("expected URL 'gentoo-mirror/gentoo', got %q", info.URL)
	}
	if info.Branch != "master" {
		t.Errorf("expected Branch 'master', got %q", info.Branch)
	}
}

func TestEndToEnd_GitLabRepoResolves(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, endToEndXML)
	}))
	defer server.Close()

	dir := t.TempDir()
	registry := &RepositoryRegistry{
		CacheDir: dir,
		CacheTTL: defaultCacheTTL,
		XMLPath:  filepath.Join(dir, registryXMLFile),
		url:      server.URL,
	}

	info, err := ResolveRepository("gitlab-repo", nil, registry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "gitlab" {
		t.Errorf("expected Provider 'gitlab', got %q", info.Provider)
	}
	if info.URL != "https://gitlab.com/user/overlay.git" {
		t.Errorf("expected full gitlab URL, got %q", info.URL)
	}
}

func TestEndToEnd_GenericGitRepoResolves(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, endToEndXML)
	}))
	defer server.Close()

	dir := t.TempDir()
	registry := &RepositoryRegistry{
		CacheDir: dir,
		CacheTTL: defaultCacheTTL,
		XMLPath:  filepath.Join(dir, registryXMLFile),
		url:      server.URL,
	}

	info, err := ResolveRepository("generic-repo", nil, registry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "git" {
		t.Errorf("expected Provider 'git', got %q", info.Provider)
	}
	if info.URL != "https://anongit.example.org/repo.git" {
		t.Errorf("expected generic URL, got %q", info.URL)
	}
}

func TestEndToEnd_ConfigOverrideBeatsRegistry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, endToEndXML)
	}))
	defer server.Close()

	dir := t.TempDir()
	registry := &RepositoryRegistry{
		CacheDir: dir,
		CacheTTL: defaultCacheTTL,
		XMLPath:  filepath.Join(dir, registryXMLFile),
		url:      server.URL,
	}

	configRepos := map[string]*RepositoryInfo{
		"gentoo": {
			Name:     "gentoo",
			Provider: "git",
			URL:      "/var/db/repos/gentoo",
			Branch:   "main",
		},
	}

	info, err := ResolveRepository("gentoo", configRepos, registry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "git" {
		t.Errorf("expected config override provider 'git', got %q", info.Provider)
	}
	if info.URL != "/var/db/repos/gentoo" {
		t.Errorf("expected config URL, got %q", info.URL)
	}
}

func TestEndToEnd_UnknownRepoReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, endToEndXML)
	}))
	defer server.Close()

	dir := t.TempDir()
	registry := &RepositoryRegistry{
		CacheDir: dir,
		CacheTTL: defaultCacheTTL,
		XMLPath:  filepath.Join(dir, registryXMLFile),
		url:      server.URL,
	}

	_, err := ResolveRepository("does-not-exist", nil, registry)
	if err == nil {
		t.Fatal("expected error for unknown repo")
	}
	if !errors.Is(err, ErrRepositoryNotFound) {
		t.Errorf("expected ErrRepositoryNotFound, got %v", err)
	}
}
