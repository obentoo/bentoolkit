// Package state persists bentoo-tray's state: the feed ETag and serial, the
// read state and source of every notice, the pause end, the backoff and the
// earliest next fetch (S072-R10).
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/fileutil"
)

// Format is the state file version this package writes.
const Format = 2

// formatV1 is the previous version, still loaded and migrated (R10.5): it has no
// next fetch time and no record source.
const formatV1 = 1

// retention is how long a record absent from both sources is kept (R10.4).
const retention = 90 * 24 * time.Hour

// ErrCorrupt marks a state file that could not be parsed or carries an unknown
// format. Load has already moved it aside and returned a fresh state; the
// caller only has to WARN. Any other Load error is a real read failure.
var ErrCorrupt = errors.New("state file is damaged")

// State is everything bentoo-tray remembers between runs.
type State struct {
	Format     int               `json:"format"`
	ETag       string            `json:"etag"`
	Serial     int64             `json:"serial"`
	Notices    map[string]Record `json:"notices"`
	PauseUntil time.Time         `json:"pause_until"`
	Failures   int               `json:"failures"`
	// NextFetch is the earliest time the next fetch may run; zero means none is
	// scheduled (R10.1, R2.14).
	NextFetch time.Time `json:"next_fetch"`
	// Established reports that R6.9's first run is over: a check has accepted
	// a feed, or read the news when no feed is configured. A state without it
	// is a first run even once saved, so a first run can persist its backoff
	// and Retry-After (R2.13, R2.14) without ending. A format 1 file loads
	// established: it was only ever written after the first run.
	Established bool `json:"established"`
	// Saved reports that a file was loaded or written; it is derived, never
	// stored.
	Saved bool `json:"-"`
}

// Record is what is kept per notice. Summary lets a held or failed
// notification be rebuilt after a 304 or a restart. Source is "feed", "news",
// or "" for a record migrated from format 1, whose source is unknown.
type Record struct {
	Title    string    `json:"title"`
	Summary  string    `json:"summary"`
	URL      string    `json:"url"`
	Severity string    `json:"severity"`
	Type     string    `json:"type"`
	Source   string    `json:"source"`
	Read     bool      `json:"read"`
	Notified bool      `json:"notified"`
	LastSeen time.Time `json:"last_seen"`
	Updated  time.Time `json:"updated"`
}

// Prune drops the records not seen for more than 90 days (R10.4).
func (s *State) Prune(now time.Time) {
	for id, r := range s.Notices {
		if now.Sub(r.LastSeen) > retention {
			delete(s.Notices, id)
		}
	}
}

// Store reads and writes one state file.
type Store struct {
	path string
}

// Open returns a Store for path; nothing is touched until Load or Save.
func Open(path string) *Store {
	return &Store{path: path}
}

// Load reads the state. A missing file is a first run: a fresh state with
// Saved and Established false. A format 1 file is loaded with every record's
// Source left "", Established set (a format 1 tray wrote no state before its
// first run was over) and Format set to 2 in memory, so the next Save writes
// format 2 (R10.5). A format 2 file without "established" is a first run. A
// file that cannot be parsed or carries a format other than 1 or 2 is moved to
// <path>.corrupt-<unix time> and a fresh state is returned together with an
// error wrapping ErrCorrupt (R10.3).
func (s *Store) Load() (State, error) {
	fresh := State{Format: Format, Notices: map[string]Record{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return fresh, nil
	}
	if err != nil {
		return fresh, fmt.Errorf("read state %s: %w", s.path, err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return fresh, s.moveAside(err)
	}
	if st.Format != Format && st.Format != formatV1 {
		return fresh, s.moveAside(fmt.Errorf("unknown format %d", st.Format))
	}
	if st.Format == formatV1 {
		st.Established = true
	}
	st.Format = Format
	if st.Notices == nil {
		st.Notices = map[string]Record{}
	}
	st.Saved = true
	return st, nil
}

// moveAside renames the damaged file out of the way and reports why.
func (s *Store) moveAside(cause error) error {
	dst := s.path + ".corrupt-" + strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.Rename(s.path, dst); err != nil {
		return fmt.Errorf("%w: %s (%w); moving it aside failed: %w", ErrCorrupt, s.path, cause, err)
	}
	return fmt.Errorf("%w: %s (%w); moved to %s", ErrCorrupt, s.path, cause, dst)
}

// Save writes st atomically with mode 0600 in a 0700 directory (R10.2), always
// in the current format. A failed save leaves the previous file intact.
func (s *Store) Save(st State) error {
	st.Format = Format
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state %s: %w", s.path, err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create state directory for %s: %w", s.path, err)
	}
	if err := fileutil.WriteFileAtomic(s.path, data, 0o600); err != nil {
		return fmt.Errorf("save state %s: %w", s.path, err)
	}
	return nil
}
