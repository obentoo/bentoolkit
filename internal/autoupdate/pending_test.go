package autoupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestPendingListCompleteness tests Property 8: Pending List Completeness
// **Feature: ebuild-autoupdate, Property 8: Pending List Completeness**
// **Validates: Requirements 5.2, 5.3, 5.4**
func TestPendingListCompleteness(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: List returns all updates with required fields
	properties.Property("List returns all updates with package, versions, and status", prop.ForAll(
		func(updates []testPendingUpdate) bool {
			tmpDir := t.TempDir()

			fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
			pending, err := NewPendingList(tmpDir, WithPendingNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create pending list: %v", err)
				return false
			}

			// Add all updates
			for _, u := range updates {
				update := PendingUpdate{
					Package:        u.Package,
					CurrentVersion: u.CurrentVersion,
					NewVersion:     u.NewVersion,
					Status:         StatusPending,
				}
				if err := pending.Add(update); err != nil {
					t.Logf("Failed to add update: %v", err)
					return false
				}
			}

			// Get list
			list := pending.List()

			// Verify count matches unique packages
			uniquePkgs := make(map[string]bool)
			for _, u := range updates {
				uniquePkgs[u.Package] = true
			}
			if len(list) != len(uniquePkgs) {
				t.Logf("Expected %d updates, got %d", len(uniquePkgs), len(list))
				return false
			}

			// Verify each update has required fields
			for _, update := range list {
				if update.Package == "" {
					t.Log("Update missing package name")
					return false
				}
				if update.CurrentVersion == "" {
					t.Log("Update missing current version")
					return false
				}
				if update.NewVersion == "" {
					t.Log("Update missing new version")
					return false
				}
				if update.Status == "" {
					t.Log("Update missing status")
					return false
				}
				if !IsValidStatus(update.Status) {
					t.Logf("Update has invalid status: %s", update.Status)
					return false
				}
			}

			return true
		},
		genPendingUpdateSlice(),
	))

	// Property: Get returns update with all fields
	properties.Property("Get returns update with all required fields", prop.ForAll(
		func(pkg, currentVer, newVer string) bool {
			tmpDir := t.TempDir()

			fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
			pending, err := NewPendingList(tmpDir, WithPendingNowFunc(func() time.Time { return fixedNow }))
			if err != nil {
				t.Logf("Failed to create pending list: %v", err)
				return false
			}

			// Add update
			update := PendingUpdate{
				Package:        pkg,
				CurrentVersion: currentVer,
				NewVersion:     newVer,
				Status:         StatusPending,
			}
			if err := pending.Add(update); err != nil {
				t.Logf("Failed to add update: %v", err)
				return false
			}

			// Get update
			retrieved, found := pending.Get(pkg)
			if !found {
				t.Log("Update not found after Add")
				return false
			}

			// Verify all fields
			if retrieved.Package != pkg {
				t.Logf("Package mismatch: expected %q, got %q", pkg, retrieved.Package)
				return false
			}
			if retrieved.CurrentVersion != currentVer {
				t.Logf("CurrentVersion mismatch: expected %q, got %q", currentVer, retrieved.CurrentVersion)
				return false
			}
			if retrieved.NewVersion != newVer {
				t.Logf("NewVersion mismatch: expected %q, got %q", newVer, retrieved.NewVersion)
				return false
			}
			if retrieved.Status != StatusPending {
				t.Logf("Status mismatch: expected %q, got %q", StatusPending, retrieved.Status)
				return false
			}

			return true
		},
		genPackageName(),
		genVersion(),
		genVersion(),
	))

	properties.TestingRun(t)
}

// testPendingUpdate is a helper struct for generating test data
type testPendingUpdate struct {
	Package        string
	CurrentVersion string
	NewVersion     string
}

// genPendingUpdateSlice generates a slice of test pending updates
func genPendingUpdateSlice() gopter.Gen {
	return gen.SliceOfN(5, gopter.CombineGens(
		genPackageName(),
		genVersion(),
		genVersion(),
	).Map(func(values []interface{}) testPendingUpdate {
		return testPendingUpdate{
			Package:        values[0].(string),
			CurrentVersion: values[1].(string),
			NewVersion:     values[2].(string),
		}
	}))
}

// =============================================================================
// Unit Tests
// =============================================================================

// TestNewPendingListCreatesDirectory tests that NewPendingList creates the config directory
func TestNewPendingListCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "subdir", "autoupdate")

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify directory was created
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatalf("Config directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("Expected directory, got file")
	}

	// Verify pending list is empty
	if pending.Len() != 0 {
		t.Errorf("Expected empty pending list, got %d entries", pending.Len())
	}
}

