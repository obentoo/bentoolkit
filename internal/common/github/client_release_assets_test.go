package github

// Story 011 — Phase-3 authored (RED-first) test for sub-task 2.1:
//   GetReleaseAssets(owner, repo, tag) ([]ReleaseAsset, error)
//
// Target: internal/common/github/client_test.go. RED until the executor adds the
// ReleaseAsset type and the GetReleaseAssets method. Go compile error
// "undefined: ReleaseAsset / GetReleaseAssets" is valid RED.
//
// Integration via httptest (no real network), mirroring the existing
// TestGetPackageVersions* style: assert the request path
// GET /repos/{owner}/{repo}/releases/tags/{tag}, the mapped name+download URL on
// success, and a non-nil error for tag 404, rate-limit/403, and malformed JSON.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetReleaseAssets_Success(t *testing.T) {
	const owner, repo, tag = "rustdesk", "rustdesk", "1.2.3"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/repos/" + owner + "/" + repo + "/releases/tags/" + tag
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, wantPath)
		}
		// GitHub's release payload: assets[].{name, browser_download_url}.
		payload := map[string]any{
			"tag_name": tag,
			"assets": []map[string]any{
				{"name": "vcpkg-lite.tar.gz", "browser_download_url": "https://example.com/dl/vcpkg-lite.tar.gz"},
				{"name": "checksums.txt", "browser_download_url": "https://example.com/dl/checksums.txt"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	assets, err := client.GetReleaseAssets(owner, repo, tag)
	if err != nil {
		t.Fatalf("GetReleaseAssets: unexpected error: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("expected 2 assets, got %d: %+v", len(assets), assets)
	}
	if assets[0].Name != "vcpkg-lite.tar.gz" {
		t.Errorf("asset[0].Name = %q, want vcpkg-lite.tar.gz", assets[0].Name)
	}
	if assets[0].DownloadURL != "https://example.com/dl/vcpkg-lite.tar.gz" {
		t.Errorf("asset[0].DownloadURL = %q, want the browser_download_url", assets[0].DownloadURL)
	}
}

func TestGetReleaseAssets_TagNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	assets, err := client.GetReleaseAssets("o", "r", "v9.9.9")
	if err == nil {
		t.Fatal("expected an error for a 404 tag")
	}
	if len(assets) != 0 {
		t.Errorf("expected no assets on a 404, got %d", len(assets))
	}
}

func TestGetReleaseAssets_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Reset", "1234567890")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	if _, err := client.GetReleaseAssets("o", "r", "v1.0"); err == nil {
		t.Error("expected an error on 403/rate-limit")
	}
}

func TestGetReleaseAssets_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not valid json`))
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	if _, err := client.GetReleaseAssets("o", "r", "v1.0"); err == nil {
		t.Error("expected a parse error for malformed JSON")
	}
}
