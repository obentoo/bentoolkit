package overlay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/github"
)

func TestCompare(t *testing.T) {
	// Create mock GitHub server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var entries []github.ContentEntry

		switch {
		case strings.Contains(r.URL.Path, "app-misc/hello"):
			entries = []github.ContentEntry{
				{Name: "hello-1.0.ebuild", Type: "file"},
				{Name: "hello-2.0.ebuild", Type: "file"},
			}
		case strings.Contains(r.URL.Path, "app-editors/vscode"):
			entries = []github.ContentEntry{
				{Name: "vscode-1.107.1.ebuild", Type: "file"},
				{Name: "vscode-1.108.0.ebuild", Type: "file"},
			}
		case strings.Contains(r.URL.Path, "www-client/firefox"):
			entries = []github.ContentEntry{
				{Name: "firefox-129.0.ebuild", Type: "file"},
				{Name: "firefox-130.0.ebuild", Type: "file"},
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := github.NewClient()
	client.BaseURL = server.URL

	localPackages := []PackageInfo{
		{Category: "app-misc", Package: "hello", LatestVersion: "2.0"},           // up-to-date
		{Category: "app-editors", Package: "vscode", LatestVersion: "1.107.1"},   // outdated
		{Category: "www-client", Package: "firefox", LatestVersion: "128.0"},     // outdated
		{Category: "app-misc", Package: "bentoo-only", LatestVersion: "1.0"},     // not in remote
	}

	opts := CompareOptions{
		OnlyOutdated:       true,
		IncludeNotInRemote: false,
	}

	report, err := Compare(localPackages, client, opts)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}

	// Check results
	if report.TotalPackages != 4 {
		t.Errorf("Expected 4 total packages, got %d", report.TotalPackages)
	}

	if report.OutdatedCount != 2 {
		t.Errorf("Expected 2 outdated packages, got %d", report.OutdatedCount)
	}

	if report.NotInRemoteCount != 1 {
		t.Errorf("Expected 1 not-in-remote package, got %d", report.NotInRemoteCount)
	}

	// Only outdated should be in results
	if len(report.Results) != 2 {
		t.Errorf("Expected 2 results (outdated only), got %d", len(report.Results))
	}

	// Verify outdated packages
	for _, result := range report.Results {
		if result.Status != StatusOutdated {
			t.Errorf("Expected StatusOutdated, got %v for %s", result.Status, result.Package)
		}
	}
}

func TestCompareWithAllResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var entries []github.ContentEntry

		if strings.Contains(r.URL.Path, "app-misc/hello") {
			entries = []github.ContentEntry{
				{Name: "hello-1.0.ebuild", Type: "file"},
			}
		} else {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := github.NewClient()
	client.BaseURL = server.URL

	localPackages := []PackageInfo{
		{Category: "app-misc", Package: "hello", LatestVersion: "1.0"}, // up-to-date
	}

	opts := CompareOptions{
		OnlyOutdated: false,
	}

	report, err := Compare(localPackages, client, opts)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}

	if report.UpToDateCount != 1 {
		t.Errorf("Expected 1 up-to-date package, got %d", report.UpToDateCount)
	}

	// Should include up-to-date in results when OnlyOutdated is false
	if len(report.Results) != 1 {
		t.Errorf("Expected 1 result, got %d", len(report.Results))
	}
}

func TestCompareNewerInLocal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries := []github.ContentEntry{
			{Name: "hello-1.0.ebuild", Type: "file"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := github.NewClient()
	client.BaseURL = server.URL

	localPackages := []PackageInfo{
		{Category: "app-misc", Package: "hello", LatestVersion: "2.0"}, // newer locally
	}

	opts := CompareOptions{
		OnlyOutdated: false,
	}

	report, err := Compare(localPackages, client, opts)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}

	if report.NewerCount != 1 {
		t.Errorf("Expected 1 newer package, got %d", report.NewerCount)
	}

	if len(report.Results) == 1 && report.Results[0].Status != StatusNewer {
		t.Errorf("Expected StatusNewer, got %v", report.Results[0].Status)
	}
}

func TestCompareProgressCallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries := []github.ContentEntry{{Name: "hello-1.0.ebuild", Type: "file"}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := github.NewClient()
	client.BaseURL = server.URL

	localPackages := []PackageInfo{
		{Category: "app-misc", Package: "hello", LatestVersion: "1.0"},
		{Category: "app-misc", Package: "world", LatestVersion: "1.0"},
	}

	callbackCount := 0
	opts := CompareOptions{
		ProgressCallback: func(current, total int, pkg string) {
			callbackCount++
		},
	}

	_, err := Compare(localPackages, client, opts)
	if err != nil {
		t.Fatalf("Compare failed: %v", err)
	}

	if callbackCount != 2 {
		t.Errorf("Expected 2 callback calls, got %d", callbackCount)
	}
}

func TestCompareStatus(t *testing.T) {
	tests := []struct {
		status   CompareStatus
		expected string
	}{
		{StatusUpToDate, "up-to-date"},
		{StatusOutdated, "outdated"},
		{StatusNewer, "newer"},
		{StatusNotInRemote, "not-in-remote"},
		{StatusError, "error"},
		{CompareStatus(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if tt.status.String() != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, tt.status.String())
			}
		})
	}
}

func TestFormatReport(t *testing.T) {
	report := &CompareReport{
		TotalPackages:    5,
		ComparedPackages: 4,
		OutdatedCount:    2,
		Results: []CompareResult{
			{Category: "app-editors", Package: "vscode", LocalVersion: "1.107.1", RemoteVersion: "1.108.0", Status: StatusOutdated},
			{Category: "www-client", Package: "firefox", LocalVersion: "128.0", RemoteVersion: "129.0", Status: StatusOutdated},
		},
	}

	output := FormatReport(report)

	// Check that output contains expected elements
	if !strings.Contains(output, "vscode") {
		t.Error("Output should contain vscode")
	}
	if !strings.Contains(output, "firefox") {
		t.Error("Output should contain firefox")
	}
	if !strings.Contains(output, "1.107.1") {
		t.Error("Output should contain version 1.107.1")
	}
	if !strings.Contains(output, "1.108.0") {
		t.Error("Output should contain version 1.108.0")
	}
	if !strings.Contains(output, "2") {
		t.Error("Output should contain count 2")
	}
}

func TestFormatReportEmpty(t *testing.T) {
	report := &CompareReport{
		TotalPackages: 5,
		Results:       []CompareResult{},
	}

	output := FormatReport(report)

	if !strings.Contains(output, "up-to-date") {
		t.Error("Empty report should indicate all packages are up-to-date")
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"},
		{"hi", 2, "hi"},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc"},       // maxLen <= 3 returns truncated without ellipsis
		{"abcdef", 5, "abcd…"},   // maxLen > 3 uses ellipsis
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncateString(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

