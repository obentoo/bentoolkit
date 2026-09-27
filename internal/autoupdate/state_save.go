package autoupdate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/obentoo/bentoolkit/internal/common/filelock"
)

// stateLockName is the lock every save of cache.json, pending.json and
// analysis_cache.json holds in their shared config directory, across the
// re-read, the merge and the write. Its name matches the scratch exclusion the
// overlay staging applies and is never swept as a stale temporary.
const stateLockName = ".state.bentoo-lock"

// stateDirMutexes serializes the saves of one process per config directory
// before they reach the file lock, so concurrent goroutines queue on a mutex
// instead of polling the lock file.
var (
	stateDirMutexesMu sync.Mutex
	stateDirMutexes   = map[string]*sync.Mutex{}
)

// stateDirMutex returns the in-process mutex for dir.
func stateDirMutex(dir string) *sync.Mutex {
	stateDirMutexesMu.Lock()
	defer stateDirMutexesMu.Unlock()
	m, ok := stateDirMutexes[dir]
	if !ok {
		m = &sync.Mutex{}
		stateDirMutexes[dir] = m
	}
	return m
}

// withStateLock runs fn while holding the in-process mutex and the file lock
// of path's config directory. A lock that cannot be taken is returned wrapped
// naming path; fn does not run and the file on disk is left unchanged.
func withStateLock(path string, fn func() error) error {
	dir := filepath.Dir(path)
	mu := stateDirMutex(dir)
	mu.Lock()
	defer mu.Unlock()

	lock, err := filelock.Acquire(filepath.Join(dir, stateLockName), "bentoo autoupdate state")
	if err != nil {
		return fmt.Errorf("saving %s: %w", path, err)
	}
	defer lock.Release()
	return fn()
}

// snapshotState records, per key, the JSON of each value — the baseline a later
// save compares memory against. Comparing JSON rather than Go values keeps a
// time.Time's monotonic reading, which a value re-read from disk never has,
// from making an untouched entry look changed.
func snapshotState[V any](m map[string]V) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("encoding entry %q: %w", k, err)
		}
		out[k] = raw
	}
	return out, nil
}

// mergeState merges three views of one keyed map. A key whose in-memory value
// differs from the baseline — added, changed or deleted since this instance
// last loaded or saved — takes the in-memory value (absence meaning delete);
// every other key takes the value on disk, so entries another process wrote
// meanwhile survive, and entries it deleted stay deleted.
func mergeState[V any](memory map[string]V, baseline map[string]json.RawMessage, disk map[string]V) (map[string]V, error) {
	merged := make(map[string]V, len(disk)+len(memory))
	keys := make(map[string]struct{}, len(memory)+len(baseline)+len(disk))
	for k := range memory {
		keys[k] = struct{}{}
	}
	for k := range baseline {
		keys[k] = struct{}{}
	}
	for k := range disk {
		keys[k] = struct{}{}
	}

	for k := range keys {
		memVal, inMem := memory[k]
		baseRaw, inBase := baseline[k]
		changed := inMem != inBase
		if inMem && inBase {
			memRaw, err := json.Marshal(memVal)
			if err != nil {
				return nil, fmt.Errorf("encoding entry %q: %w", k, err)
			}
			changed = !bytes.Equal(memRaw, baseRaw)
		}

		if changed {
			if inMem {
				merged[k] = memVal
			}
			continue
		}
		if diskVal, ok := disk[k]; ok {
			merged[k] = diskVal
		}
	}
	return merged, nil
}
