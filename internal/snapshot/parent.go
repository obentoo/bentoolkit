package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// parentStore persists, per (subvolume, ship), the last successfully shipped
// snapshot so an incremental archive send can reference it as its -p parent.
//
// The interface is kept small and mockable (design §5) so the archive shipper
// (T3) can be tested against a fake. The "record only after a successful ship"
// rule (R3.2) is the CALLER's contract: parentStore records whenever Record is
// called and makes no judgement about ship success.
type parentStore interface {
	// Last returns the recorded parent for (subvol, ship). ok is false (nil err)
	// when none has been recorded yet — that is the normal first-run state.
	Last(subvol, ship string) (snap Snapshot, ok bool, err error)
	// Record persists snap as the new parent for (subvol, ship), atomically.
	Record(subvol, ship string, snap Snapshot) error
}

// parentRecord is the JSON shape persisted per (subvol, ship). It mirrors the
// load-bearing fields of Snapshot needed to drive an incremental `btrfs send -p`
// (R3): the parent's ID and on-disk Path, plus enough provenance to make the
// record self-describing. It is a dedicated struct (rather than marshalling
// Snapshot directly) so the on-disk schema is explicit and stable.
type parentRecord struct {
	ID        string    `json:"id"`
	Subvolume string    `json:"subvolume"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"` // RFC3339; time.Time round-trips itself via JSON
	ParentID  string    `json:"parent_id,omitempty"`
}

// fileParentStore persists parent records as one JSON file per (subvol, ship)
// under StateDir()/parents/ (R3.1). Writes are atomic (temp + rename via
// atomicWrite) and owner-only (0o600) — the records reference backup lineage.
type fileParentStore struct{}

// newParentStore returns the default production parentStore. T3's archive
// shipper uses it as the default value of its `parents` field.
func newParentStore() *fileParentStore { return &fileParentStore{} }

// compile-time assertion that the production type satisfies the interface.
var _ parentStore = (*fileParentStore)(nil)

// parentsDir is StateDir()/parents, resolved at call time so tests that redirect
// StateDir take effect.
func parentsDir() string { return filepath.Join(StateDir(), "parents") }

// keyFilename maps a (subvol, ship) pair to a safe, deterministic,
// collision-resistant filename component.
//
// Scheme: each of subvol and ship is rendered as `<slug>-<hash8>` where
//   - <slug> replaces every byte outside [A-Za-z0-9._-] with '-', so the name is
//     filesystem-safe and free of path separators (a subvol like "/home" or
//     "/mnt/data" can never escape the parents dir);
//   - <hash8> is the first 8 hex chars of sha256(raw), which restores the
//     injectivity the slug step loses (e.g. "/a/b" and "/a-b" share a slug but
//     differ in hash), keeping distinct keys on distinct files.
//
// The two components are joined with "__" and suffixed ".json":
//
//	"<subvolSlug>-<subvolHash>__<shipSlug>-<shipHash>.json".
func keyFilename(subvol, ship string) string {
	return safeComponent(subvol) + "__" + safeComponent(ship) + ".json"
}

// keyHashLen is the number of leading hex chars of sha256(raw) appended to a
// sanitized key part to restore injectivity the sanitize step loses. 8 hex chars
// (32 bits) is ample to keep the handful of (subvol, ship) pairs on a host
// collision-free while keeping filenames short.
const keyHashLen = 8

// safeComponent renders one raw key part as "<slug>-<hash>" (see keyFilename).
func safeComponent(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return sanitize(raw) + "-" + hex.EncodeToString(sum[:])[:keyHashLen]
}

// sanitize replaces every byte outside the safe set [A-Za-z0-9._-] with '-'.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '.' || c == '_' || c == '-':
			b.WriteByte(c)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Record persists snap as the parent for (subvol, ship), atomically and
// owner-only.
func (fileParentStore) Record(subvol, ship string, snap Snapshot) error {
	rec := parentRecord{
		ID:        snap.ID,
		Subvolume: snap.Subvolume,
		Path:      snap.Path,
		CreatedAt: snap.CreatedAt,
		ParentID:  snap.ParentID,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal parent record: %w", err)
	}
	path := filepath.Join(parentsDir(), keyFilename(subvol, ship))
	if err := atomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("write parent record %s: %w", path, err)
	}
	return nil
}

// Last returns the recorded parent for (subvol, ship). A missing record is the
// normal first-run state and yields (zero, false, nil).
func (fileParentStore) Last(subvol, ship string) (Snapshot, bool, error) {
	path := filepath.Join(parentsDir(), keyFilename(subvol, ship))
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Snapshot{}, false, nil
		}
		return Snapshot{}, false, fmt.Errorf("read parent record %s: %w", path, err)
	}

	var rec parentRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return Snapshot{}, false, fmt.Errorf("decode parent record %s: %w", path, err)
	}

	snap := Snapshot{
		ID:        rec.ID,
		Subvolume: rec.Subvolume,
		Path:      rec.Path,
		CreatedAt: rec.CreatedAt,
		ParentID:  rec.ParentID,
	}
	return snap, true, nil
}
