// Package autoupdate provides pending updates management for ebuild version updates.
package autoupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Error variables for pending list errors
var (
	// ErrPendingCorrupted is returned when the pending file cannot be parsed
	ErrPendingCorrupted = errors.New("pending file is corrupted")
	// ErrPackageNotInPending is returned when a package is not found in pending updates
	ErrPackageNotInPending = errors.New("package not found in pending updates")
	// ErrInvalidStatusTransition is returned when an invalid status transition is attempted
	ErrInvalidStatusTransition = errors.New("invalid status transition")
)

// UpdateStatus represents the status of a pending update.
type UpdateStatus string

// Update status constants
const (
	// StatusPending indicates the update has been detected but not yet applied
	StatusPending UpdateStatus = "pending"
	// StatusValidated indicates the update was applied and manifest succeeded
	StatusValidated UpdateStatus = "validated"
	// StatusFailed indicates the update application failed
	StatusFailed UpdateStatus = "failed"
	// StatusApplied indicates the update has been fully applied
	StatusApplied UpdateStatus = "applied"
)

// ValidStatuses returns all valid update statuses
func ValidStatuses() []UpdateStatus {
	return []UpdateStatus{StatusPending, StatusValidated, StatusFailed, StatusApplied}
}

// IsValidStatus checks if a status is valid
func IsValidStatus(s UpdateStatus) bool {
	for _, valid := range ValidStatuses() {
		if s == valid {
			return true
		}
	}
	return false
}

// PendingUpdate represents a detected update awaiting application.
type PendingUpdate struct {
	// Package is the full package name (category/package)
	Package string `json:"package"`
	// CurrentVersion is the version currently in the overlay
	CurrentVersion string `json:"current_version"`
	// NewVersion is the upstream version detected
	NewVersion string `json:"new_version"`
	// Status is the current status of this update
	Status UpdateStatus `json:"status"`
	// DetectedAt is when this update was first detected
	DetectedAt time.Time `json:"detected_at"`
	// Error contains error message if status is failed
	Error string `json:"error,omitempty"`
}


// pendingFile represents the JSON structure stored on disk
type pendingFile struct {
	Updates map[string]PendingUpdate `json:"updates"`
}

// PendingList manages the list of pending updates.
// It persists updates to disk and supports concurrent access.
type PendingList struct {
	// Updates holds all pending updates, keyed by package name
	Updates map[string]PendingUpdate `json:"updates"`
	// path is the file path where pending list is persisted
	path string
	// mu protects concurrent access to Updates
	mu sync.RWMutex
	// nowFunc allows injecting time for testing
	nowFunc func() time.Time
}

// PendingListOption is a functional option for configuring PendingList
type PendingListOption func(*PendingList)

// WithPendingNowFunc sets a custom time function for testing
func WithPendingNowFunc(fn func() time.Time) PendingListOption {
	return func(p *PendingList) {
		p.nowFunc = fn
	}
}

// NewPendingList creates or loads a pending list from disk.
// If the pending file exists, it loads existing entries.
// If the pending file doesn't exist or is corrupted, it creates a new empty list.
// The configDir should be the bentoo config directory (e.g., ~/.config/bentoo/autoupdate).
func NewPendingList(configDir string, opts ...PendingListOption) (*PendingList, error) {
	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create pending directory: %w", err)
	}

	pendingPath := filepath.Join(configDir, "pending.json")

	pending := &PendingList{
		Updates: make(map[string]PendingUpdate),
		path:    pendingPath,
		nowFunc: time.Now,
	}

	// Apply options
	for _, opt := range opts {
		opt(pending)
	}

	// Try to load existing pending list
	if err := pending.load(); err != nil {
		// If file doesn't exist, that's fine - start with empty list
		if !os.IsNotExist(err) {
			// Log corruption but continue with empty list
			// The corrupted file will be overwritten on next Save
			pending.Updates = make(map[string]PendingUpdate)
		}
	}

	return pending, nil
}

// load reads the pending list from disk
func (p *PendingList) load() error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return err
	}

	var pf pendingFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return fmt.Errorf("%w: %v", ErrPendingCorrupted, err)
	}

	if pf.Updates != nil {
		p.Updates = pf.Updates
	}

	return nil
}

// Add adds or updates a pending update.
// If the package already exists, it updates the entry.
// It automatically saves the pending list to disk after adding.
func (p *PendingList) Add(update PendingUpdate) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Set detected time if not already set
	if update.DetectedAt.IsZero() {
		update.DetectedAt = p.nowFunc()
	}

	// Validate status
	if !IsValidStatus(update.Status) {
		update.Status = StatusPending
	}

	p.Updates[update.Package] = update
	return p.saveUnsafe()
}

// Get retrieves a pending update by package name.
// Returns the update and true if found, zero value and false otherwise.
func (p *PendingList) Get(pkg string) (*PendingUpdate, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	update, exists := p.Updates[pkg]
	if !exists {
		return nil, false
	}
	return &update, true
}

// SetStatus updates the status of a pending update.
// The errMsg parameter is used to set the Error field when status is StatusFailed.
// Returns ErrPackageNotInPending if the package is not found.
func (p *PendingList) SetStatus(pkg string, status UpdateStatus, errMsg string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	update, exists := p.Updates[pkg]
	if !exists {
		return ErrPackageNotInPending
	}

	// Validate status
	if !IsValidStatus(status) {
		return fmt.Errorf("%w: invalid status %q", ErrInvalidStatusTransition, status)
	}

	update.Status = status
	if status == StatusFailed {
		update.Error = errMsg
	} else {
		update.Error = ""
	}

	p.Updates[pkg] = update
	return p.saveUnsafe()
}

// List returns all pending updates as a slice.
// The returned slice contains copies of the updates.
func (p *PendingList) List() []PendingUpdate {
	p.mu.RLock()
	defer p.mu.RUnlock()

	updates := make([]PendingUpdate, 0, len(p.Updates))
	for _, update := range p.Updates {
		updates = append(updates, update)
	}
	return updates
}

// ListByStatus returns all pending updates with the specified status.
func (p *PendingList) ListByStatus(status UpdateStatus) []PendingUpdate {
	p.mu.RLock()
	defer p.mu.RUnlock()

	updates := make([]PendingUpdate, 0)
	for _, update := range p.Updates {
		if update.Status == status {
			updates = append(updates, update)
		}
	}
	return updates
}

// Save persists the pending list to disk.
// This is thread-safe and can be called concurrently.
func (p *PendingList) Save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saveUnsafe()
}

// saveUnsafe persists the pending list to disk without locking.
// Caller must hold the write lock.
func (p *PendingList) saveUnsafe() error {
	pf := pendingFile{
		Updates: p.Updates,
	}

	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal pending list: %w", err)
	}

	// Write to temp file first, then rename for atomicity
	tmpPath := p.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write pending file: %w", err)
	}

	if err := os.Rename(tmpPath, p.path); err != nil {
		// Clean up temp file on rename failure
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename pending file: %w", err)
	}

	return nil
}

// Delete removes a package from the pending list.
// It automatically saves the pending list to disk after deletion.
func (p *PendingList) Delete(pkg string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.Updates, pkg)
	return p.saveUnsafe()
}

// Clear removes all entries from the pending list.
// It automatically saves the pending list to disk after clearing.
func (p *PendingList) Clear() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.Updates = make(map[string]PendingUpdate)
	return p.saveUnsafe()
}

// Len returns the number of entries in the pending list.
func (p *PendingList) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.Updates)
}

// Has checks if a package exists in the pending list.
func (p *PendingList) Has(pkg string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, exists := p.Updates[pkg]
	return exists
}