// TestNewPendingListLoadsExisting tests that NewPendingList loads existing pending file
func TestNewPendingListLoadsExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Create existing pending file
	pendingData := `{
		"updates": {
			"net-misc/test-pkg": {
				"package": "net-misc/test-pkg",
				"current_version": "1.0.0",
				"new_version": "1.2.3",
				"status": "pending",
				"detected_at": "2026-01-22T12:00:00Z"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(tmpDir, "pending.json"), []byte(pendingData), 0644); err != nil {
		t.Fatalf("Failed to write pending file: %v", err)
	}

	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify entry was loaded
	update, found := pending.Get("net-misc/test-pkg")
	if !found {
		t.Fatal("Expected entry to exist")
	}
	if update.CurrentVersion != "1.0.0" {
		t.Errorf("Expected current version '1.0.0', got %q", update.CurrentVersion)
	}
	if update.NewVersion != "1.2.3" {
		t.Errorf("Expected new version '1.2.3', got %q", update.NewVersion)
	}
	if update.Status != StatusPending {
		t.Errorf("Expected status 'pending', got %q", update.Status)
	}
}

// TestNewPendingListHandlesCorruptedFile tests that NewPendingList handles corrupted pending file
func TestNewPendingListHandlesCorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create corrupted pending file
	if err := os.WriteFile(filepath.Join(tmpDir, "pending.json"), []byte("{invalid json"), 0644); err != nil {
		t.Fatalf("Failed to write pending file: %v", err)
	}

	// Should not return error, just start with empty list
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify pending list is empty
	if pending.Len() != 0 {
		t.Errorf("Expected empty pending list after corruption, got %d entries", pending.Len())
	}
}

// TestPendingListAdd tests Add operation
func TestPendingListAdd(t *testing.T) {
	tmpDir := t.TempDir()

	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
	pending, err := NewPendingList(tmpDir, WithPendingNowFunc(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	update := PendingUpdate{
		Package:        "test/pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	}

	if err := pending.Add(update); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	// Verify entry exists
	retrieved, found := pending.Get("test/pkg")
	if !found {
		t.Fatal("Expected entry to exist after Add")
	}
	if retrieved.Package != "test/pkg" {
		t.Errorf("Expected package 'test/pkg', got %q", retrieved.Package)
	}
	if retrieved.CurrentVersion != "1.0.0" {
		t.Errorf("Expected current version '1.0.0', got %q", retrieved.CurrentVersion)
	}
	if retrieved.NewVersion != "2.0.0" {
		t.Errorf("Expected new version '2.0.0', got %q", retrieved.NewVersion)
	}
	if retrieved.Status != StatusPending {
		t.Errorf("Expected status 'pending', got %q", retrieved.Status)
	}
	if !retrieved.DetectedAt.Equal(fixedNow) {
		t.Errorf("Expected detected_at %v, got %v", fixedNow, retrieved.DetectedAt)
	}
}

// TestPendingListAddSetsDetectedAt tests that Add sets DetectedAt if not provided
func TestPendingListAddSetsDetectedAt(t *testing.T) {
	tmpDir := t.TempDir()

	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
	pending, err := NewPendingList(tmpDir, WithPendingNowFunc(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	update := PendingUpdate{
		Package:        "test/pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
		// DetectedAt not set
	}

	if err := pending.Add(update); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	retrieved, _ := pending.Get("test/pkg")
	if !retrieved.DetectedAt.Equal(fixedNow) {
		t.Errorf("Expected DetectedAt to be set to %v, got %v", fixedNow, retrieved.DetectedAt)
	}
}

// TestPendingListAddPreservesDetectedAt tests that Add preserves existing DetectedAt
func TestPendingListAddPreservesDetectedAt(t *testing.T) {
	tmpDir := t.TempDir()

	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
	customTime := time.Date(2026, 1, 20, 10, 0, 0, 0, time.UTC)

	pending, err := NewPendingList(tmpDir, WithPendingNowFunc(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	update := PendingUpdate{
		Package:        "test/pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
		DetectedAt:     customTime,
	}

	if err := pending.Add(update); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	retrieved, _ := pending.Get("test/pkg")
	if !retrieved.DetectedAt.Equal(customTime) {
		t.Errorf("Expected DetectedAt to be preserved as %v, got %v", customTime, retrieved.DetectedAt)
	}
}

// TestPendingListAddPersists tests that Add persists to disk
func TestPendingListAddPersists(t *testing.T) {
	tmpDir := t.TempDir()

	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	update := PendingUpdate{
		Package:        "test/pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	}

	if err := pending.Add(update); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	// Read pending file directly
	data, err := os.ReadFile(filepath.Join(tmpDir, "pending.json"))
	if err != nil {
		t.Fatalf("Failed to read pending file: %v", err)
	}

	var pf pendingFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("Failed to parse pending file: %v", err)
	}

	entry, exists := pf.Updates["test/pkg"]
	if !exists {
		t.Fatal("Entry not found in pending file")
	}
	if entry.NewVersion != "2.0.0" {
		t.Errorf("Expected new version '2.0.0' in file, got %q", entry.NewVersion)
	}
}

// TestPendingListGetMiss tests Get returns false for non-existent entry
func TestPendingListGetMiss(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	_, found := pending.Get("non-existent/pkg")
	if found {
		t.Error("Expected miss for non-existent entry")
	}
}

// TestPendingListSetStatus tests SetStatus operation
func TestPendingListSetStatus(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add an update
	update := PendingUpdate{
		Package:        "test/pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	}
	if err := pending.Add(update); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	// Update status to validated
	if err := pending.SetStatus("test/pkg", StatusValidated, ""); err != nil {
		t.Fatalf("Failed to set status: %v", err)
	}

	retrieved, _ := pending.Get("test/pkg")
	if retrieved.Status != StatusValidated {
		t.Errorf("Expected status 'validated', got %q", retrieved.Status)
	}
}

// TestPendingListSetStatusFailed tests SetStatus with failed status and error message
func TestPendingListSetStatusFailed(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add an update
	update := PendingUpdate{
		Package:        "test/pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	}
	if err := pending.Add(update); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	// Update status to failed with error message
	errMsg := "manifest command failed"
	if err := pending.SetStatus("test/pkg", StatusFailed, errMsg); err != nil {
		t.Fatalf("Failed to set status: %v", err)
	}

	retrieved, _ := pending.Get("test/pkg")
	if retrieved.Status != StatusFailed {
		t.Errorf("Expected status 'failed', got %q", retrieved.Status)
	}
	if retrieved.Error != errMsg {
		t.Errorf("Expected error %q, got %q", errMsg, retrieved.Error)
	}
}

// TestPendingListSetStatusClearsError tests that SetStatus clears error when not failed
func TestPendingListSetStatusClearsError(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add an update with error
	update := PendingUpdate{
		Package:        "test/pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusFailed,
		Error:          "previous error",
	}
	if err := pending.Add(update); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	// Update status to validated (should clear error)
	if err := pending.SetStatus("test/pkg", StatusValidated, ""); err != nil {
		t.Fatalf("Failed to set status: %v", err)
	}

	retrieved, _ := pending.Get("test/pkg")
	if retrieved.Error != "" {
		t.Errorf("Expected error to be cleared, got %q", retrieved.Error)
	}
}

// TestPendingListSetStatusNotFound tests SetStatus returns error for non-existent package
func TestPendingListSetStatusNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	err = pending.SetStatus("non-existent/pkg", StatusValidated, "")
	if err != ErrPackageNotInPending {
		t.Errorf("Expected ErrPackageNotInPending, got: %v", err)
	}
}

// TestPendingListSetStatusInvalid tests SetStatus returns error for invalid status
func TestPendingListSetStatusInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add an update
	update := PendingUpdate{
		Package:        "test/pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	}
	if err := pending.Add(update); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	err = pending.SetStatus("test/pkg", UpdateStatus("invalid"), "")
	if err == nil {
		t.Error("Expected error for invalid status")
	}
}

// TestPendingListList tests List operation
func TestPendingListList(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add multiple updates
	pending.Add(PendingUpdate{Package: "test/pkg1", CurrentVersion: "1.0.0", NewVersion: "1.1.0", Status: StatusPending})
	pending.Add(PendingUpdate{Package: "test/pkg2", CurrentVersion: "2.0.0", NewVersion: "2.1.0", Status: StatusValidated})
	pending.Add(PendingUpdate{Package: "test/pkg3", CurrentVersion: "3.0.0", NewVersion: "3.1.0", Status: StatusFailed})

	list := pending.List()
	if len(list) != 3 {
		t.Errorf("Expected 3 updates, got %d", len(list))
	}
}

// TestPendingListListByStatus tests ListByStatus operation
func TestPendingListListByStatus(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add multiple updates with different statuses
	pending.Add(PendingUpdate{Package: "test/pkg1", CurrentVersion: "1.0.0", NewVersion: "1.1.0", Status: StatusPending})
	pending.Add(PendingUpdate{Package: "test/pkg2", CurrentVersion: "2.0.0", NewVersion: "2.1.0", Status: StatusPending})
	pending.Add(PendingUpdate{Package: "test/pkg3", CurrentVersion: "3.0.0", NewVersion: "3.1.0", Status: StatusValidated})

	pendingList := pending.ListByStatus(StatusPending)
	if len(pendingList) != 2 {
		t.Errorf("Expected 2 pending updates, got %d", len(pendingList))
	}

	validatedList := pending.ListByStatus(StatusValidated)
	if len(validatedList) != 1 {
		t.Errorf("Expected 1 validated update, got %d", len(validatedList))
	}
}

// TestPendingListDelete tests Delete operation
func TestPendingListDelete(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add an update
	pending.Add(PendingUpdate{Package: "test/pkg", CurrentVersion: "1.0.0", NewVersion: "2.0.0", Status: StatusPending})

	// Delete it
	if err := pending.Delete("test/pkg"); err != nil {
		t.Fatalf("Failed to delete: %v", err)
	}

	_, found := pending.Get("test/pkg")
	if found {
		t.Error("Expected entry to be deleted")
	}
}

// TestPendingListClear tests Clear operation
func TestPendingListClear(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add multiple updates
	pending.Add(PendingUpdate{Package: "test/pkg1", CurrentVersion: "1.0.0", NewVersion: "1.1.0", Status: StatusPending})
	pending.Add(PendingUpdate{Package: "test/pkg2", CurrentVersion: "2.0.0", NewVersion: "2.1.0", Status: StatusPending})

	if pending.Len() != 2 {
		t.Errorf("Expected 2 entries, got %d", pending.Len())
	}

	if err := pending.Clear(); err != nil {
		t.Fatalf("Failed to clear: %v", err)
	}

	if pending.Len() != 0 {
		t.Errorf("Expected 0 entries after Clear, got %d", pending.Len())
	}
}

// TestPendingListHas tests Has operation
func TestPendingListHas(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add an update
	pending.Add(PendingUpdate{Package: "test/pkg", CurrentVersion: "1.0.0", NewVersion: "2.0.0", Status: StatusPending})

	if !pending.Has("test/pkg") {
		t.Error("Expected Has to return true for existing package")
	}

	if pending.Has("non-existent/pkg") {
		t.Error("Expected Has to return false for non-existent package")
	}
}

// TestPendingListAtomicWrite tests that pending list writes are atomic
func TestPendingListAtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Add an entry
	if err := pending.Add(PendingUpdate{Package: "test/pkg", CurrentVersion: "1.0.0", NewVersion: "2.0.0", Status: StatusPending}); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	// Verify no temp file remains
	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to read dir: %v", err)
	}

	for _, f := range files {
		if f.Name() == "pending.json.tmp" {
			t.Error("Temp file should not remain after successful write")
		}
	}
}

// TestPendingListPersistence tests that pending list persists across instances
func TestPendingListPersistence(t *testing.T) {
	tmpDir := t.TempDir()

	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)

	// Create first instance and add update
	pending1, err := NewPendingList(tmpDir, WithPendingNowFunc(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	update := PendingUpdate{
		Package:        "test/pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	}
	if err := pending1.Add(update); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	// Create second instance and verify data persisted
	pending2, err := NewPendingList(tmpDir, WithPendingNowFunc(func() time.Time { return fixedNow }))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	retrieved, found := pending2.Get("test/pkg")
	if !found {
		t.Fatal("Expected entry to persist across instances")
	}
	if retrieved.NewVersion != "2.0.0" {
		t.Errorf("Expected new version '2.0.0', got %q", retrieved.NewVersion)
	}
}

// TestValidStatuses tests ValidStatuses function
func TestValidStatuses(t *testing.T) {
	statuses := ValidStatuses()
	if len(statuses) != 4 {
		t.Errorf("Expected 4 valid statuses, got %d", len(statuses))
	}

	expected := []UpdateStatus{StatusPending, StatusValidated, StatusFailed, StatusApplied}
	for _, exp := range expected {
		found := false
		for _, s := range statuses {
			if s == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected status %q in ValidStatuses", exp)
		}
	}
}

// TestIsValidStatus tests IsValidStatus function
func TestIsValidStatus(t *testing.T) {
	tests := []struct {
		status UpdateStatus
		valid  bool
	}{
		{StatusPending, true},
		{StatusValidated, true},
		{StatusFailed, true},
		{StatusApplied, true},
		{UpdateStatus("invalid"), false},
		{UpdateStatus(""), false},
	}

	for _, tt := range tests {
		result := IsValidStatus(tt.status)
		if result != tt.valid {
			t.Errorf("IsValidStatus(%q) = %v, expected %v", tt.status, result, tt.valid)
		}
	}
}

// TestPendingListAddInvalidStatus tests that Add defaults invalid status to pending
func TestPendingListAddInvalidStatus(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	update := PendingUpdate{
		Package:        "test/pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         UpdateStatus("invalid"),
	}

	if err := pending.Add(update); err != nil {
		t.Fatalf("Failed to add: %v", err)
	}

	retrieved, _ := pending.Get("test/pkg")
	if retrieved.Status != StatusPending {
		t.Errorf("Expected status to default to 'pending', got %q", retrieved.Status)
	}
}
